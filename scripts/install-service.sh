#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Install a service definition around an already verified Olivares binary. This
# script never invokes sudo and never starts a daemon unless --start was explicit.
set -eu

say() { printf '%s\n' "$*"; }
# diag writes operator diagnostics to stderr. They accompany a failure, so they
# must not land in the stdout stream that carries the result-json contract.
diag() { printf '%s\n' "$*" >&2; }
err() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
# same_bytes <a> <b>: the idempotency check for rendered service files. `cmp` belongs to
# diffutils, which minimal images do not carry (fedora:latest container: "cmp: command not
# found", and the missing tool read as "refusing to replace existing service file",
# 2026-09-17). Without cmp the two files are compared by SHA-256 (sha256sum or shasum).
same_bytes() {
  if have cmp; then cmp -s "$1" "$2"; return; fi
  if have sha256sum; then [ "$(sha256sum "$1" | awk '{print $1}')" = "$(sha256sum "$2" | awk '{print $1}')" ]; return; fi
  if have shasum; then [ "$(shasum -a 256 "$1" | awk '{print $1}')" = "$(shasum -a 256 "$2" | awk '{print $1}')" ]; return; fi
  err "cannot compare $1 with $2: none of cmp, sha256sum or shasum is available"
}
usage() {
  cat <<'EOF'
Usage: install-service.sh (--user|--system) --binary ABSOLUTE_PATH
       [--init auto|systemd|openrc|launchd] [--data-dir PATH] [--config PATH]
       [--managed-binary] [--start] [--dry-run] [--root ABSOLUTE_STAGING_ROOT]

--system requires uid 0 and never invokes sudo. --root stages an offline image,
cannot be combined with --start, and does not create the service account.
EOF
}

mode=""
init=auto
binary=""
data_dir=""
config=""
start=0
dry_run=0
root_prefix=""
managed_binary=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --user) [ -z "$mode" ] || err "choose exactly one of --user or --system"; mode=user; shift ;;
    --system) [ -z "$mode" ] || err "choose exactly one of --user or --system"; mode=system; shift ;;
    --binary) [ "$#" -ge 2 ] || err "--binary needs a value"; binary="$2"; shift 2 ;;
    --init) [ "$#" -ge 2 ] || err "--init needs a value"; init="$2"; shift 2 ;;
    --data-dir) [ "$#" -ge 2 ] || err "--data-dir needs a value"; data_dir="$2"; shift 2 ;;
    --config) [ "$#" -ge 2 ] || err "--config needs a value"; config="$2"; shift 2 ;;
    --root) [ "$#" -ge 2 ] || err "--root needs a value"; root_prefix="$2"; shift 2 ;;
    --managed-binary) managed_binary=1; shift ;;
    --start) start=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "unknown argument: $1" ;;
  esac
done
[ -n "$mode" ] || err "choose exactly one of --user or --system"
case "$binary" in /*) ;; *) err "--binary must be an absolute path" ;; esac
case "$root_prefix" in "") ;; /) err "--root may not be /" ;; /*) ;; *) err "--root must be absolute" ;; esac
[ "$start" -eq 0 ] || [ -z "$root_prefix" ] || err "--start cannot target an offline --root"

# Characters that a sed replacement, a systemd/OpenRC value or a plist would
# reinterpret: separators, quotes, escapes, template markers, systemd
# specifiers/variables, shell comment/command separators and anything not
# printable (which covers tabs and newlines). A plain space is allowed and is
# quoted where the service format supports it (see clean_path/quote_unit).
safe_path() {
  case "$1" in
    *'|'*|*'&'*|*'<'*|*'>'*|*'@'*|*\\*|*'"'*|*"'"*|*'$'*|*'%'*|*';'*|*'#'*|*[![:print:]]*)
      err "path contains a character unsafe for a service definition: $1" ;;
  esac
}
# A recorded path is canonical or it is refused: absolute, no empty, "." or
# ".." components and no trailing slash. Nothing is rewritten on the
# operator's behalf, so the path in the unit is the path they asked for.
clean_path() {
  case "$1" in
    /) err "path may not be the filesystem root" ;;
    /*) ;;
    *) err "path must be absolute: $1" ;;
  esac
  case "$1" in
    */|*//*|*/./*|*/../*|*/.|*/..) err "path must be canonical (no trailing slash, empty, . or .. components): $1" ;;
  esac
}
safe_path "$binary"

goos="${OLIVARES_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
case "$goos" in linux|darwin) ;; *) err "service adapters support linux and darwin, not $goos" ;; esac
if [ "$init" = auto ]; then
  if [ "$goos" = darwin ]; then
    init=launchd
  elif have systemctl && [ -d /run/systemd/system ]; then
    init=systemd
  elif have rc-service && { [ -d /run/openrc ] || [ -d /lib/rc ]; }; then
    init=openrc
  else
    err "no supported init detected; pass --init after confirming systemd, OpenRC or launchd"
  fi
fi
case "$init" in
  systemd|openrc) [ "$goos" = linux ] || err "$init is supported only on linux" ;;
  launchd) [ "$goos" = darwin ] || err "launchd is supported only on darwin" ;;
  *) err "unsupported init: $init" ;;
esac
[ "$init" != openrc ] || [ "$mode" = system ] || err "OpenRC has no user-service adapter; use --system"

