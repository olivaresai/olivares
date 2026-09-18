---
title: Install from a package
description: >-
  Install Olivares AI from the .deb, .rpm or .apk on a hardened Linux host: verify the
  release before you trust it, run it under the packaged systemd unit or, on default
  Alpine, in the foreground, take your first source, and upgrade — online, pinned, or fully
  air-gapped.
draft: false
---

:::note[Published package names]
The v26.9.1 GitHub release publishes `.deb`, `.rpm` and `.apk` assets for both `amd64`
and `arm64`, with `checksums.txt`, `checksums.txt.sig` and `checksums.txt.pem`. The
commands below use the literal `amd64` names from that release; replace `amd64` with
`arm64` on a 64-bit ARM host. Install from those verified release assets. Repository
metadata producers in a source tree are not installation instructions for this guide.

**DIST-24-05 qualification.** What CI qualifies is the verified **shell installer** and
its service/doctor contract, not `dpkg`, `rpm` or `apk`: a dispatch/pull-request matrix
runs it against the published release in Debian stable, Ubuntu 24.04 LTS,
Fedora, openSUSE Leap and Alpine container userlands and on a hosted macOS 14 runner,
and it fails as unmeasurable when the public release cannot be reached rather than
counting a dry-run as coverage. That matrix does not certify native package-manager installation.
The native package lifecycle of packages built from this source — install, start, restart,
upgrade of a running OpenRC service and removal — has been exercised locally in a
disposable Alpine guest; that is evidence for this tree, not a signed, hosted or
preproduction qualification, which remains pending.

**DIST-24-06 proposed repositories (not a live install surface).** The source tree
contains deterministic apt, rpm-md and APK repository producers, a signed-index verifier,
a clean-client qualification and a staged publication workflow whose dispatch stays inert
until a reviewer approves it. **No package-repository URL is live**, no DNS name is
delegated and no production repository-signing key is provisioned. Nothing in this
proposal is a package-manager source; keep using the verified release assets below.
:::

This is the path for a normal Linux host where you want the engine as a service, not in a
container. Debian/Ubuntu and RHEL/Fedora/SUSE default to **systemd**. Alpine's default init
is **OpenRC**, not systemd. For containers see [Docker deployment](/how-to/docker-deployment/);
for a host with no route out at all, [Install in an air-gapped environment](/how-to/air-gap-install/),
which this page links back to at the upgrade step.

## 1. Verify the release before you trust it

For a security product the build pipeline is part of the trust model, so nothing here asks
you to take the download on faith. Put the package, `checksums.txt` and the signature in one
directory and run the verifier **from that directory**:

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh
```

Releases are signed keyless and do not publish a cosign public key, so use the keyless command
for packages downloaded from a release. It needs Sigstore trusted-root material, which cosign
fetches unless it is already cached. `--offline` removes only the Rekor lookup; it does not make
verification network-free. `--key` is only for files signed with a private key you control, and
you must obtain that public key separately from the files it verifies.

What each step checks, how it behaves on a partial release, and how to verify the container
image instead is [Verify what you downloaded](/how-to/verify-a-release/).

## 2. Install the package

The three package formats carry the binary at `/usr/bin/olivares`, a commented
environment file at `/etc/olivares/olivares.env` (marked `config|noreplace`, so your edits
survive an upgrade), the data directory `/var/lib/olivares`, and the licence texts —
`LICENSE`, `NOTICE`, `LICENSING.md`, `DISCLAIMER.md` — under `/usr/share/doc/olivares/`.
`.deb` and `.rpm` ship the hardened **systemd** unit at
`/usr/lib/systemd/system/olivares.service`. **Packages built from this source** put an
executable **OpenRC** unit at `/etc/init.d/olivares` in the `.apk`, with an explicit
`package-init` stamp so hooks do not guess from host `systemctl` presence.

The **previously published** `.apk` assets shipped that same systemd unit and did **not**
ship an OpenRC unit. That published linux tarball carries licence texts, README and
`SECURITY.md`; it does not include `scripts/install-service.sh` or `packaging/service/`.
Those adapter files are in the in-tree signed archive for the next release.

```bash
# Debian / Ubuntu
sudo dpkg -i olivares_26.9.1_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -Uvh olivares_26.9.1_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_26.9.1_linux_amd64.apk
```

Installing **creates the system user and group `olivares`** (with `/usr/sbin/nologin` as
its shell and `/var/lib/olivares` as its home), creates `/var/lib/olivares` mode `0750`
owned by that user, and creates `/etc/olivares`. systemd packages reload systemd when
`systemctl` is present; OpenRC packages do not enable or start the service. It does
**not** start anything — see [what the package does not do](#8-what-the-package-does-not-do).

### Start the engine

On Debian/Ubuntu and RHEL/Fedora/SUSE:

```bash
sudo systemctl enable --now olivares
```

On **OpenRC** (`.apk` built from this source):

```bash
sudo rc-service olivares start
# optional; the package does not do this:
sudo rc-update add olivares default
```

The first-boot token is in `/var/log/olivares.log` (and `logread` if syslogd is
running). Extra flags in
`OLIVARES_EXTRA_ARGS` are appended with globbing disabled and split on spaces;
nested quoting is not interpreted, and the env file is not sourced as shell.

On the **previously published Alpine** package, `systemctl` is absent and that `.apk` has no OpenRC
unit. Start the engine as the service user; the first-boot token is printed on stdout:

```bash
sudo -u olivares olivares serve --data-dir=/var/lib/olivares \
  --listen=:8443 --grpc-listen=:8444 --checkpoint-interval=1h
