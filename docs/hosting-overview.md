# Self-Host and Operate

This section explains how to host and operate MCP Runtime, using our public
platform at [platform.mcpruntime.org](https://platform.mcpruntime.org) as the
reference deployment. We walk through the infrastructure choices, cluster
preparation, platform installation, identity integration, updates, backups,
and verification that make up a complete deployment.

The public reference uses K3s for its Kubernetes cluster and a separate
Docker/Caddy VM for Keycloak. The guides identify those choices where they
affect commands and configuration. You can apply the same deployment
lifecycle while selecting another Kubernetes distribution, registry, ingress
controller, storage system, or compatible identity provider.

To use the existing public platform, follow the
[Hosted Quickstart](hosted-quickstart.md). The pages in this section are for
operators hosting their own platform or learning how the public reference is
operated.

## Follow the deployment lifecycle

| Task | Guide |
|---|---|
| Choose the Kubernetes distribution and install shape | [Choose a Deployment Target](deployment-targets.md) |
| Check DNS, registry access, ingress, TLS, and storage prerequisites | [Prepare Your Cluster](cluster-readiness.md) |
| Provision infrastructure using the worked cluster example | [Cluster Provisioning](cluster-provisioning.md) |
| Install the CLI and platform on your prepared cluster | [Self-Hosting MCP Runtime](self-hosting.md) |
| Understand the public deployment's topology, configuration, identity provider, updates, backups, and verification | [Public Reference Deployment](reference-deployment.md) |
| Manage server reconciliation, routing, and rollouts | [Runtime Operations](runtime-operations.md) |
| Understand component placement and team workload boundaries | [Namespaces](namespaces.md) |
| Operate platform services, audit, analytics, and telemetry | [Platform Services and Observability](platform-services.md) |
| Diagnose an installation or runtime failure | [Troubleshooting](troubleshooting.md) |

Use the general installation guides to prepare your own deployment, and the
public reference as a concrete example of how the pieces fit together. Adapt
its domains, node layout, credentials, and backup ownership to your
environment before running the reference scripts.
