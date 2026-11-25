import { resolve } from 'node:path';
import { configDefaults, defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    // Real-browser tests run with `pnpm run test:browser` (vitest.browser.config.ts).
    exclude: [...configDefaults.exclude, 'src/**/*.browser.test.{ts,tsx}'],
    restoreMocks: true,
    setupFiles: ['./src/test-utils/setup.ts'],
  },
});
