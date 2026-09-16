#!/bin/sh
# End-to-end local durability check using a file:// replica.
# Requires Litestream 0.5.x; on macOS install it with:
#   brew install benbjohnson/litestream/litestream

set -eu

for command in go curl sqlite3 litestream python3; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "missing required command: $command" >&2
    [ "$command" != litestream ] || echo "install with: brew install benbjohnson/litestream/litestream" >&2
    exit 1
  }
done

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/spanbox-litestream.XXXXXX")
spanbox_pid=
litestream_pid=

cleanup() {
  code=$?
  trap - EXIT
  [ -z "$spanbox_pid" ] || kill "$spanbox_pid" 2>/dev/null || true
  [ -z "$litestream_pid" ] || kill "$litestream_pid" 2>/dev/null || true
  [ -z "$spanbox_pid" ] || wait "$spanbox_pid" 2>/dev/null || true
  [ -z "$litestream_pid" ] || wait "$litestream_pid" 2>/dev/null || true
  if [ "$code" -eq 0 ]; then
    echo PASS
  else
    echo FAIL >&2
    for log in "$tmp"/*.log; do
      [ ! -f "$log" ] || { echo "--- $log" >&2; cat "$log" >&2; }
    done
  fi
  rm -rf "$tmp"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
mkdir -p "$tmp/data"
(cd "$root" && go build -o "$tmp/spanbox" ./cmd/spanbox)

AUTH_TOKEN= RETENTION_DAYS=0 DATA_DIR="$tmp/data" PORT="$port" "$tmp/spanbox" >"$tmp/spanbox.log" 2>&1 &
spanbox_pid=$!
i=0
while [ "$i" -lt 100 ]; do
  curl -fsS "http://127.0.0.1:$port/" >/dev/null 2>&1 && break
  kill -0 "$spanbox_pid" 2>/dev/null || exit 1
  i=$((i + 1))
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$port/" >/dev/null

litestream replicate "$tmp/data/spanbox.db" "file://$tmp/replica" >"$tmp/litestream.log" 2>&1 &
litestream_pid=$!
sleep 0.2
kill -0 "$litestream_pid" 2>/dev/null
curl -fsS -H 'Content-Type: application/json' --data-binary "@$root/internal/otlp/testdata/trace.json" \
  "http://127.0.0.1:$port/v1/traces" >/dev/null
sleep 3

kill "$spanbox_pid"
wait "$spanbox_pid" 2>/dev/null || true
spanbox_pid=
kill "$litestream_pid"
wait "$litestream_pid" 2>/dev/null || true
litestream_pid=
rm -rf "$tmp/data"

mkdir -p "$tmp/data2"
litestream restore -o "$tmp/data2/spanbox.db" "file://$tmp/replica"
AUTH_TOKEN= RETENTION_DAYS=0 DATA_DIR="$tmp/data2" PORT="$port" "$tmp/spanbox" >"$tmp/restored-spanbox.log" 2>&1 &
spanbox_pid=$!
i=0
while [ "$i" -lt 100 ]; do
  curl -fsS "http://127.0.0.1:$port/" >/dev/null 2>&1 && break
  kill -0 "$spanbox_pid" 2>/dev/null || exit 1
  i=$((i + 1))
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$port/" >"$tmp/home.html"

grep -q 'agent-root' "$tmp/home.html"
[ "$(sqlite3 "$tmp/data2/spanbox.db" 'PRAGMA integrity_check')" = ok ]
[ "$(sqlite3 "$tmp/data2/spanbox.db" 'SELECT count(*) FROM spans')" = 3 ]
