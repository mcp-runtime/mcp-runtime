# Log collector admission

Setup installs Promtail in `mcp-log-collector`. This is the only platform namespace with `privileged` Pod Security admission because Promtail mounts node log paths. Application workloads in `mcp-platform` and `mcp-observability` use `restricted` admission. The labels are declared in `k8s/00-namespace.yaml` and applied on every setup run.

The Promtail ServiceAccount and ConfigMap live beside its DaemonSet. Its `promtail-node-log-collector` ClusterRoleBinding grants Kubernetes metadata discovery to that ServiceAccount. Setup waits for the DaemonSet rollout and leaves these resources in place. Loki receives logs at `loki.mcp-observability.svc:3100`.

Promtail mounts only the host's `/var/log/pods` directory, read-only. Its single `kubernetes-pods` job discovers pods in every namespace and labels each stream with `namespace`, `pod`, `container`, `node`, and `app`. Promtail limits discovery to pods on its own node by comparing pod node names with its `HOSTNAME` environment variable, so the DaemonSet sets `HOSTNAME` from `spec.nodeName`. Without that setting, `HOSTNAME` is the Promtail pod name, discovery matches no pods, and Loki receives no Kubernetes streams.

Verify delivery with the observability E2E scenario and check the DaemonSet remains ready after setup. Namespace and RBAC checks alone do not prove delivery to Loki.
