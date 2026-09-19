---
title: "Your first hour with Olivares AI"
description: >-
  Install the control plane, open the console, connect one coding agent, run a
  governed session that allows one action and denies another, and read the
  evidence. Three shapes: local (SQLite), team (Postgres and Docker), hybrid.
---

This page is the first hour on **this tree**. It is not a mockup. Every command
in the local shape is the command `task smoke:first-hour` runs against
`./bin/olivares`. Where a shape needs Docker or Postgres, the page says so.

The product prints the next step. `olivares quickstart` names passkey
enrollment, `olivares agent tool detect`, inventory, the hook PEP and
`olivares doctor` after the setup token (`cmd/olivares/cmd_quickstart.go`).
`olivares doctor` reports `first-hour-coding-agent`, `first-hour-hook-pep` and
`first-hour-next-step` (`cmd/olivares/cmd_doctor.go`). Those checks are
optional. They do not fail a healthy install.

## What this hour is

Install → console → connect **one** coding agent → see it in inventory → run a
governed session that **allows one action and denies another** → read the
evidence.

This page uses the **Claude Code hook PEP** that this tree already ships
(`olivares claude-hook`, `OLIVARES_HOOK_PEP_CONFIG`). Launching an official CLI
as a live session process is the Community runtime seam
(`modules/sessions/cliruntime`). This hour does not duplicate
that driver. It does not send a model turn.

`--seed-demo` is not this hour. Use it only for the access-graph tutorial.

## Shape 1 — Local (SQLite, this box)

This container has **no Docker**. PostgreSQL is **not running**. The local
shape uses the embedded SQLite store and loopback HTTP. That is the shape the
smoke test replays.

### 1. Build and boot

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

Headless smoke uses `serve --insecure` so `curl` does not have to trust a
self-signed certificate, and it passes `--listen 127.0.0.1:8443` explicitly:
that loopback bind is the smoke test's, not the product's default. An
interactive operator runs `olivares quickstart` instead (TLS on, no default
credentials, a single-use setup token). Its default listen address is `:8443` —
**every interface**, because this is a server
(`cmd/olivares/binddefaults.go`); pass `--listen 127.0.0.1:8443` to restrict it
to this machine. Measured on this box on 2026-09-17, `quickstart --quiet`
printed the setup token in **3 s**.

The welcome panel (`announceQuickstart`, `cmd/olivares/cmd_quickstart.go`)
prints:

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

For the wildcard default the banner prints `https://localhost:8443` and then
lists, under the token, every other address this host answers at — those are
for reaching the console from another machine. If the banner has scrolled away,
`olivares first-boot` prints the console address(es) and the state of first
setup again. Open `https://localhost:PORT` before you enroll a passkey: a
browser refuses an IP as a WebAuthn relying party, and the product already says
so in the address advice (`cmd/olivares/consoleaddr.go`). `PORT` is `8443`
unless you passed `--listen`.

### 2. Create the administrator and the tenant

One command completes setup against the running engine, and it is the one the
startup panel prints. The token is read from stdin so it never enters the
process table:

```bash
# paste the olst_… token the engine printed when it started
olivares auth bootstrap --server https://127.0.0.1:8443 \
  --ca-cert <data-dir>/tls.crt \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` signs in and stores the session, so the next command is already
authenticated. Measured 2026-09-18 on a clean data directory: 263 ms.

The same thing over the API, if you are scripting against the endpoints directly:

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

The console wizard posts the same `/v1/setup` body. After sign-in you have an
AAL1 password session.

### 3. Connect one coding agent and see it in inventory

Detect the official CLI on this host (no control plane, no account):

```bash
./bin/olivares agent tool detect -o json
```

Measured on this box on 2026-09-17: `claude` 2.1.274 at
`~/.local/bin/claude`, match `unregistered-observed`. Detect does not register
a control-plane agent.

Register **one** agent in the inventory:

```bash
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

`POST /v1/agents` does **not** require AAL3 (`core/api/handlers_core.go`).
Creating sources, connectors, workspaces and secrets **does** (`requireAAL3`).

Render the managed hook that Claude Code will invoke:

```bash
./bin/olivares agent managed-settings --out ./managed-settings.json
```

Place that file at the OS managed-settings path in production
(`/etc/claude-code/managed-settings.json` on Linux). The first-hour smoke
keeps it in the work directory and drives `olivares claude-hook` directly.

### 4. Governed session: allow Read, deny Bash

Write a deny-closed policy and restart with the PEP mounted:

```bash
# hook-pep.json — replace TENANT with the id from step 2
# listen on 127.0.0.1:8447; default deny; Read allow; Bash deny
OLIVARES_HOOK_PEP_CONFIG=./hook-pep.json \
  ./bin/olivares serve --insecure --data-dir "$DATA" \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444
```

The engine logs `hook-pep: governed Claude Code hooks PEP mounted`.

Drive two tool-calls through the managed hook client:

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
# permissionDecision: allow
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
# permissionDecision: deny
```

| Tool-call | Verdict | Why |
|---|---|---|
| `Read /repo/README.md` | **allow** | explicit allow rule |
| `Bash rm -rf /` | **deny** | explicit deny rule |
| any other tool | **deny** | deny-closed default |

### 5. Read the evidence

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Each governed decision appends `hook.tool.allow` or `hook.tool.deny` to the
tenant ledger (`cmd/olivares/claudehookpep.go`). The smoke test asserts both
rows exist.

Confirm the next step:

