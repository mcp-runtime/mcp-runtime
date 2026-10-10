# Registry authentication

## Default for production-shaped installs

`mcp-runtime setup` finishes by enabling Distribution token authentication on
the bundled registry backend (port 5000) as well as the public route. This
step, **Step 7: Require registry authentication**, runs when all of these hold:

- setup is not in `--test-mode`;
- the bundled registry is used (not `--registry-mode external`);
- `--with-tls` is set and the platform stack is installed (platform-api is the
  token broker);
- a public platform host is configured (`MCP_PLATFORM_DOMAIN` or
  `MCP_PLATFORM_INGRESS_HOST`). The token realm is
  `https://<platform host>/api/v1/registry/token`.

The step runs after the post-setup verification gate. It reads a platform
administrator key from the platform-api credentials Secret, so no CLI login is
needed. A failure fails setup. Fix the cause and rerun setup: activation resumes
and never turns backend authentication off. A rerun against a registry that
already enforces tokens leaves it unchanged; rotate node credentials with
`registry enable-auth --pull-namespace` (see below).

Test mode and plain-HTTP lab installs keep an unauthenticated backend. Setup
warns about this. The registry NetworkPolicy is then the only barrier between
tenant workloads and the backend (see [Network policy](#network-policy)). Use
those installs for labs and disposable tests only.

## Manual activation

`mcp-runtime registry enable-auth --realm https://api.example.com/api/v1/registry/token`
activates the same authentication on an existing install. Install the updated
platform-api and runtime-api images first, and log in as a platform
administrator. Use `--dry-run` to inspect the prerequisites without writing
cluster resources.

By default the command refuses a platform-api image served by the registry it
protects. Pass `--allow-bundled-broker` to accept one. Setup always accepts it,
because setup publishes platform-api to the bundled registry (see
[Bundled broker dependency](#bundled-broker-dependency)).

Activation preserves the workload and TLS certificate authorities. It creates
a separate registry signing Secret in `mcp-platform`, copies only its public
certificate into `registry`, rolls the broker, checks its public realm, and
provisions read-only pull credentials for all existing managed namespaces.
Runtime-api then uses the broker for new namespace credentials. Distribution
checks signed repository scopes for every backend request; public forward-auth
also understands these tokens. The registry Service becomes ClusterIP.

Node credentials have the `mcpp_` prefix, are bound to their namespace username,
and expire after 90 days. Their database records contain a hash and explicit
repository/prefix scope; they can mint only five-minute pull tokens. Shared
gateway images and images already used by the namespace are included explicitly.
Run `registry enable-auth --pull-namespace mcp-team-example --realm ...` before
expiry to replace a namespace's pull Secret. Old credentials remain valid until
expiry or administrative revocation with
`DELETE /api/v1/registry/pull-credentials?id=rp_...`; already issued bearer tokens
remain valid for at most five minutes. New credentials are returned only once
by the admin-only POST endpoint, with `Cache-Control: no-store`.

Activation also rotates service admin keys found in legacy namespace pull
Secrets across their owner Secrets and compatibility mirrors, and rolls their
consumers. A private rotation journal permits retry after a partial failure;
rerun activation (setup, or `registry enable-auth` with an administrator
login, not the copied service key). Do not delete the journal during recovery.
A completed activation removes the journal.

## Publication

Publication helpers run only in the trusted `registry` namespace and mount
`mcp-registry-publisher` as a Docker authentication file. This applies both to
runtime-api publication (`server push`) and to the in-cluster helper used by
`setup`, `update`, and `admin registry push`. The service publication
credential is never copied into team pull Secrets or placed in helper commands.
Runtime API publication selects this namespace from the live registry configuration. Its namespace-scoped Role permits helper pods and logs without Secret reads or pod exec; archive-transfer Secrets remain in `mcp-platform`.

Helpers carry the `app.kubernetes.io/name: registry-push-helper` label, which
the registry NetworkPolicy uses to admit them and to grant their egress.

Anonymous pushes are rejected once backend authentication is active, including
pushes through a `kubectl port-forward` to the registry Service. The reference
deployment's `hack/deploy/mcpruntime-org/rollout.sh` must therefore use
`MCP_REGISTRY_PUSH_MODE=public`, which logs in to the public registry host with
the platform service key.

## Network policy

`config/registry/base/networkpolicy.yaml` admits only Traefik, labeled
publication helpers (in `registry` or `mcp-platform`), and the cluster doctor
reachability probe (`app.kubernetes.io/name: registry-probe`, `registry`
namespace only) to the backend port. The probe accepts a `401` Bearer token
challenge as proof that native authentication is enforced.
Tenant namespaces (`mcp-servers*`, platform-managed team namespaces), the
operator namespace, and ordinary platform service pods cannot reach it.
Kubelet pulls come from the node and authenticate with the namespace pull
Secret.

`registry-push-helper-egress` lets helpers fetch the image archive from
runtime-api (TCP 8084) and reach the public HTTPS token realm (TCP 443 to any
address, and Traefik's websecure port when the realm hairpins inside the
cluster; the k3s compatibility overlay adds Traefik in `kube-system`). No other
registry-namespace pod gets egress beyond DNS and the registry pod.

## Bundled broker dependency

When platform-api is served by the registry it protects, every new pull of
the platform-api image needs a token from a running platform-api replica. The
Deployment's rolling update keeps the old replica serving tokens while the new
one pulls, so `setup` reruns and `update` work. Before native authentication,
the authenticated public route already had the same dependency through its
forward-auth check.

A cold start where no node has the platform-api image cached and no replica is
running cannot pull the broker. Avoid this by keeping registry storage and
node image caches during maintenance. To recover, import the platform-api image
into the node runtime (for example `docker save` on a build host and
`k3s ctr images import` on the node), or point platform-api at an external
image, then let the Deployment start. Do not disable backend authentication
as a workaround.

Changes to signing roots are refused by activation; certificate renewal/root
rotation require a separately reviewed procedure before the one-year registry
signing certificate expires. Setup registry rendering and compatibility
overlays preserve the native registry authentication environment, public trust
mount, and ClusterIP-only Service.

## Tests

CI's native registry acceptance job creates and removes its own Distribution
container. It tests anonymous rejection, cross-team rejection, node upload
rejection, complete OCI publication, and read-only image retrieval. It does not
use a Kubernetes context or deploy to production. Staging E2E exercises setup's
default activation on the disposable VM.
