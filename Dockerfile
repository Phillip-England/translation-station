FROM golang:1.20

WORKDIR /app

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl zstd \
	&& curl -fsSL https://ollama.com/install.sh | sh \
	&& rm -rf /var/lib/apt/lists/* \
	&& mkdir -p /app/config /app/data

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN set -eu; \
	CGO_ENABLED=1 go build -v -o /usr/local/bin/translation-bot .; \
	rm -f /app/config/.env /app/data/main.sqlite; \
	ollama serve >/tmp/ollama-build.log 2>&1 & \
	ollama_pid=$!; \
	ready=0; \
	for attempt in $(seq 1 60); do \
		if ollama list >/dev/null 2>&1; then ready=1; break; fi; \
		sleep 1; \
	done; \
	if [ "$ready" -ne 1 ]; then cat /tmp/ollama-build.log; kill "$ollama_pid"; exit 1; fi; \
	ollama pull gemma3:1b; \
	kill "$ollama_pid"; \
	wait "$ollama_pid" || true; \
	rm -f /tmp/ollama-build.log

ENV OLLAMA_HOST=127.0.0.1:11434

EXPOSE 9917

CMD ["bash", "-c", "trap 'kill -TERM $ollama_pid $app_pid 2>/dev/null || true; wait 2>/dev/null || true' TERM INT EXIT; ollama serve & ollama_pid=$!; for attempt in $(seq 1 60); do ollama list >/dev/null 2>&1 && break; kill -0 $ollama_pid 2>/dev/null || exit 1; sleep 1; done; ollama list >/dev/null 2>&1 || exit 1; translation-bot & app_pid=$!; wait -n $ollama_pid $app_pid"]
