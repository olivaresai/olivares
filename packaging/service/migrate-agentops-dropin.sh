#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -eu

# Replace only a complete 26.10-generated drop-in. The layout substitutions below
# reverse that release's renderer; the digest covers every other byte, including
# comments, directive order and the final newline. A marker alone proves nothing.
dropin=${1:?drop-in path required}
[ -e "$dropin" ] || [ -L "$dropin" ] || exit 0
keep() { printf 'Olivares AI: kept existing drop-in %s; it overrides the base unit (olivares doctor reports it).\n' "$dropin"; exit 0; }
[ -f "$dropin" ] && [ ! -L "$dropin" ] || keep
[ "$(tail -c 1 "$dropin" | od -An -tu1 | tr -d ' ')" = 10 ] || keep
home=$(awk '/^Environment=HOME=/ { n++; v=substr($0,18) } END { if(n!=1) exit 1; print v }' "$dropin") || keep
workspace=$(awk '/^ReadWritePaths=/ { n++; v=substr($0,16) } END { if(n!=1) exit 1; print v }' "$dropin") || keep
case "$home" in \"*\") home=${home#\"}; home=${home%\"} ;; esac
case "$workspace" in -*) workspace=${workspace#-} ;; esac
case "$workspace" in \"*\") workspace=${workspace#\"}; workspace=${workspace%\"} ;; esac
case "$home" in */claude-home) data=${home%/claude-home} ;; *) keep ;; esac
for path in "$data" "$workspace"; do
  case "$path" in /*/*) ;; *) keep ;; esac
  case "$path" in
    */|*//*|*/./*|*/../*|*/.|*/..|*'|'*|*'&'*|*'<'*|*'>'*|*'@'*|*\\*|*'"'*|*"'"*|*'$'*|*'%'*|*';'*|*'#'*|*[![:print:]]*) keep ;;
  esac
done
quote() { case "$1" in *' '*) printf '"%s"' "$1" ;; *) printf '%s' "$1" ;; esac; }
pre="ExecStartPre=/usr/bin/install -d -m 0750 $(quote "$workspace")"
rw="ReadWritePaths=-$(quote "$workspace")"
if [ "$workspace" != "$data/workspaces" ]; then
  pre="# workspace $workspace is an explicitly selected external directory: not created or re-moded at start"
  rw="ReadWritePaths=$(quote "$workspace")"
fi
protect=0; bind=0
case "$workspace" in
  /home/*|/root/*|/run/user/*) protect=1; bind=1 ;;
  /tmp/*|/var/tmp/*) bind=1 ;;
esac
[ "$bind" -eq 0 ] || { case "$workspace" in *:*) keep ;; esac; }
# Snapshot in the same directory; compare again before replacing. Preserve an
# operator's concurrent edit and never follow a replacement symlink.
snapshot=$(mktemp "$dropin.upgrade.XXXXXX")
normalized=$(mktemp "$dropin.normalized.XXXXXX")
trap 'rm -f "$snapshot" "$normalized"' EXIT HUP INT TERM
cp -p "$dropin" "$snapshot"
if ! awk -v d="$data" -v w="$workspace" -v h="$(quote "$home")" -v run="$(quote "$data/run")" \
  -v pre="$pre" -v rw="$rw" -v bp="BindPaths=$(quote "$workspace")" -v ph="$protect" -v nb="$bind" '
  $0 == "# Rendered layout: data " d " · workspace " w { print "# Rendered layout: data @DATA_DIR@ · workspace @WORKSPACE_DIR@"; next }
  $0 == "EnvironmentFile=-/etc/olivares/agentops.env" { print "EnvironmentFile=-@RUNTIME_ENV@"; next }
  $0 == "Environment=HOME=" h { print "Environment=HOME=@CLAUDE_HOME@"; next }
  $0 == "ExecStartPre=/usr/bin/install -d -m 0750 " h { print "ExecStartPre=/usr/bin/install -d -m 0750 @CLAUDE_HOME@"; next }
  $0 == "ExecStartPre=/usr/bin/install -d -m 0700 " run { print "ExecStartPre=/usr/bin/install -d -m 0700 @RUN_DIR@"; next }
  $0 == pre { print "@WORKSPACE_PRE@"; next }
  $0 == rw { rwline=NR; print "ReadWritePaths=@WORKSPACE_RW@"; next }
  $0 == "ProtectHome=tmpfs" { if (rwline == 0 || ph != 1 || NR != rwline+1) exit 1; np++; next }
  $0 == bp { if (rwline == 0 || nb != 1 || NR != rwline+ph+1) exit 1; nbind++; next }
  { print }
  END { if (np != ph || nbind != nb) exit 1 }
' "$snapshot" > "$normalized"; then keep; fi
digest=$(sha256sum "$normalized" | cut -d' ' -f1)
# SHA-256 of the shipped 26.10 template with its two access markers removed.
if [ "$digest" != 79baa9597505c950795823497e7ae549e22a06a2115eb6ad7d47929132d0e3b8 ]; then keep; fi
# Keep the installed HOME and runtime configuration: subscriptions already stored
# there must remain usable. Replace the old directory masks with the base unit's
# policy, including when this drop-in accompanies an older native unit.
cp -p "$snapshot" "$normalized"
cat > "$normalized" <<EOF
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Managed by install-agentops.sh (olivares.ai/agentops-dropin/v1): regenerated on reinstall.
# Migrated from the unedited Olivares AI 26.10 AgentOps layout.
[Service]
EnvironmentFile=-/etc/olivares/agentops.env
Environment=HOME=$(quote "$home")
Environment=DISABLE_AUTOUPDATER=1
ExecStartPre=/usr/bin/install -d -m 0750 $(quote "$home")
ExecStartPre=/usr/bin/install -d -m 0700 $(quote "$data/run")
$pre
$rw
ProtectSystem=full
ProtectHome=false
PrivateTmp=false
MemoryDenyWriteExecute=false
SystemCallFilter=@system-service landlock_create_ruleset landlock_add_rule landlock_restrict_self
EOF
[ ! -L "$dropin" ] && cmp -s "$snapshot" "$dropin" || keep
mv -T "$normalized" "$dropin"
printf 'Olivares AI: upgraded generated AgentOps drop-in %s; chosen folders are available to agents and MCP.\n' "$dropin"
