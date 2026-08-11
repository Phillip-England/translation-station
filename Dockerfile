FROM golang:1.20 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN CGO_ENABLED=1 go build -v -o /usr/local/bin/translation-bot ./...

FROM ollama/ollama:latest AS ollama

FROM ubuntu:24.04

WORKDIR /app
ENV OLLAMA_HOST=127.0.0.1:11434

COPY --from=ollama /bin/ollama /bin/ollama
COPY --from=ollama --exclude=cuda_v12 --exclude=cuda_v13 --exclude=mlx_cuda_v13 /usr/lib/ollama /usr/lib/ollama

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/* \
	&& mkdir -p /app/config /app/data; \
	ollama serve >/tmp/ollama-build.log 2>&1 & \
	ollama_pid=$!; \
	ready=0; \
	for attempt in $(seq 1 60); do \
		if ollama list >/dev/null 2>&1; then ready=1; break; fi; \
		sleep 1; \
	done; \
	if [ "$ready" -ne 1 ]; then cat /tmp/ollama-build.log; kill "$ollama_pid"; exit 1; fi; \
	if ! ollama pull gemma3:1b; then kill "$ollama_pid"; wait "$ollama_pid" || true; exit 1; fi; \
	kill "$ollama_pid"; \
	wait "$ollama_pid" || true; \
	rm -f /tmp/ollama-build.log

COPY --from=build /usr/local/bin/translation-bot /usr/local/bin/translation-bot
COPY --chmod=755 docker-entrypoint.sh /usr/local/bin/translation-station-entrypoint

EXPOSE 9917

ENTRYPOINT ["/usr/local/bin/translation-station-entrypoint"]
