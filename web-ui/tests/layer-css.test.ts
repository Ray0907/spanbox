import test from 'node:test';
import assert from 'node:assert/strict';
import {loadLayerCSS} from '../scripts/layer-css.ts';

test('Layer CSS is evaluated from installed core with StyleX resolved and is deterministic', async () => {
  const first = await loadLayerCSS();
  assert.match(first, /@position-try/);
  assert.match(first, /--spacing-4/);
  assert.equal(first.includes('undefined'), false);
  assert.equal(await loadLayerCSS(), first);
});
