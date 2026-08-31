# Govern Claude Code tool-calls (PreToolUse hooks as a PEP)

Turn *observe* into *govern*: make the control plane **allow / deny / rewrite** an
agent's tool-call **before it runs**, deny-closed and audited. This is the governed
Policy-Enforcement-Point (PEP) built on Claude Code's `PreToolUse` / `PostToolUse`
hooks — the native, self-hosted enforcement point Anthropic ships for Claude Code.

> Everything below runs against the real `olivares` binary. The
> [`smoke.sh`](./smoke.sh) in this directory runs these exact steps in CI and asserts
> the verdicts, so this example can't silently rot. Run it yourself:
>
> ```sh
> task build                 # produces ./bin/olivares
> examples/govern-claude-code/smoke.sh
> ```

## How it fits together

```
Claude Code  ──PreToolUse hook──▶  olivares claude-hook  ──HTTP POST──▶  governed PEP
   (agent)         (stdin JSON)         (managed hook command)                (127.0.0.1:8447)
                                                                                    │
                          allow │ deny │ ask ◀──── governed decision ──────────────┘
                       (+ updatedInput rewrite)     deny-closed on any failure
```

- The agent's hook pipes the tool-call to the managed `olivares claude-hook`
  command, which forwards it to the PEP and relays the verdict — **deny-closed** if
  the endpoint is unset, unreachable or errors.
- The PEP resolves the **firm identity** from the bearer, applies the tenant's
  governed **policy**, layers a live **PDP** (Cedar/ABAC) hard-deny overlay, routes
  `ask` to **human approval** (HITL), and audits every decision.

## 1. Install and create a governed tenant

A fresh install — one-time setup, login, create the tenant the policy will govern
(the same on-ramp as the [quickstart](../../docs-site/src/content/docs/start/quickstart.md)):

```sh
./bin/olivares serve --insecure --data-dir ./data &     # note the olst_… setup token it prints
curl -sf -X POST localhost:8443/v1/setup \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST localhost:8443/v1/auth/login \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' | jq -r .token)
TENANT=$(curl -sf -X POST localhost:8443/v1/system/orgs -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Production","slug":"prod"}' | jq -r .tenant_id)
```

## 2. Write the governed policy and mount the PEP

The policy is a **deny-closed allowlist**: anything without an explicit `allow`
rule is denied. Drop your `$TENANT` into [`hook-pep.config.json`](#policy):

```jsonc
{
  "listen": "127.0.0.1:8447",
  "tenants": [{
    "tenant": "<your-tenant-id>",
    "require_firm_identity": false,
    "policy": {
      "version": "examples.govern/v1",
      "default": "deny",                         // deny-closed: unlisted tools are blocked
      "rules": [
        { "tool": "Read",     "decision": "allow", "reason": "reads are permitted" },
        { "tool": "Grep",     "decision": "allow", "reason": "reads are permitted" },
        { "tool": "Glob",     "decision": "allow", "reason": "reads are permitted" },
        { "tool": "Bash",     "decision": "deny",  "reason": "shell execution is blocked by this policy" },
        { "tool": "WebFetch", "decision": "allow",
          "rewrite": { "url": "https://mirror.internal/allowed" },   // governed input rewrite
          "reason": "external fetches are redirected to the internal mirror" }
      ]
    }
  }]
}
```

Restart with the PEP wired (it mounts on its own loopback socket):

```sh
OLIVARES_HOOK_PEP_CONFIG=./hook-pep.config.json \
  ./bin/olivares serve --insecure --data-dir ./data &
# log line: "hook-pep: governed Claude Code hooks PEP mounted  addr=127.0.0.1:8447 …"
```

A `rule` matches on `tool` (exact, or a trailing-`*` glob like `mcp__*`),
`resource_kind` (`file` | `shell` | `http.url` | `web.search` | `mcp.tool` | …) and
`mode` (`read` | `write` | `unknown`). The first matching rule wins; with none, the
`default` applies (an empty default means **deny**).

## 3. See the governed verdicts

POST a hook payload to the PEP exactly as `olivares claude-hook` would. The
response is the Claude Code decision the agent enforces:

```sh
post() { curl -sf -X POST localhost:8447/ \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Hook-Tenant: $TENANT" \
  -d "{\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"$1\",\"tool_input\":$2}" | jq .hookSpecificOutput; }

post Read     '{"file_path":"/repo/README.md"}'      # permissionDecision: "allow"
post Bash     '{"command":"rm -rf /"}'               # permissionDecision: "deny"  (blocked by rule)
post Write    '{"file_path":"/etc/passwd"}'          # permissionDecision: "deny"  (deny-closed default)
post WebFetch '{"url":"https://news.example.com"}'   # "allow" + updatedInput.url = mirror.internal
```

| Tool-call | Verdict | Why |
|---|---|---|
| `Read /repo/README.md` | **allow** | matches the `Read` allow rule |
| `Bash rm -rf /` | **deny** | matches the `Bash` deny rule (reason surfaced to the agent) |
| `Write /etc/passwd` | **deny** | no rule matches → the deny-closed `default` |
| `WebFetch …` | **allow + rewrite** | allowed, but `updatedInput.url` redirects it to the internal mirror before it runs |

## Wire it into Claude Code (managed settings)

In production the hook ships in the **enterprise managed-settings tier** (the highest,
non-overridable precedence) with `allowManagedHooksOnly` — so a user can't add or
disable hooks in a lower-precedence settings file (Claude Code hooks have no native
tamper protection; managed settings is the mitigation). The managed hook block runs
`olivares claude-hook`, which reads its target from the environment:

```sh
OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/   # the governed PEP
OLIVARES_HOOK_PEP_TOKEN=<the agent's PEP token>
OLIVARES_HOOK_PEP_TENANT=<your-tenant-id>
OLIVARES_HOOK_PEP_AGENT=<agent identity hint>  # refines firm attribution
```

## Going further

- **`ask` → human-in-the-loop.** A rule with `"decision": "ask"` routes the tool-call
  to a governed approval: the agent gets `ask` (the call is held), a human approves,
  and the *same* call (bound to its plan hash, anti-TOCTOU) then returns `allow`. Wire
  it by pointing `OLIVARES_APPROVAL_BRIDGE_CONFIG` at a per-tenant service token (see
  `cmd/olivares/approvalbridge.go`). Approving a tool-call is a CRITICAL decision,
  so the reviewer needs a step-up (AAL3) session. The full pending→approve→allow loop
  is proved end to end in
  [`cmd/olivares/claudehookpep_test.go`](../../cmd/olivares/claudehookpep_test.go)
  (`TestHookPEP_AskOpensHITLAndApprovalFlipsToAllow`).
- **`require_firm_identity: true`** denies any tool-call the PEP can only attribute
  approximately or not at all — never enforce on a guessed principal.
- **`enforce_nhi_lifecycle: true`** denies a tool-call by an agent whose bound
  non-human identity has been blocked/offboarded.
- **Estate kill switch**: while an emergency stop is active, every governed
  tool-call is denied — outranking even an active break-glass grant.
- **PDP overlay**: an authored Cedar/ABAC policy can only *further-restrict* a verdict
  (a `forbid` turns any disposition into a deny); it never widens one.

## References

- Decision endpoint + governed brain: `cmd/olivares/claudehookpep.go`
- Wire protocol (Claude Code hook schema): `connectors/claude/pep.go`, `connectors/claude/hooks.go`
- Managed hook command: `cmd/olivares/cmd_claudehook.go`
