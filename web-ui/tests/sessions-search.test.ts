import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';

test('info Banner colors are aliases of the approved spanbox palette', () => {
  const css = readFileSync(new URL('../src/spanbox.css', import.meta.url), 'utf8');
  assert.match(css, /--color-info-muted:\s*var\(--color-accent-muted\)/);
  assert.match(css, /--color-on-info-muted:\s*var\(--color-text-accent\)/);
});

test('session query mapping retains range, bounds and cursor; old HTML always requests 50', async () => {
  const {sessionListQuery, sessionDetailQuery, sessionNextURL} = await import('../src/sessions.ts');
  assert.equal(sessionListQuery('?range=custom&from=A&to=B&cursor=9%3Aa%2Fb&limit=1&junk=x').toString(), 'range=custom&from=A&to=B&cursor=9%3Aa%2Fb&format=json&limit=50');
  assert.equal(sessionDetailQuery('a/b &中'), '/sessions?id=a%2Fb+%26%E4%B8%AD&format=json');
  assert.equal(sessionNextURL('?range=7d&cursor=old', '10:a/b'), '/sessions?range=7d&cursor=10%3Aa%2Fb');
  assert.equal(sessionNextURL('', null), null);
});

test('turn labels use server numbering, never count orphan traces as turns', async () => {
  const {turnLabel, countLabel} = await import('../src/sessions.ts');
  assert.equal(turnLabel({has_root: false, number: 0, name: 'execute_tool lost'}), 'execute_tool lost');
  assert.equal(turnLabel({has_root: true, number: 2, name: 'agent'}), 'Turn 2');
  assert.equal(countLabel(1, 'turn'), '1 turn');
  assert.equal(countLabel(0, 'trace'), '0 traces');
  assert.equal(countLabel(2, 'span'), '2 spans');
});

test('session metadata uses compact durations and UTC time', async () => {
  const {compactDuration, sessionTime} = await import('../src/sessions.ts');
  for (const [ms, label] of [[0, '0ms'], [1, '1ms'], [999, '999ms'], [1000, '1.0s'], [1250, '1.3s']] as const) {
    assert.equal(compactDuration(ms), label);
  }
  assert.equal(sessionTime(1_234_567_890), '1970-01-01T00:00:01.234Z');
});

test('search preserves literal q, ignores old-HTML-ignored parameters and caps at 100 recent hits', async () => {
  const {searchQuery, searchPageQuery} = await import('../src/search.ts');
  assert.equal(searchQuery('?q=%22a%26b%22+%E4%B8%AD&limit=1&sort=relevance&cursor=ignored'), '"a&b" 中');
  assert.equal(searchQuery(''), '');
  assert.equal(searchQuery('?q='), '');
  assert.equal(searchQuery('?q=+++'), '   ');
  assert.equal(searchPageQuery('a&b', null, 0), '/search?q=a%26b&format=json&limit=50');
  assert.equal(searchPageQuery('a&b', '42:a/b', 50), '/search?q=a%26b&format=json&limit=50&cursor=42%3Aa%2Fb');
  assert.equal(searchPageQuery('x', 'next', 75), '/search?q=x&format=json&limit=25&cursor=next');
  assert.equal(searchPageQuery('x', 'next', 100), null);
  assert.equal(searchPageQuery('x', null, 50), null);
  assert.equal(searchPageQuery('', null, 0), null);
});

test('snippet parts: [match] markers become matched parts, stray brackets stay text', async () => {
  const {snippetParts} = await import('../src/search.ts');
  const join = (parts: {text: string; match: boolean}[]) => parts.map(part => part.text).join('');
  assert.deepEqual(snippetParts('a [refund] b'), [{text: 'a ', match: false}, {text: 'refund', match: true}, {text: ' b', match: false}]);
  assert.deepEqual(snippetParts('[x][y]'), [{text: 'x', match: true}, {text: 'y', match: true}]);
  assert.deepEqual(snippetParts('no markers'), [{text: 'no markers', match: false}]);
  assert.deepEqual(snippetParts(''), []);
  for (const raw of ['stray ] and [open', 'a [b [c] d', 'x[0] ]', '[[nested]]', '…[é中]…']) {
    const parts = snippetParts(raw);
    assert.equal(join(parts) .length <= raw.length, true);
    assert.equal(parts.every(part => part.text !== ''), true);
  }
  assert.deepEqual(snippetParts('stray ] and [open'), [{text: 'stray ] and [open', match: false}]);
  assert.deepEqual(snippetParts('a [b [c] d'), [{text: 'a [b ', match: false}, {text: 'c', match: true}, {text: ' d', match: false}]);
  assert.deepEqual(snippetParts('…[é中]…'), [{text: '…', match: false}, {text: 'é中', match: true}, {text: '…', match: false}]);
});

test('sessions Apply URL keeps range/from/to exactly as before and takes picker values as UTC in any time zone', async () => {
  const {sessionFiltersToSearch} = await import('../src/sessions.ts');
  const {filtersFromSearch, fromPickerValue, toPickerValue} = await import('../src/traces.ts');
  const zone = process.env.TZ;
  try {
    for (const tz of ['UTC', 'Asia/Taipei', 'America/Los_Angeles']) {
      process.env.TZ = tz;
      const draft = {...filtersFromSearch(''), range: 'custom', from: fromPickerValue('2026-09-15T10:30' as never), to: fromPickerValue('2026-09-15T10:45' as never)};
      assert.equal(sessionFiltersToSearch(draft), 'range=custom&from=2026-09-15T10%3A30%3A00Z&to=2026-09-15T10%3A45%3A00Z');
    }
  } finally {
    process.env.TZ = zone;
  }
  assert.equal(sessionFiltersToSearch(filtersFromSearch('')), 'range=&from=&to=');
  assert.equal(sessionFiltersToSearch({...filtersFromSearch(''), range: '7d'}), 'range=7d&from=&to=');
});

test('sessions URLs from before the picker load into it and an untouched draft is sent back byte for byte', async () => {
  const {sessionFiltersToSearch} = await import('../src/sessions.ts');
  const {filtersFromSearch, toPickerValue} = await import('../src/traces.ts');
  for (const [from, to, shownFrom, shownTo] of [
    ['2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-01T00:00', '2026-01-02T00:00'],
    ['2026-01-01T08:30:00+08:00', '2026-01-02T23:59:59Z', '2026-01-01T00:30', '2026-01-02T23:59'],
    ['1970-01-01T00:00:00Z', '1970-01-02T00:00:00Z', '1970-01-01T00:00', '1970-01-02T00:00'],
  ]) {
    const draft = filtersFromSearch('?' + new URLSearchParams({range: 'custom', from, to}));
    assert.equal(toPickerValue(draft.from), shownFrom);
    assert.equal(toPickerValue(draft.to), shownTo);
    assert.equal(sessionFiltersToSearch(draft), new URLSearchParams({range: 'custom', from, to}).toString());
  }
  assert.equal(toPickerValue(filtersFromSearch('?range=custom&from=garbage').from), undefined);
});
