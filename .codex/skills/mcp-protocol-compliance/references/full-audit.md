
# MCP Protocol Compliance

## Overview

When reviewing a proposed protocol/API shape or versioning decision, also use
[the shared design principles](../../_shared/design-principles.md) for contract
stability and compatibility. Skip it for a conformance-only audit of existing
behavior.

This skill audits MCP Runtime — the proxy/gateway, example servers, agent
adapters, and the CLI probe — against the **upstream Model Context Protocol
specification** at <https://github.com/modelcontextprotocol/modelcontextprotocol>.
The spec is versioned (`2024-11-05`, `2025-03-26`, `2025-06-18`, `draft`); the
runtime currently pins `2025-06-18` (see
`internal/agentadapter/config.go: DefaultProtocolVersion`).

Goals:

- Confirm the runtime correctly implements the spec revision it **claims** to
  support — protocol-version negotiation, Streamable HTTP, JSON-RPC 2.0
  envelope, tools/resources/prompts/sampling shapes, session semantics, error
  codes, and capability advertisement.
- Detect drift from the upstream schema (fields renamed, fields removed,
  required-fields changes) when bumping pinned revision.
- Surface relevant merged SEPs and open SEP PRs that affect runtime behavior.
- Distinguish "spec says X, runtime does X" from "spec says X, runtime does Y"
  from "spec is silent, runtime does Y by choice."

Regression evidence contract: static schema/code checks are only half of this
skill. For protocol-affecting changes, run live `initialize`, positive
`tools/call`, and malformed/unsupported-version negative cases against the Kind
cluster. If upstream fetch or live transport checks cannot run, report the
result **blocked** for that surface.

Non-goals:

- Auth/grant/session policy enforcement → `cluster-ops`.
- Operator/CLI/setup regressions → `cluster-ops`.
- Cluster RBAC/PSS hygiene → `security-audit`.

## Step 1 — Pin the spec revision under audit

```bash
SPEC_REPO="${SPEC_REPO:-https://github.com/modelcontextprotocol/modelcontextprotocol}"
SPEC_REV="${SPEC_REV:-$(grep -oE '"20[0-9]{2}-[0-9]{2}-[0-9]{2}"' \
  internal/agentadapter/config.go | head -1 | tr -d '"')}"
SPEC_REF="${SPEC_REF:-main}"     # upstream branch or tag to fetch from
echo "Auditing runtime against MCP spec rev=$SPEC_REV @ ref=$SPEC_REF"
```

`SPEC_REV` defaults to what the runtime claims (the `DefaultProtocolVersion`
constant). `SPEC_REF` is the upstream git ref to fetch docs/schemas from —
default `main` for the latest published, override to a tag like
`v2025.06.18` if upstream publishes one.

Record both in the report header. A compliance finding is meaningless without
"compliance with **which** revision."

## Step 2 — Fetch the upstream spec artifacts

Cache locally so the skill can re-run without re-fetching.

```bash
MCP_SPEC_TMP="${MCP_SPEC_TMP:-$(mktemp -d)}"
trap 'rm -rf "$MCP_SPEC_TMP"' EXIT
SPEC_CACHE="${SPEC_CACHE:-/tmp/mcp-spec/$SPEC_REF}"
mkdir -p "$SPEC_CACHE"
fetch() {
  local path="$1" url="$SPEC_REPO/raw/$SPEC_REF/$1"
  local out="$SPEC_CACHE/$path"
  mkdir -p "$(dirname "$out")"
  [ -s "$out" ] || curl -fsSL "$url" -o "$out"
  echo "$out"
}

SCHEMA="$(fetch "schema/$SPEC_REV/schema.json")"
CHANGELOG_DRAFT="$(fetch "docs/specification/draft/changelog.mdx")"
SEP_INDEX="$(fetch "docs/seps/index.mdx")"
ROADMAP="$(fetch "docs/development/roadmap.mdx")"
# Per-revision spec body (Streamable HTTP, lifecycle, server features):
SPEC_BASE="docs/specification/$SPEC_REV"
fetch "$SPEC_BASE/basic/transports.mdx" >/dev/null || true
fetch "$SPEC_BASE/basic/lifecycle.mdx"  >/dev/null || true
fetch "$SPEC_BASE/server/tools.mdx"     >/dev/null || true
fetch "$SPEC_BASE/server/resources.mdx" >/dev/null || true
fetch "$SPEC_BASE/server/prompts.mdx"   >/dev/null || true
```

