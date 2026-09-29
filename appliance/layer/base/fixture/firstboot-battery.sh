#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# firstboot-battery.sh — the hosted fixture of olivares-appliance-base.
#
# It boots disposable containers of the family IMAGE was built for, Debian 13 from
# fixture/Containerfile or Fedora 44 from fixture/fedora/Containerfile, with systemd as PID 1, cloud-init reading a NoCloud seed and, where a scenario says so, the answers passed as
# a system credential; then it asserts what first boot recorded for that input, and the
# Appliance Console's selection it published (or, without the console's fields, did not). Every named
# assertion runs against evidence captured from the container and has a negative control: the
# same assertion fed a counterfeit of that evidence must fail. Before any boot it asserts that
# no login the seed declares is already a user or a group of the image: cloud-init's useradd
# refuses such a login and leaves the seed unapplied.
#
# A container is component evidence. It does not qualify an ISO, a hypervisor import, Secure
# Boot, firmware or cloud-init on a real VM; those belong to the image phase.
#
# usage: firstboot-battery.sh IMAGE EVIDENCE_DIR
#   exit 0  every assertion held and every negative control failed
#   exit 1  an assertion failed or a negative control passed
#   exit 2  the fixture could not be measured: no docker or jq, a container that did not
#           finish booting, a seed cloud-init did not apply or a credential systemd did not
#           import. An inability is never a pass.
#
# The assertions and the cleanup are reached through check, control and the EXIT trap.
# shellcheck disable=SC2317
set -euo pipefail

image=${1:?usage: firstboot-battery.sh IMAGE EVIDENCE_DIR}
evidence=${2:?usage: firstboot-battery.sh IMAGE EVIDENCE_DIR}
here=$(cd "$(dirname "$0")" && pwd)
testdata="$here/../../../answers/carriers/testdata"
hostname=olivares.example.test
failures=0
unmeasured=0
containers=()

say() { printf '%s\n' "$*"; }
cleanup() {
  local c
  for c in "${containers[@]}"; do
    docker rm --force "$c" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

for tool in docker jq; do
  command -v "$tool" >/dev/null 2>&1 || { say "UNMEASURED: $tool is not available"; exit 2; }
done
mkdir -p "$evidence"
docker image inspect --format '{{.Id}}' "$image" > "$evidence/image.id"
image_id=$(cat "$evidence/image.id")

# ---- the harness ----------------------------------------------------------------------------
# check NAME COMMAND... — a named assertion; it must exit 0.
check() {
  local name=$1
  shift
  if "$@"; then
    say "PASS $name"
  else
    say "FAIL $name"
    failures=$((failures + 1))
  fi
}

# vacuous NAME REASON COMMAND... — an assertion this head cannot fail, run and reported as such:
# it guards a later head, and it is never counted as evidence of the behavior it names.
vacuous() {
  local name=$1 reason=$2
  shift 2
  if "$@"; then
    say "PASS $name (vacuous on this head: $reason)"
  else
    say "FAIL $name"
    failures=$((failures + 1))
  fi
}

# control NAME COMMAND... — the same assertion on counterfeit evidence; it must fail.
control() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then
    say "FAIL control:$name accepted counterfeit evidence"
    failures=$((failures + 1))
  else
    say "PASS control:$name"
  fi
}

unable() {
  say "UNMEASURED $*"
  unmeasured=$((unmeasured + 1))
}

# counterfeit FILE JQ — a copy of a JSON record with one change; prints its path.
counterfeit() {
  local out
  out=$(mktemp "$evidence/counterfeit.XXXXXX")
  jq "$2" "$1" > "$out"
  printf '%s\n' "$out"
}

# counterfeit_text TEXT — a file holding TEXT; prints its path.
counterfeit_text() {
  local out
  out=$(mktemp "$evidence/counterfeit.XXXXXX")
  printf '%s\n' "$1" > "$out"
  printf '%s\n' "$out"
}

# counterfeit_lines FILE [APPEND] — FILE without the lines saying "recorded effect verified",
# then APPEND; prints its path.
counterfeit_lines() {
  local out
  out=$(mktemp "$evidence/counterfeit.XXXXXX")
  grep -v 'recorded effect verified' "$1" > "$out" || true
  printf '%s\n' "${2:-}" >> "$out"
  printf '%s\n' "$out"
}

# ---- the assertions -------------------------------------------------------------------------
record_is() { jq -e --arg state "$2" --arg stage "$3" '.state == $state and (.stage // "") == $stage' "$1" >/dev/null; }

reason_names() {
  local file=$1 word
  shift
  for word in "$@"; do
    jq -e --arg w "$word" '(.reason // "") | contains($w)' "$file" >/dev/null || return 1
  done
}

reason_quotes_none() {
  local file=$1 word
  shift
  for word in "$@"; do
    jq -e --arg w "$word" '(.reason // "") | contains($w) | not' "$file" >/dev/null || return 1
  done
}

# completed_are FILE STAGE... — exactly these stages completed, in this order.
completed_are() {
  local file=$1
  shift
  [ "$(jq -r '[.completed[].stage] | join(" ")' "$file")" = "$*" ]
}

# no_setup_token FILE — no setup-token plaintext (the product's olst_ prefix and base32 body).
no_setup_token() {
  ! grep -Eq 'olst_[A-Z2-7]{16,}' "$1"
}

# configuration_applied STATE JOURNAL — 0: one recorded application; 1: duplicate;
# 2: no usable original evidence. Missing evidence is UNMEASURED, not a restart success.
configuration_applied() {
  local records applications
  records=$(jq '[.completed[]? | select(.stage == "generate-product-config")] | length' "$1") || return 2
  applications=$(grep -cF 'generate-product-config: applied' "$2") || true
  [ "$records" -le 1 ] && [ "${applications:-0}" -le 1 ] || return 1
  [ "$records" -eq 1 ] && [ "${applications:-0}" -eq 1 ] || return 2
  jq -e '.completed[] | select(.stage == "generate-product-config") |
    .effect | type == "string" and length > 0' "$1" >/dev/null || return 2
}

