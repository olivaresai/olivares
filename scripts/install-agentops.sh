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
#   OLIVARES_DATA_DIR         native data dir (default: /var/lib/olivares, or the directory an
#                             already installed olivares.service names). A custom directory is
#                             rendered into the unit (by the signed adapter), the agentops drop-in
#                             (HOME, token/run dirs, sandbox paths), the runtime env and the
#                             ownership manifest. It must be a dedicated, canonical path at least
#                             two levels deep; this installer never relocates an installed service.
#   OLIVARES_WORKSPACE_DIR    native workspace root (default: $OLIVARES_DATA_DIR/workspaces, or the
#                             one recorded by a previous run). An external directory is granted
#                             ReadWritePaths in the drop-in, created for the service only if absent,
#                             and otherwise left exactly as found; uninstall never removes it.
#   OLIVARES_RUNTIME_TOKEN_FILE  native: what /etc/olivares/agentops.env should load as
#                             OLIVARES_SESSION_RUNTIME_TOKEN_FILE. Unset (the default) the installer
#                             decides by OWNERSHIP: it generates that file once for the selected data
#                             directory, re-points only a value it can prove it generated for another
#                             estate, preserves everything else, and refuses rather than announce a
#                             wired co-deployment over a credential path that belongs to an estate
#                             which is not this one. "estate" selects <data-dir>/run/session-token;
#                             "keep" preserves the configured path exactly (a deliberate external or
#                             refresher path); an absolute path sets the file your refresher writes.
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
# The base unit is installed by the signed release's service adapter and names the data
# directory it serves; the files below are ours and are rendered from that same layout.
# The AgentOps drop-in and the ownership manifest carry a managed marker so a reinstall
# regenerates exactly what this installer wrote and never an operator's own content.
NATIVE_SERVICE_UNIT=/etc/systemd/system/olivares.service
NATIVE_DROPIN=/etc/systemd/system/olivares.service.d/agentops.conf
NATIVE_RUNTIME_ENV=/etc/olivares/agentops.env
DROPIN_MARKER='# Managed by install-agentops.sh (olivares.ai/agentops-dropin/v1): regenerated on reinstall.'
RUNTIME_ENV_MARKER='# Generated by install-agentops.sh from packaging/olivares-agentops.env.example; preserved on reinstall.'
# The drop-in shipped before layouts were configurable had no marker and hard-coded the
# default paths. It is recognised so the operator hears why it is not regenerated.
#
# ⛔ THE BYTES BELOW ARE NOT PROVENANCE PROSE — THEY ARE DATA ABOUT FILES ALREADY ON DISK, and
# they are matched WHOLE-LINE. They are the verbatim first content line of the drop-in this
# project installed before the managed marker existed: line 4 of
# packaging/systemd/olivares.service.d/agentops.conf at 51b9acb52b^, byte for byte.
# Rewording them would compile, pass review and silence the export's leak gate, and every
# estate installed before that change would stop being recognised as ours;
# matching a PREFIX instead would start accepting an operator's file that merely quotes the
# prose. Both directions have a red case in scripts/test-agentops-install.sh (legacy-dropin,
# near-miss-dropin, operator-dropin). The export exception that keeps this line exact in the
# public tree is recorded, with its bounded scope and the adjudication it is pending, beside
# the entry itself in the hub-only export allow list.
LEGACY_DROPIN_LINE='# "Operate Claude Code" drop-in for the hardened olivares.service (FASE V, S183).'
# The ONE assignment in that runtime env this installer generates: the example's own token
# path, re-pointed at the selected data dir. Its exact rendered value is what later tells an
# untouched generated default from operator content — the file as a whole is never
# regenerated, because the contract printed with it asks the operator to edit it.
RUNTIME_ENV_TOKEN_KEY=OLIVARES_SESSION_RUNTIME_TOKEN_FILE
RUNTIME_ENV_TOKEN_SUFFIX=/run/session-token

# check_native_path <what> <path> — the shape every selected path must have before it is
# rendered into a unit or provisioned under privilege: absolute, canonical (no empty, "."
# or ".." components, no trailing slash) and free of characters that sed, systemd or a
# shell would reinterpret. A plain space is allowed and quoted by quote_unit. Nothing is
# rewritten on the operator's behalf: a refusal names the rule and the path stays theirs.
native_path_valid() {
  native_path_problem=""
  case "$1" in
    /) native_path_problem="may not be the filesystem root"; return 1 ;;
    /*) ;;
    *) native_path_problem="must be an absolute path"; return 1 ;;
  esac
  case "$1" in
    */|*//*|*/./*|*/../*|*/.|*/..)
      native_path_problem="must be canonical (no trailing slash, empty, . or .. components)"; return 1 ;;
  esac
  case "$1" in
    *'|'*|*'&'*|*'<'*|*'>'*|*'@'*|*\\*|*'"'*|*"'"*|*'$'*|*'%'*|*';'*|*'#'*|*[![:print:]]*)
      native_path_problem="contains a character unsafe for a service definition"; return 1 ;;
  esac
  return 0
}
check_native_path() {
  native_path_valid "$2" || err "$1 $native_path_problem: $2"
}
# quote_unit <path> — a systemd value: bare without whitespace, double-quoted otherwise.
quote_unit() { case "$1" in *' '*) printf '"%s"' "$1" ;; *) printf '%s' "$1" ;; esac; }

# The EnvironmentFile grammar is not shell syntax and the file must never be sourced/evaled.
# This byte-state recognizer follows systemd's parse_env_file_internal contract instead:
# leading/trailing whitespace, quote concatenation, unquoted escapes, quoted escapes,
# continuations, whole-line comments and last-assignment-wins. The rules are taken per byte
# from the official sources of the supported managers (v235 fileio.c; v241, v247, v254 and
# v257 env-file.c), and they moved three times:
#
#   · unquoted "\c" (VALUE_ESCAPE): every version keeps c and eats CR or LF;
#   · single quotes: v235 still had an escape state (c kept, CR or LF eaten); from 241 a
#     backslash inside single quotes is literal;
#   · double quotes ("\c", DOUBLE_QUOTE_VALUE_ESCAPE): v235 keeps c and eats CR or LF;
#     v241 unescapes only \", eats CR or LF and keeps "\c" otherwise; from 247 only the
#     shell set (" \ ` $) is unescaped, ONLY LF is eaten and everything else, a CR
#     included, keeps its backslash (c != '\n', not NEWLINE — the independent review of
#     2026-09-05 measured backslash+CR reaching the process on 257);
#   · "\<newline>" inside a comment: until 253 the comment continues; from 254 it ends.
#
# The installed manager version selects the rules. If it cannot be measured, encountering
# a form whose meaning differs between those versions makes the result ambiguous; the
# caller refuses before treating it as an external token path.
#
# Two properties of the FILE decide whether systemd loads it at all, and both are judged
# before any value is trusted: a NUL byte (checked by the caller, see runtime_env_first_nul)
# and a key or value that is not valid UTF-8 — every loader version runs
# check_utf8ness_and_warn on each pushed assignment and returns EINVAL, which fails the
# whole EnvironmentFile= rather than one line (retained v257 env-file.c and utf8.c;
# measured on 257 with a real 0xff byte). The UTF-8 rule ported here is
# utf8_encoded_valid_unichar: expected length from the lead byte, 10xxxxxx continuations,
# no overlong form, no surrogate, no U+FDD0..U+FDEF, no U+xxFFFE/U+xxFFFF, nothing past
# U+10FFFF. Comment bytes are never pushed, so they are never validated — as in systemd.
#
# The same program rewrites by BYTE OFFSET. Everything outside the one logical assignment is
# emitted from the original input verbatim, including CRLF and a missing terminal newline.
SYSTEMD_ENV_FILE_RECOGNIZER='
BEGIN { for (i = 1; i < 256; i++) ord[sprintf("%c", i)] = i }
function ws(c) { return c == " " || c == "\t" || c == "\r" || c == "\n" }
function nl(c) { return c == "\r" || c == "\n" }
function shell_escape(c) { return c == "\"" || c == "\\" || c == "`" || c == "$" }
# utf8_valid(s): systemd utf8_is_valid over bytes (LC_ALL=C), see the header above.
function utf8_valid(s,   i, n, b, len, cp, j, bb, elen) {
  n = length(s); i = 1
  while (i <= n) {
    b = ord[substr(s, i, 1)]
    if (b < 128) { i++; continue }
    if (b >= 192 && b < 224) { len = 2; cp = b - 192 }
    else if (b >= 224 && b < 240) { len = 3; cp = b - 224 }
    else if (b >= 240 && b < 248) { len = 4; cp = b - 240 }
    else if (b >= 248 && b < 252) { len = 5; cp = b - 248 }
    else if (b >= 252 && b < 254) { len = 6; cp = b - 252 }
    else return 0
    if (i + len - 1 > n) return 0
    for (j = 1; j < len; j++) {
      bb = ord[substr(s, i + j, 1)]
      if (bb < 128 || bb >= 192) return 0
      cp = cp * 64 + (bb - 128)
    }
    if (cp < 128) elen = 1
    else if (cp < 2048) elen = 2
    else if (cp < 65536) elen = 3
    else if (cp < 2097152) elen = 4
    else if (cp < 67108864) elen = 5
    else elen = 6
    if (elen != len) return 0
    if (cp >= 1114112) return 0
    if (cp >= 55296 && cp <= 57343) return 0
    if (cp >= 64976 && cp <= 65007) return 0
    if (cp % 65536 >= 65534) return 0
    i += len
  }
  return 1
}
function reset_assignment() {
  parsed_key = ""; parsed_value = ""; key_start = 0; trailing_value_ws = 0
}
function push_assignment(end_offset,   k) {
  k = parsed_key
  sub(/[ \t\r\n]+$/, "", k)
  if (state == "value" && trailing_value_ws > 0)
    parsed_value = substr(parsed_value, 1, trailing_value_ws - 1)
  # Every assignment is validated, as every push is in systemd: one bad byte anywhere
  # fails the whole file. Which assignment carried it decides whether a rewrite of the
  # wanted one can repair the file.
  if (!utf8_valid(k) || !utf8_valid(parsed_value)) {
    if (k == wanted_key) utf8_bad_wanted = 1
    else utf8_bad_other = 1
  }
  if (k == wanted_key) {
    found++
    found_start = key_start
    found_end = end_offset
    found_value = parsed_value
  }
  reset_assignment()
}
function consume(c, offset) {
  if (state == "pre_key") {
    if (c == "#" || c == ";") state = "comment"
    else if (!ws(c)) { state = "key"; parsed_key = c; key_start = offset }
  } else if (state == "key") {
    if (nl(c)) { state = "pre_key"; reset_assignment() }
    else if (c == "=") { state = "pre_value"; trailing_value_ws = 0 }
    else parsed_key = parsed_key c
  } else if (state == "pre_value") {
    if (nl(c)) { push_assignment(offset - 1); state = "pre_key" }
    else if (c == "\047") state = "single"
    else if (c == "\"") state = "double"
    else if (c == "\\") state = "value_escape"
    else if (!ws(c)) { state = "value"; parsed_value = parsed_value c; trailing_value_ws = 0 }
  } else if (state == "value") {
    if (nl(c)) { push_assignment(offset - 1); state = "pre_key" }
    else if (c == "\\") { state = "value_escape"; trailing_value_ws = 0 }
    else {
      if (ws(c)) { if (trailing_value_ws == 0) trailing_value_ws = length(parsed_value) + 1 }
      else trailing_value_ws = 0
      parsed_value = parsed_value c
    }
  } else if (state == "value_escape") {
    state = "value"
    if (!nl(c)) parsed_value = parsed_value c
  } else if (state == "single") {
    if (c == "\047") state = "pre_value"
    else if (c == "\\" && systemd_version > 0 && systemd_version < 241) state = "single_escape"
    else if (c == "\\" && systemd_version == 0) { uncertain = 1; parsed_value = parsed_value c }
    else parsed_value = parsed_value c
  } else if (state == "single_escape") {
    state = "single"
    if (!nl(c)) parsed_value = parsed_value c
  } else if (state == "double") {
    if (c == "\"") state = "pre_value"
    else if (c == "\\") state = "double_escape"
    else parsed_value = parsed_value c
  } else if (state == "double_escape") {
    state = "double"
    if (systemd_version == 0) {
      uncertain = 1
      if (!nl(c)) parsed_value = parsed_value "\\" c
    } else if (systemd_version < 241) {
      # v235: the byte itself; CR and LF are eaten.
      if (!nl(c)) parsed_value = parsed_value c
    } else if (systemd_version < 247) {
      # v241: only \" unescapes; CR and LF are eaten; any other byte keeps its backslash.
      if (c == "\"") parsed_value = parsed_value c
      else if (!nl(c)) parsed_value = parsed_value "\\" c
    } else {
      # v247 and later: the shell set unescapes; ONLY LF is eaten (c != \n); a CR keeps
      # its backslash and reaches the process as two bytes.
      if (shell_escape(c)) parsed_value = parsed_value c
      else if (c != "\n") parsed_value = parsed_value "\\" c
    }
  } else if (state == "comment") {
    if (c == "\\") state = "comment_escape"
    else if (nl(c)) state = "pre_key"
  } else if (state == "comment_escape") {
    if (systemd_version == 0) {
      if (nl(c)) uncertain = 1
      state = nl(c) ? "pre_key" : "comment"
    } else if (systemd_version >= 254 && nl(c)) state = "pre_key"
    else state = "comment"
  }
}
{
  records[NR] = $0
  body_bytes += length($0)
}
END {
  separators = file_bytes - body_bytes
  if (separators < 0 || separators > NR) uncertain = 1
  contents = ""
  for (r = 1; r <= NR; r++) {
    contents = contents records[r]
    if (r <= separators) contents = contents "\n"
  }
  if (length(contents) != file_bytes) uncertain = 1
  state = "pre_key"; reset_assignment()
  for (i = 1; i <= length(contents); i++) consume(substr(contents, i, 1), i)
  if (state == "pre_value" || state == "value" || state == "value_escape" ||
      state == "single" || state == "single_escape" || state == "double" ||
      state == "double_escape")
    push_assignment(length(contents))

  # A value with a line break cannot name one token file and a command substitution
  # cannot carry one faithfully. Mark rather than normalize any such value, naming which
  # byte it is; the caller will refuse it before the external-path branch.
  value_transport = "ok"
  if (found_value ~ /\n/) value_transport = "newline"
  else if (found_value ~ /\r/) value_transport = "carriage-return"
  printf "%s", found_value > value_file
  close(value_file)
  utf8_status = "ok"
  if (utf8_bad_other) utf8_status = "invalid-elsewhere"
  else if (utf8_bad_wanted) utf8_status = "invalid-token"

  if (rewrite_file != "") {
    if (uncertain || found > 1 || utf8_bad_other) exit 4
    if (found == 1)
      rendered = substr(contents, 1, found_start - 1) wanted_key "=" replacement substr(contents, found_end + 1)
    else {
      separator = ""
      if (length(contents) > 0 && substr(contents, length(contents), 1) != "\n" &&
          substr(contents, length(contents), 1) != "\r") separator = "\n"
      rendered = contents separator wanted_key "=" replacement "\n"
    }
    printf "%s", rendered > rewrite_file
    close(rewrite_file)
  }
  printf("%d\n%d\n%d\n%s\n%s\n%s\n", found + 0, found_start + 0, found_end + 0,
         uncertain ? "ambiguous" : "ok", value_transport, utf8_status)
}'