If any fetch fails, record it as a blocker and stop — do not silently audit
against a missing schema. If the spec layout has changed upstream (paths
moved), update this skill rather than guess.

## Step 3 — Inventory runtime claims

Find every place the runtime states a protocol version, advertises
capabilities, or shapes a JSON-RPC message. These are the surfaces that must
match the pinned revision.

```bash
# Protocol version constants and literals.
grep -rIn 'DefaultProtocolVersion\|protocolVersion\|Mcp-Protocol-Version\|MCP-Protocol-Version\|"20[0-9]\{2\}-[0-9]\{2\}-[0-9]\{2\}"' \
  --include='*.go' --include='*.md' --include='*.yaml' \
  -- internal/ services/ examples/ docs/ pkg/ cmd/

# Streamable HTTP transport contract in the gateway:
sed -n '1,$p' services/mcp-gateway/rpc.go services/mcp-gateway/proxy.go \
  services/mcp-gateway/types.go 2>/dev/null | grep -nE \
  'jsonrpc|method|Mcp-Session-Id|content-type|text/event-stream|application/json|notifications/initialized|tools/(list|call)|initialize'

# Agent HTTP adapter protocol surface.
sed -n '1,$p' internal/agentadapter/proxy.go \
  internal/agentadapter/config.go internal/agentadapter/rpc_metadata.go \
  2>/dev/null | grep -nE 'Header\.Set\|Mcp-|protocolVersion|jsonrpc|initialize'
```

The output is the "claims" list for the report. Every later check maps a
claim to spec text or schema and reports match / mismatch / extension.

## Step 4 — Static schema conformance

Validate runtime-side fixtures and golden responses against the upstream
schema. Use a JSON Schema validator that supports the draft used by the spec
(typically draft 2020-12).

```bash
command -v jv >/dev/null || go install github.com/santhosh-tekuri/jsonschema/cmd/jv@latest
JV="$(go env GOPATH)/bin/jv"

# Collect captured MCP envelopes from existing tests and fixtures.
FIXTURES=()
while IFS= read -r fixture; do
  [ -n "$fixture" ] && FIXTURES+=("$fixture")
done < <(grep -rIl --include='*.go' --include='*.json' \
  '"jsonrpc":\s*"2\.0"' \
  -- internal/ services/ examples/ test/ pkg/ 2>/dev/null)
echo "Found ${#FIXTURES[@]} files containing JSON-RPC envelopes"

# Extract message-shaped JSON bodies into a unique temporary directory
# (one tool that does this safely lives in the spec repo's tests; here we
# accept that not every embedded string is round-trippable and only validate
# files where extraction succeeded).
MCP_SPEC_TMP="${MCP_SPEC_TMP:-$(mktemp -d)}"
trap 'rm -rf "$MCP_SPEC_TMP"' EXIT
FIXTURE_TMP="$MCP_SPEC_TMP/fixtures"
mkdir -p "$FIXTURE_TMP"
python3 - "$SPEC_CACHE" "$FIXTURE_TMP" "${FIXTURES[@]}" <<'PY'
import json, re, sys, os
cache, out, files = sys.argv[1], sys.argv[2], sys.argv[3:]
RE = re.compile(r'(\{[^{}]*"jsonrpc"\s*:\s*"2\.0"[^{}]*\})', re.S)
n = 0
for f in files:
    try:
        body = open(f).read()
    except Exception:
        continue
    for m in RE.finditer(body):
        try:
            obj = json.loads(m.group(1))
        except Exception:
            continue
        n += 1
        with open(f"{out}/{os.path.basename(f)}.{n}.json","w") as g:
            json.dump(obj, g)
print(f"Extracted {n} envelopes -> {out}")
PY

# Validate every extracted envelope against the pinned schema.
fail=0
for f in "$FIXTURE_TMP"/*.json; do
  test -e "$f" || continue
  "$JV" "$SCHEMA" "$f" >/dev/null 2>&1 || { fail=$((fail+1)); echo "INVALID: $f"; "$JV" "$SCHEMA" "$f" 2>&1 | head -5; }
done
echo "Schema validation: $fail invalid envelopes"
```

