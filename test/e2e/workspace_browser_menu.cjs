// The account-menu check must ignore a server-list request the catalog page
// already started. Login lands on Servers, then the test opens My workspace
// while that read can still be in flight. Count requests that start at or
// after the click, not responses that happen to finish in the click window.

const SERVER_LIST_PATH = '/api/ui/v1/runtime/servers';

function serverListStartedByClick(requests, clickAt) {
  return (requests || []).filter((request) => request.path === SERVER_LIST_PATH && request.startedAt >= clickAt);
}

function menuServerListFailure(role, requests, clickAt) {
  const detail = serverListStartedByClick(requests, clickAt).map((request) => {
    const method = request.method || 'GET';
    return method + ' ' + request.path + ' started ' + (request.startedAt - clickAt) + 'ms after click';
  }).join('; ');
  return role + ': account menu performs no server-list request (' + detail + ')';
}

module.exports = { SERVER_LIST_PATH, serverListStartedByClick, menuServerListFailure };
