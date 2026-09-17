#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Execute the complete POSIX entrypoint, including set -eu and cleanup traps.
# Only the /etc, /var/lib/olivares, /home, /tmp and /var/tmp namespaces are mapped
# into a fixture, each exactly once, whatever the prefix contains; PATH
# provides inert command ports for everything that would touch the host (sudo,
# install, chown, systemctl, accounts, curl, the engine) and real utilities for
# the rest, so the privileged provisioning shell runs its real mkdir/chmod/-L
# checks against fixture paths. The sudo port clears the environment so
# inheritance cannot conceal a lost pin. No host accounts, ownership, services,
# network or production files are changed.
# Signature verification itself belongs to test-release-installer.sh; this battery
# checks that native provisioning delegates to that installer with its controls
# and renders every AgentOps file from the one selected layout.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
golden_dir="$root/cmd/olivares/internal/localinstall/testdata"
passes=0
fail() { printf 'not ok - %s\n' "$*" >&2; exit 1; }
# A precondition this battery cannot meet is "could not look" (2), never a finding (1): the
# installer under test was not measured, so neither verdict about it would be true.
cannot() { printf 'not ok - %s\n' "$*" >&2; exit 2; }
case_ok() { passes=$((passes + 1)); printf 'ok %d - %s\n' "$passes" "$1"; }
# Scratch parent. Every fixture, and so the default data directory and default workspace the
# installer under test selects, lives under it. The installer refuses a workspace under /dev,
# /proc or /sys: an API file system is not durable state and the hardened unit hides it. The
# fixture namespace below maps /etc, /var/lib/olivares, /home, /tmp and /var/tmp into the
# fixture on purpose and leaves the API file systems unmapped, so that refusal stays real for
# the /proc negative case; a scratch under /dev/shm therefore makes every positive case fail
# on the installer's own guard. Measured 2026-09-07 in mainline-ci (run 34167328609, job
# 101880875672, step leg-release-mechanics): the job points TMPDIR at /dev/shm so other
# batteries get short unix-socket paths, and the first positive case here was refused with
# "/dev/shm/olv3331250/.../var/lib/olivares/workspaces is under an API file system". That
# TMPDIR is right for those batteries and wrong for this one, whose scratch must be a place
# where a real install could live. Selection, in order:
#   - an explicit OLIVARES_TEST_SCRATCH_ROOT is honoured as given and must be valid; it is
#     never replaced silently, so an invalid explicit choice is reported, not worked around;
#   - otherwise TMPDIR (or /tmp when unset), unless that is under an API file system or inside
#     the checkout, in which case the parent of the checkout is used, outside the checkout and on
#     a durable file system, exactly as test-service-install.sh and test-release-installer.sh do.
# The scratch is never inside the checkout, whichever way it was chosen: the fixtures are an
# untracked tree of 120 rendered installs, and while this battery runs, other legs of the same
# push, git status, packaging, mutation checks and file walkers would see them as part of the
# tree under test. Cleanup at exit is not isolation during the run. An explicit root inside the
# checkout is refused (2) rather than replaced; an implicit TMPDIR inside it falls back, and says
# so. Only the directory mktemp creates below is cleaned; the parent is never touched. The choice
# is disclosed as a TAP comment so a log shows where the fixtures lived.
# Every test is on the PHYSICAL path (symlinks resolved): a link into /dev/shm is still /dev/shm,
# a link into the checkout is still the checkout, and the physical path is also what the fixtures
# are built under, so the installer sees the same text a host would and the golden comparisons
# strip a stable prefix. Containment is a directory-boundary test, not a string prefix: a sibling
# whose name merely extends the checkout's (`…/repo-two` beside `…/repo`) is outside it.
api_fs() { case "$1" in /dev|/dev/*|/proc|/proc/*|/sys|/sys/*) return 0 ;; *) return 1 ;; esac; }
physical() { (CDPATH= cd -- "$1" 2>/dev/null && pwd -P); }
within() { [[ "$1" == "$2" || "$2" == / || "$1" == "$2"/* ]]; } # <dir> is <ancestor> or below it
root_phys="$(physical "$root")" || cannot "checkout $root is not an enterable directory"
if [[ -n "${OLIVARES_TEST_SCRATCH_ROOT:-}" ]]; then
  scratch_parent="$(physical "$OLIVARES_TEST_SCRATCH_ROOT")" || cannot "OLIVARES_TEST_SCRATCH_ROOT=$OLIVARES_TEST_SCRATCH_ROOT is not an enterable directory; set it to an executable directory outside /dev, /proc, /sys and outside the checkout"
  scratch_why="OLIVARES_TEST_SCRATCH_ROOT, explicit"
  if api_fs "$scratch_parent"; then
    cannot "OLIVARES_TEST_SCRATCH_ROOT=$OLIVARES_TEST_SCRATCH_ROOT resolves to $scratch_parent, under an API file system (/dev, /proc, /sys); the installer under test refuses a workspace there by design, so fixtures cannot be staged there: choose a real directory"
  fi
  if within "$scratch_parent" "$root_phys"; then
    cannot "OLIVARES_TEST_SCRATCH_ROOT=$OLIVARES_TEST_SCRATCH_ROOT resolves to $scratch_parent, inside the checkout $root_phys; fixtures are never staged in the tree under test, where other legs, repository status and file walkers would see them: choose a directory outside the checkout"
  fi
else
  scratch_parent="${TMPDIR:-/tmp}"
  scratch_why="TMPDIR"
  [[ -n "${TMPDIR:-}" ]] || scratch_why="/tmp, TMPDIR unset"
  if resolved="$(physical "$scratch_parent")"; then
    if api_fs "$resolved"; then
      scratch_parent="$(dirname -- "$root_phys")"
      scratch_why="parent of the checkout, because TMPDIR=${TMPDIR:-/tmp} resolves to $resolved, under an API file system"
    elif within "$resolved" "$root_phys"; then
      scratch_parent="$(dirname -- "$root_phys")"
      scratch_why="parent of the checkout, because TMPDIR=${TMPDIR:-/tmp} resolves to $resolved, inside the checkout"
    fi
  fi
  scratch_parent="$(physical "$scratch_parent")" || cannot "scratch parent ${TMPDIR:-/tmp} ($scratch_why) is not an enterable directory; set OLIVARES_TEST_SCRATCH_ROOT to an executable directory outside /dev, /proc, /sys and outside the checkout"
  # Only a checkout at / can make its own parent lie inside it; even then nothing is staged.
  within "$scratch_parent" "$root_phys" && cannot "scratch parent $scratch_parent ($scratch_why) is inside the checkout $root_phys; set OLIVARES_TEST_SCRATCH_ROOT to an executable directory outside it"
fi
[[ -d "$scratch_parent" && -w "$scratch_parent" ]] || cannot "scratch parent $scratch_parent ($scratch_why) is not a writable directory; set OLIVARES_TEST_SCRATCH_ROOT to an executable directory outside /dev, /proc, /sys and outside the checkout"
scratch="$(mktemp -d "$scratch_parent/olivares-agentops-test.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
printf '# scratch: %s (parent from %s)\n' "$scratch" "$scratch_why"
/bin/sh -n "$root/scripts/install-agentops.sh"
# The ownership-record reader is carried byte-identically by both installers, which are
# delivered separately and may not source each other. Drift between the copies would mean
# the two producers of the same record disagree about what it says, so it is checked here
# rather than left to review.
reader_block() {
  awk '/^# --- BEGIN shared ownership-record reader/ { on = 1; next }
       /^# --- END shared ownership-record reader/ { on = 0 }
       on' "$1"
}
[[ -n "$(reader_block "$root/scripts/install-service.sh")" ]] || fail 'install-service.sh carries no marked ownership-record reader'
diff <(reader_block "$root/scripts/install-service.sh") <(reader_block "$root/scripts/install-agentops.sh") ||
  fail 'the two installers carry different copies of the shared ownership-record reader'
case_ok 'both installers carry the same ownership-record reader'
# The test executes command ports; make a hardened noexec scratch mount explicit. It is a
# precondition of this battery, not a finding about the installer, so it is 2 and it is never
# hidden by falling back to another directory.
printf '#!/bin/sh\nexit 0\n' > "$scratch/exec-probe"
chmod +x "$scratch/exec-probe"
if ! "$scratch/exec-probe" >/dev/null 2>&1; then
  cannot "scratch $scratch ($scratch_why) is not executable; set OLIVARES_TEST_SCRATCH_ROOT or TMPDIR to an executable directory outside /dev, /proc, /sys and outside the checkout"
fi
case "$scratch" in *' '*|*'@'*|*'$'*|*'%'*|*';'*|*'#'*|*'|'*|*'&'*) fail "scratch path contains a character the installer refuses in a layout path: $scratch" ;; esac

# An allowlist prevents a missing port from reaching a real host/network command.
mkdir "$scratch/tools"
for utility in basename dirname env mktemp rm mkdir cp chmod ln cat sort grep sed awk cmp readlink stat sh test tr wc; do
  ln -s "$(type -P "$utility")" "$scratch/tools/$utility"
done
cat > "$scratch/port.sh" <<'PORT'
#!/bin/sh
set -eu
kind="$(basename "$0")"
printf '%s %s\n' "$kind" "$*" >> "$PROBE_ROOT/events"
case "$kind" in
  id)
    if [ "${1:-}" = -u ]; then cat "$PROBE_ROOT/uid"; else test -f "$PROBE_ROOT/account"; fi ;;
  sudo)
    # Preserve only test transport, never the caller's OLIVARES_* or sentinel.
    exec env -i PATH="$PATH" PROBE_ROOT="$PROBE_ROOT" "$@" ;;
  install)
    rc="$(cat "$PROBE_ROOT/install-rc")"
    [ "$rc" = 0 ] || exit "$rc"
    # Enough of install(1) to leave real files behind: -d creates directories,
    # otherwise the last operand receives a copy of the first. Owner/group/mode
    # options are recorded above and otherwise ignored (no host ownership).
    dir=0; src=""; target=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -d) dir=1; shift ;;
        -m|-o|-g) shift 2 ;;
        *) if [ "$dir" = 1 ]; then mkdir -p "$1"; elif [ -z "$src" ]; then src="$1"; else target="$1"; fi; shift ;;
      esac
    done
    if [ "$dir" = 0 ]; then
      cp "$src" "$target"
      case "$target" in
        */olivares.service.d/agentops.conf) : > "$PROBE_ROOT/dropin-installed" ;;
        */olivares/agentops.env) : > "$PROBE_ROOT/env-installed" ;;
      esac
    fi ;;
  chown) ;;
  systemctl)
    case "$1" in
      --version)
        cat "$PROBE_ROOT/systemd-version"
        exit "$(cat "$PROBE_ROOT/systemd-version-rc")" ;;
      daemon-reload) exit "$(cat "$PROBE_ROOT/reload-rc")" ;;
      enable)
        [ -f "$PROBE_ROOT/etc/systemd/system/olivares.service" ] || exit 71
        [ -f "$PROBE_ROOT/dropin-installed" ] && [ -f "$PROBE_ROOT/env-installed" ] || exit 72
        exit "$(cat "$PROBE_ROOT/start-rc")" ;;
      *) exit 73 ;;
    esac ;;
  curl)
    [ "$#" = 4 ] && [ "$1" = -fsSL ] && [ "$3" = -o ] || exit 81
    if [ -f "$PROBE_ROOT/download-signal" ]; then kill -TERM "$PPID"; exit 143; fi
    rc="$(cat "$PROBE_ROOT/download-rc")"
    [ "$rc" = 0 ] || exit "$rc"
    case "$2" in
      */scripts/install.sh) cp "$PROBE_ROOT/verified-installer-port.sh" "$4" ;;
      */packaging/service/agentops.conf) cp "$PROBE_ROOT/assets/agentops.conf" "$4" ;;
      */packaging/olivares-agentops.env.example) cp "$PROBE_ROOT/assets/olivares-agentops.env.example" "$4" ;;
      *) exit 82 ;;
    esac ;;
  olivares)
    if [ "${1:-}" = agent ] && [ "${2:-}" = managed-settings ]; then
      rc="$(cat "$PROBE_ROOT/pep-rc")"
      [ "$rc" = 0 ] || exit "$rc"
      printf '{"hooks":{}}\n'
    elif [ "${1:-}" = uninstall ]; then
      [ "${2:-}" = --plan ] || exit 84
      rc="$(cat "$PROBE_ROOT/plan-rc")"
      [ "$rc" = 0 ] || { printf 'fixture engine: manifest rejected\n' >&2; exit "$rc"; }
      printf 'fixture plan\n'
    else printf 'fixture engine\n'; fi ;;
  claude) exit 0 ;;
  useradd|adduser) exit "$(cat "$PROBE_ROOT/account-rc")" ;;
  *) exit 83 ;;