home="${HOME:-}"
if [ "$mode" = user ]; then
  case "$home" in /*) ;; *) err "HOME must be absolute for --user" ;; esac
  xdg_config="${XDG_CONFIG_HOME:-$home/.config}"
  xdg_data="${XDG_DATA_HOME:-$home/.local/share}"
  case "$xdg_config:$xdg_data" in /*:/*) ;; *) err "XDG_CONFIG_HOME and XDG_DATA_HOME must be absolute" ;; esac
  [ -n "$data_dir" ] || data_dir="$xdg_data/olivares"
  [ -n "$config" ] || config="$xdg_config/olivares/olivares.env"
  case "$init" in
    systemd) unit="$xdg_config/systemd/user/olivares.service" ;;
    launchd) unit="$home/Library/LaunchAgents/dev.olivares.olivares.plist" ;;
  esac
else
  if [ "$init" = launchd ]; then
    [ -n "$data_dir" ] || data_dir="/Library/Application Support/Olivares"
    [ -n "$config" ] || config="/Library/Preferences/dev.olivares.olivares.env"
    unit="/Library/LaunchDaemons/dev.olivares.olivares.plist"
  else
    [ -n "$data_dir" ] || data_dir=/var/lib/olivares
    [ -n "$config" ] || config=/etc/olivares/olivares.env
    case "$init" in
      systemd) unit=/etc/systemd/system/olivares.service ;;
      openrc) unit=/etc/init.d/olivares ;;
    esac
  fi
fi
for path in "$data_dir" "$config" "$unit"; do
  case "$path" in /*) ;; *) err "resolved path is not absolute: $path" ;; esac
  safe_path "$path"
  clean_path "$path"
done

# Binary, config and unit are the exact routes emitted as install_layout by
# render-release-index.sh: the uninstaller deletes them by name and refuses
# any other. The data directory is either the tuple's default or a custom
# directory admitted by the policy below; the manifest records which
# ("layout": default|custom) and the uninstaller then requires the unit
# rendered here to name the same directory before it plans anything.
layout=default
if [ "$mode" = system ]; then
  case "$binary" in
    /usr/bin/olivares|/usr/local/bin/olivares|/opt/olivares|/opt/olivares/bin/olivares) ;;
    *) err "system binary path is outside the release-index install_layout: $binary" ;;
  esac
  case "$init:$config:$unit" in
    systemd:/etc/olivares/olivares.env:/etc/systemd/system/olivares.service|\
    openrc:/etc/olivares/olivares.env:/etc/init.d/olivares) default_data=/var/lib/olivares ;;
    "launchd:/Library/Preferences/dev.olivares.olivares.env:/Library/LaunchDaemons/dev.olivares.olivares.plist")
      default_data="/Library/Application Support/Olivares" ;;
    *) err "system config/unit tuple is outside the release-index install_layout" ;;
  esac
else
  case "$binary" in "$home/.local/bin/olivares") ;; *) err "user binary path is outside the release-index install_layout: $binary" ;; esac
  case "$init:$config:$unit" in
    "systemd:$home/.config/olivares/olivares.env:$home/.config/systemd/user/olivares.service") default_data="$home/.local/share/olivares" ;;
    "launchd:$home/Library/Preferences/dev.olivares.olivares.env:$home/Library/LaunchAgents/dev.olivares.olivares.plist")
      default_data="$home/Library/Application Support/Olivares" ;;
    *) err "user config/unit tuple is outside the release-index install_layout" ;;
  esac
fi
if [ "$data_dir" != "$default_data" ]; then
  layout=custom
  # A custom data directory is purged as a tree, so it must be a dedicated
  # directory at least two levels deep (a top-level directory is a shared
  # system root everywhere) and must not contain the files the plan removes by
  # name. This is a shape rule, not an allowlist: no location is forbidden.
  case "$data_dir" in
    /*/*) ;;
    *) err "custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir" ;;
  esac
  for path in "$binary" "$config" "$unit"; do
    case "$path" in
      "$data_dir"|"$data_dir"/*) err "custom data directory $data_dir must not contain the installed path $path" ;;
    esac
  done
fi
case "$init" in
  openrc) case "$binary$data_dir$config" in *' '*) err "openrc paths may not contain spaces" ;; esac ;;
  systemd) case "$binary$config" in *' '*) err "systemd binary and config paths may not contain spaces" ;; esac ;;
esac
# SYSTEMD_BIND_MIN — the first systemd release in which a BindPaths= destination is
# created by systemd itself, which is what makes a bind mount under /tmp or /var/tmp
# work together with PrivateTmp=. Upstream commit a227a4be489333b1b149df124ab284d82397ff2d
# ("namespace: if we can create the destination of bind and PrivateTmp= mounts",
# 2017-09-28) says so in its own message: "we can use namespace bind mounts on dirs in
# /tmp or /var/tmp even in conjunction with PrivateTmp=". Measured against the official
# repository on 2026-09-05: that commit is an ancestor of v235 and is NOT contained in
# v234, and the maintainer closed systemd#7272 by pointing at it.
SYSTEMD_BIND_MIN=235
# systemd_major — the running manager's major version, or nothing when it cannot be
# read. `systemctl --version` prints "systemd 257 (257.13-1~deb13u1)" on its first line.
# Absence is not treated as a failure: a staging root (--root) has no manager at all,
# and this adapter must stay usable for image builds.
systemd_major() {
  have systemctl || return 0
  systemctl --version 2>/dev/null | awk 'NR == 1 && $2 ~ /^[0-9]+$/ { print $2; exit }'
}
# bind_safe <path> <what> — a path that has to be re-exposed with BindPaths= may not
# contain ":": systemd reads that value as source:destination[:options], so a colon in
# the path would silently bind somewhere else. Only the two locations that need a bind
# are checked, so a colon stays admissible everywhere it is inert.
bind_safe() {
  case "$1" in
    *:*) err "$2 $1 contains ':' and this location can only be reached with BindPaths=, whose value uses ':' to separate source from destination; choose a path without it" ;;
  esac
}
# sandbox_access <path> — how the hardened systemd unit can reach a directory.
#   plain          ReadWritePaths= re-exposes it under ProtectSystem=strict.
#   protected-home ProtectHome=true masks /home, /root and /run/user entirely, so the
#                  unit switches to ProtectHome=tmpfs and binds exactly this path
#                  (the systemd-documented pairing); other homes stay hidden.
#   private-tmp    PrivateTmp=true replaces /tmp and /var/tmp with private ones, so a
#                  directory there is NOT visible by default. BindPaths=<dir> mounts the
#                  host's directory into that private tree, and only it — the rest of
#                  the host's /tmp stays hidden. See SYSTEMD_BIND_MIN above for the
#                  version this needs and docs/RELEASE-INSTALLER.md for what has and has
#                  not been verified on a live manager.
#   api-fs         /dev, /proc and /sys hold kernel and device interfaces, not durable
#                  state, and the hardening (PrivateDevices=, ProtectKernelTunables=,
#                  ProtectControlGroups=) replaces or read-only-mounts what the service
#                  would see there. Refused: a data directory whose tree a purge removes
#                  does not belong on one.
#
# ⛔ THIS TABLE SAID, UNTIL 2026-09-05, THAT NO DIRECTIVE COULD RE-EXPOSE THE HOST'S
# /tmp AND REFUSED THE LOCATION ON THAT GROUND. The claim was false, and an
# untested assumption stated as an impossibility is worse than an unsupported
# option: it turned an adapter's debt into a product veto.
sandbox_access() {
  case "$1" in
    /home|/home/*|/root|/root/*|/run/user|/run/user/*) printf protected-home ;;
    /tmp|/tmp/*|/var/tmp|/var/tmp/*) printf private-tmp ;;
    /dev|/dev/*|/proc|/proc/*|/sys|/sys/*) printf api-fs ;;
    *) printf plain ;;
  esac
}
data_access=plain
if [ "$init" = systemd ]; then
  data_access="$(sandbox_access "$data_dir")"
  # A user service gets the SAME private /tmp from the shared template, so the bind is
  # rendered for it too. The other classes stay system-only on purpose: ProtectHome= is
  # not applied in user mode (the default user data directory lives under the operator's
  # own home, and binding it would be a change of behaviour with no defect behind it),
  # and widening the API-file-system refusal to user mode would refuse something this
  # adapter accepts today, outside what the review asked for.
  if [ "$mode" != system ] && [ "$data_access" != private-tmp ]; then data_access=plain; fi
  case "$data_access" in
    protected-home) bind_safe "$data_dir" "data directory" ;;
    private-tmp)
      bind_safe "$data_dir" "data directory"
      running="$(systemd_major)"
      if [ -n "$running" ] && [ "$running" -lt "$SYSTEMD_BIND_MIN" ]; then
        err "data directory $data_dir is under /tmp or /var/tmp and this host runs systemd $running: creating a BindPaths= destination inside the private /tmp needs systemd $SYSTEMD_BIND_MIN or later (upstream a227a4be), so the service would see its own empty /tmp instead of $data_dir; choose a location outside /tmp and /var/tmp on this host"
      fi
      ;;
    api-fs) err "data directory $data_dir is under an API file system (/dev, /proc, /sys): those hold kernel and device interfaces rather than durable state, and the hardened unit replaces or read-only-mounts what the service would see there; choose a real directory" ;;
  esac
fi
# quote_unit renders a path for a systemd or OpenRC value: bare when it has
# no whitespace, double-quoted otherwise (systemd unquotes whole or partial
# words; OpenRC never receives a path with spaces, see above).
quote_unit() {
  case "$1" in *' '*) printf '"%s"' "$1" ;; *) printf '%s' "$1" ;; esac
}

target() { printf '%s%s\n' "$root_prefix" "$1"; }
binary_target="$(target "$binary")"
unit_target="$(target "$unit")"
data_target="$(target "$data_dir")"
config_target="$(target "$config")"
asset_root="${OLIVARES_ASSET_ROOT:-$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)}"
templates="$asset_root/packaging/service"
for template in systemd.service openrc.sh launchd.xml; do
  [ -r "$templates/$template" ] || err "service template is missing: $templates/$template"
done

say "Olivares AI service installation plan"
say "  mode: $mode"
say "  init: $init"
say "  binary: $binary"
say "  config: $config (create only; existing files are preserved)"
case "$data_access" in
  protected-home) access_note=", protected-home: ProtectHome=tmpfs + BindPaths" ;;
  private-tmp) access_note=", private-tmp: PrivateTmp=true + BindPaths" ;;
  *) access_note="" ;;
esac
say "  data: $data_dir ($layout layout$access_note)"
[ "$data_access" != private-tmp ] ||
  say "  note: $data_dir is under a shared temporary directory. The unit keeps PrivateTmp=true and binds exactly this directory into it, so the rest of the host's /tmp stays hidden from the service; but /tmp and /var/tmp are world-writable and many distributions clear them on boot or on a timer (systemd-tmpfiles), which would delete this estate under a running service."
say "  unit: $unit"
say "  start: $([ "$start" -eq 1 ] && printf explicit || printf no)"
[ -z "$root_prefix" ] || say "  staging root: $root_prefix (no account or daemon mutation)"
if [ "$dry_run" -eq 1 ]; then
  say "  result: dry-run; no filesystem, account or init mutation"
  exit 0
fi

if [ "$mode" = system ] && [ -z "$root_prefix" ] && [ "$(id -u)" -ne 0 ]; then
  err "--system requires uid 0; this script never invokes sudo"
fi
[ -f "$binary_target" ] || err "verified binary is absent: $binary_target"
[ -x "$binary_target" ] || err "verified binary is not executable: $binary_target"

account_name=olivares
account_group=olivares
account_user_created=0
account_group_created=0
ensure_account() {
  [ "$mode" = system ] && [ -z "$root_prefix" ] || return 0
  if [ "$goos" = linux ]; then
    have getent || err "getent is required to verify the system service account"
    if ! getent group olivares >/dev/null 2>&1; then
      if have groupadd; then groupadd --system olivares
      elif have addgroup; then addgroup -S olivares
      else err "groupadd or addgroup is required to create group olivares"; fi
      account_group_created=1
    fi
    getent group olivares >/dev/null 2>&1 || err "group olivares was not created"
    if ! getent passwd olivares >/dev/null 2>&1; then
      if have useradd; then
        useradd --system --gid olivares --home-dir "$data_dir" --shell /usr/sbin/nologin \
          --comment "Olivares AI" olivares
      elif have adduser; then
        adduser -S -D -H -G olivares -h "$data_dir" -s /sbin/nologin olivares
      else err "useradd or adduser is required to create user olivares"; fi
      account_user_created=1
    fi
    getent passwd olivares >/dev/null 2>&1 || err "user olivares was not created"
    return 0
  fi

  # launchd has no package-manager account hook. Use a hidden, non-login account
  # in Apple's system uid range and fail instead of silently running as root.
  account_name=_olivares
  account_group=staff
  have dscl || err "dscl is required to create the launchd service account"
  if ! dscl . -read /Users/_olivares >/dev/null 2>&1; then
    uid=499
    while [ "$uid" -ge 400 ] && dscl . -search /Users UniqueID "$uid" >/dev/null 2>&1; do
      uid=$((uid - 1))
    done
    [ "$uid" -ge 400 ] || err "no unused macOS system uid in the 400-499 range"
    dscl . -create /Users/_olivares
    dscl . -create /Users/_olivares RealName "Olivares AI"
    dscl . -create /Users/_olivares UniqueID "$uid"
    dscl . -create /Users/_olivares PrimaryGroupID 20
    dscl . -create /Users/_olivares NFSHomeDirectory "$data_dir"
    dscl . -create /Users/_olivares UserShell /usr/bin/false
    account_user_created=1
  fi
  dscl . -read /Users/_olivares UniqueID >/dev/null 2>&1 ||
    err "launchd service account _olivares was not created"
}

stat_mode() {
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null ||
    err "cannot measure permissions of $1"
}
stat_uid() {
  stat -c '%u' "$1" 2>/dev/null || stat -f '%u' "$1" 2>/dev/null ||
    err "cannot measure owner of $1"
}

# A custom data directory is provisioned through the operator's own path, so
# every existing component of that path must be a real directory: a link
# anywhere along it would make mkdir/chmod/chown act on an unrelated target.
# The operator names the resolved path instead; nothing is resolved for them.
refuse_link_components() {
  prefix=""
  rest="${1#/}"
  while [ -n "$rest" ]; do
    component="${rest%%/*}"
    prefix="$prefix/$component"
    case "$rest" in */*) rest="${rest#*/}" ;; *) rest="" ;; esac
    if [ -L "$prefix" ]; then
      err "path component is a symbolic link ($prefix -> $(readlink "$prefix" 2>/dev/null || printf '?')); pass the resolved path instead of provisioning through a link: $1"
    fi
  done
}
if [ "$layout" = custom ]; then
  refuse_link_components "$data_target"
