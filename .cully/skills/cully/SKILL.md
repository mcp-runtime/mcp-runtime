---
name: cully
description: Use Cully to check session health, find and fix a stuck coding session, replay or hand off work, and save or recall project context through its MCP server. Works with Claude Code, Codex, Cursor and other coding agents.
---

<!-- cully:skill:managed -->
# Cully

Cully is the intelligent workspace around coding agents. It observes a session, keeps a private journal of what the agent did, detects problems such as loops, and keeps durable project memory through MCP. Use the installed `cully` CLI for session guidance and the configured Cully MCP tools for durable records. The connected agent makes memory calls with its own MCP identity; the local advisor does not hold OAuth credentials. Local guidance works without a server. Do not assume a particular URL or login flow.

## Know where you are running

Cully has Solo and Team modes. Solo controls and private memory stay owner-scoped. Team actions use `cully_workspace_read` and `cully_workspace_write` with the agent's configured OAuth identity, explicit team/project IDs and project membership. Shared tasks are separate from `cully task` and `cully_session` links. Never infer sharing from a repository URL or the `company` section.

For authorized team work, read `whoami`, `projects`, `board`, `inbox`, `task_get` or `lessons`. Claim/checkpoint/release a task explicitly, supply its current version, and refresh after a conflict. Start agents manually; do not auto-launch or reclaim an expired attempt. Review evidence is reported and tied to a revision. A task contributor cannot approve it; ask an authorized noncontributing maintainer to accept delivery. Draft lessons privately. Publish only when the user explicitly requests sharing and can review the exact content. Playbook adoption requires a project maintainer and a completed source task; it never changes instruction files automatically. See https://docs.cully.net/team-workflows for action inputs and limits.

Cully's session features need the agent to run inside the Cully terminal, started by the user with `cully run claude`, `cully run codex`, `cully run cursor` or `cully run AGENT`. There the user sees a health bar and an advisor panel, and Cully records the session journal. Outside it, memory and the CLI commands below still work, but there is no journal for the current session.

Never start `cully run` or `cully handoff AGENT` yourself. Both open an interactive terminal and will hang a tool call.

## Drive Cully yourself

Run these on your own when the situation fits. They are read-only and local.

| Situation | Run | Why |
| --- | --- | --- |
| The user asks how the session is going, or you are unsure of verification state | `cully status` | Measured health: duration, checks passed and failed, loops, uncommitted files, risk and why |
| The user says you or another agent is stuck, or the same command has failed repeatedly | `cully rescue --no-ai` | Evidence and recovery steps from recorded data. Without `--no-ai` it also starts a headless advisor run, so use that only when the user asks for a diagnosis. |
| The user asks what happened, what files changed, or what a previous agent did | `cully timeline`, or `cully replay --instant` for the step-by-step transcript with file activity | Never guess from memory when the journal can answer |
| You are about to finish substantive work, or the user wants another agent to continue | `cully handoff --print` | A structured summary of state, verification, open problems and next steps |
| The user wants suggested workflow improvements | `cully suggestions` | Preview a numbered suggestion with `cully apply <n> --dry-run` before changing files or agent settings. Apply only when the user requests or approves the concrete change. |

Do not run status checks routinely or interrupt unrelated work. When the journal is empty, say so; do not infer a session. A missing signal means unknown, not healthy.

## Read the advisor honestly

The advisor combines immediate local rules with an optional background worker that uses the originating agent's own CLI and Cully connection read-only. Explain only the suggestions that actually appear. An empty list can mean no issue was detected or that the agent supplied too few signals. A running daemon proves the background process is alive, not that a particular agent's signals arrive.

What the terminal can know differs by agent:

- Codex supplies model, context, tokens and quota through its native footer.
- Claude Code supplies context pressure through a hook-fed snapshot; its own status line stays silent inside the terminal.
- Cursor supplies shell, file-edit and MCP events. Its shell hook reports no exit code, so a failed Cursor command is not marked failed. Model, context, tokens and rate limits have no Cursor feed, so the panel shows them as unavailable rather than waiting.
- Other agents get the health bar and Git state, and tool events only if they can run Cully's hook.

