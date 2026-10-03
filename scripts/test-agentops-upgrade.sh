#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail

# Exercise the actual migration and native installer in an offline 26.10 layout.
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/olivares-agentops-upgrade.XXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
helper="$root/packaging/service/migrate-agentops-dropin.sh"
dropin="$scratch/agentops.conf"

render_old() {
  python3 - "$root/scripts/fixtures/package-upgrade/agentops-26.10.conf" "$dropin" "$1" <<'PY'
from pathlib import Path
import sys
src, dst, workspace = sys.argv[1:]
data = '/var/lib/olivares'
quote = lambda v: '"'+v+'"' if ' ' in v else v
default = workspace == data+'/workspaces'
values = {
 'DATA_DIR': data, 'WORKSPACE_DIR': workspace,
 'RUNTIME_ENV': '/etc/olivares/agentops.env', 'CLAUDE_HOME': quote(data+'/claude-home'),
 'RUN_DIR': quote(data+'/run'),
 'WORKSPACE_PRE': 'ExecStartPre=/usr/bin/install -d -m 0750 '+quote(workspace) if default else
   '# workspace '+workspace+' is an explicitly selected external directory: not created or re-moded at start',
 'WORKSPACE_RW': ('-' if default else '')+quote(workspace),
}
text = Path(src).read_text()
for key, value in values.items(): text = text.replace('@'+key+'@', value)
home = workspace.startswith(('/home/', '/root/', '/run/user/'))
bind = home or workspace.startswith(('/tmp/', '/var/tmp/'))
text = text.replace('@WORKSPACE_PROTECT_HOME@\n', 'ProtectHome=tmpfs\n' if home else '')
text = text.replace('@WORKSPACE_BIND@\n', 'BindPaths='+quote(workspace)+'\n' if bind else '')
Path(dst).write_text(text)
PY
  chmod 0644 "$dropin"
}

for workspace in /var/lib/olivares/workspaces /home/operator/project '/home/operator/my project' /tmp/project /srv/project; do
  render_old "$workspace"
  /bin/sh "$helper" "$dropin"
  grep -qx 'ProtectHome=false' "$dropin"
  grep -qx 'ProtectSystem=full' "$dropin"
  grep -qx 'PrivateTmp=false' "$dropin"
  grep -qx 'MemoryDenyWriteExecute=false' "$dropin"
  grep -qx 'Environment=HOME=/var/lib/olivares/claude-home' "$dropin"
  grep -Fq "$workspace" "$dropin"
  [[ "$(stat -c %a "$dropin")" == 644 ]]
  cp "$dropin" "$scratch/once"
  /bin/sh "$helper" "$dropin" >/dev/null
  cmp "$dropin" "$scratch/once"
  printf 'ok - exact 26.10 layout upgraded, HOME retained: %s\n' "$workspace"
done

for edit in comment directive duplicate reordered leading-home leading-tmp truncated; do
  render_old /home/operator/project
  case "$edit" in
    comment) printf '# my local policy\n' >> "$dropin" ;;
    directive) sed -i 's/MemoryDenyWriteExecute=false/MemoryDenyWriteExecute=true/' "$dropin" ;;
    duplicate) printf 'ProtectHome=tmpfs\n' >> "$dropin" ;;
    reordered) sed -i '/^ProtectHome=tmpfs$/d; /\[Service\]/a ProtectHome=tmpfs' "$dropin" ;;
    leading-home) sed -i '/^ProtectHome=tmpfs$/d; /^BindPaths=/d' "$dropin"; sed -i '1i ProtectHome=tmpfs\nBindPaths=/home/operator/project' "$dropin" ;;
    leading-tmp) render_old /tmp/project; sed -i '/^BindPaths=/d' "$dropin"; sed -i '1i BindPaths=/tmp/project' "$dropin" ;;
    truncated) truncate -s -1 "$dropin" ;;
  esac
  cp "$dropin" "$scratch/custom"
  /bin/sh "$helper" "$dropin" | grep -q 'kept existing drop-in.*overrides the base unit'
  cmp "$dropin" "$scratch/custom"
  printf 'ok - hand edit preserved byte for byte: %s\n' "$edit"
done
rm "$dropin"
cp "$scratch/custom" "$scratch/linked-before"
ln -s "$scratch/custom" "$dropin"
/bin/sh "$helper" "$dropin" >/dev/null
[[ -L "$dropin" ]]
cmp "$scratch/custom" "$scratch/linked-before"
printf 'ok - linked drop-in preserved\n'

image="$scratch/native"
mkdir -p "$image/etc/systemd/system" "$image/usr/local/bin"
printf '#!/bin/sh\nexit 0\n' > "$image/usr/local/bin/olivares"
chmod 0755 "$image/usr/local/bin/olivares"
sed -e 's|@USER_LINE@|User=olivares|' -e 's|@GROUP_LINE@|Group=olivares|' \
  -e 's|@CONFIG@|/etc/olivares/olivares.env|g' -e 's|@BINARY@|/usr/local/bin/olivares|g' \
  -e 's|@DATA_DIR@|/var/lib/olivares|g' -e 's|@PROTECT_HOME@|true|' \
  -e '/@BIND_PATHS@/d' -e 's|@WANTED_BY@|multi-user.target|' \
  "$root/packaging/service/systemd-26.10.service" > "$image/etc/systemd/system/olivares.service"
/bin/sh "$root/scripts/install-service.sh" --system --init systemd --root "$image" --binary /usr/local/bin/olivares
grep -qx 'ProtectHome=false' "$image/etc/systemd/system/olivares.service"
printf 'ok - native 26.10 base unit upgraded\n'