# runtime_env_first_nul <file> — prints the 1-based byte offset of the first NUL byte, or
# nothing when the file has none. The byte count is the test; awk never sees a NUL (POSIX
# leaves its handling unspecified, which is exactly why the byte is judged here and not
# inside the recognizer). systemd never parses such a file: on 257 the strict
# EnvironmentFile= fails the unit and the EnvironmentFile=- the drop-in uses skips the whole
# file (measured), and the v235 loader walks a C string that stops at the byte, so whatever
# follows it is not what systemd sees either.
runtime_env_first_nul() {
  runtime_env_bytes="$(wc -c <"$1" | tr -d ' ')"
  runtime_env_nul_free="$(tr -d '\000' <"$1" | wc -c | tr -d ' ')"
  [ "$runtime_env_bytes" != "$runtime_env_nul_free" ] || return 0
  # NUL and LF swap places, so the first line ends at the first NUL and every original
  # LF becomes an ordinary byte for awk.
  LC_ALL=C tr '\000\n' '\n\001' <"$1" | LC_ALL=C awk 'NR == 1 { print length($0) + 1; exit }'
}

# runtime_env_token_scan <file> <decoded-value-file> — prints count, byte start/end,
# parse status, transport status and UTF-8 status. The decoded value is written separately
# so quoting and backslashes never acquire a second shell interpretation. The file must
# already be known to be NUL-free (runtime_env_first_nul).
runtime_env_token_scan() {
  runtime_env_bytes="$(wc -c <"$1" | tr -d ' ')"
  LC_ALL=C awk -v wanted_key="$RUNTIME_ENV_TOKEN_KEY" \
    -v systemd_version="${systemd_env_version:-0}" -v file_bytes="$runtime_env_bytes" \
    -v value_file="$2" "$SYSTEMD_ENV_FILE_RECOGNIZER" "$1"
}

# runtime_env_token_rewrite <input> <output> <rendered-value> — invokes the SAME recognizer,
# so the location changed is the assignment whose effective value was judged.
runtime_env_token_rewrite() {
  runtime_env_bytes="$(wc -c <"$1" | tr -d ' ')"
  LC_ALL=C awk -v wanted_key="$RUNTIME_ENV_TOKEN_KEY" \
    -v systemd_version="${systemd_env_version:-0}" -v file_bytes="$runtime_env_bytes" \
    -v value_file="$native_tmp/rewrite.value" -v rewrite_file="$2" -v replacement="$3" \
    "$SYSTEMD_ENV_FILE_RECOGNIZER" "$1" >/dev/null
}
# estate_at <dir> <scratch> — succeeds when <dir> is an Olivares estate: its OWN ownership
# record names <dir> as the data directory it serves. Sets estate_generated_env=1 when that
# same record also carries $NATIVE_RUNTIME_ENV as a MANAGED runtime env, which is the estate
# stating that this installer generated that file for it.
#
# This is the whole ownership proof, and it is deliberately not a guess about shape: a
# refresher directory that happens to be called .../run/session-token has no such record, and
# an estate that has one cannot be confused with an operator's own path.
estate_at() {
  estate_generated_env=0
  estate_manifest="$1/install-manifest.json"
  if run_priv test -L "$estate_manifest"; then return 1; fi
  if ! run_priv test -f "$estate_manifest"; then return 1; fi
  run_priv cat "$estate_manifest" >"$2" 2>/dev/null || return 1
  estate_record="$(read_ownership_record "$2" 2>/dev/null)" || return 1
  [ "$(record_field "$estate_record" data_dir)" = "$1" ] || return 1
  if printf '%s\n' "$estate_record" | awk -F'\t' -v env="$NATIVE_RUNTIME_ENV" '
       $1 == "file" && $2 == "runtime-env" && $3 == env && $5 == "true" { found = 1 }
       END { exit(found ? 0 : 1) }'; then estate_generated_env=1; fi
  return 0
}

