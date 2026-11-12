import test from 'node:test';
import assert from 'node:assert/strict';
import {getJSON} from '../src/http.ts';

test('getJSON keeps HTTP status in a typed error independently of the response text', async t => {
  for (const status of [404, 500]) {
    t.mock.method(globalThis, 'fetch', async () => new Response('arbitrary response text', {status}));
    await assert.rejects(getJSON('/sessions?id=missing'), {name: 'HTTPError', status, message: `${status} arbitrary response text`});
    t.mock.restoreAll();
  }
});

test('getJSON keeps JSON requests, capped messages, and network errors intact', async t => {
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.equal(url, '/data');
    assert.equal(options?.headers?.Accept, 'application/json');
    return Response.json({items: []});
  });
  assert.deepEqual(await getJSON('/data'), {items: []});
  t.mock.restoreAll();
  t.mock.method(globalThis, 'fetch', async () => new Response('x'.repeat(200), {status: 503}));
  await assert.rejects(getJSON('/data'), {message: '503 ' + 'x'.repeat(120)});
  t.mock.restoreAll();
  const network = new TypeError('network unavailable');
  t.mock.method(globalThis, 'fetch', async () => { throw network; });
  await assert.rejects(getJSON('/data'), error => error === network);
});
