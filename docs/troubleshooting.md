# Troubleshooting

Common errors in deployment, gateway policy, registry, analytics, and
connectivity, with fixes.

## Server deployment

### `tool_side_effect_unknown`

The gateway returned this error on a tool call.

**Cause:** The tool name in the grant or the session does not match a tool listed in
the server's `.mcp/servers.yaml` metadata. The gateway cannot look up the tool's
side-effect class, so it denies the call.

**Fix:**

```bash
# 1. Check what tools are in the metadata
cat .mcp/servers.yaml

# 2. Check what tools the running server actually exposes
mcp-runtime server init --from-server http://localhost:8088 --force

# 3. Validate the grant against the metadata
mcp-runtime server validate --metadata-dir .mcp --grant-file grant.yaml

# 4. Re-apply the corrected grant
mcp-runtime access grant apply --file grant.yaml
```

### `tool_not_granted`

The agent tried to call a tool that is not in any `allow` rule in the active grant.

**Fix:** Add the tool to the grant with `--tool <name>` and re-apply.

### `grant_expired`

Every grant that matches the caller has passed its `spec.expiresAt`.

**Fix:** Re-apply the grant with a later `expiresAt`, or remove the field to make it
open-ended. Sessions issued from the grant cannot outlive it, so the adapter
obtains a new session on its next refresh.

### Server stuck in `Pending` or `NotReady`

```bash
mcp-runtime server status --namespace mcp-team-<slug>
kubectl describe pod -n mcp-team-<slug> -l app=<server-name>
kubectl get events -n mcp-team-<slug> --sort-by='.lastTimestamp'
```

Common causes:

| Symptom | Cause | Fix |
|---|---|---|
| `ImagePullBackOff` | Registry credentials stale | Re-push image; check pull secret |
| `Pending` (no node) | Cluster resource exhaustion | Scale nodes or reduce replicas |
| `CrashLoopBackOff` | Server crashes on start | `mcp-runtime server logs <name> --use-kube` |

### `server push` returns 401

```bash
# Check the registry pull secret is valid
kubectl get secret mcp-runtime-registry-pull -n mcp-team-<slug>

# Re-login and retry
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime server push --image ...
```

## Access control

### Grant applied but calls still denied

1. Confirm the grant exists:
   ```bash
   mcp-runtime access grant list --namespace mcp-team-<slug>
   ```

2. Confirm the session exists and is not expired or revoked:
   ```bash
   mcp-runtime access session list --namespace mcp-team-<slug>
   ```

3. Check the gateway logs for the denial reason:
   ```bash
   mcp-runtime server logs <server-name> --namespace mcp-team-<slug> \
     --use-kube 2>&1 | grep -E "deny|allow|session|grant"
   ```

4. Inspect the effective policy:
   ```bash
   mcp-runtime server policy inspect <server-name> \
     --namespace mcp-team-<slug>
   ```

### Session expired or `session_not_found`

The adapter auto-refreshes sessions when started with `--auto-refresh`. If you are
using manual sessions:

Use the active managed agent ID and team ID for this server. The sample ID
below only shows the format; replace it with an ID from the team's agent
directory.

```bash
# Create a new session
mcp-runtime access session init new-session \
  --server <name> --namespace mcp-team-<slug> \
  --human-id <user-id> --team-id <team-id> \
  --agent-id agt_01arz3ndektsv4rrffq69g5fav --trust low --expires-in 4h \
  --output session.yaml

MCP_PLATFORM_API_PROFILE=admin \
  mcp-runtime access session apply --file session.yaml
```

## Registry and images

### `x509: certificate signed by unknown authority`

The cluster node does not trust the registry's TLS certificate.

For `bundled-https` mode, the registry uses the internal `mcp-runtime-ca`. Nodes
must trust this CA. See [Cluster Readiness](cluster-readiness.md) for distribution-
specific node trust configuration.

### `no basic auth credentials` on image pull

The `mcp-runtime-registry-pull` pull secret in the server's namespace has stale
credentials. This happens after a `mcp-runtime setup` rerun that rotates API keys.

