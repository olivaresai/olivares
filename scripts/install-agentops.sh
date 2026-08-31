#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# REUSE-IgnoreStart
# Co-deployment installer for "Operate Claude Code" (FASE V): provisions
# Olivares + the official Claude Code CLI sharing a workspace, on an already-installed
# Linux box, SECURE BY DEFAULT. It reproduces srv17-style environment in one
# command. The control plane CONDUCTS governed `claude` sessions as child processes via
# the native procRunner — no Docker socket, no privilege escalation.
#
#   curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh | sh
#
# It detects the topology, VERIFIES every artifact before running it (cosign — the
# engine binary/image is signed; claude is installed from Anthropic's GPG-SIGNED
# source with the key fingerprint pinned), provisions the workspace + writable surfaces,
# wires the deny-closed inference credential, and leaves the first session one command
# away. It NEVER runs an unverified binary unless you explicitly opt out, and it does
# NOT auto-start a governance plane — running one is your explicit decision (set
# OLIVARES_START=1 to bring it up).
#
# TOPOLOGIES (the prompt's four; this installer wires the two clean co-located ones,
# the secure default — see the how-to for the mixed Docker/native cases):
#   docker  — both in one hardened container (engine + claude), workspace in a volume.
#   native  — both on the host (systemd), workspace in /var/lib/olivares/workspaces.
#
# Knobs (environment variables):
#   OLIVARES_TOPOLOGY         auto (default) | docker | native
#   OLIVARES_VERSION          engine release tag for the native binary (default: latest)
#   OLIVARES_AGENTOPS_IMAGE   docker: the combined image to run (default: build from Dockerfile.agentops)
#   OLIVARES_IMAGE            docker: the engine base image to verify + build FROM
#                             (default: docker.io/olivaresai/olivares:latest — the official registry;
#                             ghcr.io/olivaresai/olivares is the fallback, same digest, no anonymous
#                             pull rate limit)
#   OLIVARES_CLAUDE_INSTALL   native claude source: repo (default, signed apt/dnf/apk) | installer | byo
#   OLIVARES_CLAUDE_CHANNEL   signed-repo channel: stable (default) | latest
#   OLIVARES_CLAUDE_VERSION   exact claude version to pin (docker build-arg + installer pin; default: channel head)
#   OLIVARES_DATA_DIR         native data dir (default: /var/lib/olivares)
#   OLIVARES_WORKSPACE_DIR    native workspace root (default: $OLIVARES_DATA_DIR/workspaces)
#   OLIVARES_START            1 to start the plane after wiring (default: 0 — explicit decision)
#   OLIVARES_CERT_IDENTITY    cosign certificate-identity regexp for the engine image (security-relevant override)
#   OLIVARES_CERT_OIDC_ISSUER cosign OIDC issuer for the engine image (security-relevant override)
#   OLIVARES_SKIP_COSIGN      1 to PROCEED UNVERIFIED when cosign is absent — NO integrity check is performed;
#                             rely on a digest-pinned OLIVARES_IMAGE/OLIVARES_AGENTOPS_IMAGE (NOT advised)
set -eu

# The keyless Sigstore identity the release workflow signs as. FULLY ANCHORED: cosign
# matches --certificate-identity-regexp UNANCHORED, so the previous
# '^https://github.com/olivaresai/olivares' also accepted `.../olivares-anything/...`
# and any workflow file on any branch -- i.e. far more identities than the one that
# actually signs a release.
DEFAULT_CERT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'

REPO="olivaresai/olivares"
RAW="https://raw.githubusercontent.com/${REPO}/main"
# Anthropic's published Claude Code signing-key fingerprint (verified 2026-06-16,
# https://code.claude.com/docs/en/setup). We PIN against it — never trust-on-first-use.
CLAUDE_KEY_FPR="31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"
CLAUDE_APK_KEY_SHA256="395759c1f7449ef4cdef305a42e820f3c766d6090d142634ebdb049f113168b6"
CLAUDE_KEY_URL="https://downloads.claude.ai/keys/claude-code.asc"
CLAUDE_APK_KEY_URL="https://downloads.claude.ai/keys/claude-code.rsa.pub"