esac
PORT
# The verified installer port stands in for install.sh + the signed adapter: it
# leaves a real unit naming the --data-dir it was given (quoted when it has a
# space, as the adapter renders it) and the adapter-shaped ownership manifest.
cat > "$scratch/verified-installer-port.sh" <<'INSTALLER'
#!/bin/sh
set -eu
printf 'verified-installer\n' >> "$PROBE_ROOT/events"
printf '%s\n' "$@" > "$PROBE_ROOT/installer-argv"
env | sort > "$PROBE_ROOT/installer-env"
[ -z "${PROBE_MUST_BE_DROPPED:-}" ] || exit 92
if [ -f "$PROBE_ROOT/expected-env" ]; then
  while IFS= read -r expected; do
    grep -Fqx -- "$expected" "$PROBE_ROOT/installer-env" || exit 93
  done < "$PROBE_ROOT/expected-env"
fi
rc="$(cat "$PROBE_ROOT/installer-rc")"
[ "$rc" = 0 ] || exit "$rc"
ln -sf "$PROBE_ROOT/port.sh" "$PROBE_ROOT/bin/olivares"
system=0; data=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --system) system=1; shift ;;
    --data-dir) data="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ "$system" = 1 ] && [ "$(cat "$PROBE_ROOT/land-unit")" = 1 ]; then
  case "$data" in *' '*) data_q="\"$data\"" ;; *) data_q="$data" ;; esac
  layout=custom
  [ "$data" != "$(cat "$PROBE_ROOT/default-data")" ] || layout=default
  {
    printf '[Service]\n'
    printf 'ExecStart=/usr/local/bin/olivares serve --data-dir=%s --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444\n' "$data_q"
    printf 'ReadWritePaths=%s\n' "$data_q"
  } > "$PROBE_ROOT/etc/systemd/system/olivares.service"
  mkdir -p "$data"
  # Like scripts/install-service.sh, a rerun carries the co-deployment records of the
  # previous manifest (workspace, drop-in and runtime env entries) verbatim.
  carried_ws=""; carried_entries=""
  if [ -r "$data/install-manifest.json" ]; then
    carried_ws="$(sed -n 's/^[[:space:]]*"workspace_dir":[[:space:]]*"\(.*\)",\{0,1\}[[:space:]]*$/\1/p' "$data/install-manifest.json")"
    carried_entries="$(grep -E '"role": "(dropin|runtime-env)"' "$data/install-manifest.json" | sed -e 's/,[[:space:]]*$//' -e 's/^[[:space:]]*//' || true)"
  fi
  {
    printf '{\n  "schema": "olivares.ai/local-install/v2",\n  "mode": "system",\n  "init": "systemd",\n  "layout": "%s",\n' "$layout"
    printf '  "data_dir": "%s",\n  "config": "%s/etc/olivares/olivares.env",\n' "$data" "$PROBE_ROOT"
    [ -z "$carried_ws" ] || printf '  "workspace_dir": "%s",\n' "$carried_ws"
    printf '  "files": [\n'
    printf '    {"path": "/usr/local/bin/olivares", "role": "binary", "mode": "0755", "managed": true},\n'
    printf '    {"path": "%s/etc/olivares/olivares.env", "role": "config", "mode": "0640", "managed": true},\n' "$PROBE_ROOT"
    printf '    {"path": "%s/etc/systemd/system/olivares.service", "role": "unit", "mode": "0644", "managed": true}' "$PROBE_ROOT"
    if [ -n "$carried_entries" ]; then
      printf '%s\n' "$carried_entries" | while IFS= read -r entry; do printf ',\n    %s' "$entry"; done
    fi
    printf '\n  ],\n  "account": {"user": "olivares", "group": "olivares", "user_created": true, "group_created": true},\n'
    printf '  "manifest": "%s/install-manifest.json"\n}\n' "$data"
  } > "$data/install-manifest.json"
fi
INSTALLER

# fixture <name> [binary-present] [unit-present]: a fresh namespace. With a unit
# present, it names the fixture's default data dir the way the adapter renders it
# and that data dir exists with the adapter's manifest (an installed host).
fixture() {
  local name="$1" binary="${2:-0}" unit="${3:-0}"
  fx="$scratch/$name"
  mkdir -p "$fx"/{bin,scripts,tmp,assets,etc/systemd/system,packaging/service,var/lib,srv,mnt,home}
  cp "$scratch/port.sh" "$fx/port.sh"
  cp "$scratch/verified-installer-port.sh" "$fx/verified-installer-port.sh"
  cp "$fx/verified-installer-port.sh" "$fx/scripts/install.sh"
  chmod +x "$fx/port.sh"
  for port in id sudo install chown systemctl curl claude useradd adduser; do ln -s "$fx/port.sh" "$fx/bin/$port"; done
  for value in install reload start download pep installer plan; do printf '0\n' > "$fx/$value-rc"; done
  printf 'systemd 257 (fixture)\n' > "$fx/systemd-version"
  printf '0\n' > "$fx/systemd-version-rc"
  printf '1\n' > "$fx/land-unit"
  printf '1001\n' > "$fx/uid"
  printf '57\n' > "$fx/account-rc"
  printf '%s\n' "$fx/var/lib/olivares" > "$fx/default-data"
  : > "$fx/account"
  # The real template and example, in the fixture namespace. The template has
  # no namespaced path (markers only); the example's default paths are mapped so
  # the installer's re-pointing acts on the same text it would on a host.
  cp "$root/packaging/service/agentops.conf" "$fx/packaging/service/agentops.conf"
  python3 - "$root/packaging/olivares-agentops.env.example" "$fx/packaging/olivares-agentops.env.example" "$fx" <<'PY'
import pathlib
import sys
source, target, fixture = sys.argv[1:]
pathlib.Path(target).write_text(pathlib.Path(source).read_text().replace('/var/lib/olivares', fixture + '/var/lib/olivares'))
PY
  cp "$fx/packaging/service/agentops.conf" "$fx/assets/agentops.conf"
  cp "$fx/packaging/olivares-agentops.env.example" "$fx/assets/olivares-agentops.env.example"
  if [[ "$binary" = 1 ]]; then ln -s "$fx/port.sh" "$fx/bin/olivares"; fi
  if [[ "$unit" = 1 ]]; then
    PROBE_ROOT="$fx" /bin/sh "$fx/verified-installer-port.sh" --system --init systemd --data-dir "$fx/var/lib/olivares"
    : > "$fx/events"
  fi
  python3 - "$root/scripts/install-agentops.sh" "$fx" <<'PY'
import pathlib
import sys
source, fixture = map(pathlib.Path, sys.argv[1:])
# Namespace mapping only: do not extract functions or alter shell semantics. EVERY mapped
# root is sentinelled FIRST, longest first, and every sentinel is expanded LAST, so each
# original root is replaced exactly once and no inserted prefix is ever rewritten by a
# later root. That is not tidiness: the prefix is a real absolute path chosen by the host,
# and a mapped root inside it has to stay literal. Measured in mainline-ci run 34286700558,
# job 102263870572, step leg-release-mechanics, where the scratch was under /home/runner:
# expanding /etc/ and /var/lib/olivares before replacing /home rewrote the prefix those two
# had just inserted, the unit landed at $fx$fx/etc/systemd/system/olivares.service, and
# install-agentops.sh refused the co-deployment it had just wired -- correctly, by its own
# fail-closed unit check. Longest first is what keeps "/var/tmp" from being read as "/tmp".
# The two shared temporary roots keep their fixture homes, siblings of the installer's own
# TMPDIR ($fx/tmp), which run_case still requires to be empty after every run.
roots = {'/var/lib/olivares': '/var/lib/olivares', '/var/tmp': '/var-tmp-host',
         '/etc/': '/etc/', '/home': '/home', '/tmp': '/tmp-host'}
text = source.read_text()
assert '\x00' not in text, 'the installer carries a NUL byte: the sentinels below are not unique'
expansions = {}
for index, original in enumerate(sorted(roots, key=len, reverse=True)):
    sentinel = '\x00%d\x00' % index
    expansions[sentinel] = str(fixture) + roots[original]
    text = text.replace(original, sentinel)
for sentinel, mapped in expansions.items():
    text = text.replace(sentinel, mapped)
(fixture / 'scripts/install-agentops.sh').write_text(text)
PY
}

run_case() {
  local want="$1" label="$2" got
  shift 2
  : > "$fx/events"
  set +e
  env -i PATH="$fx/bin:$scratch/tools" PROBE_ROOT="$fx" \
    PROBE_MUST_BE_DROPPED=yes TMPDIR="$fx/tmp" \
    OLIVARES_TOPOLOGY=native OLIVARES_CLAUDE_INSTALL=byo \
    OLIVARES_START=0 "$@" /bin/sh "$fx/scripts/install-agentops.sh" > "$fx/out" 2> "$fx/err"
  got=$?
  set -e
  if [[ "$got" != "$want" ]]; then
    cat "$fx/out" "$fx/err" >&2
    fail "$label: wanted rc=$want, got rc=$got"
  fi
  [[ -z "$(find "$fx/tmp" -mindepth 1 -print -quit)" ]] || fail "$label: temporary files leaked"
  if [[ "$want" = 0 ]]; then
    grep -q 'native co-deployment wired' "$fx/out" || fail "$label: missing success banner"
  else
    if grep -q 'native co-deployment wired' "$fx/out"; then fail "$label: false success banner"; fi
  fi
  case_ok "$label"
}
unmap() { sed "s|$fx||g" "$1"; }
dropin() { printf '%s\n' "$fx/etc/systemd/system/olivares.service.d/agentops.conf"; }
runtime_env() { printf '%s\n' "$fx/etc/olivares/agentops.env"; }
# check_golden <name> <manifest>: the annotated manifest, with the fixture
# namespace removed, must equal the golden the Go consumer loads in its tests.
check_golden() {
  local name="$1" manifest="$2"
  unmap "$manifest" > "$fx/$name"
  if [[ "${OLIVARES_UPDATE_GOLDEN:-0}" = 1 ]]; then cp "$fx/$name" "$golden_dir/$name"; fi
  cmp "$fx/$name" "$golden_dir/$name" || { diff -u "$golden_dir/$name" "$fx/$name" >&2 || true; fail "annotated manifest differs from golden $name"; }
}

