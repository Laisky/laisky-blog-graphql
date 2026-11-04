import { resolve } from 'node:path';

import { defineConfig, loadEnv, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { sentryVitePlugin } from '@sentry/vite-plugin';
import { finalizePrivateSourceMaps } from './scripts/finalize-private-sourcemaps.mjs';

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const backendUrl = env.VITE_BACKEND_URL || 'http://localhost:17800';
  const privateSourceMaps = process.env.SENTRY_BUILD_PRIVATE_SOURCEMAPS === 'true';
  let outputDirectory = resolve(__dirname, 'dist');
  const privateSourceMapQuarantine: Plugin = {
    name: 'private-source-map-quarantine',
    apply: 'build',
    configResolved(config) { outputDirectory = resolve(config.root, config.build.outDir); },
    closeBundle: {
      order: 'post',
      sequential: true,
      async handler() {
        const artifact = await finalizePrivateSourceMaps({ enabled: privateSourceMaps, distDir: outputDirectory });
        if (artifact) console.log(`Private source maps retained at ${artifact}`);
      },
    },
  };

  return {
    base: '/',
    plugins: [react(), ...(privateSourceMaps ? [sentryVitePlugin({
      sourcemaps: { disable: 'disable-upload' },
      telemetry: false,
      release: { inject: false, create: false, finalize: false },
    })] : []), privateSourceMapQuarantine],
    build: { sourcemap: privateSourceMaps ? 'hidden' : false },
    resolve: {
      alias: {
        '@': resolve(__dirname, 'src'),
      },
    },
    server: {
      proxy: {
        '/query': backendUrl,
        '/graphql': backendUrl,
        '/runtime-config.json': backendUrl,
        '/mcp': backendUrl,
        '/ui': backendUrl,
        '/status': backendUrl,
        '/health': backendUrl,
        '/tools/ask_user/api': backendUrl,
        '/tools/get_user_requests/api': backendUrl,
        '/tools/call_log/api': backendUrl,
      },
    },
  };
});
