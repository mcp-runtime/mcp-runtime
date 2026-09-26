# MCP Runtime troubleshooting reference

Symptom-oriented remedies. Prefer `cluster doctor` and targeted fixes before full reinstall.

## Common failures

- **Adapter certificate returns `session_not_found` immediately after enrollment:** a created `MCPAgentSession` and updated policy ConfigMap do not prove that the gateway has loaded the session. Kubernetes still needs to project the mounted ConfigMap and the gateway must reload it. Check the target server's applied revision at `/config/status` through an authenticated internal connection, or use a bounded certificate-authenticated initialize probe through its real ingress. For revocation, require `401 session_revoked` for that exact certificate on that server; do not inspect another server's policy or match an unrelated revoked session. The Kind helper is `test/e2e/lib/adapter-certificates.sh`. Never retry tool calls to wait for policy propagation.
- **Every adapter `tools/call` returns `401 {"error":"session_not_found"}` although the `MCPAgentSession` and the `<server>-gateway-policy` ConfigMap contain the session:** the gateway has not loaded the ConfigMap revision yet. Both granted and ungranted tools fail the same way because the session lookup runs before the grant check. The kubelet only re-projects a mounted ConfigMap when it syncs the pod, which is about every 60-90s by default. The operator forces that sync by stamping `mcpruntime.org/gateway-policy-revision` on the server pods whenever the rendered policy changes. Compare the ConfigMap `revision` with the gateway's `/config/status` (`kubectl port-forward deploy/<server> 18091:8091`) and with the pod annotation. If the annotation is stale, check the operator log for `Could not annotate gateway pod` and confirm the operator ClusterRole grants `pods` `list` and `patch` (`config/rbac/role.yaml`).
- **“ingressHost is required” (operator):** set `spec.ingressHost` on the `MCPServer`, or operator env `MCP_DEFAULT_INGRESS_HOST`, or `MCP_PLATFORM_DOMAIN` for `mcp.<domain>` defaults.
- **MCPServer stuck `PartiallyReady` with working ingress traffic:** default ingress readiness is strict and waits for `Ingress.status.loadBalancer.ingress[]`. For dev / NodePort-style ingress controllers that route without publishing LB status, set operator env `MCP_INGRESS_READINESS_MODE=permissive`; this treats an Ingress with rules as ready. Keep the default `strict` mode for production setups that rely on published LB status.
- **Port mismatch:** the bundled workspace assistant sample listens on `8088` by default; align `MCPServer` `port` / `servicePort` and container `PORT` if you overrode them.
- **Analytics 401:** use gateway/ingest URL and key, not the app’s random env. Example: `ANALYTICS_INGEST_URL=http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events` and `ANALYTICS_API_KEY` from `mcp-sentinel-secrets` (`INGEST_API_KEYS` key).
- **Kafka `InconsistentClusterIdException`:** Kafka logs and stored metadata describe different clusters. Setup refuses to delete the Kafka PVC automatically. Scale Kafka to zero, delete all three `kafka-data-kafka-{0,1,2}` PVCs when a clean reset is acceptable, then rerun setup.
- **Kafka KRaft quorum unavailable:** the bundled cluster requires two of three combined broker/controller pods for quorum. Check `kubectl get pods -n mcp-sentinel -l app=kafka -o wide`, then run `kafka-metadata-quorum --bootstrap-server localhost:9092 describe --status` in `kafka-0`. For an intentional clean reset, scale Kafka to zero and delete all three `kafka-data-kafka-{0,1,2}` PVCs before rerunning setup.
- **ClickHouse and/or Kafka CrashLoopBackOff after long cluster uptime (not `InconsistentClusterIdException`):** local-path PVC data corrupted by an unclean Docker Desktop/Kind VM shutdown, not a code bug — don't chase it as one. Confirm with `kubectl logs -n mcp-sentinel <pod> --previous`: ClickHouse exits 70 with no reason on stdout; Kafka logs `Malformed line in checkpoint file .../replication-offset-checkpoint`. Only the corrupted replica fails (e.g. `kafka-1` can stay healthy while `kafka-0`/`kafka-2` don't); `mcp-sentinel-processor` and `mcp-analytics-api` fail purely downstream with `connect: connection refused` to ClickHouse. Fix: `kubectl delete pod -n mcp-sentinel <replica>` then `kubectl delete pvc -n mcp-sentinel <its PVC>` (`data-clickhouse-0`, `kafka-data-kafka-<n>`) — the StatefulSet recreates both and Kafka resyncs from a healthy broker. Wiping ClickHouse's volume also wipes its schema; `kubectl create job --from=job/clickhouse-init` is not supported on all kubectl versions (`unknown object type *v1.Job`), so replay the init Job's script directly over stdin instead of nesting `sh -c` (parens in the SQL break nested quoting):
  ```bash
  kubectl get job -n mcp-sentinel clickhouse-init \
    -o jsonpath='{.spec.template.spec.containers[0].args[0]}' \
    | sed 's/${CLICKHOUSE_DB}/mcp/g' \
    | kubectl exec -i -n mcp-sentinel clickhouse-0 -- sh
  ```
  The script is idempotent (`IF NOT EXISTS` throughout), safe to replay. Force-restart any processor/analytics pods still stuck in a stale crashloop backoff timer with `kubectl delete pod`. Verify via `/api/ui/v1/dashboard/summary`, `/api/ui/v1/analytics/usage`, `/api/ui/v1/events` returning 200 (empty data is expected on a fresh ClickHouse).
