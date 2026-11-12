const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

module.exports = function ({root, base, token, report}) {
  return async function seed() {
    const fixture = JSON.parse(fs.readFileSync(path.join(root, 'internal/otlp/testdata/trace.json')));
    const now = BigInt(Date.now()) * 1000000n;
    for (let index = 0; index < 2; index++) {
      const data = structuredClone(fixture);
      const resource = data.resourceSpans[0];
      const scope = resource.scopeSpans[0];
      const spans = scope.spans;
      const long = field => `What is the weather? ${'😀中'.repeat(2200)} END-${field}`;
      resource.resource.attributes.push({key: 'long.resource', value: {stringValue: long('resource')}});
      scope.scope.attributes.push({key: 'long.scope', value: {stringValue: long('scope')}});
      const llm = spans[1];
      llm.attributes.find(attribute => attribute.key === 'gen_ai.input.messages').value.stringValue = JSON.stringify([{role: 'user', content: long('input')}]);
      llm.attributes.push({key: 'gen_ai.output.messages', value: {stringValue: JSON.stringify([{role: 'assistant', content: long('output')}])}});
      llm.attributes.push({key: 'long.attributes', value: {stringValue: long('attributes')}});
      llm.events[0].attributes.push({key: 'long.events', value: {stringValue: long('events')}});
      llm.links[0].attributes.push({key: 'long.links', value: {stringValue: long('links')}});
      if (index === 1) spans[0].status = {code: 'STATUS_CODE_ERROR', message: 'test error'};
      for (const span of spans) {
        span.traceId = Buffer.alloc(16, index + 1).toString('base64');
        span.startTimeUnixNano = String(now + BigInt(span.startTimeUnixNano) - 1300000000n);
        span.endTimeUnixNano = String(now + BigInt(span.endTimeUnixNano) - 1300000000n);
        span.attributes.push({key: 'gen_ai.conversation.id', value: {stringValue: `mobile-session-${index + 1}`}});
        span.attributes.push({key: 'user.id', value: {stringValue: `mobile-user-${index + 1}`}});
        for (const event of span.events || []) event.timeUnixNano = String(now + BigInt(event.timeUnixNano) - 1300000000n);
      }
      const response = await fetch(`${base}/v1/traces`, {
        method: 'POST', headers: {Authorization: `Bearer ${token}`, 'Content-Type': 'application/json'}, body: JSON.stringify(data)
      });
      assert.equal(response.status, 200, await response.text());
    }
    report('PASS seed — two real OTLP traces, three spans each, two sessions');
  };
};
