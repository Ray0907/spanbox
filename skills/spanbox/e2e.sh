#!/usr/bin/env bash
# E2E: build spanbox, seed traces, run every SKILL.md command verbatim, assert shapes.
# Usage: skills/spanbox/e2e.sh [bash|zsh]   (shell used to run the skill's q function)
set -euo pipefail
cd "$(dirname "$0")/../.."
SH=${1:-bash}
TMP=$(mktemp -d); trap 'kill $PID 2>/dev/null || true; rm -rf "$TMP"' EXIT
go build -o "$TMP/spanbox" ./cmd/spanbox
PORT=$((20000 + RANDOM % 20000))
export SPANBOX_URL=http://127.0.0.1:$PORT SPANBOX_TOKEN=e2e-token-0123456789abcdef0123456789abcdef
AUTH_TOKEN=$SPANBOX_TOKEN PORT=$PORT DATA_DIR=$TMP/data RETENTION_DAYS=0 "$TMP/spanbox" >"$TMP/log" 2>&1 & PID=$!
for _ in $(seq 50); do curl -fsS "$SPANBOX_URL/healthz" >/dev/null 2>&1 && break; sleep 0.1; done

python3 - "$TMP/seed.json" <<'PY'
import json, sys, time, base64
now = time.time_ns()
long_out = ("第一段說明 step ok. " * 400) + "ERROR: rate limit exceeded for gpt-4o"
def a(k, v): return {"key": k, "value": {"intValue": str(v)} if isinstance(v, int) else {"stringValue": v}}
def b64(h): return base64.b64encode(bytes.fromhex(h)).decode()
spans = []
for i in range(3):
    tid, root, llm = f"{i+1:032x}", f"{i+1:016x}", f"{i+101:016x}"
    t0 = now - (3 - i) * 10**9
    err = i == 2
    spans += [
        {"traceId": b64(tid), "spanId": b64(root), "name": f"run-{i}", "startTimeUnixNano": str(t0), "endTimeUnixNano": str(t0 + 9 * 10**8),
         "attributes": [a("gen_ai.operation.name", "invoke_agent")]},
        {"traceId": b64(tid), "spanId": b64(llm), "parentSpanId": b64(root), "name": "chat gpt-4o",
         "startTimeUnixNano": str(t0 + 10**8), "endTimeUnixNano": str(t0 + 8 * 10**8),
         "status": {"code": "STATUS_CODE_ERROR" if err else "STATUS_CODE_OK"},
         "attributes": [a("gen_ai.operation.name", "chat"), a("gen_ai.request.model", "gpt-4o"),
                        a("gen_ai.usage.input_tokens", 100), a("gen_ai.usage.output_tokens", 20),
                        a("gen_ai.input.messages", json.dumps([{"role": "user", "content": f"task {i}"}])),
                        a("gen_ai.output.messages", json.dumps([{"role": "assistant", "content": long_out if err else "done"}], ensure_ascii=False))]},
    ]
json.dump({"resourceSpans": [{"resource": {"attributes": [a("service.name", "e2e")]}, "scopeSpans": [{"spans": spans}]}]}, open(sys.argv[1], "w"), ensure_ascii=False)
PY
curl -fsS -H "Authorization: Bearer $SPANBOX_TOKEN" -H 'Content-Type: application/json' --data-binary @"$TMP/seed.json" "$SPANBOX_URL/v1/traces" >/dev/null
sleep 1

# Extract the q() definition from SKILL.md so the test runs exactly what agents run.
Q=$(grep '^q() {' skills/spanbox/SKILL.md)
run() { "$SH" -c "$Q; q '$1'"; }
fail() { echo "FAIL: $*"; exit 1; }
ERR=00000000000000000000000000000003; LLM=0000000000000067

echo "## spanbox skill E2E ($SH)"; echo
r=$(run '/?format=json&limit=1');                          echo "ready: $(jq -c '{n:(.items|length),next:(.next_cursor!=null)}' <<<"$r")"
[ "$(jq '.items|length' <<<"$r")" = 1 ] && [ "$(jq '.next_cursor!=null' <<<"$r")" = true ] || fail ready
r=$(run '/?format=json&limit=10&range=24h&errors=1');      echo "rung1 errors=1: $(jq -c '[.items[].trace_id]' <<<"$r")"
[ "$(jq -r '.items[0].trace_id' <<<"$r")" = $ERR ] && [ "$(jq '.items|length' <<<"$r")" = 1 ] || fail rung1
r=$(run '/search?format=json&limit=10&q=rate%20limit');     echo "rung2 search: $(jq -c '[.items[]|{trace_id,span_id}]' <<<"$r")"
[ "$(jq -r '.items[0].span_id' <<<"$r")" = $LLM ] || fail rung2
r=$(run "/traces/$ERR?format=json");                        echo "rung3 outline: $(jq -c '[.spans[]|{span_id,kind,status_code,output_chars}]' <<<"$r")"
grep -q '"text"' <<<"$r" && fail "rung3 leaks bodies"
total=$(jq "[.spans[]|select(.span_id==\"$LLM\")][0].output_chars" <<<"$r")
r=$(run "/spans/$ERR/$LLM?format=json");                    echo "rung4 head: output total_chars=$(jq .output.total_chars <<<"$r") text=$(jq '.output.text|length' <<<"$r") next=$(jq .output.next_offset <<<"$r")"
[ "$(jq '.output.text|length' <<<"$r")" = 2000 ] && [ "$(jq .output.next_offset <<<"$r")" = 2000 ] || fail rung4
[ "$(jq .output.total_chars <<<"$r")" = "$total" ] || fail "rung4 total_chars != outline output_chars ($total)"
text=$(jq -r .output.text <<<"$r"); off=2000; pages=1
while [ "$off" != null ]; do
  r=$(run "/spans/$ERR/$LLM?format=json&field=output&offset=$off&len=4000"); pages=$((pages+1))
  text+=$(jq -r .output.text <<<"$r"); off=$(jq .output.next_offset <<<"$r")
done
echo "rung5 paged output: pages=$pages chars=${#text} ends_with_error=$([[ $text == *'rate limit exceeded for gpt-4o"}]' ]] && echo yes || echo no)"
[ "${#text}" = "$total" ] && [[ $text == *'rate limit exceeded'* ]] || fail rung5
r=$(SPANBOX_TOKEN=wrong "$SH" -c "$Q; q '/?format=json'");  echo "bad token: $r"
[ "$(jq -r .error <<<"$r")" = unauthorized ] || fail "bad token"
echo; echo "PASS"
