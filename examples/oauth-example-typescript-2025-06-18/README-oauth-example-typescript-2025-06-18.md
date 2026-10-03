# oauth-example-typescript-2025-06-18

This fixture uses the [`mcp-auth` TypeScript SDK](https://github.com/Agent-Hellboy/mcp-auth/tree/main/auth-client/typescript).
The server declares MCP protocol revision `2025-06-18` and validates OAuth
tokens itself with `@mcp-auth/client`.

The `.mcp/servers.yaml` metadata uses `gateway.enabled: false`, so this
standalone resource server authenticates requests without Runtime gateway
grants, sessions, policy enforcement, or gateway audit events. Adapter mTLS is
a separate adapter-to-Runtime identity feature.
The Runtime server identity ends in `-standalone` to make that mode visible.

## Build and deploy

The image build needs a selected `mcp-auth` checkout because the package is
linked from that repository. Stage the SDK and example into a build context:

```bash
MCP_AUTH_SOURCE=/path/to/mcp-auth
MCP_AUTH_REF=issue-13-cimd
MCP_AUTH_COMMIT="$(git -C "$MCP_AUTH_SOURCE" rev-parse "$MCP_AUTH_REF^{commit}")"
MCP_AUTH_TAG="verify-ts-$(date -u +%Y%m%dT%H%M%S)-${MCP_AUTH_COMMIT:0:8}"
BUILD_CONTEXT="$(mktemp -d)"

git -C "$MCP_AUTH_SOURCE" archive "$MCP_AUTH_REF" \
  auth-client/typescript examples/typescript-mcp \
  | tar -x -C "$BUILD_CONTEXT"
cp examples/oauth-example-typescript-2025-06-18/server.ts \
  "$BUILD_CONTEXT/examples/typescript-mcp/src/server.ts"

./bin/mcp-runtime server build image oauth-example-typescript-2025-06-18-standalone \
  --metadata-dir examples/oauth-example-typescript-2025-06-18/.mcp \
  --dockerfile examples/oauth-example-typescript-2025-06-18/Dockerfile \
  --context "$BUILD_CONTEXT" --tag "$MCP_AUTH_TAG"
```

Publish and deploy through the platform API. The checked-in metadata is the
source for issuer, resource, audience, route, and gateway mode:

```bash
IMAGE_REF="registry.mcpruntime.org/oauth-example-typescript-2025-06-18:${MCP_AUTH_TAG}"
./bin/mcp-runtime server push --scope public --image "$IMAGE_REF"
./bin/mcp-runtime server deploy oauth-example-typescript-2025-06-18-standalone \
  --scope public \
  --metadata-file examples/oauth-example-typescript-2025-06-18/.mcp/servers.yaml
```

The platform uses the existing `mcp-auth-server` issuer and TLS certificate;
this flow does not run setup or request certificates.

Runtime injects `MCP_AUTH_ISSUER` and `MCP_AUTH_RESOURCE`. The example discovers
JWKS from the issuer and derives protected-resource metadata from the resource
URL; there are no separate issuer, JWKS, or resource environment overrides in
the metadata.

With `gateway.enabled: true`, Runtime also supplies `MCP_AUTH_ISSUER` and
`MCP_AUTH_RESOURCE`. The gateway forwards the validated bearer and this app
still verifies its signature, issuer, audience, and scopes. Direct access to
the app requires a valid bearer too. `MCP_PATH` sets the upstream request path
when the gateway strips a prefix; the token audience stays the public resource
URL. The gateway owns public challenges, metadata, and governance policy.
