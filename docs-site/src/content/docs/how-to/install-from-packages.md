---
title: Install from a package
description: >-
  Install Olivares AI from the .deb, .rpm or .apk on a hardened Linux host: verify the
  release before you trust it, run it under the packaged systemd unit as an unprivileged
  user, take your first source, and upgrade — online, pinned, or fully air-gapped.
draft: true
---

:::caution[Draft — package names unverified]
The prose, the unit and every command on this page are read from the tree and from the
binary's own help. What is **not** yet verified is the published **asset file names**:
no release tag exists yet, so nothing has ever been built by the release pipeline.
Every filename below is marked `[VERIFICAR]` and must be checked against the first real
release before this page leaves draft. The packages themselves are still to be tested on
Leap/Tumbleweed and RHEL/Fedora.
:::

This is the path for a normal Linux host — Debian/Ubuntu, RHEL/Fedora/SUSE, or Alpine —
where you want the engine running under systemd as a service, not in a container. For
containers see [Docker deployment](/how-to/docker-deployment/); for a host with no route
out at all, [Install in an air-gapped environment](/how-to/air-gap-install/), which this
page links back to at the upgrade step.

## 1. Verify the release before you trust it

For a security product the build pipeline is part of the trust model, so nothing here asks
you to take the download on faith. Put the package, `checksums.txt` and the signature in one
directory and run the verifier **from that directory**:

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh

# key-based, and genuinely disconnected — use this one on an air-gapped host
./verify-release.sh --key cosign.pub --offline
```

The one distinction worth carrying into a package install: **`--key` with `--offline` is
genuinely disconnected**, while keyless verification still fetches Sigstore trusted-root
material unless it is already cached. `--offline` removes the Rekor lookup; it is not by
itself a promise that no socket is opened.

What each step checks, how it behaves on a partial release, and how to verify the container
image instead is [Verify what you downloaded](/how-to/verify-a-release/).

## 2. Install the package

The three package formats carry the same contents: the binary at `/usr/bin/olivares`, the
hardened unit at `/usr/lib/systemd/system/olivares.service`, a commented environment file
at `/etc/olivares/olivares.env` (marked `config|noreplace`, so your edits survive an
upgrade), the data directory `/var/lib/olivares`, and the licence texts — `LICENSE`,
`NOTICE`, `LICENSING.md`, `DISCLAIMER.md` — under `/usr/share/doc/olivares/`.

```bash
# Debian / Ubuntu                              [VERIFICAR: asset name]
sudo dpkg -i olivares_<version>_linux_amd64.deb

# RHEL / Fedora / SUSE                         [VERIFICAR: asset name]
sudo rpm -Uvh olivares_<version>_linux_amd64.rpm

# Alpine                                       [VERIFICAR: asset name]
sudo apk add --allow-untrusted olivares_<version>_linux_amd64.apk
```

Installing **creates the system user and group `olivares`** (with `/usr/sbin/nologin` as
its shell and `/var/lib/olivares` as its home), creates `/var/lib/olivares` mode `0750`
owned by that user, creates `/etc/olivares`, and runs `systemctl daemon-reload`. It does
**not** start anything — see [what the package does not do](#7-what-the-package-does-not-do).

## 3. The hardened systemd unit

The packaged unit runs the engine as the unprivileged `olivares` user with an empty
capability bounding set — it holds no capabilities at all, ambient or bounding — and
`NoNewPrivileges=true`, so nothing it launches can gain any. On top of that it carries
`ProtectSystem=strict` (the filesystem is read-only except `ReadWritePaths=/var/lib/olivares`),
`ProtectHome`, `PrivateTmp`, `PrivateDevices`, the four `ProtectKernel*`/`ProtectClock`
directives, `RestrictNamespaces`, `RestrictSUIDSGID`, `RestrictRealtime`, `LockPersonality`,
`MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, a `@system-service` syscall
filter that additionally drops `@privileged` and `@resources`, and `UMask=0027`.

