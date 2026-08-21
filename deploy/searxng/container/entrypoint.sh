#!/bin/sh
# webgate compose unit: private Google CDP proxy + official SearXNG entrypoint.
set -u

SETTINGS=/etc/searxng/settings.yml
TEMPLATE=/usr/local/searxng/settings.template.yml

# First boot only: materialize settings.yml with a random secret_key.
# Existing deploy/searxng/config/settings.yml is never rewritten.
if [ ! -f "$SETTINGS" ] && [ -f "$TEMPLATE" ]; then
	mkdir -p /etc/searxng
	if command -v openssl >/dev/null 2>&1; then
		SECRET=$(openssl rand -hex 32)
	else
		SECRET=$(/usr/local/searxng/.venv/bin/python -c 'import secrets; print(secrets.token_hex(32))')
	fi
	sed "s/__SEARXNG_SECRET__/${SECRET}/g" "$TEMPLATE" > "$SETTINGS"
fi

/opt/google-cdp/.venv/bin/python /opt/google-cdp/proxy.py &
proxy_pid=$!

/usr/local/searxng/entrypoint.sh &
searxng_pid=$!

stop_process() {
	kill -TERM "$1" 2>/dev/null || true
	wait "$1" 2>/dev/null || true
}

shutdown() {
	stop_process "$searxng_pid"
	stop_process "$proxy_pid"
	exit 0
}

trap shutdown INT TERM

# Exit on either child failure. Docker restart policy then restores the complete
# service instead of leaving SearXNG or the proxy running alone.
while :; do
	if ! kill -0 "$proxy_pid" 2>/dev/null; then
		wait "$proxy_pid"
		status=$?
		stop_process "$searxng_pid"
		exit "$status"
	fi

	if ! kill -0 "$searxng_pid" 2>/dev/null; then
		wait "$searxng_pid"
		status=$?
		stop_process "$proxy_pid"
		exit "$status"
	fi

	sleep 1
done
