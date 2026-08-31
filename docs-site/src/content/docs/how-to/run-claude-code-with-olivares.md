---
title: "Run Claude Code with Olivares (co-deployment)"
description: "Co-deploy the Olivares control plane and a Claude Code runtime on one Linux box, secure by default, so the engine launches, governs and tears down Claude Code sessions sharing a workspace — in four topologies."
---

This is the **Operate** half of the Anthropic-first story: not just *observe* and
*govern* Claude Code, but **conduct** it. The control plane launches a real `claude`
process, bridges its I/O into a governed stream, anchors every lifecycle transition in
the audit ledger, and tears it down — over a shared workspace, from the API/CLI (and,
later, the portal), **without SSH**. This page co-deploys both halves on one Linux host
in four topologies, secure by default.

For the *cooperative observe* path (OTLP telemetry → access map) see
[Connect Claude Code](/how-to/connect-claude-code/); for the *govern* path (PreToolUse
hooks as a PEP) see the [govern-claude-code example](https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code).
This page is **co-deployment**: getting the two runtimes running together.

:::note[How governance actually reaches the session]
A session is governed because **the engine owns `claude`'s stdin/stdout** — the
`stream-json` headless transport. The engine spawns `claude` as a child process (the
native procRunner) and bridges every NDJSON frame. That only works when the engine and
`claude` share an execution context (the same host, or the same container). The
recommended topologies put them together for exactly this reason; the mixed topologies,
and their honest constraints, are below.
:::

## Two principles before you start

1. **Opt-in.** The base Olivares image is distroless and **carries no `claude`**. The
   Operate-Claude-Code layer is a *separate* artifact — a combined image
   (`Dockerfile.agentops`) or a native install add-on. If you do not run governed Claude
   Code, you never pull it, and its extra surface never touches your control plane.
2. **Official source, never redistributed.** Anthropic's terms do not permit
   redistributing the `claude` binary, so we **install it from Anthropic's official,
   GPG-signed source** at build/first-run (the signed apt/dnf/apk repositories), pinned
   and with the auto-updater disabled. We ship no third-party binary. You can also
   **bring your own** `claude` and point the engine at it.

## The four topologies at a glance

| # | Olivares | Claude Code | How the engine conducts it | Status |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **Same container** (combined image), procRunner child | **Recommended** (same governed path as 2) |
| 2 | Native | Native | Same host (systemd), procRunner child | **Recommended**, smoke-tested end-to-end |
| 3 | Docker | Native (host) | Cross-namespace — not governable as-is | Co-locate instead (see below) |
| 4 | Native | Docker (per-session) | Per-session container via the Docker API | Follow-up (documented) |

