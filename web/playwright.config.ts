import { defineConfig, devices } from '@playwright/test';

/**
 * Responsive and access-control tests against a real browser.
 *
 * These exist because the two classes of bug they catch are invisible to `tsc`
 * and to review: a page that pans sideways on a phone, and a surface a role
 * should not be able to reach. Both were found by hand and both would come back
 * silently.
 *
 * The dev server is reused when one is already running, so a local run does not
 * fight the server you are looking at. The API must be up separately — these are
 * not mocked, deliberately: a responsive test against a skeleton screen proves
 * nothing about the page with data in it.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  /* One worker: the full Chromium (rather than the headless shell) is heavy
     enough that five concurrent launches exceed the 180s launch timeout on a
     loaded machine. These tests are fast; serial is fine. */
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'line' : [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:5174',
    trace: 'retain-on-failure',
    /* The full Chromium rather than the headless shell. The shell is a separate
       download and is not always present in a cached environment; the full
       browser runs the same new-headless mode and is what CI images already
       carry. */
    channel: 'chromium',
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } } },
    { name: 'laptop', use: { ...devices['Desktop Chrome'], viewport: { width: 1024, height: 768 } } },
    { name: 'tablet', use: { ...devices['Desktop Chrome'], viewport: { width: 768, height: 1024 } } },
    { name: 'phone', use: { ...devices['Pixel 7'] } },
    // 320px is the narrowest viewport worth supporting and the one that breaks.
    { name: 'narrow', use: { ...devices['Desktop Chrome'], viewport: { width: 320, height: 700 } } },
  ],
  webServer: {
    command: 'npm run dev',
    url: 'http://localhost:5174',
    reuseExistingServer: true,
    timeout: 60_000,
  },
});
