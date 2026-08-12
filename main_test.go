package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLoadServerConfigRequiresAdminValues(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("OLLAMA_URL", "")
	t.Setenv("OLLAMA_MODEL", "")
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("TRANSLATION_TOKEN", "")

	cfg, err := loadServerConfig()
	if err == nil {
		t.Fatal("expected missing admin values to fail")
	}
	for _, name := range []string{"ADMIN_USERNAME", "ADMIN_PASSWORD", "SESSION_SECRET", "TRANSLATION_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("missing %s in error: %v", name, err)
		}
	}
	if cfg.Addr != "0.0.0.0:9917" {
		t.Fatalf("expected default docker-safe address, got %q", cfg.Addr)
	}
	if cfg.OllamaURL != "http://127.0.0.1:11434" {
		t.Fatalf("expected default ollama URL, got %q", cfg.OllamaURL)
	}
	if cfg.OllamaModel != "translategemma:4b" {
		t.Fatalf("expected default ollama model, got %q", cfg.OllamaModel)
	}
}

func TestHandleWebhookRequiresTranslationToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &appServer{
		cfg: serverConfig{TranslationToken: "secret-token"},
	}

	for _, path := range []string{"/", "/?translation-token=wrong-token"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"group_id":"group","text":"$spanish Hello"}`))

			server.handleWebhook(c)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected unauthorized response, got %d", w.Code)
			}
		})
	}
}

func TestHomePageDocumentsTranslationCommands(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	server := &appServer{}
	server.registerRoutes(router)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected home page status 200, got %d", w.Code)
	}
	for _, content := range []string{"Translation Station", "$spanish", "$english", "🤔"} {
		if !strings.Contains(w.Body.String(), content) {
			t.Fatalf("home page is missing %q", content)
		}
	}
}

func TestEnsureAuthDBFilePrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "main.sqlite")

	if err := ensureAuthDBFile(path); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected 0600 database permissions, got %o", got)
	}
}

func TestInitAuthDBCreatesTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "main.sqlite")
	db, err := initAuthDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{"groupme_bots", "webhook_request_logs"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != table {
			t.Fatalf("expected %s table, got %q", table, name)
		}
	}
}

func TestWebhookRequestLogKeepsLatestTwenty(t *testing.T) {
	db, err := initAuthDB(filepath.Join(t.TempDir(), "data", "main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	server := &appServer{db: db}
	for i := 1; i <= 25; i++ {
		if err := server.recordWebhookRequest("127.0.0.1", "invalid", http.StatusUnauthorized, "", "attempt"); err != nil {
			t.Fatal(err)
		}
	}

	requests, err := server.listWebhookRequests()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != requestLogLimit {
		t.Fatalf("expected %d requests, got %d", requestLogLimit, len(requests))
	}
	if requests[0].ID != 25 || requests[len(requests)-1].ID != 6 {
		t.Fatalf("expected request IDs 25 through 6, got %d through %d", requests[0].ID, requests[len(requests)-1].ID)
	}
}

func TestHandleWebhookRecordsInvalidTokenWithoutStoringIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := initAuthDB(filepath.Join(t.TempDir(), "data", "main.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	server := &appServer{cfg: serverConfig{TranslationToken: "secret-token"}, db: db}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/?translation-token=wrong-secret", bytes.NewBufferString(`{"group_id":"group"}`))
	server.handleWebhook(c)

	requests, err := server.listWebhookRequests()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].TokenStatus != "invalid" || requests[0].StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected request log: %+v", requests)
	}
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webhook_request_logs WHERE result LIKE '%wrong-secret%'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("request log stored the supplied token")
	}
}

func TestParseTranslationCommand(t *testing.T) {
	target, text, ok := parseTranslationCommand("$spanish Hello")
	if !ok || target != "Spanish" || text != "Hello" {
		t.Fatalf("unexpected spanish parse: target=%q text=%q ok=%v", target, text, ok)
	}

	target, text, ok = parseTranslationCommand("$English Hola")
	if !ok || target != "English" || text != "Hola" {
		t.Fatalf("unexpected english parse: target=%q text=%q ok=%v", target, text, ok)
	}

	if _, _, ok := parseTranslationCommand("hello"); ok {
		t.Fatal("expected unprefixed message to be ignored")
	}
}

func TestTranslateUsesOllamaGenerate(t *testing.T) {
	var request struct {
		Model   string         `json:"model"`
		Prompt  string         `json:"prompt"`
		Stream  bool           `json:"stream"`
		Options map[string]any `json:"options"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("expected /api/generate, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"Hola mundo"}`))
	}))
	defer server.Close()

	translated, err := translate(server.URL, "translategemma:4b", "Spanish", "Hello world")
	if err != nil {
		t.Fatal(err)
	}

	if translated != "Hola mundo" {
		t.Fatalf("expected translated text, got %q", translated)
	}
	if request.Model != "translategemma:4b" {
		t.Fatalf("expected translategemma:4b model, got %q", request.Model)
	}
	if request.Stream {
		t.Fatal("expected non-streaming request")
	}
	if !strings.Contains(request.Prompt, "Translate the following text to Spanish") || !strings.Contains(request.Prompt, "Hello world") {
		t.Fatalf("prompt does not include target language and source text: %q", request.Prompt)
	}
}

func TestSchemaJSONCoversConfigVariables(t *testing.T) {
	raw, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Variables []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for _, variable := range schema.Variables {
		found[variable.Name] = true
	}

	for _, name := range []string{"ADDR", "OLLAMA_URL", "OLLAMA_MODEL", "ADMIN_USERNAME", "ADMIN_PASSWORD", "SESSION_SECRET", "TRANSLATION_TOKEN"} {
		if !found[name] {
			t.Fatalf("schema.json is missing %s", name)
		}
	}
}
