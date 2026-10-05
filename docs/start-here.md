# Getting Started

Start here if you are new to MCP Runtime. Choose where to run it, then learn
how a server, grant, session, gateway, and client fit together.

## Choose your path

| Your goal | Start with | What you need |
|---|---|---|
| Try the existing public platform | [Hosted Quickstart](hosted-quickstart.md) | A platform account; follow the guide's CLI and example prerequisites |
| Host your own platform | [Deployment and Operations](hosting-overview.md) | A Kubernetes cluster or infrastructure to provision one |
| Change or test MCP Runtime itself | [Development and Testing](contributor/README.md) | A source checkout and a disposable development cluster |

The public platform's quickstart explains which deployment and identity flows
are available there. For a complete governed tool-call exercise, use a platform
with adapter-certificate identity enabled, as described in the tutorials.

## Learn in order

The [Guided Tutorials](learn/README.md) build on each other:

1. [Core Concepts](learn/01-core-concepts.md): understand the resources and policy model.
2. [First Governed Server](learn/02-first-governed-server.md): publish a server,
   grant access, connect a client, and inspect the result.
3. [Multi-Team Access](learn/03-multi-team-access.md): create team boundaries and
   validate delegated access across them.

## Continue with task guides

Once you have a working platform, use [Server and Client Guides](usage-overview.md)
for publishing, team access, client connections, and optional OAuth setup.
Read [Concepts and Architecture](concepts-overview.md) for the underlying model,
or [CLI and API Reference](reference-overview.md) to look up a command or contract.
