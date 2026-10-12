# Reference platform operations

Kubernetes distribution is a deployment choice. These scripts compose Runtime
CLI workflows for the public reference platform and retain a narrowly scoped
k3s host backup path.

Copy `config/deployments/mcpruntime-org.env.example` to an untracked private
profile. Set `MCP_DEPLOY_REF`, `MCP_KUBE_CONTEXT`, `MCP_SETUP_KUBECONFIG`, and
`MCP_EXPECTED_CLUSTER_UID`. Obtain the UID read-only with:

```bash
kubectl --kubeconfig /path/to/prod-config --context prod-mcp-runtime   get namespace kube-system -o jsonpath='{.metadata.uid}'
```

Never make production the default kube context. Profiles are trusted shell
files: review them, protect their permissions, and never commit credentials.

| Operation | Command | Scope |
|---|---|---|
| Release plan | `rollout.sh --dry-run` | Clean checkout at the selected stable release; CLI component plan |
| Release rollout | `rollout.sh --yes` | All Runtime release components via `update --build`; preserves MCP Auth and scoped pull credentials |
| Existing-platform full reconciliation | `setup.sh --yes` | Strict production setup; existing TLS Secrets; explicit current MCP Auth profile; no automatic restore |
| Configuration/object snapshot | `backup.sh --setup` | Private resource/Secret inventory, not volume data |
| Host/volume recovery bundle | `backup.sh --full --online-copy` | Also verifies SSH host cluster UID; online PVC copy needs database-native recovery validation |
| Reset preview | `clean.sh --dry-run` | Prints selected Runtime namespaces/resources only |
| Deliberate reset | `clean.sh --yes --confirm-cluster UID --recovery-snapshot PATH` | Requires same-cluster completed full backup and checksums; deletes tenant data; preserves system and ingress-controller namespaces |
| Configuration recovery preview | `restore.sh --snapshot PATH --dry-run` | Explicit verified snapshot, never automatic `latest` |
| Configuration recovery | `restore.sh --snapshot PATH --yes` | Selected TLS/config/credentials and API restarts; no force-conflicts; not database/PVC restore |
| Multi-team demo | `multitenancy-test.sh` | Loopback or disposable `.e2e.mcpruntime.org` only; uniquely named demo resources |

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

Demo passwords are generated randomly and stored with mode 0600 in the private profile directory as `demo-credentials-<run-id>.json`. Reusing the same run/profile retains them; treat this directory as credential material and exclude it from shared artifacts. Demo accounts/teams remain on disposable infrastructure for inspection.
