#!/bin/sh
# webgate compose unit: private Google CDP proxy + official SearXNG entrypoint.
set -u

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