- **Secret not found in workload namespace:** copy `mcp-sentinel-secrets` or use a shared secret reference.
- **Dashboard / API 401:** direct admin `x-api-key` curl calls need a key present in both `API_KEYS` and `ADMIN_API_KEYS`; browser login uses `UI_API_KEY`. Keep `UI_API_KEY` present in both lists, then roll `mcp-platform-api`, `mcp-runtime-api`, `mcp-analytics-api`, and `mcp-sentinel-ui` after secret changes. Signed-in dashboard reads use the UI service's allowlisted `GET /api/ui/v1/*` BFF (`RUNTIME_UPSTREAM` → runtime-api, `ANALYTICS_UPSTREAM` → analytics-api). Direct `/api/v1/*` still requires a bearer token or API key and does not accept `mcp_ui_session`.
- **Dashboard 308 redirect loop in dev:** the UI service redirects HTTP→HTTPS for non-local hosts when it sees `X-Forwarded-Proto: http`. Override with the `UI_REQUIRE_HTTPS` env on the `mcp-sentinel-ui` deployment: `auto` (default — redirect public hosts only), `true`/`on`/`1`/`yes` (always redirect HTTP for public hosts), `false`/`off`/`0`/`no` (never redirect, use this when the UI is fronted by a non-TLS terminator on a real hostname).
- **Ingress / routes:** `kubectl get ingress -A` and confirm paths match the gateway and demo servers you expect.
- **Custom ingress namespaces:** bundled Traefik manifests watch `registry`, `mcp-sentinel`, `mcp-servers`, `mcp-servers-org`, and `mcp-servers-public` only so the controller does not need cluster-wide Secret access. If you place public Ingresses in another namespace, including per-team namespaces such as `mcp-team-acme`, add that namespace to the Traefik `--providers.kubernetesingress.namespaces` list, bind the `traefik-watch` Role there, and allow ingress-controller traffic through the namespace NetworkPolicy. Platform API `team create` performs that setup for the repo-managed `traefik/traefik` Deployment; for k3s / external Traefik (`kube-system/traefik`, cluster-wide ingress watch), setup sets `PLATFORM_TEAM_TRAEFIK_WATCH=disabled` automatically. Override with `PLATFORM_TEAM_TRAEFIK_WATCH=required` only when patching repo-managed Traefik is intentional. See `docs/multi-team.md` for the multi-team namespace/RBAC pattern.
- **Tenant `server push` 500 / copy timeout:** the CLI uploads a docker save tar to `POST /api/v1/runtime/registry/push`. The API must not copy that tar through `pods/exec` stdin (kubelet stream timeout ~35s). The supported path registers a one-time internal transfer URL and runs a short-lived skopeo helper pod in `mcp-sentinel` that `curl`s the tar then `skopeo copy`s to `registry.<domain>` (rewritten to in-cluster registry Service DNS). Ensure `k8s/08-runtime-api-rbac.yaml` includes the `mcp-sentinel` registry-push Role and runtime-api serves `/internal/registry-push/tar/{token}`.
- **`server push` fails for larger images (`API 502` / runtime-api `400` after exactly 15s):** every split service runs with the svcboot 15s read/write timeouts; only the push route lifts them per request via `http.ResponseController`. If runtime-api logs `POST /api/v1/runtime/registry/push 400 15.0s` the running image predates that fix — roll `mcp-runtime-api`. The upload window is `MCP_REGISTRY_PUSH_UPLOAD_TIMEOUT` on `mcp-runtime-api` (default `20m`, Go duration); a slow upload now returns `408` naming it and an oversize archive `413` (512 MiB limit). The platform host routes `/api/v1/runtime/registry/push` straight from Traefik to runtime-api (no UI or platform-api hop). Repo-managed Traefik v2.10 has no entrypoint read timeout; k3s-bundled Traefik v3 (and v2.11.2+) defaults `respondingTimeouts.readTimeout` to 60s, so raise it with a `HelmChartConfig` (`--entryPoints.websecure.transport.respondingTimeouts.readTimeout=20m`) when uploads take longer. A bare `API 502` in the CLI now explains that a proxy closed the upload.
- **`server deploy --scope org` → `403 org scope is not enabled on this platform`:** org/public scopes follow `PLATFORM_MODE` on runtime-api (tenant: `tenant` only; org: `tenant, org`; public: `tenant, public`). `server push` applies the same rule, so both commands now reject a disabled scope with the mode and enabled scopes. Use `--scope tenant` or omit `--scope` and set `scope: tenant` in `.mcp` metadata.
- **`spec.auth.audience` appears in an MCPServer although `.mcp` has none:** the current CLI, runtime-api, and admission webhook never persist a derived audience/issuer (`ResolveDerivedAuth` runs only on the operator's reconcile copy). A persisted derived value means the cluster runs an operator/webhook older than that change; roll `mcp-runtime-operator-controller-manager` (the targeted `hack/deploy/mcpruntime-org/rollout.sh` only rolls the Sentinel services). `server deploy --update` then replaces the spec and drops the stale value.
- **`admin registry push` prints a ClusterIP ref:** the in-cluster helper pushes to the registry Service endpoint; the CLI now also prints `Image available to deploy as registry.<domain>/...`. Deploy that public ref, not the helper destination.
- **Operator lost `OAUTH_INTERNAL_ISSUER_URL` after a setup rerun:** setup now derives it from cluster state whenever the bundled `mcp-auth-server` Service exists in `mcp-sentinel`, so reruns that skip the mcp-auth step keep it. An explicit `OAUTH_INTERNAL_ISSUER_URL` env still wins.
- **Private / HTTP in-cluster registry / k3s:** Pull and push can fail with `https` vs `http` or `registry.local` DNS on nodes. See **k3s and HTTP registry (config files)** below, set **`MCP_REGISTRY_*`** before `server generate` when you want `ClusterIP:port` in manifests, and raise **`MCP_DEPLOYMENT_TIMEOUT`** if setup rollouts time out on slow first pulls.
- **Prod DNS / ACME:** with `MCP_PLATFORM_DOMAIN=example.com`, setup derives `registry.example.com`, `mcp.example.com`, and `platform.example.com`. All three public DNS records must point at the ingress IP and port 80 must reach Traefik for HTTP-01. If cert-manager reports NXDOMAIN, verify from outside and inside the cluster: `getent hosts registry.example.com`, `getent hosts mcp.example.com`, `getent hosts platform.example.com`, and `kubectl run dns-check --rm -i --restart=Never --image=busybox:1.36 -- nslookup platform.example.com`.
- **cert-manager "already installed" but TLS times out:** setup's installed check only tests for CRD existence, not pod health. After a k3s restart or cluster disruption, CRDs survive but pods may be gone. If `kubectl get pods -n cert-manager` shows no Running pods, reinstall: `curl -sL https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml | kubectl apply -f -` and wait for all three pods to be Ready before rerunning setup.
- **Multiple kubeconfigs / wrong cluster targeted by setup:** always pass `--kubeconfig <path>` explicitly when your workstation has more than one kubeconfig. The `KUBECONFIG` env var alone is not sufficient — TLS and cert-manager operations inside setup use `platformKubernetesClients()` which requires the explicit path to be set (fixed in `internal/cli/setup/platform/kube_client.go`). Without this, ClusterIssuer and Certificate resources land on the wrong cluster and the cert never issues on the target cluster.
- **Sentinel pods ImagePullBackOff after setup with registry host drift:** `MCP_REGISTRY_HOST` is the public ingress hostname alias for external access; it must not silently override the platform image pull endpoint. For bundled HTTPS public installs, set `MCP_REGISTRY_ENDPOINT=registry.<domain>` so kubelet pulls match the public TLS certificate. For private HTTP or node-local registry paths, set `MCP_REGISTRY_ENDPOINT` / `MCP_REGISTRY_PULL_HOST` to the exact node-pullable host:port and configure node trust or insecure-registry settings for that exact host.
- **Tenant MCPServer ImagePullBackOff with registry image refs:** `server generate` rewrites MCPServer `spec.image` to `MCP_REGISTRY_PULL_HOST` / `MCP_REGISTRY_ENDPOINT` (default `registry.registry.svc.cluster.local:5000`) so tenant pods use the configured node-pullable registry endpoint. Keep `MCP_REGISTRY_INGRESS_HOST` for workstation `docker push` and `server push`; set `MCP_REGISTRY_PULL_HOST` before `server generate` when tenant pods need a different pull address than platform setup uses. Platform-backed `server deploy` provisions an `mcp-runtime-registry-pull` dockerconfig secret in the target namespace and attaches it to the `mcp-workload` SA. Raw manifest apply and direct `server apply --use-kube` bypass that platform provisioning. Pull secrets must target `mcp-workload`, not `default` — the operator's `ApplyRestrictedPodDefaults` sets `serviceAccountName: mcp-workload` on all MCPServer pods.
- **registry-allow-ingress NetworkPolicy blocks k3s Traefik:** portable rules live in `config/registry/base/networkpolicy.yaml` (repo-managed Traefik in the `traefik` namespace). k3s runs Traefik in `kube-system`; setup applies `config/registry/overlays/compatibility/k3s` automatically when it detects k3s or kube-system Traefik. Manual refresh: `kubectl kustomize config/registry/overlays/compatibility/k3s | kubectl apply -f -`. See `config/registry/overlays/compatibility/README.md`.
- **k3s ≥1.35 NetworkPolicy ipset stale for new pods (registry unreachable from new pods):** On k3s 1.35.x+k3s1, kube-router may not refresh namespace-based policy ipsets for pods created after the policy is first evaluated. Symptoms: long-running pods can reach `registry.registry.svc.cluster.local:5000` but newly-created pods in the same namespaces get "Connection refused". The k3s compatibility overlay adds `ipBlock: 10.0.0.0/8` (covers default k3s/flannel pod CIDRs). If your pod CIDR differs, patch the overlay or add an `ipBlock` for it. `cluster doctor` registry reachability checks fail until the overlay is applied.
- **Platform UI 404 / wrong host:** when `MCP_PLATFORM_DOMAIN` (or `MCP_PLATFORM_INGRESS_HOST`) is set, setup applies a host-based ingress `mcp-sentinel-platform-ui` in `mcp-sentinel`, a sibling `mcp-sentinel-platform-observability` Ingress for `/grafana`, and, when TLS is enabled, `mcp-sentinel-platform-ui-http` for HTTP→HTTPS redirect. Verify with `kubectl get ingress mcp-sentinel-platform-ui mcp-sentinel-platform-observability -n mcp-sentinel -o yaml`; the UI rule should be host=`platform.<domain>` routing `/` to `mcp-sentinel-ui:8082` and `/api/v1/*` to the split API services, while observability routes `/grafana` to `grafana:3000` with `sentinel-admin-auth@file`. A direct `/prometheus` route is intentionally absent. If the dashboard returns Traefik default 404, check that DNS resolves `platform.<domain>` to the cluster ingress, then `kubectl logs -n traefik deploy/traefik --tail=120` for routing errors. The dev path-based gateway (`mcp-sentinel-gateway`) keeps working when `MCP_PLATFORM_DOMAIN` is unset.
- **Duplicate Traefik:** setup reuses an active external Traefik such as k3s `kube-system/traefik` and refuses `--force-ingress-install` when that would install repo-managed `traefik/traefik` as a second stack. Remove one Traefik install, or rerun setup with `--ingress none` when your platform ingress is already managed outside this repo. External ingress controllers must provide an equivalent registry admin-auth guard to `/api/v1/registry/authz` before exposing `registry.<domain>` publicly.
- **Adapter certificate setup cannot identify Traefik:** when `MCP_ADAPTER_CERTIFICATES=true`, setup needs the Traefik Deployment namespace, selector labels, and service account to constrain gateway traffic and verify the ingress SPIFFE identity. Check Kubernetes read access to the Traefik Deployment; set `PLATFORM_TRAEFIK_NAMESPACE` if it is outside the detected namespace, then retry setup. Adapter certificates remain disabled unless explicitly enabled.
- **Prod registry 404 / image pulls say “not found”:** if `registry-cert` is Ready but pods fail to pull `registry.<domain>/<repo>:<tag>`, check the public registry route with an admin key: `curl -k -i -H "x-api-key: $ADMIN_API_KEY" https://registry.<domain>/v2/`. Expected is HTTP 200 with `docker-distribution-api-version: registry/2.0`; HTTP 401/403 means the route is active but admin auth was missing or rejected; Traefik `404 page not found` means the ingress/router is not active. Check `kubectl logs -n traefik deploy/traefik --tail=120` and `kubectl get ingress registry -n registry -o yaml`. In prod, the registry ingress must not reference the dev-only `pii-redactor@file` middleware.
- **Traefik 404 on the registry host (even anonymous `/v2/`):** check `kubectl get ingress registry -n registry -o jsonpath='rules={.spec.rules[*].host} tls={.spec.tls[*].hosts}'`. `rules=registry.local` with `tls=registry.<domain>` means the non-TLS base manifest was applied over a public install (apply replaces `spec.rules`, keeps `spec.tls`); Traefik has no router for the public host, so node pulls fail with `NotFound`. `mcp-runtime cluster doctor` ("registry Ingress hosts") prints the exact `kubectl patch ingress registry -n registry --type=json ...` fix; afterwards anonymous `/v2/` must return 401. Setup and `k8sclient.ApplyManifestYAML` now refuse this downgrade. See `docs/k3s-deployment-runbook.md` → "Traefik 404 on the registry host".
- **Prod MCP server URLs:** prefer path-based public routing for clients: `https://mcp.<domain>/<server-name>/mcp`. Use `spec.publicPathPrefix: <server-name>` and set the server’s `MCP_PATH` to `/<server-name>/mcp`; `mcp-runtime server deploy` does this automatically for its default `/<name>/mcp` route. Avoid examples that require a custom `Host` header such as `go.example.local`.
- **`setup` fails with `failed to install CRD: exit status 1`:** the CLI resolves manifests as repo-relative paths — the MCPServer CRD, the ingress overlays, and the registry overlays are all looked up against the working directory, and nothing is embedded. Run `setup` from the repository root. This bites when the CLI is launched over SSH or from a service manager, where the working directory is the login home rather than the checkout. The in-cluster image push writes its `docker save` tar to the working directory too (`os.CreateTemp(".", ...)`, to satisfy kubectl path validation), so that directory must also be writable.
- **Deployment timeout with no failing container, pods `Evicted` or `ContainerStatusUnknown`:** the node is out of ephemeral storage, not slow. Check `kubectl get nodes -o jsonpath='{..conditions[?(@.type=="DiskPressure")].status}'` and `df -h /` on the node; the kubelet evicts pods once free space crosses its threshold and setup only reports the resulting rollout timeout minutes later. Setup builds one image per service component, so a node that also builds images fills up quickly — reclaim with `docker system prune -af --volumes` (safe alongside k3s, which uses containerd, not Docker).
- **Operator image build fails with `invalid go version '1.24.0': must match format 1.23`:** the `go` on `PATH` predates 1.21 and cannot fetch the toolchain `go.mod` asks for — Ubuntu 22.04 ships Go 1.18 at `/usr/bin/go` while a current toolchain often sits unused at `/usr/local/go/bin`. Put the newer toolchain first on `PATH`, or install one matching the `go` directive. Probe candidates with `GOTOOLCHAIN=local go env GOVERSION` so asking the question does not itself trigger a download.
- **`kubectl wait ... --all` returns `error: no matching resources found` immediately on a fresh cluster:** `wait` does not wait for a resource to *appear*; it exits non-zero the moment the selector matches nothing. k3s writes its kubeconfig before the kubelet registers a Node, so scripts that install k3s and wait in the next breath race it. Poll until at least one object exists, then wait on its condition.
- **Doctor Traefik checks fail right after a k3s install:** k3s applies its bundled Traefik through helm-controller *after* the node reports Ready, so an immediate preflight sees no IngressClass, no deployment, and no web entrypoint. Wait for `kubectl -n kube-system rollout status deploy/traefik` before asserting on ingress. Exposure is satisfied by either a LoadBalancer address or a NodePort, matching the doctor check.

## MCP client-side debugging (Cursor, Claude Desktop)

When a real client cannot connect but `curl` against the server looks fine, **read the
client's own log before changing anything on the cluster**. The client performs discovery,
metadata validation, DCR, and PKCE that curl does not, and it is usually the only place the
real error appears.

### Cursor

```bash
D=$(ls -td ~/Library/Application\ Support/Cursor/logs/*/ | head -1)
tail -n 100 "$D"/mcp-server-user-*.log
```

Cursor writes MCP logs in two places and **the obvious one is the wrong one**. A glob like
`logs/**/MCP*.log` matches only the legacy `exthost` files, which can sit frozen for hours
while the connection is actively failing elsewhere. Always sort by mtime.

| File (relative to the launch log dir) | Contents |
|------|----------|
| `mcp-server-user-<name>.log` | **The live, authoritative log** — connect attempts, OAuth state machine, the real error |
| `window<N>/mcp-server-user-<name>.workbench.log` | `[MCPService] createClient completed … statusType=…`, one line per attempt |
| `window<N>/workbench.mcp.allowlist.log` | Tool invocation and permission decisions — proof a tool actually ran |
| `window<N>/workbench.mcp.oauth.log`, `mcpprocess.log` | OAuth and MCP process detail |
| `exthost/anysphere.cursor-mcp/MCP user-<name>.log` | **Legacy** — often stale; do not conclude from it |
| `exthost/anysphere.cursor-mcp/MCP Logs.log` | `Received MCP OAuth return-to-Cursor deeplink`, connectivity |

A deeplink in `MCP Logs.log` with no matching activity in the legacy per-server log means the
flow is running and being logged at the root instead — not that the client is stuck.

Cursor creates a **new timestamped log directory per app launch** and a new
`window<N>_wb<M>` subdirectory per workspace reload, so the tree accumulates dozens of stale
files. Sort by mtime before trusting anything:

```bash
ls -t ~/Library/Application\ Support/Cursor/logs/**/MCP\ user-*.log | head -5
```

A per-server log whose last line is older than your most recent fix means **the client never
retried** — the log is stale evidence, not a failure. `DeleteClient action, reason:
mcp_process_client_factory_changed` is the last line when the server was removed or edited in
`mcp.json`.

### Reading the Cursor OAuth flow

Healthy sequence in `MCP user-<name>.log`:

```
CreateClient action → MCP OAuth provider initialized → Registration lock acquired
→ Persisting new OAuth client registration → Saving PKCE code verifier
→ MCP OAuth redirect to authorization → statusType: needsAuth
```

`needsAuth` is **not** an error — it means the client is waiting for you to finish the
browser flow. Completion shows as `Received MCP OAuth return-to-Cursor deeplink` in
`MCP Logs.log`. A deeplink with no matching per-server activity usually means it was produced
by a manual/scripted flow using the `cursor://` redirect URI, not by Cursor itself.

### Symptom → cause

- **`Error POSTing to endpoint: 404 page not found`, then `SSE error: Non-200 status code (404)`:**
  the SSE line is a red herring — Cursor falls back to the deprecated SSE transport after
  Streamable HTTP fails. Diagnose the **first** 404. Common cause: the authorization server
  does not serve RFC 8414 discovery for a path-mounted issuer. For issuer
  `https://auth.<domain>/<path>`, the client requests
  `/.well-known/oauth-authorization-server/<path>` (RFC 8414 §3.1 — suffix, not
  `<path>/.well-known/...`). Verify both forms:
  ```bash
  curl -s -o /dev/null -w '%{http_code}\n' https://auth.<domain>/.well-known/oauth-authorization-server/<path>
  curl -s -o /dev/null -w '%{http_code}\n' https://auth.<domain>/.well-known/openid-configuration/<path>
  ```
  Do **not** paper this over with a Traefik `replacePathRegex` middleware; the authorization
  server has to serve it, or every other client hits the same wall.
- **`Invalid input: expected array, received undefined` for `subject_types_supported` /
  `id_token_signing_alg_values_supported`:** Cursor validates the discovery document against
  the **OpenID Connect Discovery** schema, which requires fields RFC 8414 lists as optional.
  The whole document is rejected, so the flow dies before the browser ever opens. The AS must
  advertise them even for a pure OAuth 2.1 deployment.
- **Browser never reaches the upstream IdP (Keycloak) consent page:** check the consent form's
  `action` attribute. A root-relative `action="/authorize/consent"` 404s when the issuer is
  path-mounted; it must be absolute and issuer-prefixed.
  ```bash
  curl -s '<authorize-url>' | grep -o 'action="[^"]*"'
  ```
- **`Server returned 403 after trying upscoping` (Cursor), or a 403 with an empty body:** the
  resource server enforces a required scope that its RFC 9728 document does not advertise.
  "Upscoping" is the client retrying with a larger scope; it has nothing to ask for when
  `scopes_supported` is absent, so it gives up. Confirm by decoding the token — `"scope": ""`
  while the 403 challenge names a scope:
  ```bash
  curl -s https://mcp.<domain>/<server>/.well-known/oauth-protected-resource/... | jq .scopes_supported
  ```
  The fix belongs in the resource server, not the client: derive `scopes_supported` from the
  verifier's required scopes (`mcpauth.ProtectedResourceMetadataHandler`) rather than
  hand-writing the JSON, so the advertised set cannot drift from the enforced one. Note the AS
  metadata advertising the scope is **not** enough — clients read the resource's document.
- **Client re-registers on every attempt:** RFC 7591 requires the registration response to
  echo `client_id_issued_at`, `grant_types`, `response_types`, and `scope`. Clients that
  cannot read back what they registered treat the stored registration as unusable.
- **`400 {"error":"resource is not recognized"}` from the token or authorize endpoint:** the
  RFC 8707 `resource` the client sends is not in the AS allowlist. On this platform check
  that `MCP_AUTH_RESOURCES` (plural, comma-separated — **not** just `MCP_AUTH_RESOURCE`)
  carries every deployed server's absolute resource URI. See
  `internal/cli/setup/platform/mcp_auth_server.go` and `k8s/23-mcp-auth-server.yaml`.

After any server-side fix, **remove and re-add the server in the client and restart it** —
Cursor caches the DCR client registration and discovery document per server, so a stale
registration survives a server redeploy and reproduces the old error.

### Claude Desktop

```bash
tail -n 100 ~/Library/Logs/Claude/mcp*.log
```
