import { existsSync } from 'node:fs';
import { resolve } from 'node:path';

import { defineConfig, loadEnv, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { sentryVitePlugin } from '@sentry/vite-plugin';
import { finalizePrivateSourceMaps } from './scripts/finalize-private-sourcemaps.mjs';

const reactVendorModule = /[\\/]node_modules[\\/](?:react|react-dom|scheduler|react-router|react-router-dom|cookie)[\\/]/;

// Build output names are a contract with internal/web/spa.go (fingerprintedAssetName): the Go
// server sends a one-year immutable Cache-Control only for "assets/<name>-<8-char hash><ext>".
// Keep the Rollup output names below, this pattern and the Go pattern in sync.
const hashedAssetName = /^assets\/(?:[^/]+\/)*[^/]*[^/-]-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+(?:\.map)?$/;

/**
 * fingerprintedAssetContract fails the build when the immutable-cache contract would be
 * broken: a public/assets directory would put stable-named files next to hashed bundles,
 * and an emitted file under assets/ without the expected hash would silently lose caching.
 */
const fingerprintedAssetContract: Plugin = {
  name: 'fingerprinted-asset-contract',
  apply: 'build',
  configResolved(config) {
    if (config.publicDir && existsSync(resolve(config.publicDir, config.build.assetsDir))) {
      throw new Error(`public/${config.build.assetsDir}/ is reserved for hashed build output; move those files elsewhere in public/`);
    }
  },
  generateBundle(_options, bundle) {
    const unhashed = Object.keys(bundle).filter((name) => name.startsWith('assets/') && !hashedAssetName.test(name));
    if (unhashed.length > 0) {
      this.error(`build output under assets/ must carry an 8-character content hash (see internal/web/spa.go): ${unhashed.join(', ')}`);
    }
  },
};

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
    })] : []), fingerprintedAssetContract, privateSourceMapQuarantine],
    build: {
      sourcemap: privateSourceMaps ? 'hidden' : false,
      rollupOptions: {
        output: {
          // Content-hashed names let internal/web/spa.go cache these files as immutable.
          entryFileNames: 'assets/[name]-[hash:8].js',
          chunkFileNames: 'assets/[name]-[hash:8].js',
          assetFileNames: 'assets/[name]-[hash:8][extname]',
          // Keep the React runtime and router in their own long-lived chunk so a deploy
          // that only changes application code does not invalidate them in the cache.
          manualChunks(id) {
            return reactVendorModule.test(id) ? 'vendor-react' : undefined;
          },
        },
      },
    },
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