The two **co-located** topologies (1, 2) are the secure default. Topology 2 (native) is
tested end-to-end by [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh);
topology 1 reuses the **same** governed procRunner path (the combined image's build/run
is not yet wired into an automated test). Topologies 3 and 4 want the governor and the governed in
*different* containers; bridging stdio across that boundary needs Docker-API access (a
privilege the engine deliberately does **not** take by default). Their honest paths are
spelled out in [Mixed topologies](#mixed-topologies-3-and-4).

---

## Topology 1 — both in Docker (recommended)

One hardened container runs the engine **and** `claude`; a workspace volume is the
shared working directory. Loopback-only, non-root, read-only root filesystem — identical
posture to the base compose, plus the conducted runtime.

### Build the combined image

`claude` is installed at build time from Anthropic's **signed apt repository**, with the
signing-key fingerprint pinned (`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`) and
auto-update disabled. Pin the engine base by digest and verify it first:

```sh
# verify the engine image you build FROM (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:26.8.0 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --build-arg CLAUDE_CHANNEL=stable \
  -t olivares-agentops:26.8.0 .
```

Bring your own `claude` instead with `--build-arg CLAUDE_INSTALL=byo` (the image ships
without `claude`; mount yours at runtime and set `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`).

### Bring it up

```sh
export OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.8.0
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.agentops.yml up -d
```

The override changes only what Operate needs: the combined image, four writable volumes
(engine data, **workspace**, claude's `~/.claude` home, the short-lived inference token),
and the session-runtime env. Everything else — `127.0.0.1`-bound ports, uid 65532,
`read_only` root, `cap_drop: ALL`, `no-new-privileges` — is inherited from the base.

:::caution[The first governed session needs an inference credential]
The credential source is **deny-closed**: a `stream-json` launch reads a *short-lived*
bearer token from `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` (`/run/olivares/session-token`,
on the `olivares-runtime` volume) and discards it — only a non-sensitive `credential_id`
is ever stored. Point your WIF/SPIFFE/OIDC refresher at that volume. Until a token is
present, `stream-json` launches fail **closed** — the engine still runs and is otherwise
governable; wiring auth is your deliberate step. (The live in-process token exchange is
wired separately.)
:::

---

## Topology 2 — both native (no Docker)

Engine and `claude` on the host; systemd runs the engine, which conducts `claude`. The
workspace lives at `/var/lib/olivares/workspaces`.

### One command

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

It auto-detects the native topology, installs the **verified** engine binary (the
cosign-gated `install.sh`), installs `claude` from the signed apt/dnf/apk repository (with
key-fingerprint verification — or `OLIVARES_CLAUDE_INSTALL=byo` to skip), creates the
no-login `olivares` service user and the workspace dir, and drops in the hardened systemd
override + env example. It does **not** auto-start a governance plane — running one is
your explicit decision.

### What the installer wires (and why)

- `packaging/systemd/olivares.service.d/agentops.conf` — a drop-in that gives the
  conducted `claude` a writable `HOME` for `~/.claude` (kept under `/var/lib/olivares`,
  so `ProtectHome=true` still shields real users), ensures the workspace dir exists, and
  lifts exactly **one** sandbox property: `MemoryDenyWriteExecute` (the `claude` runtime
  JIT-compiles and needs W→X memory). Every other hardening directive from the base unit
  stays in force.
- `/etc/olivares/agentops.env` — the session-runtime config (token file, TTL, optional
  gateway base URL, optional BYO `claude` path).

Then, deliberately:

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # loopback-only by default
```

:::note[Why there is no separate `claude` service]
A long-running `claude` daemon would put its stdin/stdout out of the engine's reach — and
the governed transport *is* stdio. So the engine launches and owns the `claude` process
itself; the "runtime unit" is the engine's own service, configured for the Operate role
by the drop-in.
:::

---

## Launch the first governed session

Same steps in either co-located topology. Authenticate the CLI, register the shared
workspace, launch:

```sh
export OLIVARES_SERVER_URL=https://127.0.0.1:8443
export OLIVARES_TOKEN=<your-api-token>
export OLIVARES_TENANT=<your-tenant-id>

# 1) register the shared workspace (the session's working dir; jailed file API on top)
olivares agent workspace add /var/lib/olivares/workspaces/project-x --name project-x --mode rw

# 2) launch a governed session over the stream-json transport
olivares agent session create --transport stream-json \
  --permission-mode acceptEdits --model opus \
  --workspace <workspace-ref> --isolation native

# 3) attach to its live, bridged I/O (lossless replay from a cursor); send input; stop
olivares agent session attach <run-ref>
olivares agent session input  <run-ref> --line '{"type":"user","message":{"role":"user","content":"…"}}'
olivares agent session stop   <run-ref>
```

Every transition (`created → launched → … → stopped`) is **anchored in the signed audit
ledger** (`olivares agent session events <run-ref>`); the workspace file API
(`olivares agent workspace files|get|put|…`) is jailed and audited. The reproducibility
contract for all of this is [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh),
which brings the native co-deployment up against a hermetic fake `claude` and asserts the
session is governable end to end.

:::note[Only `--isolation native` is functional this release]
`--isolation container` and `--isolation sandbox` are **forward-compatibility seam
values, not wired yet** (the per-session container Runner is the documented follow-up in
[Topology 4](#topology-4--olivares-native-claude-in-a-per-session-container)). The native
runner **refuses** a container/sandbox launch (a clear error) rather than silently run
`claude` without the isolation you asked for. Use `native` — under the combined image /
systemd co-deployment that is the engine's own hardened container/host boundary.
:::

:::caution[`bypassPermissions` belongs behind governance]
Running `claude` headless with a permissive `--permission-mode` (`bypassPermissions`,
`dontAsk`) is exactly when you want the governance plane. The engine's allowlisted
environment never leaks an `OLIVARES_*`/`ANTHROPIC_*` secret to the agent, and the
PreToolUse PEP / budget / kill-switch decide what the session may actually do.
:::

---

## Mixed topologies (3 and 4)

These split the governor and the governed across a container boundary. Be clear-eyed
about what that costs.

### Topology 3 — Olivares in Docker, Claude on the host

There is **no clean governed path**: a containerised engine cannot own the stdio of a
process in the host's namespaces, and the governed transport is stdio. Reaching a host
`claude` would require sharing the host PID namespace and mounts into the engine
container — a large, deliberate de-isolation that defeats the point of containing the
engine. **Co-locate instead**: run both in the combined image (that *is* topology 1), or
run both native (topology 2). This is a real limit, stated rather than papered over.

### Topology 4 — Olivares native, Claude in a per-session container

This is the natural home for **per-session fresh-container isolation**: each session gets
a brand-new hardened `claude` container (workspace bind-mounted, read-only root, non-root,
cap-drop), created and torn down by the engine through the Docker API, with stdio bridged
via Docker attach/hijack. The data-model seam already **models** it (`--isolation container`
is a valid value, and the executor mount primitive it will consume already ships) — but the
runner behind it is not wired yet, so the native runner refuses that value today (see the
note above).

**It is a documented follow-up, not shipped in this release.** Driving sibling containers
means giving the engine Docker-API access (ideally through a least-privilege socket
proxy) — a trust surface this release deliberately avoids in favour of the socket-free
combined image. Choosing this topology is choosing stronger governor/governed isolation
*at the cost of* that Docker-API grant; it will arrive behind the existing
`isolation=container` seam. Until then, the secure default is co-location.

---

## Security posture (all topologies)

- **Loopback by default.** Host ports publish on `127.0.0.1` only. In a container the
  engine listens on `0.0.0.0` *inside* the container, so the **host port mapping is the
  exposure boundary** — never publish it on a non-loopback host address without your own
  TLS-terminating auth proxy. The native/systemd default bind is loopback. Expose deliberately.
- **Non-root, least privilege.** uid/gid 65532, read-only root filesystem, `cap_drop:
  ALL`, `no-new-privileges` (Docker) / the full `Protect*`/`Restrict*` set minus the one
  documented W^X relaxation (systemd).
- **Minimal-data, allowlisted env.** The child `claude` inherits only an explicit
  allowlist (PATH, HOME, locale…) plus the in-memory inference token — **no** `OLIVARES_*`
  signing keys, **no** ambient `ANTHROPIC_*`/`CLAUDE_CODE_*` that could shadow the minted
  credential.
- **Verified supply chain.** The engine is cosign-signed (verify it / pin by digest);
  `claude` installs from Anthropic's signed repos with the key fingerprint pinned. The
  installer **refuses to run an unverified engine** unless you explicitly opt out.
- **Anchored audit.** Every lifecycle transition and every workspace mutation is sealed in
  the hash-chained, signed ledger by `PayloadHash` — the bytes of files and the contents
  of frames are never persisted.

## See also

- [Connect Claude Code](/how-to/connect-claude-code/) — the cooperative observe path.
- [Security & hardening](/how-to/security-hardening/) — the engine's baseline posture.
- [Verify a release](/how-to/verify-a-release/) — cosign / SBOM / SLSA verification.
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — the install matrix, including this co-deployment.