for start in 0 1; do
  fixture "fresh-$start"
  run_case 0 "fresh default install, START=$start" OLIVARES_START="$start"
  printf '%s\n' --system --init systemd --data-dir "$fx/var/lib/olivares" > "$fx/expected-argv"
  cmp "$fx/expected-argv" "$fx/installer-argv" || fail 'installer arguments changed or implicit --start added'
  if [[ "$start" = 0 ]]; then
    if grep -q '^systemctl enable' "$fx/events"; then fail 'START=0 started service'; fi
  else
    grep -qx 'systemctl enable --now olivares' "$fx/events" || fail 'START=1 did not start'
  fi
  python3 - "$fx/events" <<'PY'
import pathlib
import sys
lines = pathlib.Path(sys.argv[1]).read_text().splitlines()
installed = lines.index('verified-installer')
dropin = next(i for i, line in enumerate(lines) if line.startswith('install -m 0644 ') and line.endswith('/agentops.conf'))
assert installed < dropin
for i, line in enumerate(lines):
    if line.startswith('systemctl enable'):
        assert dropin < i
PY
done
# The default render is the shipped default drop-in, byte for byte (modulo namespace).
unmap "$(dropin)" | cmp - "$root/packaging/systemd/olivares.service.d/agentops.conf" ||
  fail 'default render of packaging/service/agentops.conf differs from the shipped packaging/systemd drop-in'
case_ok 'default drop-in render equals the shipped default drop-in'
grep -Fqx '# Generated by install-agentops.sh from packaging/olivares-agentops.env.example; preserved on reinstall.' "$(runtime_env)" || fail 'runtime env lacks its generated marker'
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$fx/var/lib/olivares/run/session-token" "$(runtime_env)" || fail 'default runtime env token path not under the data dir'
for created in "$fx/var/lib/olivares/claude-home" "$fx/var/lib/olivares/run" "$fx/var/lib/olivares/workspaces"; do
  [[ -d "$created" ]] || fail "owned directory not provisioned: $created"
  grep -Fqx "chown -h olivares:olivares $created" "$fx/events" || fail "owned directory not chowned: $created"
done
[[ "$(stat -c '%a' "$fx/var/lib/olivares/run")" = 700 && "$(stat -c '%a' "$fx/var/lib/olivares/claude-home")" = 750 ]] || fail 'owned directory modes drifted'
if grep -Fqx "chown -h olivares:olivares $fx/var/lib/olivares" "$fx/events"; then fail 'the adapter-vetted data dir was re-owned'; fi
check_golden manifest-default.json "$fx/var/lib/olivares/install-manifest.json"
case_ok 'default layout: env, owned directories and manifest annotation'

# WHERE the fixture lives is not ours to choose: on a GitHub runner this scratch is under
# /home/runner, and a data root can be anywhere an operator staged it. So the prefix itself
# carries mapped roots, and mapping them one root at a time rewrites the prefix already
# inserted by an earlier one -- every path the installer then writes carries the prefix
# twice, or under the wrong root, and the installer's own fail-closed unit check refuses the
# co-deployment it just wired (mainline-ci run 34286700558, job 102263870572, where the unit
# went to $fx$fx/etc/systemd/system/olivares.service). Both fixtures below are the ordinary
# default install of the first case; the only thing under test is the prefix they live under.
# The assertions are on the mapped text BEFORE running it, so a doubled root is named where
# it happens rather than diagnosed from a refusal downstream, and on the artifacts after.
for mapped_prefix in home/runner/work/mapped-home var/lib/olivares-fixtures/mapped-data-root; do
  fixture "$mapped_prefix"
  mapped="$fx/scripts/install-agentops.sh"
  if grep -Fq "$fx$fx" "$mapped"; then fail "prefix $mapped_prefix: a mapped root was expanded twice"; fi
  for assignment in \
    "NATIVE_SERVICE_UNIT=$fx/etc/systemd/system/olivares.service" \
    "NATIVE_DROPIN=$fx/etc/systemd/system/olivares.service.d/agentops.conf" \
    "NATIVE_RUNTIME_ENV=$fx/etc/olivares/agentops.env"; do
    grep -Fqx "$assignment" "$mapped" || fail "prefix $mapped_prefix: $assignment is not the mapped path"
  done
  grep -Fq "DATA_DIR=$fx/var/lib/olivares" "$mapped" || fail "prefix $mapped_prefix: the default data root is not mapped once"
  # The prefix's own /home stays literal: only the installer's own guard is namespaced, and
  # /root and /run/user are deliberately not mapped at all.
  grep -Fq "$fx/home|$fx/home/*|/root|/root/*|/run/user|/run/user/*)" "$mapped" ||
    fail "prefix $mapped_prefix: the protected-home guard is not mapped once"
  case_ok "prefix $mapped_prefix: every mapped root is expanded exactly once"
  run_case 0 "prefix $mapped_prefix: the default install runs at its single-prefix paths"
  [[ -f "$fx/etc/systemd/system/olivares.service" ]] || fail "prefix $mapped_prefix: the unit is not at the single-prefix path"
  [[ -f "$(dropin)" && -f "$(runtime_env)" ]] || fail "prefix $mapped_prefix: drop-in or runtime env is not at the single-prefix path"
  grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$fx/var/lib/olivares/run/session-token" "$(runtime_env)" ||
    fail "prefix $mapped_prefix: the generated token path is not under the mapped data root"
  check_golden manifest-default.json "$fx/var/lib/olivares/install-manifest.json"
  case_ok "prefix $mapped_prefix: unit, drop-in, runtime env and manifest land under one prefix"
done

fixture custom-fresh
mkdir -p "$fx/mnt/shared"
chmod 0770 "$fx/mnt/shared"
run_case 0 'fresh custom data dir with an absent external workspace' \
  OLIVARES_DATA_DIR="$fx/srv/olivares" OLIVARES_WORKSPACE_DIR="$fx/mnt/workspaces"
grep -Fxq "$fx/srv/olivares" "$fx/installer-argv" || fail 'custom data dir did not reach the signed adapter'
grep -Fqx "Environment=HOME=$fx/srv/olivares/claude-home" "$(dropin)" || fail 'drop-in HOME not under the custom data dir'
grep -Fqx "ExecStartPre=/usr/bin/install -d -m 0700 $fx/srv/olivares/run" "$(dropin)" || fail 'drop-in token dir not under the custom data dir'
grep -Fqx "ReadWritePaths=$fx/mnt/workspaces" "$(dropin)" || fail 'external workspace not granted ReadWritePaths (unprefixed)'
if grep -q "ExecStartPre=.*workspaces" "$(dropin)"; then fail 'external workspace is re-moded at start'; fi
grep -Fq "explicitly selected external directory" "$(dropin)" || fail 'external workspace not disclosed in the drop-in'
if grep -q '/var/lib/olivares' "$(dropin)"; then fail 'custom drop-in still carries the default data dir'; fi
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$fx/srv/olivares/run/session-token" "$(runtime_env)" || fail 'runtime env token path not under the custom data dir'
[[ -d "$fx/mnt/workspaces" ]] || fail 'absent external workspace was not created'
grep -Fqx "chown -h olivares:olivares $fx/mnt/workspaces" "$fx/events" || fail 'created external workspace not owned by the service account'
grep -q 'created the external workspace' "$fx/out" || fail 'workspace creation not disclosed'
check_golden manifest-custom.json "$fx/srv/olivares/install-manifest.json"
grep -Fq '"layout": "custom"' "$fx/srv/olivares/install-manifest.json" || fail 'adapter layout marker lost by annotation'
case_ok 'custom layout renders drop-in, env, workspace and manifest coherently'

# Reinstall with no knobs: the layout is recovered from the unit and manifest.
before_dropin="$(cat "$(dropin)")"
before_env="$(cat "$(runtime_env)")"
before_manifest="$(cat "$fx/srv/olivares/install-manifest.json")"
run_case 0 'reinstall without knobs recovers the recorded custom layout'
if grep -q '^verified-installer$' "$fx/events"; then fail 'installed service unnecessarily reinstalled'; fi
grep -Fq "data=$fx/srv/olivares (recorded by $fx/etc/systemd/system/olivares.service) workspace=$fx/mnt/workspaces (external, recorded by $fx/srv/olivares/install-manifest.json" "$fx/out" || fail 'recorded layout sources not disclosed truthfully'
[[ "$(cat "$(dropin)")" = "$before_dropin" ]] || fail 'reinstall changed the managed drop-in'
grep -q 'already matches the layout' "$fx/out" || fail 'idempotent drop-in not reported'
[[ "$(cat "$(runtime_env)")" = "$before_env" ]] || fail 'reinstall rewrote the operator runtime env'
[[ "$(cat "$fx/srv/olivares/install-manifest.json")" = "$before_manifest" ]] || fail 'reinstall changed the annotated manifest'
if grep -q "install -m 0640 .*agentops.env" "$fx/events"; then fail 'runtime env reinstalled over operator content'; fi
if grep -Fqx "chown -h olivares:olivares $fx/mnt/workspaces" "$fx/events"; then fail 'existing external workspace re-owned on reinstall'; fi

run_case 1 'contradictory OLIVARES_DATA_DIR against the installed unit is refused' OLIVARES_DATA_DIR="$fx/srv/other"
grep -q 'does not relocate an installed service' "$fx/err" || fail 'relocation refusal not explained'
if grep -q '^install \|^sudo sh ' "$fx/events"; then fail 'refused relocation still provisioned'; fi
[[ "$(cat "$(dropin)")" = "$before_dropin" ]] || fail 'refused relocation touched the drop-in'

run_case 0 'reselecting the workspace regenerates only the managed drop-in' OLIVARES_WORKSPACE_DIR="$fx/mnt/shared"
grep -q 'regenerating the managed drop-in' "$fx/out" || fail 'drop-in regeneration not reported'
grep -Fqx "ReadWritePaths=$fx/mnt/shared" "$(dropin)" || fail 'reselected workspace not rendered'
grep -Fq "\"workspace_dir\": \"$fx/mnt/shared\"" "$fx/srv/olivares/install-manifest.json" || fail 'reselected workspace not recorded'
[[ "$(grep -c '"role": "runtime-env"' "$fx/srv/olivares/install-manifest.json")" = 1 ]] || fail 'annotation duplicated runtime-env entries'
[[ "$(grep -c '"role": "dropin"' "$fx/srv/olivares/install-manifest.json")" = 1 ]] || fail 'annotation duplicated dropin entries'
[[ "$(stat -c '%a' "$fx/mnt/shared")" = 770 ]] || fail 'existing external workspace mode was changed'
if grep -Fqx "chown -h olivares:olivares $fx/mnt/shared" "$fx/events"; then fail 'existing external workspace was re-owned'; fi
grep -q 'left exactly as found' "$fx/err" || fail 'untouched external workspace ownership not disclosed'
[[ "$(cat "$(runtime_env)")" = "$before_env" ]] || fail 'workspace change rewrote the runtime env'

