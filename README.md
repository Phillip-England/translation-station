# translation-bot

translation-bot receives GroupMe bot callbacks and reposts Ollama translations when a message starts with `$spanish` or `$english`.

Configured GroupMe groups are managed in the private admin portal instead of hardcoded environment variables.

## Configuration

Runtime settings are loaded from `config/.env`. The supported variables are documented in `schema.json`.

```text
ADDR=0.0.0.0:9917
OLLAMA_URL=http://127.0.0.1:11434
OLLAMA_MODEL=gemma3:1b
ADMIN_USERNAME=admin
ADMIN_PASSWORD=change-this-password
SESSION_SECRET=replace-with-a-long-random-secret
TRANSLATION_TOKEN=replace-with-a-long-random-webhook-token
```

`config/` is ignored by Git. Do not commit real credentials.

## Persistent Data

The SQLite database is stored at `data/main.sqlite`. It contains admin login failure records and GroupMe group-to-bot mappings. The app creates the parent directory and database file on startup.

`data/` is ignored by Git.

## Run Locally

```bash
go mod tidy
ollama pull gemma3:1b
go run .
```

Ollama must be running on the same system, or set `OLLAMA_URL` to the reachable Ollama server. If Ollama is unavailable when a translation request arrives, the webhook returns `503` with a message telling you Ollama is not running or reachable.

Open `http://localhost:9917/admin` and sign in with the configured admin credentials. Add each GroupMe mapping with a display name, the GroupMe `group_id`, and the GroupMe bot ID that should repost translations into that group.

Configure GroupMe to post webhooks to `http://localhost:9917/?translation-token=replace-with-a-long-random-webhook-token`, using your real public server URL and `TRANSLATION_TOKEN` value.

## Docker

```bash
docker build -t translation-bot .
docker run --rm -p 9917:9917 \
  -v "$PWD/config:/app/config" \
  -v "$PWD/data:/app/data" \
  translation-bot
```

The container listens on `0.0.0.0:9917`, reads `/app/config/.env`, and stores persistent state in `/app/data/main.sqlite`. When running in Docker, `127.0.0.1` points at the container; set `OLLAMA_URL` to an address the container can reach if Ollama runs on the host.
