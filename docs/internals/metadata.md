# Metadata Package

Package `pkg/metadata` supports the `.mcp` authoring flow. It loads lightweight
server metadata files, applies defaults, resolves registry hosts, and renders
`MCPServer` manifests.

Refresh the package reference with:

```bash
go doc -all ./pkg/metadata
```

## Metadata Schema

`RegistryFile` is the root document:

```yaml
version: v1
servers:
  - name: demo
    image: registry.local/demo
    imageTag: latest
```

`ServerMetadata` mirrors the parts of `MCPServerSpec` that contributors commonly
author by hand:

- server description
- image and tag
- route, ingress host, and public path prefix
- container port and replicas
- resources
- literal and secret-backed env vars
- namespace
- tools, prompts, resources, and tasks
- auth, policy, session, gateway, analytics, and rollout config

Keep this schema aligned with `api/v1alpha1.MCPServerSpec`. If a new CRD field
should be available in `.mcp` files, add it here, map it in the generator, and
cover it with loader/generator tests.

## Loading and Defaults

`LoadFromFile` reads one YAML file and applies defaults. `LoadFromDirectory`
aggregates all `.yaml` and `.yml` files in a directory into one registry.

Defaulting currently fills:

- registry-backed image when image is absent
- `latest` tag when image tag is absent
- route shape when route is absent
- default port `8088`
- one replica
- namespace `mcp-servers`

Default image host resolution is environment-aware. `ResolveRegistryHost`,
`ResolveRegistryEndpoint`, `ResolveMcpIngressHost`, and
`ResolvePlatformIngressHost` use `MCP_REGISTRY_*`, `MCP_PLATFORM_DOMAIN`, and
related env vars to keep generated manifests pullable and routable.

Public registry host selection is shared across CLI metadata and runtime API
behavior: `MCP_REGISTRY_INGRESS_HOST`, `MCP_REGISTRY_HOST`,
`registry.<MCP_PLATFORM_DOMAIN>`, then the local default. The public host is
used for image names, ingress, and registry credentials. The separate endpoint
resolver gives an explicitly configured `MCP_REGISTRY_ENDPOINT` priority for
internal pulls and transfers; that endpoint must not become a public ingress
or credential host. Manifest generation rewrites unqualified and known
platform-registry image refs to the kubelet pull host; external registry refs
such as `ghcr.io/owner/image` remain unchanged.

## Manifest Generation

`GenerateCRD` converts one `ServerMetadata` into an `MCPServer` YAML manifest.
`GenerateCRDsFromRegistry` writes one manifest per server to an output directory.

Generation should be deterministic:

- input metadata plus environment should fully determine output
- output filenames should remain stable
- omitted optional fields should stay omitted unless defaults are intentional
- generated YAML should match the API contract, not CLI-only assumptions

## Contributor Workflow

When changing metadata behavior:

1. Update `pkg/metadata` schema and generator mapping.
2. Add or update fixtures in `pkg/metadata/testdata` when useful.
3. Update user docs for `.mcp` authoring if the field is user-facing.
4. Check examples that rely on generated manifests.

Run:

```bash
go test ./pkg/metadata/... -count=1
go test ./internal/cli/server -run 'TestGenerate|TestBuild' -count=1
```
