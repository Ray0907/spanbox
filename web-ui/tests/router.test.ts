import assert from 'node:assert/strict';
import {test} from 'node:test';
import {legacyHashPath, matchRoute, onLinkClick} from '../src/router.ts';

test('History routes include deep links, /ui alias, and legacy pages', () => {
  for (const [path, page, trace, span] of [
    ['/traces', 'traces'], ['/ui', 'traces'], ['/', 'traces'],
    ['/traces/abc', 'trace', 'abc'], ['/spans/abc/def', 'span', 'abc', 'def'],
    ['/sessions', 'sessions'], ['/search', 'search'], ['/dashboard', 'dashboard'],
    ['/sql', 'sql'], ['/login', 'login'],
  ]) {
    assert.deepEqual(matchRoute(path!), {page, trace, span});
  }
  assert.equal(matchRoute('/export'), null);
  assert.equal(matchRoute('/traces/abc/extra'), null);
});

test('in-app clicks push History; modified, target, download, external and legacy links stay native', () => {
  const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location');
  const originalHistory = Object.getOwnPropertyDescriptor(globalThis, 'history');
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const pushed: string[] = [];
  let dispatched = 0;
  Object.defineProperty(globalThis, 'location', {configurable: true, value: new URL('http://localhost/ui')});
  Object.defineProperty(globalThis, 'history', {configurable: true, value: {pushState: (_: unknown, __: string, url: string) => pushed.push(url)}});
  Object.defineProperty(globalThis, 'window', {configurable: true, value: {dispatchEvent: () => { dispatched++; }}});
  try {
    const click = (props = {}, attributes: Record<string, string> = {}, href = '/traces/abc') => {
      let prevented = false;
      onLinkClick({
        button: 0, defaultPrevented: false, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
        currentTarget: {href: new URL(href, 'http://localhost').href,
          hasAttribute: (key: string) => key in attributes,
          getAttribute: (key: string) => attributes[key] ?? null},
        preventDefault: () => { prevented = true; }, ...props,
      } as unknown as Parameters<typeof onLinkClick>[0]);
      return prevented;
    };
    assert.equal(click(), true);
    assert.deepEqual(pushed, ['/traces/abc']);
    assert.equal(dispatched, 1);
    for (const props of [{button: 1}, {metaKey: true}, {ctrlKey: true}, {shiftKey: true}, {altKey: true}, {defaultPrevented: true}]) {
      assert.equal(click(props), false);
    }
    assert.equal(click({}, {target: '_blank'}), false);
    assert.equal(click({}, {target: '_self'}), false);
    assert.equal(click({}, {download: ''}), false);
    for (const href of ['https://example.org/traces/abc', '/export', '/traces?format=json', '/traces#section']) {
      assert.equal(click({}, {}, href), false, href);
    }
    for (const href of ['/sessions', '/sessions?id=a%2Fb', '/search', '/search?q=weather', '/dashboard', '/sql', '/login', '/']) {
      assert.equal(click({}, {}, href), true, href);
      assert.equal(pushed.at(-1), href);
    }
    assert.equal(pushed.length, 9);
  } finally {
    for (const [key, descriptor] of [['location', originalLocation], ['history', originalHistory], ['window', originalWindow]] as const) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else Reflect.deleteProperty(globalThis, key);
    }
  }
});

test('legacy /ui#/t/<id> maps to the trace path', () => {
  assert.equal(legacyHashPath('/ui', '#/t/abc'), '/traces/abc');
  assert.equal(legacyHashPath('/ui', ''), null);
  assert.equal(legacyHashPath('/traces', '#/t/abc'), null);
});
