# Registry distribution compatibility overlays

Portable registry manifests live under `config/registry/base`. Distribution-specific
compatibility is layered here and applied at install time when setup detects it is needed.

## Overlays

| Path | When applied | Purpose |
|------|----------------|---------|
| `k3s/` | k3s clusters, or when Traefik runs in `kube-system` | Extra ingress NetworkPolicy scoped to the Traefik pods in `kube-system` |

Kind and other clusters that install repo-managed Traefik in the `traefik` namespace only
need the base manifest.

## Manual apply

If you applied base registry YAML before upgrading, refresh compatibility rules on k3s:

```bash
kubectl kustomize config/registry/overlays/compatibility/k3s | kubectl apply -f -
```