# unit_data_dir <unit> — the --data-dir the installed unit EXECUTES THE OLIVARES ENGINE
# with. Two things have to hold before a value counts, and the second is what makes it a
# witness rather than a mention:
#
#   1. it must sit in an ExecStart= inside [Service], the only section whose ExecStart=
#      systemd ever runs (line continuations joined, systemd quoting honoured, an empty
#      assignment resets the list); and
#   2. that directive must run the Olivares engine: argv[0] an absolute path whose name
#      is exactly "olivares", with no systemd prefix character (@ - : + !) on it, because
#      the adapters never render one.
#
# Comments (# ;), Environment=, ExecStartPre=, every other directive, ExecStart= in
# another section and ExecStart= running another program never count, whatever they
# mention. This is the same rule the engine's uninstaller applies
# (localinstall.UnitDataDir), narrowed where it has to be: the uninstaller compares
# argv[0] against the binary its own ownership manifest records, and this installer has
# no such record yet — it is READING the unit precisely to find out which estate is
# installed, and an operator-managed unit legitimately has no manifest at all (see the
# battery case that installs on top of one). So the program test here is the engine's
# NAME, not one recorded path.
#
# Prints the single value (rc 0), nothing when no witnessing directive names one (rc 0),
# rc 3 when the unit is ambiguous (several data directories, the space-separated
# --data-dir form, escapes, unbalanced quotes) and rc 4 when the unit has ExecStart=
# assignments but none of them is an effective Olivares invocation.
unit_data_dir() {
  awk '
    function flush_logical(line,   trimmed, key, value, n, i, tok, c, q, inword, bad, args, nargs, name) {
      trimmed = line; sub(/^[[:space:]]+/, "", trimmed)
      if (trimmed == "" || substr(trimmed, 1, 1) == "#" || substr(trimmed, 1, 1) == ";") return
      if (substr(trimmed, 1, 1) == "[") {
        # A section header is the whole line, [Name]; anything else is not a header this
        # parser understands, so what follows counts as being in no section at all
        # rather than in the last one named.
        section = ""
        if (trimmed ~ /^\[[^]]*\][[:space:]]*$/) {
          section = substr(trimmed, 2, index(trimmed, "]") - 2)
        }
        return
      }
      if (index(trimmed, "=") == 0) return
      key = substr(trimmed, 1, index(trimmed, "=") - 1); sub(/[[:space:]]+$/, "", key)
      if (key != "ExecStart") return
      if (section != "Service") { outside++; return }
      value = substr(trimmed, index(trimmed, "=") + 1); sub(/^[[:space:]]+/, "", value); sub(/[[:space:]]+$/, "", value)
      if (value == "") { count = 0; return }
      nargs = 0; tok = ""; q = ""; inword = 0
      n = length(value)
      for (i = 1; i <= n; i++) {
        c = substr(value, i, 1)
        if (q != "") {
          if (c == q) q = ""
          else if (c == "\\") { ambiguous = 1; return }
          else tok = tok c
        } else if (c == "\"" || c == "\047") { q = c; inword = 1 }
        else if (c == "\\") { ambiguous = 1; return }
        else if (c == " " || c == "\t") { if (inword) { args[++nargs] = tok; tok = ""; inword = 0 } }
        else { tok = tok c; inword = 1 }
      }
      if (q != "") { ambiguous = 1; return }
      if (inword) args[++nargs] = tok
      if (nargs == 0) return
      # argv[0] decides whether this command line is evidence about Olivares at all.
      name = args[1]; sub(/^.*\//, "", name)
      if (substr(args[1], 1, 1) != "/" || name != "olivares") { foreign++; return }
      for (i = 1; i <= nargs; i++) {
        if (args[i] == "--data-dir") { ambiguous = 1; return }
        if (substr(args[i], 1, 11) == "--data-dir=") values[++count] = substr(args[i], 12)
      }
    }
    BEGIN { count = 0; ambiguous = 0; logical = ""; continued = 0; section = ""; outside = 0; foreign = 0 }
    {
      line = $0; sub(/\r$/, "", line)
      if (continued) { sub(/^[[:space:]]+/, "", line); logical = logical " " line } else logical = line
      if (line ~ /\\$/) { logical = substr(logical, 1, length(logical) - 1); continued = 1; next }
      flush_logical(logical); logical = ""; continued = 0
    }
    END {
      if (continued) flush_logical(logical)
      if (ambiguous) exit 3
      if (count == 0 && (outside > 0 || foreign > 0)) exit 4
      if (count == 0) exit 0
      for (i = 2; i <= count; i++) if (values[i] != values[1]) exit 3
      if (values[1] == "") exit 3
      print values[1]
    }
  ' "$1"
}
# --- BEGIN shared ownership-record reader ------------------------------------------
# READ_OWNERSHIP_RECORD is a semantic reader for the ownership manifest this
# adapter itself writes and, on a reinstall, has to carry forward.
#
# WHY IT IS A PARSER AND NOT A PATTERN. The record is JSON, and JSON does not
# say anything about line breaks, indentation or the order of properties: the
# engine that consumes it accepts every presentation of the same object. The
# line-oriented sed/grep this block used before only recognised the exact rows
# THIS script prints, so a semantically identical record written compactly, or
# with its properties reordered, silently carried NOTHING — a successful upgrade
# dropped the recorded workspace and the AgentOps roles without a word. That is
# the defect measured in the independent review of 2026-09-05.
#
# WHY awk AND NOT jq, python OR THE INSTALLED ENGINE. jq and python are not
# guaranteed on a target host, and adding a runtime dependency to an installer
# to read one small file is not a trade this product makes. The engine binary
# could parse it — it is the authority on this contract — but it must not be
# EXECUTED here: `--root` stages images for another host (and, for a release
# image, another architecture), and this adapter never runs the staged binary
# just to read a file. awk is POSIX, is already required by the co-deployment
# installer, and the reader below is checked by scripts/test-service-install.sh
# against the very presentations the review used.
#
# BOTH INSTALLERS CARRY THIS BLOCK BYTE-IDENTICALLY, between the two markers around it,
# and scripts/test-agentops-install.sh compares them. They are delivered independently
# — install-service.sh comes out of the signed archive, install-agentops.sh is fetched on
# its own — so neither may source the other, and fetching a parser over plain HTTPS is
# exactly the trust boundary this installer refuses to cross elsewhere.
#
# It prints TAB-separated records (no recorded path may contain a control
# character, so the separator is unambiguous):
#   field<TAB>NAME<TAB>VALUE          one top-level string field
#   account<TAB>NAME<TAB>VALUE        one account field
#   file<TAB>ROLE<TAB>PATH<TAB>MODE<TAB>MANAGED
# and refuses, with the reason and the file named: malformed JSON, a duplicate
# key, an unknown field (the engine refuses those too), a wrong type, a string
# escape it will not guess at, or trailing content.
READ_OWNERSHIP_RECORD='
function fail(msg) { printf("error: ownership record %s: %s\n", f, msg) > "/dev/stderr"; exit 3 }
function skipws(   c) {
  while (pos <= N) { c = substr(doc, pos, 1)
    if (c == " " || c == "\t" || c == "\n" || c == "\r") pos++; else return }
}
function parseValue(path,   c) {
  if (path in T) fail("duplicate key \"" path "\"")
  skipws()
  if (pos > N) fail("unexpected end of document")
  c = substr(doc, pos, 1)
  if (c == "{") { T[path] = "object"; parseObject(path); return }
  if (c == "[") { T[path] = "array"; parseArray(path); return }
  if (c == "\"") { T[path] = "string"; V[path] = parseString(); return }
  parseLiteral(path)
}
function parseObject(path,   c, key, child) {
  pos++; skipws()
  if (substr(doc, pos, 1) == "}") { pos++; return }
  while (1) {
    skipws()
    if (substr(doc, pos, 1) != "\"") fail("an object key must be a quoted string")
    key = parseString(); skipws()
    if (substr(doc, pos, 1) != ":") fail("expected \":\" after key \"" key "\"")
    pos++
    child = (path == "" ? key : path "." key)
    parseValue(child)
    CH[path, ++NCH[path]] = key
    skipws(); c = substr(doc, pos, 1)
    if (c == ",") { pos++; continue }
    if (c == "}") { pos++; return }
    fail("expected \",\" or \"}\" in an object")
  }
}
function parseArray(path,   c, i) {
  pos++; skipws()
  if (substr(doc, pos, 1) == "]") { pos++; NEL[path] = 0; return }
  i = 0
  while (1) {
    parseValue(path "[" i "]"); i++
    skipws(); c = substr(doc, pos, 1)
    if (c == ",") { pos++; continue }
    if (c == "]") { pos++; NEL[path] = i; return }
    fail("expected \",\" or \"]\" in an array")
  }
}
function parseString(   out, c, e) {
  pos++; out = ""
  while (1) {
    if (pos > N) fail("unterminated string")
    c = substr(doc, pos, 1)
    if (c == "\"") { pos++; return out }
    if (c == "\\") {
      e = substr(doc, pos + 1, 1)
      if (e == "\"" || e == "\\" || e == "/") { out = out e; pos += 2; continue }
      fail("string escape \\" e " is not supported in an ownership record; repair the record")
    }
    if (c ~ /[[:cntrl:]]/) fail("a control character inside a string")
    out = out c; pos++
  }
}
function parseLiteral(path,   start, c, lit) {
  start = pos
  while (pos <= N) { c = substr(doc, pos, 1)
    if (c == "," || c == "}" || c == "]" || c == " " || c == "\t" || c == "\n" || c == "\r") break
    pos++ }
  lit = substr(doc, start, pos - start)
  if (lit == "true" || lit == "false") { T[path] = "bool"; V[path] = lit; return }
  if (lit == "null") { T[path] = "null"; V[path] = ""; return }
  if (lit ~ /^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$/) { T[path] = "number"; V[path] = lit; return }
  fail("unexpected value " (lit == "" ? "(empty)" : "\"" lit "\""))
}
function emitAccount(   i, key, path) {
  for (i = 1; i <= NCH["account"]; i++) {
    key = CH["account", i]; path = "account." key
    if (key == "user" || key == "group") {
      if (T[path] != "string") fail("\"" path "\" must be a string")
    } else if (key == "user_created" || key == "group_created") {
      if (T[path] != "bool") fail("\"" path "\" must be true or false")
    } else fail("unexpected field \"" path "\"")
    printf("account\t%s\t%s\n", key, V[path])
  }
}
function emitFiles(   i, j, base, key, path, role, p, mode, managed) {
  for (i = 0; i < NEL["files"]; i++) {
    base = "files[" i "]"
    if (T[base] != "object") fail("\"" base "\" must be an object")
    role = ""; p = ""; mode = ""; managed = ""
    for (j = 1; j <= NCH[base]; j++) {
      key = CH[base, j]; path = base "." key
      if (key == "path" || key == "role" || key == "mode") {
        if (T[path] != "string") fail("\"" path "\" must be a string")
        if (key == "path") p = V[path]; else if (key == "role") role = V[path]; else mode = V[path]
      } else if (key == "managed") {
        if (T[path] != "bool") fail("\"" path "\" must be true or false")
        managed = V[path]
      } else fail("unexpected field \"" path "\"")
    }
    if (p == "" || role == "" || mode == "" || managed == "") fail("\"" base "\" must record path, role, mode and managed")
    printf("file\t%s\t%s\t%s\t%s\n", role, p, mode, managed)
  }
}
function emit(   i, key) {
  if (T[""] != "object") fail("the top-level value must be a JSON object")
  for (i = 1; i <= NCH[""]; i++) {
    key = CH["", i]
    if (key == "schema" || key == "mode" || key == "init" || key == "layout" ||
        key == "data_dir" || key == "config" || key == "workspace_dir" || key == "manifest") {
      if (T[key] != "string") fail("\"" key "\" must be a string")
      printf("field\t%s\t%s\n", key, V[key])
    } else if (key == "account") {
      if (T[key] != "object") fail("\"account\" must be an object")
      emitAccount()
    } else if (key == "files") {
      if (T[key] != "array") fail("\"files\" must be an array")
      emitFiles()
    } else fail("unexpected field \"" key "\"; the engine refuses unknown fields, so this record cannot be carried")
  }
}
BEGIN { doc = ""; f = "the previous manifest" }
{ doc = doc $0 "\n"; if (FILENAME != "") f = FILENAME }
END {
  N = length(doc); pos = 1
  if (N == 0) fail("is empty")
  parseValue(""); skipws()
  if (pos <= N) fail("trailing content after the top-level JSON value")
  emit()
}
'
read_ownership_record() {
  have awk || err "awk is required to read the ownership record $1"
  awk "$READ_OWNERSHIP_RECORD" "$1"
}
# --- END shared ownership-record reader --------------------------------------------
# record_field <record> <key> — one top-level string field of a record the reader above
# printed. Absent prints nothing, which is how "not recorded" is told from "recorded
# empty" (the reader refuses an empty value where one is not allowed).
record_field() {
  printf '%s\n' "$1" | awk -F'\t' -v key="$2" '$1 == "field" && $2 == key { print $3; exit }'
}

