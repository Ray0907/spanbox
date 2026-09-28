## spanbox skill E2E (bash)

ready: {"n":1,"next":true}
rung1 errors=1: ["00000000000000000000000000000003"]
rung2 search: [{"trace_id":"00000000000000000000000000000003","span_id":"0000000000000067"}]
rung3 outline: [{"span_id":"0000000000000003","kind":"agent","status_code":0,"output_chars":0},{"span_id":"0000000000000067","kind":"llm","status_code":2,"output_chars":6075}]
rung4 head: output total_chars=6075 text=2000 next=2000
rung5 paged output: pages=3 chars=6075 ends_with_error=yes
bad token: {"error":"unauthorized"}

PASS