```bash
# Check secret exists
kubectl get secret mcp-runtime-registry-pull -n mcp-team-<slug>

# Re-run setup or re-create the secret manually:
ADMIN_KEY=$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.UI_API_KEY}' | base64 -d)

kubectl create secret docker-registry mcp-runtime-registry-pull \
  -n mcp-team-<slug> \
  --docker-server=registry.example.com \
  --docker-username=platform-service \
  --docker-password="$ADMIN_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -
```

## Analytics and observability

### Tool calls not showing in the Analytics dashboard

1. Check the ingest service is receiving events:
   ```bash
   KUBECONFIG=~/.kube/config mcp-runtime sentinel logs ingest --since 5m
   ```
   `401` errors mean the analytics API key in the gateway sidecar is stale.
   Re-run `setup` or restart the analytics deployments.

2. Check the processor is consuming from Kafka:
   ```bash
   KUBECONFIG=~/.kube/config mcp-runtime sentinel logs processor --since 5m
   ```

3. Check Kafka has the `mcp.events` topic:
   ```bash
   kubectl exec -n mcp-sentinel kafka-0 -- \
     kafka-topics --list --bootstrap-server localhost:9092
   ```
   If `mcp.events` is missing, inspect `job/kafka-topic-init` and rerun `setup`.

4. Check the three-broker KRaft quorum and replicas:
   ```bash
   kubectl get pods -n mcp-sentinel -l app=kafka -o wide
   kubectl exec -n mcp-sentinel kafka-0 -- \
     kafka-metadata-quorum --bootstrap-server localhost:9092 describe --status
   kubectl exec -n mcp-sentinel kafka-0 -- \
     kafka-topics --bootstrap-server localhost:9092 --describe --topic mcp.events
   ```
   Healthy output shows three Kafka pods, three `mcp.events` partitions, replica
   factor `3`, and all assigned replicas in ISR.

5. If Kafka reports `InconsistentClusterIdException`, do not delete the Kafka
   PVC automatically. The stored broker metadata no longer matches the
   configured KRaft cluster ID. Back up any data you need, scale Kafka to zero,
   delete all three `kafka-data-kafka-{0,1,2}` PVCs, then rerun setup.

### Split Sentinel API returns 401

The split API pods (`mcp-platform-api`, `mcp-runtime-api`, `mcp-analytics-api`) may have started with stale API keys from a previous
setup run.

```bash
# Restart to pick up current keys
kubectl rollout restart deployment/mcp-platform-api deployment/mcp-runtime-api deployment/mcp-analytics-api -n mcp-sentinel
kubectl rollout status deployment/mcp-platform-api -n mcp-sentinel --timeout=120s
```

## Platform and cluster health

### `cluster diagnostics` reports failures

```bash
KUBECONFIG=~/.kube/config mcp-runtime cluster diagnostics
```

Diagnostics runs the post-setup check suite and prints a remedy for each
failure. Follow the printed instructions. Most failures are missing ingress,
stale certificates, or image pull errors, and the remedy includes the `kubectl`
commands to fix them.

Before setup, run `KUBECONFIG=~/.kube/config mcp-runtime cluster doctor`.

### Setup pre-flight check blocked by stale Certificate

```
ERROR  Stale Certificate "registry-cert" has DNS names [registry.local]
       but the expected registry host is "registry.example.com"
```

```bash
kubectl delete certificate -n registry registry-cert
kubectl delete certificaterequest -n registry --all
# Re-run setup
```

### Namespace stuck in Terminating

```bash
kubectl patch ns <namespace> \
  -p '{"metadata":{"finalizers":null}}' \
  --type=merge
```

## Getting more help

- Run `mcp-runtime <command> --help` for flag reference
- Run `mcp-runtime cluster diagnostics` for the full post-setup cluster diagnostic
- Check [GitHub Issues](https://github.com/mcp-runtime/mcp-runtime/issues)
- [Contributor Troubleshooting](contributor/troubleshooting.md) for development-environment specific issues
