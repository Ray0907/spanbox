#!/usr/bin/env bash
# Requires Go and Python 3; every invocation builds and uses fresh databases.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/spanbox-roundtrip.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo 'Building fresh spanbox binary'
go build -o "$work/spanbox" ./cmd/spanbox
python3 - "$PWD" "$work" <<'PY'
import base64
import copy
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

root, work = map(Path, sys.argv[1:])
token = "roundtrip-e2e-secret"
processes = []
logs = []
# Do not send localhost traffic through an inherited HTTP proxy.
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(base, path, body=None, content_type="application/x-ndjson", credential=token, status=200):
    headers = {"Content-Type": content_type}
    if credential:
        headers["Authorization"] = "Bearer " + credential
    req = urllib.request.Request(base + path, data=body, headers=headers)
    try:
        response = http.open(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = response.read()
        assert response.status == status, (path, response.status, data[:500])
        return data


def start(name):
    # An OS-selected port avoids fixed-port assumptions. A bind race fails loudly.
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    log_path = work / (name + ".log")
    log = log_path.open("wb")
    logs.append(log)
    env = {"PATH": os.environ.get("PATH", ""), "PORT": str(port),
           "DATA_DIR": str(work / name), "RETENTION_DAYS": "0", "AUTH_TOKEN": token}
    process = subprocess.Popen([str(work / "spanbox")], env=env, stdout=log, stderr=log)
    processes.append(process)
    base = "http://127.0.0.1:" + str(port)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        assert process.poll() is None, log_path.read_text()
        try:
            request(base, "/healthz")
            return base
        except urllib.error.URLError:
            time.sleep(0.05)
    raise AssertionError(name + " did not become healthy")


try:
    source, target = start("source"), start("target")
    for base in (source, target):
        assert json.loads(request(base, "/?format=json"))["items"] == [], "database not empty"
        for credential in ("", "wrong"):
            request(base, "/export", credential=credential, status=401)
            request(base, "/import", b"", credential=credential, status=401)
    print("PASS fresh source/target databases; export/import reject missing and wrong auth")

    seed = json.loads((root / "internal/otlp/testdata/trace.json").read_text())
    spans = seed["resourceSpans"][0]["scopeSpans"][0]["spans"]
    now = time.time_ns() - 60_000_000_000
    for span in spans:
        for field in ("startTimeUnixNano", "endTimeUnixNano"):
            span[field] = str(now + int(span[field]) - 1_000_000_000)
    # Preserve the fixture's hierarchy, bytes attribute, events and links.
    spans[1]["attributes"].extend([
        {"key": "gen_ai.input.messages", "value": {"stringValue": "世界 🦄 é\n\u0000" * 3000}},
        {"key": "gen_ai.output.messages", "value": {"stringValue": ""}},
    ])
    error_span = copy.deepcopy(spans[1])
    error_span["traceId"] = base64.b64encode(bytes.fromhex("ee" * 16)).decode()
    error_span["spanId"] = base64.b64encode(bytes.fromhex("ff" * 8)).decode()
    error_span.pop("parentSpanId", None)
    error_span["status"] = {"code": "STATUS_CODE_ERROR", "message": "失敗"}
    error_span["attributes"].append({"key": "gen_ai.request.model", "value": {"stringValue": "roundtrip-error-model"}})
    spans.append(error_span)
    request(source, "/v1/traces", json.dumps(seed).encode(), "application/json")
    exported = request(source, "/export")
    records = [json.loads(line) for line in exported.splitlines()]
    assert len(records) == 4, ("seed span count", len(records))
    assert len({row["TraceID"] for row in records}) == 2
    assert any("世界" in row["InputContent"] for row in records), "unicode input not seeded"
    assert any(row["OutputContent"] == "" for row in records), "empty output not seeded"
    print("PASS OTLP seed: 4 spans, 2 traces, hierarchy, unicode/NUL, empty output, events and links")

    paths = ["/?format=json&limit=50", "/search?format=json&q=chat"]
    for trace_id in sorted({row["TraceID"] for row in records}):
        paths.append("/traces/" + trace_id + "?format=json")
    for row in records:
        for field in ("input", "output", "attributes"):
            for offset in (0, 1, 7, 20000, 100000):
                paths.append("/spans/{}/{}?format=json&field={}&offset={}&len=17".format(
                    row["TraceID"], row["SpanID"], field, offset))
    expected = {path: request(source, path) for path in paths}
    trace_list = json.loads(expected[paths[0]])
    assert len(trace_list["items"]) == 2 and trace_list["next_cursor"] is None
    assert sum(row["span_count"] for row in trace_list["items"]) == 4
    for attempt in (1, 2):
        result = json.loads(request(target, "/import", exported))
        assert result == {"imported": 4}, result
        assert request(target, "/export") == exported, "NDJSON bytes changed"
        for path, data in expected.items():
            assert request(target, path) == data, "read API differs: " + path
        print("PASS import {}: byte-exact export; {} JSON reads match (list, detail, field windows, search)".format(attempt, len(paths)))

    for query in ("model=roundtrip-error-model", "errors=1", "model=roundtrip-error-model&errors=1"):
        filtered = request(source, "/export?" + query)
        rows = [json.loads(line) for line in filtered.splitlines()]
        assert len(rows) == 1 and rows[0]["TraceID"] == "ee" * 16, query
        assert request(target, "/export?" + query) == filtered, query
    assert request(target, "/export?model=missing") == b""
    assert json.loads(request(target, "/import", b"")) == {"imported": 0}
    assert request(target, "/export") == exported
    assert all(process.poll() is None for process in processes), "instance exited unexpectedly"
    print("PASS model/error filters and empty export/import; repeated import is idempotent")
    print("PASS roundtrip-e2e (fresh binary, two instances, OTLP seed)")
except BaseException:
    for log_path in work.glob("*.log"):
        print("--- " + log_path.name + " ---\n" + log_path.read_text(), file=sys.stderr)
    raise
finally:
    for process in processes:
        if process.poll() is None:
            process.terminate()
    for process in processes:
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
    for log in logs:
        log.close()
PY