fi

data_created=0
if [ -e "$data_target" ]; then
  [ -d "$data_target" ] || err "data path is not a directory: $data_target"
  data_mode="$(stat_mode "$data_target")"
  if [ "$mode" = system ]; then
    case "$data_mode" in 700|750) ;; *) err "existing system data directory mode is $data_mode; require 0700 or 0750" ;; esac
  else
    [ "$data_mode" = 700 ] || err "existing user data directory mode is $data_mode; require 0700"
  fi
else
  if [ "$layout" = custom ]; then
    # Never create ancestors under privilege for an operator-chosen location:
    # the operator owns the parent and its ownership, this adapter owns only
    # the dedicated directory.
    [ -d "$(dirname -- "$data_target")" ] ||
      err "parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")"
    mkdir "$data_target"
  else
    mkdir -p "$data_target"
  fi
  data_created=1
  if [ "$mode" = system ]; then chmod 0750 "$data_target"; else chmod 0700 "$data_target"; fi
fi
mkdir -p "$(dirname -- "$config_target")" "$(dirname -- "$unit_target")"

config_managed=0
if [ ! -e "$config_target" ]; then
  umask 0077
  {
    printf '%s\n' '# Generated by install-service.sh; preserved on reinstall.'
    printf '%s\n' 'OLIVARES_EXTRA_ARGS='
  } >"$config_target"
  config_managed=1
  if [ "$mode" = system ]; then chmod 0640 "$config_target"; else chmod 0600 "$config_target"; fi
