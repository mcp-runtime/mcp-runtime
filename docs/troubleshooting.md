# Troubleshooting

Common errors in deployment, gateway policy, registry, analytics, and
connectivity, with fixes.

Use the saved platform login for server and access management. Kubernetes
commands below are administrator diagnostics against an explicitly selected
kubeconfig. For production, pass `KUBECONFIG=<production-file>` per command;
keep production out of the default context. Reuse the installation’s saved
configuration for any setup repair.

## Server deployment

### `tool_side_effect_unknown`

The gateway returned this error on a tool call.

**Cause:** The requested tool is absent from the rendered tool inventory, or
has no declared side effect. The gateway cannot determine the tool’s
side-effect class, so it denies the call. Sessions identify the caller and
consent; they do not contain tool inventories.

**Fix:**

```bash
# 1. Check what tools are in the metadata
cat .mcp/servers.yaml

# 2. Check what tools the running server actually exposes
mcp-runtime server init <server-name> --from-server http://localhost:8088 --force

# 3. Validate the grant against the metadata
mcp-runtime server validate --metadata-dir .mcp --grant-file grant.yaml

# 4. Redeploy corrected server metadata and apply the reviewed grant
mcp-runtime server deploy <server-name> --scope tenant --metadata-dir .mcp --update
mcp-runtime access grant apply --file grant.yaml
```

### `tool_not_granted`

The agent tried to call a tool that is not in any `allow` rule in the active grant.

**Fix:** Review the grant’s `toolRules` and `allowedSideEffects`. Add an allow
rule for the intended tool and its side-effect class, validate the manifest
with `server validate --grant-file grant.yaml`, then re-apply it. `--tool` is
a flag on `access grant init`, not on `access grant apply`.

### `grant_expired`

Every grant that matches the caller has passed its `spec.expiresAt`.

**Fix:** Re-apply the grant with a later `expiresAt`, or remove the field to make it
open-ended for a same-team delegation. Cross-team grants must retain an expiry
within `MCP_CROSS_TEAM_GRANT_MAX_TTL` (default seven days). Existing session
expiries do not extend when the grant changes; an adapter using `--auto-refresh`
requests renewed access, subject to the current grant.

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
# A push uses saved platform/registry credentials, not a pod pull secret.
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

The image's registry host must match a pull secret attached to the workload
ServiceAccount. A missing or stale secret, the wrong host, or a credential
without repository access can cause this error. Setup preserves existing
credential values on ordinary reruns; it does not rotate all API keys.

```bash
# Admin diagnostics: inspect secret metadata and ServiceAccount references.
kubectl get secret mcp-runtime-registry-pull -n mcp-team-<slug>
kubectl get serviceaccount mcp-workload -n mcp-team-<slug> -o yaml
kubectl describe pod -n mcp-team-<slug> <pod-name>

# Repair managed server pull wiring through the platform deployment path.
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime server deploy <server-name> --scope tenant --metadata-dir .mcp --update
```

If that workflow fails, retain its error and diagnose the registry/platform API
failure. Do not copy a platform service key into a hand-written tenant Secret.

## Analytics and observability

### Tool calls not showing in the Analytics dashboard

1. Check the ingest service is receiving events:
   ```bash
   mcp-runtime sentinel logs ingest --since 5m
   ```
   `401` errors mean the analytics API key in the gateway sidecar is stale.
   Check the server’s analytics Secret reference and ingest logs. Repair using
   the saved setup configuration; restarting alone does not fix a wrong key.

2. Check the processor is consuming from Kafka:
   ```bash
   mcp-runtime sentinel logs processor --since 5m
   ```

3. Check Kafka has the `mcp.events` topic:
   ```bash
   kubectl exec -n mcp-observability kafka-0 -- \
     kafka-topics --list --bootstrap-server localhost:9092
   ```
   If `mcp.events` is missing, inspect `job/kafka-topic-init` and rerun `setup`.

4. Check the three-broker KRaft quorum and replicas:
   ```bash
   kubectl get pods -n mcp-observability -l app=kafka -o wide
   kubectl exec -n mcp-observability kafka-0 -- \
     kafka-metadata-quorum --bootstrap-server localhost:9092 describe --status
   kubectl exec -n mcp-observability kafka-0 -- \
     kafka-topics --bootstrap-server localhost:9092 --describe --topic mcp.events
   ```
   Healthy output shows three Kafka pods, three `mcp.events` partitions, replica
   factor `3`, and all assigned replicas in ISR.