**The listeners are loopback-only by default** — `--listen=127.0.0.1:8443` for HTTP (REST
plus the embedded console) and `--grpc-listen=127.0.0.1:8444` for gRPC. Widen them
deliberately, through `OLIVARES_EXTRA_ARGS` in `/etc/olivares/olivares.env`, and front the
result with your own TLS termination. For IPv6 loopback use `--listen=[::1]:8443`.

Start it:

```bash
sudo systemctl enable --now olivares
```

### The one thing a hardened host gets wrong: an executable TMPDIR

This is the failure worth knowing in advance, because the symptom does not name its cause.

Out-of-process first-party connectors ship **embedded in the binary**. At boot the engine
extracts the ones it needs into a private scratch directory and **executes** them as
subprocesses. That scratch directory comes from `os.MkdirTemp("", …)`, which honours
`$TMPDIR` and falls back to `/tmp`.

So if the directory it lands in is mounted `noexec` — and `/tmp` being `noexec` is a
standard hardening measure, required by several CIS benchmarks — **those connectors cannot
launch**. The engine itself starts fine; individual sources fail. Under the packaged unit
`PrivateTmp=true` gives the service its own `/tmp`, which removes the host's mount options
from the question on most systems, but not on all of them, and not if you run the binary
outside the unit.

Check it on the filesystem the service will actually use:

```bash
findmnt -no OPTIONS --target /tmp | tr ',' '\n' | grep -qx noexec \
  && echo 'noexec — set TMPDIR' || echo 'exec-capable — nothing to do'
```

If it says `noexec`, point `TMPDIR` at a directory that is both writable under
`ProtectSystem=strict` and exec-capable. `/var/lib/olivares` is already in
`ReadWritePaths`, so a subdirectory of it needs no other change:

```bash
sudo install -d -o olivares -g olivares -m 0750 /var/lib/olivares/tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/var/lib/olivares/tmp
```

Then `sudo systemctl restart olivares`. Verify `/var/lib` is not itself `noexec` with the
same `findmnt` line — on a host that hardens `/var` too, use any exec-capable path the
service can write and add it to `ReadWritePaths=` in the same drop-in.

Use `systemctl edit`, never a direct edit of `/usr/lib/systemd/system/olivares.service`:
that file belongs to the package and an upgrade replaces it.

## 4. First boot: `olivares quickstart`

`quickstart` is `serve` with friendly defaults and a guided banner. It never invents
default credentials; it points you at the embedded console to create your first
administrator with a **one-time token**.

Running under the packaged unit you are already serving, so you do not run `quickstart` —
**the first-boot setup token is printed to the journal**, and the package tells you exactly
how to read it back:

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

Open the console at `https://127.0.0.1:8443` (a self-signed certificate is generated on
first start), present that token, and create the administrator. The token is single-use.

On a workstation, to look around without installing a service, `olivares quickstart` does
the same thing in the foreground with `--listen`/`--grpc-listen`/`--data-dir` if you need
to move it off the defaults.

## 5. Your first source: pgAudit

A source is where the engine ingests observations from. The verbs are split by what each one
costs, and it is worth using them in that order: `plan` says what would change and writes
nothing, `validate` says the configuration is coherent by itself **without touching the
network**, `test` opens the source for real to prove it answers, and `set` applies.
Configuration carries secret **references** (`store:<name>`), never values.

pgAudit reads the PostgreSQL audit log, so `log_path` is the only required field:

```bash
# coherent by itself? (writes nothing, opens no socket)
sudo -u olivares olivares sources validate --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# does it actually answer? (opens the source for real)
sudo -u olivares olivares sources test --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# apply it
sudo -u olivares olivares sources set --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --actor "$(id -un)" --reason "onboard the production audit log" \
  --data-dir /var/lib/olivares
```

Three things the command line above is not padding:

- **`--tenant` is required.** A source must name the business tenant its observations belong
  to; without it the command refuses rather than guessing an owner for your audit data.
- **`--actor` and `--reason` are required by `set`, and only by `set`.** A privileged offline
  operation has to record who did it and why. `validate` needs neither, because it writes
  nothing — the asymmetry is the point.
