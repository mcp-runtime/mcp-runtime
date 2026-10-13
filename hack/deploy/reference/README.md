# Reference platform operations

Kubernetes distribution is a deployment choice. Releases are delivered by
`mcp-runtime update` (the hosted release pipeline) and installs by
`mcp-runtime setup`; these scripts are the **recovery** tools that have no CLI
equivalent: configuration snapshots, the narrowly scoped k3s host backup, a
verified restore and a deliberate reset.

Copy `config/deployments/mcpruntime-org.env.example` to an untracked private
profile. Set `MCP_KUBE_CONTEXT`, `MCP_SETUP_KUBECONFIG`, and
`MCP_EXPECTED_CLUSTER_UID`. Obtain the UID read-only with:

```bash
kubectl --kubeconfig /path/to/prod-config --context prod-mcp-runtime   get namespace kube-system -o jsonpath='{.metadata.uid}'
```

Never make production the default kube context. Profiles are trusted shell
files: review them, protect their permissions, and never commit credentials.

| Operation | Command | Scope |
|---|---|---|
| Release plan / rollout | `mcp-runtime update --to vX.Y.Z --build --expect-cluster-id UID --dry-run` then `--yes` | Replaces `rollout.sh`; run from a clean checkout of the tag with the explicit prod kubeconfig/context |
| Full setup of an existing platform | `mcp-runtime setup --strict-prod --with-tls --provided-tls-secrets --skip-cert-manager-install --expect-cluster-id UID ...` | Replaces `setup.sh`; refuses a wrong cluster and an installed mcp-auth it would drop |
| Configuration/object snapshot | `backup.sh --setup` | Private resource/Secret inventory, not volume data |
| Host/volume recovery bundle | `backup.sh --full --online-copy` | Also verifies SSH host cluster UID; online PVC copy needs database-native recovery validation |
| Reset preview | `reset.sh --dry-run` | Prints selected Runtime namespaces/resources only |
| Deliberate reset | `reset.sh --yes --confirm-cluster UID --recovery-snapshot PATH` | Requires same-cluster completed full backup and checksums; deletes tenant data; preserves system and ingress-controller namespaces |
| Configuration recovery preview | `restore.sh --snapshot PATH --dry-run` | Explicit verified snapshot, never automatic `latest` |
| Configuration recovery | `restore.sh --snapshot PATH --yes` | Selected TLS/config/credentials and API restarts; no force-conflicts; not database/PVC restore |

`lib/` contains sourced helpers, not independent commands. `verify-snapshot.py`
checks completion, file coverage/digests and cluster identity. Recovery scripts
are explicit administrative operations using Kubernetes resource restoration;
they do not count as customer CLI validation. Image updates and normal resource
management use the CLI instead.

Registry publishing uses existing Docker credentials. Authenticate through your
approved credential source (`docker login --password-stdin`); credentials are
never read and copied into workload pull Secrets by these helpers. Updating MCP
Auth is a separate selected `mcp-runtime update --only mcp-auth` operation.

For first installation, use `mcp-runtime setup` with the reviewed issuance
profile. The wrapper intentionally requires existing TLS Secrets for reruns.
See [the full runbook](../../../docs/reference-deployment.md) for backup scope,
external identity-provider backup, fresh setup, restoration and verification.

For the disposable multi-team demo, see [QA automation](../../qa/README.md).
