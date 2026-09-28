# Agent JSON + cursor paging result

## Verification

Commands run (from repo root):

```sh
go test ./internal/web -run TestAgentJSON -count=1
go test ./internal/web -run TestAgentJSONWindowsAcrossScripts -count=1
go test ./internal/web -run TestAgentJSONInvalidUTF8Window -count=1
go test ./internal/web -run TestAgentJSONPaging -count=1
go test ./internal/web -run TestAgentJSONCaps -count=1
go test ./internal/web -run TestAgentJSONErrorsAndHTML -count=1
go test ./internal/web -run TestAgentJSON -count=1
go vet ./... && go test -race ./...
go build -o ./spanbox ./cmd/spanbox
git diff --check
```

Test-first red: the agent tests initially failed because requests returned HTML, malformed parameters did not return JSON 400, and bearer authentication redirected. After implementation, the targeted agent tests passed. The first `go vet ./...` found a protobuf lock copied in the test fixture; that fixture was changed to `proto.Clone`. The combined `go vet ./... && go test -race ./...` then exited 0. The added script tests passed without production changes: all nine scripts reassembled byte-identically at both window lengths, and English search remained unchanged. Earlier rerun: `go vet ./...` exited 0; `go test -race ./...` exited 0 (`cmd/spanbox`: no test files; `internal/config`, `normalize`, `otlp`, `pricing`, `proxy`, `store`, `web`: `ok`; `internal/web`: 30.838s). `git diff --check` exited 0.

Review follow-up: `TestAgentJSONInvalidUTF8Window` failed before the fix (`invalid_utf8` absent), then passed after adding the flag to malformed field windows; valid field windows omit it. Paging now checks the exact seeded trace, session, and span ID sets. Cap tests seed 52 traces and >20,000 input characters, checking a 50-row page and a 20,000-rune window with continuation. HTML checks assert route-specific content, not only status and content type. All targeted tests passed. Final `go vet ./... && go test -race ./...` exited 0: `cmd/spanbox` has no test files; all seven internal packages passed (`internal/web` 28.899s; others cached). `git diff --check` exited 0. No commit was made.

## Real curl responses

Built `./spanbox`, ran it with a temporary `DATA_DIR`, `RETENTION_DAYS=0`, `PORT=55620`, and `AUTH_TOKEN=sample-token`. Seeded two traces through `curl -X POST -H 'Content-Type: application/json' -H 'Authorization: Bearer sample-token' --data-binary @payload.json http://127.0.0.1:55620/v1/traces` → `{}`. The payload was derived from `internal/otlp/testdata/trace.json` with two unique trace/span IDs, current timestamps, and `gen_ai.input.messages="Hello 字"`.

```sh
curl -sS -H 'Authorization: Bearer sample-token' 'http://127.0.0.1:55620/?format=json&limit=1'
```
```json
{"items":[{"cost_usd":null,"duration_ms":100,"end_ns":1790580638700165000,"has_error":false,"input_tokens":null,"llm_count":0,"models":"","name":"agent-root","output_tokens":null,"service":"demo","session_id":"","span_count":1,"start_ns":1790580638600165000,"trace_id":"0102030405060708090a0b0c0d0e0001","user_id":""}],"next_cursor":"1790580638600165000:0102030405060708090a0b0c0d0e0001"}
```

```sh
curl -sS -H 'Authorization: Bearer sample-token' 'http://127.0.0.1:55620/?format=json&limit=1&cursor=1790580638600165000:0102030405060708090a0b0c0d0e0001'
```
```json
{"items":[{"cost_usd":null,"duration_ms":100,"end_ns":1790580638699165000,"has_error":false,"input_tokens":null,"llm_count":0,"models":"","name":"agent-root","output_tokens":null,"service":"demo","session_id":"","span_count":1,"start_ns":1790580638599165000,"trace_id":"0102030405060708090a0b0c0d0e0000","user_id":""}],"next_cursor":null}
```

```sh
curl -sS -H 'Authorization: Bearer sample-token' 'http://127.0.0.1:55620/spans/0102030405060708090a0b0c0d0e0000/1112131415161700?format=json&field=input&offset=0&len=5'
```
```json
{"cost_usd":null,"duration_ms":100,"input":{"text":"{\"mes","total_chars":22,"next_offset":5},"input_tokens":null,"kind":"agent","model":"","name":"agent-root","output_tokens":null,"parent_span_id":"","provider":"","span_id":"1112131415161700","start_offset_ms":0,"status_code":0,"status_message":"","trace_id":"0102030405060708090a0b0c0d0e0000"}
```

Keep all non-cursor query parameters unchanged when requesting the next list page. The temporary server and data were stopped/removed after capture.
