.PHONY: init

ENV_FILE := config/.env

init:
	@mkdir -p config
	@if [ -s "$(ENV_FILE)" ]; then \
		echo "$(ENV_FILE) already exists; leaving it unchanged."; \
	else \
		{ \
			printf '%s\n' 'ADDR=0.0.0.0:9917'; \
			printf '%s\n' 'OLLAMA_URL=http://127.0.0.1:11434'; \
			printf '%s\n' 'ADMIN_USERNAME=admin'; \
			printf '%s\n' 'ADMIN_PASSWORD=change-this-password'; \
			printf '%s\n' 'SESSION_SECRET=mock-session-secret-change-me'; \
		} > "$(ENV_FILE)"; \
		chmod 600 "$(ENV_FILE)"; \
		echo "Created $(ENV_FILE) with mock values."; \
	fi