Any **invalid** envelope in `services/mcp-gateway/`, `examples/`, or
`internal/agentadapter/` is a finding (the runtime is emitting / asserting
non-spec shapes). Invalid envelopes in `test/golden/`, `test/integration/`,
or e2e fixtures are findings on the test, not the runtime, but track them
either way.

## Step 5 — Live transport conformance (against the Kind cluster)

Precondition: `contributor-cluster` has run, port-forward is up, and a demo
server is deployed with a valid grant/session. For this dynamic portion, read
`references/live-conformance.md` and run its sub-suite exactly:

- initialize response conformance
- Streamable HTTP headers and `Mcp-Session-Id`
- `tools/list` shape
- `tools/call` result envelope
- JSON-RPC error mapping
- protocol-version negotiation

## Step 6 — Capability advertisement vs implementation

For each capability advertised in `initialize`, confirm the corresponding
method actually works. Missing capability + working method, or advertised
capability + missing method, are both findings.

```bash
caps="$(echo "$INIT" | jq -r '.result.capabilities | keys[]')"
for cap in $caps; do
  case "$cap" in
    tools)     M='tools/list'     ;;
    resources) M='resources/list' ;;
    prompts)   M='prompts/list'   ;;
    logging)   M='logging/setLevel' ;;
    *)         M='' ;;
  esac
  [ -n "$M" ] || continue
  code="$(curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
    -d '{"jsonrpc":"2.0","id":50,"method":"'"$M"'"}' "$BASE" \
    | jq -r '.error.code // "ok"')"
  echo "capability $cap -> $M : $code"
done
```

Cross-check: any method the gateway forwards (`services/mcp-gateway/rpc.go`
method allow-list) that is **not** present in `initialize.capabilities`
should be flagged — the gateway should not encourage clients to call a
capability the server did not advertise.

## Step 7 — SEP and roadmap cross-reference

Read the merged SEP index and roadmap fetched in Step 2; produce a short
table of relevant items and how the runtime stands.

```bash
echo "=== Merged SEPs in index ==="
grep -E '^\s*-\s+SEP-[0-9]+' "$SEP_INDEX" | head -60

echo "=== Draft spec changelog (deltas vs $SPEC_REV) ==="
sed -n '1,80p' "$CHANGELOG_DRAFT"

echo "=== Open SEP PRs (snapshot at audit time) ==="
gh -R modelcontextprotocol/modelcontextprotocol pr list \
  --search 'SEP in:title is:open' --limit 30 \
  --json number,title,labels,updatedAt 2>/dev/null \
  || curl -fsSL "https://api.github.com/repos/modelcontextprotocol/modelcontextprotocol/pulls?state=open&per_page=30" \
       | jq -r '.[] | select(.title|test("SEP";"i")) | "#\(.number) \(.title)"'
```

For each SEP relevant to gateways/transport/auth/sessions/discovery/tasks,
classify the runtime's stance in the report:

- **Implemented** — link to file:line where it lives.
- **Tracked** — design note in docs; no code yet.
- **Not applicable** — explain why (client-only SEP, etc.).
- **Should track** — affects runtime but neither implemented nor documented.

The skill must not invent a SEP list — read the upstream index every run.

## Step 8 — Cross-component coherence

Several places encode protocol assumptions; they must agree.

```bash
# Default protocol version must match across the agent adapter, the CLI doctor
# probe, and the docs.
PIN_AGENT="$(grep -oE '"20[0-9]{2}-[0-9]{2}-[0-9]{2}"' internal/agentadapter/config.go | head -1)"
PIN_DOCTOR="$(grep -oE '"20[0-9]{2}-[0-9]{2}-[0-9]{2}"' internal/cli/cluster/doctor_impl.go | head -1)"
PIN_DOCS="$(grep -oE '20[0-9]{2}-[0-9]{2}-[0-9]{2}' docs/self-hosting.md | head -1)"
echo "agent=$PIN_AGENT doctor=$PIN_DOCTOR docs=$PIN_DOCS"
[ "$PIN_AGENT" = "\"$PIN_DOCS\"" ] && [ "$PIN_AGENT" = "$PIN_DOCTOR" ] \
  || echo "FAIL: protocol version drift across agent adapter / doctor / docs"
```

Also check the example servers and SDK pin:

