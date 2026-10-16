# Error reference

Identify the layer that produced the failure before deciding whether to retry.
Runtime preserves upstream MCP responses where possible; gateway denials and
HTTP proxy failures use Runtime's own HTTP error envelope.

| Layer / signal | Meaning | Caller action |
|---|---|---|
| JSON-RPC `-32700`, `-32600`, `-32601`, `-32602`, `-32603` | Parse, request, method, parameter, or internal JSON-RPC error. The MCP server or JSON-RPC endpoint produced it. | Correct the request or server method/arguments. Do not retry unchanged invalid requests. |
| MCP tool result with `isError: true` | The server handled `tools/call` and returned a tool-level failure result. This is distinct from a JSON-RPC transport error. | Read the tool's result content and follow the tool-specific recovery guidance. |
| HTTP `401`, body `{"error":"…"}` | Gateway authentication or required-session failure. Reasons include `missing_client_certificate`, `untrusted_proxy`, `missing_verified_identity`, `invalid_spiffe_identity`, `missing_identity`, `missing_bearer_token`, `invalid_token`, `missing_session`, `session_not_found`, `session_expired`, `session_revoked`, and certificate/OAuth identity or team mismatch. OAuth routes may include `WWW-Authenticate`. | Refresh or re-enroll the appropriate credential/session; do not retry the same invalid credential. |
| HTTP `403`, body `{"error":"…"}` | Gateway policy denial, such as `no_matching_grant`, `grant_expired`, `grant_without_trust`, `tool_not_granted`, `tool_denied`, `tool_not_allowed`, `tool_side_effect_unknown`, `side_effect_not_allowed`, `trust_too_low`, `delegated_policy_unsupported`, or `delegated_identity_policy_unsupported`. | Review the matching grant, session consent, tool metadata, and policy decision. Retrying the same call does not change authorization. |
| HTTP `503`, body `{"error":"…"}` | Required policy/session state is unavailable (`policy_unavailable`), or OAuth configuration/metadata is unusable (`oauth_config_missing`, `oauth_issuer_missing`, `oauth_audience_missing`, `oauth_provider_unavailable`). | For a transient sync/provider outage, retry after recovery. Missing configuration requires an operator fix; do not retry unchanged requests. |
| HTTP `502`, `{"error":"upstream_error"}` | The gateway could not complete the upstream request. | Check server health and gateway/upstream logs; retry only if the operation is safe. |
| HTTP `504`, `{"error":"upstream_timeout"}` | The gateway exceeded its upstream response-header wait. It does not prove the server did no work. | Check server state before repeating a side-effecting call. |
| HTTP `404` or `410` for an MCP session | The upstream server may no longer recognize the transport session ID. | Start a new `initialize` exchange and repeat only operations whose outcome is known to be safe. |

JSON-RPC errors have an `id` and an `error` object with a numeric code. Gateway
denials instead use HTTP status and a small JSON object with a bounded reason;
do not parse that object as a JSON-RPC response. Tool execution can also return
HTTP success with a result whose `isError` field is true.

For OAuth, `oauth_config_missing`, `oauth_issuer_missing`, and
`oauth_audience_missing` indicate an incomplete resource configuration;
`oauth_provider_unavailable` indicates discovery/JWKS/provider access failed.
`certificate_oauth_subject_mismatch` and `certificate_oauth_team_mismatch`
indicate that the bearer identity conflicts with the enrolled adapter session.
Other policy inspection failures use `rpc_inspection_failed`.

The exact caller-visible reason is the JSON `error` value and the correlated
gateway log/audit event. This table covers Runtime's current gateway classes;
upstream application and JSON-RPC messages remain owned by the MCP server.
Do not rely on free-form error messages as stable identifiers.

The adapter retries idempotent reads (`tools/list`, `resources/list`,
`prompts/list`, and `ping`) on selected `502`/`504` and connection-reset
failures, with bounded attempts and backoff. It does not retry `tools/call`.
Applications should not blindly retry a call after a timeout or disconnect:
the upstream may have committed the side effect before the response was lost.

Source: [`services/mcp-gateway/proxy.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/services/mcp-gateway/proxy.go), [`pkg/policy/evaluator.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/pkg/policy/evaluator.go), and [`internal/agentadapter/transport.go`](https://github.com/mcp-runtime/mcp-runtime/blob/main/internal/agentadapter/transport.go).