say()  { printf '%s\n' "$*"; }
note() { printf '==> %s\n' "$*"; }
warn() { printf '!!  %s\n' "$*" >&2; }
err()  { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# sudo wrapper: use sudo only when not already root (and only if present).
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if have sudo; then SUDO="sudo"; else SUDO=""; fi
fi
run_priv() { # run_priv <cmd...> — run privileged, or plainly if already root
  if [ -n "$SUDO" ]; then $SUDO "$@"; else "$@"; fi
}

dl() { if have curl; then curl -fsSL "$1" -o "$2"; elif have wget; then wget -qO "$2" "$1"; else err "need curl or wget"; fi; }

TOPOLOGY="${OLIVARES_TOPOLOGY:-auto}"
DATA_DIR="${OLIVARES_DATA_DIR:-/var/lib/olivares}"
WORKSPACE_DIR="${OLIVARES_WORKSPACE_DIR:-$DATA_DIR/workspaces}"
START="${OLIVARES_START:-0}"

# --- topology detection -----------------------------------------------------------
detect_topology() {
  if [ "$TOPOLOGY" != "auto" ]; then echo "$TOPOLOGY"; return; fi
  if have docker && docker compose version >/dev/null 2>&1; then echo docker; return; fi
  if have systemctl; then echo native; return; fi
  err "could not auto-detect a topology (no working 'docker compose' and no systemd). Set OLIVARES_TOPOLOGY=docker|native."
}

# --- claude provisioning (native) — official signed source, fingerprint-pinned ----
verify_gpg_fpr() { # verify_gpg_fpr <keyfile> — assert it matches CLAUDE_KEY_FPR
  have gpg || err "gpg is required to verify the Claude Code signing key (install gnupg) or set OLIVARES_CLAUDE_INSTALL=byo"
  got="$(gpg --show-keys --with-colons "$1" 2>/dev/null | awk -F: '/^fpr:/ {print $10; exit}')"
  [ "$got" = "$CLAUDE_KEY_FPR" ] || err "Claude Code signing-key fingerprint MISMATCH: got '$got' want '$CLAUDE_KEY_FPR' (refusing — supply-chain integrity)"
  say "    claude signing key OK ($CLAUDE_KEY_FPR)"
}

install_claude_repo() {
  ch="${OLIVARES_CLAUDE_CHANNEL:-stable}"
  if have apt-get; then
    note "configuring the signed Claude Code apt repository (channel: $ch)"
    run_priv install -d -m 0755 /etc/apt/keyrings
    tmpkey="$(mktemp)"; dl "$CLAUDE_KEY_URL" "$tmpkey"; verify_gpg_fpr "$tmpkey"
    run_priv install -m 0644 "$tmpkey" /etc/apt/keyrings/claude-code.asc; rm -f "$tmpkey"
    echo "deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/${ch} ${ch} main" \
      | run_priv tee /etc/apt/sources.list.d/claude-code.list >/dev/null
    run_priv apt-get update
    run_priv apt-get install -y --no-install-recommends claude-code
  elif have dnf; then
    note "configuring the signed Claude Code dnf repository (channel: $ch)"
    tmpkey="$(mktemp)"; dl "$CLAUDE_KEY_URL" "$tmpkey"; verify_gpg_fpr "$tmpkey"; rm -f "$tmpkey"
    printf '[claude-code]\nname=Claude Code\nbaseurl=https://downloads.claude.ai/claude-code/rpm/%s\nenabled=1\ngpgcheck=1\ngpgkey=%s\n' \
      "$ch" "$CLAUDE_KEY_URL" | run_priv tee /etc/yum.repos.d/claude-code.repo >/dev/null
    run_priv dnf install -y claude-code
  elif have apk; then
    note "configuring the signed Claude Code apk repository (channel: $ch)"
    tmpkey="$(mktemp)"; dl "$CLAUDE_APK_KEY_URL" "$tmpkey"
    got="$( (sha256sum "$tmpkey" 2>/dev/null || shasum -a 256 "$tmpkey") | awk '{print $1}')"
    [ "$got" = "$CLAUDE_APK_KEY_SHA256" ] || err "Claude Code apk key SHA-256 MISMATCH (got $got)"
    run_priv install -m 0644 "$tmpkey" /etc/apk/keys/claude-code.rsa.pub; rm -f "$tmpkey"
    grep -q "downloads.claude.ai/claude-code/apk/${ch}" /etc/apk/repositories 2>/dev/null \
      || echo "https://downloads.claude.ai/claude-code/apk/${ch}" | run_priv tee -a /etc/apk/repositories >/dev/null
    run_priv apk add claude-code
  else
    err "no supported package manager (apt/dnf/apk) for OLIVARES_CLAUDE_INSTALL=repo; use 'installer' or 'byo'"
  fi
}

install_claude_installer() {
  pin="${OLIVARES_CLAUDE_VERSION:-}"
  note "installing claude via the official native installer (${pin:-latest channel})"
  # Honest posture: the installer SCRIPT is fetched over TLS only (no GPG of the script
  # itself); it then verifies the claude BINARY against Anthropic's signed release
  # manifest. The signed apt/dnf/apk repo (OLIVARES_CLAUDE_INSTALL=repo, the default) is
  # GPG-verified end to end and is preferred — this path is the network-light fallback.
  warn "claude via the official installer: script fetched over TLS, binary verified by Anthropic's signed manifest. Prefer OLIVARES_CLAUDE_INSTALL=repo (GPG-signed) where a package manager exists."
  # Run as the invoking (non-root) user so it lands in ~/.local/bin per the official docs.
  dl "https://claude.ai/install.sh" /tmp/claude-install.sh
  if [ -n "$pin" ]; then sh /tmp/claude-install.sh "$pin"; else sh /tmp/claude-install.sh; fi
  rm -f /tmp/claude-install.sh
}

provision_claude_native() {
  method="${OLIVARES_CLAUDE_INSTALL:-repo}"
  if have claude; then
    note "claude already present ($(command -v claude)) — skipping install (idempotent; upgrade via your package manager / claude update)"
    return
  fi
  case "$method" in
    repo)      install_claude_repo ;;
    installer) install_claude_installer ;;
    byo)       note "OLIVARES_CLAUDE_INSTALL=byo — not installing claude; provide it yourself and set OLIVARES_SESSION_RUNTIME_CLAUDE_BIN" ;;
    *)         err "invalid OLIVARES_CLAUDE_INSTALL=$method (want repo|installer|byo)" ;;
  esac
}

