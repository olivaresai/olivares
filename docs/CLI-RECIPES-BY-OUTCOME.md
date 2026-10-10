<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# CLI recipes by OUTCOME

Audit SIEM export, directory archives and external archive verification require Business. Community retains the signed ledger and `olivares audit verify`; `olivares dr backup` remains available. See [edition placement](editions.md).

**Date:** 2026-08-10 · **Status:** every recipe below was RUN against a real
`olivares serve`, and each one states what was executed and what was not.

> **Why this file exists, and why it is not more `--help` text.** The per-command help
> answers *«what does this command do?»*. It cannot answer *«how do I get X?»*, because
> every outcome an operator actually has crosses three or four command groups, and the
> ordering between them — which verb must run first, which one refuses until another has
> — is exactly what no single command's help can hold. An independent review of the CLI
> found the help itself complete and named this as the missing thing instead. So nothing
> here is help prose: it is the path between verbs.
>
> **These commands are gated.** `cmd/olivares/cmd_recipes_test.go` extracts every
> `olivares …` line **inside a fenced code block** on this page and resolves it against
> the real command tree — command path and every long flag. Rename a flag and this page
> goes red with the code, so it cannot quietly rot into a page of commands that no longer
> exist. (Only fenced blocks: a command named in prose may deliberately be one that does
> **not** exist, as in `docs/CLI-VERB-PARITY.md`.)

**Conventions.** `$TENANT` is a tenant id, `$DATA` a data directory, `$SERVER` the
control-plane base URL. Commands that take `--data-dir` work **offline against the
store** and need no running engine; commands that take `--server` are HTTP clients and
need one.

---

## 1 · Connect Claude Code — governed, deny-closed

**The outcome:** Claude Code's tool-calls are decided by your control plane *before they
run*, and a control plane that is unreachable **denies** rather than waves them through.

**The path crosses four groups:** `serve` (mount the PEP with its policy) →
`hookpep` (the PDP overlay, optional) → `agent managed-settings` (make the hook
non-overridable) → `claude-hook` (the client Claude Code actually runs).

⚠ **There are TWO policy layers here and confusing them wastes an afternoon.** The
allow/deny/rewrite rules the PEP applies live in the **operator config file** below. The
`hookpep` group authors something else: the live **PDP** (Cedar/ABAC) hard-deny overlay
the PEP layers on top. You need the config file to get any decision at all; the PDP is
what you add when a rule needs attributes a tool-name match cannot express.

```sh
# The PDP overlay: validate and dry-run a candidate BEFORE it can deny anything.
# request.json is the request to decide: principal{kind,id}, permission, resource{kind,id,sensitivity}.
olivares hookpep validate --engine cedar --file policy.cedar
echo '{"principal":{"kind":"user","id":"u1"},"permission":"models:keys:read","resource":{"kind":"credential","sensitivity":"secret"}}' > request.json
olivares hookpep dry-run --engine cedar --file policy.cedar --request-file request.json

# Business: author and activate Cedar. Community can inspect stored policies.
olivares hookpep publish --engine cedar --file policy.cedar --note "approved change"
```

The governed hooks PEP is mounted by `serve` from an operator config, on **its own
loopback socket** — not the API port. The socket is the config's `listen` field, so the
file is not optional context: without it there is no PEP and every hook denies.
[`examples/govern-claude-code/README.md`](../examples/govern-claude-code/README.md)
carries the annotated policy to copy; the shape is

```jsonc
{
  "listen": "127.0.0.1:8447",
  "tenants": [{ "tenant": "<your-tenant-id>", "require_firm_identity": false,
                "policy": { "version": "yours/v1", "default": "deny",
                            "rules": [{ "tool": "Read", "decision": "allow", "reason": "reads are permitted" }] } }]
}
```

```sh
OLIVARES_HOOK_PEP_CONFIG=./hook-pep.config.json \
  olivares serve --insecure --listen 127.0.0.1:8080 --data-dir ./data
# log line: hook-pep: governed Claude Code hooks PEP mounted addr=127.0.0.1:8447 tenants=1
```

⚠ **Check that line before you measure anything.** Both ports must be free, and a taken
one is quiet in the wrong way: the PEP mounts, `serve` then dies on
`listener failed: listen tcp 127.0.0.1:8080: bind: address already in use`, and if an
older engine still holds that port you go on testing against a process that never read
your config. This cost real measurements while writing this page. On a shared host, pick
free ports rather than the defaults — `examples/govern-claude-code/smoke.sh` asks the
kernel for three.

