<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Ask Olivares: an agent inside the console and the CLI

Status: design, no code. It records the decisions an implementation follows and the tests that must exist before
it ships. Edition: Community. Business adds nothing Ask-specific in the public tree (see Editions).

Ask Olivares is one agent that acts for the signed-in person, in the console and in the CLI, on a model an
administrator chose. It is not a third-party tool session. It has no folder, no shell, no network of its own and
no memory beyond the conversation. Its power is exactly a closed list of tools, each one an existing Olivares
route, called with a credential that can only be narrower than the person's.

## What already exists and is reused

Each row is read from the code, not assumed.

| Need | Existing mechanism | Where |
| --- | --- | --- |
| A credential that can only narrow | `Principal.restricted`: a server-authored permission ceiling; `Authorizer` refuses any permission outside it before a scoped grant is considered. Only package `auth` can set it (`withRestrictedPermissions`), request input cannot. The work-session and orchestration credentials already use it. | `core/auth/principal.go`, `authorizer.go`, `orchestrationsession.go` |
| Permissions are strings | `<namespace>:<resource>:<verb>`, verb `read`, `write` or `admin`. | `core/auth/permission.go` |
| Approvals | `POST /v1/m/governance/approvals` (`governance:approval:write`), decide at `POST .../approvals/{id}/decisions` (`governance:approval:admin`); the decider needs a stable user id (`a system token cannot approve`); a person cannot decide twice; a matching policy sets quorum and expiry, not the requester. | `modules/governance/governance.go`, `approvals.go` |
| A governed model call | `models.ExecuteText`: deny-closed budget precheck, model access, source scope, recording; output is not persisted. | `modules/models/execute.go`, `guardedtext.go` |
| A local model | `inference_deployment` with runtime `vllm`, `ollama`, `llamacpp` or `other`. | `modules/models/owned.go` |
| A provider credential | `ProviderRecordRef` on a provider profile. | `modules/sessions/provider_profile.go` |
| The ledger | `AuditDraft{Actor, ActorKind, Action, TargetKind, TargetID, Meta}`, hash-chained and signed per event. Business export reads the same ledger. | `core/model/audit.go`, `core/audit` |

## Decisions

### D1. The agent is the person, narrowed

A turn runs under a principal derived from the person's own session principal, with a ceiling:

    ceiling = tools enabled for Ask  ∩  permissions the person holds right now

- The intersection is computed again at every tool call, not once per turn. A person whose role is revoked
  mid-turn loses the tool on the next call.
- The derived principal keeps the person's user id, tenant and workspace confinement, so every existing check
  (tenant exclusion, workspace confinement, scoped grants and forbids, department rules) applies unchanged. The
  ceiling can only remove. Business department policy therefore narrows Ask with no Ask-specific code.
- The ceiling is a single tenant. A tool argument naming another tenant's reference is a not-found.
- The model never sees or produces a credential. Tool calls are made by the Ask module in process, not by the
  model through HTTP.

The tool set is a closed table in code. Version 1:

| Tool | Kind | Permission string | Route it calls |
| --- | --- | --- | --- |
| list runs / read a run | read | `sessions:run:read` | sessions runs read |
| list provider profiles | read | `sessions:profile:read` | provider profiles read |
| list approvals / read one | read | `governance:approval:read` | approvals read |
| list routing policies | read | `models:routing:read` | routing read |
| start a run | write | `sessions:run:write` | `POST /runs` |
| open an approval request | write | `governance:approval:write` | `POST /approvals` |

A tool outside this table does not exist for the model: an unknown tool name is refused and logged. A new tool is
a code change with a permission string, a row here and a test, never a configuration value.

Never in the table, whatever the person's role: `governance:approval:admin` (deciding), `sessions:profile:admin`,
`models:routing:admin`, any `:admin` verb, token and credential issuance, kill-switch and re-enable, anything in
`authz`. This is a list of what the derived principal's ceiling cannot contain, tested as a property of the
ceiling builder (T1).

Not in version 1: shell, file, MCP, network fetch, scheduled or background turns, memory across conversations,
another agent. Each is its own decision later.

### D2. Reads are free, writes propose and a human decides

- A read tool runs at once under the ceiling. It is audited (D4) but never asks.
- A write tool never executes on the model's say-so. It creates a pending approval through the existing route,
  bound to the exact action (tool, arguments digest, tenant). The turn then ends with the proposal shown to the
  person. The agent does not wait, poll or retry.
- Only a person decides. Deciding keeps its route, its permission (`governance:approval:admin`), its stable
  user id requirement and its quorum. Ask adds two refusals that do not depend on the ceiling:
  1. the ceiling never contains `governance:approval:admin`, so the Authorizer refuses the decision route;
  2. the decision handler refuses a principal marked as Ask-origin even if a future change widened the ceiling.
