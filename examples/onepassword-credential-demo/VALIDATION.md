# Hosted validation — 2026-10-23

Endpoint: `https://mcp.mcpruntime.org/onepassword-credential-demo/mcp`

The example was built, pushed, and deployed through the Runtime CLI, using
`server init`, `server validate`, `server build image`, `server push`, and
`server deploy`. Interactive acceptance used
`registry.mcpruntime.org/public/onepassword-credential-demo:demo-20261023-4`.
The final image, `demo-20261023-5`, rebuilds the same server behavior with
Go 1.26.10 to match current Runtime main; readiness, anonymous denial and
discovery were checked again after that rollout.
The server was Ready with one replica in `mcp-servers-public`.

The existing Runtime deployment was v0.12.1 and the existing authorization
server was `princekrroshan01/mcp-auth-server:0.4.4`. Neither platform nor auth
images were updated. Runtime registered the example audience from the server
metadata while retaining the existing resource.

## Observed results

| Check | Result |
|---|---|
| Anonymous MCP request | 401 with protected-resource discovery challenge |
| Protected-resource metadata | Exact example audience, existing mcp-auth issuer, `tools:read` |
| Real browser OAuth sign-in | Passed with PKCE and loopback callback |
| MCP initialize and tools/list | Passed; three expected tools |
| Native browser form with wrong test credentials | Rejected by credential validation, after passing origin and CSRF checks |
| User-completed browser / saved 1Password Login connection | Passed |
| Browser connection reuse | Two upstream calls succeeded without resubmitting credentials |
| Browser disconnect | Next upstream call denied |
| Local 1Password CLI desktop integration | Retrieved the dedicated test Login through the approved local CLI |
| Desktop connector submission | Passed over HTTPS to the pinned hosted example |
| Desktop connection reuse | Two upstream calls succeeded without resubmitting credentials |
| Desktop disconnect | Next upstream call denied |
| Final acceptance-client result | All requested hosted checks passed; credentials discarded |

The desktop acceptance exercised the one-shot connector, which shares its
credential lookup/submission code with the local MCP tool. A separate stdio MCP test passed initialize/tools-list and verified that an
unapproved destination is rejected before any 1Password read. The hosted
desktop acceptance used the one-shot command.

`go test -race ./...`, `go vet ./...`, Runtime metadata validation, and
`git diff --check` passed. Unit/HTTP tests cover subject/token isolation,
expiry, one-use links, rejected credentials, CSRF and origin checks, stable
approval when a link is reopened, credential-free responses, Basic-only
upstream authentication, OAuth signature/audience/expiry checks and refusal
of tokens without a unique ID.

## Issues found and corrected during acceptance

- The ingress routes the MCP path, so the form must live at
  `/onepassword-credential-demo/mcp/connect`, not a sibling `/connect` route.
- The deployed auth version supplies `jti` rather than `sid`. The example
  explicitly binds credentials to the current access token ID; refreshing
  the token requires reconnecting.
- `Referrer-Policy: no-referrer` turned native form POST's Origin into `null`.
  The corrected `same-origin` policy preserves the exact-origin check.
- Opening the same link twice used to replace its CSRF approval. The same
  unconsumed link now retains its approval until expiry or successful use.

Only public fixture credentials were used. No passwords, OAuth tokens,
1Password field values or live connection links are retained in this report.
The example remains deployed for review; the tested connections were
explicitly disconnected.