Then render the managed settings that make the hook non-overridable, and point the hook
client at the PEP:

```sh
olivares agent managed-settings --out /etc/claude-code/managed-settings.json
```

The rendered document carries `allowManagedHooksOnly: true` and a `PreToolUse` +
`PostToolUse` pair running `olivares claude-hook`, so a session cannot add a hook in a
lower-precedence settings file that undercuts it.

**The proof, and it is the whole point.** Four calls, four *different* answers — a probe
that answered the same to all four would prove nothing:

| tool-call | decision | reason the PEP gave |
|---|---|---|
| `Read /repo/README.md` | **allow** | `reads are permitted` (matched an allow rule) |
| `Bash rm -rf /` | **deny** | `shell execution is blocked by this policy` |
| `Write /etc/passwd` | **deny** | `no governed policy rule permits this tool-call (deny-closed default)` |
| `Read …` with `OLIVARES_HOOK_PEP_URL` unset | **deny** | `governed PEP endpoint not configured (deny-closed)` |

```sh
# Each payload goes in on stdin; the decision comes out on stdout.
printf '%s' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

Every decision is audited by the engine with the tool, the resource kind, the mode, the
policy version, the principal, the identity tier and whether it was deny-closed.

**Executed 2026-08-10** on a build of `09fe14667`: the four rows above, plus
`examples/govern-claude-code/smoke.sh`, which asserts the same verdicts (and a governed
`updatedInput` rewrite) and **passed**. Run it yourself — it is this outcome's
reproducibility contract:

```sh
OLIVARES_BIN=./bin/olivares examples/govern-claude-code/smoke.sh
```

---

## 2 · Launch a governed session

**The outcome:** a Claude Code session runs under the control plane — with a registered
workspace, a recorded lifecycle, and an operator who can stop it.

**What governs the session.** A session launches under the tool's own login or under a key
from Providers, and the engine points the session's managed PreToolUse hook at its own
policy endpoint, so the control plane decides its tool-calls. The session's
`pep_provisioned` field says whether the hook was wired: `true` in the run recorded at the
end of this section.

**The path a person takes is `olivares session`.** One command registers the folder the
session may see and starts the tool. Once the tool is signed in (`olivares tool login
claude`) or Providers holds a key or a local model, no workspace, profile or provider
reference has to be registered first:

```sh
olivares session start /srv/projects/acme "explain this repository"
```

Without `--tool` and `--profile` it runs the first tool that is ready, as the console's New
session does (with `--profile` and no `--tool` the tool is Claude Code). Without `--profile`
the engine picks how the tool runs (its own login, or a key or local model from Providers),
and one line says which. Narrow the session when you need to:

```sh
olivares session start /srv/projects/acme --name acme-1 --tool claude --permission read-only --dlp deny --model opus
```

Watch it, feed it, stop it, remove it. `rm` runs the cleanup of a stopped session for you:

```sh
olivares session ls
olivares session show acme-1 -o json
olivares session send acme-1 "now run the linter"
olivares session follow acme-1
olivares session stop acme-1
olivares session rm acme-1
```

### The expert path: a pinned workspace and profile

`olivares agent session …` is the same engine call with every reference spelled out. It is
out of `--help` (`olivares commands` lists it) and stays for scripts that must name the
workspace and the profile themselves. Steps 1 and 3 are optional: skip them to launch under
the tool's own login, once it is signed in.

```sh
# 1. Optional: register a key the session launches with, and prove it works.
#    The key is read from stdin: a flag value lands in the shell history.
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt
olivares provider test prv_123

# 2. Register the host directory the session is allowed to see.
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent workspace ls -o json

# 3. Optional: register a profile bound to that provider.
olivares agent profile create --driver claude \
  --config-home /home/ops/.claude --user-home /home/ops --name "Claude (ops)" \
  --auth-source managed_injection --provider prv_123

# 4. Launch. --workspace takes the workspace_ref from step 2; --provider-profile,
#    when you pass it, takes the profile_ref from step 3.
olivares agent session create --name acme-1 --workspace ws-123 \
  --provider-profile ppf_123 --model opus --permission-mode plan

