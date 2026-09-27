# Build context must contain the selected mcp-auth checkout root. See README.md.
FROM node:22-bookworm-slim AS build
WORKDIR /src

COPY auth-client/typescript/package*.json ./auth-client/typescript/
COPY auth-client/typescript/tsconfig.json ./auth-client/typescript/
COPY auth-client/typescript/src/ ./auth-client/typescript/src/
RUN cd auth-client/typescript && npm ci && npm run build

COPY examples/typescript-mcp/package*.json ./examples/typescript-mcp/
COPY examples/typescript-mcp/tsconfig.json ./examples/typescript-mcp/
COPY examples/typescript-mcp/src/ ./examples/typescript-mcp/src/
RUN cd examples/typescript-mcp && npm ci && npm run typecheck && npx tsc --outDir dist

FROM node:22-bookworm-slim
WORKDIR /src
COPY --from=build /src/auth-client/typescript/package.json ./auth-client/typescript/package.json
COPY --from=build /src/auth-client/typescript/dist/ ./auth-client/typescript/dist/
COPY --from=build /src/examples/typescript-mcp/package*.json ./examples/typescript-mcp/
COPY --from=build /src/examples/typescript-mcp/dist/ ./examples/typescript-mcp/dist/
WORKDIR /src/examples/typescript-mcp
RUN npm ci --omit=dev && chown -R node:node /src
USER node
ENV MCP_PORT=8081
EXPOSE 8081
CMD ["node", "dist/server.js"]
