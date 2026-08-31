# Installing Olivares AI

The engine is a **single static Go binary** (`olivares`) with the web console embedded. It
runs self-hosted on Linux or macOS, as a container, or on Kubernetes. This page is the
per-OS install matrix and production setup; the [README](README.md#install) has the
short version, and the deployment tutorials (Compose, Kubernetes/Helm, air-gapped) live in
[`docs-site/`](docs-site/).

> **Beta.** Releases are cut from this repository. The binaries,
> images and packages below **publish with the first tagged release (`v26.8.0`)**; until
> that tag lands, [build from source](#from-source). Everything is self-hosted: the engine
> makes no mandatory outbound calls at boot and verifying a licence never calls us. The one
> command that reaches us is `olivares upgrade`, which fetches from the update channel unless
> `--endpoint` or `--bundle` points it elsewhere; commercial add-ons, updates and patches are
> downloaded with your subscription as the credential ([`LICENSING.md`](LICENSING.md#what-the-subscription-does-and-does-not-call-home-for)).

**Verify before you run.** For a security product the supply chain is part of the trust
model — every artifact is cosign-signed with an SBOM, an OpenVEX document and SLSA build
provenance. The one-line installer and the Homebrew cask verify automatically; for manual
downloads use [`scripts/verify-release.sh`](scripts/verify-release.sh) (see
[Verifying a release](#verifying-a-release)).

## Versioning

Releases use **CalVer**: `vYY.M.PATCH` — two-digit year, month, and the release number
within that month. The first public release is `v26.8.0` (August 2026); a same-month fix is
`v26.8.1`. Container tags follow: `:26.8.0`, `:latest`, plus the `-fips` / `-stig` variants.
The maturity label (**beta**) is separate from the version; a release that should be flagged
*pre-release* on GitHub is tagged with a suffix, e.g. `v26.8.0-beta.1`.

---

## Linux

### One command (recommended)

Detects your OS/arch, downloads the release archive, **verifies the cosign signature and
SHA-256**, and installs `olivares` to `/usr/local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh
```

Knobs: `OLIVARES_VERSION=v26.8.0` (pin a tag), `OLIVARES_BINDIR=~/.local/bin` (install dir).
The script needs `cosign` for the signature check; without it, it refuses unless you set
`OLIVARES_SKIP_COSIGN=1` (SHA-256 only — not advised).

### Native packages (`.deb` / `.rpm` / `.apk`)

Each package installs the binary to `/usr/bin/olivares`, ships a **hardened systemd unit**
and an example env file, and creates a no-login `olivares` service user plus the data dir
`/var/lib/olivares`. It does **not** auto-start the service — starting it is your
explicit decision.

```sh
# Debian / Ubuntu
sudo dpkg -i olivares_*_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -i olivares_*_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_*_linux_amd64.apk

# then start it (loopback-only by default; see the env file to widen it)
sudo systemctl enable --now olivares
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'      # the one-time first-boot setup token
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
ver=v26.8.0; os=linux; arch=amd64
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
switch the host to `ghcr.io`, if a CI node or a large fleet hits the ceiling. Tags: `:26.8.0`
(pin a release), `:latest`, `:26.8.0-fips` (FIPS 140-3 mode, CMVP #5247) and `:26.8.0-stig`
(STIG-profiled UBI base) — see [SCP-09](docs/SCP-09-FIPS-STIG.md). The base and `:latest` tags
are multi-arch (amd64/arm64); `-fips`/`-stig` are amd64-only. **For production, pin by digest**
(`docker.io/olivaresai/olivares@sha256:…`); the mutable tags above are for evaluation only. Verify
the image: `cosign verify docker.io/olivaresai/olivares:26.8.0 --certificate-identity-regexp
'^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
--certificate-oidc-issuer
https://token.actions.githubusercontent.com` (the same verification works identically against the
`ghcr.io/olivaresai/olivares:26.8.0` fallback — same digest, signatures and attestations).

### Docker Compose

A hardened, ready-to-edit stack (SQLite single-node, optional Postgres + backup):

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
```

See [`deploy/compose/`](deploy/compose/) and the
[Docker Compose tutorial](docs-site/src/content/docs/tutorials/getting-started/docker-compose.mdx).

### Kubernetes

Signed Helm chart, or a flat Helm-free manifest for a `kubectl`-only / air-gapped host:

```sh
# Helm (OCI; the chart is cosign-signed)
helm install olivares oci://ghcr.io/olivaresai/charts/olivares -n olivares-system --create-namespace

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
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares:26.8.0 -t olivares-agentops:26.8.0 .
OLIVARES_AGENTOPS_IMAGE=olivares-agentops:26.8.0 \
  docker compose -f deploy/compose/docker-compose.yml \
                 -f deploy/compose/docker-compose.agentops.yml up -d
```

**Both native** — one command (verifies the engine signature, installs `claude` from the
signed repo, wires the hardened systemd drop-in; does not auto-start):

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
```

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
ver=v26.8.0; arch=arm64   # or amd64 on Intel
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
  `apk del` stops the service but **keeps** `/var/lib/olivares` (audit ledger + keys) —
  remove it by hand if intended.
- **install.sh / tarball:** re-run the installer, or replace the binary in place.
- **Docker:** pull and verify the new `docker.io/olivaresai/olivares` digest (the
  `ghcr.io/olivaresai/olivares` fallback is identical by digest) and recreate the
  container; the data volume persists.

Always **back up before upgrading** ([`docs/DR-RUNBOOK.md`](docs/DR-RUNBOOK.md)) and
re-verify the new artifact before you switch to it.

**Community → enterprise (in place).** With a valid license installed,
`olivares upgrade --enterprise --token <TOKEN>` downloads the signed enterprise binary,
verifies its signature **offline** (a tamper aborts, the running binary untouched) and swaps
it in atomically with a kept backup. Then restart and turn on the add-ons with
`olivares enterprise enable <preset>` (`starter` / `regulated` / `full`) — a governed,
audited activation that shows a diff first and stages any add-on needing a secret or a review.
See the [Upgrade & rollback runbook §7](docs/UPGRADE-AND-ROLLBACK.md#7-editions-and-the-in-place-upgrade-community--enterprise).
