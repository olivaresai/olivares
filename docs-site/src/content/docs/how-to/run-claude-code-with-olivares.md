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
To launch Claude, Codex or Grok under a provider profile after the runtimes
exist, see [Operate a provider session](/how-to/operate-provider-sessions/).

:::note[How governance actually reaches the session]
A session is governed because **the engine owns `claude`'s stdin/stdout** — the
`stream-json` headless transport. The engine spawns `claude` as a child process (the
native procRunner) and bridges every NDJSON frame. That only works when the engine and
`claude` share an execution context (the same host, or the same container). The
recommended topologies put them together for exactly this reason; the mixed topologies,
and their honest constraints, are below.
:::

## Two principles before you start

1. **Opt-in.** The Olivares image **carries no `claude`**. In Docker, Olivares installs
   the agent tools into its data volume and signs them in through the product when you
   ask for them; natively, the install add-on below does it. If you do not run governed
   Claude Code, nothing is installed.
2. **Official source, never redistributed.** Anthropic's terms do not permit
   redistributing the `claude` binary, so we **install it from Anthropic's official,
   GPG-signed source** at install time (natively the signed apt/dnf/apk repositories; in
   the image Anthropic's signed release, checked against the same key), pinned and with
   the auto-updater disabled. We ship no third-party binary. You can also
   **bring your own** `claude` and point the engine at it.

## The four topologies at a glance

| # | Olivares | Claude Code | How the engine conducts it | Status |
|---|----------|-------------|----------------------------|--------|
| 1 | Docker | Docker | **Same container** (the standard image), procRunner child | **Recommended** (same governed path as 2) |
| 2 | Native | Native | Same host (systemd), procRunner child | **Recommended**, smoke-tested end-to-end |
| 3 | Docker | Native (host) | Cross-namespace — not governable as-is | Co-locate instead (see below) |
| 4 | Native | Per-session container (podman or Docker) | Per-session container through the container CLI | Available where the host has podman or Docker |

The two **co-located** topologies (1, 2) are the secure default. Topology 2 (native) is
tested end-to-end by [`scripts/smoke-agentops.sh`](https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh);
topology 1 reuses the **same** governed procRunner path, and CI runs the image's agent
runtime check (`scripts/qualify-container-agent-runtime.sh`). Topology 3 wants the governor and the governed in
*different* namespaces, which the engine cannot bridge. Topology 4 starts the session
container with the host's own container CLI, so it takes no Docker-API access. Both are
spelled out in [Mixed topologies](#mixed-topologies-3-and-4).

---

## Topology 1 — both in Docker (recommended)

The standard engine container runs `claude` as well: Olivares installs the agent tools into
its data volume and signs them in through the product. Non-root, read-only
root filesystem — the base compose posture, with no second image and no override file.

### Bring it up

Pin the engine by digest and verify it first:

<!-- release -->
```sh
# verify the engine image you run (it is cosign-signed)
cosign verify docker.io/olivaresai/olivares:0.4 \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

export OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
export OLIVARES_BIND=127.0.0.1
docker compose -f deploy/compose/docker-compose.yml up -d
```
<!-- /release -->

### Install and sign in Claude Code

From **AI tools** in the console, or with the CLI:

```sh
olivares tool install claude
olivares tool login claude
```

The tool lives in the data volume and the sign-in belongs to your organization; sessions
never use a sign-in stored in the server account's home.

To see what the tool itself reports about a login (the email, masked, the plan, the usage
windows with their reset time, and the models), run `olivares tool providers`. It also lists
the login of the user that runs the engine, read through the tool's own commands and never from
its credential files; it is shown for information, and sessions still use only the sign-in made
through Olivares.

---

## Topology 2 — both native (no Docker)

Engine and `claude` on the host; systemd runs the engine, which conducts `claude`. The
workspace lives at `/var/lib/olivares/workspaces`.

### Download, verify, then run

Do not pipe the installer into a shell. Verify the release's signed checksum list, check out
the exact commit it names, and run the installer from that checkout. It then reads
`install.sh` and its packaging files from the checkout, never from a branch. The block runs
in a subshell with `set -eu`, so it stops at the first failed step and never reaches the
installer after a failed check.

<!-- release -->
```sh
(
set -eu
cd "$(mktemp -d)"
ver=0.4
base=https://github.com/olivaresai/olivares/releases/download/$ver
curl -fsSLO $base/checksums.txt
curl -fsSLO $base/checksums.txt.sig
curl -fsSLO $base/checksums.txt.pem
curl -fsSLO $base/release-commit.txt
cosign verify-blob \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity "https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/$ver" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
grep ' release-commit.txt$' checksums.txt | sha256sum --check
git clone https://github.com/olivaresai/olivares.git
cd olivares && git checkout --detach "$(cat ../release-commit.txt)"
OLIVARES_TOPOLOGY=native OLIVARES_VERSION=$ver sh scripts/install-agentops.sh
)
```
<!-- /release -->

It auto-detects the native topology, installs the **verified** engine binary (the
cosign-gated `install.sh`), installs `claude` from the signed apt/dnf/apk repository (with
key-fingerprint verification — or `OLIVARES_CLAUDE_INSTALL=byo` to skip), creates the
no-login `olivares` service user and the workspace dir, and drops in the hardened systemd
override + env example. It does **not** auto-start a governance plane — running one is
your explicit decision.

### What the installer wires (and why)

- `packaging/systemd/olivares.service.d/agentops.conf` — a drop-in that gives the
  conducted `claude` a writable `HOME` for `~/.claude` (kept under `/var/lib/olivares`),
  ensures the workspace dir exists, and restates the base unit's file-system policy:
  `ProtectSystem=full`, `ProtectHome=false`, `PrivateTmp=false` and
  `MemoryDenyWriteExecute=false` (the `claude` runtime JIT-compiles and needs W→X memory).
  Homes and `/tmp` stay visible to the engine so the folders you choose stay reachable;
  each session's `claude` and its stdio MCP servers run under the product's per-folder
  Landlock confinement instead. Every other hardening directive from the base unit stays
  in force.
- `/etc/olivares/agentops.env` — the session-runtime config (token file, TTL, optional
  gateway base URL, optional BYO `claude` path).

Then, deliberately:

```sh
sudo nano /etc/olivares/agentops.env     # wire the short-lived inference token (refresher)
sudo systemctl enable --now olivares     # listens on every interface by default
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

:::note[`--isolation container` needs podman or Docker on the engine's host]
`native` is the default. `container` runs the session in a container of its own where the
host has podman or Docker (see
[Topology 4](#topology-4--olivares-native-claude-in-a-per-session-container)); where it has
neither, and for `sandbox`, which no runner provides yet, the engine **refuses** the launch
with HTTP 422 (`isolation_unsupported`) before creating a run, workspace directory or
credential. Run `olivares doctor` to see which runtime the engine found.
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
engine. **Co-locate instead**: run both in the standard image (that *is* topology 1), or
run both native (topology 2). This is a real limit, stated rather than papered over.

### Topology 4 — Olivares native, Claude in a per-session container

Each session runs in a brand-new container of its own: the root filesystem is read-only,
no capability is kept, no privilege can be gained, and the session sees only its workspace,
the tool's account home and the tool's own program, mounted from the host. The engine
starts it with the host's container CLI (`podman run`, or `docker run`) and bridges its
stdio exactly as it does for a native child. The engine needs no Docker socket and no
extra privilege: it runs the CLI as its own user, which is rootless with podman.

```bash
olivares session start . --isolation container
```

**The runtime.** The engine uses podman when it is installed and Docker otherwise; podman
is what AlmaLinux and RHEL 10 ship. `OLIVARES_SESSION_CONTAINER_RUNTIME` pins `podman` or
`docker`, or turns container isolation `off`. To use Docker CE on an Olivares Server,
install it from Docker's official repository for your distribution, remove `podman-docker`
(it owns the `docker` command), and set `OLIVARES_SESSION_CONTAINER_RUNTIME=docker`.
Restart the engine; `olivares doctor` shows the runtime it found.

**The image.** `OLIVARES_SESSION_CONTAINER_IMAGE` names it. The default,
`localhost/olivares-session:latest`, is the name an Olivares Server image carries its session
image under; nothing in this repository builds it, so on any other host name an image you have.
It must be compatible with the host's tool program, which is mounted into it, and hold what
the tool shells out to: a shell and `git`.

**Rootless and labels.** podman runs rootless when the engine runs as a user other than root,
which is how the service user of a server runs it. An engine that runs as root gets rootful
containers, as it gets root sessions. On a host that enforces SELinux the session's mounts
carry the `z` label, which changes the label of the folder and the account home for good, and
a large folder takes time to relabel.

**Governance and network.** A server that governs tool calls with hooks (the default) keeps
them in the container. The hook program and the run's settings are mounted read-only, the
host's Claude Code managed settings are mounted where the container reads them
(`/etc/claude-code`), and a hook that cannot run in the image (another architecture, no
`/bin/sh`) blocks the tool call instead of letting it through. The hooks call the engine on
its loopback address, so a container whose environment names the engine there shares the
host's network. Be clear about what that means: the container isolates files, processes and
privileges, and its network is the host's, as a native session's is. It does not isolate
the network, and it does not scope the host's abstract Unix sockets the way a native
session's Landlock policy does. A container that names no engine endpoint keeps the
runtime's own network.

**What it does not do yet.** A session bound to a provider's endpoint from Providers stays
native: its network boundary is a proxy and a network namespace that only the native runner
holds, and the engine will not start a container session that is meant to reach one endpoint
on a wider network. That includes a local model such as Ollama added in Providers. A tool that is a single program file works; one that needs its own directory beside it, or a
runtime such as Node, needs an image that holds it.
The image needs `/bin/sh`, `git` and what the tool shells out to. Assigned skills and git
worktrees need native isolation.
Co-location, the standard image, remains the default.

---

## Security posture (all topologies)

- **Published ports.** Docker Compose publishes HTTPS and gRPC on every host interface
  (`0.0.0.0`) by default. Set `OLIVARES_BIND=127.0.0.1`, as in the command above, to
  restrict them to this host. The engine listens on `0.0.0.0` inside the container;
  the host port mapping controls exposure. Native/systemd listeners also bind every
  interface by default; configure their listen addresses to restrict access.
- **Non-root, least privilege.** uid/gid 65532, read-only root filesystem, `cap_drop:
  ALL`, `no-new-privileges` (Docker) / the kernel and namespace `Protect*`/`Restrict*`
  directives, with homes and `/tmp` visible, W^X allowed and each session confined to its
  folder by Landlock (systemd).
- **Minimal-data, allowlisted env.** The child `claude` inherits only an explicit
  allowlist (PATH, HOME, locale…) plus the in-memory inference token — **no** `OLIVARES_*`
  signing keys, **no** ambient `ANTHROPIC_*`/`CLAUDE_CODE_*` that could shadow the minted
  credential.
- **Verified supply chain.** The engine is cosign-signed (verify it / pin by digest);
  `claude` installs from Anthropic's signed repos with the key fingerprint pinned. The
  installer **refuses to run an unverified engine**, with no verification bypass.
- **Anchored audit.** Every lifecycle transition and every workspace mutation is sealed in
  the hash-chained, signed ledger by `PayloadHash` — the bytes of files and the contents
  of frames are never persisted.

## See also

- [Connect Claude Code](/how-to/connect-claude-code/) — the cooperative observe path.
- [Security & hardening](/how-to/security-hardening/) — the engine's baseline posture.
- [Verify a release](/how-to/verify-a-release/) — cosign / SBOM / SLSA verification.
- [INSTALL.md](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment) — the install matrix, including this co-deployment.
