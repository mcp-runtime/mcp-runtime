// Real-platform headless acceptance for #542. Credentials arrive only on stdin.
// Keep traces/HAR/storageState disabled: those can retain login credentials.
const fs = require('node:fs');
const path = require('node:path');
const https = require('node:https');
const tls = require('node:tls');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.WORKSPACE_PLAYWRIGHT_MODULE || 'playwright');

async function verifiedCertificatePin(base, caPEM) {
  if (base.protocol === 'http:') return [];
  // Verify hostname and chain with Node first, then pin that verified leaf in
  // Chromium so a disposable staging CA need not enter the host trust store.
  const cert = await new Promise((resolve, reject) => {
    const request = https.get(base, { ca: [...tls.rootCertificates, ...(caPEM ? [caPEM] : [])], timeout: 15000 }, response => {
      const raw = response.socket.getPeerCertificate().raw;
      response.resume(); resolve(raw);
    });
    request.on('timeout', () => request.destroy(new Error('TLS verification timed out')));
    request.on('error', reject);
  });
  const spki = new crypto.X509Certificate(cert).publicKey.export({ type: 'spki', format: 'der' });
  return ['--ignore-certificate-errors-spki-list=' + crypto.createHash('sha256').update(spki).digest('base64')];
}

async function main() {
  let fixture;
  try { fixture = JSON.parse(fs.readFileSync(0, 'utf8')); } catch { throw new Error('invalid browser fixture JSON'); }
  const base = new URL(fixture.baseURL);
  assert(!base.username && !base.password && !base.search && !base.hash, 'invalid target URL');
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(base.hostname);
  assert(loopback || base.hostname.endsWith('.e2e.mcpruntime.org'), 'browser QA requires loopback or disposable E2E target');
  assert(base.protocol === 'https:' || loopback && base.protocol === 'http:', 'HTTPS required for remote target');
  const output = process.env.WORKSPACE_BROWSER_ARTIFACTS;
  assert(output, 'WORKSPACE_BROWSER_ARTIFACTS required');
  fs.mkdirSync(output, { recursive: true, mode: 0o700 });
  const secrets = Object.values(fixture.roles).map(role => role.password).filter(Boolean);
  const redact = value => {
    let text = String(value);
    for (const secret of secrets) text = text.split(secret).join('[redacted]');
    return text.replace(/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g, '[redacted-token]');
  };
  const result = { candidate: fixture.candidate || '', target: base.origin, browser: 'headless Chromium', checks: [], roles: {}, failures: [] };
  const check = (value, label) => { assert(value, label); result.checks.push(label); console.log('[workspace-browser] ' + label); };
  const browser = await chromium.launch({ headless: true, executablePath: process.env.WORKSPACE_CHROMIUM_EXECUTABLE || undefined, args: await verifiedCertificatePin(base, fixture.caPEM) });
  try {
    for (const name of ['member', 'owner', 'admin']) {
      const identity = fixture.roles[name]; assert(identity && identity.email && identity.password, 'missing role fixture: ' + name);
      const context = await browser.newContext({ viewport: { width: 1100, height: 800 } });
      const page = await context.newPage();
      page.setDefaultTimeout(25000);
      const evidence = { network: [], console: [], pageErrors: [], injected: [], blockedOrigins: [], publicAssetOrigins: [] };
      await context.route('**/*', route => {
        const request = route.request(), target = new URL(request.url());
        if (['http:', 'https:'].includes(target.protocol) && target.origin !== base.origin) {
          // The shipped UI loads public fonts. Permit those read-only asset
          // requests only when no account credentials accompany them.
          const headers = request.headers();
          if (['https://fonts.googleapis.com', 'https://fonts.gstatic.com'].includes(target.origin) && request.method() === 'GET' && !headers.authorization && !headers.cookie) {
            if (!evidence.publicAssetOrigins.includes(target.origin)) evidence.publicAssetOrigins.push(target.origin);
            return route.continue();
          }
          evidence.blockedOrigins.push(target.origin); return route.abort('blockedbyclient');
        }
        return route.continue();
      });
      let authenticated = false;
      result.roles[name] = evidence;
      page.on('pageerror', error => evidence.pageErrors.push(redact(error.message)));
      page.on('console', event => { if (['error', 'warning'].includes(event.type())) evidence.console.push({ type: event.type(), text: redact(event.text()) }); });
      page.on('response', response => {
        const url = new URL(response.url());
        if (url.origin === base.origin && (/\/api\//.test(url.pathname) || /\/auth\//.test(url.pathname))) evidence.network.push({ path: url.pathname, method: response.request().method(), status: response.status(), authenticated });
      });
      const json = async endpoint => page.evaluate(async endpoint => {
        const response = await fetch(endpoint, { credentials: 'same-origin' });
        if (!response.ok) throw new Error('read-only verification returned ' + response.status + ' for ' + endpoint);
        return response.json();
      }, new URL(endpoint, base).pathname);
      const go = async hash => { await page.goto(new URL('/' + hash, base).href, { waitUntil: 'domcontentloaded' }); };
      const section = id => page.locator('section[aria-labelledby="' + id + '"]');
      await go('#/signin');
      await page.getByTestId('login-email').fill(identity.email);
      await page.getByTestId('login-password').fill(identity.password);
      const login = page.waitForResponse(response => new URL(response.url()).pathname === '/auth/login' && response.request().method() === 'POST');
      await page.getByTestId('login-submit').click();
      check((await login).status() === 200, name + ': supported password UI login');
      authenticated = true;
      await page.getByTestId('account-trigger').filter({ visible: true }).waitFor();
      await go('#/workspace');
      await page.getByRole('heading', { name: 'My workspace', exact: true }).waitFor();
      const teams = (await json('/api/ui/v1/runtime/workspace/teams')).teams;
      const servers = (await json('/api/ui/v1/runtime/workspace/servers')).servers;
      await page.getByText('Accessible to me: ' + servers.length, { exact: true }).waitFor();
      await page.getByText('Manageable by me: ' + servers.filter(server => server.can_manage).length, { exact: true }).waitFor();
      for (const team of teams) await section('workspace-teams-title').getByRole('heading', { name: team.name, exact: true }).waitFor();
      check(teams.length === await section('workspace-teams-title').locator('.overview-card').count(), name + ': rendered memberships match personal API rows');
      check(servers.every(server => typeof server.can_manage === 'boolean'), name + ': accessible and manageable counts match API authority');
      await page.getByText(/Deployed by me: unavailable\./).waitFor();
      check(true, name + ': historical deployment attribution stays explicit');
      if (name !== 'admin') {
        const membership = teams.find(team => team.slug === identity.teamSlug);
        check(membership && membership.role === name, name + ': fixture role comes from real membership');
        await page.getByText('Gateway activity across your user and team namespaces in the last seven days. These are not exclusively your actions.').waitFor();
      }
      const serverRequests = () => evidence.network.filter(event => event.path === '/api/ui/v1/runtime/servers').length;
      const beforeMenu = serverRequests();
      await page.getByTestId('account-trigger').filter({ visible: true }).click();
      await page.getByRole('region', { name: 'Account details' }).getByRole('link', { name: 'My workspace', exact: true }).waitFor();
      await page.waitForTimeout(250);
      check(serverRequests() === beforeMenu, name + ': account menu performs no server-list request');
      await page.keyboard.press('Escape');
      await page.getByRole('link', { name: 'Open server catalog', exact: true }).click();
      await page.waitForURL('**/#/servers');
      await page.goBack(); await page.getByRole('heading', { name: 'My workspace', exact: true }).waitFor();
      await page.goForward(); await page.waitForURL('**/#/servers');
      await page.goBack(); await page.reload();
      await page.getByText('Accessible to me: ' + servers.length, { exact: true }).waitFor();
      check(true, name + ': hash back/forward/refresh preserves workspace route and data');
      if (name === 'member') {
        check(await section('workspace-teams-title').getByRole('link', { name: 'Manage members', exact: true }).count() === 0, 'member: management links hidden');
        await go('#/workspace/team?team=' + encodeURIComponent(identity.teamSlug));
        await page.getByText('You cannot manage this team.', { exact: true }).waitFor();
        const denied = await page.evaluate(async slug => {
          const auth = await (await fetch('/auth/status')).json();
          const response = await fetch('/api/ui/v1/runtime/teams/' + encodeURIComponent(slug) + '/members/' + encodeURIComponent(auth.principal.subject), { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': auth.csrf_token }, body: JSON.stringify({ role: 'member' }) });
          return response.status;
        }, identity.teamSlug);
        check(denied === 403, 'member: real backend rejects CSRF-valid membership write');
      }
      if (name === 'owner') {
        const ownedTeam = teams.find(team => team.slug === identity.teamSlug);
        await section('workspace-teams-title').locator('.overview-card').filter({ has: page.getByRole('heading', { name: ownedTeam.name, exact: true }) }).getByRole('link', { name: 'Manage members', exact: true }).click();
        await page.getByRole('heading', { name: 'Team management', exact: true }).waitFor();
        await page.getByTestId('teams-table').waitFor();
        await page.getByTestId('team-members-table').waitFor();
        check(await page.getByTestId('team-create-toggle').count() === 0, 'owner: scoped membership management avoids platform team creation');
        await page.getByRole('link', { name: 'Back to My workspace', exact: true }).click();
      }
      if (name !== 'admin') {
        evidence.injected.push('/api/ui/v1/user/analytics/usage');
        await page.route('**/api/ui/v1/user/analytics/usage**', route => route.fulfill({ status: 503, json: { error: 'workspace QA injected analytics outage' } }));
        await go('#/workspace'); await page.reload();
        await page.getByTestId('workspace-activity-error').waitFor();
        await page.getByText('Accessible to me: ' + servers.length, { exact: true }).waitFor();
        await section('workspace-teams-title').getByRole('heading', { name: teams[0].name, exact: true }).waitFor();
        check(true, name + ': analytics failure leaves membership and server sections usable');
        await page.unrouteAll({ behavior: 'wait' });
      } else {
        await page.getByText(/Administrator authority is platform-wide/).waitFor();
        await page.locator('.notice').filter({ hasText: 'Administrator authority is platform-wide' }).getByRole('link', { name: 'Platform overview', exact: true }).click();
        await page.getByRole('heading', { name: 'Platform overview', exact: true }).waitFor();
        const directory = await json('/api/ui/v1/admin/overview/directory');
        const counts = { Teams: directory.teams, Users: directory.users, Agents: directory.agents, 'Active sessions': directory.active_sessions };
        for (const [label, value] of Object.entries(counts)) {
          assert(Number.isInteger(value) && value >= 0, 'invalid directory count');
          const card = section('overview-counts-title').locator('.overview-card').filter({ has: page.getByRole('link', { name: label, exact: true }) });
          await card.locator('dd').filter({ hasText: new RegExp('^' + value + '$') }).waitFor();
        }
        evidence.counts = counts;
        check(true, 'admin: all rendered totals match authoritative directory API');
        const serverTotals = await json('/api/ui/v1/admin/overview/servers');
        const serverCount = section('overview-counts-title').locator('p').filter({ hasText: /^Servers:/ });
        await serverCount.locator('strong').filter({ hasText: new RegExp('^' + serverTotals.servers + '$') }).waitFor();
        await page.screenshot({ path: path.join(output, 'admin-platform-overview.png'), fullPage: true });
        for (const [endpoint, testID] of [['/api/ui/v1/admin/overview/directory', 'overview-directory-error'], ['/api/ui/v1/admin/operations', 'overview-audit-error']]) {
          evidence.injected.push(endpoint);
          await page.route('**' + endpoint + '**', route => route.fulfill({ status: 503, json: { error: 'workspace QA injected section outage' } }));
          await page.reload(); await page.getByTestId(testID).waitFor();
          await page.getByRole('heading', { name: 'Platform health', exact: true }).waitFor();
          await serverCount.locator('strong').filter({ hasText: new RegExp('^' + serverTotals.servers + '$') }).waitFor();
          if (endpoint.includes('operations')) for (const [label, value] of Object.entries(counts)) await section('overview-counts-title').locator('.overview-card').filter({ has: page.getByRole('link', { name: label, exact: true }) }).locator('dd').filter({ hasText: new RegExp('^' + value + '$') }).waitFor();
          check(true, 'admin: isolated ' + testID + ' preserves unaffected sections');
          await page.unrouteAll({ behavior: 'wait' });
        }
      }
      await go('#/workspace'); await page.reload();
      await page.getByText('Accessible to me: ' + servers.length, { exact: true }).waitFor();
      await page.screenshot({ path: path.join(output, name + '-workspace.png'), fullPage: true });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.screenshot({ path: path.join(output, name + '-workspace-390.png'), fullPage: true });
      evidence.mobileBounds = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scrollWidth: document.documentElement.scrollWidth }));
      check(evidence.mobileBounds.scrollWidth <= evidence.mobileBounds.width + 1, name + ': 390px viewport has no horizontal overflow');
      await page.getByTestId('account-trigger').filter({ visible: true }).click();
      authenticated = false;
      await page.getByTestId('logout-button').click();
      const status = await json('/auth/status');
      check(status.authenticated === false, name + ': supported UI logout clears session');
      check(evidence.pageErrors.length === 0, name + ': no JavaScript page errors');
      check(evidence.blockedOrigins.length === 0, name + ': requests stay on disposable origin or credential-free public font assets');
      check(evidence.console.filter(event => event.type === 'error' && !/Failed to load resource:.*(?:403|503)/.test(event.text)).length === 0, name + ': no unexpected console errors');
      const unexpected = evidence.network.filter(event => event.authenticated && event.status >= 400 && !(event.status === 503 && evidence.injected.some(endpoint => event.path.startsWith(endpoint))) && !(name === 'member' && event.status === 403 && event.method === 'PUT'));
      check(unexpected.length === 0, name + ': no unexpected authenticated API failures');
      await context.close();
    }
  } catch (error) {
    result.failures.push(redact(error.message)); process.exitCode = 1;
  } finally {
    await browser.close();
    fs.writeFileSync(path.join(output, 'workspace-browser.json'), JSON.stringify(result, null, 2), { mode: 0o600 });
  }
  if (result.failures.length) throw new Error(result.failures.join('; '));
}
main().catch(error => { console.error('[workspace-browser] ' + error.message); process.exitCode = 1; });