# applied_once BEFORE_STATE BEFORE_JOURNAL AFTER_STATE AFTER_JOURNAL — first prove the
# original application; a single counterfeit line after an early refusal cannot replace it.
applied_once() {
  configuration_applied "$1" "$2" || return $?
  configuration_applied "$3" "$4"
}

# compared_more BEFORE AFTER — the later journal holds more recorded-effect comparisons.
compared_more() {
  [ "$(grep -cF 'generate-product-config: recorded effect verified' "$2")" -gt \
    "$(grep -cF 'generate-product-config: recorded effect verified' "$1")" ]
}

# lacks FILE UNIT — the captured unit list does not name UNIT.
lacks() { ! grep -qw -- "$2" "$1"; }

# no_ordering_cycle FILE — systemd logged no ordering cycle ("Found ordering cycle on ...").
no_ordering_cycle() { ! grep -qi 'ordering cycle' "$1"; }

# storage_claims_no_store FILE — initialize-storage does not record a store nobody created yet.
storage_claims_no_store() {
  jq -e '[.completed[] | select(.stage == "initialize-storage") | .effect | contains("created")] | any | not' "$1" >/dev/null
}

is() { [ "$(tr -d '[:space:]' < "$1")" = "$2" ]; }
is_not() { [ "$(tr -d '[:space:]' < "$1")" != "$2" ]; }
holds() { grep -Fq -- "$2" "$1"; }
same() { cmp -s "$1" "$2"; }

# ---- the fixture ----------------------------------------------------------------------------
# answers WORD — the carrier fixture document with host.hostname WORD.example.test.
answers() {
  sed "s/\"hostname\": \"olivares.example.test\"/\"hostname\": \"$1.example.test\"/" "$testdata/answers.json"
}

# seed — a NoCloud seed directory whose cloud-config writes the fixture answers.
seed() {
  local dir
  dir=$(mktemp -d "$evidence/seed.XXXXXX")
  cp "$testdata/nocloud/meta-data" "$testdata/nocloud/user-data" "$dir/"
  chmod 0755 "$dir"
  printf '%s\n' "$dir"
}

# ---- the seed's logins, before any boot -----------------------------------------------------
# cloud-init's users_groups module creates each users[].name of the seed with useradd, which
# refuses a name the image already defines as a user or as a group: Debian 13's base-passwd
# defines the group operator, useradd exits 9 on it and cloud-init ends in error. Each seed
# login must therefore be absent from both databases, as getent reports it in a disposable
# container of the image. Only getent's "not found" (exit 2) is absence; any other exit is
# unmeasured, never a pass.

# seed_logins USER_DATA — each users[].name of a cloud-config whose body, after its
# #cloud-config line, is JSON. An entry without a string name makes it fail, not skip.
seed_logins() {
  sed '1{/^#cloud-config$/d}' "$1" | jq -r '(.users // [])[] |
    if type == "object" and (.name | type) == "string" then .name
    else error("a users entry without a name") end'
}

# getent_in_image DATABASE KEY — getent in a disposable container of the image; prints the
# entry and exits 0 when found, 2 when not found, and otherwise when it could not look.
getent_in_image() {
  docker run --rm --entrypoint getent "$image_id" "$1" "$2" </dev/null
}

# defined_in LOGIN — one "DATABASE: ENTRY" line per database of the image that defines LOGIN;
# when a lookup could not answer, a line naming it and status 1.
defined_in() {
  local login=$1 db entry status
  for db in passwd group; do
    status=0
    entry=$(getent_in_image "$db" "$login") || status=$?
    case $status in
      0) printf '%s: %s\n' "$db" "$entry" ;;
      2) ;;
      *)
        printf 'getent %s %s exited %s\n' "$db" "$login" "$status"
        return 1
        ;;
    esac
  done
}

# seed_logins_absent USER_DATA — no login the seed declares is a user or a group of the image.
seed_logins_absent() {
  local logins login hits
  if ! logins=$(seed_logins "$1"); then
    unable "seed: the users of $1 could not be read"
    return
  fi
  if [ -z "$logins" ]; then
    say "PASS seed_login_absent_from_image (vacuous: the seed declares no users)"
    return
  fi
  while IFS= read -r login; do
    if ! hits=$(defined_in "$login"); then
      unable "seed_login_absent_from_image:$login: $hits"
    elif [ -n "$hits" ]; then
      say "FAIL seed_login_absent_from_image:$login: the image already defines it as ${hits//$'\n'/; }"
      failures=$((failures + 1))
    else
      say "PASS seed_login_absent_from_image:$login"
    fi
  done <<< "$logins"
}

# seed_lookup_control — root is a user and a group of every Debian and Fedora image, so the same lookup
# must find it in both databases; a lookup that finds nothing would pass every seed login.
seed_lookup_control() {
  local hits
  if ! hits=$(defined_in root); then
    unable "control:seed_login_absent_from_image: $hits"
  elif [ "$(grep -c -e '^passwd: ' -e '^group: ' <<< "$hits")" -eq 2 ]; then
    say "PASS control:seed_login_absent_from_image"
  else
    say "FAIL control:seed_login_absent_from_image accepted counterfeit evidence: root"
    failures=$((failures + 1))
  fi
}

