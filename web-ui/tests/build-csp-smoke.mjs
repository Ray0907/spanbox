import {mkdirSync, readFileSync, realpathSync, writeFileSync} from 'node:fs';
import {dirname, relative, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {build} from 'vite';
import react from '@vitejs/plugin-react';
import {astryxStylex} from '@astryxdesign/build/vite';

const project = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const temporary = resolve(process.argv[2]);
const root = resolve(temporary, 'source');
mkdirSync(root, {recursive: true});
const entry = relative(realpathSync(root), resolve(project, 'tests/csp-smoke.tsx'));
writeFileSync(resolve(root, 'index.html'), `<!doctype html><html><head><meta charset="UTF-8"></head><body><div id="root"></div><script type="module" src="${entry}"></script></body></html>`);
const shell = readFileSync(resolve(project, '../internal/web/static/ui/index.html'), 'utf8');
const link = shell.match(/<link\b[^>]*id="astryx-layer-slide-rules"[^>]*>/)?.[0];
if (!link) throw new Error('Build the production Layer stylesheet first');
await build({
  configFile: false, root, publicDir: resolve(project, 'public'), base: '/static/ui/csp-smoke/',
  plugins: [...astryxStylex({rootDir: project}), react(), {
    name: 'scratch-strict-csp',
    transformIndexHtml: {order: 'post', handler: html => html.replace(/\s*<style>@layer[^<]*<\/style>/, '').replace('<head>', `<head>${link}`)},
  }],
  build: {outDir: resolve(temporary, 'dist'), emptyOutDir: true},
});