# --- native co-deployment ---------------------------------------------------------
install_native() {
  note "topology: NATIVE (engine + claude on the host, systemd)"

  # 1) the engine binary — delegate to the verifying installer (cosign gate inside).
  if have olivares; then
    note "olivares already installed ($(command -v olivares)) — skipping (run scripts/install.sh to upgrade)"
  else
    note "installing the verified Olivares engine binary (scripts/install.sh — cosign-gated)"
    if [ -f "$(dirname "$0")/install.sh" ]; then sh "$(dirname "$0")/install.sh"
    else dl "$RAW/scripts/install.sh" /tmp/olivares-install.sh && sh /tmp/olivares-install.sh && rm -f /tmp/olivares-install.sh; fi
  fi

  # 2) claude from the official signed source (or BYO).
  provision_claude_native

  # 3) service user + writable surfaces (idempotent).
  if ! id olivares >/dev/null 2>&1; then
    note "creating the no-login 'olivares' service user"
    run_priv useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin olivares 2>/dev/null \
      || run_priv adduser --system --home "$DATA_DIR" --shell /usr/sbin/nologin olivares 2>/dev/null || true
  fi
  note "provisioning $DATA_DIR, $WORKSPACE_DIR and the claude HOME (owned by olivares)"
  run_priv install -d -o olivares -g olivares -m 0750 "$DATA_DIR" "$WORKSPACE_DIR" "$DATA_DIR/claude-home"
  # The token dir holds the short-lived inference bearer (file 0600) — 0700 parent,
  # matching the image's /run/olivares posture.
  run_priv install -d -o olivares -g olivares -m 0700 "$DATA_DIR/run"

  # 4) systemd drop-in + env (shipped under packaging/systemd; installed idempotently).
  note "installing the agentops systemd drop-in + env example"
  run_priv install -d -m 0755 /etc/systemd/system/olivares.service.d /etc/olivares
  src_dir="$(dirname "$0")/.."
  if [ -f "$src_dir/packaging/systemd/olivares.service.d/agentops.conf" ]; then
    run_priv install -m 0644 "$src_dir/packaging/systemd/olivares.service.d/agentops.conf" /etc/systemd/system/olivares.service.d/agentops.conf
  else
    dl "$RAW/packaging/systemd/olivares.service.d/agentops.conf" /tmp/agentops.conf
    run_priv install -m 0644 /tmp/agentops.conf /etc/systemd/system/olivares.service.d/agentops.conf; rm -f /tmp/agentops.conf
  fi
  if [ ! -f /etc/olivares/agentops.env ]; then
    if [ -f "$src_dir/packaging/olivares-agentops.env.example" ]; then
      run_priv install -m 0640 "$src_dir/packaging/olivares-agentops.env.example" /etc/olivares/agentops.env
    else
      dl "$RAW/packaging/olivares-agentops.env.example" /tmp/agentops.env
      run_priv install -m 0640 /tmp/agentops.env /etc/olivares/agentops.env; rm -f /tmp/agentops.env
    fi
    note "wrote /etc/olivares/agentops.env (edit it: point the inference token at your refresher)"
  fi

  # 5) PEP: the STATIC managed-settings.json that makes every governed session's
  # tool-calls pass the in-line PEP. OPT-IN (OLIVARES_PEP_MANAGED_SETTINGS=1) because it
  # installs a non-overridable PreToolUse hook for ALL claude on this host: deny-closed
  # until the per-session OLIVARES_SESSION_PEP_URL env is also set, so an interactive
  # claude with no PEP env would be blocked from running tools. The per-session endpoint
  # is wired in agentops.env; this only places the static, non-secret hook file.
  if [ "${OLIVARES_PEP_MANAGED_SETTINGS:-0}" = "1" ] && have olivares; then
    note "installing the managed PreToolUse PEP hook at /etc/claude-code/managed-settings.json (OLIVARES_PEP_MANAGED_SETTINGS=1)"
    run_priv install -d -m 0755 /etc/claude-code
    if [ -f /etc/claude-code/managed-settings.json ]; then
      warn "/etc/claude-code/managed-settings.json exists; leaving it untouched (merge the PreToolUse hook by hand)"
    else
      olivares agent managed-settings --pep-command 'olivares claude-hook' > /tmp/managed-settings.json \
        && run_priv install -m 0644 /tmp/managed-settings.json /etc/claude-code/managed-settings.json
      rm -f /tmp/managed-settings.json
      note "set OLIVARES_SESSION_PEP_URL in /etc/olivares/agentops.env to activate it (else tool-calls are deny-closed)"
    fi
  fi
  run_priv systemctl daemon-reload

  say ""
  say "✅ native co-deployment wired."
  say "   workspace: $WORKSPACE_DIR    claude: $(command -v claude 2>/dev/null || echo '<BYO — set OLIVARES_SESSION_RUNTIME_CLAUDE_BIN>')"
  if [ "$START" = "1" ]; then
    note "starting olivares (OLIVARES_START=1)"; run_priv systemctl enable --now olivares
  else
    say ""
    say "Next (running a governance plane is your explicit decision):"
    say "  1) edit /etc/olivares/agentops.env — wire the short-lived inference token (refresher)"
    say "  2) sudo systemctl enable --now olivares     # loopback-only by default"
    say "  3) register a workspace + launch the first governed session (see the how-to)"
  fi
}