fi
if [ "$config_managed" -eq 0 ] &&
  grep -Fqx '# Generated by install-service.sh; preserved on reinstall.' "$config_target"; then
  config_managed=1
fi
[ -f "$config_target" ] || err "configuration path is not a regular file: $config_target"
config_mode="$(stat_mode "$config_target")"
if [ "$mode" = system ]; then
  case "$config_mode" in 400|440|600|640) ;; *) err "existing system config mode is $config_mode; require 0400, 0440, 0600 or 0640" ;; esac
else
  case "$config_mode" in 400|600) ;; *) err "existing user config mode is $config_mode; require 0400 or 0600" ;; esac
fi

validate_config_names() {
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue ;; *=*) key=${line%%=*} ;; *) err "invalid config line (expected KEY=value; value redacted): ${line%%=*}" ;; esac
    case "$key" in OLIVARES_EXTRA_ARGS) continue ;; esac
    case "$key" in OLIVARES_*) ;; *) err "invalid config key: $key" ;; esac
    case "$key" in *[!A-Z0-9_]*) err "invalid config key: $key" ;; esac
    env -i PATH="$PATH" HOME="${HOME:-/}" "$key=" "$binary_target" config validate >/dev/null 2>&1 ||
      err "configuration contains an unrecognized key: $key"
  done <"$config_target"
}
validate_config_names

