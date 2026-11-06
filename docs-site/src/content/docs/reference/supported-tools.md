---
title: Supported tools and integration depth
description: What each tool can do in Olivares, its limits, and recipes for parallel work and review before shipping.
---

Choose a tool by the integration you need. Being visible in inventory does not
mean a tool has a managed conversation. These tables describe the Community
implementation; an installed release may predate a listed adapter.
Start with [Your first hour](/how-to/first-hour/), then
[operate a provider session](/how-to/operate-provider-sessions/).

## Managed conversations

**Yes** means an Olivares adapter exists; the tool must still be installed,
signed in or bound to a provider, and ready on the engine host.
**Hooks** means Olivares tool-call policy hooks inside the tool.
**Approvals** means the managed driver can pause a tool call for a decision;
it does not mean every action under every permission preset asks permission.
**MCP** means delivery of the governed Olivares session MCP server, subject to
its grants and, for ACP tools, advertised HTTP MCP support.
**Skills** means delivery of pinned catalog skills into the tool's discovery
folder for a new conversation. Delivery requires native filesystem confinement;
container launches cannot receive assigned skills. It does not promise execution
of a skill.

| Tool (`--tool`) | Depth | Managed session | Hooks | Approvals | MCP | Skills | Resume | Conversation handoff | Account usage windows |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Code (`claude`) | Native stream + hooks | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes, when reported |
| Codex (`codex`) | Native app-server | Yes | No | Yes | Yes | Yes | Yes | Yes | Yes, when reported |
| Grok Build (`grok`) | ACP | Yes | No | Yes | Yes | Yes | Negotiated | No | No |
| OpenCode (`opencode`) | ACP | Yes | No | Yes | Yes | Yes | Negotiated | Yes | No |
| Gemini CLI (`gemini-cli`) | ACP | Yes | No | Yes | Yes | Yes | Negotiated | Yes | No |
| Cursor CLI (`cursor`) | ACP | Yes | No | Yes | No | No | Negotiated | No | No |

ACP is the Agent Client Protocol. Resume uses the tool's advertised resume/load
capability; a tool without the required capability refuses instead of silently
starting a new conversation. Claude Code and Codex use their native session/thread
identifiers. A handoff exports the stored conversation as context for a new session;
it does not transfer the original tool's private reasoning or credentials.
The source conversation must be available in the authorized tool home.

Usage windows are account limits, separate from per-session token and cost
observations. A missing value is unknown, never zero. Cursor skills delivery is
unsupported because the CLI's discovery contract and login home cannot be safely
combined by the current adapter. Cursor also has no governed session MCP delivery.

## Confinement depends on the connection

The following is **provider network confinement**, not a claim of complete file
system isolation. The engine must support the namespace/proxy boundary or a
launch requiring it is refused. Container isolation is a separate option and
cannot be combined with these network-isolated launches.

| Tool | Provider-key session | Tool-account session |
| --- | --- | --- |
| Claude Code | Yes, bound endpoint | No |
| Codex | Yes, bound endpoint | No |
| Grok Build | No | No |
| OpenCode | Yes, bound endpoint; inventory evidence only | Yes, recognized model provider's sign-in hosts |
| Gemini CLI | Yes, Google's endpoint; key header is not pinned | No |
| Cursor CLI | No provider-key binding | Yes, vendor-declared hosts; no signed-in turn measured behind the proxy yet |

Claude Code and Codex provider-key requests also pin the credential header to
the record's key. Restricting a host alone does not pin a credential. OpenCode
refuses an account launch whose model does not identify supported provider hosts.
A network-isolated agent cannot freely fetch dependencies or contact git remotes;
use approved setup and the server-side publication path. A git worktree separates
changes but shares repository metadata; it is not a security sandbox.

## Model services and lighter integrations

The remaining tools named in the README do not all supply a managed agent.
In this table, **No** applies to Olivares-managed features, not to features a
standalone tool may provide itself.

| Tool | Depth | Managed session | Hooks / approvals | Session MCP | Skills | Resume | Handoff | Usage windows | Session confinement |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Ollama | Installable local model service; use through Codex or OpenCode | No | No | No | No | No | No | No | No |
| llama.cpp (`llama-cpp`) | Start/stop an installed model server | No | No | No | No | No | No | No | No |
| vLLM (`vllm`) | Start/stop an installed model server | No | No | No | No | No | No | No | No |
| OpenHands | Connector / inventory | No | No | No | No | No | No | No | No |
| Other CLI agents | No generic managed-session adapter | No | No | No | No | No | No | No | No |

A local model connection inherits the capabilities of the agent driving it.
Select the reachable endpoint and tested model under **Tools → API keys**;
Ollama needs no key. See [Add a provider](/how-to/add-a-provider/).

