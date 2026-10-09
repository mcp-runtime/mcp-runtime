# Cluster Provisioning

<span id="provision-the-reference-cluster"></span>

<span id="k3s-on-prem-cluster"></span>

Provision a Kubernetes cluster for the [reference deployment](reference-deployment.md),
with DNS, TLS, ingress, storage, and registry access. The worked example uses
**K3s** as its Kubernetes distribution; MCP Runtime can also run on the other
distributions described in [Deployment Options](deployment-targets.md).

The node layout, packaged Traefik, ServiceLB labels, install commands, and
container-runtime configuration below are specific to the K3s example. If you
choose another distribution, use its provisioning instructions and the shared
[Cluster Requirements](cluster-readiness.md) checks before installing Runtime.

The four-node layout is a demo or pilot reference design, not an inventory of
the current public deployment. A five-node variant is covered below. The
control plane is not highly available; a production K3s control plane needs
its HA topology with three server nodes and separate datastore backups.

This guide provisions the Runtime cluster. The reference deployment's external
Keycloak identity provider runs separately; its VM and Docker/Caddy lifecycle
are covered in [Public Reference Deployment](reference-deployment.md#identity-provider).

## Reference Topology

| Node | k3s role | Recommended size | Purpose |
|---|---|---|---|
| `mcp-cp-1` | server | 4-8 vCPU, 8-16 GiB RAM | Kubernetes API, scheduler, controller manager, embedded datastore, light platform workloads |
| `mcp-ingress-1` | agent | 2-4 vCPU, 4-8 GiB RAM | Public Traefik ServiceLB node for ports 80 and 443 |
| `mcp-worker-1` | agent | 2-4 vCPU, 4-8 GiB RAM | Platform, operator, registry, and MCP server workloads |
| `mcp-worker-2` | agent | 2-4 vCPU, 4-8 GiB RAM | Extra capacity and scheduling headroom |

For a five-node demo, add `mcp-worker-3` as another general worker. If you need
control-plane high availability, use the k3s HA server topology; adding one
more server node does not provide HA.

This guide assumes all nodes use the same CPU architecture. Standard VPS and
most on-prem x86 servers are `amd64`, so setup builds `linux/amd64` images. Do
not mix `amd64` and `arm64` nodes; MCP Runtime does not yet publish multi-arch
setup images.

## Prerequisites

- Ubuntu 24.04 or another k3s-supported Linux distribution on every node.
- Root or passwordless sudo access on every node.
- Node-to-node network connectivity for k3s and the pod network.
- Public ports 80 and 443 open on the ingress node.
- Kubernetes API port 6443 reachable from worker nodes and your admin
  workstation. Restrict it to trusted IPs when the node has a public address.
- A default storage path. k3s installs `local-path` by default; use a real CSI,
  NFS, Longhorn, or another durable storage class for production.
- DNS records for the platform hosts:

  ```text
  platform.example.com -> <ingress-public-ip>
  registry.example.com -> <ingress-public-ip>
  mcp.example.com      -> <ingress-public-ip>
  ```

Use your own apex domain in place of `example.com`. `MCP_PLATFORM_DOMAIN` takes
the apex only, so `MCP_PLATFORM_DOMAIN=example.com` derives the three names
above.

Let's Encrypt HTTP-01 requires public DNS and public port 80. For private-only
on-prem DNS, use an enterprise cert-manager `ClusterIssuer` or pre-created TLS
secrets in place of `--acme-email`.

## Choose the front door

Pick the public or internal traffic path before installing MCP Runtime. Every
path uses the same three hostnames:

- `platform.example.com` for the dashboard, API, and Grafana.
- `registry.example.com` for OCI registry push and pull flows.
- `mcp.example.com` for MCP server routes such as `/<server-name>/mcp`.

### Direct DNS to k3s Ingress

The simplest public demo shape:

```text
client -> DNS A record -> mcp-ingress-1 public IP -> k3s ServiceLB -> Traefik
```

Use this when you can expose ports 80 and 443 directly on the ingress node or
on a small external load balancer. `--acme-email` works in this shape because
Let's Encrypt HTTP-01 can reach Traefik on port 80.

### Cloudflare, WAF, or public reverse proxy

For internet-facing demos, put Cloudflare, an enterprise WAF, or another
reverse proxy in front of the ingress node:

```text
client -> Cloudflare/WAF/proxy -> origin ingress IP -> k3s ServiceLB -> Traefik
```

In this shape:

- Point the public DNS records at the proxy, not directly at the node, if the
  proxy is meant to hide or shield the origin.
- Configure the proxy origin to forward all three hosts to the ingress node or
  external load balancer.
- Preserve the original `Host` header and `X-Forwarded-Proto`.
  When TLS terminates on this proxy and cluster Traefik is HTTP, Traefik
  overwrites `X-Forwarded-Proto` to `http` on the inner hop. The UI default
  `UI_REQUIRE_HTTPS=auto` then answers `308` to the same HTTPS URL and the
  dashboard loops. Set `UI_REQUIRE_HTTPS=false` and
  `UI_FORCE_SECURE_COOKIE=true` on `mcp-ui` for that shape. Cookies stay
  `Secure`. This is the production external-terminator case, not only a
  non-TLS dev install.
- Do not cache or rewrite `/api`, `/v2`, `/<server-name>/mcp`, or
  `/.well-known/acme-challenge/*`.
- Allow long-lived and streaming HTTP responses for MCP traffic.
- Allow registry blob upload/download behavior, including large request bodies,
  range requests, and Docker/OCI auth headers.
- Restrict origin firewall access to the proxy source ranges when possible, and
  keep those ranges updated from the proxy provider.

`--acme-email` still uses HTTP-01. If the proxy is in front during issuance,
`/.well-known/acme-challenge/*` must pass through to Traefik without auth,
cache, forced HTTPS loops, or WAF blocks. Start with DNS-only/direct records
until cert-manager issues certificates, then enable the proxy after validation.
For private or always-proxied environments, use an enterprise cert-manager
`ClusterIssuer`, proxy-managed origin certificates, or pre-created TLS secrets
in place of public HTTP-01.

Test the registry path through the proxy before you finish the install:

```bash
curl -i https://registry.example.com/v2/
```

Unauthenticated `401` or `403` is healthy. A proxy-generated HTML error,
timeout, body-size error, or cached response means Docker/OCI clients may fail
even if the dashboard works.

### Internal enterprise proxy or load balancer

For private on-prem installs, put an internal reverse proxy, F5/HAProxy/NGINX,
or a private load balancer in front of `mcp-ingress-1`:

```text
internal client -> internal DNS/proxy/LB -> k3s ServiceLB -> Traefik
```

Keep the same hostnames, but resolve them in internal DNS. Use
`--tls-cluster-issuer <issuer-name>` or pre-created TLS secrets so certificates
chain to your enterprise trust store. Public Let's Encrypt ACME works only when
the names and HTTP-01 challenge path are publicly reachable.

## Install k3s

Install the first node as the single k3s server. Keep the packaged k3s Traefik
in `kube-system`: this guide reuses it as the ingress controller and installs
MCP Runtime with `--ingress none`, so the cluster never runs two ingress
stacks.

Run on `mcp-cp-1`:

```bash
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
  --node-name mcp-cp-1 \
  --write-kubeconfig-mode 0644 \
  --node-ip <cp-node-ip> \
  --node-external-ip <cp-node-public-ip> \
  --tls-san <cp-node-public-ip> \
  --tls-san platform.example.com \
  --tls-san registry.example.com \
  --tls-san mcp.example.com" sh -
```

To have MCP Runtime own ingress, add
`--disable traefik` to the server install command. Then run setup without
`--ingress none` so it installs the repo-managed Traefik into the `traefik`
namespace, and leave `PLATFORM_TRAEFIK_NAMESPACE` unset (it defaults to
`traefik`). Every Traefik and ServiceLB check below then uses the `traefik`
namespace in place of `kube-system`.

If nodes have more than one network interface, add `--flannel-iface <iface>` to
the server and every agent install command so pod networking uses the intended
interface.

Get the node token:

```bash
sudo cat /var/lib/rancher/k3s/server/node-token
```

Treat the node token as a secret; it lets other machines join the cluster.

Join each worker. Run this once per agent node, changing the node name and IPs:

```bash
curl -sfL https://get.k3s.io | K3S_URL=https://<cp-node-ip>:6443 \
  K3S_TOKEN=<node-token> \
  sh -s - agent \
  --node-name mcp-ingress-1 \
  --node-ip <worker-node-ip> \
  --node-external-ip <worker-node-public-ip>
```

Repeat for `mcp-worker-1`, `mcp-worker-2`, and optionally `mcp-worker-3`.

## Configure kubectl

From your workstation, copy the kubeconfig from the server node, replace
`127.0.0.1` with the reachable control-plane address, and keep the file
private:

```bash
install -d -m 700 "$HOME/.kube"
PROD_KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config"
scp root@<cp-node-ip>:/etc/rancher/k3s/k3s.yaml "$PROD_KUBECONFIG"
sed -i.bak 's/127.0.0.1/<cp-node-ip>/g' "$PROD_KUBECONFIG"
chmod 0600 "$PROD_KUBECONFIG"
kubectl --kubeconfig "$PROD_KUBECONFIG" get nodes -o wide
```

For macOS, the `sed -i.bak` form works with the default BSD `sed`.
Treat the kubeconfig as a cluster-admin credential and do not commit it.
Keep production out of the default kubeconfig and do not export it as the
ambient `KUBECONFIG`; pass the production file explicitly on each command.

## Pin ServiceLB to the ingress node

k3s ServiceLB schedules load-balancer pods on eligible nodes. For a public
demo, keep ports 80 and 443 on one known public ingress node.

Label the ingress node:

```bash
kubectl --kubeconfig "$PROD_KUBECONFIG" label node mcp-ingress-1 \
  svccontroller.k3s.cattle.io/enablelb=true \
  ingress.mcpruntime.org/public=true \
  node-role.mcpruntime.org/public-ingress=true
```

If another node was labeled for ServiceLB during earlier testing, remove the
ServiceLB label from it:

```bash
kubectl --kubeconfig "$PROD_KUBECONFIG" label node <node-name> svccontroller.k3s.cattle.io/enablelb- --overwrite
```

Verify the `svclb-traefik` pods land only on the ingress node:

```bash
kubectl --kubeconfig "$PROD_KUBECONFIG" -n kube-system get pods -o wide \
  -l svccontroller.k3s.cattle.io/svcname=traefik
```

If one node also serves non-Kubernetes docs or a website with Docker/nginx, keep
that node out of Kubernetes scheduling:

```bash
kubectl --kubeconfig "$PROD_KUBECONFIG" cordon <docs-node-name>
```

A cordoned node stays in the cluster, but no new pods are scheduled on it.

## Preflight Checks

Before installing MCP Runtime, verify the cluster shape:

```bash
kubectl --kubeconfig "$PROD_KUBECONFIG" get nodes -o wide
kubectl --kubeconfig "$PROD_KUBECONFIG" get storageclass
kubectl --kubeconfig "$PROD_KUBECONFIG" -n kube-system get pods
kubectl --kubeconfig "$PROD_KUBECONFIG" get ingressclass
```

Check DNS from your workstation and from inside the cluster:

```bash
dig +short platform.example.com
dig +short registry.example.com
dig +short mcp.example.com

kubectl --kubeconfig "$PROD_KUBECONFIG" run dns-check --rm -i --restart=Never --image=busybox:1.36 -- \
  nslookup platform.example.com
```

All three public names must resolve to the ingress node before using
Let's Encrypt HTTP-01. Port 80 must reach Traefik for certificate issuance.

## Install MCP Runtime

The cluster is now ready for platform installation. Follow
[Platform Installation](self-hosting.md) for CLI installation, registry and TLS
choices, platform setup, and the first server. For the public reference's
saved configuration, installation scripts, and subsequent updates, use the
[Deployment Guide](reference-deployment.md#install-and-update).

When using the K3s-provided Traefik from this example:

- Pass `--ingress none` to setup to reuse the existing ingress controller.
- Set `PLATFORM_TRAEFIK_NAMESPACE=kube-system` and
  `PLATFORM_TEAM_TRAEFIK_WATCH=disabled` in the deployment environment.
- Pass `--kubeconfig "$PROD_KUBECONFIG"` explicitly to setup and other commands
  targeting this cluster.

If you disabled K3s Traefik during provisioning, let setup install the
repo-managed ingress controller instead, as described above.

<span id="enterprise-provided-tls-certificate-files"></span>
<span id="renewal"></span>

Enterprise-supplied certificate installation and renewal are documented in
[Platform Installation](self-hosting.md#enterprise-provided-tls-certificate-files).
These instructions also apply to other Kubernetes distributions.

## Pod egress to an internal network

Team namespaces start with a default-deny NetworkPolicy. DNS, the registry,
same-namespace pods, and the ingress controller are allowed. Other
destinations are refused, including HTTPS APIs on the company network.

Flannel already rewrites pod sources to the node address. Opening those
destinations is a cluster-admin NetworkPolicy choice, not a per-server
address. Set both values when you run setup:

```bash
mcp-runtime setup \
  --pod-egress-cidrs 10.0.0.0/8 \
  --pod-egress-except-cidrs 10.42.0.0/16,10.43.0.0/16
```

`--pod-egress-cidrs` is the destination list. Prefixes must be /8 or longer
for IPv4 and /32 or longer for IPv6, and `0.0.0.0/0` is rejected. The allow
is TCP 443 only. `--pod-egress-except-cidrs` must include the cluster pod
CIDR and service CIDR whenever a destination is private (`10.0.0.0/8`,
`172.16.0.0/12`, or `192.168.0.0/16`), so pods cannot use this rule to reach
other pods or Service IPs. Use the ranges from your Kubernetes install. The
same values can be supplied as `MCP_POD_EGRESS_CIDRS` and
`MCP_POD_EGRESS_EXCEPT_CIDRS`. An empty destination list installs nothing.

Hostnames stay in the MCP server configuration. Do not put a destination
address or a company range in server metadata.

## Validate

Run the platform checks:

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster diagnostics
kubectl --kubeconfig "$PROD_KUBECONFIG" get pods -A
kubectl --kubeconfig "$PROD_KUBECONFIG" get ingress -A
kubectl --kubeconfig "$PROD_KUBECONFIG" get certificate -A
```

Check the public routes:

```bash
curl -I https://platform.example.com/
curl -i https://registry.example.com/v2/
```

The platform route should return `200`. The registry route should return
`401` or `403` without credentials, which means the public registry route is up
and guarded.

Before deploying MCP servers, `https://mcp.example.com/<server>/mcp` can return
404 because no server route exists yet. Follow
[Publish an MCP Server](publish-mcp-server.md) to build, push, deploy, and
verify a real server.

## Five-Node Variant

For a five-node demo, keep the same control-plane and ingress roles and add one
more general worker:

| Node | Role |
|---|---|
| `mcp-cp-1` | k3s server |
| `mcp-ingress-1` | ServiceLB / Traefik public ingress |
| `mcp-worker-1` | general workloads |
| `mcp-worker-2` | general workloads |
| `mcp-worker-3` | general workloads, observability, or larger MCP servers |

Do not label the extra worker with
`svccontroller.k3s.cattle.io/enablelb=true` unless you want ports 80 and 443
spread across more than one public node. Keep DNS pointed at the
node or load balancer that actually receives HTTP and HTTPS traffic.

## Migration Notes

Fresh **first-time** installs do not need certificate backup. Use fresh ACME,
your enterprise issuer, or pre-created TLS secrets.

**Reinstalling on the same public domain** (app-namespace wipe, setup rerun):
Let's Encrypt limits duplicate certificates to five per domain set per seven days.
Use [Reference Deployment - Step 0](reference-deployment.md#step-0-back-up-platform-runtime-state-before-any-wipe)
or `hack/deploy/mcpruntime-org/clean.sh --yes` to back up platform-runtime TLS
before delete, then `hack/deploy/mcpruntime-org/setup.sh` to restore after setup.

Back up cert-manager `Certificate`, `Issuer` or `ClusterIssuer`, and TLS
`Secret` objects when migrating an existing public install that already
owns valid certificates or issuer state. Keep those backups encrypted and out
of git because TLS secrets contain private keys.

## Troubleshooting

| Symptom | Check |
|---|---|
| `exec format error` in a setup-built pod | The image architecture does not match the node. Use a homogeneous cluster and set `MCP_IMAGE_PLATFORM=linux/amd64` or `linux/arm64`. |
| cert-manager reports NXDOMAIN or HTTP-01 failure | `platform`, `registry`, and `mcp` DNS records must point to the ingress IP, and port 80 must reach Traefik. |
| Setup rejects public mode because login is missing | Set `GOOGLE_CLIENT_ID` / `MCP_GOOGLE_CLIENT_ID`, or set `OIDC_ISSUER`, `OIDC_AUDIENCE`, and `OIDC_JWKS_URL`. |
| Setup rejects production admin config | Set `MCP_PLATFORM_ADMIN_EMAIL` or `MCP_ADMIN_USERS`. Do not confuse this with `--acme-email`, which is only the certificate contact. |
| Dashboard has no password login | Set both `MCP_PLATFORM_ADMIN_EMAIL` and `MCP_PLATFORM_ADMIN_PASSWORD` before setup. Email alone does not seed the user. |
| Dashboard or docs return `308` to the same URL | TLS terminates outside the cluster and Traefik is HTTP. Set `UI_REQUIRE_HTTPS=false` and `UI_FORCE_SECURE_COOKIE=true` on `mcp-ui`. |
| Registry setup fails shrinking storage | `--registry-storage` is smaller than the bound PVC. Kubernetes cannot shrink it. Rerun with a size at least as large as the claim. |
| Setup image build says buildx is missing | Install the Docker buildx plugin, or set `DOCKER_BUILDKIT=0` and rerun setup. |
| `server push` returns `405 method_not_allowed` | The API base URL is `http://` and the front door redirects POST to HTTPS. Use the HTTPS origin. |
| `server push` fails on a small request body | The proxy in front of `/api/` is still at the default body limit. Allow a large body on that location. `/v2/` is a different path. |
| Tenant `server push` says the caller has no team | The admin API key is not a team member. Create a team, add a user, and push with that user's token. `--scope org` is disabled in tenant mode. |
| Team MCP server gets `ECONNREFUSED` to an internal HTTPS API the node can open | The team default-deny policy is refusing the pod. Set `--pod-egress-cidrs` and `--pod-egress-except-cidrs`. See [Pod egress to an internal network](#pod-egress-to-an-internal-network). |
| Registry image pulls fail | Confirm `MCP_REGISTRY_ENDPOINT` is the exact host nodes pull, DNS resolves from every node, and the certificate chain is trusted by the node runtime. |
| Setup dies in operator webhook wait with connection refused | The Kubernetes API was down, often right after a k3s restart to load `registries.yaml`. Rerun setup after the API answers. Confirm registry ingress auth was restored. |
| Traefik 404 for the dashboard | Confirm `MCP_PLATFORM_DOMAIN=example.com`, `kubectl get ingress -A`, and DNS for `platform.example.com` points to the ingress node. |
| ServiceLB lands on the wrong node | Check `kubectl get pods -A -o wide | grep svclb` and fix `svccontroller.k3s.cattle.io/enablelb` labels. |
