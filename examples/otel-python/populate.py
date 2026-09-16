"""Populate a running spanbox with one week of varied demo traces (support agent, four models, tools, retrieval, some errors).

    SPANBOX_URL=http://localhost:4318 AUTH_TOKEN=... python populate.py
"""
import os, time, json, random
from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.trace import StatusCode
random.seed(7)
tp = TracerProvider(resource=Resource.create({"service.name": "support-agent"}))
tp.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=os.environ.get("SPANBOX_URL", "http://localhost:4318").rstrip("/") + "/v1/traces", headers={"Authorization": "Bearer " + os.environ["AUTH_TOKEN"]} if os.environ.get("AUTH_TOKEN") else {}), max_queue_size=65536))
tr = tp.get_tracer("demo")
models = [("openai","gpt-4o","gpt-4o-2024-08-06",2.5,10),("anthropic","claude-sonnet-4-5","claude-sonnet-4-5",3,15),("openai","gpt-4o-mini","gpt-4o-mini-2024-07-18",0.15,0.6),("gcp.gemini","gemini-2.5-flash","gemini-2.5-flash",0.3,2.5)]
qs = ["How do I reset my password?","Refund for order #4821 never arrived","Can I change the shipping address after checkout?","為什麼我的發票金額和訂單不一樣？","Cancel my subscription please","API returns 429 since this morning, what changed?"]
NS = 10**9
now = time.time_ns()
for day in range(6, -1, -1):
    for k in range(random.randint(18, 40)):
        t0 = now - day*86400*NS - random.randint(0, 86000)*NS
        prov, req, resp, pin, pout = random.choice(models)
        q = random.choice(qs); conv = f"conv-{random.randint(100,140)}"; user = random.choice(["alice","bob","chen","ray"])
        err = random.random() < 0.06
        root = tr.start_span("invoke_agent support", start_time=t0, attributes={"gen_ai.operation.name":"invoke_agent","gen_ai.agent.name":"support","gen_ai.conversation.id":conv,"user.id":user})
        ctx = trace.set_span_in_context(root)
        t = t0 + random.randint(5, 40)*10**6
        # retrieval
        r = tr.start_span("retrieval kb", context=ctx, start_time=t, attributes={"gen_ai.operation.name":"retrieval","gen_ai.input.messages":q})
        t += random.randint(40, 180)*10**6; r.end(end_time=t)
        # llm turns
        for turn in range(random.randint(1, 3)):
            ti = random.randint(600, 4000); to = random.randint(40, 600)
            s = tr.start_span(f"chat {req}", context=ctx, start_time=t, attributes={
                "gen_ai.operation.name":"chat","gen_ai.provider.name":prov,"gen_ai.request.model":req,"gen_ai.response.model":resp,
                "gen_ai.usage.input_tokens":ti,"gen_ai.usage.output_tokens":to,"gen_ai.usage.cache_read.input_tokens":random.choice([0,0,ti//3]),
                "gen_ai.input.messages":json.dumps([{"role":"system","parts":[{"type":"text","content":"You are a concise support agent for an e-commerce store."}]},{"role":"user","parts":[{"type":"text","content":q}]}]),
                "gen_ai.output.messages":json.dumps([{"role":"assistant","parts":[{"type":"text","content":"I checked your account. "+random.choice(["A reset link is on its way.","The refund was issued today; allow 3-5 business days.","Address updated before dispatch.","Your plan is cancelled effective end of period."])}]}]),
                "gen_ai.response.finish_reasons":["stop"]})
            t += random.randint(400, 6000)*10**6
            if err and turn == 0:
                s.set_status(StatusCode.ERROR, "rate limited: 429 Too Many Requests"); s.record_exception(RuntimeError("429"))
            s.end(end_time=t)
            if random.random() < 0.5:
                tool = random.choice(["lookup_order","issue_refund","update_address"])
                x = tr.start_span(f"execute_tool {tool}", context=ctx, start_time=t, attributes={"gen_ai.operation.name":"execute_tool","gen_ai.tool.name":tool,"gen_ai.tool.call.id":f"call_{random.randint(1000,9999)}","gen_ai.tool.call.arguments":json.dumps({"order_id":4821}),"gen_ai.tool.call.result":json.dumps({"status":"ok"})})
                t += random.randint(30, 900)*10**6; x.end(end_time=t)
        if err: root.set_status(StatusCode.ERROR, "upstream failure")
        root.end(end_time=t + 5*10**6)
tp.force_flush(); tp.shutdown(); print("populated")
