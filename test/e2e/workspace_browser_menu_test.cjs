const test = require('node:test');
const assert = require('node:assert/strict');
const { serverListStartedByClick, menuServerListFailure } = require('./workspace_browser_menu.cjs');

test('a server list started before the account click is not attributed to the menu', () => {
  const requests = [
    { method: 'GET', path: '/api/ui/v1/runtime/servers', startedAt: 1000 },
  ];
  assert.deepEqual(serverListStartedByClick(requests, 1500), []);
});

test('a server list that starts on the click fails and names the request', () => {
  const requests = [
    { method: 'GET', path: '/api/ui/v1/runtime/servers', startedAt: 1500 },
  ];
  assert.equal(serverListStartedByClick(requests, 1500).length, 1);
  const message = menuServerListFailure('owner', requests, 1500);
  assert.match(message, /owner: account menu performs no server-list request/);
  assert.match(message, /GET \/api\/ui\/v1\/runtime\/servers started 0ms after click/);
});

test('workspace and tool reads after the click are not a server-list request', () => {
  const requests = [
    { method: 'GET', path: '/api/ui/v1/runtime/workspace/servers', startedAt: 2000 },
    { method: 'GET', path: '/api/ui/v1/runtime/tools', startedAt: 2000 },
  ];
  assert.deepEqual(serverListStartedByClick(requests, 1500), []);
});