# Account creation is intentionally after every pre-existing path/config check:
# an unsafe directory or unknown key must not leave a new host identity behind.
ensure_account
if [ "$mode" = system ] && [ -z "$root_prefix" ]; then
  account_uid="$(id -u "$account_name")"
  if [ "$data_created" -eq 1 ]; then
    chown "$account_name:$account_group" "$data_target"
  elif [ "$(stat_uid "$data_target")" != "$account_uid" ]; then
    err "existing data directory is not owned by $account_name; refusing recursive ownership changes"
  fi
  if [ "$(stat_uid "$config_target")" != 0 ]; then
    err "existing system config is not owned by root; refusing to take it over"
  fi
  chown "root:$account_group" "$config_target"
elif [ "$mode" = user ] && [ -z "$root_prefix" ] && [ "$(stat_uid "$data_target")" != "$(id -u)" ]; then
  err "existing user data directory is not owned by the invoking uid"
fi

render() {
  input="$1" output="$2" mode_bits="$3"
  user_line='# user service runs as the invoking account'
  group_line='# group is inherited from the invoking account'
  protect_home=false
  wanted_by=default.target
  user_key='<!-- user agent runs as the logged-in account -->'
  if [ "$mode" = system ]; then
    user_line="User=$account_name"; group_line="Group=$account_group"; protect_home=true
    wanted_by=multi-user.target
    user_key="<key>UserName</key><string>$account_name</string>"
  fi
  program="$binary"
  wrapper=""
  log_path="$data_dir/olivares.log"
  if [ "$init" = launchd ]; then
    wrapper="$data_dir/launchd-run.sh"
    program="$wrapper"
  fi
  tmp="$(mktemp "${TMPDIR:-/tmp}/olivares-service-render.XXXXXX")"
  data_value="$data_dir"
  [ "$init" = launchd ] || data_value="$(quote_unit "$data_dir")"
  # The bind is what exposes the selected directory; ProtectHome=tmpfs is only for the
  # home case. They are rendered independently: a data directory under /tmp needs the
  # bind and must NOT relax ProtectHome, and the rest of the hardening is untouched in
  # both cases.
  bind_expr='/@BIND_PATHS@/d'
  case "$data_access" in
    protected-home) protect_home=tmpfs; bind_expr="s|@BIND_PATHS@|BindPaths=$data_value|" ;;
    private-tmp) bind_expr="s|@BIND_PATHS@|BindPaths=$data_value|" ;;
  esac
  sed \
    -e "s|@USER_LINE@|$user_line|g" \
    -e "s|@GROUP_LINE@|$group_line|g" \
    -e "s|@PROTECT_HOME@|$protect_home|g" \
    -e "s|@WANTED_BY@|$wanted_by|g" \
    -e "s|@USER_KEY@|$user_key|g" \
    -e "s|@BINARY@|$binary|g" \
    -e "s|@DATA_DIR@|$data_value|g" \
    -e "s|@CONFIG@|$config|g" \
    -e "s|@PROGRAM@|$program|g" \
    -e "s|@LOG_PATH@|$log_path|g" \
    -e "$bind_expr" \
    "$input" >"$tmp"
  if grep -Eq '@[A-Z_]+@' "$tmp"; then rm -f "$tmp"; err "unresolved service template marker"; fi
  if [ -e "$output" ]; then
    if same_bytes "$tmp" "$output"; then rm -f "$tmp"; chmod "$mode_bits" "$output"; return 0; fi
    rm -f "$tmp"
    err "refusing to replace existing service file: $output"
  fi
  chmod "$mode_bits" "$tmp"
  mv "$tmp" "$output"
}

render_launchd_wrapper() {
  output="$1"
  tmp="$(mktemp "${TMPDIR:-/tmp}/olivares-launchd-wrapper.XXXXXX")"
  {
    printf '%s\n' '#!/bin/sh'
    printf 'config=%s\n' "'$config'"
    printf '%s\n' "if [ -r \"\$config\" ]; then"
    printf '%s\n' "  while IFS= read -r line || [ -n \"\$line\" ]; do"
    printf '%s\n' "    case \"\$line\" in \"\"|\"#\"*) continue ;; *=*) export \"\$line\" || exit 1 ;; *) exit 1 ;; esac"
    printf '%s\n' "  done <\"\$config\""
    printf '%s\n' 'fi'
    printf '%s\n' 'set -f'
    # --listen=:8443 is the dual-stack wildcard: the console accepts connections from
    # the network, TLS is on with a self-signed first-boot certificate and there are no
    # default credentials. OLIVARES_EXTRA_ARGS is appended, so
    # --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 in the env file restricts it.
    printf "exec %s serve --data-dir=%s --listen=:8443 --grpc-listen=:8444 --checkpoint-interval=1h \${OLIVARES_EXTRA_ARGS:-}\n" "'$binary'" "'$data_dir'"
  } >"$tmp"
  if [ -e "$output" ]; then
    if same_bytes "$tmp" "$output"; then rm -f "$tmp"; chmod 0755 "$output"; return 0; fi
    rm -f "$tmp"
    err "refusing to replace existing launchd wrapper: $output"
  fi
  chmod 0755 "$tmp"
  mv "$tmp" "$output"
}

