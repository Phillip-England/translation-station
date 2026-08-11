package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
)

const (
	configPath = "config/.env"
	dbPath     = "data/main.sqlite"

	sessionCookieName = "uppr_session"
	sessionTTL        = 12 * time.Hour

	maxLoginFailures = 5
	loginWindow      = 24 * time.Hour
	requestLogLimit  = 20
)

type serverConfig struct {
	Addr             string
	OllamaURL        string
	OllamaModel      string
	AdminUsername    string
	AdminPassword    string
	SessionSecret    []byte
	TranslationToken string
}

type appServer struct {
	cfg      serverConfig
	db       *sql.DB
	sessions *sessionStore
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

type Message struct {
	Text    string `json:"text"`
	GroupID string `json:"group_id"`
}

type TranslationResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

type GroupmeRequestBody struct {
	Text  string `json:"text"`
	BotID string `json:"bot_id"`
}

type groupmeBot struct {
	ID        int64
	Name      string
	GroupID   string
	BotID     string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type webhookRequestLog struct {
	ID          int64
	RequestedAt time.Time
	IP          string
	TokenStatus string
	StatusCode  int
	GroupID     string
	Result      string
}

func main() {
	cfg, err := loadServerConfig()
	if err != nil {
		log.Fatal(err)
	}

	db, err := initAuthDB(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	server := &appServer{
		cfg:      cfg,
		db:       db,
		sessions: newSessionStore(),
	}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), CORSMiddleware())

	server.registerRoutes(r)

	log.Printf("listening on %s", cfg.Addr)
	if err := r.Run(cfg.Addr); err != nil {
		log.Fatal(err)
	}
}

func loadServerConfig() (serverConfig, error) {
	_ = godotenv.Load(configPath)

	cfg := serverConfig{
		Addr:             strings.TrimSpace(os.Getenv("ADDR")),
		OllamaURL:        strings.TrimSpace(os.Getenv("OLLAMA_URL")),
		OllamaModel:      strings.TrimSpace(os.Getenv("OLLAMA_MODEL")),
		AdminUsername:    strings.TrimSpace(os.Getenv("ADMIN_USERNAME")),
		AdminPassword:    os.Getenv("ADMIN_PASSWORD"),
		SessionSecret:    []byte(os.Getenv("SESSION_SECRET")),
		TranslationToken: strings.TrimSpace(os.Getenv("TRANSLATION_TOKEN")),
	}
	if cfg.Addr == "" {
		cfg.Addr = "0.0.0.0:9917"
	}
	if cfg.OllamaURL == "" {
		cfg.OllamaURL = "http://127.0.0.1:11434"
	}
	if cfg.OllamaModel == "" {
		cfg.OllamaModel = "gemma3:1b"
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		if strings.HasPrefix(cfg.Addr, ":") {
			cfg.Addr = "0.0.0.0" + cfg.Addr
		} else if !strings.Contains(cfg.Addr, ":") {
			cfg.Addr = "0.0.0.0:" + cfg.Addr
		}
	}

	var missing []string
	if cfg.AdminUsername == "" {
		missing = append(missing, "ADMIN_USERNAME")
	}
	if cfg.AdminPassword == "" {
		missing = append(missing, "ADMIN_PASSWORD")
	}
	if len(cfg.SessionSecret) == 0 {
		missing = append(missing, "SESSION_SECRET")
	}
	if cfg.TranslationToken == "" {
		missing = append(missing, "TRANSLATION_TOKEN")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required config values in %s: %s", configPath, strings.Join(missing, ", "))
	}

	return cfg, nil
}

func initAuthDB(path string) (*sql.DB, error) {
	if err := ensureAuthDBFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS login_failures (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ip TEXT NOT NULL,
			attempted_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS login_failures_ip_attempted_at_idx ON login_failures (ip, attempted_at)`,
		`CREATE TABLE IF NOT EXISTS groupme_bots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			group_id TEXT NOT NULL UNIQUE,
			bot_id TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS webhook_request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			requested_at DATETIME NOT NULL,
			ip TEXT NOT NULL,
			token_status TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			group_id TEXT NOT NULL DEFAULT '',
			result TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS webhook_request_logs_requested_at_idx ON webhook_request_logs (requested_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}

	return db, nil
}

func ensureAuthDBFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s *appServer) registerRoutes(r *gin.Engine) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.POST("/", s.handleWebhook)
	r.GET("/login", s.handleLoginPage)
	r.POST("/login", s.handleLogin)
	r.POST("/logout", s.requireAdmin(s.handleLogout))

	admin := r.Group("/admin")
	admin.Use(s.requireAdminMiddleware())
	admin.GET("", s.handleAdmin)
	admin.POST("/groupmes", s.handleCreateGroupme)
	admin.POST("/groupmes/:id", s.handleUpdateGroupme)
	admin.POST("/groupmes/:id/delete", s.handleDeleteGroupme)
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func (s *appServer) handleWebhook(c *gin.Context) {
	token := c.Query("translation-token")
	tokenStatus := "invalid"
	if token == "" {
		tokenStatus = "missing"
	} else if constantTimeEqual(token, s.cfg.TranslationToken) {
		tokenStatus = "valid"
	}
	groupID := ""
	result := "request received"
	defer func() {
		if err := s.recordWebhookRequest(clientIP(c.Request), tokenStatus, c.Writer.Status(), groupID, result); err != nil {
			log.Printf("record webhook request: %v", err)
		}
	}()

	if tokenStatus != "valid" {
		result = "invalid translation token"
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid translation token"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		result = "could not read request body"
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var message Message
	if err := json.Unmarshal(body, &message); err != nil {
		result = "invalid JSON body"
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	groupID = message.GroupID

	groupmeBotID, err := s.botIDForGroup(message.GroupID)
	if err != nil {
		result = "group is not configured"
		c.JSON(http.StatusNotFound, gin.H{"error": "group is not configured"})
		return
	}

	targetLanguage, subString, ok := parseTranslationCommand(message.Text)
	if !ok {
		result = "no translation keyword provided"
		c.JSON(http.StatusBadRequest, gin.H{"message": "no keyword provided"})
		return
	}

	if err := postGroupmeMessage(groupmeBotID, "Thinking..."); err != nil {
		result = "GroupMe acknowledgement failed: " + truncateLogValue(err.Error(), 180)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	translatedText, err := translate(s.cfg.OllamaURL, s.cfg.OllamaModel, targetLanguage, subString)
	if err != nil {
		result = "translation failed: " + truncateLogValue(err.Error(), 180)
		if errors.Is(err, errOllamaUnavailable) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := postGroupmeMessage(groupmeBotID, translatedText); err != nil {
		result = "GroupMe post failed: " + truncateLogValue(err.Error(), 180)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result = "translation posted to GroupMe"
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func truncateLogValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

func (s *appServer) recordWebhookRequest(ip, tokenStatus string, statusCode int, groupID, result string) error {
	if s.db == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO webhook_request_logs (requested_at, ip, token_status, status_code, group_id, result) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now(), truncateLogValue(ip, 100), tokenStatus, statusCode, truncateLogValue(groupID, 100), truncateLogValue(result, 240),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM webhook_request_logs WHERE id NOT IN (SELECT id FROM webhook_request_logs ORDER BY id DESC LIMIT ?)`, requestLogLimit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *appServer) botIDForGroup(groupID string) (string, error) {
	var botID string
	err := s.db.QueryRow(`SELECT bot_id FROM groupme_bots WHERE group_id = ? AND enabled = 1`, groupID).Scan(&botID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return botID, err
}

func parseTranslationCommand(text string) (string, string, bool) {
	fields := strings.SplitN(strings.TrimSpace(text), " ", 2)
	if len(fields) != 2 {
		return "", "", false
	}

	switch strings.ToLower(fields[0]) {
	case "$english":
		return "English", fields[1], true
	case "$spanish":
		return "Spanish", fields[1], true
	default:
		return "", "", false
	}
}

var errOllamaUnavailable = errors.New("ollama is not running or unreachable")

func translate(ollamaURL, model, targetLanguage, text string) (string, error) {
	endpoint := strings.TrimRight(ollamaURL, "/") + "/api/generate"
	prompt := fmt.Sprintf("Translate the following text to %s. Return only the translated text, with no explanation or quotation marks.\n\nText:\n%s", targetLanguage, text)

	requestBody, err := json.Marshal(map[string]any{
		"model":  model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": 0,
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w at %s; start Ollama and run `ollama pull %s`", errOllamaUnavailable, ollamaURL, model)
	}
	defer resp.Body.Close()

	var translationResponse TranslationResponse
	if err := json.NewDecoder(resp.Body).Decode(&translationResponse); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if translationResponse.Error != "" {
			return "", fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, translationResponse.Error)
		}
		return "", fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}
	if translationResponse.Error != "" {
		return "", fmt.Errorf("ollama returned error: %s", translationResponse.Error)
	}
	translatedText := strings.TrimSpace(translationResponse.Response)
	if translatedText == "" {
		return "", errors.New("ollama returned no translation")
	}

	return translatedText, nil
}

func postGroupmeMessage(botID, text string) error {
	requestBody, err := json.Marshal(GroupmeRequestBody{
		Text:  text,
		BotID: botID,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.groupme.com/v3/bots/post", bytes.NewBuffer(requestBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("groupme returned status %d", resp.StatusCode)
	}

	return nil
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]time.Time{}}
}

func (s *sessionStore) create() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}

	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiresAt, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(expiresAt) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

func (s *appServer) signSession(token string) string {
	mac := hmac.New(sha256.New, s.cfg.SessionSecret)
	mac.Write([]byte(token))
	return token + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *appServer) verifySessionCookie(value string) (string, bool) {
	token, sig, ok := strings.Cut(value, ".")
	if !ok || token == "" || sig == "" {
		return "", false
	}

	mac := hmac.New(sha256.New, s.cfg.SessionSecret)
	mac.Write([]byte(token))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	return token, s.sessions.valid(token)
}

func (s *appServer) requireAdmin(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.hasAdminSession(c) {
			redirectToLogin(c)
			return
		}
		next(c)
	}
}

func (s *appServer) requireAdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.hasAdminSession(c) {
			redirectToLogin(c)
			return
		}
		c.Next()
	}
}

func (s *appServer) hasAdminSession(c *gin.Context) bool {
	cookie, err := c.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	_, ok := s.verifySessionCookie(cookie)
	return ok
}

func redirectToLogin(c *gin.Context) {
	c.Redirect(http.StatusSeeOther, "/login")
	c.Abort()
}

func (s *appServer) handleLoginPage(c *gin.Context) {
	if cookie, err := c.Cookie(sessionCookieName); err == nil {
		if _, ok := s.verifySessionCookie(cookie); ok {
			c.Redirect(http.StatusSeeOther, "/admin")
			return
		}
	}
	renderHTML(c, loginTemplate, gin.H{"Error": c.Query("error")})
}

func (s *appServer) handleLogin(c *gin.Context) {
	ip := clientIP(c.Request)
	blocked, err := s.loginBlocked(ip)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	if blocked {
		c.String(http.StatusTooManyRequests, "too many failed login attempts")
		return
	}

	username := c.PostForm("username")
	password := c.PostForm("password")
	if !constantTimeEqual(username, s.cfg.AdminUsername) || !constantTimeEqual(password, s.cfg.AdminPassword) {
		_ = s.recordLoginFailure(ip)
		c.Redirect(http.StatusSeeOther, "/login?error=invalid")
		return
	}

	token, err := s.sessions.create()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.signSession(token),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		MaxAge:   int(sessionTTL.Seconds()),
	})
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (s *appServer) handleLogout(c *gin.Context) {
	if cookie, err := c.Cookie(sessionCookieName); err == nil {
		if token, ok := s.verifySessionCookie(cookie); ok {
			s.sessions.delete(token)
		}
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	c.Redirect(http.StatusSeeOther, "/login")
}

func constantTimeEqual(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return hmac.Equal(aHash[:], bHash[:])
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *appServer) loginBlocked(ip string) (bool, error) {
	cutoff := time.Now().Add(-loginWindow)
	if _, err := s.db.Exec(`DELETE FROM login_failures WHERE attempted_at < ?`, cutoff); err != nil {
		return false, err
	}

	var failures int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM login_failures WHERE ip = ? AND attempted_at >= ?`, ip, cutoff).Scan(&failures); err != nil {
		return false, err
	}
	return failures >= maxLoginFailures, nil
}

func (s *appServer) recordLoginFailure(ip string) error {
	_, err := s.db.Exec(`INSERT INTO login_failures (ip, attempted_at) VALUES (?, ?)`, ip, time.Now())
	return err
}

func (s *appServer) handleAdmin(c *gin.Context) {
	bots, err := s.listGroupmeBots()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	requests, err := s.listWebhookRequests()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	renderHTML(c, adminTemplate, gin.H{
		"Bots":     bots,
		"Requests": requests,
	})
}

func (s *appServer) handleCreateGroupme(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	groupID := strings.TrimSpace(c.PostForm("group_id"))
	botID := strings.TrimSpace(c.PostForm("bot_id"))
	if name == "" || groupID == "" || botID == "" {
		c.String(http.StatusBadRequest, "name, group_id, and bot_id are required")
		return
	}

	now := time.Now()
	_, err := s.db.Exec(
		`INSERT INTO groupme_bots (name, group_id, bot_id, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		name,
		groupID,
		botID,
		now,
		now,
	)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (s *appServer) handleUpdateGroupme(c *gin.Context) {
	id := c.Param("id")
	enabled := 0
	if c.PostForm("enabled") == "on" {
		enabled = 1
	}

	result, err := s.db.Exec(
		`UPDATE groupme_bots SET name = ?, group_id = ?, bot_id = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(c.PostForm("name")),
		strings.TrimSpace(c.PostForm("group_id")),
		strings.TrimSpace(c.PostForm("bot_id")),
		enabled,
		time.Now(),
		id,
	)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		c.String(http.StatusNotFound, "groupme mapping not found")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (s *appServer) handleDeleteGroupme(c *gin.Context) {
	_, err := s.db.Exec(`DELETE FROM groupme_bots WHERE id = ?`, c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (s *appServer) listGroupmeBots() ([]groupmeBot, error) {
	rows, err := s.db.Query(`SELECT id, name, group_id, bot_id, enabled, created_at, updated_at FROM groupme_bots ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bots []groupmeBot
	for rows.Next() {
		var bot groupmeBot
		var enabled int
		if err := rows.Scan(&bot.ID, &bot.Name, &bot.GroupID, &bot.BotID, &enabled, &bot.CreatedAt, &bot.UpdatedAt); err != nil {
			return nil, err
		}
		bot.Enabled = enabled == 1
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

func (s *appServer) listWebhookRequests() ([]webhookRequestLog, error) {
	rows, err := s.db.Query(`SELECT id, requested_at, ip, token_status, status_code, group_id, result FROM webhook_request_logs ORDER BY id DESC LIMIT ?`, requestLogLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []webhookRequestLog
	for rows.Next() {
		var request webhookRequestLog
		if err := rows.Scan(&request.ID, &request.RequestedAt, &request.IP, &request.TokenStatus, &request.StatusCode, &request.GroupID, &request.Result); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func renderHTML(c *gin.Context, source string, data gin.H) {
	tmpl := template.Must(template.New("page").Funcs(template.FuncMap{
		"checked": func(v bool) template.HTMLAttr {
			if v {
				return "checked"
			}
			return ""
		},
	}).Parse(source))
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(c.Writer, data); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
	}
}

const loginTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>translation-bot login</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <main class="auth-shell">
    <form class="panel login-panel" method="post" action="/login">
      <h1>translation-bot</h1>
      {{if .Error}}<p class="error">Invalid username or password.</p>{{end}}
      <label>Username <input name="username" autocomplete="username" required></label>
      <label>Password <input name="password" type="password" autocomplete="current-password" required></label>
      <button type="submit">Sign in</button>
    </form>
  </main>
</body>
</html>`

const adminTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>translation-bot admin</title>
  <style>` + baseCSS + `</style>
</head>
<body>
  <header>
    <h1>GroupMe mappings</h1>
    <form method="post" action="/logout"><button type="submit" class="secondary">Sign out</button></form>
  </header>
  <main>
    <section class="panel">
      <div class="section-heading">
        <h2>Latest translation requests</h2>
        <span class="muted">Newest first · last 20 only</span>
      </div>
      {{if .Requests}}
        <div class="table-scroll">
          <table>
            <thead><tr><th>Time</th><th>Source IP</th><th>Token</th><th>Status</th><th>Group ID</th><th>Result</th></tr></thead>
            <tbody>
              {{range .Requests}}
              <tr>
                <td class="nowrap">{{.RequestedAt.Format "Jan 02, 2006 3:04:05 PM MST"}}</td>
                <td><code>{{.IP}}</code></td>
                <td><span class="badge token-{{.TokenStatus}}">{{.TokenStatus}}</span></td>
                <td><span class="badge status-{{.StatusCode}}">{{.StatusCode}}</span></td>
                <td><code>{{if .GroupID}}{{.GroupID}}{{else}}—{{end}}</code></td>
                <td>{{.Result}}</td>
              </tr>
              {{end}}
            </tbody>
          </table>
        </div>
      {{else}}
        <p class="muted">No translation requests have arrived yet.</p>
      {{end}}
    </section>
    <section class="panel">
      <h2>Add mapping</h2>
      <form method="post" action="/admin/groupmes" class="grid-form">
        <label>Name <input name="name" required></label>
        <label>Group ID <input name="group_id" required></label>
        <label>Bot ID <input name="bot_id" required></label>
        <button type="submit">Add</button>
      </form>
    </section>
    <section class="panel">
      <h2>Configured groups</h2>
      {{if .Bots}}
        <div class="rows">
          {{range .Bots}}
          <form method="post" action="/admin/groupmes/{{.ID}}" class="mapping-row">
            <label>Name <input name="name" value="{{.Name}}" required></label>
            <label>Group ID <input name="group_id" value="{{.GroupID}}" required></label>
            <label>Bot ID <input name="bot_id" value="{{.BotID}}" required></label>
            <label class="checkbox"><input type="checkbox" name="enabled" {{checked .Enabled}}> Enabled</label>
            <button type="submit">Save</button>
            <button form="delete-{{.ID}}" type="submit" class="danger">Delete</button>
          </form>
          <form id="delete-{{.ID}}" method="post" action="/admin/groupmes/{{.ID}}/delete"></form>
          {{end}}
        </div>
      {{else}}
        <p class="muted">No GroupMe mappings are configured.</p>
      {{end}}
    </section>
  </main>
</body>
</html>`

const baseCSS = `
:root { color-scheme: light; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #18212f; background: #f4f6f8; }
* { box-sizing: border-box; }
body { margin: 0; }
header { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 24px clamp(16px, 4vw, 40px); background: #ffffff; border-bottom: 1px solid #d9e0e7; }
main { width: min(1180px, calc(100% - 32px)); margin: 28px auto; display: grid; gap: 20px; }
h1, h2 { margin: 0; line-height: 1.2; letter-spacing: 0; }
h1 { font-size: 28px; }
h2 { font-size: 18px; margin-bottom: 16px; }
.section-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin-bottom: 16px; }
.section-heading h2 { margin-bottom: 0; }
.auth-shell { min-height: 100vh; display: grid; place-items: center; margin: 0 auto; }
.panel { background: #ffffff; border: 1px solid #d9e0e7; border-radius: 8px; padding: 20px; box-shadow: 0 1px 2px rgb(24 33 47 / 0.05); }
.login-panel { width: min(420px, calc(100vw - 32px)); display: grid; gap: 16px; }
form { margin: 0; }
.grid-form, .mapping-row { display: grid; grid-template-columns: minmax(150px, 1fr) minmax(180px, 1.3fr) minmax(180px, 1.3fr) auto; gap: 12px; align-items: end; }
.mapping-row { grid-template-columns: minmax(150px, 1fr) minmax(180px, 1.3fr) minmax(180px, 1.3fr) auto auto auto; padding: 14px 0; border-top: 1px solid #e6ebf0; }
.mapping-row:first-child { border-top: 0; padding-top: 0; }
label { display: grid; gap: 6px; font-size: 13px; font-weight: 650; color: #445166; }
label.checkbox { display: flex; align-items: center; gap: 8px; height: 40px; }
input { width: 100%; min-height: 40px; border: 1px solid #b9c3d0; border-radius: 6px; padding: 8px 10px; font: inherit; color: #18212f; background: #ffffff; }
button { min-height: 40px; border: 0; border-radius: 6px; padding: 0 14px; font: inherit; font-weight: 700; color: #ffffff; background: #176b5f; cursor: pointer; }
button.secondary { color: #18212f; background: #e8edf2; }
button.danger { background: #a8323a; }
.error { margin: 0; color: #a8323a; font-weight: 700; }
.muted { margin: 0; color: #68758a; }
.table-scroll { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { padding: 10px 12px; border-top: 1px solid #e6ebf0; text-align: left; vertical-align: top; }
th { border-top: 0; color: #445166; font-size: 12px; text-transform: uppercase; letter-spacing: .04em; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.nowrap { white-space: nowrap; }
.badge { display: inline-block; border-radius: 999px; padding: 3px 8px; font-size: 12px; font-weight: 750; background: #e8edf2; }
.token-valid { color: #126154; background: #dff4ed; }
.token-invalid, .token-missing { color: #8d2830; background: #f9e2e4; }
@media (max-width: 850px) {
  header { align-items: flex-start; flex-direction: column; }
  .grid-form, .mapping-row { grid-template-columns: 1fr; align-items: stretch; }
  label.checkbox { height: auto; }
}
`