# credential WORD — a directory the container manager passes to PID 1 as credentials.
credential() {
  local dir
  dir=$(mktemp -d "$evidence/credential.XXXXXX")
  answers "$1" > "$dir/olivares.appliance.answers"
  chmod 0755 "$dir"
  printf '%s\n' "$dir"
}

# boot NAME [SEED] [CREDENTIALS] — start a container with systemd as PID 1.
#
# systemd's container interface requires the container's mount hierarchy to be MS_SHARED
# before systemd is invoked as PID 1 (https://systemd.io/CONTAINER_INTERFACE/), and in a
# container systemd leaves propagation as it finds it. That Docker does not mount it shared is
# inferred from run 36187014845, not measured there, so the boot command prints the propagation
# of / and /run it finds before the remount (docker logs, "fixture-boot: before remount:").
# systemd builds each unit's /run/credentials/UNIT in a helper mount namespace and moves it
# into place there, so while /run is not shared that mount never reaches PID 1's namespace:
# the unit gets a $CREDENTIALS_DIRECTORY that does not exist, although PID 1 holds the
# credential. The container's first process therefore marks / recursively shared and then
# execs /sbin/init, which stays PID 1; a failed remount prints "$remount_failed" and ends the
# container before systemd starts, and the scenario is unmeasured, naming the remount. On a
# machine systemd marks / shared itself.
remount_failed='fixture-boot: mount --make-rshared / failed'
# Expand this program only inside the disposable container.
# shellcheck disable=SC2016
boot_command='address=$(ip -o -4 address show dev eth0 scope global | awk "{print \$4}")
  gateway=$(ip -4 route show default dev eth0 | awk "{print \$3}")
  [ -n "$address" ] && [ -n "$gateway" ] || exit 2
  printf "network:\n  version: 2\n  ethernets:\n    eth0:\n      addresses: [%s]\n      gateway4: %s\n" "$address" "$gateway" > /etc/cloud/cloud.cfg.d/98-fixture-network.cfg
  # Docker has already configured eth0. On the first NM start, select the
  # persistent cloud-init profile instead of adopting that external configuration.
  mkdir -p /etc/NetworkManager/conf.d
  printf "[device-olivares-fixture]\nmatch-device=interface-name:eth0\nkeep-configuration=no\n" > /etc/NetworkManager/conf.d/90-olivares-fixture.conf
  for m in / /run; do
    echo "fixture-boot: before remount: $(findmnt --noheadings --output TARGET,PROPAGATION --mountpoint "$m" 2>&1)"
  done
  mount --make-rshared / || {
    s=$?
    echo "fixture-boot: mount --make-rshared / failed with status $s; systemd was not started" >&2
    exit "$s"
  }
  exec /sbin/init'
boot() {
  local name=$1 seed_dir=${2:-} credentials=${3:-}
  local args=(--detach --name "$name" --hostname "$hostname" --privileged --cgroupns=private
    --tmpfs /run --tmpfs /run/lock)
  if [ -n "$seed_dir" ]; then
    args+=(--volume "$seed_dir:/var/lib/cloud/seed/nocloud:ro")
  fi
  if [ -n "$credentials" ]; then
    args+=(--env CREDENTIALS_DIRECTORY=/fixture-credentials --volume "$credentials:/fixture-credentials:ro")
  fi
  containers+=("$name")
  docker run "${args[@]}" "$image" /bin/sh -c "$boot_command" >/dev/null
}

# settle NAME — wait until boot finished and neither first-boot unit is running. It stops
# waiting as soon as Docker reports the container not running: nothing inside can settle then.
settle() {
  local name=$1 system units
  for _ in $(seq 1 150); do
    if [ "$(docker inspect --format '{{.State.Running}}' "$name" 2>/dev/null || true)" = false ]; then
      return 1
    fi
    system=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
    units=$(docker exec "$name" systemctl show --property=ActiveState --value \
      olivares-appliance-firstboot.service olivares-appliance-readiness.service 2>/dev/null || true)
    case "$system:$units" in
      running:* | degraded:*)
        case "$units" in
          *activating*) ;;
          *) return 0 ;;
        esac
        ;;
    esac
    sleep 2
  done
  return 1
}

# Keep read-only network facts and failures at every observation, including early refusal.
# Profile metadata is enough to identify its renderer; do not copy keys or connection secrets.
network_probe() {
  local name=$1 dir=$2 label=$3 status=0
  shift 3
  docker exec "$name" "$@" > "$dir/$label.txt" 2>&1 || status=$?
  printf '\nexit %s\n' "$status" >> "$dir/$label.txt"
}

capture_network() {
  local name=$1 dir=$2
  network_probe "$name" "$dir" nm-version nmcli --version
  network_probe "$name" "$dir" nm-active nmcli --mode multiline --fields all connection show --active
  network_probe "$name" "$dir" nm-profiles nmcli --mode multiline --fields all connection show
  network_probe "$name" "$dir" nm-devices nmcli --fields GENERAL device show
  network_probe "$name" "$dir" cloud-init-version cloud-init --version
  network_probe "$name" "$dir" cloud-init-renderer sh -c \
    'grep -inE "renderer|netplan|network-manager|NetworkManager" /var/log/cloud-init.log'
  network_probe "$name" "$dir" cloud-init-renderer-config sh -c \
    'grep -nE -A 6 "renderers:|activators:" /etc/cloud/cloud.cfg /etc/cloud/cloud.cfg.d/*.cfg'
  network_probe "$name" "$dir" network-files find /etc/NetworkManager/system-connections \
    /run/NetworkManager/system-connections /etc/netplan /run/systemd/network \
    /etc/network/interfaces.d /etc/sysconfig/network-scripts /etc/systemd/network \
    /usr/lib/systemd/network -maxdepth 1 -type f -print
  # File variables belong to the container, not the invoking host shell.
  # shellcheck disable=SC2016
  network_probe "$name" "$dir" network-owner-configs sh -c \
    'for file in /etc/network/interfaces /etc/network/interfaces.d/* /etc/sysconfig/network-scripts/ifcfg-* /etc/systemd/network/*.network /run/systemd/network/*.network /usr/lib/systemd/network/*.network /etc/netplan/*.yaml /etc/netplan/*.yml; do
      [ -f "$file" ] || continue
      printf "FILE %s\n" "$file"
      stat -c "%a %U:%G %s" "$file"
      sed -n -E "/^[[:space:]]*(\\[Match\\]|Name=|OriginalName=|Kind=|Type=|Driver=|Virtualization=|iface[[:space:]]|source[[:space:]]|auto[[:space:]]|allow-hotplug[[:space:]]|renderer:|networkd)/p" "$file"
    done'
  network_probe "$name" "$dir" networkd-enabled systemctl is-enabled systemd-networkd.service systemd-networkd.socket
  network_probe "$name" "$dir" networkd-active systemctl is-active systemd-networkd.service systemd-networkd.socket
}

