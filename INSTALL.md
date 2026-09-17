# Installing Olivares AI

The engine is a **single static Go binary** (`olivares`) with the web console embedded. It
runs self-hosted on Linux or macOS, as a container, or on Kubernetes. This page is the
per-OS install matrix and production setup; the [README](README.md#install) has the
short version, and the deployment tutorials (Compose, Kubernetes/Helm, air-gapped) live in
[`docs-site/`](docs-site/).

> **Beta.** Releases are cut from this repository. The binaries,
> images and packages below **are published from the latest tagged release (`v26.9.0`)**;
> [building from source](#from-source) remains supported. Everything is self-hosted: the engine
> makes no mandatory outbound calls at boot and verifying a licence never calls us. The one
> command that reaches us is `olivares upgrade`, which fetches from the update channel unless
> `--endpoint` or `--bundle` points it elsewhere; commercial add-ons, updates and patches are
> downloaded with your subscription as the credential ([`LICENSING.md`](LICENSING.md#what-the-subscription-does-and-does-not-call-home-for)).

**Verify before you run.** For a security product the supply chain is part of the trust
model. A cosign-signed checksum manifest covers the archives and native packages; archives
also carry SBOM attestations, and container images are signed and SBOM-attested by digest.
OpenVEX and SLSA provenance cover the release set. The installer verifies cosign plus
SHA-256; Homebrew checks each cask download against its recorded SHA-256. For manual downloads
use [`scripts/verify-release.sh`](scripts/verify-release.sh) (see
[Verifying a release](#verifying-a-release)).

## Versioning

Releases use **CalVer**: `vYY.M.PATCH` — two-digit year, month, and the release number
within that month. The latest public release is `v26.9.0` (September 2026); a same-month fix is
`v26.9.1`. Container tags follow: `:26.9.0`, `:latest`, plus the `-fips` / `-stig` variants.
The maturity label (**beta**) is separate from the version; a release that should be flagged
*pre-release* on GitHub is tagged with a suffix, e.g. `v26.9.0-beta.1`.

---

## The address a browser reaches the console at

A **bind is not an address.** `--listen :8443` asks the kernel for every interface;
`--listen 127.0.0.1:8443` names a loopback socket. Neither tells the engine the URL your
operators actually type, and behind a reverse proxy the two are never the same thing. Tell
it, with the flag or with the environment key:

```sh
olivares serve --listen :8443 --public-url https://olivares.example.com
# or, the same thing, in the env file / Compose / your orchestrator:
OLIVARES_PUBLIC_URL=https://olivares.example.com
```

Two things depend on it.

1. **The first-boot panel prints it.** Without it the panel falls back to the bind, which
   on a wildcard bind can only offer a same-machine URL — true on the host, useless in the
   message you paste to a colleague.
2. **Passkeys need it.** A passkey relying party has to be a domain name, and the two ways
   an address fails that are refused in different places, by different things:
   - at an **IP address** — `https://10.0.0.7:8443` — the *browser* refuses the ceremony. The
     console still asks the engine for options first, so a request does leave; what the browser
     refuses is the ceremony that would follow.
   - at a **single-label name** other than `localhost` — `https://olivares` — this build's
     *verifier* refuses, and the engine answers the options request with a 503 carrying the
     remedy. That limit is the installed library's, not a rule of the WebAuthn specification.
   - at a **dotted name that is not a valid domain** — `https://my_host.example.com`,
     `https://-lead.example` — the strict-domain rule refuses it. Adding a dot is not the
     remedy here: the name already has one.

   `localhost` is a working relying party, so a local install needs nothing here. Declare
   the name and privileged login works at it **provided** the console is reached at that
   name, over a secure context (https, or the loopback family over http).

The rules, in full:

| | |
|---|---|
| accepted value | an absolute origin, `scheme://host[:port]`, `https` or `http`. No path, no query, no fragment, no credentials — those are refused at start-up and the value is **not** echoed into the log |
| precedence | `--public-url` wins over `OLIVARES_PUBLIC_URL`. Passing the flag **empty** (`--public-url=`) clears an environment setting rather than falling back to it |
| default | empty — no declared address, and the behaviour is exactly what it was before |
| scope | `serve`, `quickstart` and `quickstart governed-rag`, each with its own flag |
| when it is read | start-up only. **A change takes a restart**; there is no hot reload and nothing is stored |
| relation to `--listen` | none. It changes no bind, no port and no TLS setting |
| scheme vs transport | declaring `https` while the engine serves plain HTTP, or the reverse, is legitimate when something in front of the engine changes the transport. The panel names the difference, states only the protocol **this** process is serving, and never refuses the combination or claims a particular topology |

If your deployment already pins `OLIVARES_WEBAUTHN_RPID` **and** `OLIVARES_WEBAUTHN_ORIGINS`,
that pair still wins — it is how you keep one relying party across several panel host names.
An origin in that list is **not required to be under the relying-party ID**: WebAuthn Level 3
allows related origins, validated by the browser against
`https://<rp-id>/.well-known/webauthn`. The engine accepts such a pair as configuration; it
neither fetches nor publishes that document, so acceptance at start-up is not evidence that a
browser will complete the ceremony.
The pair is **validated at start-up**: half a pair, an IP, or a name no browser
will accept now refuses the boot with the key to fix, instead of being taken on trust and
failing every ceremony later. Correct the pair, or clear **both** keys, and restart.

---

## Linux

### HTTPS convenience path

The script body itself arrives over HTTPS; the pipe does not pre-verify those bytes.
Once running, it detects your OS/architecture, requires `cosign`, verifies the signed
checksum manifest and archive SHA-256, and installs to a writable directory without
invoking sudo. Non-interactive use must pin the version:

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.9.0
```

Use `--bindir "$HOME/.local/bin"` to select an absolute install directory. Verification
cannot be bypassed and privilege escalation is always an explicit operator step. The
default installs only the binary. For a user service, add `--user`; for a system service,
run the already verified script from an explicitly privileged shell with `--system`.
Neither path invokes `sudo`, and neither starts the service unless `--start` is also
present:

```sh
# systemd user service on Linux, LaunchAgent on macOS; review before starting
ver=YY.M.PATCH
sh "olivares-install-$ver.sh" --version "v$ver" --user
olivares doctor --mode user

# explicit system privilege; auto-detects systemd/OpenRC/launchd
sudo sh "olivares-install-$ver.sh" --version "v$ver" --system --start
sudo olivares doctor --mode system --data-dir /var/lib/olivares
```

The adapter preserves an existing config, refuses unsafe data/config modes, validates
key names before any init mutation, and checks both `/livez` and `/readyz` after an
explicit start. It records `<data-dir>/install-manifest.json` and names the log from
which to retrieve the one-time first-boot token. `olivares doctor` emits text or JSON
without config values; rc 0 is healthy, rc 1 a measured defect and rc 2 a required
check that could not be measured. For the full download-verify-execute and service
contracts, read [`docs/RELEASE-INSTALLER.md`](docs/RELEASE-INSTALLER.md).

**DIST-24-05 qualification.** Pull requests that touch the installer or engine run the
verified shell installer against the published release on Debian stable,
Ubuntu 24.04 LTS, Fedora, openSUSE Leap and Alpine userlands, plus a hosted macOS 14
runner. Each live leg checks the installed path/mode/owner and version, then exercises
this commit's opt-in service adapter, an explicitly separate `--start`, and
`olivares doctor -o json` through real TLS health probes. The workflow returns an
unmeasurable failure if the public release cannot be reached; its dry-run is not counted
as live coverage. This matrix does not certify native package-manager installation:
`.deb`, `.rpm` and `.apk` repository/package-manager qualification is a separate surface.

### Native packages (`.deb` / `.rpm` / `.apk`)

**DIST-24-06 proposed repositories (not a live install surface).** The source tree now
contains deterministic apt, rpm-md and APK repository producers plus signed-index and
byte-identity qualification. **No package-repository URL is live**, no DNS name is
delegated, and no production repository key is provisioned. Until those separate operator
acts happen, use the signed GitHub Release assets and the direct commands below; do not add
a package-manager source based on this proposal.

Each package installs the binary to `/usr/bin/olivares`, an example env file, and a
no-login `olivares` service user plus the data dir `/var/lib/olivares`. `.deb` and
`.rpm` ship a **hardened systemd unit**. `.apk` packages built from this source ship
an executable **OpenRC** unit at `/etc/init.d/olivares`. The previously published `.apk`
assets still shipped the systemd unit and no OpenRC unit. The package does **not**
auto-start or enable the service — starting it is your explicit decision.

```sh
# Debian / Ubuntu
sudo dpkg -i olivares_*_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -i olivares_*_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_*_linux_amd64.apk

# systemd hosts (loopback-only by default; see the env file to widen it)
sudo systemctl enable --now olivares
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# OpenRC hosts (apk from this source; the package does not rc-update add)
sudo rc-service olivares start
sed -n '/FIRST-BOOT SETUP/,/========================/p' /var/lib/olivares/olivares.log
```

**Configure it the guided way (recommended).** Rather than hand-editing the env file,
run the expert installer: it picks a profile, asks only what that profile needs,
validates every value, and writes a structured env file with secrets kept out of it
(referenced as `file:<path>`).

```sh
sudo olivares setup            # interactive: eval | single-node-prod | postgres-prod | k8s
# …then apply it:
sudo systemctl restart olivares
```

For **Postgres**, provision the least-privilege roles from the binary first (no SQL by
hand) and verify them before you boot:

```sh
olivares db init  --superuser-dsn "postgres://postgres@db:5432/postgres" \
  --app-role olivares_app --app-password-file /run/secrets/app_password
#   add --owner-role / --admin-role for the owner-split and cross-tenant admin roles
olivares db check --dsn "postgres://olivares_app@db:5432/olivares?sslmode=verify-full"
```

`olivares config generate …` is the non-interactive twin for CI/config-management.
Prefer to edit `/etc/olivares/olivares.env` directly? It is preserved across upgrades:

```sh
# expose beyond loopback with a dual-stack Go bind (front it with your own
# TLS-terminating reverse proxy), or switch to Postgres, pin a TLS cert, etc.
OLIVARES_EXTRA_ARGS=--listen=:8443 --grpc-listen=:8444
```

The default listeners are **loopback-only** with a self-signed cert generated on first
boot (secure-by-default, [`docs/SECURITY-HARDENING.md`](docs/SECURITY-HARDENING.md)). Uninstalling never
deletes `/var/lib/olivares` — it holds the append-only audit ledger and signing key; remove
it by hand if you really mean to.

### Manual binary (tarball)

```sh
ver=v26.9.0; os=linux; arch=amd64
base=https://github.com/olivaresai/olivares/releases/download/$ver
curl -fsSLO $base/olivares_${ver#v}_${os}_${arch}.tar.gz
curl -fsSLO $base/checksums.txt
curl -fsSLO $base/checksums.txt.sig
curl -fsSLO $base/checksums.txt.pem
scripts/verify-release.sh                       # cosign + SHA-256 (+ SBOM/VEX/SLSA if present)
tar -xzf olivares_${ver#v}_${os}_${arch}.tar.gz
sudo install -m0755 olivares /usr/local/bin/olivares
```

### Docker

Multi-arch (amd64/arm64), distroless, non-root. Run it — secure by default (TLS, loopback,
one-time setup token) with a persistent data volume:

```sh
docker run -d --name olivares -p 127.0.0.1:8443:8443 -p 127.0.0.1:8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares:latest \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`--listen :8443` is the container-safe bind: Go listens dual-stack (IPv4+IPv6), and on
IPv6-disabled kernels it still serves IPv4. `0.0.0.0:8443` would bind IPv4 only. The host
mapping (`-p 127.0.0.1:…`) is what keeps it loopback-only on the host; use
`-p [::1]:8443:8443` / `-p [::1]:8444:8444` for IPv6 loopback instead.

Or, just to look around, an ephemeral synthetic estate (loopback, plaintext — never for real data):

```sh
docker run --rm -p 127.0.0.1:8443:8443 --tmpfs /data:uid=65532,gid=65532 \
  docker.io/olivaresai/olivares:latest \
  serve --seed-demo --insecure --listen :8443 --data-dir /data
```

The packaged systemd unit stays loopback-only with `--listen=127.0.0.1:8443`; change it to
`--listen=[::1]:8443` (and similarly for gRPC) if you want IPv6 loopback there.

The official image is `docker.io/olivaresai/olivares` (Docker Hub). `ghcr.io/olivaresai/olivares`
is the **fallback**: the release pipeline builds and signs on ghcr.io and then copies the same
content to Docker Hub **by digest** (`cosign copy`), so both coordinates resolve to identical
layers, signatures and attestations. Docker Hub applies a rate limit to **anonymous** pulls;
ghcr.io does not rate-limit anonymous pulls of public images — `docker login` on Docker Hub, or
switch the host to `ghcr.io`, if a CI node or a large fleet hits the ceiling. Tags: `:26.9.0`
(pin a release), `:latest`, `:26.9.0-fips` (FIPS 140-3 mode, CMVP #5247) and `:26.9.0-stig`
(STIG-profiled UBI base) — see [SCP-09](docs/SCP-09-FIPS-STIG.md). The base and `:latest` tags
are multi-arch (amd64/arm64); `-fips`/`-stig` are amd64-only. **For production, pin by digest**
(`docker.io/olivaresai/olivares@sha256:…`); the mutable tags above are for evaluation only. Verify
the image: `cosign verify docker.io/olivaresai/olivares:26.9.0 --certificate-identity-regexp
'^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
--certificate-oidc-issuer
https://token.actions.githubusercontent.com` (the same verification works identically against the
`ghcr.io/olivaresai/olivares:26.9.0` fallback — same digest, signatures and attestations).

### Docker Compose

A hardened, ready-to-edit stack (SQLite single-node, optional Postgres + backup):

```sh
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**DIST-24-12 current tree contract.** The image's own `olivares readyz` command is
the reference healthcheck; it needs no shell, curl, or wget. Local HTTP/TLS fixtures
prove 200 → rc 0, a received non-200 → rc 1, and an unmeasurable probe → rc 2.
**Docker qualification remains unmeasured until the dispatch workflow succeeds** for
this commit; the checked-in workflow is a qualification path, not evidence of a run.

See [`deploy/compose/`](deploy/compose/) and the
[Docker Compose tutorial](docs-site/src/content/docs/tutorials/getting-started/docker-compose.mdx).

### Kubernetes

The Helm chart ships as source in [`deploy/helm/olivares`](deploy/helm/olivares). Its OCI
publication — `oci://ghcr.io/olivaresai/charts/olivares`, cosign-signed — is **not live yet**
(measured 2026-09-01: the registry answers `NAME_UNKNOWN`), so install the chart from the tree,
or use the flat Helm-free manifest for a `kubectl`-only / air-gapped host:

```sh
# Helm, from the tree (the OCI coordinate above is not published yet)
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace

# or Helm-free
kubectl create namespace olivares-system
kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

See [`deploy/helm/`](deploy/helm/) and the
[Kubernetes tutorial](docs-site/src/content/docs/tutorials/getting-started/kubernetes.mdx).

### Air-gapped

Bundle the signed image + chart + verification material and move it across the gap; see the
[air-gap how-to](docs-site/src/content/docs/how-to/air-gap-install.md) and
[`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md).

---

## Operate Claude Code (co-deployment)

Beyond *observing* and *governing* Claude Code, the engine can **conduct** it — launch a
real `claude` process, bridge its I/O into a governed stream, and tear it down, over a
shared workspace. This is an **opt-in** layer: the base image above is distroless and
carries no `claude`; you add this only if you run governed Claude Code sessions.

`claude` is installed from **Anthropic's official, GPG-signed source** (the signed
apt/dnf/apk repos), pinned, auto-update off — never redistributed by us (their terms
don't permit it). Bring-your-own is supported too.

**Both in Docker** — one hardened combined image + a workspace volume:

```sh
docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares:26.9.0 -t olivares-agentops:26.9.0 .
OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.9.0 \
  docker compose -f deploy/compose/docker-compose.yml \
                 -f deploy/compose/docker-compose.agentops.yml up -d
```

**Both native** — one command (verifies the engine signature, installs `claude` from the
signed repo, wires the hardened systemd drop-in; does not auto-start):

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

The native layout is configurable and recorded. `OLIVARES_DATA_DIR` selects the data
directory (default `/var/lib/olivares`) and `OLIVARES_WORKSPACE_DIR` the workspace root
(default `<data-dir>/workspaces`). One selected layout is rendered everywhere: the signed
adapter writes the data directory into `olivares.service` and the ownership manifest, and
the installer renders the AgentOps drop-in (claude `HOME`, token dir, `ReadWritePaths` for
an external workspace), creates `/etc/olivares/agentops.env` once with the token path under
that data directory, and records the drop-in, env and workspace in the manifest so
`olivares doctor` and `olivares uninstall` resolve the same layout. Paths must be absolute
and canonical; a data directory must be a dedicated directory at least two levels deep
(`/srv/olivares`, not `/srv`), and a path is never provisioned through a symbolic link
(pass the resolved path). A space is quoted where systemd unquotes it; OpenRC refuses it.

`/etc/olivares/agentops.env` is one fixed path shared by every estate on the host, so a
second estate installed over a preserved first one is a transition, not a fresh file. The
installer decides it by ownership and rewrites at most the single
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE` assignment: a value inside the selected data directory
is left alone; a value inside no estate is preserved as the deliberate external path your
refresher writes, unless this installer generated the file and the value has exactly the
`<data-dir>/run/session-token` form for a directory that no longer answers with an ownership
record, which is ambiguous and refused instead; a value that is still exactly the default
generated for another estate —
proved by the generated marker in the file and by that estate's own ownership record carrying
this file as a managed runtime env — is re-pointed at the selected data directory, with every
other line, comment included, preserved; anything else is refused, before the layout is
recorded and before the success banner, rather than announced as wired over a credential path
belonging to an estate that is about to be removed. `OLIVARES_RUNTIME_TOKEN_FILE` decides it
explicitly: `estate` for `<data-dir>/run/session-token`, `keep` to preserve the configured
path exactly, or an absolute path to name the file your refresher writes.
The comparison uses systemd's `EnvironmentFile=` grammar — including whitespace, quotes,
escapes, continuations and last-assignment-wins — without sourcing or evaluating the file.
If the installed systemd version cannot be measured and the file uses syntax whose meaning
changed between supported versions, the installer refuses before classifying the value as
external; `keep` remains the explicit byte-preserving choice. Each supported manager gets its
own reading of an escape (a double-quoted `\<CR>` is eaten before systemd 247 and handed to
the process as backslash+CR from 247 on, which is no path). A file systemd refuses to load at
all — one with a NUL byte, or with any assignment whose key or value is not valid UTF-8 — is
refused before any value in it is classified or re-pointed, because with the
`EnvironmentFile=-` the drop-in uses the whole file would be skipped and no token path would
be wired; `keep` preserves such a file and says so instead of claiming a loaded value.

A rerun without knobs recovers the layout from the installed unit (its `ExecStart=`
command line, never a comment or another directive) and manifest, including after an
engine upgrade through the signed adapter, which carries the recorded workspace and
AgentOps files across its manifest rewrite. Only the managed drop-in (marked
`olivares.ai/agentops-dropin/v1`) is regenerated; an operator's own drop-in, the runtime
env and an existing external workspace are left untouched, and a knob that contradicts
the installed unit's data directory is refused rather than re-rendered around it.

The hardened unit hides some locations from the service unless the drop-in binds exactly
one directory back in, which is what it renders: a workspace under `/home`, `/root` or
`/run/user` gets `ProtectHome=tmpfs` plus `BindPaths=` for that directory (other homes
stay hidden), and a workspace under `/tmp` or `/var/tmp` keeps `PrivateTmp=true` and gets
`BindPaths=` for that directory alone (the rest of the host's temporary tree stays
hidden), which needs systemd 235 or later — the installer reads `systemctl --version` and
refuses an older host, naming the version. `/tmp` and `/var/tmp` are shared and are
cleared periodically on many distributions, and the installer warns before using one. A
workspace under `/dev`, `/proc` or `/sys` is refused: those hold kernel and device
interfaces rather than durable state. (Until 2026-09-05 this paragraph claimed that no
directive could reach a workspace under `/tmp`; that was false — see
[`docs/RELEASE-INSTALLER.md`](docs/RELEASE-INSTALLER.md) for the upstream commit and what
has and has not been verified on a live manager.)

Secure by default in every case: loopback-only, non-root (65532), read-only root, the
deny-closed inference credential, and an anchored audit ledger over the session lifecycle.
The four topologies (both-Docker, both-native, and the two mixed cases with their honest
constraints), the first-session walkthrough, and the bring-up smoke
([`scripts/smoke-agentops.sh`](scripts/smoke-agentops.sh)) are in the
[Run Claude Code with Olivares](docs-site/src/content/docs/how-to/run-claude-code-with-olivares.md)
how-to.

Once it is installed, the four outcomes an operator actually has — connect Claude Code
under governance, launch a governed session, verify the evidence ledger, and recover a
failed delivery — are written as CLI recipes in
[`docs/CLI-RECIPES-BY-OUTCOME.md`](docs/CLI-RECIPES-BY-OUTCOME.md). Each one crosses
several command groups, so no single `--help` can hold it; every command on that page is
gated against the real command tree by a test.

---

## macOS

### Homebrew (recommended)

The cask installs the signed binary and **clears the Gatekeeper quarantine** for you:

```sh
brew install olivaresai/tap/olivares
olivares quickstart                               # secure by default; prints the console URL + one-time setup token
# or, just to look around, an ephemeral synthetic estate (loopback, plaintext):
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

### Manual binary

```sh
ver=v26.9.0; arch=arm64   # or amd64 on Intel
base=https://github.com/olivaresai/olivares/releases/download/$ver
curl -fsSLO $base/olivares_${ver#v}_darwin_${arch}.tar.gz
# ...verify (see Verifying a release), then:
tar -xzf olivares_${ver#v}_darwin_${arch}.tar.gz
xattr -d com.apple.quarantine olivares   # the binary is not yet Apple-notarized
sudo install -m0755 olivares /usr/local/bin/olivares
```

> **Notarization status:** the darwin binaries are signed by cosign (supply-chain trust) but
> are **not yet Apple-notarized**, so Gatekeeper quarantines a direct download — the Homebrew
> cask handles this automatically, or clear it manually as above. Apple Developer ID signing +
> notarization is a planned step (needs an Apple Developer account).

---

## Windows

**Not built yet.** There is no `goos: windows` build, so there is no Windows binary, archive
or installer today. Interim options:

- Run the **Linux container** (Docker Desktop / WSL2): the `docker run` command above works.
- Run under **WSL2** with the Linux one-line installer.
- [Build from source](#from-source) (Go is cross-platform).

### Plan (when prioritized)

The engine is a server; Windows support targets the **CLI/operator** use first. The release
config is designed for it — when greenlit, the additions are:

- `builds`: add `windows` to `goos` (the code is pure-Go, CGO-off, so it cross-compiles).
- `archives`: a `zip` format override for `windows` (instead of `tar.gz`).
- Package managers: a **Scoop** bucket (`scoops:`) and a **winget** manifest (`winget:`),
  each pushing to its own manifest repo (`olivaresai/scoop-bucket`, a winget-pkgs PR).
- Code signing: an Authenticode certificate (EV or standard) to avoid SmartScreen friction —
  the Windows analogue of Apple notarization.

Running the **engine** as a Windows *service* (vs. the CLI) is a larger piece (no systemd;
needs an SCM wrapper) and is out of scope for the first Windows pass.

---

## From source

For development, air-gapped builds, or before the first release. Needs Go 1.26+,
[Task](https://taskfile.dev) and pnpm (the web UI is built into the binary):

```sh
task build            # → ./bin/olivares, web console embedded
./bin/olivares version
./bin/olivares quickstart   # secure by default; prints the console URL + one-time setup token
# or, just to look around, an ephemeral synthetic estate (loopback, plaintext):
./bin/olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full development setup.

---

## Verifying a release

Run from the directory holding the downloaded artifacts:

```sh
scripts/verify-release.sh                  # keyless / Sigstore (default)
scripts/verify-release.sh --key cosign.pub # key-based (air-gap)
scripts/verify-release.sh --offline --key cosign.pub
```

It checks, in order: the cosign signature over `checksums.txt`, the SHA-256 of each
artifact, the SBOM and OpenVEX attestations, and the SLSA build provenance (each step
skipped with a clear note if its files or tools are absent). Details:
[`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md).

## Upgrading & uninstalling

Schema migrations apply **automatically and idempotently on boot** (the online
expand-contract model), so an upgrade is just a new binary/image over the same data
directory. The full procedure — verifying the new image, the per-path steps, **rolling
back safely** (roll back the binary, not the schema), inspecting migration state with
`olivares migrate status`, and which config changes need a restart — is the
[Upgrade & rollback runbook](docs/UPGRADE-AND-ROLLBACK.md). The short version:

- **Packages:** `dpkg -i` / `rpm -U` / `apk add` the new version, then `systemctl restart
  olivares`; your `/etc/olivares/olivares.env` is preserved. `apt remove` / `rpm -e` /
  `apk del` applies the same validated preserve policy and **keeps** `/var/lib/olivares`,
  its configuration, audit ledger and keys. On systemd hosts the hook also stops/disables
  the service; on Alpine it validates the record and lets apk remove its package-owned
  files because there is no systemd daemon. Run the manifest-checked purge before removing
  the package if erasure is intended; never replace it with an unbounded `rm -rf`.
- **install.sh / tarball:** re-run the installer, or replace the binary in place.
- **Docker:** pull and verify the new `docker.io/olivaresai/olivares` digest (the
  `ghcr.io/olivaresai/olivares` fallback is identical by digest) and recreate the
  container; the data volume persists.

Always **back up before upgrading** ([`docs/DR-RUNBOOK.md`](docs/DR-RUNBOOK.md)) and
re-verify the new artifact before you switch to it.

For a local removal, inspect the exact ownership record first. The command validates the
whole record against the `install_layout` in the distribution index before its first
mutation; an unexpected path returns 2. A custom data directory (`layout: custom`) is
admitted by shape, not by list, and only when the installed unit's `ExecStart=` executes
the engine with the same directory, or when a preserve already removed that unit and left
its uninstall witness beside the service configuration; preserve then purge therefore
works in that order, and a purge interrupted inside the data tree can be retried. An
explicitly selected external workspace is listed as kept and never removed. Preserve
keeps configuration, data, logs and keys. Purge requires a TTY confirmation or `--yes`
and removes only indexed paths:

```sh
olivares uninstall --plan --data-dir /var/lib/olivares
olivares uninstall --preserve --data-dir /var/lib/olivares
olivares uninstall --purge --data-dir /var/lib/olivares --yes
# Same contract through the verified installer:
sh olivares-install-YY.M.PATCH.sh --uninstall --plan --data-dir /var/lib/olivares
```

To move an estate, create a DR bundle before purge, install the destination, then use
`olivares dr restore`. The bundle authenticates its manifest and every payload with
`hmac-sha256-kek-v1`; restore refuses an export from a newer engine before writing. A
separately authenticated pre-v26.9 bundle needs the explicit migration exception
`--allow-legacy-unsigned`. See the DR runbook for custody and continuity verification.

**Community → enterprise (in place).** With a valid license installed,
`olivares upgrade --enterprise --token <TOKEN>` downloads the signed enterprise binary,
verifies its signature **offline** (a tamper aborts, the running binary untouched) and swaps
it in atomically with a kept backup. Then restart and turn on the add-ons with
`olivares enterprise enable <preset>` (`starter` / `regulated` / `full`) — a governed,
audited activation that shows a diff first and stages any add-on needing a secret or a review.
See the [Upgrade & rollback runbook §7](docs/UPGRADE-AND-ROLLBACK.md#7-editions-and-the-in-place-upgrade-community--enterprise).