```

## 3. The hardened systemd unit

The packaged systemd unit runs the engine as the unprivileged `olivares` user with an empty
capability bounding set — it holds no capabilities at all, ambient or bounding — and
`NoNewPrivileges=true`, so nothing it launches can gain any. On top of that it carries
`ProtectSystem=strict` (the filesystem is read-only except `ReadWritePaths=/var/lib/olivares`),
`ProtectHome`, `PrivateTmp`, `PrivateDevices`, the four `ProtectKernel*`/`ProtectClock`
directives, `RestrictNamespaces`, `RestrictSUIDSGID`, `RestrictRealtime`, `LockPersonality`,
`MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, a `@system-service` syscall
filter that additionally drops `@privileged` and `@resources`, and `UMask=0027`.

On default Alpine those systemd directives do not apply to a **previously published**
`.apk`, because that payload's systemd unit is not running. `.apk` packages built from
this source run under OpenRC instead: they use the `olivares` account, write the
first-boot token to `/var/log/olivares.log`, and do not implement systemd
sandbox directives.

**The listeners accept connections from the network by default** — `--listen=:8443` for
HTTP (REST plus the embedded console) and `--grpc-listen=:8444` for gRPC, the dual-stack
wildcard. This is a server, and what protects it is TLS on, no default credentials and a
single-use setup token. Restrict it deliberately with
`OLIVARES_EXTRA_ARGS=--listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444` in
`/etc/olivares/olivares.env` — those flags are appended after the unit's own and the later
flag wins — and front the result with your own TLS termination for a certificate browsers
trust. For IPv6 loopback use `--listen=[::1]:8443`.

### Executable scratch mount

This is the failure worth knowing in advance on a systemd host, because the symptom does
not name its cause.

Out-of-process first-party connectors ship **embedded in the binary**. At boot the engine
extracts the ones it needs into private scratch and executes them as subprocesses. When
`TMPDIR` is unset, scratch is created under `<data-dir>/tmp`; only an unwritable data
directory makes the engine fall back to the system temporary directory. An explicit
`TMPDIR` always wins.

The packaged systemd service therefore uses `/var/lib/olivares/tmp`, not `/tmp`, by default.
Check the mount that will actually hold the executable scratch:

```bash
check_scratch_mount() {
  target=${1:-/var/lib/olivares}
  opts=$(findmnt -no OPTIONS --target "$target") || {
    printf '%s\n' "cannot read mount options for $target (missing path or permission)" >&2
    return 1
  }
  [ -n "$opts" ] || {
    printf '%s\n' "mount options for $target are unknown" >&2
    return 1
  }
  case ",$opts," in
    *,noexec,*) printf '%s\n' 'noexec — set TMPDIR' ;;
    *) printf '%s\n' 'exec-capable — nothing to do' ;;
  esac
}
check_scratch_mount /var/lib/olivares
```

If it says `noexec`, point `TMPDIR` at a directory that is writable under
`ProtectSystem=strict` **and** lies on an exec-capable mount:

```bash
sudo install -d -o olivares -g olivares -m 0750 /run/olivares-exec-tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/run/olivares-exec-tmp
ReadWritePaths=/run/olivares-exec-tmp
```

