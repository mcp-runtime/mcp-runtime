# Governed Agent Example

This example shows MCP Runtime governing an agent that uses an MCP server through
the gateway. The use case is a support ticket triage agent:

- the agent can call the low-trust `slugify` tool to normalize a ticket title
- the same agent is blocked from calling the medium-trust `upper` tool because
  its active `MCPAgentSession` only consents to `low` trust

The Python entrypoint has two modes:

- `probe` performs deterministic MCP JSON-RPC calls with the same governance
  headers and needs no OpenAI API key
- `agents-sdk` uses the OpenAI Agents SDK with `MCPServerStreamableHttp`

## Framework-Neutral Contract

The platform does not care which agent framework created the agent. OpenAI
Agents SDK is just one example adapter.

Any framework can be governed if it can:

- call the MCP Runtime route, for example
  `http://localhost:18080/governed-agent-demo-mcp/mcp`
- use Streamable HTTP MCP, or send the equivalent MCP JSON-RPC requests
- attach the issued identity and session values on each MCP request

For LangChain, LlamaIndex, CrewAI, a custom Python/Go/Node agent, or an IDE
agent, the platform-side setup stays the same: create an `MCPAccessGrant` and an
`MCPAgentSession` for the subject that the agent presents. If a framework cannot
set custom request headers directly, put a small adapter/proxy in front of the
MCP client that injects the issued headers before forwarding traffic to MCP
Runtime.

## Governance Hand-Off

There are two sides to this example:

- Agent builder: creates the agent and points it at the MCP Runtime route.
- Platform admin: grants that agent access by applying one `MCPAccessGrant` and
  one `MCPAgentSession`.

The agent does not decide its own authority. It only sends the identity and
session headers it was issued:

```text
X-MCP-Human-ID: support-lead
X-MCP-Agent-ID: ticket-triage-agent
X-MCP-Team-ID: <optional-team-id>
X-MCP-Agent-Session: sess-ticket-triage-agent
```

The platform then evaluates:

- `MCPAccessGrant`: which subject can use which tools on which MCP server, and
  the maximum trust the admin allows.
- `MCPAgentSession`: the active consent/session for that same subject, including
  the trust level currently consented.

In this demo, [grant.yaml](deploy/grant.yaml) allows both `slugify` and
`upper`, but [session.yaml](deploy/session.yaml) only consents to `low` trust.
That means `slugify` is allowed and `upper` is blocked with `trust_too_low`.

## Deploy the Governed MCP Server

Start from a local Kind/test-mode cluster with Traefik forwarded to
`localhost:18080`, as described in the root `AGENTS.md`.

Build and publish the workspace assistant sample under a governed demo name:

```bash
go build -o bin/mcp-runtime ./cmd/mcp-runtime

./bin/mcp-runtime server build image governed-agent-demo-mcp \
  --metadata-file examples/governed-agent/deploy/server.metadata.yaml \
  --dockerfile examples/oauth-example-go-2025-11-25/Dockerfile \
  --context examples/oauth-example-go-2025-11-25 \
  --tag latest

IMAGE_REF="$(awk '
  $1 == "image:" { image = $2 }
  $1 == "imageTag:" { tag = $2 }
  END {
    if (image == "" || tag == "") exit 1
    print image ":" tag
  }
' examples/governed-agent/deploy/server.metadata.yaml)"
./bin/mcp-runtime server push --image "${IMAGE_REF}"

rm -rf /tmp/mcp-runtime-governed-agent-manifests
./bin/mcp-runtime pipeline generate \
  --file examples/governed-agent/deploy/server.metadata.yaml \
  --output /tmp/mcp-runtime-governed-agent-manifests
./bin/mcp-runtime pipeline deploy --dir /tmp/mcp-runtime-governed-agent-manifests
```

Apply the grant and low-trust session:

```bash
./bin/mcp-runtime access grant apply --file examples/governed-agent/deploy/grant.yaml
./bin/mcp-runtime access session apply --file examples/governed-agent/deploy/session.yaml
./bin/mcp-runtime server policy inspect governed-agent-demo-mcp --namespace mcp-servers
```

You can provide the same grant and session through the platform UI governance
tab or through the HTTP API, but these manifests are the smallest repeatable
path for local testing.

Wait a few seconds after applying access resources so the gateway sidecar reloads
the rendered policy file.

## Run the Deterministic Governance Probe

```bash
python3 examples/governed-agent/governed_agent.py --mode probe
```

Expected result:

- `slugify allow check` returns HTTP 200 and `ticket-reset-payroll-password`
- `upper deny check` returns HTTP 403 with `{"error":"trust_too_low"}`

## Run the Agent Framework Example

```bash
python3 -m venv /tmp/mcp-runtime-governed-agent-venv
source /tmp/mcp-runtime-governed-agent-venv/bin/activate
pip install -r examples/governed-agent/requirements.txt
export OPENAI_API_KEY=sk-...

python3 examples/governed-agent/governed_agent.py --mode agents-sdk
```

The agent sends these headers on MCP requests:

```text
X-MCP-Human-ID: support-lead
X-MCP-Agent-ID: ticket-triage-agent
X-MCP-Team-ID: <optional-team-id>
X-MCP-Agent-Session: sess-ticket-triage-agent
```

To test a different route or subject, override:

```bash
MCP_AGENT_MCP_URL=http://localhost:18080/governed-agent-demo-mcp/mcp \
MCP_AGENT_HUMAN_ID=support-lead \
MCP_AGENT_ID=ticket-triage-agent \
MCP_AGENT_TEAM_ID= \
MCP_AGENT_SESSION=sess-ticket-triage-agent \
python3 examples/governed-agent/governed_agent.py --mode probe
```

For host-based ingress, also set `MCP_AGENT_HOST_HEADER`.