# unsettled SCENARIO NAME DIR WHAT — settle failed: keep Docker's view of the container
# (container.state.json, container.log) in DIR and report the scenario unmeasured. When the
# boot command's remount failed, systemd never started, and the line says so instead of WHAT.
unsettled() {
  local scenario=$1 name=$2 dir=$3 what=$4 state
  mkdir -p "$dir"
  capture_network "$name" "$dir"
  docker inspect --format '{{json .State}}' "$name" > "$dir/container.state.json" 2>&1 || true
  docker logs "$name" > "$dir/container.log" 2>&1 || true
  state=$(jq -r 'if .Running then "running" else "exited with status \(.ExitCode)" end' \
    "$dir/container.state.json" 2>/dev/null || echo "state not readable")
  if grep -qF "$remount_failed" "$dir/container.log"; then
    unable "$scenario: the boot command's mount --make-rshared / failed, so systemd never started (container $state; container.log)"
  else
    unable "$scenario: $what (container $state; container.state.json, container.log)"
  fi
}

# in NAME COMMAND... — PASS/FAIL of a test inside the container, as the word present/absent.
in_container() {
  local name=$1
  shift
  if docker exec "$name" "$@" >/dev/null 2>&1; then echo present; else echo absent; fi
}

# remove_base NAME — remove olivares-appliance-base with the booted family's own package tool,
# chosen by the container's /etc/os-release: dpkg on Debian, rpm on Fedora. One family's tool
# never runs on the other. It prints what failed: status 1 when the tool failed, 2 when the
# container names no family this battery knows, which is unmeasured, never a guess.
remove_base() {
  local name=$1 id
  id=$(docker exec "$name" sh -c '. /etc/os-release && printf %s "$ID"' 2>/dev/null) || id=
  case $id in
    debian)
      docker exec "$name" dpkg --remove olivares-appliance-base >/dev/null ||
        { echo "dpkg could not remove olivares-appliance-base"; return 1; }
      ;;
    fedora)
      docker exec "$name" rpm --erase olivares-appliance-base >/dev/null ||
        { echo "rpm could not erase olivares-appliance-base"; return 1; }
      ;;
    *)
      echo "the container's /etc/os-release names no family this battery knows (ID=${id:-unreadable})"
      return 2
      ;;
  esac
}

# capture NAME DIR — copy the evidence of one moment out of the container.
capture() {
  local name=$1 dir=$2
  mkdir -p "$dir"
  capture_network "$name" "$dir"
  docker exec "$name" cat /var/lib/olivares-appliance/state.json > "$dir/state.json" 2>/dev/null || echo '{}' > "$dir/state.json"
  # The delivered setup token's SHAPE, mode and owner — never its bytes, which stay on the
  # machine in the root-only file.
  docker exec "$name" sh -c 'tok=/var/lib/olivares-appliance/setup-token; \
    if [ -r "$tok" ]; then printf "shape "; head -c 4 "$tok"; printf "\n"; \
      printf "mode %s owner %s\n" "$(stat -c %a "$tok")" "$(stat -c %U "$tok")"; \
    else echo absent; fi' > "$dir/setup-token" 2>/dev/null || true
  docker exec "$name" journalctl --boot --no-pager > "$dir/journal.txt" 2>/dev/null || true
  docker exec "$name" cloud-init status --format json > "$dir/cloud-init.json" 2>/dev/null || true
  docker exec "$name" systemctl is-active olivares.service > "$dir/product.active" 2>/dev/null || true
  docker exec "$name" systemctl is-enabled olivares.service > "$dir/product.enabled" 2>/dev/null || true
  docker exec "$name" systemctl is-enabled olivares-appliance-firstboot.service > "$dir/firstboot.enabled" 2>/dev/null || true
  docker exec "$name" sh -c 'cat /etc/olivares/olivares.env; stat -c "%i %Y" /etc/olivares/olivares.env' > "$dir/olivares.env" 2>/dev/null || true
  docker exec "$name" cat /etc/systemd/system/olivares.service.d/50-olivares-appliance.conf > "$dir/drop-in.conf" 2>/dev/null || true
  # The Appliance Console's directory and the selection first boot publishes in it: owner, mode
  # and type as stat prints them ("absent" when there is none), and the selection's bytes.
  docker exec "$name" stat -c '%U:%G %a %F' /etc/olivares-portal > "$dir/console-dir.stat" 2>/dev/null ||
    echo absent > "$dir/console-dir.stat"
  docker exec "$name" stat -c '%U:%G %a %F' /etc/olivares-portal/selection.json > "$dir/selection.stat" 2>/dev/null ||
    echo absent > "$dir/selection.stat"
  docker exec "$name" cat /etc/olivares-portal/selection.json > "$dir/selection.json" 2>/dev/null || true
  in_container "$name" test -e /var/lib/olivares-appliance/ready > "$dir/ready"
  # The job graph: what multi-user.target and first boot are ordered after, as systemd loaded it.
  docker exec "$name" systemctl show --property=After --value multi-user.target > "$dir/multi-user.after" 2>/dev/null || true
  docker exec "$name" systemctl show --property=After --value olivares-appliance-firstboot.service \
    > "$dir/firstboot.after" 2>/dev/null || true
  docker exec "$name" systemctl list-dependencies --after --no-pager olivares-appliance-firstboot.service \
    > "$dir/firstboot.list-dependencies-after" 2>&1 || true
  docker exec "$name" systemctl list-dependencies --no-pager multi-user.target > "$dir/multi-user.list-dependencies" 2>&1 || true
  docker exec "$name" sh -c 'systemd-analyze verify --man=no --generators=yes multi-user.target \
    olivares-appliance-firstboot.service olivares-appliance-readiness.service; echo "exit $?"' \
    > "$dir/systemd-analyze-verify.txt" 2>&1 || true
}

