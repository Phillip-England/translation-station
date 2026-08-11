FROM golang:1.20 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN CGO_ENABLED=1 go build -v -o /usr/local/bin/translation-bot ./...

FROM debian:bookworm-slim

WORKDIR /app
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/* \
	&& mkdir -p /app/config /app/data

COPY --from=build /usr/local/bin/translation-bot /usr/local/bin/translation-bot

EXPOSE 9917

CMD ["translation-bot"]
