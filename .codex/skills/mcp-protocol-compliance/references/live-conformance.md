# Live MCP Transport Conformance

Use this reference only for Step 5 of `mcp-protocol-compliance`, after the static
schema checks have identified `SPEC_REV`, `SCHEMA`, `JV`, and `SPEC_CACHE`.

Precondition: `contributor-cluster-setup` has run, port-forward is up, and a demo
server is deployed with a valid grant/session.

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
kubectl --kubeconfig "$TEST_KUBECONFIG" config current-context \
  | grep -qx test-mcp-runtime \
  || { echo "Run contributor-cluster-setup first"; exit 1; }
export KUBECONFIG="$TEST_KUBECONFIG"
BASE=http://localhost:18080/go-example-mcp/mcp
PROTO="$SPEC_REV"
H=(-H "content-type: application/json"
   -H "accept: application/json, text/event-stream"
   -H "Mcp-Protocol-Version: $PROTO"
   -H "X-MCP-Human-ID: local-user"
   -H "X-MCP-Agent-ID: local-agent"
   -H "X-MCP-Agent-Session: local-session")
MCP_SPEC_TMP="${MCP_SPEC_TMP:-$(mktemp -d)}"
trap 'rm -rf "$MCP_SPEC_TMP"' EXIT
```

## initialize response

```bash
INIT="$(curl -sS "${H[@]}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
        "protocolVersion":"'"$PROTO"'",
        "capabilities":{},
        "clientInfo":{"name":"qa-spec-compliance","version":"0"}}}' "$BASE")"
echo "$INIT" | jq .
echo "$INIT" > "$MCP_SPEC_TMP/mcp-init.json"
"$JV" "$SCHEMA" "$MCP_SPEC_TMP/mcp-init.json" 2>&1 | head -10

echo "$INIT" | jq -e '.result.protocolVersion == "'"$PROTO"'"' >/dev/null \
  || echo "FAIL: server did not echo or downgrade protocolVersion correctly"
echo "$INIT" | jq -e '.result.capabilities | type == "object"' >/dev/null \
  || echo "FAIL: capabilities missing or wrong type"
echo "$INIT" | jq -e '.result.serverInfo.name and .result.serverInfo.version' >/dev/null \
  || echo "FAIL: serverInfo missing name/version"
```

## Streamable HTTP headers

```bash
SESSION="$(curl -si "${H[@]}" \
  -d '{"jsonrpc":"2.0","id":2,"method":"initialize","params":{
        "protocolVersion":"'"$PROTO"'","capabilities":{},
        "clientInfo":{"name":"qa","version":"0"}}}' "$BASE" \
  | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}' | tr -d '\r')"
[ -n "$SESSION" ] || echo "FAIL: server did not issue Mcp-Session-Id"

curl -sSI "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$BASE" \
  | tr -d '\r' | grep -iE '^Content-Type:' | head -1

curl -sS -X DELETE "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -o "$MCP_SPEC_TMP/del.out" -w "delete=%{http_code}\n" "$BASE"
curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":99,"method":"tools/list"}' "$BASE" \
  | grep -qiE 'error|session|invalid' \
  || echo "FAIL: session reuse after DELETE was accepted"
```

If `DELETE` returns 405 or the server accepts post-DELETE calls, record a
transport-spec violation unless the runtime intentionally decouples
`Mcp-Session-Id` from platform governance sessions and documents that choice.

## tools/list shape

```bash
TOOLS="$(curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/list"}' "$BASE")"
echo "$TOOLS" | jq -e '.result.tools | type == "array" and length > 0' >/dev/null \
  || echo "FAIL: tools/list missing or empty"
echo "$TOOLS" | jq -e '.result.tools | all(.name and (.inputSchema | type == "object"))' >/dev/null \
  || echo "FAIL: tool entry missing required name or inputSchema"
echo "$TOOLS" > "$MCP_SPEC_TMP/mcp-tools.json"
"$JV" "$SCHEMA" "$MCP_SPEC_TMP/mcp-tools.json" 2>&1 | head -10
```

## tools/call result envelope

```bash
CALL="$(curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call",
       "params":{"name":"add","arguments":{"a":2,"b":3}}}' "$BASE")"
echo "$CALL" | jq -e '.result.content | type == "array"' >/dev/null \
  || echo "FAIL: tools/call result.content must be array"
echo "$CALL" | jq -e '.result.content[0].type and .result.content[0].text != null' >/dev/null \
  || echo "FAIL: content[0] missing required type/text"
echo "$CALL" > "$MCP_SPEC_TMP/mcp-call.json"
"$JV" "$SCHEMA" "$MCP_SPEC_TMP/mcp-call.json" 2>&1 | head -10
```

## JSON-RPC error mapping

The spec inherits JSON-RPC 2.0 error codes
`-32700`/`-32600`/`-32601`/`-32602`/`-32603` and adds MCP-specific application
errors.

```bash
curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  --data-binary '{not json' "$BASE" \
  | jq -e '.error.code == -32700' >/dev/null \
  || echo "FAIL: parse error should map to -32700"

curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":5,"method":"definitely/not/a/method"}' "$BASE" \
  | jq -e '.error.code == -32601' >/dev/null \
  || echo "FAIL: unknown method should map to -32601"

curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":6,"method":"tools/call",
       "params":{"name":"add","arguments":{"a":"not-a-number"}}}' "$BASE" \
  | jq -e '.error.code == -32602 or (.result.isError == true)' >/dev/null \
  || echo "FAIL: invalid params should map to -32602 or isError result"
```

MCP allows tool-execution errors to surface either as a top-level `error.code`
or as `result.isError=true` with content; both shapes are spec-legal but should
be consistent for similar failure modes.

## Protocol-version negotiation

```bash
RESP="$(curl -sS "${H[@]/Mcp-Protocol-Version: $PROTO/Mcp-Protocol-Version: 1999-01-01}" \
  -d '{"jsonrpc":"2.0","id":7,"method":"initialize","params":{
        "protocolVersion":"1999-01-01","capabilities":{},
        "clientInfo":{"name":"qa","version":"0"}}}' "$BASE")"
echo "$RESP" | jq -e '.result.protocolVersion != "1999-01-01"' >/dev/null \
  || echo "FAIL: server echoed unsupported protocolVersion"

curl -sS -H "content-type: application/json" -H "accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":8,"method":"initialize","params":{
        "protocolVersion":"'"$PROTO"'","capabilities":{},
        "clientInfo":{"name":"qa","version":"0"}}}' "$BASE" | jq -e '.result // .error' >/dev/null
```
