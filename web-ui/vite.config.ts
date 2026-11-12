import {defineConfig, type Plugin} from 'vite';
import {createHash} from 'node:crypto';
import {resolve} from 'node:path';
import {loadLayerCSS} from './scripts/layer-css.ts';
import react from '@vitejs/plugin-react';
import {astryxStylex} from '@astryxdesign/build/vite';
import {readFileSync, readdirSync, renameSync, writeFileSync} from 'node:fs';

// spanbox serves `style-src 'self'`, so the inline <style> that the Astryx plugin prepends to
// index.html (the @layer order) is blocked. The same declaration is the first import in
// src/layers.css, so the tag can go.
const strictCsp = (): Plugin => {
  let layerCSSFile = '';
  let layerCSS = '';
  let outDir = '';
  return {
    name: 'spanbox-strict-csp',
    apply: 'build',
    configResolved(config) { outDir = resolve(config.root, config.build.outDir); },
    async buildStart() {
      layerCSS = await loadLayerCSS();
      const hash = createHash('sha256').update(layerCSS).digest('hex').slice(0, 12);
      layerCSSFile = `assets/astryx-layer-slide-rules-${hash}.min.css`;
    },
    // Keep the exact generated text out of Vite's CSS merging/renaming pipeline.
    closeBundle: {
      sequential: true,
      handler() { writeFileSync(resolve(outDir, layerCSSFile), layerCSS); },
    },
    transformIndexHtml: {
      order: 'post',
      handler: html => ({
        html: html.replace(/\s*<style>@layer[^<]*<\/style>/, ''),
        tags: [{tag: 'link', attrs: {rel: 'stylesheet', id: 'astryx-layer-slide-rules', href: `/static/ui/${layerCSSFile}`}, injectTo: 'head-prepend'}],
      }),
    },
  };
};

const minCSSNames = (): Plugin => ({
  name: 'spanbox-min-css-names',
  apply: 'build',
  closeBundle() {
    // Astryx writes its shared CSS after Rollup's asset naming has run.
    const dist = new URL('../internal/web/static/ui/', import.meta.url);
    const index = new URL('index.html', dist);
    let html = readFileSync(index, 'utf8');
    for (const name of readdirSync(new URL('assets/', dist))) {
      if (!name.endsWith('.css') || name.endsWith('.min.css')) continue;
      const minName = name.replace(/\.css$/, '.min.css');
      renameSync(new URL(`assets/${name}`, dist), new URL(`assets/${minName}`, dist));
      html = html.replaceAll(name, minName);
    }
    writeFileSync(index, html);
  },
});

// The build is embedded into the Go binary (internal/web/static/ui) and served at /ui.
export default defineConfig({
  base: '/static/ui/',
  plugins: [...astryxStylex(), react(), strictCsp(), minCSSNames()],
  build: {
    outDir: '../internal/web/static/ui',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name]-[hash].min.js',
        chunkFileNames: 'assets/[name]-[hash].min.js',
        assetFileNames: 'assets/[name]-[hash].min[extname]',
      },
    },
  },
});