# F2: custom install -> engine upgrade through the signed adapter (its manifest rewrite
# carries the AgentOps records) -> no-knob rerun keeps the external workspace and roles.
fixture upgrade-sequence
run_case 0 'upgrade sequence step 1: fresh custom install with an external workspace' \
  OLIVARES_DATA_DIR="$fx/srv/olivares" OLIVARES_WORKSPACE_DIR="$fx/mnt/workspaces"
PROBE_ROOT="$fx" /bin/sh "$fx/verified-installer-port.sh" --system --init systemd --data-dir "$fx/srv/olivares" >/dev/null
grep -Fq "\"workspace_dir\": \"$fx/mnt/workspaces\"" "$fx/srv/olivares/install-manifest.json" || fail 'adapter rerun dropped the recorded workspace'
[[ "$(grep -o '"role": "[a-z-]*"' "$fx/srv/olivares/install-manifest.json" | tr '\n' ' ')" = '"role": "binary" "role": "config" "role": "unit" "role": "dropin" "role": "runtime-env" ' ]] || fail 'adapter rerun changed the recorded roles'
case_ok 'upgrade sequence step 2: adapter rerun keeps workspace and AgentOps roles'
check_golden manifest-custom-upgraded.json "$fx/srv/olivares/install-manifest.json"
before_dropin="$(cat "$(dropin)")"
run_case 0 'upgrade sequence step 3: no-knob rerun keeps the external workspace'
grep -Fq "workspace=$fx/mnt/workspaces (external, recorded by $fx/srv/olivares/install-manifest.json" "$fx/out" || fail 'workspace source misreported after upgrade'
grep -Fqx "ReadWritePaths=$fx/mnt/workspaces" "$(dropin)" || fail 'external workspace lost after upgrade'
[[ "$(cat "$(dropin)")" = "$before_dropin" ]] || fail 'drop-in changed across the upgrade'
grep -Fq "\"workspace_dir\": \"$fx/mnt/workspaces\"" "$fx/srv/olivares/install-manifest.json" || fail 'workspace reset after upgrade'
[[ "$(grep -c '"role": "dropin"' "$fx/srv/olivares/install-manifest.json")" = 1 && "$(grep -c '"role": "runtime-env"' "$fx/srv/olivares/install-manifest.json")" = 1 ]] || fail 'roles duplicated or lost after upgrade'

# F3: the installed unit is read by its ExecStart= command line only.
fixture decoy-comment 1 1
printf '# --data-dir=%s/srv/decoy is what we would like\n[Service]\nEnvironment=OLIVARES_HINT=--data-dir=%s/srv/decoy2\nExecStartPre=/usr/local/bin/olivares check --data-dir=%s/srv/decoy3\nExecStart=/usr/local/bin/olivares serve \\\n  --data-dir=%s/var/lib/olivares \\\n  --listen=127.0.0.1:8443\n' "$fx" "$fx" "$fx" "$fx" > "$fx/etc/systemd/system/olivares.service"
run_case 0 'decoy mentions in comments and other directives never select the data dir'
grep -Fq "data=$fx/var/lib/olivares (recorded by" "$fx/out" || fail 'decoy or continuation line misread'
grep -Fqx "Environment=HOME=$fx/var/lib/olivares/claude-home" "$(dropin)" || fail 'drop-in rendered for a decoy data dir'
if grep -Fq "$fx/srv/decoy" "$(dropin)"; then fail 'decoy path reached the drop-in'; fi

fixture ambiguous-unit 1 1
printf '[Service]\nExecStart=/usr/local/bin/olivares serve --data-dir=%s/srv/a\nExecStart=/usr/local/bin/olivares serve --data-dir=%s/srv/b\n' "$fx" "$fx" > "$fx/etc/systemd/system/olivares.service"
run_case 1 'a unit naming several data directories is refused as ambiguous'
grep -q 'ambiguous about the data directory' "$fx/err" || fail 'ambiguity not named'
if grep -q '^sudo ' "$fx/events"; then fail 'ambiguous unit still crossed the privilege boundary'; fi

# Only the directive systemd RUNS, running the engine, is evidence about the layout.
# These two bodies are byte-for-byte the ones the independent review of 2026-09-05
# reproduced against this reader; both were accepted and both named /srv/olivares.
fixture wrong-section-unit 1 1
printf '[Unit]\nExecStart=/usr/local/bin/olivares serve --data-dir=/srv/olivares\n[Service]\nExecStart=/usr/local/bin/olivares serve\n' \
  > "$fx/etc/systemd/system/olivares.service"
run_case 1 'an ExecStart= outside [Service] never witnesses the layout'
grep -q 'outside \[Service\] is inert text' "$fx/err" || fail 'inert section not named'
if grep -q '^sudo ' "$fx/events"; then fail 'a unit with no effective invocation still crossed the privilege boundary'; fi
[[ ! -e /srv/olivares ]] || fail 'a refused unit still selected a host data directory'
[[ ! -e "$(dropin)" ]] || fail 'drop-in rendered for a unit that witnesses nothing'

fixture different-program-unit 1 1
printf '[Service]\nExecStart=/bin/echo --data-dir=/srv/olivares\n' > "$fx/etc/systemd/system/olivares.service"
run_case 1 "another program's command line never witnesses the layout"
grep -q "that program's command line" "$fx/err" || fail 'foreign program not named'
if grep -q '^sudo ' "$fx/events"; then fail 'a unit running another program still crossed the privilege boundary'; fi
[[ ! -e /srv/olivares ]] || fail 'a refused unit still selected a host data directory'
[[ ! -e "$(dropin)" ]] || fail 'drop-in rendered for another program'

# The positive control the two above must not break: a legitimate unit whose engine
# lives at a non-default absolute path is still read (the AgentOps reader tests the
# engine's NAME, because it has no ownership record to compare a path against).
fixture engine-elsewhere 1 1
printf '[Service]\nExecStart=/opt/olivares/bin/olivares serve --data-dir=%s/srv/olivares --listen=127.0.0.1:8443\n' "$fx" \
  > "$fx/etc/systemd/system/olivares.service"
rm "$fx/var/lib/olivares/install-manifest.json"
run_case 0 'an engine installed at another absolute path still witnesses its layout'
grep -Fq "data=$fx/srv/olivares (recorded by" "$fx/out" || fail 'non-default engine path was not honoured'
grep -Fqx "Environment=HOME=$fx/srv/olivares/claude-home" "$(dropin)" || fail 'drop-in not rendered for the witnessed layout'

# Sandbox reach of the selected workspace.
fixture protected-home 1 1
mkdir -p "$fx/home/operator"
run_case 0 'workspace under a ProtectHome path renders ProtectHome=tmpfs and BindPaths' \
  OLIVARES_WORKSPACE_DIR="$fx/home/operator/workspaces"
[[ -d "$fx/home/operator/workspaces" ]] || fail 'protected-home workspace not created for the service'
grep -Fqx 'ProtectHome=tmpfs' "$(dropin)" || fail 'ProtectHome=tmpfs not rendered'
grep -Fqx "BindPaths=$fx/home/operator/workspaces" "$(dropin)" || fail 'BindPaths not rendered'
grep -Fqx "ReadWritePaths=$fx/home/operator/workspaces" "$(dropin)" || fail 'ReadWritePaths not rendered'
grep -q 'sandbox access: protected-home' "$fx/out" || fail 'protected-home access not disclosed'
grep -q 'other home directories stay hidden' "$fx/out" || fail 'scoped relaxation not disclosed'
if grep -qE '^(ProtectHome|BindPaths)=' "$root/packaging/systemd/olivares.service.d/agentops.conf"; then fail 'default drop-in gained a ProtectHome or BindPaths directive'; fi

# R5. A workspace under /tmp keeps PrivateTmp=true and is bound in on its own. It is
# staged inside the fixture's own /tmp so nothing is created on the host's.
fixture private-tmp 1 1
mkdir -p "$fx/tmp-host"
run_case 0 'workspace under a private temporary directory keeps PrivateTmp and binds only itself' \
  OLIVARES_WORKSPACE_DIR="$fx/tmp-host/olivares-ws"
[[ -d "$fx/tmp-host/olivares-ws" ]] || fail 'private-tmp workspace not created for the service'
grep -Fqx "BindPaths=$fx/tmp-host/olivares-ws" "$(dropin)" || fail 'BindPaths not rendered for the /tmp workspace'
grep -Fqx "ReadWritePaths=$fx/tmp-host/olivares-ws" "$(dropin)" || fail 'ReadWritePaths not rendered'
if grep -q '^ProtectHome=' "$(dropin)"; then fail 'the /tmp mapping relaxed ProtectHome'; fi
grep -q 'sandbox access: private-tmp' "$fx/out" || fail 'private-tmp access not disclosed'
grep -q 'stays hidden from the service' "$fx/out" || fail 'scoped relaxation not disclosed'
grep -q 'needs systemd 235 or later' "$fx/out" || fail 'the version the mapping needs is not disclosed'
grep -q 'clear them on boot or on a timer' "$fx/err" || fail 'shared temporary directory caveat not disclosed'
if grep -qE '^(ProtectHome|BindPaths)=' "$root/packaging/systemd/olivares.service.d/agentops.conf"; then fail 'default drop-in gained a ProtectHome or BindPaths directive'; fi

fixture private-tmp-colon 1 1
mkdir -p "$fx/var-tmp-host"
run_case 1 'a colon in a bind-exposed workspace is refused' OLIVARES_WORKSPACE_DIR="$fx/var-tmp-host/olivares:ws"
grep -q 'separate source from destination' "$fx/err" || fail 'the BindPaths separator is not named'
[[ ! -e "$fx/var-tmp-host/olivares:ws" ]] || fail 'refused workspace was created'
[[ ! -e "$(dropin)" ]] || fail 'drop-in rendered for a refused workspace'

fixture api-fs 1 1
run_case 1 'workspace under /proc is refused as an API file system' OLIVARES_WORKSPACE_DIR=/proc/olivares
grep -q 'API file system' "$fx/err" || fail 'API file system limitation not named'
if grep -q '^install \|^sudo sh \|^chown ' "$fx/events"; then fail 'refused workspace still provisioned or rendered'; fi
[[ ! -e "$(dropin)" ]] || fail 'drop-in rendered for a refused workspace'