# provision_dir <path> <mode> <owned|selected> — create or verify ONE directory under
# privilege without following links. The whole check-and-create runs in a single
# privileged shell: every existing component of the path must be a real directory (a link
# anywhere along it would make mkdir/chmod/chown act on an unrelated target, so the
# operator is told to pass the resolved path instead), an absent directory is created with
# mkdir (never -p, never through a link, never creating ancestors under privilege), and
# only a directory this installer OWNS has its mode and owner asserted. A SELECTED
# directory (the data dir the adapter already vetted, or an external workspace the
# operator chose) is created for the service only if absent and otherwise left exactly
# as found: no chown, no chmod.
PROVISION_DIR='
set -eu
path="$1"; mode="$2"; kind="$3"; owner="$4"
prefix=""; rest="${path#/}"
while [ -n "$rest" ]; do
  component="${rest%%/*}"; prefix="$prefix/$component"
  case "$rest" in */*) rest="${rest#*/}" ;; *) rest="" ;; esac
  if [ -L "$prefix" ]; then
    printf "error: path component is a symbolic link (%s -> %s); pass the resolved path instead of provisioning through a link: %s\n" \
      "$prefix" "$(readlink "$prefix" 2>/dev/null || printf ?)" "$path" >&2
    exit 3
  fi
done
if [ -e "$path" ]; then
  [ -d "$path" ] || { printf "error: not a directory: %s\n" "$path" >&2; exit 4; }
  if [ "$kind" = owned ]; then chmod "$mode" "$path"; chown -h "$owner" "$path"; fi
  exit 0
fi
parent="${path%/*}"; [ -n "$parent" ] || parent=/
[ -d "$parent" ] || { printf "error: parent directory does not exist; create it with the intended owner first: %s\n" "$parent" >&2; exit 5; }
mkdir "$path"
chmod "$mode" "$path"
chown -h "$owner" "$path"
'
provision_dir() { run_priv sh -c "$PROVISION_DIR" provision_dir "$1" "$2" "$3" olivares:olivares; }

