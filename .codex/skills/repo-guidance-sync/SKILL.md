---
name: repo-guidance-sync
description: Audit and update repository documentation, AGENTS.md, contributor runbooks, and test guidance so they match implemented behavior. Use when code, UI, CLI, API/CRD, config, deployment, or test-flow changes may affect user-facing docs, setup instructions, operational/debugging guidance, golden help snapshots, E2E assumptions, or agent instructions; when the user asks to sync docs after implementation; or when stale repo guidance could mislead contributors. Do not use for ordinary code review or implementation work unless documentation or guidance drift is part of the task.
---

# Repo Guidance Sync

## Overview

Use this skill to decide what documentation must change after a code, design, or product update, then make those edits in the right files with verifiable links back to source behavior.

If the user asks for a doc-sync audit and no files need changes, say that clearly and cite the source files or searches checked.

## Workflow

1. Identify the change surface.
   - Inspect the diff, touched packages, new commands, config flags, UI behavior, API shapes, CRDs, generated schemas, deployment manifests, and tests.
   - If the change affects setup, CLI workflows, operator reconciliation, ingress/gateway behavior, auth/policy, registry/image handling, observability, or cache-mode assumptions, identify the matching E2E scenario in `test/e2e/qa-e2e.sh` or adjacent integration tests before editing docs.
   - Search for existing docs before adding new files: `rg -n "<command|field|feature|concept>" README.md AGENTS.md docs website config api internal services`.
   - Prefer updating the nearest existing guide, reference, or runbook over creating a new document.

2. Classify required doc updates.
   - User-facing product change: update README, docs guides, CLI examples, API/reference pages, screenshots or UI copy references if present.
   - Developer workflow change: update AGENTS.md, build/test instructions, local setup notes, failure modes, environment variables, or repo map entries.
   - Codebase architecture or contributor-flow change: review `docs/internals/` and decide whether the overview, package tours, or Mermaid diagrams need updates. Add or revise diagrams only when they clarify package boundaries, request flow, reconciliation flow, service interactions, or contributor decision points.
   - Design change: update design-system rules, component guidance, UX behavior notes, or docs that describe screens/workflows.
   - Operational change: update install, deployment, TLS, registry, Kubernetes, observability, and rollback/debug instructions.
   - Schema/contract change: update CRD/API docs, examples, generated docs if the repo owns them, and any golden snapshots tied to CLI help.
   - E2E-visible behavior change: update or add the relevant E2E assertion in `test/e2e/qa-e2e.sh` when the behavior is exercised through Kind, real MCP traffic, policy/session state, OAuth, observability, registry/image paths, or `cluster doctor`. If narrower unit/integration coverage is sufficient, note why in the final validation summary.

3. Verify source truth before writing.
   - Treat code, tests, CRD types, OpenAPI specs, CLI definitions, workflow files, and manifests as source of truth.
   - For commands and examples, confirm exact flags, defaults, resource names, paths, and environment variables from implementation.
   - Avoid inventing roadmap promises or behavior not present in code unless the user explicitly asks for planned-product wording.

4. Edit with the right scope.
   - Keep docs concise and task-oriented; remove stale claims instead of layering caveats.
   - Preserve the repo's voice, terminology, command style, and existing document structure.
   - For `docs/internals/`, prefer updating the nearest existing walkthrough, mental model, or diagram over adding a new page. Use diagrams for relationships and flows, not for listing files that a table can express more clearly.
   - Update AGENTS.md only for durable contributor or agent guidance, not one-off implementation details.
   - Keep examples copy-pasteable: include required tags, namespaces, paths, auth headers, and prerequisite environment variables.
   - Update links and table-of-contents entries when adding or renaming pages.

5. Validate.
   - Run the narrowest relevant checks: markdown/docs build if available, golden tests for CLI help changes, generated-doc drift checks when docs are generated, E2E scenario checks for behavior covered by `test/e2e/qa-e2e.sh`, and targeted code tests if examples exercise behavior.
   - At minimum, run searches for old field names, commands, or stale wording that the change replaces.
   - If validation cannot be run, state the blocker and what was manually checked.

## AGENTS.md Rules

Update AGENTS.md when a change affects how future agents or contributors should work in the repo. Good reasons include new required checks, changed setup commands, new failure modes, changed repository layout, new service ownership, changed CLI workflows, or new operational safety rules.

Do not put product marketing, exhaustive API reference, release notes, or transient PR context in AGENTS.md. Link or summarize stable docs instead.

## Output Standard

When finished, summarize:
- docs and AGENTS.md files changed
- source behavior each update reflects
- validation run, including searches for stale terms
- known doc gaps left intentionally open
