import { defineConfig, devices } from '@playwright/test'
const live = !!process.env.E2E_BASE_URL
export default defineConfig({
  testDir: './tests/e2e',
  testMatch: live ? 'live.spec.ts' : 'ui.spec.ts',
  timeout: 45000,
  expect: { timeout: 10000 },
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list']],
  use: { baseURL: process.env.E2E_BASE_URL || 'http://127.0.0.1:15173', trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: live ? undefined : { command: 'npm run preview -- --host 127.0.0.1 --port 15173 --strictPort', url: 'http://127.0.0.1:15173', reuseExistingServer: false, timeout: 30000 },
})