install_native() (
  note "topology: NATIVE (engine + claude on the host, systemd)"

  # A subshell scopes the cleanup traps to native provisioning. The engine bootstrap,
  # agentops assets, rendered files and PEP output created below are removed on failures
  # and signals too.
  native_tmp="$(mktemp -d "${TMPDIR:-/tmp}/olivares-agentops.XXXXXX")"
  trap 'rm -rf "$native_tmp"' 0
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  # 0) the effective layout: an explicit knob wins, then what the installed service already
  # records, then the default. A knob that contradicts the installed unit is refused rather
  # than silently re-rendering our files around a service that serves another directory.
  requested_data="${OLIVARES_DATA_DIR:-}"
  requested_workspace="${OLIVARES_WORKSPACE_DIR:-}"
  recorded_data=""
  if [ -f "$NATIVE_SERVICE_UNIT" ]; then
    unit_rc=0
    recorded_data="$(unit_data_dir "$NATIVE_SERVICE_UNIT")" || unit_rc=$?
    case "$unit_rc" in
      0) ;;
      3) err "$NATIVE_SERVICE_UNIT is ambiguous about the data directory it executes the engine with (several --data-dir values, the space-separated form, escapes or unbalanced quotes in ExecStart=); repair the unit before installing AgentOps on top of it" ;;
      4) err "$NATIVE_SERVICE_UNIT has ExecStart= assignments but none of them is an Olivares invocation systemd would run: an ExecStart= outside [Service] is inert text, and one that starts another program is that program's command line, not evidence about this engine. Repair the unit (the signed adapter renders 'ExecStart=<bindir>/olivares serve --data-dir=…' inside [Service]) before installing AgentOps on top of it" ;;
      *) err "$NATIVE_SERVICE_UNIT could not be read for the data directory it executes the engine with (rc=$unit_rc)" ;;
    esac
  fi
  if [ -n "$requested_data" ]; then
    DATA_DIR="$requested_data"
    if [ -n "$recorded_data" ] && [ "$recorded_data" != "$DATA_DIR" ]; then
      err "$NATIVE_SERVICE_UNIT serves data directory $recorded_data but OLIVARES_DATA_DIR=$DATA_DIR was requested; this installer does not relocate an installed service (unset OLIVARES_DATA_DIR to keep the recorded layout, or move the estate with 'olivares dr backup', 'olivares uninstall' and a fresh install)"
    fi
    check_native_path OLIVARES_DATA_DIR "$DATA_DIR"
  elif [ -n "$recorded_data" ]; then
    DATA_DIR="$recorded_data"
    check_native_path "data directory recorded by $NATIVE_SERVICE_UNIT" "$DATA_DIR"
  else
    DATA_DIR=/var/lib/olivares
  fi
  case "$DATA_DIR" in
    /*/*) ;;
    *) err "OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $DATA_DIR" ;;
  esac
  MANIFEST="$DATA_DIR/install-manifest.json"
  recorded_workspace=""
  if [ -f "$NATIVE_SERVICE_UNIT" ]; then
    if run_priv test -L "$MANIFEST"; then
      err "$MANIFEST is a symbolic link; an ownership manifest is never read through a link"
    fi
    if run_priv test -f "$MANIFEST"; then
      run_priv cat "$MANIFEST" >"$native_tmp/manifest.recorded"
      # Read as JSON, not as lines: the engine accepts every presentation of the same
      # document, so a compact or reordered record has to yield the same workspace here.
      # Reading it line-wise is the defect the independent review measured in the signed
      # adapter (R4), and this installer is the other producer of the same record.
      recorded_record="$(read_ownership_record "$native_tmp/manifest.recorded")" ||
        err "$MANIFEST is not a readable ownership record; repair it, or move it aside to install without its records"
      recorded_workspace="$(record_field "$recorded_record" workspace_dir)"
    fi
  fi
  if [ -n "$requested_workspace" ]; then
    WORKSPACE_DIR="$requested_workspace"
    check_native_path OLIVARES_WORKSPACE_DIR "$WORKSPACE_DIR"
  elif [ -n "$recorded_workspace" ]; then
    WORKSPACE_DIR="$recorded_workspace"
    check_native_path "workspace recorded by $MANIFEST" "$WORKSPACE_DIR"
  else
    WORKSPACE_DIR="$DATA_DIR/workspaces"
  fi
  if [ "$WORKSPACE_DIR" = "$DATA_DIR/workspaces" ]; then workspace_kind=default; else workspace_kind=external; fi
  # How the hardened unit (ProtectSystem=strict, ProtectHome=true, PrivateTmp=true,
  # PrivateDevices=true) can reach the selected workspace. ReadWritePaths= re-exposes a
  # path under ProtectSystem=strict; it cannot undo ProtectHome or PrivateTmp, and
  # BindPaths= is what mounts one selected host directory through either of them:
  #
  #   /home, /root, /run/user  ProtectHome=tmpfs + BindPaths=<workspace>; every other
  #                            home stays hidden.
  #   /tmp, /var/tmp           PrivateTmp=true is KEPT and BindPaths=<workspace> mounts
  #                            that one directory into the private tree. Needs systemd
  #                            235 or later, which is where upstream a227a4be
  #                            ("we can use namespace bind mounts on dirs in /tmp or
  #                            /var/tmp even in conjunction with PrivateTmp=") first
  #                            shipped; it is an ancestor of v235 and absent from v234.
  #   /dev, /proc, /sys        refused: kernel and device interfaces, not durable state,
  #                            and the hardening replaces or read-only-mounts them.
  #
  # ⛔ UNTIL 2026-09-05 THIS BLOCK REFUSED /tmp AND /var/tmp CLAIMING "no drop-in can
  # make that workspace reachable". That was false — an untested assumption published
  # as an impossibility — and it is the R5 correction of the independent review.
  case "$WORKSPACE_DIR" in
    /home|/home/*|/root|/root/*|/run/user|/run/user/*) workspace_access=protected-home ;;
    /tmp|/tmp/*|/var/tmp|/var/tmp/*) workspace_access=private-tmp ;;
    /dev|/dev/*|/proc|/proc/*|/sys|/sys/*) err "OLIVARES_WORKSPACE_DIR=$WORKSPACE_DIR is under an API file system (/dev, /proc, /sys): those hold kernel and device interfaces rather than durable state, and the hardened unit replaces or read-only-mounts what the service would see there; choose a real directory" ;;
    *) workspace_access=plain ;;
  esac
  if [ "$workspace_access" != plain ]; then
    # BindPaths= reads its value as source:destination[:options], so a ":" in the path
    # would silently bind somewhere else. Only the paths that need a bind are checked.
    case "$WORKSPACE_DIR" in
      *:*) err "OLIVARES_WORKSPACE_DIR=$WORKSPACE_DIR contains ':' and this location can only be reached with BindPaths=, whose value uses ':' to separate source from destination; choose a path without it" ;;
    esac
  fi
  if [ "$workspace_access" = private-tmp ]; then
    # 235 is the first release carrying a227a4be, which creates the bind destination and
    # is what makes the mount work inside the private /tmp.
    systemd_running="$(systemctl --version 2>/dev/null | awk 'NR == 1 && $2 ~ /^[0-9]+$/ { print $2; exit }' || true)"
    if [ -n "$systemd_running" ] && [ "$systemd_running" -lt 235 ]; then
      err "OLIVARES_WORKSPACE_DIR=$WORKSPACE_DIR is under /tmp or /var/tmp and this host runs systemd $systemd_running: creating a BindPaths= destination inside the private /tmp needs systemd 235 or later (upstream a227a4be), so the service would see its own empty /tmp instead of the workspace; choose a location outside /tmp and /var/tmp on this host (for example /srv/olivares-workspaces)"
    fi
  fi
  data_source="default"
  [ -z "$recorded_data" ] || data_source="recorded by $NATIVE_SERVICE_UNIT"
  [ -z "$requested_data" ] || data_source="OLIVARES_DATA_DIR"
  workspace_source="default (no recorded workspace)"
  [ -z "$recorded_workspace" ] || workspace_source="recorded by $MANIFEST"
  [ -z "$requested_workspace" ] || workspace_source="OLIVARES_WORKSPACE_DIR"
  note "native layout: data=$DATA_DIR ($data_source) workspace=$WORKSPACE_DIR ($workspace_kind, $workspace_source, sandbox access: $workspace_access)"
  case "$workspace_access" in
    protected-home)
      note "workspace under a ProtectHome path: the drop-in sets ProtectHome=tmpfs and binds exactly $WORKSPACE_DIR; other home directories stay hidden from the service" ;;
    private-tmp)
      note "workspace under a private temporary directory: the drop-in keeps PrivateTmp=true and binds exactly $WORKSPACE_DIR into it; the rest of the host's /tmp stays hidden from the service (needs systemd 235 or later)"
      warn "$WORKSPACE_DIR is under /tmp or /var/tmp: those are world-writable and many distributions clear them on boot or on a timer (systemd-tmpfiles), which would delete governed session material under a running service" ;;
  esac

  # 1) the engine binary AND its systemd unit — delegate to the verifying installer
  # (cosign gate inside). `--system --init systemd` is what makes it extract the SIGNED
  # archive's own scripts/install-service.sh and lay down $NATIVE_SERVICE_UNIT. Without
  # those flags only the binary landed, so step 4's drop-in extended a unit that did not
  # exist: a fresh host announced "wired" with nothing to start, and OLIVARES_START=1 died
  # on `systemctl enable --now olivares`.
  # The verified archive is the ONLY trustworthy source of that adapter, so a missing unit
  # is repaired by re-running this installer — never by fetching install-service.sh over
  # plain HTTPS. That is also why an already-installed binary no longer short-circuits the
  # whole step: the binary and the unit are two separate preconditions.
  # `--start` is deliberately NOT passed: the plane must not come up before the drop-in and
  # agentops.env exist (step 4). Starting stays step 6, and stays OLIVARES_START's decision.
  # `--data-dir` carries the selected data directory into the signed adapter, which renders
  # it into the unit and records it (with its layout policy) in the ownership manifest.
  if have olivares && [ -f "$NATIVE_SERVICE_UNIT" ]; then
    note "olivares ($(command -v olivares)) and $NATIVE_SERVICE_UNIT already present — skipping (run scripts/install.sh to upgrade)"
  else
    if have olivares; then
      note "olivares is installed but $NATIVE_SERVICE_UNIT is absent — re-running the verified installer to lay the service down"
      note "this reinstalls the engine too; OLIVARES_VERSION and OLIVARES_BINDIR select it (otherwise the installer resolves latest and its default bindir)"
    else
      note "installing the verified Olivares engine binary + systemd service (scripts/install.sh — cosign-gated)"
    fi
    installer="$(dirname "$0")/install.sh"
    if [ ! -f "$installer" ]; then
      installer="$native_tmp/install.sh"
      dl "$RAW/scripts/install.sh" "$installer"
    fi
    # install.sh refuses --system unless it is ALREADY uid 0 and never invokes sudo, so the
    # privilege step is ours. `sudo` resets the environment, so the knobs are forwarded
    # explicitly: install.sh reads every one of them with `${VAR:-default}`, which makes
    # forwarding an unset knob as empty exactly equivalent to not forwarding it — while
    # dropping them would silently swap an operator's pinned version or cosign identity for
    # the built-in default, under privilege, without saying so.
    run_priv env \
      OLIVARES_VERSION="${OLIVARES_VERSION:-}" \
      OLIVARES_BINDIR="${OLIVARES_BINDIR:-}" \
      OLIVARES_OS="${OLIVARES_OS:-}" \
      OLIVARES_ARCH="${OLIVARES_ARCH:-}" \
      OLIVARES_CERT_IDENTITY="${OLIVARES_CERT_IDENTITY:-}" \
      OLIVARES_CERT_OIDC_ISSUER="${OLIVARES_CERT_OIDC_ISSUER:-}" \
      OLIVARES_GITHUB_URL="${OLIVARES_GITHUB_URL:-}" \
      OLIVARES_GITHUB_API_URL="${OLIVARES_GITHUB_API_URL:-}" \
      /bin/sh "$installer" --system --init systemd --data-dir "$DATA_DIR" || {
        installer_rc=$?
        warn "the verified installer failed (rc=$installer_rc); correct the precondition it named and re-run"
        exit "$installer_rc"
      }
  fi
  [ -f "$NATIVE_SERVICE_UNIT" ] ||
    err "$NATIVE_SERVICE_UNIT is absent after installation; refusing to report a wired co-deployment (the drop-in extends a unit that does not exist)"
  olivares_bin="$(command -v olivares)" || err "olivares is not on PATH after installation; add its bindir to PATH and re-run"

  # 2) claude from the official signed source (or BYO).
  provision_claude_native

  # 3) service user + writable surfaces (idempotent, link-free; see provision_dir).
  if ! id olivares >/dev/null 2>&1; then
    note "creating the no-login 'olivares' service user"
    run_priv useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin olivares 2>/dev/null \
      || run_priv adduser --system --home "$DATA_DIR" --shell /usr/sbin/nologin olivares 2>/dev/null \
      || err "could not create the olivares service account"
    id olivares >/dev/null 2>&1 || err "the olivares service account is still absent"
  fi
  note "provisioning $DATA_DIR, the claude HOME and token dir (owned by olivares) and the workspace $WORKSPACE_DIR"
  provision_dir "$DATA_DIR" 0750 selected
  provision_dir "$DATA_DIR/claude-home" 0750 owned
  # The token dir holds the short-lived inference bearer (file 0600) — 0700 parent,
  # matching the image's /run/olivares posture.
  provision_dir "$DATA_DIR/run" 0700 owned
  if [ "$workspace_kind" = default ]; then
    provision_dir "$WORKSPACE_DIR" 0750 owned
  else
    workspace_existed=0
    if run_priv test -d "$WORKSPACE_DIR"; then workspace_existed=1; fi
    provision_dir "$WORKSPACE_DIR" 0750 selected
    if [ "$workspace_existed" -eq 1 ]; then
      workspace_owner="$(run_priv stat -c %U "$WORKSPACE_DIR" 2>/dev/null || printf '?')"
      if [ "$workspace_owner" = olivares ]; then
        note "external workspace $WORKSPACE_DIR exists and is owned by olivares (left as found)"
      else
        warn "external workspace $WORKSPACE_DIR exists and is owned by $workspace_owner, not olivares: left exactly as found (no chown/chmod); governed sessions can write there only if its mode or ACL grants the olivares account write access"
      fi
    else
      note "created the external workspace $WORKSPACE_DIR for the olivares account (0750)"
    fi
  fi

  # 3b) THE TOKEN FILE THE SERVICE WOULD ACTUALLY LOAD, decided BEFORE any of our four
  # artifacts is written. $NATIVE_RUNTIME_ENV is one FIXED path shared by every estate on the
  # host, so installing a second estate over a preserved first one used to leave the FIRST
  # estate's generated token path in force: the drop-in named the new data dir, the env still
  # named the old one, this installer printed a warning, returned 0 and announced a wired
  # co-deployment — and purging the first estate then deleted the very file the second one was
  # configured to read. Measured on a real Debian 13.6 / systemd 257 host by the independent
  # review of 2026-09-05 (logs/vm-console.log:1131-1151, :1175-1215).
  #
  # The decision turns on OWNERSHIP, never on how a path looks, and rewrites at most that ONE
  # assignment:
  #
  #   · the value this estate would load                → nothing to do;
  #   · no assignment at all                            → operator content, preserved;
  #   · a value inside no Olivares estate               → a deliberate external reference (the
  #                                                       file your refresher writes), preserved;
  #   · EXACTLY the default this generator wrote for ANOTHER estate, in a file still carrying
  #     the generated marker, assigned once, and that estate's own ownership record carries
  #     this file as a managed runtime env                → that one assignment is re-pointed
  #                                                       here, every other byte preserved;
  #   · anything else that contradicts this estate      → REFUSED, with the explicit options,
  #                                                       before the manifest annotation and
  #                                                       before the banner.
  #
  # OLIVARES_RUNTIME_TOKEN_FILE decides any of them explicitly. Nothing here regenerates the
  # file from its marker: the contract shipped inside it asks the operator to edit it.
  systemd_env_version="$(systemctl --version 2>/dev/null | awk 'NR == 1 && $2 ~ /^[0-9]+$/ { print $2; exit }' || true)"
  case "$systemd_env_version" in ''|*[!0-9]*) systemd_env_version=0 ;; esac
  runtime_token_default="$DATA_DIR$RUNTIME_ENV_TOKEN_SUFFIX"
  runtime_token_selected="${OLIVARES_RUNTIME_TOKEN_FILE:-}"
  case "$runtime_token_selected" in
    ''|keep) ;;
    estate) runtime_token_selected="$runtime_token_default" ;;
    /*) check_native_path OLIVARES_RUNTIME_TOKEN_FILE "$runtime_token_selected" ;;
    *) err "invalid OLIVARES_RUNTIME_TOKEN_FILE=$runtime_token_selected (want 'estate' for $runtime_token_default, 'keep' to preserve the configured path exactly, or the absolute path of the file your refresher writes)" ;;
  esac
  runtime_token_action=none
  runtime_env_marked=0
  runtime_token_count=0
  runtime_token_start=0
  runtime_token_end=0
  runtime_token_parse=ok
  runtime_token_transport=ok
  runtime_token_utf8=ok
  runtime_token_value=""
  runtime_env_unloadable=""
  runtime_env_loadable=1
  runtime_env_decided=0
  runtime_token_estate=""
  runtime_token_shape=""
  runtime_token_render=""
  if run_priv test -f "$NATIVE_RUNTIME_ENV"; then
    run_priv cat "$NATIVE_RUNTIME_ENV" >"$native_tmp/agentops.env.existing"
    if grep -Fqx "$RUNTIME_ENV_MARKER" "$native_tmp/agentops.env.existing"; then runtime_env_marked=1; fi
    # Whether systemd loads the FILE is judged before any value in it is trusted: a NUL byte
    # (before the recognizer runs, which never sees one) and, from the recognizer, an
    # assignment that is not valid UTF-8. Either fails the whole EnvironmentFile= in every
    # supported manager, so a value the recognizer would read is not one the service gets.
    runtime_env_nul="$(runtime_env_first_nul "$native_tmp/agentops.env.existing")"
    if [ -n "$runtime_env_nul" ]; then
      runtime_env_unloadable="a NUL byte at offset $runtime_env_nul"
    else
      runtime_token_scan="$(runtime_env_token_scan "$native_tmp/agentops.env.existing" "$native_tmp/runtime-token.value")"
      runtime_token_count="$(printf '%s\n' "$runtime_token_scan" | sed -n 1p)"
      runtime_token_start="$(printf '%s\n' "$runtime_token_scan" | sed -n 2p)"
      runtime_token_end="$(printf '%s\n' "$runtime_token_scan" | sed -n 3p)"
      runtime_token_parse="$(printf '%s\n' "$runtime_token_scan" | sed -n 4p)"
      runtime_token_transport="$(printf '%s\n' "$runtime_token_scan" | sed -n 5p)"
      runtime_token_utf8="$(printf '%s\n' "$runtime_token_scan" | sed -n 6p)"
      runtime_token_value="$(cat "$native_tmp/runtime-token.value")"
      if [ "$runtime_token_utf8" = invalid-elsewhere ]; then
        runtime_env_unloadable="an assignment other than $RUNTIME_ENV_TOKEN_KEY whose key or value is not valid UTF-8"
      fi
    fi
    if [ -n "$runtime_env_unloadable" ] || [ "$runtime_token_utf8" = invalid-token ]; then
      runtime_env_why="${runtime_env_unloadable:-a $RUNTIME_ENV_TOKEN_KEY assignment whose value is not valid UTF-8}"
      runtime_env_refusal="systemd refuses to load such an EnvironmentFile= at all (measured on systemd 257: the strict form fails the unit; the EnvironmentFile=- form the drop-in uses skips the whole file)"
      if [ "$runtime_token_selected" = keep ]; then
        # keep preserves the bytes; it does not get to claim a token path systemd never loads.
        warn "OLIVARES_RUNTIME_TOKEN_FILE=keep: $NATIVE_RUNTIME_ENV is left byte-for-byte as requested, but it contains $runtime_env_why, and $runtime_env_refusal. No assignment in it, $RUNTIME_ENV_TOKEN_KEY included, reaches the service: no token path is wired and stream-json launches stay deny-closed until the file is repaired by hand"
        runtime_env_loadable=0
        runtime_env_decided=1
      elif [ -n "$runtime_env_unloadable" ]; then
        # Re-pointing one assignment would leave a file systemd still does not load, and
        # removing the offending bytes would discard content this installer does not own.
        err "$NATIVE_RUNTIME_ENV contains $runtime_env_unloadable, and $runtime_env_refusal, so no $RUNTIME_ENV_TOKEN_KEY it assigns is effective and its ownership cannot be established. Refusing to classify it as an external refresher path, to re-point one assignment inside a file systemd will not load, or to announce the co-deployment; removing the offending bytes would discard content this installer does not own. Repair the file by hand and re-run, or pass OLIVARES_RUNTIME_TOKEN_FILE=keep to preserve it byte-for-byte without claiming a wired token path"
      elif [ -z "$runtime_token_selected" ]; then
        err "$NATIVE_RUNTIME_ENV contains $runtime_env_why, and $runtime_env_refusal, so no assignment in it is effective. Refusing to classify that value as an external refresher path or announce the co-deployment. Choose explicitly and re-run: OLIVARES_RUNTIME_TOKEN_FILE=estate replaces that one assignment with $runtime_token_default, OLIVARES_RUNTIME_TOKEN_FILE=<absolute path> replaces it with the file your refresher writes (either repairs the file, every other line preserved), or OLIVARES_RUNTIME_TOKEN_FILE=keep preserves it byte-for-byte without claiming a wired token path"
      fi
      # estate or an absolute path with the token assignment as the only invalid one falls
      # through: replacing it repairs the file, under the count and offset checks below.
    fi
    if [ "$runtime_env_decided" = 1 ]; then
      :
    elif [ "$runtime_token_selected" = keep ]; then
      if [ "$runtime_token_parse" = ok ] && [ "$runtime_token_transport" = ok ]; then
        note "OLIVARES_RUNTIME_TOKEN_FILE=keep: $NATIVE_RUNTIME_ENV is left exactly as it is (systemd loads $RUNTIME_ENV_TOKEN_KEY=${runtime_token_value:-<unset>})"
      else
        note "OLIVARES_RUNTIME_TOKEN_FILE=keep: $NATIVE_RUNTIME_ENV is left byte-for-byte as requested; its $RUNTIME_ENV_TOKEN_KEY assignment could not be represented safely by this installer, so verify the effective value with your systemd manager"
      fi
    elif [ "$runtime_token_parse" != ok ]; then
      err "$NATIVE_RUNTIME_ENV uses version-sensitive EnvironmentFile syntax while this host's systemd version could not be measured, so the effective $RUNTIME_ENV_TOKEN_KEY cannot be established. Refusing to classify it as an external refresher path or announce the co-deployment. Choose explicitly and re-run: OLIVARES_RUNTIME_TOKEN_FILE=keep preserves the file byte-for-byte; after simplifying it to one unambiguous assignment, OLIVARES_RUNTIME_TOKEN_FILE=estate selects $runtime_token_default or OLIVARES_RUNTIME_TOKEN_FILE=<absolute path> sets the file your refresher writes"
    elif [ -n "$runtime_token_selected" ] && [ "$runtime_token_value" = "$runtime_token_selected" ]; then
      note "$NATIVE_RUNTIME_ENV already loads $RUNTIME_ENV_TOKEN_KEY=$runtime_token_selected"
    elif [ -n "$runtime_token_selected" ]; then
      if [ "$runtime_token_count" -gt 1 ]; then
        err "$NATIVE_RUNTIME_ENV assigns $RUNTIME_ENV_TOKEN_KEY $runtime_token_count times, so OLIVARES_RUNTIME_TOKEN_FILE cannot name which one to set without discarding the others; reduce it to one assignment (or pass OLIVARES_RUNTIME_TOKEN_FILE=keep to leave the file exactly as it is)"
      fi
      runtime_token_action=select
      runtime_token_render="$(quote_unit "$runtime_token_selected")"
    elif [ "$runtime_token_count" = 0 ]; then
      warn "$NATIVE_RUNTIME_ENV assigns no $RUNTIME_ENV_TOKEN_KEY: preserved as operator content, and stream-json launches stay deny-closed until an inference credential source is wired there"
    elif [ "$runtime_token_transport" != ok ] || ! native_path_valid "$runtime_token_value"; then
      [ "$runtime_token_transport" = ok ] || native_path_problem="contains a $runtime_token_transport that cannot name one token file"
      err "$NATIVE_RUNTIME_ENV makes systemd load $RUNTIME_ENV_TOKEN_KEY in a form that $native_path_problem. Refusing to classify it as an external refresher path or announce the co-deployment. Choose explicitly and re-run: OLIVARES_RUNTIME_TOKEN_FILE=keep preserves the file byte-for-byte, OLIVARES_RUNTIME_TOKEN_FILE=estate selects $runtime_token_default, or OLIVARES_RUNTIME_TOKEN_FILE=<absolute path> sets the file your refresher writes"
    else
      case "$runtime_token_value" in
        "$DATA_DIR"/*)
          note "$NATIVE_RUNTIME_ENV loads $RUNTIME_ENV_TOKEN_KEY=$runtime_token_value, inside the selected data directory (left exactly as it is)" ;;
        *)
          # The directory this value would be the GENERATED DEFAULT of, if it has that exact
          # form at all: <data directory>/run/session-token, with a data directory of the
          # shape this installer accepts. An operator's own path usually has neither.
          runtime_token_shape="${runtime_token_value%"$RUNTIME_ENV_TOKEN_SUFFIX"}"
          case "$runtime_token_shape" in
            "$runtime_token_value") runtime_token_shape="" ;;
            /*/*) ;;
            *) runtime_token_shape="" ;;
          esac
          # The estate the configured path lives in, if any: the deepest ancestor whose own
          # ownership record names it as the data directory it serves. A data directory is at
          # least two levels deep, so the walk stops there.
          runtime_token_probe="${runtime_token_value%/*}"
          while :; do
            case "$runtime_token_probe" in /*/*) ;; *) break ;; esac
            if estate_at "$runtime_token_probe" "$native_tmp/manifest.estate"; then
              runtime_token_estate="$runtime_token_probe"; break
            fi
            runtime_token_probe="${runtime_token_probe%/*}"
          done
          if [ -z "$runtime_token_estate" ] && [ "$runtime_env_marked" = 1 ] &&
             [ "$runtime_token_count" = 1 ] && [ -n "$runtime_token_shape" ]; then
            # Generated file, single assignment, and the value has exactly the form this
            # installer generates for an estate — but no estate answers for it. A default left
            # behind by an estate whose record is already gone and a path the operator chose
            # that happens to have that shape are indistinguishable from here, and the first of
            # the two is the defect this section exists to stop. Ask instead of guessing.
            err "$NATIVE_RUNTIME_ENV loads $RUNTIME_ENV_TOKEN_KEY=$runtime_token_value, which is not under the selected data directory $DATA_DIR; this installer generated that file, and the value has exactly the form it generates for an estate ($runtime_token_shape$RUNTIME_ENV_TOKEN_SUFFIX), but $runtime_token_shape carries no ownership record, so a default left over from an estate that is already gone cannot be told apart from a path you chose. Choose explicitly and re-run: OLIVARES_RUNTIME_TOKEN_FILE=estate re-points that one assignment at $runtime_token_default, OLIVARES_RUNTIME_TOKEN_FILE=keep preserves it exactly, OLIVARES_RUNTIME_TOKEN_FILE=<absolute path> sets the file your refresher writes; every other line of $NATIVE_RUNTIME_ENV is preserved either way"
          elif [ -z "$runtime_token_estate" ]; then
            warn "$NATIVE_RUNTIME_ENV loads $RUNTIME_ENV_TOKEN_KEY=$runtime_token_value, outside the selected data directory $DATA_DIR and inside no Olivares estate: preserved as a deliberate external reference (the file your refresher writes). Pass OLIVARES_RUNTIME_TOKEN_FILE=estate to re-point it at $runtime_token_default."
          elif [ "$runtime_env_marked" = 1 ] && [ "$runtime_token_count" = 1 ] &&
               [ "$runtime_token_shape" = "$runtime_token_estate" ] &&
               [ "$estate_generated_env" = 1 ]; then
            runtime_token_action=repoint
            runtime_token_render="$(quote_unit "$runtime_token_default")"
          else
            if [ "$runtime_env_marked" != 1 ]; then
              runtime_token_why="$NATIVE_RUNTIME_ENV carries no generated marker, so this installer did not write that value"
            elif [ "$runtime_token_count" != 1 ]; then
              runtime_token_why="$NATIVE_RUNTIME_ENV assigns $RUNTIME_ENV_TOKEN_KEY $runtime_token_count times, so no single assignment is provably this installer's"
            elif [ "$estate_generated_env" != 1 ]; then
              runtime_token_why="the ownership record of $runtime_token_estate does not carry $NATIVE_RUNTIME_ENV as a managed runtime env, so that estate does not say this installer generated it"
            else
              runtime_token_why="$runtime_token_value is not the default this installer generates for $runtime_token_estate ($runtime_token_estate$RUNTIME_ENV_TOKEN_SUFFIX), so it was chosen by hand"
            fi
            err "$NATIVE_RUNTIME_ENV loads $RUNTIME_ENV_TOKEN_KEY=$runtime_token_value, which belongs to the estate at $runtime_token_estate and not to the selected data directory $DATA_DIR, and $runtime_token_why. Refusing to wire a co-deployment whose session runtime would read its inference credential from another estate's tree (removing that estate takes the file with it). Choose explicitly and re-run: OLIVARES_RUNTIME_TOKEN_FILE=estate re-points that one assignment at $runtime_token_default, OLIVARES_RUNTIME_TOKEN_FILE=keep preserves it exactly, OLIVARES_RUNTIME_TOKEN_FILE=<absolute path> sets the file your refresher writes; every other line of $NATIVE_RUNTIME_ENV is preserved either way"
          fi ;;
      esac
    fi
  elif [ -n "$runtime_token_selected" ] && [ "$runtime_token_selected" != keep ]; then
    runtime_token_action=select
    runtime_token_render="$(quote_unit "$runtime_token_selected")"
  fi

  # 4) systemd drop-in (rendered from packaging/service/agentops.conf for THIS layout) +
  # runtime env (created once from the example, then preserved as operator content).
  src_dir="$(dirname "$0")/.."
  dropin_template="$src_dir/packaging/service/agentops.conf"
  if [ ! -f "$dropin_template" ]; then
    dropin_template="$native_tmp/agentops.conf.template"
    dl "$RAW/packaging/service/agentops.conf" "$dropin_template"
  fi
  claude_home_q="$(quote_unit "$DATA_DIR/claude-home")"
  run_dir_q="$(quote_unit "$DATA_DIR/run")"
  workspace_q="$(quote_unit "$WORKSPACE_DIR")"
  if [ "$workspace_kind" = default ]; then
    workspace_pre="ExecStartPre=/usr/bin/install -d -m 0750 $workspace_q"
    workspace_rw="-$workspace_q"
  else
    workspace_pre="# workspace $WORKSPACE_DIR is an explicitly selected external directory: not created or re-moded at start"
    workspace_rw="$workspace_q"
  fi
  # The two access markers are whole lines: deleted for a plain path, rendered only for
  # a workspace under a ProtectHome path (see the mapping above).
  # The bind is what exposes the selected workspace; ProtectHome=tmpfs is only for the
  # home case. They are rendered independently, so a workspace under /tmp gets its bind
  # WITHOUT relaxing ProtectHome, and neither case touches the rest of the hardening.
  protect_expr='/@WORKSPACE_PROTECT_HOME@/d'
  bind_expr='/@WORKSPACE_BIND@/d'
  case "$workspace_access" in
    protected-home)
      protect_expr="s|@WORKSPACE_PROTECT_HOME@|ProtectHome=tmpfs|"
      bind_expr="s|@WORKSPACE_BIND@|BindPaths=$workspace_q|" ;;
    private-tmp)
      bind_expr="s|@WORKSPACE_BIND@|BindPaths=$workspace_q|" ;;
  esac
  sed \
    -e "s|@RUNTIME_ENV@|$NATIVE_RUNTIME_ENV|g" \
    -e "s|@CLAUDE_HOME@|$claude_home_q|g" \
    -e "s|@RUN_DIR@|$run_dir_q|g" \
    -e "s|@WORKSPACE_PRE@|$workspace_pre|g" \
    -e "s|@WORKSPACE_RW@|$workspace_rw|g" \
    -e "$protect_expr" \
    -e "$bind_expr" \
    -e "s|@WORKSPACE_DIR@|$WORKSPACE_DIR|g" \
    -e "s|@DATA_DIR@|$DATA_DIR|g" \
    "$dropin_template" >"$native_tmp/agentops.conf"
  if grep -Eq '@[A-Z_]+@' "$native_tmp/agentops.conf"; then err "unresolved marker in the rendered agentops drop-in (template: $dropin_template)"; fi
  grep -Fqx "$DROPIN_MARKER" "$native_tmp/agentops.conf" || err "the agentops drop-in template lacks its managed marker (template: $dropin_template)"
  dropin_managed=0
  if [ -f "$NATIVE_DROPIN" ]; then
    if grep -Fqx "$DROPIN_MARKER" "$NATIVE_DROPIN"; then
      dropin_managed=1
      if cmp -s "$native_tmp/agentops.conf" "$NATIVE_DROPIN"; then
        note "managed drop-in $NATIVE_DROPIN already matches the layout"
      else
        note "regenerating the managed drop-in $NATIVE_DROPIN for the layout"
        run_priv install -m 0644 "$native_tmp/agentops.conf" "$NATIVE_DROPIN"
      fi
    else
      warn "$NATIVE_DROPIN exists and is not managed by this installer (no managed marker): left untouched"
      if grep -Fqx "$LEGACY_DROPIN_LINE" "$NATIVE_DROPIN"; then
        warn "it is the drop-in shipped by an earlier installer, which hard-codes /var/lib/olivares; remove it to let this installer render a managed one for data=$DATA_DIR workspace=$WORKSPACE_DIR"
      fi
      grep -Fq "$DATA_DIR/claude-home" "$NATIVE_DROPIN" ||
        warn "$NATIVE_DROPIN does not reference $DATA_DIR/claude-home; align it with the layout (HOME, token dir, ReadWritePaths) or remove it"
    fi
  else
    note "installing the managed agentops drop-in $NATIVE_DROPIN"
    run_priv install -d -m 0755 "$(dirname "$NATIVE_DROPIN")"
    run_priv install -m 0644 "$native_tmp/agentops.conf" "$NATIVE_DROPIN"
    dropin_managed=1
  fi
  run_priv install -d -m 0755 "$(dirname "$NATIVE_RUNTIME_ENV")"
  env_managed=0
  if run_priv test -f "$NATIVE_RUNTIME_ENV"; then
    if [ "$runtime_env_marked" = 1 ]; then env_managed=1; fi
    if [ "$runtime_token_action" != none ]; then
      # Only the assignment judged above is replaced, and only where it still is the one that
      # was judged: the file is re-read and re-scanned, so a change between the decision and
      # the write is named instead of overwritten. Every other byte, comments and other values
      # included, is carried through untouched, and the file keeps its mode and ownership.
      run_priv cat "$NATIVE_RUNTIME_ENV" >"$native_tmp/agentops.env.current"
      runtime_token_scan="$(runtime_env_token_scan "$native_tmp/agentops.env.current" "$native_tmp/runtime-token.current.value")"
      if ! cmp -s "$native_tmp/agentops.env.existing" "$native_tmp/agentops.env.current" ||
         [ "$(printf '%s\n' "$runtime_token_scan" | sed -n 1p)" != "$runtime_token_count" ] ||
         [ "$(printf '%s\n' "$runtime_token_scan" | sed -n 2p)" != "$runtime_token_start" ] ||
         [ "$(printf '%s\n' "$runtime_token_scan" | sed -n 3p)" != "$runtime_token_end" ] ||
         [ "$(printf '%s\n' "$runtime_token_scan" | sed -n 4p)" != "$runtime_token_parse" ]; then
        err "$NATIVE_RUNTIME_ENV changed while this run was deciding what it says about $RUNTIME_ENV_TOKEN_KEY; nothing was rewritten, re-run"
      fi
      runtime_env_token_rewrite "$native_tmp/agentops.env.current" \
        "$native_tmp/agentops.env.repointed" "$runtime_token_render" ||
        err "$NATIVE_RUNTIME_ENV could not be re-parsed at the authorized assignment; nothing was rewritten, re-run"
      env_mode="$(run_priv stat -c %a "$NATIVE_RUNTIME_ENV" 2>/dev/null || printf '640')"
      env_owner="$(run_priv stat -c %u:%g "$NATIVE_RUNTIME_ENV" 2>/dev/null || printf '0:0')"
      run_priv install -m "$env_mode" -o "${env_owner%:*}" -g "${env_owner#*:}" \
        "$native_tmp/agentops.env.repointed" "$NATIVE_RUNTIME_ENV"
      if [ "$runtime_token_action" = repoint ]; then
        note "re-pointed $RUNTIME_ENV_TOKEN_KEY in $NATIVE_RUNTIME_ENV from $runtime_token_value (the default this installer generated for the estate at $runtime_token_estate) to $runtime_token_default; every other line is preserved"
        # Preserving the rest is the contract, and it has a visible consequence worth saying
        # out loud rather than silently repairing: the commented examples the generator wrote
        # for the previous estate keep naming it. They configure nothing until the operator
        # uncomments one, and they are the operator's file to edit.
        if run_priv grep -Fq "$runtime_token_estate" "$NATIVE_RUNTIME_ENV"; then
          note "$NATIVE_RUNTIME_ENV still mentions $runtime_token_estate in lines this installer does not own (commented examples and your own notes): they configure nothing as they stand, but review them before enabling one"
        fi
      else
        note "set $RUNTIME_ENV_TOKEN_KEY=$runtime_token_selected in $NATIVE_RUNTIME_ENV (OLIVARES_RUNTIME_TOKEN_FILE); every other line is preserved"
      fi
    fi
  else
    env_example="$src_dir/packaging/olivares-agentops.env.example"
    if [ ! -f "$env_example" ]; then
      env_example="$native_tmp/agentops.env.example"
      dl "$RAW/packaging/olivares-agentops.env.example" "$env_example"
    fi
    # The example documents the default layout; its path assignments are re-pointed at the
    # selected data dir (quoted when it contains a space, which EnvironmentFile= unquotes).
    {
      printf '%s\n' "$RUNTIME_ENV_MARKER"
      case "$DATA_DIR" in
        *' '*) sed "s|=/var/lib/olivares/\(.*\)\$|=\"$DATA_DIR/\1\"|" "$env_example" ;;
        *) sed "s|=/var/lib/olivares/|=$DATA_DIR/|" "$env_example" ;;
      esac
    } >"$native_tmp/agentops.env"
    if [ "$runtime_token_action" = select ]; then
      runtime_token_scan="$(runtime_env_token_scan "$native_tmp/agentops.env" "$native_tmp/generated-token.value")"
      runtime_token_count="$(printf '%s\n' "$runtime_token_scan" | sed -n 1p)"
      [ "$runtime_token_count" = 1 ] ||
        err "$env_example assigns no $RUNTIME_ENV_TOKEN_KEY, so OLIVARES_RUNTIME_TOKEN_FILE has nothing to select in the generated $NATIVE_RUNTIME_ENV"
      runtime_env_token_rewrite "$native_tmp/agentops.env" "$native_tmp/agentops.env.selected" \
        "$runtime_token_render" || err "$env_example has an ambiguous $RUNTIME_ENV_TOKEN_KEY assignment"
      cp "$native_tmp/agentops.env.selected" "$native_tmp/agentops.env"
      note "generated $NATIVE_RUNTIME_ENV with $RUNTIME_ENV_TOKEN_KEY=$runtime_token_selected (OLIVARES_RUNTIME_TOKEN_FILE)"
    fi
    run_priv install -m 0640 "$native_tmp/agentops.env" "$NATIVE_RUNTIME_ENV"
    env_managed=1
    note "wrote $NATIVE_RUNTIME_ENV (edit it: point the inference token at your refresher)"
  fi

  # 4b) record the AgentOps layout in the signed adapter's ownership manifest so uninstall
  # and doctor resolve the same files and workspace. Our previous entries are replaced,
  # everything the adapter wrote is kept verbatim, and the installed engine validates the
  # result (it refuses unknown fields, off-layout paths and a workspace it cannot parse)
  # before the previous record is discarded.
  if ! run_priv test -f "$MANIFEST"; then
    warn "no ownership manifest at $MANIFEST (an operator-managed service has no signed-adapter record): uninstall will not know about $NATIVE_DROPIN and $NATIVE_RUNTIME_ENV"
  else
    run_priv cat "$MANIFEST" >"$native_tmp/manifest.old"
    # The annotated record is EMITTED FROM THE PARSED DOCUMENT, not patched line by line.
    # A line-oriented rewrite only recognises the exact rows the adapter prints, so a
    # previous record written compactly or with its properties reordered — both valid,
    # both accepted by the engine — came out with nothing added while this installer
    # announced a recorded layout. Emitting from the record makes the output depend on
    # what the document MEANS; for the presentation the adapter writes it is byte for
    # byte the same file as before, which is what the goldens pin.
    read_ownership_record "$native_tmp/manifest.old" >"$native_tmp/manifest.rows" ||
      err "$MANIFEST is not a readable ownership record; repair it, or move it aside to install without its records"
    awk -F'\t' -v ws="$WORKSPACE_DIR" -v dropin="$NATIVE_DROPIN" -v dropin_managed="$dropin_managed" \
      -v env="$NATIVE_RUNTIME_ENV" -v env_managed="$env_managed" '
      function bool(v) { return v == 1 ? "true" : "false" }
      # esc re-escapes what the reader decoded, so a round trip through this emitter can
      # never produce a document the reader would then refuse. Recorded paths cannot hold
      # either character today (check_native_path/safe_path refuse them), which is why it
      # is three lines of insurance rather than a feature.
      function esc(v) { gsub(/\\/, "\\\\", v); gsub(/"/, "\\\"", v); return v }
      function entry(path, role, mode, managed) {
        return "    {\"path\": \"" esc(path) "\", \"role\": \"" esc(role) "\", \"mode\": \"" esc(mode) "\", \"managed\": " managed "}"
      }
      $1 == "field" { value[$2] = $3; seen[$2] = 1; next }
      $1 == "account" { akey[++na] = $2; avalue[$2] = $3; next }
      $1 == "file" {
        # Our own previous entries are replaced; everything else is carried in the order
        # the record listed it.
        if ($2 == "dropin" || $2 == "runtime-env") next
        carried[++nc] = entry($3, $2, $4, $5); next
      }
      END {
        # Only what the record actually had is written back, in the order the adapter
        # prints it: a field this installer does not compute is never invented as "".
        split("schema mode init layout data_dir config", head, " ")
        printf("{\n")
        for (i = 1; i <= 6; i++)
          if (head[i] in seen) printf("  \"%s\": \"%s\",\n", head[i], esc(value[head[i]]))
        printf("  \"workspace_dir\": \"%s\",\n", esc(ws))
        printf("  \"files\": [\n")
        n = 0
        for (i = 1; i <= nc; i++) rows[n++] = carried[i]
        if (dropin_managed == 1) rows[n++] = entry(dropin, "dropin", "0644", "true")
        rows[n++] = entry(env, "runtime-env", "0640", bool(env_managed))
        for (i = 0; i < n; i++) printf("%s%s\n", rows[i], i < n - 1 ? "," : "")
        printf("  ],\n")
        if (na > 0) {
          printf("  \"account\": {")
          for (i = 1; i <= na; i++) {
            key = akey[i]
            quoted = (key == "user" || key == "group")
            printf("%s\"%s\": %s%s%s", i > 1 ? ", " : "", esc(key),
                   quoted ? "\"" : "", quoted ? esc(avalue[key]) : avalue[key], quoted ? "\"" : "")
          }
          printf("},\n")
        }
        # The manifest path closes the object, so it is the one field whose absence would
        # leave a trailing comma: an ownership record without it is not one.
        if (!("manifest" in seen)) {
          printf("error: the previous ownership record names no manifest path\n") > "/dev/stderr"
          exit 3
        }
        printf("  \"manifest\": \"%s\"\n}\n", esc(value["manifest"]))
      }
    ' "$native_tmp/manifest.rows" >"$native_tmp/manifest.new" ||
      err "the previous ownership record at $MANIFEST cannot be carried forward (it was left exactly as it was); repair it, or move it aside to install without its records"
    # The rewrite above edits the presentation the adapter writes. Before it replaces the
    # previous record, the result is READ BACK semantically and required to say exactly
    # what this run promised: a record this installer could not annotate is refused with
    # the reason named, never installed while the banner claims a recorded layout.
    annotated_record="$(read_ownership_record "$native_tmp/manifest.new")" ||
      err "the annotated ownership record is not readable; $MANIFEST was left exactly as it was"
    printf '%s\n' "$annotated_record" | awk -F'\t' \
      -v ws="$WORKSPACE_DIR" -v dropin="$NATIVE_DROPIN" -v want_dropin="$dropin_managed" \
      -v env="$NATIVE_RUNTIME_ENV" '
      $1 == "field" && $2 == "workspace_dir" { got_ws = $3; seen_ws = 1 }
      $1 == "file" && $2 == "dropin" { dropins++; dropin_path = $3 }
      $1 == "file" && $2 == "runtime-env" { envs++; env_path = $3 }
      END {
        if (!seen_ws || got_ws != ws) { printf("the record does not carry workspace_dir=%s\n", ws); exit 1 }
        if (dropins != want_dropin + 0) { printf("the record carries %d drop-in entries, not %d\n", dropins, want_dropin); exit 1 }
        if (dropins == 1 && dropin_path != dropin) { printf("the record carries drop-in %s, not %s\n", dropin_path, dropin); exit 1 }
        if (envs != 1) { printf("the record carries %d runtime env entries, not 1\n", envs); exit 1 }
        if (env_path != env) { printf("the record carries runtime env %s, not %s\n", env_path, env); exit 1 }
      }' >"$native_tmp/annotation.verdict" || {
        sed 's/^/    /' "$native_tmp/annotation.verdict" >&2
        err "this installer could not record its layout in $MANIFEST (the previous record is untouched, while the drop-in and runtime env written above remain in place); repair the ownership manifest and re-run"
      }
    run_priv install -m 0640 -o root -g olivares "$native_tmp/manifest.new" "$MANIFEST"
    if ! run_priv "$olivares_bin" uninstall --plan --data-dir "$DATA_DIR" >"$native_tmp/manifest.plan" 2>&1; then
      run_priv install -m 0640 -o root -g olivares "$native_tmp/manifest.old" "$MANIFEST"
      sed 's/^/    /' "$native_tmp/manifest.plan" >&2
      err "the installed engine rejected the annotated ownership manifest; the previous ownership record was restored, while the drop-in and runtime env written above remain in place (an engine older than the layout contract needs OLIVARES_VERSION set to a newer release, or the recorded layout needs repair)"
    fi
    note "recorded the AgentOps layout in $MANIFEST (workspace=$WORKSPACE_DIR)"
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
      olivares agent managed-settings --pep-command 'olivares claude-hook' > "$native_tmp/managed-settings.json"
      run_priv install -m 0644 "$native_tmp/managed-settings.json" /etc/claude-code/managed-settings.json
      note "set OLIVARES_SESSION_PEP_URL in $NATIVE_RUNTIME_ENV to activate it (else tool-calls are deny-closed)"
    fi
  fi
  run_priv systemctl daemon-reload

  # The banner below is the only line most operators read, so it does not get to claim a
  # wired co-deployment over a host with no unit — that IS the defect this function had.
  [ -f "$NATIVE_SERVICE_UNIT" ] ||
    err "$NATIVE_SERVICE_UNIT is absent after installation; refusing to report a wired co-deployment (the drop-in extends a unit that does not exist)"
  # Nor over a runtime env systemd will not load: keep preserved it, and the banner says so.
  [ "$runtime_env_loadable" = 1 ] ||
    warn "$NATIVE_RUNTIME_ENV is preserved as you asked but systemd cannot load it as it stands (see above): the service starts without any of its assignments, and no inference token path is wired, until it is repaired"

  if [ "$START" = "1" ]; then
    note "starting olivares (OLIVARES_START=1)"; run_priv systemctl enable --now olivares
  fi
  say ""
  say "✅ native co-deployment wired."
  say "   data: $DATA_DIR    workspace: $WORKSPACE_DIR ($workspace_kind)"
  say "   claude: $(command -v claude 2>/dev/null || echo '<BYO — set OLIVARES_SESSION_RUNTIME_CLAUDE_BIN>')"
  if [ "$START" != "1" ]; then
    say ""
    say "Next (running a governance plane is your explicit decision):"
    say "  1) edit $NATIVE_RUNTIME_ENV — wire the short-lived inference token (refresher)"
    say "  2) sudo systemctl enable --now olivares     # loopback-only by default"
    say "  3) register a workspace + launch the first governed session (see the how-to)"
  fi
)

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