fixture custom-space
run_case 0 'data dir with a space is quoted in the drop-in and env' OLIVARES_DATA_DIR="$fx/srv/olivares data"
grep -Fxq "$fx/srv/olivares data" "$fx/installer-argv" || fail 'spaced data dir was not forwarded verbatim'
grep -Fqx "Environment=HOME=\"$fx/srv/olivares data/claude-home\"" "$(dropin)" || fail 'spaced HOME not quoted'
grep -Fqx "ReadWritePaths=-\"$fx/srv/olivares data/workspaces\"" "$(dropin)" || fail 'spaced default workspace not quoted'
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=\"$fx/srv/olivares data/run/session-token\"" "$(runtime_env)" || fail 'spaced token path not quoted'
before_dropin="$(cat "$(dropin)")"
run_case 0 'reinstall recovers a quoted data dir from the unit'
[[ "$(cat "$(dropin)")" = "$before_dropin" ]] || fail 'quoted data dir was not recovered identically'
grep -q 'already matches the layout' "$fx/out" || fail 'quoted layout not recognised as idempotent'

fixture operator-dropin 1 1
mkdir -p "$fx/etc/systemd/system/olivares.service.d"
printf '[Service]\nEnvironment=HOME=/home/operator/claude\n' > "$(dropin)"
run_case 0 'operator-owned drop-in is left untouched'
[[ "$(cat "$(dropin)")" = $'[Service]\nEnvironment=HOME=/home/operator/claude' ]] || fail 'operator drop-in was overwritten'
grep -q 'not managed by this installer' "$fx/err" || fail 'operator drop-in not disclosed'
grep -q 'does not reference' "$fx/err" || fail 'operator drop-in layout mismatch not disclosed'
# NEGATIVE CONTROL for the legacy fingerprint below: an operator's own file must never be
# claimed as OUR earlier artifact. The recognition is worth having only if it discriminates,
# and "left untouched" is true of both cases, so the untouched bytes cannot show the
# difference -- only the absence of the legacy sentence can.
if grep -q 'shipped by an earlier installer' "$fx/err"; then fail 'a foreign drop-in was claimed as one of ours'; fi
if grep -q '"role": "dropin"' "$fx/var/lib/olivares/install-manifest.json"; then fail 'operator drop-in was recorded as managed'; fi
grep -q '"role": "runtime-env"' "$fx/var/lib/olivares/install-manifest.json" || fail 'runtime env not recorded'

fixture legacy-dropin 1 1
mkdir -p "$fx/etc/systemd/system/olivares.service.d"
printf '# "Operate Claude Code" drop-in for the hardened olivares.service (FASE V, S183).\n[Service]\nEnvironment=HOME=/var/lib/olivares/claude-home\n' > "$(dropin)"
run_case 0 'drop-in shipped by an earlier installer is recognised and left untouched'
grep -q 'shipped by an earlier installer' "$fx/err" || fail 'legacy drop-in not recognised'
grep -Fq 'Environment=HOME=/var/lib/olivares/claude-home' "$(dropin)" || fail 'legacy drop-in was overwritten'

# The fixture line above is written out HERE, independently of the installer's own constant,
# and that is the point: it is the verbatim first content line of the drop-in this project
# shipped before drop-ins carried a managed marker: line 4 of
# packaging/systemd/olivares.service.d/agentops.conf at 51b9acb52b^, byte for byte. If the
# constant is ever rewritten -- the tempting way to answer the export's leak gate -- this case
# turns red instead of the pair agreeing with itself while every installed estate goes
# unrecognised. The export exception that keeps both copies exact is recorded, with its
# bounded scope, in scripts/export-scrub/allow-strings.txt.
#
# And the recognition is EXACT, not a prefix or a keyword: a drop-in whose first line carries
# the same prose but a different parenthetical is a DIFFERENT artifact, so it gets the generic
# unmanaged treatment. Without this case, widening the match to the quotable prose -- which
# would silence the gate just as well -- passes every other assertion in this file.
fixture near-miss-dropin 1 1
mkdir -p "$fx/etc/systemd/system/olivares.service.d"
printf '# "Operate Claude Code" drop-in for the hardened olivares.service (FASE VI).\n[Service]\nEnvironment=HOME=/var/lib/olivares/claude-home\n' > "$(dropin)"
run_case 0 'a drop-in that only resembles the legacy one is not claimed as ours'
if grep -q 'shipped by an earlier installer' "$fx/err"; then fail 'a near-miss drop-in was recognised as the legacy artifact'; fi
grep -q 'not managed by this installer' "$fx/err" || fail 'near-miss drop-in not disclosed as unmanaged'
grep -Fq 'Environment=HOME=/var/lib/olivares/claude-home' "$(dropin)" || fail 'near-miss drop-in was overwritten'

fixture operator-unit 1 1
rm "$fx/var/lib/olivares/install-manifest.json"
printf '[Service]\nExecStart=/opt/olivares/bin/olivares serve --data-dir=%s/srv/olivares --listen=127.0.0.1:8443\n' "$fx" > "$fx/etc/systemd/system/olivares.service"
run_case 0 'operator-managed unit without a manifest: layout from the unit, no record invented' \
  OLIVARES_WORKSPACE_DIR="$fx/mnt/external"
if grep -q '^verified-installer$' "$fx/events"; then fail 'existing unit unnecessarily reinstalled'; fi
grep -Fqx "Environment=HOME=$fx/srv/olivares/claude-home" "$(dropin)" || fail 'unit data dir not honoured'
grep -q 'no ownership manifest' "$fx/err" || fail 'missing manifest not disclosed'
[[ ! -e "$fx/srv/olivares/install-manifest.json" ]] || fail 'a manifest was invented'
[[ -d "$fx/srv/olivares/claude-home" ]] || fail 'claude HOME not provisioned under the unit data dir'

# R4 on this side of the same record. install-agentops.sh is the OTHER producer of the
# ownership manifest, so it reads it as JSON too: a compact document must yield the same
# recorded workspace as the presentation the adapter writes.
fixture compact-manifest 1 1
python3 - "$fx/var/lib/olivares/install-manifest.json" "$fx/mnt/workspaces" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
d = json.loads(p.read_text(encoding="utf-8"))
d["workspace_dir"] = sys.argv[2]
p.write_text(json.dumps(d, separators=(",", ":")), encoding="utf-8")
PY
mkdir -p "$fx/mnt/workspaces"
run_case 0 'a compact ownership record still selects the recorded external workspace'
grep -Fq "workspace=$fx/mnt/workspaces (external, recorded by $fx/var/lib/olivares/install-manifest.json" "$fx/out" ||
  fail 'a compact record was read line-wise and the workspace defaulted'
grep -Fqx "ReadWritePaths=$fx/mnt/workspaces" "$(dropin)" || fail 'drop-in rendered for a defaulted workspace'
grep -Fq "\"workspace_dir\": \"$fx/mnt/workspaces\"" "$fx/var/lib/olivares/install-manifest.json" ||
  fail 'the annotated record lost the workspace'

fixture unreadable-manifest 1 1
printf '{"schema": "olivares.ai/local-install/v2", "schema": "twice"}\n' > "$fx/var/lib/olivares/install-manifest.json"
run_case 1 'an unreadable ownership record is named, not silently ignored'
grep -q 'not a readable ownership record' "$fx/err" || fail 'the unreadable record is not named'
if grep -q '^sudo install ' "$fx/events"; then fail 'a refused record still reached the privilege boundary'; fi

# The annotated record is read back and required to say what this run promised, so a
# layout is never ANNOUNCED as recorded when it was not written. The guard is exercised
# by breaking the emitter itself: a mutant that drops the workspace it just selected.
fixture unannotatable-manifest 1 1
mkdir -p "$fx/mnt/workspaces"
python3 - "$fx/scripts/install-agentops.sh" <<'PY'
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
lines = path.read_text().splitlines(keepends=True)
row = [line for line in lines if 'printf("  ' in line and 'workspace_dir' in line]
assert len(row) == 1, row
path.write_text(''.join(line for line in lines if line != row[0]))
PY
before_manifest="$(cat "$fx/var/lib/olivares/install-manifest.json")"
run_case 1 'mutant: an annotation that lost the workspace is refused, not announced' \
  OLIVARES_WORKSPACE_DIR="$fx/mnt/workspaces"
grep -q 'could not record its layout' "$fx/err" || fail 'the failed annotation is not named'
grep -q 'does not carry workspace_dir' "$fx/err" || fail 'the guard does not say what was missing'
[[ "$(cat "$fx/var/lib/olivares/install-manifest.json")" = "$before_manifest" ]] || fail 'the previous record was replaced anyway'
if grep -q 'native co-deployment wired' "$fx/out"; then fail 'false success banner over an unrecorded layout'; fi

fixture rejected-manifest 1 1
printf '2\n' > "$fx/plan-rc"
before_manifest="$(cat "$fx/var/lib/olivares/install-manifest.json")"
run_case 1 'engine rejection of the annotated manifest restores the previous record'
[[ "$(cat "$fx/var/lib/olivares/install-manifest.json")" = "$before_manifest" ]] || fail 'rejected manifest was left in place'
grep -q 'previous ownership record was restored' "$fx/err" || fail 'restoration not disclosed'
grep -q 'drop-in and runtime env written above remain in place' "$fx/err" || fail 'partial rollback scope not disclosed'
grep -q 'manifest rejected' "$fx/err" || fail 'engine diagnostic not relayed'

fixture link-component 1 1
mkdir -p "$fx/target"
chmod 0700 "$fx/target"
ln -s "$fx/target" "$fx/linked"
run_case 3 'symlink component in a workspace path is refused before provisioning through it' \
  OLIVARES_WORKSPACE_DIR="$fx/linked/ws"
grep -q 'symbolic link' "$fx/err" || fail 'link component not named'
[[ "$(stat -c '%a' "$fx/target")" = 700 ]] || fail 'link target mode changed'
[[ ! -e "$fx/target/ws" ]] || fail 'workspace was created through the link'
if grep -q "chown .*$fx/target" "$fx/events"; then fail 'link target was re-owned'; fi
if [[ -e "$(dropin)" ]]; then fail 'drop-in rendered for a refused layout'; fi

fixture link-data 1 1
mkdir -p "$fx/elsewhere"
chmod 0755 "$fx/elsewhere"
rm -r "$fx/var/lib/olivares/claude-home" 2>/dev/null || true
ln -s "$fx/elsewhere" "$fx/var/lib/olivares/claude-home"
run_case 3 'symlink planted inside the data dir is refused before chown/chmod'
[[ "$(stat -c '%a' "$fx/elsewhere")" = 755 ]] || fail 'planted link target mode changed'
if grep -q "chown .*$fx/elsewhere\|chown .*claude-home" "$fx/events"; then fail 'planted link target was re-owned'; fi

fixture traversal
run_case 1 'traversal components are refused before any privileged step' OLIVARES_WORKSPACE_DIR="$fx/mnt/../etc"
grep -q 'canonical' "$fx/err" || fail 'traversal refusal not explained'
if grep -q '^sudo ' "$fx/events"; then fail 'traversal refusal crossed the privilege boundary'; fi

fixture shallow
run_case 1 'top-level data directory is refused before any privileged step' OLIVARES_DATA_DIR=/srv
grep -q 'two levels deep' "$fx/err" || fail 'depth refusal not explained'
if grep -q '^sudo ' "$fx/events"; then fail 'depth refusal crossed the privilege boundary'; fi

