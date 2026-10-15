#!/usr/bin/env bash
# Verifies that the capture proxy derives one execute_tool span per tool call
# from the tool results a client sends back. Requires Go and Python 3; every
# run builds a fresh binary and database, and uses a fake vendor upstream.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/spanbox-proxy-tools.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo 'Building fresh spanbox binary'
binary="$work/spanbox$(go env GOEXE)"
go build -o "$binary" ./cmd/spanbox
"${PYTHON:-python3}" - "$work" "$binary" <<'PY'
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

work, binary = map(Path, sys.argv[1:])
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
TOOL_GAP = 0.5  # seconds the client "runs the tool" between two model calls


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def call(base, path, body=None, **extra):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json", **extra}
    if body is not None:
        headers["x-api-key"] = "vendor-key"
        headers["Authorization"] = "Bearer vendor-key"
    with http.open(urllib.request.Request(base + path, data=data, headers=headers), timeout=15) as response:
        raw = response.read()
        try:
            return json.loads(raw or b"null")
        except ValueError:
            return raw  # e.g. an SSE stream


# Fake vendor: the reply depends on whether the request already carries a tool result.
# Every reply gets a unique id; X-Test-Call-Id overrides the tool call id it issues.
counter = iter(range(1, 10_000))


class Vendor(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def codex_stream(self, n, call_id, has_result):
        """ChatGPT Codex SSE as really observed: items arrive in output_item.done, completed.output is []."""
        def send(data):
            self.wfile.write(("event: %s\ndata: %s\n\n" % (data["type"], json.dumps(data))).encode())
            self.wfile.flush()
        item = ({"type": "message", "id": "msg_%d" % n, "role": "assistant", "status": "completed",
                 "content": [{"type": "output_text", "text": "done"}]} if has_result else
                {"type": "function_call", "id": "fc_%d" % n, "call_id": call_id, "name": "shell",
                 "arguments": "{\"c\":1}", "status": "completed"})
        base_response = {"id": "resp_cx_%d" % n, "object": "response", "model": "gpt-test"}
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        send({"type": "response.created", "response": dict(base_response, status="in_progress", output=[])})
        send({"type": "response.output_item.added", "output_index": 0, "item": item})
        send({"type": "response.output_item.done", "output_index": 0, "item": item})
        send({"type": "response.completed", "response": dict(base_response, status="completed", output=[],
              usage={"input_tokens": 10, "output_tokens": 4, "total_tokens": 14})})

    def slow_stream(self, n, call_id, tail):
        """Anthropic SSE: the tool_use block completes at once, the message ends after `tail` seconds."""
        def send(event, data):
            self.wfile.write(("event: %s\ndata: %s\n\n" % (event, json.dumps(data))).encode())
            self.wfile.flush()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        send("message_start", {"type": "message_start", "message": {"id": "msg_stream_%d" % n, "type": "message",
             "role": "assistant", "model": "claude-test", "content": [], "usage": {"input_tokens": 10, "output_tokens": 1}}})
        send("content_block_start", {"type": "content_block_start", "index": 0,
             "content_block": {"type": "tool_use", "id": call_id, "name": "bash", "input": {}}})
        send("content_block_delta", {"type": "content_block_delta", "index": 0,
             "delta": {"type": "input_json_delta", "partial_json": "{\"cmd\":\"ls\"}"}})
        send("content_block_stop", {"type": "content_block_stop", "index": 0})
        time.sleep(tail)
        send("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"output_tokens": 5}})
        send("message_stop", {"type": "message_stop"})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        text = json.dumps(body)
        n = next(counter)
        call_id = self.headers.get("X-Test-Call-Id")
        tool_name = self.headers.get("X-Test-Tool-Name")
        time.sleep(float(self.headers.get("X-Test-Delay") or 0))
        if "/codex/responses" in self.path:
            return self.codex_stream(n, call_id or "call_cx", "function_call_output" in text)
        if self.path.endswith("/messages") and body.get("stream"):
            return self.slow_stream(n, call_id or "toolu_1", float(self.headers.get("X-Test-Tail") or 0))
        if self.path.endswith("/messages"):
            has_result = '"tool_result"' in text
            if has_result:
                reply = {"id": "msg_final_%d" % n, "type": "message", "role": "assistant", "model": "claude-test",
                         "content": [{"type": "text", "text": "done"}], "stop_reason": "end_turn",
                         "usage": {"input_tokens": 20, "output_tokens": 3}}
            else:
                reply = {"id": "msg_call_%d" % n, "type": "message", "role": "assistant", "model": "claude-test",
                         "content": [{"type": "tool_use", "id": call_id or "toolu_1", "name": "bash", "input": {"cmd": "ls"}}],
                         "stop_reason": "tool_use", "usage": {"input_tokens": 10, "output_tokens": 5}}
        elif ":generateContent" in self.path:
            has_result = '"functionResponse"' in json.dumps(body["contents"][-1])
            function_call = {"name": tool_name or "search", "args": {"q": "x"}}
            if call_id:
                function_call["id"] = call_id  # Gemini's optional native call id
            part = {"text": "done"} if has_result else {"functionCall": function_call}
            reply = {"responseId": "gem_%d" % n, "modelVersion": "gemini-test",
                     "candidates": [{"content": {"role": "model", "parts": [part]}, "finishReason": "STOP"}],
                     "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 4, "totalTokenCount": 14}}
        elif self.path.endswith("/responses"):
            has_result = "function_call_output" in text
            output = ([{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "done"}]}]
                      if has_result else
                      [{"type": "function_call", "call_id": call_id or "call_r1", "name": "shell", "arguments": "{\"c\":1}"}])
            reply = {"id": "resp_%d" % n, "object": "response", "model": "gpt-test", "status": "completed",
                     "output": output, "usage": {"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}
        else:
            has_result = '"role": "tool"' in text
            if has_result:
                message = {"role": "assistant", "content": "done"}
            else:
                # A big issuing reply makes its capture slower than the (tiny) result request that follows.
                message = {"role": "assistant", "content": "x" * (12 << 20) if call_id == "call_big" else None,
                           "tool_calls": [{"id": call_id or "call_1", "type": "function",
                                           "function": {"name": "read", "arguments": "{\"path\":\"a\"}"}}]}
            reply = {"id": "chatcmpl_%d" % n, "object": "chat.completion", "model": "gpt-test",
                     "choices": [{"index": 0, "message": message,
                                  "finish_reason": "stop" if has_result else "tool_calls"}],
                     "usage": {"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14}}
        payload = json.dumps(reply).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


vendor_port = free_port()
vendor = ThreadingHTTPServer(("127.0.0.1", vendor_port), Vendor)
threading.Thread(target=vendor.serve_forever, daemon=True).start()

port = free_port()
log = (work / "spanbox.log").open("ab")
env = {"PATH": os.environ.get("PATH", ""), "PORT": str(port), "DATA_DIR": str(work / "data"),
       "RETENTION_DAYS": "0",
       "ANTHROPIC_UPSTREAM": "http://127.0.0.1:%d" % vendor_port,
       "OPENAI_UPSTREAM": "http://127.0.0.1:%d" % vendor_port,
       "GEMINI_UPSTREAM": "http://127.0.0.1:%d" % vendor_port,
       "CHATGPT_UPSTREAM": "http://127.0.0.1:%d" % vendor_port,
       "OPENAI_COMPAT_UPSTREAMS": "ua=http://127.0.0.1:%d,ub=http://127.0.0.1:%d" % (vendor_port, vendor_port)}
base = "http://127.0.0.1:%d" % port
process = None


def stop():
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()


def start():
    global process
    process = subprocess.Popen([str(binary)], env=env, stdout=log, stderr=log)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        assert process.poll() is None, (work / "spanbox.log").read_text()
        try:
            http.open(base + "/healthz", timeout=2).close()
            return
        except urllib.error.URLError:
            time.sleep(0.05)
    raise AssertionError("spanbox did not become healthy")


def traces():
    return call(base, "/?format=json&limit=50")["items"]


def trace_spans(trace_id):
    return [dict(span, trace_id=trace_id) for span in call(base, "/traces/" + trace_id + "?format=json")["spans"]]


def all_spans():
    return [span for item in traces() for span in trace_spans(item["trace_id"])]


def wait_for(predicate, what):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.1)
    raise AssertionError("timed out waiting for " + what + "\n" + json.dumps(all_spans(), indent=1)[:3000])


def tool_spans(name=None):
    return [s for s in all_spans() if s["name"].startswith("execute_tool") and (name is None or s["name"].endswith(" " + name))]


def span_attrs(span):
    return json.loads(call(base, "/spans/%s/%s?format=json&field=attributes&len=20000" %
                           (span["trace_id"], span["span_id"]))["attributes"]["text"])


try:
    start()

    # --- Anthropic: one tool call, one tool result ----------------------------------
    anthropic = base + "/proxy/anthropic"
    user = {"role": "user", "content": "list files"}
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": [user]})
    time.sleep(TOOL_GAP)
    history = [user,
               {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": {"cmd": "ls"}}]},
               {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "file.txt"}]}]
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": history})

    spans = wait_for(lambda: tool_spans("bash"), "anthropic execute_tool span")
    assert len(spans) == 1, spans
    tool = spans[0]
    chat_call = next(s for s in all_spans() if s["name"].startswith("chat") and "msg_call_" in json.dumps(span_attrs(s)))
    assert tool["trace_id"] == chat_call["trace_id"], "tool span must join the trace of the model call that issued it"
    assert tool["parent_span_id"] == chat_call["span_id"], "tool span must be a child of the issuing model call"
    assert tool["duration_ms"] >= TOOL_GAP * 1000 * 0.8, ("tool span must measure the client-side gap", tool["duration_ms"])
    attrs = span_attrs(tool)
    assert attrs["gen_ai.operation.name"] == "execute_tool", attrs
    assert attrs["gen_ai.tool.name"] == "bash", attrs
    assert attrs["gen_ai.tool.call.id"] == "toolu_1", attrs
    assert "ls" in json.dumps(attrs["gen_ai.tool.call.arguments"]), attrs
    assert "file.txt" in json.dumps(attrs["gen_ai.tool.call.result"]), attrs
    print("PASS anthropic: tool_result becomes an execute_tool child of the issuing call with the client-side duration")

    # --- Replays and longer histories must not duplicate the span ---------------------
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": history})
    longer = history + [{"role": "assistant", "content": [{"type": "text", "text": "done"}]},
                        {"role": "user", "content": "thanks"}]
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": longer})
    time.sleep(1)
    assert len(tool_spans("bash")) == 1, "replayed or older tool results must not create new tool spans"
    print("PASS anthropic: replayed request and longer history keep exactly one tool span")

    # --- Error results mark the tool span as failed -----------------------------------
    failing = [user,
               {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_err", "name": "edit", "input": {}}]},
               {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_err", "is_error": True, "content": "boom"}]}]
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": failing})
    failed = wait_for(lambda: tool_spans("edit"), "error tool span")[0]
    assert failed["status_code"] == 2, failed
    print("PASS anthropic: is_error tool_result yields an error-status tool span")

    # --- OpenAI chat completions -------------------------------------------------------
    openai = base + "/proxy/openai"
    ask = {"role": "user", "content": "read a"}
    call(openai, "/v1/chat/completions", {"model": "gpt-test", "messages": [ask]})
    time.sleep(TOOL_GAP)
    call(openai, "/v1/chat/completions", {"model": "gpt-test", "messages": [
        ask,
        {"role": "assistant", "content": None, "tool_calls": [
            {"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{\"path\":\"a\"}"}}]},
        {"role": "tool", "tool_call_id": "call_1", "content": "contents of a"}]})
    read = wait_for(lambda: tool_spans("read"), "openai execute_tool span")
    assert len(read) == 1, read
    assert span_attrs(read[0])["gen_ai.tool.call.id"] == "call_1"
    assert read[0]["parent_span_id"], "openai tool span must have the issuing call as parent"
    print("PASS openai chat: role=tool message becomes an execute_tool child span")

    # --- A tool result whose call we never saw still records, just without a parent ----
    orphan = [{"role": "user", "content": "resume"},
              {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_orphan", "name": "grep", "input": {}}]},
              {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_orphan", "content": "x"}]}]
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": orphan})
    lone = wait_for(lambda: tool_spans("grep"), "orphan tool span")
    assert len(lone) == 1 and not lone[0]["parent_span_id"], lone
    print("PASS orphan: unseen tool call still yields a tool span without a parent")

    # --- Responses API, including a continuation that sends only the output ------------
    responses = base + "/proxy/openai"
    call(responses, "/v1/responses", {"model": "gpt-test", "input": "run it"})
    time.sleep(TOOL_GAP)
    call(responses, "/v1/responses", {"model": "gpt-test", "previous_response_id": "resp_prev", "input": [
        {"type": "function_call_output", "call_id": "call_r1", "output": "exit 0"}]})
    shell = wait_for(lambda: tool_spans("shell"), "responses execute_tool span")
    assert len(shell) == 1, shell
    shell_attrs = span_attrs(shell[0])
    assert shell_attrs["gen_ai.tool.call.id"] == "call_r1" and "exit 0" in json.dumps(shell_attrs["gen_ai.tool.call.result"])
    assert "c" in json.dumps(shell_attrs["gen_ai.tool.call.arguments"]), "arguments must come from the issuing response"
    assert shell[0]["parent_span_id"] and shell[0]["duration_ms"] >= TOOL_GAP * 800, shell[0]
    print("PASS responses: output-only continuation still yields a tool span with arguments, parent and duration")

    # --- Gemini: no call ids, matched by function name -----------------------------------
    gemini = base + "/proxy/gemini"
    ask_g = {"role": "user", "parts": [{"text": "search x"}]}
    call(gemini, "/v1beta/models/gemini-test:generateContent", {"contents": [ask_g]})
    time.sleep(TOOL_GAP)
    call(gemini, "/v1beta/models/gemini-test:generateContent", {"contents": [
        ask_g,
        {"role": "model", "parts": [{"functionCall": {"name": "search", "args": {"q": "x"}}}]},
        {"role": "user", "parts": [{"functionResponse": {"name": "search", "response": {"r": "found it"}}}]}]})
    search = wait_for(lambda: tool_spans("search"), "gemini execute_tool span")
    assert len(search) == 1, search
    search_attrs = span_attrs(search[0])
    assert search_attrs["gen_ai.tool.call.id"], "gemini needs a synthesized call id"
    assert "found it" in json.dumps(search_attrs["gen_ai.tool.call.result"])
    assert search[0]["parent_span_id"], "gemini tool span must be a child of the issuing call"
    print("PASS gemini: functionResponse becomes an execute_tool child matched by function name")

    # --- Same call id in two independent sessions must stay two tool spans -------------
    for session in ("session-a", "session-b"):
        hdr = {"X-Spanbox-Session": session, "X-Test-Call-Id": "toolu_dup"}
        call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": [user]}, **hdr)
        time.sleep(0.1)
        call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": [
            user,
            {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_dup", "name": "dup", "input": {"s": session}}]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_dup", "content": "r-" + session}]}]},
            **{"X-Spanbox-Session": session})
    dups = wait_for(lambda: len(tool_spans("dup")) == 2 and tool_spans("dup"), "two dup tool spans")
    assert len({s["parent_span_id"] for s in dups}) == 2, "each session's tool span needs its own parent"
    assert {json.dumps(span_attrs(s)["gen_ai.tool.call.result"]) for s in dups} == {'"r-session-a"', '"r-session-b"'}, dups
    print("PASS isolation: a reused call id in two sessions yields two separate tool spans")

    # --- Retried result request must not stretch the recorded tool duration ------------
    before = {s["span_id"]: (s["start_offset_ms"], s["duration_ms"]) for s in tool_spans("bash")}
    time.sleep(1.2)
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": history})
    time.sleep(1)
    after = {s["span_id"]: (s["start_offset_ms"], s["duration_ms"]) for s in tool_spans("bash")}
    assert before == after, ("a retry must keep the first observed timing", before, after)
    print("PASS retry: replaying the result request later keeps the original tool timing")

    # --- Capture order: result request arrives while the issuing capture is still parsing
    call(openai, "/v1/chat/completions", {"model": "gpt-test", "messages": [ask]}, **{"X-Test-Call-Id": "call_big"})
    call(openai, "/v1/chat/completions", {"model": "gpt-test", "messages": [
        ask,
        {"role": "assistant", "content": None, "tool_calls": [
            {"id": "call_big", "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
        {"role": "tool", "tool_call_id": "call_big", "content": "ok"}]})

    def big_spans():
        return [s for s in tool_spans("read") if span_attrs(s)["gen_ai.tool.call.id"] == "call_big"]

    wait_for(big_spans, "call_big tool span")
    time.sleep(2)  # best effort: the link may be missing when capture order flips, but never a duplicate
    big = big_spans()
    assert len(big) == 1, ("a result captured before its issuing call must still yield exactly one tool span", big)
    print("PASS ordering: a result captured before its issuing call yields exactly one tool span (link is best effort)")

    # --- Restart: correlation memory is lost, so a replay may add one orphan, never damage the original
    count = len(tool_spans())
    original = {(x["trace_id"], x["span_id"]): x for x in tool_spans("bash")}
    stop()
    start()
    call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": history})
    time.sleep(1.5)
    assert len(tool_spans()) <= count + 1, "replay after restart may add at most one orphan tool span"
    for (trace_id, span_id), x in original.items():
        now = next(y for y in tool_spans("bash") if (y["trace_id"], y["span_id"]) == (trace_id, span_id))
        assert now["parent_span_id"] == x["parent_span_id"] and now["duration_ms"] == x["duration_ms"], \
            "a replay after restart must not alter an already linked tool span"
    print("PASS restart: replay after a restart leaves the linked tool span intact (at most one extra orphan)")

    # --- Gemini: the same tool called again in a later turn is a new execution ---------
    gsess = {"X-Spanbox-Session": "gsess"}
    gpath = "/v1beta/models/gemini-test:generateContent"
    model_call = {"role": "model", "parts": [{"functionCall": {"name": "search", "args": {"q": "x"}}}]}

    def tool_reply(marker):
        return {"role": "user", "parts": [{"functionResponse": {"name": "search", "response": {"r": marker}}}]}

    turn1 = [ask_g, model_call, tool_reply("turn-one")]
    call(gemini, gpath, {"contents": [ask_g]}, **gsess)
    time.sleep(0.2)
    call(gemini, gpath, {"contents": turn1}, **gsess)
    turn2 = turn1 + [{"role": "model", "parts": [{"text": "done"}]}, {"role": "user", "parts": [{"text": "again"}]}]
    call(gemini, gpath, {"contents": turn2}, **gsess)
    time.sleep(0.2)
    call(gemini, gpath, {"contents": turn2 + [model_call, tool_reply("turn-two")]}, **gsess)

    def gemini_turns():
        found = {}
        for x in tool_spans("search"):
            result = json.dumps(span_attrs(x).get("gen_ai.tool.call.result"))
            for marker in ("turn-one", "turn-two"):
                if marker in result:
                    found[marker] = x
        return found if len(found) == 2 else None

    turns = wait_for(gemini_turns, "two gemini tool spans for the same tool in successive turns")
    assert (turns["turn-one"]["trace_id"], turns["turn-one"]["span_id"]) != (turns["turn-two"]["trace_id"], turns["turn-two"]["span_id"]), \
        "a repeated Gemini tool call in a later turn must not overwrite the earlier execution"
    assert turns["turn-one"]["parent_span_id"] and turns["turn-two"]["parent_span_id"] \
        and turns["turn-one"]["parent_span_id"] != turns["turn-two"]["parent_span_id"], "each turn needs its own issuing parent"
    print("PASS gemini turns: the same tool in two turns of one session yields two tool spans")

    # --- Gemini: an explicit error envelope marks the tool span as failed ---------------
    err_hdr = {"X-Spanbox-Session": "gerr", "X-Test-Tool-Name": "fs"}
    fs_call = {"role": "model", "parts": [{"functionCall": {"name": "fs", "args": {}}}]}
    call(gemini, gpath, {"contents": [ask_g]}, **err_hdr)
    time.sleep(0.2)
    call(gemini, gpath, {"contents": [ask_g, fs_call, {"role": "user", "parts": [
        {"functionResponse": {"name": "fs", "response": {"error": "Permission denied"}}}]}]}, **{"X-Spanbox-Session": "gerr"})
    fs = wait_for(lambda: tool_spans("fs"), "gemini error tool span")
    assert len(fs) == 1 and fs[0]["status_code"] == 2, ("a Gemini error envelope must produce an error-status tool span", fs)
    print("PASS gemini error: response.error marks the tool span as failed")

    # --- Two OpenAI-compatible upstreams that reuse the same local call id --------------
    for name in ("ua", "ub"):
        route = base + "/proxy/openai-compat/" + name
        call(route, "/chat/completions", {"model": "gpt-test", "messages": [ask]}, **{"X-Test-Call-Id": "call_0"})
        time.sleep(0.1)
        call(route, "/chat/completions", {"model": "gpt-test", "messages": [
            ask,
            {"role": "assistant", "content": None, "tool_calls": [
                {"id": "call_0", "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
            {"role": "tool", "tool_call_id": "call_0", "content": "result-" + name}]})

    def compat_spans():
        found = [x for x in tool_spans("read") if span_attrs(x)["gen_ai.tool.call.id"] == "call_0"]
        return found if len(found) == 2 else None

    compat = wait_for(compat_spans, "one tool span per compatible upstream")
    assert {json.dumps(span_attrs(x)["gen_ai.tool.call.result"]) for x in compat} == {'"result-ua"', '"result-ub"'}, compat
    assert len({x["parent_span_id"] for x in compat}) == 2, "each upstream's tool span needs its own parent"
    print("PASS openai-compat: the same call id on two configured upstreams stays two tool spans")

    # --- Streaming: the result request may start before the issuing stream has finished ---
    stream = urllib.request.Request(anthropic + "/v1/messages", method="POST",
        data=json.dumps({"model": "claude-test", "max_tokens": 64, "stream": True, "messages": [user]}).encode(),
        headers={"Content-Type": "application/json", "x-api-key": "vendor-key",
                 "X-Test-Call-Id": "call_stream", "X-Test-Tail": "0.6"})
    with http.open(stream, timeout=15) as issuing:
        while b"content_block_stop" not in issuing.readline():
            pass
        call(anthropic, "/v1/messages", {"model": "claude-test", "max_tokens": 64, "messages": [
            user,
            {"role": "assistant", "content": [{"type": "tool_use", "id": "call_stream", "name": "bash", "input": {"cmd": "ls"}}]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_stream", "content": "ok"}]}]},
            **{"X-Test-Delay": "1.5"})
        issuing.read()

    def stream_spans():
        return [x for x in tool_spans("bash") if span_attrs(x)["gen_ai.tool.call.id"] == "call_stream"]

    wait_for(stream_spans, "call_stream tool span")
    streamed = stream_spans()
    assert len(streamed) == 1 and streamed[0]["duration_ms"] >= 0, ("overlapping requests must never give a negative duration", streamed)
    assert streamed[0]["parent_span_id"], "the issuing call finished first, so the link must exist"
    print("PASS streaming: a result sent before the issuing stream ends keeps a non-negative duration")

    # --- Gemini native call ids win over positional ids ---------------------------------
    for native in ("call-1", "call-2"):
        call(gemini, gpath, {"contents": [ask_g]}, **{"X-Spanbox-Session": "gid", "X-Test-Call-Id": native})
        time.sleep(0.2)
        call(gemini, gpath, {"contents": [
            ask_g,
            {"role": "model", "parts": [{"functionCall": {"id": native, "name": "search", "args": {"q": native}}}]},
            {"role": "user", "parts": [{"functionResponse": {"id": native, "name": "search", "response": {"r": "res-" + native}}}]}]},
            **{"X-Spanbox-Session": "gid"})

    def native_spans():
        found = {}
        for x in tool_spans("search"):
            a = span_attrs(x)
            if a["gen_ai.tool.call.id"] in ("call-1", "call-2"):
                found[a["gen_ai.tool.call.id"]] = (x, a)
        return found if len(found) == 2 else None

    native = wait_for(native_spans, "two gemini tool spans keyed by native ids")
    for cid, (x, a) in native.items():
        assert "res-" + cid in json.dumps(a["gen_ai.tool.call.result"]) and cid in json.dumps(a["gen_ai.tool.call.arguments"]), (cid, a)
        assert x["parent_span_id"], cid
    assert native["call-1"][0]["parent_span_id"] != native["call-2"][0]["parent_span_id"], "each native id needs its own issuing parent"
    print("PASS gemini native ids: supplied functionCall ids are used, so same-position calls stay distinct")

    # --- ChatGPT Codex as pi really sends it: full history resent, items only in output_item.done
    codex = base + "/proxy/chatgpt"
    cx_user = {"role": "user", "content": [{"type": "input_text", "text": "run it"}]}
    call(codex, "/codex/responses", {"model": "gpt-test", "store": False, "stream": True, "input": [cx_user]},
         **{"X-Test-Call-Id": "call_cx1"})
    time.sleep(TOOL_GAP)
    call(codex, "/codex/responses", {"model": "gpt-test", "store": False, "stream": True, "input": [
        cx_user,
        {"type": "function_call", "call_id": "call_cx1", "name": "shell", "arguments": "{\"c\":1}"},
        {"type": "function_call_output", "call_id": "call_cx1", "output": "codex result"}]})

    def codex_spans():
        return [x for x in tool_spans("shell") if span_attrs(x)["gen_ai.tool.call.id"] == "call_cx1"]

    wait_for(codex_spans, "codex tool span")
    cx = codex_spans()
    assert len(cx) == 1 and "codex result" in json.dumps(span_attrs(cx[0])["gen_ai.tool.call.result"]), cx
    assert cx[0]["parent_span_id"], "the Codex issuing call lives in output_item.done and must register as the parent"
    assert cx[0]["duration_ms"] >= TOOL_GAP * 800, ("Codex tool span must measure the client-side gap", cx[0]["duration_ms"])
    print("PASS codex: output_item.done function_call registers the origin, so the tool span has parent and duration")

    assert process.poll() is None, "spanbox exited unexpectedly"
    print("PASS proxy tool spans e2e")
finally:
    if process is not None:
        stop()
    vendor.shutdown()
    log.close()
PY