# --- docker co-deployment ---------------------------------------------------------
# verify_engine_image <ref> — resolves the engine image to a DIGEST, verifies that
# digest's signature, and echoes the digest-pinned ref (repo@sha256:…) on stdout. The
# caller builds FROM that digest, so the bytes we verified are the exact bytes used —
# closing the verify-tag-then-build-tag TOCTOU. All diagnostics go to stderr.
verify_engine_image() {
  img="$1"
  case "$img" in
    *@sha256:*) pinned="$img" ;;   # already digest-pinned by the operator
    *)
      note "pulling $img to pin its digest (so the build uses the exact verified bytes)" >&2
      docker pull "$img" >&2 || err "docker pull $img failed"
      pinned="$(docker image inspect --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}' "$img" 2>/dev/null)"
      [ -n "$pinned" ] || err "could not resolve a digest for $img (no RepoDigests) — pin OLIVARES_IMAGE by digest"
      ;;
  esac
  if have cosign; then
    note "cosign-verifying the engine image $pinned" >&2
    cosign verify "$pinned" \
      --certificate-identity-regexp "${OLIVARES_CERT_IDENTITY:-$DEFAULT_CERT_IDENTITY}" \
      --certificate-oidc-issuer "${OLIVARES_CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}" \
      >/dev/null 2>&1 || err "engine image signature verification FAILED for $pinned"
    note "    image signature OK" >&2
  elif [ "${OLIVARES_SKIP_COSIGN:-0}" = "1" ]; then
    warn "cosign absent — proceeding UNVERIFIED (OLIVARES_SKIP_COSIGN=1); building FROM the pinned digest $pinned with NO signature check."
  else
    err "cosign not found, so $pinned cannot be verified. Install cosign and re-run, or (NOT advised) set OLIVARES_SKIP_COSIGN=1."
  fi
  echo "$pinned"
}