fixture unsafe-char
run_case 1 'unit-unsafe characters are refused' OLIVARES_WORKSPACE_DIR="$fx/mnt/ws%i"
grep -q 'unsafe for a service definition' "$fx/err" || fail 'unsafe character refusal not explained'

fixture repair 1 0
run_case 0 'present binary without unit reruns verifying installer'
grep -qx 'verified-installer' "$fx/events" || fail 'repair skipped installer'
grep -q 'reinstalls the engine too' "$fx/out" || fail 'repair replacement behavior is not disclosed'

fixture custom-new
printf '48\n' > "$fx/installer-rc"
run_case 48 'new custom layout reaches adapter and preserves its refusal' OLIVARES_DATA_DIR="$fx/srv/new-layout"
grep -Fxq "$fx/srv/new-layout" "$fx/installer-argv" || fail 'custom data option was restricted or rewritten'

fixture installer-failure
printf '37\n' > "$fx/installer-rc"
run_case 37 'installer failure propagates without provisioning'
if grep -q '^install \|^sudo sh ' "$fx/events"; then fail 'provisioning continued after installer failure'; fi

for start in 0 1; do
  fixture "missing-unit-$start"
  printf '0\n' > "$fx/land-unit"
  run_case 1 "missing unit postcondition rejects success, START=$start" OLIVARES_START="$start"
done

for operation in install reload start pep; do
  fixture "failed-$operation" 1 1
  printf '55\n' > "$fx/$operation-rc"
  run_case 55 "$operation failure survives real set -eu" OLIVARES_START=1 OLIVARES_PEP_MANAGED_SETTINGS=1
done

fixture account-failure 1 1
rm "$fx/account"
run_case 1 'both service account creation commands failing prevents success'

fixture downloaded
rm "$fx/scripts/install.sh"
rm -r "$fx/packaging"
run_case 0 'downloaded bootstrap and assets are cleaned after success'
[[ "$(grep -c '^curl ' "$fx/events")" = 3 ]] || fail 'download fallbacks were not exercised'
unmap "$(dropin)" | cmp - "$root/packaging/systemd/olivares.service.d/agentops.conf" || fail 'downloaded template renders differently'

fixture download-failure
rm "$fx/scripts/install.sh"
printf '22\n' > "$fx/download-rc"
run_case 22 'bootstrap download failure propagates and cleans temporary directory'
if grep -q '^sudo ' "$fx/events"; then fail 'privilege boundary crossed after failed download'; fi

fixture download-signal
rm "$fx/scripts/install.sh"
: > "$fx/download-signal"
run_case 143 'TERM during download cleans temporary directory'

# F4: TWO ESTATES ON ONE HOST, IN SEQUENCE. /etc/olivares/agentops.env is a single fixed
# path shared by every estate, so this is the transition the independent review of
# 2026-09-05 reproduced on a real Debian 13.6 / systemd 257 host: estate A installed,
# preserved, estate B installed over it. B used to inherit A's generated token path, warn,
# return 0 and announce a wired co-deployment; purging A then deleted the file B was
# configured to read. Both installs below go through the real public entrypoint; preserve
# and purge are applied to the fixture exactly as the engine's own reviewed plan applies
# them (preserve: unit, drop-in and binary removed, data, manifest and env kept; purge of A
# while B is the live service: A's tree removed, B's files kept — R1).
estate_a=""
estate_b=""
operator_env=""
witness_before=""
# stage_ab <fixture>: estate A installed and recorded, its runtime env edited the way the
# file itself asks the operator to edit it, then estate A preserved.
stage_ab() {
  fixture "$1"
  estate_a="$fx/srv/estate-a"
  estate_b="$fx/srv/estate-b"
  run_case 0 "$1: estate A installs and generates its runtime env" \
    OLIVARES_DATA_DIR="$estate_a" OLIVARES_WORKSPACE_DIR="$fx/home/estate-a"
  grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_a/run/session-token" "$(runtime_env)" ||
    fail "$1: the generated token path is not under estate A"
  grep -Fq "\"path\": \"$(runtime_env)\", \"role\": \"runtime-env\", \"mode\": \"0640\", \"managed\": true" \
    "$estate_a/install-manifest.json" || fail "$1: estate A did not record the runtime env as managed"
  # The operator does what the generated file asks: wires the PEP endpoint, matches the
  # refresher cadence and leaves a note of their own. None of it may be lost or rewritten.
  python3 - "$(runtime_env)" <<'PY'
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
text = path.read_text()
for before, after in (
    ('OLIVARES_SESSION_RUNTIME_TOKEN_TTL=15m', 'OLIVARES_SESSION_RUNTIME_TOKEN_TTL=5m'),
    ('# OLIVARES_SESSION_PEP_URL=http://127.0.0.1:8447/', 'OLIVARES_SESSION_PEP_URL=http://127.0.0.1:8447/'),
):
    assert text.count(before) == 1, before
    text = text.replace(before, after)
path.write_text(text + '\n# operator: minted by our own WIF timer, do not touch\n')
PY
  rm "$fx/etc/systemd/system/olivares.service" "$(dropin)" "$fx/bin/olivares"
  # The real witness is hash-suffixed from the config path (R1/R2 cover its naming); what
  # this fixture asserts is only that a file this product left in /etc/olivares survives the
  # transition untouched.
  printf '{"schema": "olivares.ai/uninstall-witness/v2", "data_dir": "%s"}\n' "$estate_a" \
    > "$fx/etc/olivares/olivares-uninstall-witness-fixture.json"
  witness_before="$(cat "$fx/etc/olivares/olivares-uninstall-witness-fixture.json")"
  operator_env="$(cat "$(runtime_env)")"
}
# operator_lines <file>: everything except the one assignment this installer owns.
operator_lines() { grep -Fv 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=' "$1"; }

stage_ab estate-transition
run_case 0 'estate B over a preserved estate A re-points only the generated token path' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 're-pointed OLIVARES_SESSION_RUNTIME_TOKEN_FILE' "$fx/out" || fail 'the re-point was not disclosed'
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'estate B did not re-point the generated token path at its own estate'
# The effective lines are CAPTURED, not piped into a boolean: under pipefail `… | grep -Fq`
# closes the pipe on its first hit and the producer's SIGPIPE (141) reads exactly like absence
# (lint:sigpipe-booleans). The reader's own status stays apart from the answer: 1 is «no
# effective line» (a legitimately comment-only file), anything above 1 is a reader failure and
# is said as such instead of passing for «estate A is gone». The saved status is what keeps
# `set -e` from killing the assignment mute (lint:mute-pipefail).
effective_rc=0
effective_lines="$(grep -Ev '^[[:space:]]*[#;]' "$(runtime_env)")" || effective_rc=$?
[ "$effective_rc" -le 1 ] || fail "could not read the effective lines of the runtime env (grep exited $effective_rc)"
case "$effective_lines" in
  *"$estate_a"*) fail 'an effective assignment in the runtime env still references estate A' ;;
esac
# The rest of the file stays the operator's, which the commented example the generator wrote
# for estate A makes visible: it is preserved verbatim and named instead of rewritten.
grep -Fqx "# OLIVARES_SESSION_PEP_TOKEN_FILE=$estate_a/run/pep-token" "$(runtime_env)" ||
  fail 'a commented line of the runtime env was rewritten'
