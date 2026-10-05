# Deployment and Operations

<span id="self-host-and-operate"></span>

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

<span id="follow-the-deployment-lifecycle"></span>

## Installation

Start here when hosting your own platform. These guides apply to managed
and self-managed Kubernetes clusters.

1. [Deployment Options](deployment-targets.md): choose a Kubernetes distribution,
   registry, ingress, TLS, and storage model.
2. [Cluster Requirements](cluster-readiness.md): check node image pulls, DNS,
   ingress, certificates, and storage before installing.
3. [Platform Installation](self-hosting.md): install the CLI, configure the
   platform, check its health, and publish your first server.

## Public reference

Follow the public platform's worked example to see how those choices fit
together in a complete deployment.

1. [Cluster Provisioning](cluster-provisioning.md): build the example node
   topology, configure cluster access, and prepare ingress.
2. [Deployment Guide](reference-deployment.md): configure Runtime and its
   external identity provider, install and update the platform, back up and
   recover data, and verify the deployment.

The provisioning guide hands off to the installation guides once the cluster
is ready. Adapt the example's domains, node layout, credentials, and backup
ownership to your environment before running the reference scripts.

## Operations

Return to these guides after installation for component placement, runtime
behavior, service maintenance, and incident diagnosis.

- [Runtime Operations](runtime-operations.md): server resources, reconciliation,
  routing, and rollouts.
- [Namespaces](namespaces.md): component placement, team boundaries, service DNS,
  and credential ownership.
- [Services and Observability](platform-services.md): platform services,
  gateway enforcement, audit events, metrics, logs, and traces.
- [Troubleshooting](troubleshooting.md): diagnosis of installation, access,
  registry, analytics, and cluster failures.
