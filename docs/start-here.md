# Getting Started

Start here if you are new to MCP Runtime. Choose where to run it, then learn
how a server, grant, session, gateway, and client fit together.

## Choose your path

| Your goal | Start with | What you need |
|---|---|---|
| Try user and team workflows on the public platform | [Public platform walkthrough](hosted-quickstart.md) | A user account and team access; follow the guide's example prerequisites |
| Try setup and platform administration | [Deployment and Operations](hosting-overview.md) | Your own Kubernetes cluster or infrastructure to provision one |
| Change or test MCP Runtime itself | [Development and Testing](contributor/README.md) | A source checkout and a disposable development cluster |

The public platform lets you deploy a sample server, create grants, and see
user and team views. Its example tool calls are denied because it cannot
verify which agent made them. To run the full tool-call exercise or try
platform admin paths, install MCP Runtime in your own environment and follow
the tutorials.

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