# 5. Watch it, feed it, stop it.
olivares agent session ls
olivares agent session get run-123 -o json
olivares agent session stop run-123
```

**`--provider-profile` is optional.** Without it the engine launches under the profile it
resolves for Claude Code, creating it on first use, exactly as `session start` does. That
needs the caller's provider-profile write permission; a caller without it is answered
**400 `select a provider profile before launching a session`**. A profile bound to a
registered provider (step 1) resolves its credential from that provider, and a bound
credential that cannot be opened **denies** the launch: it does not fall back to another
credential, because that would run the session on an account nobody selected for it.

**The ordering that no single command's help states:** on this path `rm` refuses a stopped
session with **HTTP 409 `session must be cleaned before delete (state=stopped)`**. The
lifecycle is `stop` → `cleanup` → `rm`, and skipping the middle verb is the mistake to make
once (`olivares session rm` runs the cleanup itself):

```sh
olivares agent session cleanup run-123
olivares agent session rm run-123
```

**A launch with nothing to run on is refused, not guessed.** When the engine picks the
profile (`session start` without `--profile`, `agent session create` without
`--provider-profile`), it answers **409** and says what to add: sign the tool in under AI
tools, or add a key or a local model in Providers. A pinned profile that has no credential
source answers **503**, and the sentence names the remedy: sign the tool in with its own
login, or start it with an API key saved in Providers. A deployment with its own inference
gateway sets `OLIVARES_SESSION_RUNTIME_WIF` or `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`. A token
file that is set but unreadable answers 503 with its own sentence.

**Executed 2026-10-07** on a build of `d5512b44` against a fresh data directory, with a
labelled stand-in for the `claude` program and no vendor account. The path a person takes:
`session start` with a prompt (the reply came back; `session show -o json` said
`pep_provisioned: true`) → `ls` → `show` → `send` → `follow` (cut off after 3 s) → `stop` → `rm`, then
the narrowed `start --name acme-1 --tool claude --permission read-only --dlp deny --model
opus` → `stop` → `rm`. The expert path with no provider, no profile and no
`--provider-profile`: `workspace add` → `session create` (`state=running`; the engine
created the "Claude Code" profile itself) → `get` → `stop` → `rm` (409) → `cleanup` → `rm` →
`ls` empty. **Not executed:** `provider add` and `provider test` (they need a real key),
`agent profile create` with a bound provider, and a real Claude Code sign-in. Before the tool
was signed in, both launches without a profile answered 409 `Claude Code is not signed in
and Providers has nothing it can use` (exit 5).

---

## 3 · Verify evidence

**The outcome:** you can state — and a script can state — whether the tamper-evident
ledger is intact, and you can tell *«intact»* from *«nobody has attested it yet»*.

```sh
# Offline against the store; no running engine required.
olivares audit verify --tenant $TENANT --data-dir $DATA
```

**Read `status`, not the absence of an error.** A fresh install answers
`"status": "unattested"` with `checkpoints.Reason = "no-checkpoints"`. That is the third
answer — *not looked at* — and it is not the same as clean. Anchor it:

```sh
olivares audit checkpoint --tenant $TENANT --data-dir $DATA
olivares audit verify --tenant $TENANT --data-dir $DATA
```

…and `status` becomes `ok` with `checkpoints.OK = true`.

**In automation, always `--strict`.** Without it the command exits **0 on a corrupt
ledger** — deliberately, because the default is advisory and reports status only in the
JSON. `--strict` is what makes the exit code load-bearing:

```sh
olivares audit verify --tenant $TENANT --data-dir $DATA --strict
```

For an attacker-resistant check, pin off-box public keys as well — the engine verifying
its own signatures is advisory by construction:

```sh
olivares audit verify --tenant $TENANT --pubkey "ecdsa-p256-sha256:MFkw..." --strict
```

**Executed 2026-08-10, including the direction that matters — that it can go RED.**
A copy of a real data directory was tampered with directly in SQLite:

| what was done | what `verify` said | `--strict` exit |
|---|---|---|
| nothing (untouched) | `status: ok` | **0** |
| one `actor` column rewritten | `status: corrupt`, `chain.BreakAt=1`, `Reason: hash-mismatch` | **1** |

The tamper also had to defeat the store first: the `UPDATE` was refused by the database
with **`audit_events is append-only`**, and only landed after dropping that trigger — so
the ledger has two independent defences, and the second one names the break.

Ship the evidence onward. `--format` takes the seven the command declares: `cef`, `leef`,
`syslog`, `otlp`, `otlp_envelope`, `otlp_log_record` and `ocsf`.

```sh
olivares audit export --tenant $TENANT --format cef
```

---

## 4 · Recover a delivery

**The outcome:** an event that failed to reach its destination is found, understood and
re-sent — rather than discovered missing by whoever was supposed to receive it.

*«Delivery»* here is the **webhook delivery** of the eventing platform: capture →
delivery → dead-letter queue → redelivery. All four verbs are **offline against the
store**.

```sh
# Where is it now? Deliveries carry the status; dead ones have stopped retrying.
olivares eventing deliveries ls --tenant $TENANT --data-dir $DATA --status dead
olivares eventing dead-letters ls --tenant $TENANT --data-dir $DATA

