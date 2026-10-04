# Learn MCP Runtime

Three modules take you from the core concepts to a governed server and
multi-team access. Install the CLI and choose a platform through the
[Quickstart](../hosted-quickstart.md) or [self-hosting guide](../self-hosting.md) first. Each module builds on the previous one; you can also start at the
module that matches your setup.

## Module 1: Core concepts

**What you will learn:** The key abstractions (MCPServer, Grant, Session, Gateway,
Adapter) and how they fit together.

**Time:** 15 minutes reading

[Start Module 1 →](01-core-concepts.md)

## Module 2: Your first governed server

**What you will learn:** Deploy an example server, create a grant, connect an
MCP client, and see traffic in the analytics dashboard.

**Prerequisites:** Module 1 completed; platform account or local cluster running.

**Time:** 30–45 minutes

[Start Module 2 →](02-first-governed-server.md)

## Module 3: Multi-team access

**What you will learn:** Create two teams, grant cross-team access, deploy servers
into separate namespaces, and validate isolation.

**Prerequisites:** Module 2 completed; admin credentials; a platform with
adapter-certificate identity enabled for successful governed calls.

**Time:** 45–60 minutes

[Start Module 3 →](03-multi-team-access.md)

## After the modules

- [CLI reference](../cli-reference.md): every command with flags
- [Troubleshooting](../troubleshooting.md): common errors and fixes
- [API reference](../api-reference.md): full CRD field documentation
