import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { playwright } from '@vitest/browser-playwright';
import { defineConfig } from 'vitest/config';

// Real-browser tests (`*.browser.test.tsx`) cover what jsdom cannot: layout, scrolling and the
// browser's own focus handling. They run in a locally installed Google Chrome (preinstalled on
// GitHub-hosted Ubuntu runners), so no browser download is needed; set VITEST_BROWSER_CHANNEL to
// pick another Playwright channel, e.g. `chromium` after `pnpm exec playwright install chromium`.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  test: {
    include: ['src/**/*.browser.test.{ts,tsx}'],
    setupFiles: ['./src/test-utils/setup.ts'],
    restoreMocks: true,
    browser: {
      enabled: true,
      headless: true,
      provider: playwright({ launchOptions: { channel: process.env.VITEST_BROWSER_CHANNEL || 'chrome' } }),
      instances: [{ browser: 'chromium' }],
      viewport: { width: 1280, height: 720 },
    },
  },
});