# Prove the destination answers BEFORE re-sending, or you will just re-fill the queue.
olivares eventing subscriptions ls --tenant $TENANT --data-dir $DATA
olivares eventing subscriptions test --tenant $TENANT --data-dir $DATA --id sub-123

# Then requeue it.
olivares eventing dead-letters redeliver --tenant $TENANT --data-dir $DATA --id delivery-123
```

**Two refusals you will meet on the way, and both are the product working.** A
subscription may only point where the deployment's **egress ceiling** permits — with a
policy in force, an unlisted host is refused at authoring time with `endpoint: endpoint
host is not allowed`, and a loopback destination additionally needs
`OLIVARES_EVENTING_ALLOW_LOOPBACK`. The ceiling lives in operator config
(`OLIVARES_EVENTING_EGRESS_POLICY`) precisely so the tenant role that creates
subscriptions cannot widen it. The file is small, and `allow` present-but-empty is an
authored **deny-all** — the way to mean "unconstrained" is to omit the entry:

```jsonc
{ "tenants": { "<tenant-id>": { "allow": [ { "host": "siem.example" } ] } } }
```

The same env var is read by the **offline** verbs too, not just by `serve`: these
commands open the store directly, so run them with the policy in the environment or the
authoring guard will not be the one the engine applies.

⚠ **`subscriptions test` exits 0 even when the endpoint does not answer** — it reports
`test FAILED: … connection refused` on stdout and still exits 0. Read the output, not
the exit code. (`audit verify --strict` is the pattern to copy; this verb has no
equivalent flag.)

**Executed 2026-08-10 — and here is what was NOT.** Executed: `subscriptions create`
against an egress policy in force (refused for an unlisted host; accepted once
permitted), `subscriptions test` against a deliberately unreachable endpoint
(`test FAILED … dial tcp 127.0.0.1:9: connect: connection refused`), `deliveries ls`,
`dead-letters ls` and `subscriptions rm`.

**NOT executed: the `redeliver` step on a genuinely dead delivery.** No delivery row
could be manufactured in this environment: the `audit.recorded` type is `Internal` and
reaches the eventing engine only through the leader-gated ledger-forward pump
(`cmd/olivares/ledgerforwardpump.go:19-31`), and no row materialised within the session.
The verbs and their flags are real and gated by the test on this page; the round trip
through a dead letter is **unproven here**, and is not claimed.

---

## Where these live, and why not in `--help`

`docs/`, not `olivares help <outcome>`. Two reasons, both mechanical rather than
stylistic:

1. **A help topic that is not a command cannot exist in this tree.** `olivares help X`
   resolves `X` through cobra's `Find` and returns a **usage error** for anything that is
   not a command (`cmd/olivares/subcommand_contract.go:136-165`). Carrying four recipes
   would mean four fake commands — which would then appear in `olivares commands` (the
   release smoke diffs that list against the packaged artifact), and would have to carry
   a `Long` and an `Example` to satisfy `TestCLIHelpCompleteness`. Prose would be paying
   for itself with three new obligations.
2. **The help was measured clean.** Adding prose to a surface a contrast declared clean,
   in order to answer a question the surface is not shaped to answer, is work that looks
   like an improvement and is not.

What a doc normally lacks is teeth, and that is the part that is fixed here rather than
accepted: every invocation on this page is resolved against the real command tree by
`cmd/olivares/cmd_recipes_test.go`, the same way `TestExamplesInvokeRealCommandsAndFlags`
already guards the help text itself.
