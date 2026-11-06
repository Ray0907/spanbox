import {defineConfig, type Plugin} from 'vite';
import react from '@vitejs/plugin-react';
import {astryxStylex} from '@astryxdesign/build/vite';

// spanbox serves `style-src 'self'`, so the inline <style> that the Astryx plugin prepends to
// index.html (the @layer order) is blocked. The same declaration is the first import in
// src/layers.css, so the tag can go.
const strictCsp = (): Plugin => ({
  name: 'spanbox-strict-csp',
  transformIndexHtml: {
    order: 'post',
    handler: (html) => html.replace(/\s*<style>@layer[^<]*<\/style>/, ''),
  },
});

// The build is embedded into the Go binary (internal/web/static/ui) and served at /ui.
export default defineConfig({
  base: '/static/ui/',
  plugins: [...astryxStylex(), react(), strictCsp()],
  build: {outDir: '../internal/web/static/ui', emptyOutDir: true},
});
