# Concepts and Architecture

This section explains the resource model, identity and policy decisions, and
how the platform components work together. It is useful before designing a
deployment, choosing access rules, or contributing a change.

## Recommended reading order

1. [Core Concepts](core-concepts.md): servers, grants, sessions, trust,
   side effects, gateways, and adapters.
2. [Identity and Authorization](identity-and-authorization.md): platform login,
   human and agent identity, delegated access, session lifetime, and enforcement.
3. [Architecture](architecture.md): control, runtime, policy, and observability
   planes and the paths between their components.

## Put the model to work

- [Server and Client Guides](usage-overview.md): publish a server and authorize client access.
- [Deployment and Operations](hosting-overview.md): host the platform and understand the public example.
- [CLI and API Reference](reference-overview.md): exact commands, resource fields, and endpoint contracts.
- [Implementation Details](internals/README.md): source packages, reconciliation, and request flows.
