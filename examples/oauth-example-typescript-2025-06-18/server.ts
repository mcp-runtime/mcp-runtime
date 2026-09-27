import { createServer, type ServerResponse } from "node:http";
import {
  AuthenticationError,
  JWTVerifier,
  authenticateRequest,
  bearerChallenge,
  discoverAuthorizationServer,
  protectedResourceMetadata,
  protectedResourceMetadataUrl,
} from "@mcp-auth/client";

// Runtime derives these from auth.issuerURL and the public MCP URL when the
// gateway is disabled. Local runs provide the same two settings themselves.
const issuer = process.env.MCP_AUTH_ISSUER;
const resource = process.env.MCP_AUTH_RESOURCE;
if (!issuer || !resource) {
  throw new Error("OAuth requires MCP_AUTH_ISSUER and MCP_AUTH_RESOURCE");
}
const resourcePath = new URL(resource).pathname || "/mcp";
const metadataUrl = protectedResourceMetadataUrl(resource);
const metadataPath = new URL(metadataUrl).pathname;
const authorizationServer = await discoverAuthorizationServer(issuer);
const verifier = new JWTVerifier({
  issuer,
  audience: resource,
  jwksUri: authorizationServer.jwks_uri,
  requiredScopes: ["tools:read"],
});

function json(response: ServerResponse, status: number, body: unknown): void {
  response.writeHead(status, { "content-type": "application/json" });
  response.end(JSON.stringify(body));
}

const server = createServer(async (request, response) => {
  if (request.method === "GET" && ["/.well-known/oauth-protected-resource", metadataPath].includes(request.url ?? "")) {
    return json(response, 200, protectedResourceMetadata(verifier, resource, issuer));
  }
  if (request.method !== "POST" || request.url !== resourcePath) {
    return json(response, 404, { error: "not_found" });
  }

  let claims;
  try {
    claims = await authenticateRequest(request, verifier);
  } catch (error) {
    const authError = error instanceof AuthenticationError ? error : new AuthenticationError(401, "invalid_token", "Unauthorized");
    response.setHeader("WWW-Authenticate", bearerChallenge(metadataUrl, authError.code, authError.message));
    return json(response, authError.status, { error: authError.code });
  }

  const chunks: Buffer[] = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  let message: { id?: unknown; method?: unknown; params?: { name?: unknown } };
  try {
    message = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    return json(response, 400, { jsonrpc: "2.0", error: { code: -32700, message: "Parse error" }, id: null });
  }
  if (!message || typeof message !== "object" || Array.isArray(message)) {
    return json(response, 400, { jsonrpc: "2.0", error: { code: -32600, message: "Invalid Request" }, id: null });
  }
  if (message.id === undefined) {
    response.writeHead(202);
    return response.end();
  }
  if (message.method === "initialize") {
    return json(response, 200, { jsonrpc: "2.0", id: message.id, result: {
      protocolVersion: "2025-06-18", capabilities: { tools: {} },
      serverInfo: { name: "oauth-example-typescript-2025-06-18", version: "1.0.0" },
    } });
  }
  if (message.method === "tools/list") {
    return json(response, 200, { jsonrpc: "2.0", id: message.id, result: { tools: [{
      name: "whoami", description: "Return the authenticated MCP subject", inputSchema: { type: "object", properties: {} },
    }] } });
  }
  if (message.method === "tools/call" && message.params?.name === "whoami") {
    return json(response, 200, { jsonrpc: "2.0", id: message.id, result: {
      content: [{ type: "text", text: JSON.stringify({ subject: claims.subject, scopes: [...claims.scopes] }) }],
    } });
  }
  return json(response, 200, { jsonrpc: "2.0", id: message.id, error: { code: -32601, message: "Method not found" } });
});

const port = Number(process.env.MCP_PORT ?? 8081);
server.listen(port, "0.0.0.0", () => console.log(`TypeScript MCP server listening at ${resource}`));