grep -Fq "still mentions $estate_a" "$fx/out" || fail 'the remaining mentions of estate A were not named'
diff <(printf '%s\n' "$operator_env" | grep -Fv 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=') \
     <(operator_lines "$(runtime_env)") || fail 'the operator content of the runtime env changed'
grep -Fqx "Environment=HOME=$estate_b/claude-home" "$(dropin)" || fail 'the drop-in is not rendered for estate B'
grep -Fqx "BindPaths=$fx/home/estate-b" "$(dropin)" || fail 'the workspace of estate B is not bound in'
grep -Fq "\"workspace_dir\": \"$fx/home/estate-b\"" "$estate_b/install-manifest.json" ||
  fail 'estate B did not record its workspace'
grep -Fq "\"path\": \"$(runtime_env)\", \"role\": \"runtime-env\", \"mode\": \"0640\", \"managed\": true" \
  "$estate_b/install-manifest.json" || fail 'estate B did not record the runtime env it now owns'
[[ "$(cat "$fx/etc/olivares/olivares-uninstall-witness-fixture.json")" = "$witness_before" ]] ||
  fail 'the uninstall witness of estate A was touched'
# Purge of estate A while estate B is the live service.
rm -r "$estate_a" "$fx/etc/olivares/olivares-uninstall-witness-fixture.json"
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'purging estate A changed what estate B loads'
[[ -d "$estate_b/run" ]] || fail 'the token directory estate B loads does not exist'
env_after_purge="$(cat "$(runtime_env)")"
run_case 0 'a rerun after the purge recovers estate B and leaves its runtime env alone'
grep -Fq "data=$estate_b (recorded by" "$fx/out" || fail 'the layout of estate B was not recovered'
[[ "$(cat "$(runtime_env)")" = "$env_after_purge" ]] || fail 'the rerun rewrote a coherent runtime env'
if grep -Fq 're-pointed' "$fx/out"; then fail 'a coherent runtime env was re-pointed again'; fi
case_ok 'sequential estates: A generated, A preserved, B re-pointed, A purged, B still coherent'

# The ownership decision is over the value SYSTEMD LOADS, not a substring of its physical
# line. These are all representations accepted by EnvironmentFile= across the supported
# systemd range. Each must prove the same A default and re-point it, while the recognizer's
# byte-offset rewrite leaves all operator content outside that one logical assignment intact.
stage_ab estate-systemd-whitespace
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
path.write_text(text.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=  "{estate}/run/session-token"  '))
PY
operator_before="$(operator_lines "$(runtime_env)")"
run_case 0 'systemd whitespace plus a quoted A default re-points by effective value' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'whitespace/quoted EnvironmentFile value was not re-pointed'
[[ "$(operator_lines "$(runtime_env)")" = "$operator_before" ]] ||
  fail 'whitespace/quoted rewrite changed bytes outside the assignment'

stage_ab estate-systemd-concatenated-quotes
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
path.write_text(text.replace(old, f"OLIVARES_SESSION_RUNTIME_TOKEN_FILE='{estate}'/run/session-token"))
PY
run_case 0 'systemd concatenated quoted and unquoted segments resolve to the A default' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'concatenated EnvironmentFile value was not re-pointed'

stage_ab estate-systemd-unquoted-escapes
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
encoded = (estate + '/run/session-token').replace('/', r'\/')
path.write_text(text.replace(old, 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=' + encoded))
PY
run_case 0 'systemd unquoted escapes resolve to the A default' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'unquoted escaped EnvironmentFile value was not re-pointed'

stage_ab estate-systemd-continuation
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
path.write_text(text.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/\\\nsession-token'))
PY
run_case 0 'systemd continuation resolves one logical A assignment and removes it wholly' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'continued EnvironmentFile value was not re-pointed'
if grep -Fqx 'session-token' "$(runtime_env)"; then fail 'continued assignment left an orphan physical line'; fi
grep -Fqx '# operator: minted by our own WIF timer, do not touch' "$(runtime_env)" ||
  fail 'continued assignment rewrite lost the operator note'

stage_ab estate-systemd-crlf
python3 - "$(runtime_env)" "$estate_a" "$estate_b" "$fx/expected-crlf.env" <<'PY'
import pathlib, sys
path, estate_a, estate_b, expected = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], pathlib.Path(sys.argv[4])
raw = path.read_bytes().replace(b'\n', b'\r\n')
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_a}/run/session-token'.encode()
new = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_b}/run/session-token'.encode()
assert raw.count(old) == 1
path.write_bytes(raw)
expected.write_bytes(raw.replace(old, new))
PY
run_case 0 'explicit estate rewrites CRLF assignment while preserving every other byte' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
cmp "$fx/expected-crlf.env" "$(runtime_env)" || fail 'CRLF rewrite changed bytes outside the assignment'

# Last-wins is parsed even though two assignments deliberately make automatic ownership
# insufficient: the refusal names both the effective A value and the two assignments.
stage_ab estate-systemd-last-wins
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
text = text.replace(old, 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=/var/lib/refresher/first-token')
path.write_text(text + f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=  "{estate}/run/session-token"  \n')
PY
env_before="$(cat "$(runtime_env)")"
run_case 1 'systemd last assignment wins but two assignments remain ownership-ambiguous' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq "loads OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_a/run/session-token" "$fx/err" ||
  fail 'last-wins refusal did not name the effective A value'
grep -Fq 'assigns OLIVARES_SESSION_RUNTIME_TOKEN_FILE 2 times' "$fx/err" ||
  fail 'last-wins refusal did not name both assignments'
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'last-wins refusal changed the env'

# A newline inside a quoted value is valid EnvironmentFile syntax but cannot name one safe
# token file. It must never fall through to "external"; estate can replace the one logical
# assignment without touching the operator lines around it.
stage_ab estate-systemd-nonpath-value
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
path.write_text(text.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE="{estate}/run/\nsession-token"'))
PY
run_case 1 'a parsed systemd value that cannot name one token file is refused before external' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'contains a newline that cannot name one token file' "$fx/err" ||
  fail 'non-path value refusal did not name the newline'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'non-path value was classified external'; fi
run_case 0 'estate explicitly replaces one multiline assignment after the refusal' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'estate did not replace the multiline assignment'

# If the manager version cannot be read, only syntax whose historical meaning differs is
# unrecognised. It fails closed without guessing; explicit keep still preserves exact bytes.
stage_ab estate-systemd-version-unknown
printf '1\n' > "$fx/systemd-version-rc"
: > "$fx/systemd-version"
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
path.write_text(text.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE="{estate}/run/\\q/session-token"'))
PY
env_before="$(cat "$(runtime_env)")"
run_case 1 'version-sensitive EnvironmentFile syntax is refused when systemd is unmeasurable' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'version-sensitive EnvironmentFile syntax' "$fx/err" || fail 'unknown grammar was not named'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'unknown grammar was classified external'; fi
run_case 0 'keep explicitly preserves version-sensitive syntax byte-for-byte' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=keep
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'keep changed version-sensitive operator syntax'

# --- Byte semantics measured by the independent review of 2026-09-05 on systemd 257. -------
# Inside double quotes, "\<CR>" is a continuation only for managers before 247: v235 and v241
# eat CR and LF alike, while 247 and later eat LF only and hand the process backslash+CR. Each
# manager gets its own reading; on 257 the value is two bytes longer than a path.
stage_ab estate-systemd-quoted-escaped-cr
python3 - "$(runtime_env)" "$estate_a" "$estate_b" "$fx/expected-cr-estate.env" <<'PY'
import pathlib, sys
path, estate_a, estate_b, expected = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], pathlib.Path(sys.argv[4])
raw = path.read_bytes()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_a}/run/session-token\n'.encode()
assert raw.count(old) == 1
path.write_bytes(raw.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE="{estate_a}/run/session-token'.encode() + b'\\\r"\n'))
expected.write_bytes(raw.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_b}/run/session-token\n'.encode()))
PY
cp "$(runtime_env)" "$fx/cr-before.env"
run_case 1 'systemd 257 keeps a double-quoted escaped CR, so that value is refused as no path' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'contains a carriage-return that cannot name one token file' "$fx/err" || fail 'the kept CR was not named'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'the escaped-CR form was classified external'; fi
if grep -Fq 're-pointed' "$fx/out"; then fail 'the escaped-CR form was re-pointed as if it were the A default'; fi
cmp -s "$fx/cr-before.env" "$(runtime_env)" || fail 'the escaped-CR refusal changed the env'
[[ ! -e "$(dropin)" ]] || fail 'the escaped-CR refusal still wrote the drop-in'
run_case 0 'keep preserves the escaped-CR form byte-for-byte without claiming its value' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=keep
cmp -s "$fx/cr-before.env" "$(runtime_env)" || fail 'keep changed the escaped-CR form'
grep -Fq 'could not be represented safely' "$fx/out" || fail 'keep did not say the value is unrepresentable'
if grep -Fq 'systemd loads OLIVARES_SESSION_RUNTIME_TOKEN_FILE=' "$fx/out"; then fail 'keep claimed a loaded value'; fi
run_case 0 'estate replaces the one escaped-CR assignment and no other byte' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
cmp "$fx/expected-cr-estate.env" "$(runtime_env)" || fail 'estate changed bytes outside the escaped-CR assignment'

# The same bytes on a manager before 247 (fixture 246, the v241 rule) mean the A default: the
# CR is eaten. What tells v241 from v235 (fixture 240) is an escaped byte outside the shell
# set: v241 keeps its backslash, v235 drops it.
stage_ab estate-systemd-241-quoted-escaped-cr
printf 'systemd 246 (fixture)\n' > "$fx/systemd-version"
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
raw = path.read_bytes()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token\n'.encode()
assert raw.count(old) == 1
path.write_bytes(raw.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE="{estate}/run/session-token'.encode() + b'\\\r"\n'))
PY
operator_before="$(operator_lines "$(runtime_env)")"
run_case 0 'systemd 241 to 246 eat the same escaped CR, so the value is the A default and is re-pointed' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 're-pointed OLIVARES_SESSION_RUNTIME_TOKEN_FILE' "$fx/out" || fail 'the v241 reading did not re-point'
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'the v241 re-point was not rendered'
[[ "$(operator_lines "$(runtime_env)")" = "$operator_before" ]] || fail 'the v241 rewrite changed bytes outside the assignment'

stage_ab estate-systemd-241-quoted-escaped-byte
printf 'systemd 246 (fixture)\n' > "$fx/systemd-version"
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
encoded = (estate + '/run/session-token').replace('/', r'\/')
path.write_text(text.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE="{encoded}"'))
PY
env_before="$(cat "$(runtime_env)")"
run_case 1 'systemd 241 to 246 keep the backslash of a double-quoted escaped byte outside the shell set' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'makes systemd load OLIVARES_SESSION_RUNTIME_TOKEN_FILE in a form that must be an absolute path' "$fx/err" ||
  fail 'the value with its kept backslash was not refused as no path'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'the v241 backslash form was classified external'; fi
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'the v241 refusal changed the env'

stage_ab estate-systemd-235-quoted-escaped-byte
printf 'systemd 240 (fixture)\n' > "$fx/systemd-version"
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib, sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate}/run/session-token'
assert text.count(old) == 1
encoded = (estate + '/run/session-token').replace('/', r'\/')
path.write_text(text.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE="{encoded}"'))
PY
run_case 0 'systemd 235 to 240 drop that backslash, so the value is the A default and is re-pointed' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 're-pointed OLIVARES_SESSION_RUNTIME_TOKEN_FILE' "$fx/out" || fail 'the v235 reading did not re-point'
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'the v235 re-point was not rendered'

# A NUL byte anywhere makes systemd refuse the whole file (measured on 257: the strict unit
# fails, the EnvironmentFile=- drop-in skips the file). Nothing in it is effective, so nothing
# in it can be classified, re-pointed or announced; keep preserves the bytes and says so.
stage_ab estate-systemd-nul
python3 - "$(runtime_env)" "$fx/nul-offset" <<'PY'
import pathlib, sys
path, offset = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
data = path.read_bytes()
assert data.endswith(b'\n')
path.write_bytes(data + b'\0TRAILING=after-nul\n')
offset.write_text(str(len(data) + 1))
PY
cp "$(runtime_env)" "$fx/nul-before.env"
run_case 1 'a NUL byte in the runtime env is refused before any classification, naming its offset' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq "contains a NUL byte at offset $(cat "$fx/nul-offset")" "$fx/err" || fail 'the NUL offset was not named'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'a NUL-containing file was classified external'; fi
if grep -Fq 're-pointed' "$fx/out"; then fail 'a NUL-containing file was re-pointed'; fi
cmp -s "$fx/nul-before.env" "$(runtime_env)" || fail 'the NUL refusal changed the env'
[[ ! -e "$(dropin)" ]] || fail 'the NUL refusal still wrote the drop-in'
run_case 1 'estate cannot repair a NUL-containing file without discarding content, so it is refused too' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
grep -Fq 'contains a NUL byte' "$fx/err" || fail 'estate on a NUL-containing file did not name the NUL'
cmp -s "$fx/nul-before.env" "$(runtime_env)" || fail 'estate rewrote a NUL-containing file'
run_case 0 'keep preserves a NUL-containing file byte-for-byte and claims no wired token path' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=keep
cmp -s "$fx/nul-before.env" "$(runtime_env)" || fail 'keep changed a NUL-containing file'
grep -Fq 'no token path is wired' "$fx/err" || fail 'keep did not say that no token path is wired'
grep -Fq 'cannot load it as it stands' "$fx/err" || fail 'the banner was not qualified for an unloadable file'
if grep -Fq 'systemd loads OLIVARES_SESSION_RUNTIME_TOKEN_FILE=' "$fx/out"; then fail 'keep claimed a loaded token path'; fi

# A key or value that is not valid UTF-8 fails the whole file in every supported systemd
# (check_utf8ness_and_warn on each pushed assignment; measured on 257 with a real 0xff byte).
# Comment bytes are never pushed, so they are never validated; valid multibyte UTF-8 loads.
stage_ab estate-systemd-utf8-other-value
python3 - "$(runtime_env)" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
path.write_bytes(path.read_bytes() + b'BAD=\xff\n')
PY
cp "$(runtime_env)" "$fx/utf8-before.env"
run_case 1 'invalid UTF-8 in another assignment makes systemd refuse the file, so nothing is classified' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'whose key or value is not valid UTF-8' "$fx/err" || fail 'the invalid UTF-8 was not named'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'a file systemd refuses was classified external'; fi
if grep -Fq 're-pointed' "$fx/out"; then fail 'a file systemd refuses was re-pointed'; fi
cmp -s "$fx/utf8-before.env" "$(runtime_env)" || fail 'the UTF-8 refusal changed the env'
run_case 1 'estate cannot repair invalid UTF-8 outside the token assignment, so it is refused too' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
grep -Fq 'not valid UTF-8' "$fx/err" || fail 'estate did not name the invalid UTF-8'
cmp -s "$fx/utf8-before.env" "$(runtime_env)" || fail 'estate rewrote a file systemd refuses'
run_case 0 'keep preserves a file with invalid UTF-8 byte-for-byte and claims no wired token path' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=keep
cmp -s "$fx/utf8-before.env" "$(runtime_env)" || fail 'keep changed a file with invalid UTF-8'
grep -Fq 'no token path is wired' "$fx/err" || fail 'keep did not say that no token path is wired'

stage_ab estate-systemd-utf8-other-key
python3 - "$(runtime_env)" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
path.write_bytes(path.read_bytes() + b'B\xffD=fine\n')
PY
cp "$(runtime_env)" "$fx/utf8-before.env"
run_case 1 'invalid UTF-8 in another key is refused the same way' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'whose key or value is not valid UTF-8' "$fx/err" || fail 'the invalid key was not named'
cmp -s "$fx/utf8-before.env" "$(runtime_env)" || fail 'the invalid-key refusal changed the env'

stage_ab estate-systemd-utf8-token-value
python3 - "$(runtime_env)" "$estate_a" "$estate_b" "$fx/expected-utf8-estate.env" <<'PY'
import pathlib, sys
path, estate_a, estate_b, expected = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], pathlib.Path(sys.argv[4])
raw = path.read_bytes()
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_a}/run/session-token\n'.encode()
assert raw.count(old) == 1
path.write_bytes(raw.replace(old, old[:-1] + b'\xff\n'))
expected.write_bytes(raw.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_b}/run/session-token\n'.encode()))
PY
cp "$(runtime_env)" "$fx/utf8-before.env"
run_case 1 'invalid UTF-8 in the token assignment itself is refused with the explicit options' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE assignment whose value is not valid UTF-8' "$fx/err" ||
  fail 'the invalid token value was not named'
grep -Fq 'OLIVARES_RUNTIME_TOKEN_FILE=estate replaces that one assignment' "$fx/err" || fail 'the repair was not offered'
if grep -Fq 'deliberate external reference' "$fx/err"; then fail 'an invalid token value was classified external'; fi
cmp -s "$fx/utf8-before.env" "$(runtime_env)" || fail 'the invalid-token refusal changed the env'
run_case 0 'estate replaces the invalid token assignment, which repairs the file, and no other byte' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
cmp "$fx/expected-utf8-estate.env" "$(runtime_env)" || fail 'estate changed bytes outside the invalid token assignment'

stage_ab estate-systemd-utf8-comment-and-multibyte
python3 - "$(runtime_env)" "$estate_a" "$estate_b" "$fx/expected-utf8-ok.env" <<'PY'
import pathlib, sys
path, estate_a, estate_b, expected = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], pathlib.Path(sys.argv[4])
raw = path.read_bytes() + b'# caf\xff: a comment is never pushed, so never validated\n' + 'NOTE=café \U0001f600\n'.encode('utf-8')
old = f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_a}/run/session-token\n'.encode()
assert raw.count(old) == 1
path.write_bytes(raw)
expected.write_bytes(raw.replace(old, f'OLIVARES_SESSION_RUNTIME_TOKEN_FILE={estate_b}/run/session-token\n'.encode()))
PY
run_case 0 'invalid bytes in a comment and valid multibyte UTF-8 values load, so the A default is re-pointed' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 're-pointed OLIVARES_SESSION_RUNTIME_TOKEN_FILE' "$fx/out" || fail 'a loadable file was not re-pointed'
cmp "$fx/expected-utf8-ok.env" "$(runtime_env)" || fail 'the re-point changed bytes outside the assignment'

# Ownership, not shape: a value the operator chose inside the other estate is refused, and
# the refusal names the options instead of rewriting it.
stage_ab estate-modified-token
python3 - "$(runtime_env)" "$estate_a" <<'PY'
import pathlib
import sys
path, estate = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
before = 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=%s/run/session-token' % estate
assert text.count(before) == 1
path.write_text(text.replace(before, 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=%s/run/other-token' % estate))
PY
env_before="$(cat "$(runtime_env)")"
run_case 1 'a token path chosen by hand inside another estate is refused, never rewritten' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'is not the default this installer generates' "$fx/err" || fail 'the reason for the refusal is not named'
grep -Fq 'OLIVARES_RUNTIME_TOKEN_FILE=estate' "$fx/err" || fail 'the explicit options are not offered'
grep -Fq 'OLIVARES_RUNTIME_TOKEN_FILE=keep' "$fx/err" || fail 'preserving the path is not offered'
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'a refused transition rewrote the runtime env'
[[ ! -e "$(dropin)" ]] || fail 'a refused transition still wrote the drop-in'
if grep -Fq '"role": "runtime-env"' "$estate_b/install-manifest.json"; then
  fail 'a refused transition still annotated estate B as connected'
fi

run_case 0 'OLIVARES_RUNTIME_TOKEN_FILE=keep preserves the configured path exactly' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=keep
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'keep rewrote the runtime env'
grep -Fq 'left exactly as it is' "$fx/out" || fail 'keep was not disclosed'
grep -Fq '"role": "runtime-env"' "$estate_b/install-manifest.json" || fail 'keep did not finish the installation'

run_case 0 'OLIVARES_RUNTIME_TOKEN_FILE=estate re-points that one assignment' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'estate did not select the generated default of the selected data directory'
diff <(printf '%s\n' "$env_before" | grep -Fv 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=') \
     <(operator_lines "$(runtime_env)") || fail 'an explicit selection changed operator content'

# A deliberate external path: selected explicitly, then preserved by a knob-free rerun
# without demanding that it live under the selected data directory.
run_case 0 'an absolute OLIVARES_RUNTIME_TOKEN_FILE sets the file the refresher writes' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" \
  OLIVARES_RUNTIME_TOKEN_FILE="$fx/srv/refresher/session-token"
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$fx/srv/refresher/session-token" "$(runtime_env)" ||
  fail 'the explicitly selected external path was not set'
run_case 0 'a token path inside no estate is preserved, not forced under this one' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'deliberate external reference' "$fx/err" || fail 'the preserved external reference was not disclosed'
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$fx/srv/refresher/session-token" "$(runtime_env)" ||
  fail 'a deliberate external path was rewritten'

# No assignment at all is operator content too: preserved, and the deny-closed posture said.
python3 - "$(runtime_env)" <<'PY'
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
path.write_text('\n'.join(
    ('# ' + line) if line.startswith('OLIVARES_SESSION_RUNTIME_TOKEN_FILE=') else line
    for line in path.read_text().split('\n')))
PY
env_before="$(cat "$(runtime_env)")"
run_case 0 'a runtime env that assigns no token file is preserved and its posture named' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'assigns no OLIVARES_SESSION_RUNTIME_TOKEN_FILE' "$fx/err" || fail 'the deny-closed posture was not named'
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'an absent assignment was invented'

# A generated default whose estate is already gone: the record that would prove ownership
# no longer exists, so a stale default and a path the operator chose with the same shape are
# indistinguishable. That is asked about, never guessed at.
stage_ab estate-record-gone
rm -r "$estate_a"
env_before="$(cat "$(runtime_env)")"
run_case 1 'an estate-shaped default with no ownership record left is refused, not assumed' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'carries no ownership record' "$fx/err" || fail 'the missing record is not named'
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'a refused transition rewrote the runtime env'
run_case 0 'the same state is resolved by choosing OLIVARES_RUNTIME_TOKEN_FILE=estate' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=estate
grep -Fqx "OLIVARES_SESSION_RUNTIME_TOKEN_FILE=$estate_b/run/session-token" "$(runtime_env)" ||
  fail 'the explicit choice did not re-point the assignment'
diff <(printf '%s\n' "$env_before" | grep -Fv 'OLIVARES_SESSION_RUNTIME_TOKEN_FILE=') \
     <(operator_lines "$(runtime_env)") || fail 'the explicit choice changed operator content'

# An operator-owned file (no generated marker) is never rewritten, and a value of theirs
# that belongs to another estate is refused rather than announced as wired.
stage_ab estate-operator-owned-env
python3 - "$(runtime_env)" <<'PY'
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
lines = path.read_text().split('\n')
marker = '# Generated by install-agentops.sh from packaging/olivares-agentops.env.example; preserved on reinstall.'
assert lines.count(marker) == 1
path.write_text('\n'.join(line for line in lines if line != marker))
PY
env_before="$(cat "$(runtime_env)")"
run_case 1 'an operator-owned runtime env pointing into another estate is refused' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b"
grep -Fq 'carries no generated marker' "$fx/err" || fail 'the missing marker is not named'
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'an operator-owned runtime env was rewritten'
run_case 0 'the same operator-owned file is preserved once the operator chooses keep' \
  OLIVARES_DATA_DIR="$estate_b" OLIVARES_WORKSPACE_DIR="$fx/home/estate-b" OLIVARES_RUNTIME_TOKEN_FILE=keep
[[ "$(cat "$(runtime_env)")" = "$env_before" ]] || fail 'keep rewrote an operator-owned runtime env'
if grep -Fq '"role": "runtime-env", "mode": "0640", "managed": true' "$estate_b/install-manifest.json"; then
  fail 'an unmarked operator file was recorded as managed'
fi

fixture estate-bad-knob 1 1
run_case 1 'an OLIVARES_RUNTIME_TOKEN_FILE that is neither a mode nor an absolute path is refused' \
  OLIVARES_RUNTIME_TOKEN_FILE=run/session-token
grep -Fq 'invalid OLIVARES_RUNTIME_TOKEN_FILE' "$fx/err" || fail 'the unusable selection is not named'
if [[ -e "$(dropin)" ]]; then fail 'a refused selection still rendered the drop-in'; fi

knobs=(OLIVARES_VERSION OLIVARES_BINDIR OLIVARES_OS OLIVARES_ARCH OLIVARES_CERT_IDENTITY OLIVARES_CERT_OIDC_ISSUER OLIVARES_GITHUB_URL OLIVARES_GITHUB_API_URL)
values=('v26.9.0' '/opt/olivares/bin' 'linux' 'arm64' '^https://fixture\.invalid/release$' 'https://issuer.fixture.invalid' 'https://release.fixture.invalid' 'https://api.fixture.invalid')
assignments=()
for i in "${!knobs[@]}"; do assignments+=("${knobs[$i]}=${values[$i]}"); done
fixture forwarding
printf '%s\n' "${assignments[@]}" > "$fx/expected-env"
run_case 0 'all eight installer controls cross a clean sudo environment' "${assignments[@]}"

# A missing explicit assignment must fail despite the caller still exporting it.
for knob in "${knobs[@]}"; do
  fixture "lost-$knob"
  printf '%s\n' "${assignments[@]}" > "$fx/expected-env"
  python3 - "$fx/scripts/install-agentops.sh" "$knob" <<'PY'
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
lines = path.read_text().splitlines(keepends=True)
removed = [line for line in lines if line.lstrip().startswith(sys.argv[2] + '=')]
assert len(removed) == 1, (sys.argv[2], removed)
path.write_text(''.join(line for line in lines if line not in removed))
PY
  run_case 93 "removing explicit $knob forwarding is detected" "${assignments[@]}"
done
printf '1..%d\n' "$passes"