```bash
grep -nE 'modelcontextprotocol/(go-sdk|python-sdk|typescript-sdk)\b' \
  examples/*/go.mod examples/*/requirements.txt examples/*/package.json 2>/dev/null
```

A runtime that pins `2025-06-18` but ships an example using an older SDK
that does not yet emit that version is a documentation finding (the
contributor flow will fail before the audit even starts).

## Step 9 — Forward-compatibility probe (optional)

Validate the same fixtures against `schema/draft/schema.json` to see what
breaks under the upcoming revision. Findings here are **Info** unless the
runtime explicitly claims draft support.

```bash
DRAFT_SCHEMA="$(fetch 'schema/draft/schema.json')"
fail=0
shopt -s nullglob
draft_inputs=()
test -n "${FIXTURE_TMP:-}" && draft_inputs+=("$FIXTURE_TMP"/*.json)
test -n "${MCP_SPEC_TMP:-}" && draft_inputs+=("$MCP_SPEC_TMP"/mcp-init.json "$MCP_SPEC_TMP"/mcp-tools.json "$MCP_SPEC_TMP"/mcp-call.json)
for f in "${draft_inputs[@]}"; do
  [ -s "$f" ] || continue
  "$JV" "$DRAFT_SCHEMA" "$f" >/dev/null 2>&1 || fail=$((fail+1))
done
echo "Draft-schema validation failures: $fail (informational)"
```

## Step 10 — Severity rubric (compliance-specific)

Map findings to `../_shared/FINDINGS-TEMPLATE.md` severities using this
domain-specific rubric. Pick the highest that applies:

- **Critical** — Runtime claims protocol version X, but a conforming X
  client cannot complete `initialize` + `tools/call` against it; or the
  gateway corrupts/strips spec-required headers; or the runtime emits
  responses that fail schema validation against the version it advertised.
- **High** — Required behavior missing (no `Mcp-Session-Id`, no
  `DELETE` session handling, wrong JSON-RPC error codes, capability
  advertised but corresponding method returns -32601). Or capability NOT
  advertised but gateway accepts/forwards the method anyway.
- **Medium** — Optional spec feature documented as supported but
  observably broken; cross-component protocol-version drift between agent
  adapter, doctor probe, docs, and example SDKs.
- **Low** — Hardening for upcoming SEPs the runtime says it tracks;
  inconsistent error-shape choice (mixing `error.code` and
  `result.isError` for similar failures).
- **Info** — Draft-schema failures against fixtures that conform to the
  pinned revision; SEPs worth tracking that the runtime is silent on.

## Step 11 — Report

Use the structure in `../_shared/FINDINGS-TEMPLATE.md`. The report header must
state the pinned spec revision, the upstream `SPEC_REF` and commit SHA used
for the fetch, and the runtime commit SHA under audit — none of those
findings are interpretable without that triple.

Required report sections, in order:

1. **Spec triple** — `SPEC_REV`, `SPEC_REF + upstream SHA`, runtime commit
   SHA.
2. **Claims inventory** — every place the runtime advertises a version or
   capability, with file:line.
3. **Static conformance** — schema validation pass/fail per envelope.
4. **Live conformance** — per sub-step (5a–5f), command + expected vs
   observed.
5. **Capability vs implementation matrix** — Step 6 output.
6. **SEP / roadmap cross-reference** — Step 7 table.
7. **Cross-component coherence** — Step 8.
8. **Forward-compatibility** — Step 9 (informational).
9. **Findings** — ordered by severity per the rubric in Step 10.
10. **Checks skipped** — what and why (no cluster, fetch blocked,
    upstream schema layout changed).

Cross-link from each finding to the runtime file:line **and** to a stable
upstream URL (anchor in `docs/specification/$SPEC_REV/...` or a line in the
schema) so a reviewer can audit the citation themselves without re-running
the skill.

## When NOT to use this skill

- Auth/policy/audit regression hunting → `cluster-ops`.
- Operator/CLI/setup regression hunting → `cluster-ops`.
- UI/dashboard regression hunting → `dashboard-browser-qa`.
- Performance regression → `cluster-ops`.
- Cluster RBAC/PSS/NetworkPolicy hygiene → `security-audit`.
- Static repo security review → `security-audit` / `security-audit`.

Use this skill specifically when the question is **"does what we ship match
what the MCP spec says we ship?"**