# applied NAME DIR — cloud-init completed from the seed and emitted the NoCloud document.
applied() {
  local name=$1 dir=$2
  jq -e '.status == "done"' "$dir/cloud-init.json" >/dev/null 2>&1 &&
    docker exec "$name" test -s /etc/olivares-appliance/carriers/nocloud.json
}

# cloud_init_errors DIR — cloud-init's own .errors from the captured status, on one line and at
# most 300 characters, so a "did not apply" line names what cloud-init reported.
cloud_init_errors() {
  local errors
  if ! [ -s "$1/cloud-init.json" ] ||
    ! errors=$(jq -er '(.errors // []) | map(tostring) | join("; ")' "$1/cloud-init.json" 2>/dev/null); then
    errors="the captured cloud-init status is not readable"
  elif [ -z "$errors" ]; then
    errors="none reported"
  fi
  errors=${errors//$'\n'/ }
  printf '%s' "${errors:0:300}"
}

# imported NAME — systemd imported the system credential the container manager passed.
imported() {
  docker exec "$1" sh -c 'systemd-creds --system list 2>/dev/null | grep -q olivares.appliance.answers ||
    test -s /run/credentials/@system/olivares.appliance.answers'
}

# capture_credential_path NAME DIR — the path of the answers credential from PID 1 to a unit,
# as names, sizes and settings only, never a value: systemd's version, the credential settings
# of first boot, the system credential store and its contents, the mount propagation of / and
# /run before the boot command's remount (mount-propagation.before-remount) and after it, and a
# transient unit, olivares-credential-probe.service, that imports the credential the way first
# boot does. That unit writes present or absent to credential.probe and its
# $CREDENTIALS_DIRECTORY and listing to credential.probe-directory; after it, its journal
# (credential.probe-journal, where systemd logs a credential setup failure) and PID 1's whole
# mount table (mountinfo) are kept.
capture_credential_path() {
  local name=$1 dir=$2
  mkdir -p "$dir"
  docker exec "$name" systemctl --version > "$dir/systemctl.version" 2>&1 || true
  docker exec "$name" systemctl show olivares-appliance-firstboot.service \
    -p ImportCredential -p LoadCredential -p SetCredential > "$dir/firstboot.credential-settings" 2>&1 || true
  docker exec "$name" ls -la /run/credentials > "$dir/run-credentials.ls" 2>&1 || true
  docker exec "$name" ls -laL /run/credentials/@system/ >> "$dir/run-credentials.ls" 2>&1 || true
  docker exec "$name" systemd-creds --system list > "$dir/system-credentials.list" 2>&1 || true
  docker logs "$name" 2>&1 | grep -F 'fixture-boot: before remount:' > "$dir/mount-propagation.before-remount" || true
  docker exec "$name" findmnt --noheadings --output TARGET,PROPAGATION --mountpoint / > "$dir/mount-propagation" 2>&1 || true
  docker exec "$name" findmnt --noheadings --output TARGET,PROPAGATION --mountpoint /run >> "$dir/mount-propagation" 2>&1 || true
  docker exec "$name" systemd-run --unit=olivares-credential-probe --quiet --wait --pipe --expand-environment=no \
    -p ImportCredential=olivares.appliance.answers /bin/sh -c '
      d=${CREDENTIALS_DIRECTORY-}
      echo "CREDENTIALS_DIRECTORY=${d:-unset}" >&2
      [ -z "$d" ] || ls -la "$d" >&2
      if [ -n "$d" ] && [ -e "$d/olivares.appliance.answers" ]; then echo present; else echo absent; fi' \
    > "$dir/credential.probe" 2> "$dir/credential.probe-directory" || true
  docker exec "$name" journalctl --boot --no-pager --unit=olivares-credential-probe.service \
    > "$dir/credential.probe-journal" 2>&1 || true
  docker exec "$name" cat /proc/1/mountinfo > "$dir/mountinfo" 2>&1 || true
}

# delivered SCENARIO DIR — a unit that imports the answers credential sees it: delivery to a
# unit, asserted apart from which carrier first boot resolved.
delivered() {
  local scenario=$1 dir=$2
  check "credential_reaches_a_unit:$scenario" is "$dir/credential.probe" present
  control "credential_reaches_a_unit:$scenario" is "$(counterfeit_text absent)" present
}

# job_graph SCENARIO DIR — first boot is ordered after cloud-final and nothing boot waits for
# is ordered after first boot, so systemd deletes no start job.
job_graph() {
  local scenario=$1 dir=$2
  check "multi_user_target_does_not_wait_for_first_boot:$scenario" \
    lacks "$dir/multi-user.after" olivares-appliance-firstboot.service
  control "multi_user_target_does_not_wait_for_first_boot:$scenario" \
    lacks "$(counterfeit_text "basic.target olivares-appliance-firstboot.service")" olivares-appliance-firstboot.service
  check "first_boot_ordered_after_cloud_final:$scenario" holds "$dir/firstboot.after" cloud-final.service
  control "first_boot_ordered_after_cloud_final:$scenario" holds "$(counterfeit_text "multi-user.target")" cloud-final.service
  check "no_ordering_cycle_at_boot:$scenario" no_ordering_cycle "$dir/journal.txt"
  control "no_ordering_cycle_at_boot:$scenario" no_ordering_cycle \
    "$(counterfeit_lines "$dir/journal.txt" 'systemd[1]: cloud-final.service: Found ordering cycle on multi-user.target/start')"
}

journal_is_clean() {
  local scenario=$1 dir=$2
  vacuous "journal_has_no_setup_token:$scenario" "the product never starts in this fixture while a later seam refuses" \
    no_setup_token "$dir/journal.txt"
  control "journal_has_no_setup_token:$scenario" no_setup_token \
    "$(counterfeit_lines "$dir/journal.txt" '  Token:    olst_ABCDEFGHIJKLMNOPQRSTUVWXYZ234567')"
}

# ---- the scenarios --------------------------------------------------------------------------
no_carrier() {
  local d="$evidence/no-carrier"
  boot fx-no-carrier
  settle fx-no-carrier || { unsettled no-carrier fx-no-carrier "$d" "the container did not finish booting"; return; }
  capture fx-no-carrier "$d"
  check no_carrier_is_pending_at_validate record_is "$d/state.json" pending validate
  control no_carrier_is_pending_at_validate record_is "$(counterfeit "$d/state.json" '.state = "ready"')" pending validate
  check no_carrier_completes_no_stage completed_are "$d/state.json"
  control no_carrier_completes_no_stage completed_are \
    "$(counterfeit "$d/state.json" '.completed = [{"stage": "prepare-identity", "effect": "x"}]')"
  check package_enabled_first_boot is "$d/firstboot.enabled" enabled
  control package_enabled_first_boot is "$(counterfeit_text disabled)" enabled
  check package_left_the_product_disabled is_not "$d/product.enabled" enabled
  control package_left_the_product_disabled is_not "$(counterfeit_text enabled)" enabled
  check package_created_the_console_directory is "$d/console-dir.stat" "root:root755directory"
  control package_created_the_console_directory is "$(counterfeit_text 'root:root 700 directory')" "root:root755directory"
  job_graph no-carrier "$d"
  journal_is_clean no-carrier "$d"
}

conflict() {
  local d="$evidence/conflict"
  boot fx-conflict "$(seed)" "$(credential other)"
  settle fx-conflict || { unsettled conflict fx-conflict "$d" "the container did not finish booting"; return; }
  capture fx-conflict "$d"
  capture_credential_path fx-conflict "$d"
  applied fx-conflict "$d" || {
    unable "conflict: cloud-init did not apply the NoCloud seed; cloud-init errors: $(cloud_init_errors "$d")"
    return
  }
  imported fx-conflict || { unable "conflict: systemd did not import the system credential"; return; }
  delivered conflict "$d"
  check conflicting_carriers_refuse_at_validate record_is "$d/state.json" refused validate
  control conflicting_carriers_refuse_at_validate record_is "$(counterfeit "$d/state.json" '.state = "pending"')" refused validate
  check conflict_names_both_carriers reason_names "$d/state.json" nocloud systemd-credential
  control conflict_names_both_carriers reason_names \
    "$(counterfeit "$d/state.json" '.reason = "answers carriers disagree: nocloud"')" nocloud systemd-credential
  check conflict_quotes_no_answers_value reason_quotes_none "$d/state.json" other.example.test olivares.example.test ssh-ed25519
  control conflict_quotes_no_answers_value reason_quotes_none \
    "$(counterfeit "$d/state.json" '.reason += " other.example.test"')" other.example.test
  journal_is_clean conflict "$d"
}

agreeing() {
  local d="$evidence/agreeing"
  boot fx-agreeing "$(seed)" "$(credential olivares)"
  settle fx-agreeing || { unsettled agreeing fx-agreeing "$d" "the container did not finish booting"; return; }
  capture fx-agreeing "$d"
  capture_credential_path fx-agreeing "$d"
  applied fx-agreeing "$d" || {
    unable "agreeing: cloud-init did not apply the NoCloud seed; cloud-init errors: $(cloud_init_errors "$d")"
    return
  }
  imported fx-agreeing || { unable "agreeing: systemd did not import the system credential"; return; }
  delivered agreeing "$d"
  # The setup-token seam now COMPLETES in the fixture too: the token is minted and delivered
  # to its root-only file, and the stop this scenario pins moves one stage on, to the firewall
  # this Debian container cannot load (no nftables kernel path inside it). The image legs
  # (the NoCloud probe) measure the ready outcome on the real Fedora image.
  check agreeing_carriers_stop_at_the_firewall record_is "$d/state.json" refused verify-firewall
  control agreeing_carriers_stop_at_the_firewall record_is \
    "$(counterfeit "$d/state.json" '.stage = "measure-readiness"')" refused verify-firewall
  check stages_through_the_setup_delivery_completed_in_order completed_are "$d/state.json" \
    prepare-identity verify-host-settings hand-over-host-settings generate-product-config initialize-storage \
    prepare-setup-delivery
  control stages_through_the_setup_delivery_completed_in_order completed_are \
    "$(counterfeit "$d/state.json" '.completed |= reverse')" \
    prepare-identity verify-host-settings hand-over-host-settings generate-product-config initialize-storage \
    prepare-setup-delivery
  # The delivery itself, on the machine: the owner's token shape in a root-only file.
  check the_setup_token_was_delivered_here holds "$d/setup-token" "shape olst"
  control the_setup_token_was_delivered_here holds "$(counterfeit_text 'shape nope')" "shape olst"
  check the_delivered_token_is_root_only_here holds "$d/setup-token" "mode 600 owner root"
  control the_delivered_token_is_root_only_here holds "$(counterfeit_text 'mode 644 owner root')" "mode 600 owner root"
  check product_generator_wrote_the_configuration holds "$d/olivares.env" "(profile: single-node-prod)"
  control product_generator_wrote_the_configuration holds "$here/../../../../packaging/olivares.env.example" "(profile: single-node-prod)"
  check public_url_declared_in_the_appliance_drop_in holds "$d/drop-in.conf" "Environment=OLIVARES_PUBLIC_URL=https://olivares.example.test"
  control public_url_declared_in_the_appliance_drop_in holds "$(counterfeit_text '[Service]')" "Environment=OLIVARES_PUBLIC_URL=https://olivares.example.test"
  # These answers declare none of the console's fields, so first boot publishes no selection.
  check no_selection_without_the_console_fields is "$d/selection.stat" absent
  control no_selection_without_the_console_fields is "$(counterfeit_text 'root:root 644 regular file')" absent
  # The setup-token seam refuses first, so this shows the product is not started while a seam
  # refuses; that the firewall stage precedes the start is asserted by the Go tests only.
  check product_not_started_while_a_seam_refuses is_not "$d/product.active" active
  control product_not_started_while_a_seam_refuses is_not "$(counterfeit_text active)" active
  # Reached only where initialize-storage completed; a record without it measures nothing here.
  if jq -e 'any(.completed[]?; .stage == "initialize-storage")' "$d/state.json" >/dev/null; then
    check storage_records_no_store_before_the_product_starts storage_claims_no_store "$d/state.json"
    control storage_records_no_store_before_the_product_starts storage_claims_no_store \
      "$(counterfeit "$d/state.json" '(.completed[] | select(.stage == "initialize-storage") | .effect) = "sqlite store created"')"
  else
    unable "agreeing: initialize-storage was not reached, so what it records is unmeasured"
  fi
  job_graph agreeing "$d"
  check never_ready_without_measured_readiness is "$d/ready" absent
  control never_ready_without_measured_readiness is "$(counterfeit_text present)" absent
  journal_is_clean agreeing "$d"
}

# portal_seed — a NoCloud seed whose answers also declare the Appliance Console's selection:
# portal.enabled true, portal.listen management and host.management_interfaces [eth1, eth0].
portal_seed() {
  local dir body answers
  dir=$(seed)
  body=$(sed '1{/^#cloud-config$/d}' "$dir/user-data")
  answers=$(jq -r '.write_files[0].content' <<< "$body" | base64 -d |
    jq -c '.portal = {"enabled": true, "listen": "management"} | .host.management_interfaces = ["eth1", "eth0"]' |
    base64 -w0)
  { echo '#cloud-config'; jq --arg content "$answers" '.write_files[0].content = $content' <<< "$body"; } > "$dir/user-data"
  printf '%s\n' "$dir"
}

# published_selection — the selection first boot publishes for portal_seed's answers: their
# validated values, the interfaces sorted, in the published document's fixed order.
published_selection() {
  cat <<'JSON'
{
  "schema_version": "olivares-portal-selection/v1",
  "portal": {
    "enabled": true,
    "listen": "management"
  },
  "host": {
    "management_interfaces": [
      "eth0",
      "eth1"
    ]
  }
}
JSON
}

# selection_recorded FILE — generate-product-config's recorded effect covers the selection.
selection_recorded() {
  jq -e 'any(.completed[]; .stage == "generate-product-config" and (.effect | contains(" portal-selection=")))' "$1" >/dev/null
}

portal_selection() {
  local d="$evidence/portal-selection" expected
  boot fx-portal "$(portal_seed)"
  settle fx-portal || { unsettled portal-selection fx-portal "$d" "the container did not finish booting"; return; }
  capture fx-portal "$d"
  applied fx-portal "$d" || {
    unable "portal-selection: cloud-init did not apply the NoCloud seed; cloud-init errors: $(cloud_init_errors "$d")"
    return
  }
  expected="$d/selection.expected"
  published_selection > "$expected"
  check console_fields_stop_at_the_token_seam record_is "$d/state.json" refused prepare-setup-delivery
  control console_fields_stop_at_the_token_seam record_is \
    "$(counterfeit "$d/state.json" '.stage = "validate"')" refused prepare-setup-delivery
  check selection_published_from_the_validated_answers same "$d/selection.json" "$expected"
  control selection_published_from_the_validated_answers same \
    "$(counterfeit_text '{"schema_version": "olivares-portal-selection/v1", "portal": {"listen": "local"}}')" "$expected"
  check selection_is_root_owned_0644 is "$d/selection.stat" "root:root644regularfile"
  control selection_is_root_owned_0644 is "$(counterfeit_text 'root:root 664 regular file')" "root:root644regularfile"
  check selection_digest_recorded selection_recorded "$d/state.json"
  control selection_digest_recorded selection_recorded \
    "$(counterfeit "$d/state.json" '(.completed[] | select(.stage == "generate-product-config") | .effect) = "olivares.env=0 drop-in=0"')"
  journal_is_clean portal-selection "$d"
}

restart_and_change() {
  local d="$evidence/restart"
  boot fx-restart "$(seed)"
  settle fx-restart || { unsettled restart fx-restart "$d/first" "the container did not finish booting"; return; }
  capture fx-restart "$d/first"
  applied fx-restart "$d/first" || {
    unable "restart: cloud-init did not apply the NoCloud seed; cloud-init errors: $(cloud_init_errors "$d/first")"
    return
  }
  check single_carrier_stops_at_the_token_seam record_is "$d/first/state.json" refused prepare-setup-delivery
  control single_carrier_stops_at_the_token_seam record_is \
    "$(counterfeit "$d/first/state.json" '.state = "ready"')" refused prepare-setup-delivery

  local initial=0
  configuration_applied "$d/first/state.json" "$d/first/journal.txt" || initial=$?
  if [ "$initial" -eq 0 ] && ! [ -s "$d/first/olivares.env" ]; then initial=2; fi
  case $initial in
    1) say "FAIL restart: the initial configuration was recorded or applied more than once"
       failures=$((failures + 1)) ;;
    2) unable "restart/change: no recorded initial configuration application; comparisons are unmeasured" ;;
  esac
  if [ "$initial" -eq 0 ]; then
    docker exec fx-restart systemctl restart olivares-appliance-firstboot.service >/dev/null 2>&1 || true
    settle fx-restart || { unsettled restart fx-restart "$d/restarted" "first boot did not stop after a restart"; return; }
    capture fx-restart "$d/restarted"
    jq '.completed' "$d/first/state.json" > "$d/first/completed.json"
    jq '.completed' "$d/restarted/state.json" > "$d/restarted/completed.json"
    check restart_keeps_the_recorded_stages same "$d/first/completed.json" "$d/restarted/completed.json"
    control restart_keeps_the_recorded_stages same "$d/first/completed.json" "$(counterfeit "$d/restarted/completed.json" '.[1:]')"
    check restart_does_not_regenerate_the_configuration same "$d/first/olivares.env" "$d/restarted/olivares.env"
    control restart_does_not_regenerate_the_configuration same "$d/first/olivares.env" "$(counterfeit_text "regenerated")"
    check restart_compares_recorded_effects compared_more "$d/first/journal.txt" "$d/restarted/journal.txt"
    control restart_compares_recorded_effects compared_more "$d/first/journal.txt" "$d/first/journal.txt"
    local restarted=0
    applied_once "$d/first/state.json" "$d/first/journal.txt" \
      "$d/restarted/state.json" "$d/restarted/journal.txt" || restarted=$?
    if [ "$restarted" -eq 2 ]; then
      unable "restart_does_not_reapply_a_recorded_stage: the restarted record or journal is unavailable"
    else
      check restart_does_not_reapply_a_recorded_stage test "$restarted" -eq 0
      control restart_does_not_reapply_a_recorded_stage applied_once \
        "$d/first/state.json" "$d/first/journal.txt" "$d/restarted/state.json" \
        "$(counterfeit_lines "$d/restarted/journal.txt" 'first boot: generate-product-config: applied')"
    fi

    answers changed | docker exec --interactive fx-restart sh -c 'cat > /etc/olivares-appliance/carriers/nocloud.json'
    docker exec fx-restart systemctl restart olivares-appliance-firstboot.service >/dev/null 2>&1 || true
    settle fx-restart || { unsettled restart fx-restart "$d/changed" "first boot did not stop after the answers changed"; return; }
    capture fx-restart "$d/changed"
    jq '.completed' "$d/changed/state.json" > "$d/changed/completed.json"
    check changed_answers_refuse_at_validate record_is "$d/changed/state.json" refused validate
    control changed_answers_refuse_at_validate record_is "$(counterfeit "$d/changed/state.json" '.stage = "prepare-setup-delivery"')" refused validate
    check changed_answers_name_the_last_completed_stage reason_names "$d/changed/state.json" initialize-storage
    control changed_answers_name_the_last_completed_stage reason_names \
      "$(counterfeit "$d/changed/state.json" '.reason = "the answers changed"')" initialize-storage
    check changed_answers_apply_nothing same "$d/first/completed.json" "$d/changed/completed.json"
    control changed_answers_apply_nothing same "$d/first/completed.json" "$(counterfeit "$d/changed/completed.json" '. + [{"stage": "prepare-setup-delivery"}]')"
    journal_is_clean restart "$d/changed"
  fi

  local how status=0
  how=$(remove_base fx-restart) || status=$?
  case $status in
    0) ;;
    2)
      unable "package_removal: $how"
      return
      ;;
    *)
      say "FAIL package_removal: $how"
      failures=$((failures + 1))
      return
      ;;
  esac
  mkdir -p "$d/removed"
  in_container fx-restart test -s /var/lib/olivares-appliance/state.json > "$d/removed/record"
  in_container fx-restart test -x /usr/bin/olivares > "$d/removed/product"
  in_container fx-restart test -e /usr/lib/systemd/system/olivares-appliance-firstboot.service > "$d/removed/unit"
  check removal_keeps_the_first_boot_record is "$d/removed/record" present
  control removal_keeps_the_first_boot_record is "$(counterfeit_text absent)" present
  check removal_keeps_the_product is "$d/removed/product" present
  control removal_keeps_the_product is "$(counterfeit_text absent)" present
  check removal_removes_the_first_boot_unit is "$d/removed/unit" absent
  control removal_removes_the_first_boot_unit is "$(counterfeit_text present)" absent
}

seed_logins_absent "$testdata/nocloud/user-data"
seed_lookup_control
no_carrier
conflict
agreeing
portal_selection
restart_and_change

say "failed: $failures; unmeasured: $unmeasured"
if [ "$failures" -gt 0 ]; then
  exit 1
fi
if [ "$unmeasured" -gt 0 ]; then
  exit 2
fi
exit 0