- **`format` defaults to `csvlog` and `follow` to `true`**, so a standard pgAudit deployment
  needs neither.

Applying prints what changed, field by field, and tells you how to make a **running** engine
pick it up without a restart — `POST /v1/console/runtime/reload`, or a `SIGHUP`, which under
the packaged unit is:

```bash
sudo systemctl reload olivares
```

The service user needs read access to that log file; on most distributions that means adding
`olivares` to the `adm` or `postgres` group — a deliberate grant you make, not something the
package does for you.

## 6. Upgrading

`olivares upgrade` replaces the binary in place, and the safety properties are the reason to
prefer it over re-installing the package by hand: it **never replaces the binary until the
downloaded candidate has been exec-probed successfully**, it keeps a timestamped backup, and
it **reverts to that backup if the post-swap probe fails**.

```bash
sudo olivares upgrade --check    # what would change, without changing anything
sudo olivares upgrade --yes      # do it
```

Three flags matter to a packaged install specifically:

- **`--endpoint`** — take updates from a GitHub repository you control rather than the
  default. This is the escape hatch for a mirror or a fork.
- **`--bundle`** — install from a local bundle directory or `.tar.gz` with **no network at
  all**. Building that bundle and moving it across is
  [Install in an air-gapped environment](/how-to/air-gap-install/).
- **`--install-timer`** — emit an **opt-in** systemd timer and service that check for updates
  on a schedule. Nothing installs this for you; see
  [what the package does not do](#7-what-the-package-does-not-do).

Note that the staging and the exec-probe happen **in the install directory, next to the
target — not in `/tmp`**, so the `noexec` mount discussed [above](#the-one-thing-a-hardened-host-gets-wrong-an-executable-tmpdir)
does not break an upgrade. A `noexec` mount on the **install** directory is a different
matter and makes the installed version unmeasurable; that case, the release channels, staged
rollout and rolling back are [Upgrade and roll back](/how-to/upgrade-and-rollback/).

## 7. What the package does not do

Stated plainly, because a security product that is vague here does not deserve the install:

- **It does not add a repository.** Nothing is written to `/etc/apt/sources.list.d`,
  `/etc/yum.repos.d` or `/etc/apk/repositories`. You installed one file; only that file was
  installed. Upgrades are yours to trigger — by a new package, or by `olivares upgrade`.
- **It does not start or enable the service.** The install runs `systemctl daemon-reload`
  and prints what to do next; `enable --now` is your decision.
- **Verifying a licence never calls anyone. Downloading what you paid for does.** Licence
  validation in the open build is offline Ed25519, there is no remote kill switch, and no
  licence key gates or degrades that build — the AGPL build is the whole platform.
  There is **no mandatory telemetry and no control-plane egress by default: what crosses
  your perimeter is what you configure to cross it** — calls to your model APIs, the
  SIEM/webhook outputs you wire, an external embedding provider if you provision one, and
  any source a connector polls (its `addr`, `base_url` or `endpoint`) at the interval you set.
- **But `olivares upgrade` does make a network call, deliberately, when you run it** —
  that is the point of an update check, and `--check` shows you the plan before anything
  moves. The honest form of the promise is: *verifying a licence never calls anyone;
  downloading what you paid for does.* Use `--bundle` if you want the update path to make
  no call either.
- **It does not open a port to the network.** The unit binds loopback only until you widen
  it yourself.

## See also

- [Verify what you downloaded](/how-to/verify-a-release/) — the full verification path
- [Upgrade and roll back](/how-to/upgrade-and-rollback/) — channels, staged rollout, rollback
- [Harden a deployment](/how-to/security-hardening/) — beyond what the unit already does
- [Install in an air-gapped environment](/how-to/air-gap-install/)
- [Deploy with Docker](/how-to/docker-deployment/)
- [Connect a source](/how-to/connect-a-source/)
- [Back up and restore](/how-to/backup-and-restore/)