## Recipe: race three agents on one task

Use this when you want three independent solutions. You pay for three runs.
On the engine host, sign in with `olivares login`, then check `olivares tool ls`
and `olivares tool providers`. Install and sign in Claude Code, Codex and
OpenCode under **AI tools**, or configure their providers. With multiple accounts,
append `--account <account-name>` to each start. Provider-key launches that need
a model also need a saved default or `--model <tested-model-id>`.

Use a clean git repository's top folder with at least one commit. Replace
`/path/to/project` below with its path **on the engine host**. All worktrees must
start at the same commit; these starts have no prompt, so none edits before the
three worktrees exist. Approve a folder's setup command first if it has one.
For Compose, run the commands inside the engine container as described in
[Operate a provider session](/how-to/operate-provider-sessions/).

```sh
olivares session start /path/to/project --tool claude --name race-claude --worktree --permission supervised
olivares session start /path/to/project --tool codex --name race-codex --worktree --permission supervised
olivares session start /path/to/project --tool opencode --name race-opencode --worktree --permission supervised
```

Choose unused names. Set one task with an observable acceptance check, then send
it to each session. `--detach` returns after acceptance so the turns can overlap.

```sh
prompt='Fix the failing parser case. Reproduce it first, make the smallest fix, run the affected tests, and report the commands and results. Do not commit or push.'
olivares session send race-claude "$prompt" --detach
olivares session send race-codex "$prompt" --detach
olivares session send race-opencode "$prompt" --detach
```

Open **Sessions** and its parallel view to follow the conversations and answer
approvals. Or use `olivares session follow <name>` in separate terminals.
When each turn ends, compare `olivares session diff <name>`, test evidence and
session cost. Pick one solution by correctness and maintainability. Stop the
others with `olivares session stop <name>`; stopping keeps their work for review.
Do not discard a losing worktree until you have checked for useful uncommitted
work. This recipe uses separate starts; it does not require a fan-out command
or automatically merge the first agent to finish.

## Recipe: review and ship the chosen solution

For example, if `race-codex` wins:

1. Inspect `olivares session diff race-codex`. In the console, open the changed
   file and send a line comment back to that conversation, or send a concrete
   request with `olivares session send race-codex "Handle empty input and rerun the parser test."`.
2. Have a second person review the resulting diff and the actual test output.
   For an agent review, start a separate read-only session on the winning worktree
   folder shown in the session detail. Give it the acceptance criteria; do not
   let it edit the winner's files. Resolve findings in the original session,
   then repeat the affected checks.
3. Configure a publication target for the session's workspace, with a repository
   credential authorized to push and open a PR. The server needs `git`; your
   Olivares user profile needs a name and email for the commit. The tool's model
   subscription is not a repository credential. Select
   the target with `--target <target-id>` when several exist.
4. Stop the winning session with `olivares session stop race-codex` so no turn
   can edit while the commit is made. Ship commits every non-ignored change in
   that folder: inspect all of it first. Run the ship command. If policy requires
   publication approval, it exits with code 11; a second person approves in
   **Approvals**, then you run the same ship command again. Missing repository, branch,
   credential or target is a refusal to fix, not permission to bypass policy.

```sh
olivares session ship race-codex --title "Fix parser empty input" --body "Reproduces the empty-input failure, fixes it, and passes the parser tests."
```

Use a title and body that match your actual change and evidence. Ship commits,
pushes and opens a PR; it does not merge it. Open the returned PR, complete the
repository's review and checks, then merge through its normal process. Only then
clean up session worktrees. Cleanup refuses to discard uncommitted or unmerged
work unless you explicitly authorize that loss.

## Sources and maintenance

This page owns the integration-depth comparison. README and the docs home link
here. Recheck the tables whenever driver registration, skill discovery, handoff
readers, usage probes or network launch policy changes:

- `core/driverfacts/facts.go`: tools, installers, bindings and skill directories.
- `cmd/olivares/sessionruntime.go` and `modules/sessions/driver_*.go`: registered
  drivers, approvals and resume negotiation.
- `modules/sessions/child_launch.go` and `session_mcp_launch.go`: hooks, network
  policy and governed MCP delivery.
- `modules/sessions/session_skills.go` and `session_handoff.go`: skill delivery
  and the explicit set of conversation exporters.
- `cmd/olivares/internal/agenttoolsapi/providers_probe.go`: account usage probes.
- [CLI reference](/reference/cli/#command-olivares-session-start) and
  [Session runtime API](/reference/session-runtime-api/): command options,
  permissions and lifecycle contracts. For publication targets, see
  [Git publication](/reference/modules/gitpublish/).