install_docker() {
  note "topology: DOCKER (engine + claude in one hardened container)"
  have docker || err "docker not found"
  docker compose version >/dev/null 2>&1 || err "'docker compose' (v2) is required"
  root="$(cd "$(dirname "$0")/.." && pwd)"
  base="$root/deploy/compose/docker-compose.yml"
  over="$root/deploy/compose/docker-compose.agentops.yml"
  [ -f "$base" ] && [ -f "$over" ] || err "compose files not found (run from a checkout, or fetch deploy/compose/*)"

  if [ -n "${OLIVARES_AGENTOPS_IMAGE:-}" ]; then
    note "using the provided combined image: $OLIVARES_AGENTOPS_IMAGE (verify it yourself if signed)"
  else
    # Pin the engine to the exact digest we verify, then build FROM that digest.
    engine_img="$(verify_engine_image "${OLIVARES_IMAGE:-docker.io/olivaresai/olivares:latest}")"
    note "building the combined image from Dockerfile.agentops (engine=$engine_img; claude from the signed apt repo)"
    docker build -f "$root/Dockerfile.agentops" \
      --build-arg "OLIVARES_IMAGE=$engine_img" \
      --build-arg "CLAUDE_CHANNEL=${OLIVARES_CLAUDE_CHANNEL:-stable}" \
      ${OLIVARES_CLAUDE_VERSION:+--build-arg "CLAUDE_VERSION=$OLIVARES_CLAUDE_VERSION"} \
      -t "olivares-agentops:local" "$root"
    OLIVARES_AGENTOPS_IMAGE="olivares-agentops:local"; export OLIVARES_AGENTOPS_IMAGE
  fi

  say ""
  say "✅ docker co-deployment ready (image: $OLIVARES_AGENTOPS_IMAGE)."
  compose_cmd="docker compose -f \"$base\" -f \"$over\""
  if [ "$START" = "1" ]; then
    note "bringing the stack up (OLIVARES_START=1)"
    OLIVARES_AGENTOPS_IMAGE="$OLIVARES_AGENTOPS_IMAGE" docker compose -f "$base" -f "$over" up -d
    say "   first-boot setup token:  docker compose -f $base -f $over logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'"
  else
    say ""
    say "Next (running a governance plane is your explicit decision):"
    say "  1) provide a short-lived inference token in the 'olivares-runtime' volume (/run/olivares/session-token)"
    say "  2) OLIVARES_AGENTOPS_IMAGE=$OLIVARES_AGENTOPS_IMAGE $compose_cmd up -d"
    say "  3) register a workspace + launch the first governed session (see the how-to)"
  fi
}

# --- main -------------------------------------------------------------------------
TOP="$(detect_topology)"
case "$TOP" in
  docker) install_docker ;;
  native) install_native ;;
  *) err "unknown topology '$TOP'" ;;
esac
# REUSE-IgnoreEnd
