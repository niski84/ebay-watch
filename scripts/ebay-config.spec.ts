/**
 * Minimal spec so `npx playwright test` has a target (plex-dashboard uses scripts/demo.spec.ts for demos).
 * Dashboard fetch runs via `node scripts/ebay-search.mjs` from Go, not via this test.
 */
import { test, expect } from '@playwright/test';

test('playwright config resolves', () => {
  expect(true).toBe(true);
});