case "$init" in
  systemd) render "$templates/systemd.service" "$unit_target" 0644 ;;
  openrc) render "$templates/openrc.sh" "$unit_target" 0755 ;;
  launchd)
    wrapper="$data_dir/launchd-run.sh"
    wrapper_target="$(target "$wrapper")"
    render_launchd_wrapper "$wrapper_target"
    render "$templates/launchd.xml" "$unit_target" 0644
    ;;
esac

manifest_target="$data_target/install-manifest.json"
manifest_logical="$data_dir/install-manifest.json"

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

# Preserve, across an idempotent reinstall or an engine upgrade, everything a
# previous run of this adapter and of install-agentops.sh recorded and this run
# does not compute for itself: the account ownership flags, the AgentOps
# workspace and the AgentOps drop-in / runtime env entries. Each carried value
# is validated against the layout THIS run renders — the workspace must be a
# canonical, service-safe absolute path, the drop-in must be THIS unit's
# agentops.conf, the runtime env must sit beside THIS config — and anything
# else is refused with the record named, never dropped in silence. The
# uninstall witness a preserve may have left is not carried: the unit rendered
# by this run is the witness again.
carried_workspace=""
carried_dropin_path=""
carried_dropin_managed=""
carried_env_path=""
carried_env_managed=""
if [ -r "$manifest_target" ]; then
  ownership_record="$(read_ownership_record "$manifest_target")" ||
    err "the previous ownership manifest cannot be read: $manifest_logical (repair it, or move it aside to reinstall without its records)"
  tab="$(printf '\t')"
  recorded_data=""
  dropin_count=0
  env_count=0
  carried_dropin_mode=""
  carried_env_mode=""
  # Every loop variable is prefixed: `read` assigns in the CURRENT shell, and a bare
  # `mode`/`value` here would overwrite this script's own $mode (user|system) with a
  # file mode from the record. That is not hypothetical — it happened, and everything
  # downstream that branches on $mode (the manifest's own "mode", the config file mode,
  # the account block, the manifest permissions, the final result JSON) silently
  # switched to the user branch, so an upgraded estate recorded `"mode": ""` and the
  # engine then refused its own manifest with `unexpected install mode ""`.
  while IFS="$tab" read -r rec_kind rec_key rec_value rec_mode rec_managed; do
    case "$rec_kind" in
      field)
        case "$rec_key" in
          data_dir) recorded_data="$rec_value" ;;
          workspace_dir) carried_workspace="$rec_value" ;;
        esac ;;
      account)
        case "$rec_key=$rec_value" in
          user_created=true) account_user_created=1 ;;
          group_created=true) account_group_created=1 ;;
        esac ;;
      file)
        case "$rec_key" in
          dropin)
            dropin_count=$((dropin_count + 1))
            carried_dropin_path="$rec_value"; carried_dropin_mode="$rec_mode"; carried_dropin_managed="$rec_managed" ;;
          runtime-env)
            env_count=$((env_count + 1))
            carried_env_path="$rec_value"; carried_env_mode="$rec_mode"; carried_env_managed="$rec_managed" ;;
        esac ;;
    esac
  done <<OWNERSHIP_RECORD
$ownership_record
OWNERSHIP_RECORD
  [ "$recorded_data" = "$data_dir" ] ||
    err "the previous manifest at $manifest_logical owns data directory ${recorded_data:-<none recorded>}, not $data_dir; repair $manifest_logical"
  if [ -n "$carried_workspace" ]; then
    safe_path "$carried_workspace"
    clean_path "$carried_workspace"
  fi
  if [ "$dropin_count" -gt 0 ]; then
    [ "$dropin_count" = 1 ] || err "previous manifest records more than one AgentOps drop-in; repair $manifest_logical"
    [ "$init" = systemd ] || err "previous manifest records an AgentOps drop-in but this adapter is $init; repair $manifest_logical"
    [ "$carried_dropin_path" = "$unit.d/agentops.conf" ] ||
      err "previous manifest records an AgentOps drop-in at $carried_dropin_path, not $unit.d/agentops.conf; repair $manifest_logical"
    [ "$carried_dropin_mode" = 0644 ] ||
      err "previous manifest records the AgentOps drop-in with mode $carried_dropin_mode, not 0644; repair $manifest_logical"
  fi
  if [ "$env_count" -gt 0 ]; then
    [ "$env_count" = 1 ] || err "previous manifest records more than one AgentOps runtime env; repair $manifest_logical"
    [ "$carried_env_path" = "$(dirname -- "$config")/agentops.env" ] ||
      err "previous manifest records an AgentOps runtime env at $carried_env_path, not $(dirname -- "$config")/agentops.env; repair $manifest_logical"
    [ "$carried_env_mode" = 0640 ] ||
      err "previous manifest records the AgentOps runtime env with mode $carried_env_mode, not 0640; repair $manifest_logical"
  fi
fi
bool() { [ "$1" -eq 1 ] && printf true || printf false; }
wrapper_json=""
[ "$init" != launchd ] || wrapper_json=",\n    {\"path\": \"$data_dir/launchd-run.sh\", \"role\": \"wrapper\", \"mode\": \"0755\", \"managed\": true}"
[ -z "$carried_dropin_path" ] ||
  wrapper_json="$wrapper_json,\n    {\"path\": \"$carried_dropin_path\", \"role\": \"dropin\", \"mode\": \"0644\", \"managed\": $carried_dropin_managed}"
