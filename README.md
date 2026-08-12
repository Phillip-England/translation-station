# translation-bot

translation-bot receives GroupMe bot callbacks and reposts Ollama translations when a message starts with `$spanish` or `$english`. It immediately posts `🤔` after accepting a translation request, then posts the translation when Ollama finishes.

Configured GroupMe groups are managed in the private admin portal instead of hardcoded environment variables.

## Configuration

Runtime settings are loaded from `config/.env`. The supported variables are documented in `schema.json`.

```text
ADDR=0.0.0.0:9917
OLLAMA_URL=http://127.0.0.1:11434
ADMIN_USERNAME=admin
ADMIN_PASSWORD=change-this-password
SESSION_SECRET=replace-with-a-long-random-secret
TRANSLATION_TOKEN=replace-with-a-long-random-webhook-token
```

`config/` is ignored by Git. Do not commit real credentials.

## Persistent Data

The SQLite database is stored at `data/main.sqlite`. It contains admin login failure records, GroupMe group-to-bot mappings, and the latest 20 translation endpoint requests. The request history records whether the translation token was valid, missing, or invalid, but never stores the token itself.

The request history is visible at `/admin` after signing in. It also shows each request's time, source IP, HTTP status, GroupMe group ID (when available), and processing result to help diagnose webhook failures.

`data/` is ignored by Git.

## Run Locally

```bash
go mod tidy
ollama pull translategemma:4b
go run .
```

Ollama must be running on the same system, or set `OLLAMA_URL` to the reachable Ollama server. If Ollama is unavailable when a translation request arrives, the webhook returns `503` with a message telling you Ollama is not running or reachable.

Open `http://localhost:9917/` for the public overview and usage guide. Visit `http://localhost:9917/admin` and sign in with the configured admin credentials to add each GroupMe mapping with a display name, the GroupMe `group_id`, and the GroupMe bot ID that should repost translations into that group.

Configure GroupMe to post webhooks to `http://localhost:9917/?translation-token=replace-with-a-long-random-webhook-token`, using your real public server URL and `TRANSLATION_TOKEN` value.

## Docker

```bash
docker build -t translation-bot .
docker run --rm -p 9917:9917 \
  -v "$PWD/config:/app/config" \
  -v "$PWD/data:/app/data" \
  translation-bot
```

The application uses `translategemma:4b` by default, so `OLLAMA_MODEL` does not need to be configured. The image includes Ollama and that model. Its entrypoint starts Ollama on container loopback before starting the bot, so the default `OLLAMA_URL=http://127.0.0.1:11434` works without another service. Ollama is not exposed outside the container.

The container listens on `0.0.0.0:9917`, reads `/app/config/.env`, and stores persistent state in `/app/data/main.sqlite`.
