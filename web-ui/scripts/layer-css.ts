import {createRequire} from 'node:module';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'vite';
import {astryxStylex} from '@astryxdesign/build/vite';

export async function loadLayerCSS(): Promise<string> {
  const require = createRequire(import.meta.url);
  const entry = resolve(dirname(require.resolve('@astryxdesign/core/Layer')), '../../src/Layer/layerSlideRules.ts');
  const root = fileURLToPath(new URL('..', import.meta.url));
  const server = await createServer({
    configFile: false,
    root,
    // Only SSR transforms are needed; dev HMR hooks start a persistent StyleX polling timer.
    plugins: astryxStylex({rootDir: root}).map(plugin => ({...plugin, configureServer: undefined})),
    server: {middlewareMode: true, hmr: false, watch: null},
    appType: 'custom',
    ssr: {noExternal: ['@astryxdesign/core']},
  });
  try {
    const {slideRulesCss} = await server.ssrLoadModule(entry);
    return slideRulesCss();
  } finally {
    await server.close();
  }
}