[ -z "$carried_env_path" ] ||
  wrapper_json="$wrapper_json,\n    {\"path\": \"$carried_env_path\", \"role\": \"runtime-env\", \"mode\": \"0640\", \"managed\": $carried_env_managed}"
umask 0077
{
	  printf '{\n  "schema": "olivares.ai/local-install/v2",\n'
	  printf '  "mode": "%s",\n  "init": "%s",\n  "layout": "%s",\n' "$mode" "$init" "$layout"
	  printf '  "data_dir": "%s",\n  "config": "%s",\n' "$data_dir" "$config"
	  [ -z "$carried_workspace" ] || printf '  "workspace_dir": "%s",\n' "$carried_workspace"
	  printf '  "files": [\n'
	  printf '    {"path": "%s", "role": "binary", "mode": "0755", "managed": %s},\n' "$binary" "$(bool "$managed_binary")"
	  printf '    {"path": "%s", "role": "config", "mode": "%s", "managed": %s},\n' "$config" "$([ "$mode" = system ] && printf 0640 || printf 0600)" "$(bool "$config_managed")"
	  printf '    {"path": "%s", "role": "unit", "mode": "%s", "managed": true}%b\n' "$unit" "$([ "$init" = openrc ] && printf 0755 || printf 0644)" "$wrapper_json"
	  printf '  ],\n'
	  if [ "$mode" = system ]; then
	    printf '  "account": {"user": "%s", "group": "%s", "user_created": %s, "group_created": %s},\n' \
	      "$account_name" "$account_group" "$(bool "$account_user_created")" "$(bool "$account_group_created")"
	  else
	    printf '  "account": {"user_created": false, "group_created": false},\n'
	  fi
	  printf '  "manifest": "%s"\n}\n' "$manifest_logical"
} >"$manifest_target"
if [ "$mode" = system ]; then chmod 0640 "$manifest_target"; else chmod 0600 "$manifest_target"; fi

# Do not report a complete live installation if a pre-existing, byte-identical
# control file is owned by another uid. Offline roots defer ownership to the
# image/package installer and are deliberately excluded from this host check.
if [ -z "$root_prefix" ]; then
  if [ "$mode" = system ]; then expected_uid=0; else expected_uid="$(id -u)"; fi
  for owned_path in "$unit_target" "$manifest_target"; do
    [ "$(stat_uid "$owned_path")" = "$expected_uid" ] ||
      err "installed control file is not owned by expected uid $expected_uid: $owned_path"
  done
  if [ "$init" = launchd ]; then
    [ "$(stat_uid "$wrapper_target")" = "$expected_uid" ] ||
      err "installed launchd wrapper is not owned by expected uid $expected_uid: $wrapper_target"
  fi
fi

# First-boot wait bounds. FIRST_BOOT_DEADLINE is the wall-clock limit on the
# readiness polling; FIRST_BOOT_MAX_ATTEMPTS caps the work even if every attempt
# returns instantly; the two request limits bound a single request and are
# clipped to whatever is left of the deadline. FIRST_BOOT_DIAGNOSIS_TIMEOUT is a
# separate bound on the one optional probe run after the wait has already ended.
FIRST_BOOT_DEADLINE=60
FIRST_BOOT_MAX_ATTEMPTS=60
FIRST_BOOT_CONNECT_TIMEOUT=2
FIRST_BOOT_REQUEST_TIMEOUT=5
FIRST_BOOT_DIAGNOSIS_TIMEOUT=5

# first_boot_tick refreshes probe_elapsed and probe_remaining.
#
# TIMING, stated rather than implied. This is a WHOLE-SECOND WALL-CLOCK budget
# read from `date +%s`, and POSIX shell has no monotonic clock, so it is subject
# to clock adjustment. The clamp below does NOT make it monotonic: a backward
# step STALLS probe_elapsed until the clock catches up, so a real wait can run
# past FIRST_BOOT_DEADLINE by roughly the size of that step. All the clamp does
# is stop the counter moving backwards, which would otherwise hand the wait
# extra budget. What does not depend on the clock at all is
# FIRST_BOOT_MAX_ATTEMPTS and each request's own connect and total limits. A
# forward step ends the wait sooner. The deadline bounds when a request may
# START and, through the clip below, how long it may run; the total can also
# exceed it by curl's own shutdown granularity.
first_boot_tick() {
  probe_spent=$(( $(date +%s) - probe_started ))
  if [ "$probe_spent" -gt "$probe_elapsed" ]; then probe_elapsed="$probe_spent"; fi
  probe_remaining=$(( FIRST_BOOT_DEADLINE - probe_elapsed ))
}

# first_boot_clip <seconds> — that limit, or the remaining budget if it is
# smaller. Never zero: curl reads --max-time 0 and --connect-timeout 0 as NO
# timeout, so a clip that reached zero would unbound the request it bounds.
# Callers only reach here with probe_remaining >= 1; the floor is the guard for
# the day one of them stops checking.
first_boot_clip() {
  if [ "$probe_remaining" -lt "$1" ]; then
    if [ "$probe_remaining" -lt 1 ]; then printf 1; else printf '%s' "$probe_remaining"; fi
  else
    printf '%s' "$1"
  fi
}

# first_boot_log_hint names the init-specific window that holds the one-time
# setup token. It is printed after a successful install and again, on stderr,
# when a first boot fails: one definition, so the two paths cannot drift.
first_boot_log_hint() {
  case "$init:$mode" in
    systemd:system) say "first-boot setup: journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'" ;;
    systemd:user) say "first-boot setup: journalctl --user -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'" ;;
    openrc:*) say "first-boot setup: sed -n '/FIRST-BOOT SETUP/,/========================/p' '$data_dir/olivares.log'" ;;
    launchd:*) say "first-boot setup: sed -n '/FIRST-BOOT SETUP/,/========================/p' '$data_dir/olivares.log'" ;;
  esac
}

