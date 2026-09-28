# API Types

Package `api/v1alpha1` is the public Kubernetes contract for MCP Runtime. Treat
these Go types and the generated CRD YAML under `config/crd/bases/` as the source
of truth for object shape, defaults, validation, and kubectl columns.

Refresh the package reference with:

```bash
go doc -all ./api/v1alpha1
```

## Group and Scheme

The API group is `mcpruntime.org/v1alpha1`. `GroupVersion`, `SchemeBuilder`,
and `AddToScheme` register every runtime type with Kubernetes and
controller-runtime. The operator, envtest suites, generated clients, and webhook
setup all depend on this registration.

When adding a root resource, update:

- Go types in `api/v1alpha1`
- scheme registration
- deepcopy generation
- CRD generation under `config/crd/bases`
- operator or service code that consumes the new contract

## MCPServer

`MCPServer` describes one MCP workload and its routing, gateway, policy, session,
analytics, and rollout settings.

Key spec areas:

| Area | Fields | Contributor notes |
|---|---|---|
| Image | `image`, `imageTag`, `registryOverride`, `useProvisionedRegistry`, `imagePullSecrets` | Reconciled by the operator into Deployment image refs and pull secrets. Keep registry behavior aligned with setup and metadata generation. |
| Scale and ports | `replicas`, `port`, `servicePort` | Defaults are applied by the admission webhook; CRD schema should allow unset optional fields when defaults exist. |
| Routing | `ingressHost`, `publicPathPrefix`, `ingressPath`, `ingressClass`, `ingressAnnotations` | Host-based and hostless path-based routing both matter. E2E should cover public path changes. |
| Runtime config | `envVars`, `secretEnvVars`, `resources` | Converted into pod container env and resource requirements. |
| Inventory | `tools`, `prompts`, `mcpResources`, `tasks` | Used by gateway policy and UI/API surfaces. |
| Governance | `auth`, `policy`, `session`, `gateway`, `analytics` | Changes usually require updates in `pkg/access`, Sentinel services, and e2e policy scenarios. |
| Rollout | `rollout` | Reconciled into Deployment strategy/canary behavior where supported. |

`MCPServerStatus` reports phase, message, Kubernetes conditions, and readiness
booleans for deployment, service, ingress, gateway, policy, and canary state.
Status fields are operator-owned; user-facing commands should not mutate them.

## Access Resources

`MCPAccessGrant` and `MCPAgentSession` model gateway authorization state.

`MCPServer.spec.teamID` records the owning platform team. `SubjectRef` carries
`humanID`, `agentID`, and `teamID`; the gateway matches every non-empty subject
field exactly, so a team-only subject can grant access to any authenticated
principal from that team. Namespace/RBAC boundaries still decide who can write
the resources; see [Multi-team isolation](../multi-team.md).

`MCPAccessGrantSpec` binds a subject to an `MCPServer` and optionally sets:

- `maxTrust`: administrative trust ceiling
- `allowedSideEffects`: side-effect classes (`read`, `write`, `destructive`) the grant may use; omitted or empty fails closed
- `policyVersion`: cache invalidation/version marker
- `disabled`: soft off switch
- `toolRules`: per-tool allow/deny decisions and required trust levels

`MCPAgentSessionSpec` stores runtime consent and session state:

- target `serverRef`
- `subject` identity
- `consentedTrust`
- optional `expiresAt`
- `revoked`
- optional upstream token secret reference
- `policyVersion`

Gateway and API code should treat disabled grants and revoked sessions as active
deny signals. Do not rely only on UI state for enforcement.

## Shared Enums and Embedded Structs

Common embedded structs include:

- `ServerReference`
- `SubjectRef`
- `ToolRule`
- `ToolConfig`
- `InventoryItem`
- `AuthConfig`
- `PolicyConfig`
- `SessionConfig`
- `GatewayConfig`
- `AnalyticsConfig`
- `RolloutConfig`
- `SecretKeyRef`
- `EnvVar`
- `SecretEnvVar`

Shared enums include:

- `PolicyDecision`: `allow`, `deny`
- `TrustLevel`: `low`, `medium`, `high`
- `PolicyMode`: `allow-list`, `observe`
- `RolloutStrategy`: `RollingUpdate`, `Recreate`, `Canary`

Keep enum values stable once published. If a value must be renamed, add a
migration path and update examples, generated CRDs, UI/API validation, and e2e.

## Webhooks, Defaults, and Validation

Defaulting and validation methods live with the API package and are registered
through controller-runtime webhook setup. They should enforce invariants that
must be true no matter which client creates or updates the object.

`MCPServer` defaults are applied by the mutating admission webhook. The
reconciler still applies the same defaults to an in-memory copy before rendering
resources so legacy objects created before the webhook existed continue to
reconcile, but it should not persist those defaults back into the API object.

Use validation for object-level API correctness, not runtime availability. For
example, malformed policy decisions belong in validation; whether an image is
pullable belongs in setup/doctor diagnostics and Kubernetes status.

Generated webhook configuration lives under `config/webhook/`. The setup flow
creates the webhook TLS secret, enables webhook serving on the operator
deployment, and injects the generated CA bundle into the webhook configuration
before applying it.

## Generated Files

`zz_generated.deepcopy.go` is generated and should not be hand-edited. Regenerate
API artifacts with:

```bash
make -f Makefile.operator generate manifests
```

After changing API types, run:

```bash
go test ./api/v1alpha1/... ./internal/operator/... -count=1
go test ./test/golden/... -count=1
```