Then restart. If an exec is rejected with `EACCES` or `ENOEXEC`, the engine's error names
the extraction mount and the two relocation controls (`TMPDIR` and data-dir); it does not
report a noexec mount as a missing connector.

Use `systemctl edit`, never a direct edit of `/usr/lib/systemd/system/olivares.service`:
that file belongs to the package and an upgrade replaces it.

## 4. First boot: `olivares quickstart`

`quickstart` is `serve` with friendly defaults and a guided banner. It never invents
default credentials; it points you at the embedded console to create your first
administrator with a **one-time token**.

Running under the packaged systemd unit you are already serving, so you do not run
`quickstart` — **the first-boot setup token is printed to the journal**:

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

If you started `serve` in the foreground (default Alpine), the same banner is on stdout.

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
pick it up without a restart — `POST /v1/console/runtime/reload`, or a `SIGHUP`. Under the
packaged systemd unit that is `sudo systemctl reload olivares`. If you started `serve` in
the foreground, send `SIGHUP` to that process.

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
- **`--install-timer`** — emit an **opt-in systemd** timer and service that check for updates
  on a schedule. Nothing installs this for you; see
  [what the package does not do](#8-what-the-package-does-not-do). It is a systemd generator.

Note that the staging and the exec-probe happen **in the install directory, next to the
target — not in `/tmp`**, so the `noexec` mount discussed [above](#executable-scratch-mount)
does not break an upgrade. A `noexec` mount on the **install** directory is a different
matter and makes the installed version unmeasurable; that case, the release channels, staged
rollout and rolling back are [Upgrade and roll back](/how-to/upgrade-and-rollback/).

## 7. Uninstall or migrate without guessing paths

The package writes `/var/lib/olivares/install-manifest.json`. The uninstaller validates
the complete manifest against the signed distribution index before touching the service or
filesystem; an unexpected path returns 2. Inspect first, then choose retention explicitly:

```bash
sudo olivares uninstall --plan --data-dir /var/lib/olivares
sudo olivares uninstall --preserve --data-dir /var/lib/olivares
sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
```

Preserve is the package-removal policy: it retains configuration, data, logs, keys and
their service identity. On systemd packages the removal hook runs `--preserve`. On OpenRC
`.apk` packages built from this source the hook stops the service if it is active, removes
the default runlevel entry without failing if it was never enabled, validates the complete
manifest with `--plan`, and lets apk remove package-owned files. Previously published Alpine
packages validated with `--plan` only, because that payload had no OpenRC unit to stop.
Run `--purge` before removing the package only when erasure is the intent. It requires
confirmation and deletes only indexed paths.

For an estate move, create an `olivares dr backup` first, install the destination and run
`olivares dr restore` there. Current bundles use `hmac-sha256-kek-v1` to authenticate the
manifest and every payload under your KEK. A newer-engine export is refused before writes;
a separately authenticated pre-v26.9 bundle needs `--allow-legacy-unsigned` explicitly.
The [backup and restore guide](/how-to/backup-and-restore/) covers KEK custody and the
post-import continuity proof.

## 8. What the package does not do

Stated plainly, because a security product that is vague here does not deserve the install:

- **It does not add a repository.** Nothing is written to `/etc/apt/sources.list.d`,
  `/etc/yum.repos.d` or `/etc/apk/repositories`. You installed one file; only that file was
  installed. Upgrades are yours to trigger — by a new package, or by `olivares upgrade`.
- **It does not start or enable the service.** systemd packages reload systemd when
  `systemctl` is present and print `systemctl enable --now`. OpenRC packages print
  `rc-service olivares start` and do not `rc-update add`. Starting remains your decision.
  An upgrade of an already-running OpenRC service stops it, replaces files, and starts it
  again; an inactive service stays inactive.
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
- **It opens a port to the network.** The unit binds every interface until you restrict
  it yourself.

## See also

- [Verify what you downloaded](/how-to/verify-a-release/) — the full verification path
- [Upgrade and roll back](/how-to/upgrade-and-rollback/) — channels, staged rollout, rollback
- [Harden a deployment](/how-to/security-hardening/) — beyond what the unit already does
- [Install in an air-gapped environment](/how-to/air-gap-install/)
- [Deploy with Docker](/how-to/docker-deployment/)
- [Connect a source](/how-to/connect-a-source/)
- [Back up and restore](/how-to/backup-and-restore/)