# first_boot_diagnosis runs ONE optional probe after the wait has already ended,
# under its own FIRST_BOOT_DIAGNOSIS_TIMEOUT — not the polling deadline, which is
# spent by then. It asks the binary this run installed, already verified before
# it was placed and already trusted here to answer `config validate`. That probe
# reaches numeric loopback only, carries no credential, follows no redirect and
# ignores ambient proxies. For a recognized not-ready state it prints fixed
# sentences held in the binary, so no response body or header reaches this
# output. A verdict it could NOT measure is different and is piped here too: it
# reports the input, TLS or transport failure that stopped it, in that error's
# own unfiltered words. The probe only reaches numeric loopback, so that text
# describes this host's own engine, but nothing classifies or redacts it. The
# installer never parses a readiness body itself: the bound and the refusal to
# echo a body stay in one reviewed place. An older binary has no `readyz`
# subcommand, which is said plainly rather than guessed around.
first_boot_diagnosis() {
  if ! env -i PATH="$PATH" HOME="${HOME:-/}" "$binary_target" readyz --help >/dev/null 2>&1; then
    diag "local readiness diagnosis is unavailable: this build has no 'olivares readyz' subcommand"
  elif [ ! -r "$data_dir/tls.crt" ]; then
    diag "local readiness diagnosis is unavailable: $data_dir/tls.crt is not readable, so the"
    diag "engine has not written its own TLS material yet"
  else
    diag "local readiness diagnosis (one ${FIRST_BOOT_DIAGNOSIS_TIMEOUT}s probe, separate from the wait above):"
    env -i PATH="$PATH" HOME="${HOME:-/}" "$binary_target" readyz \
      --server https://127.0.0.1:8443 --ca-cert "$data_dir/tls.crt" \
      --timeout "${FIRST_BOOT_DIAGNOSIS_TIMEOUT}s" 2>&1 |
      sed -n '1,6s/^/  /p' >&2 || true
  fi
  # err() exits before the success path prints this, and after a failed first
  # boot the engine's own log is the only place left to look.
  first_boot_log_hint >&2
}

if [ "$start" -eq 1 ]; then
  # This validation is deliberately adjacent to and before every init mutation.
  validate_config_names
  case "$init:$mode" in
    systemd:system) systemctl daemon-reload; systemctl enable --now olivares ;;
    systemd:user) systemctl --user daemon-reload; systemctl --user enable --now olivares ;;
    openrc:system) rc-update add olivares default; rc-service olivares start ;;
    launchd:system) launchctl bootstrap system "$unit" ;;
    launchd:user) launchctl bootstrap "gui/$(id -u)" "$unit" ;;
  esac
  have curl || err "curl is required to verify first boot"
  # The remaining budget is read before every request and before every sleep,
  # and each request's own limits are clipped to it, so no request starts or
  # runs past the deadline. A response that arrives after it is a LATE answer:
  # the wait it belonged to is over, and accepting it would report an unready
  # deployment as healthy.
  probe_ok=0
  probe_late=0
  attempts=0
  probe_elapsed=0
  probe_remaining="$FIRST_BOOT_DEADLINE"
  probe_started="$(date +%s)"
  while :; do
    attempts=$((attempts + 1))
    probe_ok=1
    [ -r "$data_dir/tls.crt" ] || probe_ok=0
    for probe_path in livez readyz; do
      [ "$probe_ok" -eq 1 ] || break
      first_boot_tick
      if [ "$probe_remaining" -lt 1 ]; then probe_ok=0; break; fi
      curl -fsS --cacert "$data_dir/tls.crt" \
        --connect-timeout "$(first_boot_clip "$FIRST_BOOT_CONNECT_TIMEOUT")" \
        --max-time "$(first_boot_clip "$FIRST_BOOT_REQUEST_TIMEOUT")" \
        "https://127.0.0.1:8443/$probe_path" >/dev/null 2>&1 || probe_ok=0
    done
    first_boot_tick
    if [ "$probe_ok" -eq 1 ]; then
      if [ "$probe_remaining" -ge 1 ]; then break; fi
      probe_late=1
      probe_ok=0
    fi
    [ "$attempts" -lt "$FIRST_BOOT_MAX_ATTEMPTS" ] && [ "$probe_remaining" -ge 1 ] || break
    sleep 1
  done
  if [ "$probe_ok" -ne 1 ]; then
    [ "$probe_late" -eq 0 ] ||
      diag "both endpoints answered, but only after the ${FIRST_BOOT_DEADLINE}s wait had ended; a late answer is not this wait's result"
    first_boot_diagnosis
    err "service started but /livez and /readyz were not both healthy: gave up after $attempts attempt(s) over ${probe_elapsed}s, bounded at $FIRST_BOOT_MAX_ATTEMPTS attempts and ${FIRST_BOOT_DEADLINE}s; the service, its configuration and its data were left exactly as installed and nothing was rolled back"
  fi
fi

say "service installation complete; manifest: $manifest_logical"
printf 'result-json: {"schema":"olivares.ai/service-install-result/v1","mode":"%s","init":"%s","started":%s,"manifest":"%s"}\n' \
  "$mode" "$init" "$([ "$start" -eq 1 ] && printf true || printf false)" "$manifest_logical"
first_boot_log_hint
[ "$start" -eq 1 ] || say "service not started; re-run with --start only after reviewing $config"