5. If Kafka reports `InconsistentClusterIdException`, inspect its configured
   cluster ID and persisted broker metadata. Restore matching metadata or use
   the documented disaster-recovery process. Deleting broker PVCs discards
   queued audit events and is not a routine troubleshooting step.

### Split Sentinel API returns 401

The split API pods (`mcp-platform-api`, `mcp-runtime-api`, `mcp-analytics-api`) may have started with stale API keys from a previous
setup run.

```bash
# After correcting configuration through the supported setup path,
# restart only a component that still needs to reload its credentials.
mcp-runtime sentinel restart runtime-api
mcp-runtime sentinel status
```

A `401` alone does not establish key drift. Check the saved login with
`mcp-runtime auth status`, identify the rejecting service, and distinguish an
expired user credential from service-to-service authentication failure.

## Platform and cluster health

### `cluster diagnostics` reports failures

```bash
mcp-runtime cluster diagnostics
```

Diagnostics runs the post-setup check suite and prints a remedy for each
failure. Follow the printed instructions. Most failures are missing ingress,
stale certificates, or image pull errors, and the remedy includes the `kubectl`
commands to fix them.

Before setup, run `mcp-runtime cluster doctor`.

### Setup pre-flight check blocked by stale Certificate

```
ERROR  Stale Certificate "registry-cert" has DNS names [registry.local]
       but the expected registry host is "registry.example.com"
```

Inspect the installed Certificate and Ingress without deleting them:

```bash
kubectl get certificate registry-cert -n registry -o yaml
kubectl get ingress -n registry -o yaml
mcp-runtime cluster cert --help
```

Compare their hostnames with the saved install profile. Use the supported
certificate/configuration workflow to repair that mismatch, preserve existing
TLS Secrets, then rerun the configured setup command. Deleting every
CertificateRequest can affect unrelated issuance and is not a targeted fix.

### Platform stack stuck after node pressure or eviction

After `DiskPressure`, memory pressure, or a node restart, platform and telemetry pods can
be evicted and recreated. The stack is built to converge on its own once the
node recovers:

- Stateful stores (Kafka, ClickHouse, Postgres) run with the
  `mcp-shared-data` PriorityClass and request-serving services with
  `mcp-shared-services`, so data stores are evicted last.
- Kafka runs in KRaft mode with a fixed cluster ID and a persistent volume per
  broker, so a broker restart reuses its own metadata. StatefulSet PVCs are
  retained on delete and scale-down.
- Slow-starting stores and the ingest/processor pods have startup probes, so
  liveness does not restart them while a dependency recovers. Ingest and
  processor pods become ready again on their own once Kafka is healthy.
- `ingest` and `processor` use `imagePullPolicy: IfNotPresent` for pinned
  images, so a restart during a registry outage reuses the node's cached image.

Recovery check and repair:

```bash
mcp-runtime cluster doctor          # reports "sentinel stale pods", Kafka, and ingest readiness
mcp-runtime setup --env-file <saved-install-profile>  # retain registry, TLS, and issuer settings
```

`cluster doctor` flags `Failed` (Evicted, Error, ContainerStatusUnknown) pods
and orphaned `Completed` pods in `mcp-platform` and `mcp-observability`; `mcp-runtime setup` removes
them before re-applying the stack. Pods owned by a Job are left alone.

If Kafka logs `InconsistentClusterIdException`, setup refuses to delete the
broker volume automatically because that discards queued events. Restore the
metadata that matches the volume, or, if losing queued events is acceptable,
reset the Kafka PVCs explicitly and rerun `mcp-runtime setup`.

### Namespace stuck in Terminating

```bash
kubectl describe namespace <namespace>
kubectl get namespace <namespace> -o yaml
```

Check the namespace conditions for unavailable API services, remaining
resources, or finalizers. Repair the responsible controller or delete resources
through their owning management workflow. Removing finalizers blindly can
orphan workloads and does not repair a discovery failure.

## Getting more help

- Run `mcp-runtime <command> --help` for flag reference
- Run `mcp-runtime cluster diagnostics` for the full post-setup cluster diagnostic
- Check [GitHub Issues](https://github.com/mcp-runtime/mcp-runtime/issues)
- [Contributor Troubleshooting](contributor/troubleshooting.md) for development-environment specific issues
