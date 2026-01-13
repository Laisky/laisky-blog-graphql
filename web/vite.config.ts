import { resolve } from 'node:path';

import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const backendUrl = env.VITE_BACKEND_URL || 'http://localhost:17800';

  return {
    base: '/',
    plugins: [react()],
    resolve: {
      alias: {
        '@': resolve(__dirname, 'src'),
      },
    },
    server: {
      proxy: {
        '/query': backendUrl,
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
