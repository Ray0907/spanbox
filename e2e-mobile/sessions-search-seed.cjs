const assert = require('node:assert/strict');

module.exports = async function ({base, token}) {
  const spans = [];
  const now = BigInt(Date.now()) * 1000000n;
  const attr = (key, value) => ({key, value: typeof value === 'number' ? {intValue: String(value)} : {stringValue: value}});
  const id = (n, bytes) => n.toString(16).padStart(bytes * 2, '0');
  const add = (n, session, kind, prompt, output, offset = n) => {
    spans.push({traceId: id(1000 + n, 16), spanId: id(1000 + n, 8), name: kind === 'invoke_agent' ? 'agent parity' : 'orphan tool',
      startTimeUnixNano: String(now + BigInt(offset) * 1000000n), endTimeUnixNano: String(now + BigInt(offset + 1) * 1000000n),
      attributes: [attr('gen_ai.conversation.id', session), attr('gen_ai.operation.name', kind),
        attr('input.value', prompt), attr('gen_ai.output.messages', output),
        attr('gen_ai.usage.input_tokens', 10), attr('gen_ai.usage.output_tokens', 2), attr('gen_ai.request.model', 'gpt-4o')],
      ...(n === 2 ? {status: {code: 'STATUS_CODE_ERROR', message: 'parity error'}} : {})});
    if (n <= 3) {
      const root = spans.at(-1);
      spans.push({...root, spanId: id(10000 + n, 8), parentSpanId: root.spanId, name: 'chat gpt-4o', status: {code: 'STATUS_CODE_OK'},
        attributes: root.attributes.map(a => a.key === 'gen_ai.operation.name' ? attr(a.key, 'chat') : a)});
    }
  };
  const session = 'parity/with slash &中';
  // Insert backwards; both store and UI must retain chronological order and orphan numbering.
  add(3, session, 'invoke_agent', 'third prompt', 'third output');
  add(2, session, 'invoke_agent', 'second <script>x</script>', 'second output');
  add(1, session, 'invoke_agent', 'first prompt', 'first output');
  add(0, session, 'execute_tool', 'orphan prompt', 'orphan output');
  add(4, 'parity-singular', 'invoke_agent', 'x'.repeat(599) + 'yz' + 'w'.repeat(100), 'singular output');
  // No usage or price on this trace: unknown totals must remain unknown.
  spans.at(-1).attributes = spans.at(-1).attributes.filter(a => !a.key.startsWith('gen_ai.usage.') && a.key !== 'gen_ai.request.model');
  for (let n = 0; n < 201; n++) add(10 + n, 'parity-truncated', 'invoke_agent', `turn prompt ${n}`, `turn output ${n}`);
  for (let n = 0; n < 51; n++) add(220 + n, `parity-page-${n}`, 'invoke_agent', 'page prompt', 'page output');
  for (let n = 0; n < 102; n++) add(300 + n, 'parity-search', 'chat', `needle-literal a&b 中 ${n}`, `search output ${n}`);
  const response = await fetch(`${base}/v1/traces`, {method: 'POST', headers: {Authorization: `Bearer ${token}`, 'Content-Type': 'application/json'},
    body: JSON.stringify({resourceSpans: [{resource: {attributes: [attr('service.name', 'parity-service')]}, scopeSpans: [{spans}]}]})});
  assert.equal(response.status, 200, await response.text());
  return session;
};