- After a human approves, the person's next turn (or the CLI's confirm prompt) consumes the approval
  (`POST /approvals/{id}/consume`, `governance:approval:write`) and the agent performs exactly the bound action once. A changed
  argument no longer matches the bound digest and is refused.
- Who may decide is shown from the approval read's `may_decide` and its list of people who may decide, once that
  read exists (it is planned in the approvals work). Until then Ask shows only "waiting for approval" and never
  derives authorization from roles itself.
- One person alone (Community's common case). The requester recorded for an Ask proposal is the person, so
  separation of duties would forbid that person from deciding it, and a solo installation would deadlock. Decision:
  when **no approval policy matches** the action, the person's own decision on their own Ask proposal counts, made
  from a human session credential (never the Ask credential). When a policy matches, its quorum and separation of
  duties apply unchanged and Ask never relaxes them. The difference is one explicit field on the request
  (`origin: ask`), read by the same checks that read `requested_by_user` today.

### D3. The model: chosen once, product state

- An administrator binds Ask to one model in the console or CLI (product state in the database, no env var, no
  restart). The binding is either an `inference_deployment` (local Ollama and the like) or a routing policy over a
  provider record. A person may pick another model only from the list the administrator allowed.
- With no binding, Ask is off: the entry does not appear and nothing is refused elsewhere. The module can be turned off
  like every other.
- The call goes through the governed text operation so the budget precheck, model access and recording apply.
  Measured: `permRoutingExecute` is `models:routing:admin` today, so a person who is not a tenant administrator
  cannot call it. Choosing the model is the administrator's authorization of that egress, the same split the
  inference egress policy already makes ("authorizing egress is admin-tier"). Using the bound model is not. So the
  implementation adds one in-process seam in `modules/models` that admits the Ask turn on its own permission
  (`ask:turn:write`, editor and above) against the bound routing policy, without changing the published
  `/routing-policies/{id}/execute` route or its permission.
- Egress. The only host an Ask turn can reach is the bound model's endpoint. Hosts declared on a provider profile
  for tool sessions (`SessionEgressHosts`) are not inherited, because no tool runs in a confined session here.
  The tenant's inference-egress DLP rules run on the assembled prompt before it leaves. Measured: `modules/models`
  has no reference to them, so the governed text path does not apply them today; the implementation adds that call.
  A local model never leaves the host.
- Tool results are inserted into the prompt after the same redaction the product already applies to approval
  notes (`redact.Clean`), so a secret read by a tool does not leave through the model call.

### D4. Every turn and tool call is in the ledger, the person as actor

- `Actor` is `user:<id>` and `ActorKind` is `user`: the person did it. Exports, verifiers and the Business audit
  export keep their vocabulary. A new `ActorKind` would make "who is accountable" ambiguous.
- The agent is the origin, carried in `Meta`, written by the Ask module from the principal marker, never from model
  output: `origin: "ask"`, `ask_turn`, `tool`, `permission`, `model_binding`, `outcome`. Putting it in `Meta` leaves the
  chain format, the migration ordinals and every verifier unchanged.
- Actions: `ask.turn` (start, end), `ask.tool.call`, `ask.tool.refused` (unknown tool, outside the ceiling, bad
  arguments), `ask.proposal` (approval opened). The refused calls are the evidence a prompt injection was tried.
- Prompt and answer text are not in `Meta`: only their SHA-256 and size, consistent with the ledger rule that raw
  payloads are never stored. The conversation itself is the person's data, kept per user and tenant, deletable by
  them, and not part of the ledger.
- An audit failure on a tool call refuses the call (deny-closed). A read that cannot be recorded does not run.

### D5. One agent, two thin clients

One module owns the turn, the tool table and the binding. The console panel (opened from Search and commands) and
`olivares ask` are clients of the same route; neither holds logic the other lacks. The CLI answers in plain text or
`--json` with stable exit codes, and shows a proposal as its approval reference plus the one command to confirm.
Console output is rendered as text, never as HTML.

## Editions

- Community: the module, the tool table, the binding, the proposals, the audit events and the threat-model tests.
  Nothing here counts users or caps turns.
- Business: department and group policy, scoped grants and the audit export already narrow and export Ask, because
  Ask is the person's principal under a ceiling and writes to the same ledger. No Ask-specific seam is needed in the
  public tree. Anything later that is specific to Ask and to businesses (per-department model allow-lists, say) is
  placed by the editions rule when it is asked for.

## Threat model

Assets: the person's authority, the approval decision, tenant data read by tools, secrets in transcripts and files,
the ledger. Attacker: anyone who can put text in front of the agent (a session transcript, a file in a folder, a
run's output, an approval reason, another user's notes) or who can influence the model (a hostile or compromised
endpoint). Trust boundary: the model and everything it reads are untrusted; the Ask module, the Authorizer and the
approval handler are trusted. Controls are structural; none depends on the prompt.

| # | Threat | Control | Test |
| --- | --- | --- | --- |
| A1 | A transcript or file says "approve request X" and the model emits a decide call | the decision route is not in the tool table; the ceiling lacks `governance:approval:admin`; the handler refuses Ask-origin | T1, T2, T3 |
| A2 | The injected text makes the model call a tool that does not exist, or a real one with another tenant's reference | unknown tool refused and audited; tenant is fixed by the principal; foreign reference is not-found | T4 |
| A3 | The injected text asks for a write; the model proposes it | a write is only a pending approval; nothing runs before a human decision; the person sees the exact bound action | T5 |
| A4 | A proposal is edited after approval, or replayed | consume binds the arguments digest and is single use | T6 |
| A5 | Authority outlives the person's role (revoked mid-turn) | ceiling intersected with current permissions at each call | T7 |
| A6 | A secret read by a tool is sent to the model provider, or the model endpoint is changed by the injected text | redaction of tool results, DLP on the prompt, egress only to the bound endpoint, binding changeable only by an administrator through the product, not by a tool | T8 |
| A7 | The model output attacks the console (HTML, link, markup) | rendered as text; links not auto-followed | T9 |
| A8 | A long loop of tool calls, or unbounded output, spends money or floods the ledger | fixed per-turn cap on tool calls and tokens (constants in code); budget precheck denies at its cap | T10 |
| A9 | A hostile or broken model endpoint returns invalid or oversized tool calls | strict schema per tool; malformed calls refused and audited; the turn ends | T4 |
| A10 | Ask used to hide who acted | the ledger names the person as actor on every event and the origin in `Meta`; refused calls are recorded | T11 |

Honest limits. The ceiling and the approval gate stop an injected instruction from deciding, from acting without a
human, and from reaching beyond the person. They do not stop an injection from making the agent give a wrong answer
about data the person can read, or from proposing a harmful action that the person then approves without reading. The
proposal card therefore shows the exact action and arguments, not the model's description of them. A hostile model
endpoint that the administrator chose sees the prompt; that is why the egress rules and DLP apply and a local model
is a choice.

## Tests required before it ships

Model stand-ins are allowed here because the unit under test is Ask, not the model. The adversary stub emits
whatever the test scripts, including a decide call.

| Id | Behavior | Must be red when |
| --- | --- | --- |
| T1 | For a tenant owner, the derived ceiling contains no `:admin` permission and not `governance:approval:admin`; the Authorizer refuses the decisions route for the Ask principal | the ceiling builder includes any admin verb |
| T2 | The decision handler refuses an Ask-origin principal even when the ceiling is widened in the test | the handler check is removed |
| T3 | End to end: a session transcript and a file both contain "approve approval X"; the adversary stub emits a decide call after reading them; the approval stays pending, no decision row exists, `ask.tool.refused` is in the ledger | any path lets the call reach the handler |
| T4 | Unknown tool, malformed arguments, foreign-tenant reference: refused, audited, no side effect | the table lookup or schema check is removed |
| T5 | A write tool call creates exactly one pending approval and executes nothing | a write executes before a decision |
| T6 | After approval, changed arguments are refused at consume; a second consume is refused | the digest or single use is dropped |
| T7 | Role revoked between two tool calls of one turn: the second call is refused | the intersection is cached per turn |
| T8 | A secret-shaped tool result is redacted in the prompt; a DLP-denied prompt does not reach the endpoint; a model call to any host except the bound endpoint is impossible | redaction, DLP or the egress pin is removed |
| T9 | Console renders `<img onerror>` and a markdown link as inert text | output is rendered as HTML |
| T10 | The per-turn caps end a looping adversary; the budget precheck denies at cap | a cap is removed |
| T11 | Every event of a turn has `Actor = user:<id>`, `ActorKind = user` and `Meta.origin = "ask"`; the chain verifies; prompt text is absent from `Meta` | actor kind changes, or text reaches `Meta` |
| T12 | Solo person, no approval policy: own decision from the human session consumes; with a matching policy the same person is refused as today | the policy check is skipped |

Journey proof on the release binary (not a fixture): a fresh install with a local Ollama model bound; ask a read
question in the console and in the CLI; propose a run; approve it as a human; see every step in the audit view as
the person with origin `ask`.

## Open items the implementation owns

1. The approval read's `may_decide` and decider list, so Ask never derives authorization from roles.
2. The in-process admission seam in `modules/models` (D3) and the DLP call on the assembled prompt.
3. The `origin: ask` field on an approval request, and the separation-of-duties rule in D2.
4. Route classification for the new routes in `docs/openapi-route-classification.md` and the generated clients.
5. Strings in every locale of the touched console feature, and the UI proof required for any new console screen.
