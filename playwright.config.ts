import { defineConfig } from '@playwright/test';

/** Same layout as plex-dashboard: specs under ./scripts, long timeout for real network. */
export default defineConfig({
  testDir: './scripts',
  testMatch: '**/*.spec.ts',
  timeout: 120_000,
  use: {
    headless: true,
    viewport: { width: 1280, height: 900 },
  },
  reporter: [['list']],
});
