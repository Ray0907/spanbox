"""Emit synthetic GenAI spans to spanbox with the real OpenTelemetry Python SDK.

    pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http
    SPANBOX_URL=http://localhost:4318 AUTH_TOKEN=... python emit.py 10
"""
import os, time, json, sys
from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

N = int(sys.argv[1]) if len(sys.argv) > 1 else 10
url = os.environ.get("SPANBOX_URL", "http://localhost:4318").rstrip("/") + "/v1/traces"
tok = os.environ.get("AUTH_TOKEN", "")
exp = OTLPSpanExporter(endpoint=url, headers={"Authorization": f"Bearer {tok}"} if tok else {})
tp = TracerProvider(resource=Resource.create({"service.name": "real-sdk"}))
tp.add_span_processor(BatchSpanProcessor(exp, max_export_batch_size=512, max_queue_size=65536, schedule_delay_millis=200))  # default queue 2048 drops spans under bursts
tr = tp.get_tracer("emit", "0.1")
msgs = json.dumps([{"role":"user","parts":[{"type":"text","content":"hello from real sdk 你好"}]}])
t0 = time.time()
for i in range(N):
    with tr.start_as_current_span("invoke_agent demo", attributes={"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"demo","gen_ai.conversation.id":f"conv-{i%50}","user.id":"ray"}):
        with tr.start_as_current_span("chat gpt-4o", attributes={
            "gen_ai.operation.name":"chat","gen_ai.provider.name":"openai","gen_ai.request.model":"gpt-4o",
            "gen_ai.response.model":"gpt-4o-2024-08-06","gen_ai.usage.input_tokens":120+i%7,"gen_ai.usage.output_tokens":30,
            "gen_ai.usage.cache_read.input_tokens":20,"gen_ai.input.messages":msgs,"gen_ai.output.messages":json.dumps([{"role":"assistant","parts":[{"type":"text","content":"hi"}]}]),
            "gen_ai.response.finish_reasons":["stop"]}) as s:
            s.add_event("gen_ai.client.inference.operation.details", {"x": 1})
        with tr.start_as_current_span("execute_tool search", attributes={"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":"search","gen_ai.tool.call.id":f"call_{i}","gen_ai.tool.call.arguments":"{\"q\":\"x\"}","gen_ai.tool.call.result":"[]"}):
            pass
tp.force_flush(); tp.shutdown()
print(f"emitted {N} traces ({N*3} spans) in {time.time()-t0:.2f}s")