```bash
./bin/olivares doctor --mode user
```

Doctor prints `first-hour-next-step`. It never prints the hook PEP URL.

Reproduce this shape:

```bash
task smoke:first-hour
```

## Shape 2 — Team (Postgres and Docker)

This container has **no Docker**. PostgreSQL is **not running** here. Do not
treat the commands below as measured on this box. They are the team shape the
product ships: Compose with the Postgres override.

```bash
cp deploy/compose/.env.example deploy/compose/.env
# set three distinct passwords (app, admin, postgres). never reuse them.
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
docker compose -f deploy/compose/docker-compose.yml logs olivares
```

The Postgres override provisions `olivares_app` (FORCE RLS, no BYPASSRLS) and
`olivares_admin` (read-only BYPASSRLS). Without the admin pool, `POST /v1/setup`
answers `501 cross_tenant_admin_pool_not_configured`. That refusal is
intentional. See [Deploy with Docker](/how-to/docker-deployment/).

After setup, the first hour is the same as local: detect one CLI, `POST
/v1/agents`, wire `OLIVARES_HOOK_PEP_CONFIG`, allow Read, deny Bash, read
`GET /v1/audit?action=hook.tool`. The store is Postgres. The PEP still binds
loopback unless you front it.

Team needs Docker **and** a running Postgres (the Compose override starts
one). This page does not claim those ran here.

## Shape 3 — Hybrid (local control plane, agent on a workstation)

Keep the control plane on the local SQLite shape. Run the coding agent on a
workstation that already has `claude` (or install it with
`olivares agent tool install`).

On the control-plane host:

1. Complete Shape 1 through the PEP mount.
2. Point the PEP listen address at a reachable loopback or an ingress you
   control. The engine warns if the PEP binds a non-loopback address.

On the workstation:

```bash
export OLIVARES_HOOK_PEP_URL=https://olivares.example.internal:8447/
export OLIVARES_HOOK_PEP_TOKEN="<agent PEP token>"
export OLIVARES_HOOK_PEP_TENANT="<tenant id>"
olivares agent managed-settings --out /etc/claude-code/managed-settings.json
# then start the official claude CLI in that environment
```

The workstation does not need Docker or Postgres. The control plane does not
need the official CLI on the same host. Hybrid is that split.

A live official-CLI session (PTY, attach, stop) is the Community runtime, not this page. This
hour proves the hook PEP path that the Community runtime reuses.

## The AAL3 wall (still true)

After setup, **creating sources, connectors, workspaces and secrets is
refused until the session is AAL3**. The gate is `requireAAL3` in
`core/api/middleware.go`. A principal below AAL3 gets `403 step_up_required`.

PIV/CAC answers **501** `piv_not_configured` on a stock install. A platform
authenticator is enough. Open the console at `https://localhost:PORT`, then
**Identity → Privileged login → Register passkey**.

`POST /v1/agents` is not behind that gate. The local first hour can register
the agent and govern the hook without a passkey. Adding a connector from the
console still needs AAL3.

## 3. Launching a Claude Code session from the console

The console can spawn a `claude` process only when the **host** has an
inference credential source. Without one, stream-json launches are
deny-closed. The composition root logs:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go`). That is still one of the two paths: set
**one** of `OLIVARES_SESSION_RUNTIME_WIF` or `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
and every profile that names no provider uses it.

⛔ **The other sentence here said "provider-key forms in the console never accept a
secret. Filling that tab does not enable launches." That was true of v26.9 and is
false from v26.10.** The console now has a Providers screen that accepts the
key, seals it in the engine, tests the connection without spending anything, and
binds it to a provider profile — and a session launched under that profile uses it,
with no variable in the server's shell. The old sentence referred to
**Models → Provider keys**, which is a different surface: it registers governance
REFERENCES for the model gateway and still stores no secret value.

The guided path: [Add a provider and launch an agent](/how-to/add-a-provider/), or
**Onboarding → Providers** in the console.

Operate-path launch, attach and stop: [Operate a provider session](/how-to/operate-provider-sessions/).
The Community runtime's own contract is documented with that runtime, not here.

## Gaps v26.10 closed (measured 2026-09-17)

| Step | Before (file:line) | After |
|---|---|---|
| Welcome panel after the token | Stopped at step 2. No passkey tab, no agent, no doctor. `cmd/olivares/cmd_quickstart.go` welcome `Fprintf` | Steps 3–5: Privileged login, `agent tool detect`, `POST /v1/agents`, hook PEP, `olivares doctor` |
| Restart before setup | Named the missing token. Did not name doctor. same file, pending panel | Names `olivares doctor` after setup |
| Returning operator | Sign-in URL only | Names `olivares doctor` and the First hour guide |
| `olivares doctor` | No first-hour checks | `first-hour-coding-agent`, `first-hour-hook-pep`, `first-hour-next-step` (optional, never fail a healthy install; never print the PEP URL) |

Passkey-at-IP advice was already printed (`cmd/olivares/consoleaddr.go`). That
paragraph was not re-worked.

## Related

- [Honesty & limits](/start/honesty-and-limits/)
- [Self-host Olivares AI](/how-to/self-hosting/)
- [Operate a provider session](/how-to/operate-provider-sessions/)
- [Claude Code hooks PEP](/how-to/connectors/claude-code-hooks-pep/) — the same PEP, with rewrite
- [Deploy with Docker](/how-to/docker-deployment/) — team shape
- [Configuration](/reference/configuration/)
