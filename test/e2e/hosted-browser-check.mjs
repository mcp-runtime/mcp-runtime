import { mkdir } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

const required = ['PLAYWRIGHT_MODULE', 'BROWSER_EVIDENCE_DIR', 'QA_NAME', 'QA_ADMIN_EMAIL', 'QA_ADMIN_PASSWORD'];
for (const name of required) {
  if (!process.env[name]) throw new Error(`${name} is required`);
}

const { chromium } = await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
const evidenceDir = process.env.BROWSER_EVIDENCE_DIR;
const qaName = process.env.QA_NAME;
await mkdir(evidenceDir, { recursive: true });

const browser = await chromium.launch({ headless: true });
try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await context.newPage();
  const responses = [];
  const pageErrors = [];
  const consoleErrors = [];
  page.on('response', (response) => {
    const url = new URL(response.url());
    if (url.pathname.startsWith('/auth/') || url.pathname.startsWith('/api/ui/')) {
      responses.push({ path: url.pathname, status: response.status() });
    }
  });
  page.on('pageerror', (error) => pageErrors.push(error.name));
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push('console error');
  });

  await page.goto('https://platform.mcpruntime.org/#/admin/analytics', { waitUntil: 'domcontentloaded' });
  // The app redirects unauthorized admin routes to the public Servers page.
  const signedOut = page.getByTestId('catalog-signed-out');
  await signedOut.waitFor({ state: 'visible', timeout: 20000 });
  if (!page.url().endsWith('#/servers')) {
    throw new Error(`signed-out admin route did not redirect to Servers: ${page.url().split('#')[1]}`);
  }
  if (await page.getByTestId('tool-usage-table').count()) {
    throw new Error('signed-out browser rendered the protected tool usage table');
  }
  await signedOut.screenshot({ path: `${evidenceDir}/signed-out.png` });
  console.log(JSON.stringify({ role: 'signed-out', route: page.url().split('#')[1], guard: 'visible' }));

  await page.getByTestId('landing-signin-button').click();
  await page.getByTestId('login-email').fill(process.env.QA_ADMIN_EMAIL);
  await page.getByTestId('login-password').fill(process.env.QA_ADMIN_PASSWORD);
  const loginResponse = page.waitForResponse((response) =>
    new URL(response.url()).pathname === '/auth/login', { timeout: 20000 });
  await page.getByTestId('login-submit').click();
  if ((await loginResponse).status() !== 200) throw new Error('browser login failed');

  pageErrors.length = 0;
  consoleErrors.length = 0;
  await page.goto('https://platform.mcpruntime.org/#/admin/analytics', { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: 'Usage analytics' }).waitFor({ state: 'visible', timeout: 20000 });
  await page.getByTestId('analytics-limit').selectOption('50');
  await page.getByTestId('tool-usage-table').waitFor({ state: 'visible', timeout: 20000 });
  const row = page.getByTestId('tool-usage-row').filter({ hasText: qaName }).filter({ hasText: 'aaa-ping' });
  let found = false;
  for (let attempt = 0; attempt < 8; attempt++) {
    if (await row.count()) {
      found = true;
      break;
    }
    await page.getByTestId('analytics-refresh').click();
    await page.waitForTimeout(3000);
  }
  if (!found) throw new Error(`Analytics → Tools did not render ${qaName}/aaa-ping`);
  await row.first().screenshot({ path: `${evidenceDir}/qa-tool-row.png` });

  const usageResponses = responses.filter((response) => response.path.includes('/analytics/usage'));
  if (!usageResponses.some((response) => response.status === 200)) {
    throw new Error('Analytics → Tools had no successful usage API response');
  }
  if (pageErrors.length || consoleErrors.length) {
    throw new Error(`browser errors after login: page=${pageErrors.length} console=${consoleErrors.length}`);
  }
  console.log(JSON.stringify({
    role: 'admin', route: page.url().split('#')[1],
    toolsRow: `${qaName}/aaa-ping`, usageResponses,
    pageErrors: pageErrors.length, consoleErrors: consoleErrors.length,
  }));
  await context.close();
} finally {
  await browser.close();
}
