#!/usr/bin/env bash
set -uo pipefail

ollama_pid=""
app_pid=""

shutdown() {
	trap - TERM INT
	if [[ -n "$app_pid" ]] && kill -0 "$app_pid" 2>/dev/null; then
		kill -TERM "$app_pid" 2>/dev/null || true
	fi
	if [[ -n "$ollama_pid" ]] && kill -0 "$ollama_pid" 2>/dev/null; then
		kill -TERM "$ollama_pid" 2>/dev/null || true
	fi
	wait 2>/dev/null || true
}

trap 'shutdown; exit 143' TERM
trap 'shutdown; exit 130' INT

ollama serve &
ollama_pid=$!

ready=0
for _ in {1..60}; do
	if ollama list >/dev/null 2>&1; then
		ready=1
		break
	fi
	if ! kill -0 "$ollama_pid" 2>/dev/null; then
		echo "Ollama exited before becoming ready" >&2
		wait "$ollama_pid"
		exit $?
	fi
	sleep 1
done

if [[ "$ready" -ne 1 ]]; then
	echo "Ollama did not become ready within 60 seconds" >&2
	shutdown
	exit 1
fi

translation-bot &
app_pid=$!

wait -n "$ollama_pid" "$app_pid"
status=$?
shutdown
exit "$status"
