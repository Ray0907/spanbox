#!/usr/bin/env python3
"""Check a built Unix binary: OTLP ingest/read and zero-exit SIGINT/SIGTERM."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

binary = str(Path(sys.argv[1]).resolve(strict=True))
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
token = "unix-smoke-secret"
trace_id, span_id = "11" * 16, "22" * 8


def request(base, path, body=None):
    req = urllib.request.Request(base + path, data=body, headers={
        "Authorization": "Bearer " + token, "Content-Type": "application/json",
    })
    with http.open(req, timeout=10) as response:
        assert response.status == 200, (path, response.status)
        return response.read()


for shutdown_signal in (signal.SIGINT, signal.SIGTERM):
    # Exercise macOS's /tmp -> /private/tmp alias with a fresh real database.
    with tempfile.TemporaryDirectory(prefix="spanbox-smoke-", dir="/tmp") as work:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = "http://127.0.0.1:" + str(port)
        log_path = Path(work) / "server.log"
        with log_path.open("wb") as log:
            process = subprocess.Popen([binary], stdout=log, stderr=log, env={
                "PATH": os.environ.get("PATH", ""), "PORT": str(port),
                "DATA_DIR": str(Path(work) / "data"), "RETENTION_DAYS": "0",
                "AUTH_TOKEN": token,
            })
            try:
                deadline = time.monotonic() + 15
                while True:
                    assert process.poll() is None, "binary exited before readiness"
                    try:
                        request(base, "/healthz")
                        break
                    except urllib.error.URLError:
                        if time.monotonic() >= deadline:
                            raise AssertionError("binary never became healthy")
                        time.sleep(0.05)
                start = time.time_ns()
                body = {"resourceSpans": [{"scopeSpans": [{"spans": [{
                    "traceId": trace_id, "spanId": span_id, "name": "unix-smoke",
                    "startTimeUnixNano": str(start), "endTimeUnixNano": str(start + 1_000_000),
                }]}]}]}
                request(base, "/v1/traces", json.dumps(body).encode())
                trace = json.loads(request(base, "/traces/" + trace_id + "?format=json"))
                assert trace["trace_id"] == trace_id and trace["span_count"] == 1, trace
                assert len(trace["spans"]) == 1, trace
                assert trace["spans"][0]["span_id"] == span_id, trace
                assert trace["spans"][0]["name"] == "unix-smoke", trace
                assert process.poll() is None, "binary exited before signal"
                process.send_signal(shutdown_signal)
                assert process.wait(timeout=15) == 0, ("non-graceful exit", process.returncode)
                diagnostics = log_path.read_text()
                assert "shutdown:" not in diagnostics and "panic:" not in diagnostics, diagnostics
                print("PASS real Unix binary: OTLP span read over HTTP; graceful "
                      + signal.Signals(shutdown_signal).name + " (exit 0)", flush=True)
            except BaseException:
                print(log_path.read_text(), file=sys.stderr)
                raise
            finally:
                # Forced cleanup is failure-only, never proof of graceful shutdown.
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=15)