In Warp, mouse tracking stays off until the advisor opens so Warp scroll mode keeps working. Open the advisor with Ctrl+] or F6; Esc closes it and releases the mouse again. Other terminals still support click-to-open on the compact panel.

Loop detection reports what repeated, for example that the same command failed three times after edits. It never states a cause. When it fires, read the first failure before editing again.

The journal records tool kind, success, project-relative file paths with the operation, and the program and subcommand of a command. It never records prompts, file contents, command arguments or output.

When asked whether Cully works, verify `cully version` and `cully status`, and for the terminal check that a real tool call changes the health bar. Describe any untested part plainly.

## Continue work across sessions

- At the start of a substantive task, automatically call `cully_context` with the current project and a focused task query, using its default three-note limit. This returns concise previews; call `cully_get` only for a record whose full details matter. If the server is older and lacks `cully_context`, use one `cully_search` or `cully_recent` call with `limit: 3`.
- If text search misses, try `cully_context` with `mode: semantic` or use `cully_recall` with `limit: 3`. Mem0 recall may lag a recent write. `cully_projects` helps discover active project records. Search again when the task or project changes; avoid repeated broad lookups. Do not ask the user to request memory manually.
- Before answering a question about earlier work, search the configured Cully MCP server with `cully_search`. Filter by project, section, or date when known; broaden a search if a narrow query misses relevant records.
- Ground claims about past work in the returned records. Include dates or record IDs when they help the user verify the answer, and say when no matching record was found.
- Use Git and live service checks for current branch, release, deployment, or process state; memory records are history, not a substitute for a fresh status check.

## Preserve what matters

- Before finishing substantive work, automatically use `cully_log` to save one concise record that answers: what task was this, what changed, how and why was it done, what worked, what failed or was missed, and what should happen next? Include only fields that add useful context. Record a reusable blocker or decision as it happens. Do not log every command, trivial turn, transient detail, or full transcript.
- If the installed Cully hook provides an opaque `session_ref`, include it in `cully_log`. This groups notes from one agent session without storing the native session ID or transcript. Do not invent a reference when no hook supplied one.
- When the user accepts a Cully task suggestion (`cully apply` prints `Task set: NAME`) or sets one with `cully task`, call `cully_session` once with the task name, the project, the section, your assistant name and the session's `session_ref`. This saves one task record linked to the session so later notes group under it. Calling it again with the same name keeps or relinks that record without adding a duplicate, and omitting the task keeps the current link. When the task is done, call `cully_session` with `clear_task` so later notes stop grouping under it; the task record itself is kept.
- Avoid duplicate records when work spans several turns or a stop hook asks you to check again. If an existing record needs correction, inspect it with `cully_get` and use `cully_update`.
- Set `assistant` to the agent in use and `section` to `personal` or `company`. Ask when the section is material and unclear. `company` is still scoped to the configured owner; it is not automatically shared with an organization. Set `entry_type` to `work`, `issue`, `learning`, or `decision` when useful; tasks are saved through `cully_session`.
- For a GitHub project, derive `project_url` from the Git remote and normalize it to `https://github.com/OWNER/REPO`. Omit it when the repository identity is unknown; do not guess from a directory name. Use a personal category only when it adds useful context.
- Use `cully_delete` only when the user asks to remove that record.
- Never save credentials, private keys, raw transcripts, or unrelated personal details. Report a write only after the tool confirms it. If MCP is unavailable, explain that the shared record was not saved and continue the user's task.

Durable project handoffs and reusable lessons belong in authenticated Cully MCP records: PostgreSQL holds the source notes and Mem0 indexes them for semantic recall. Foreground agents save the durable summaries; background workers recall notes read-only. The local journal and counters support live warnings and review; they are not a second durable memory store. The local daemon does not receive the agent's OAuth credentials or call Mem0 as the agent. The private data API and Mem0 key are operator settings, never agent configuration.
