#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Run from the candidate checkout so packaging and its helpers share one revision.
set -euo pipefail
bash scripts/test-service-install.sh
nfpm="$(bash scripts/ensure-nfpm.sh --dest "$RUNNER_TEMP/package-tools")"
OLIVARES_NFPM="$nfpm" python3 scripts/test-native-release-packages.py \
  NativeReleaseVersions.test_packages_render_systemd_from_template
mkdir -p "$RUNNER_TEMP/package-service/logs"
/usr/bin/python3 - <<'PY'
import json, os, subprocess
from pathlib import Path
source = next(p for p in json.loads(Path('packaging/nfpm/packages.json').read_text())['nfpms']
              if p['id'] == 'olivares-nfpm')
config = {key: source[key] for key in ('vendor', 'homepage', 'maintainer', 'description',
          'license', 'section', 'priority', 'scripts')}
config.update(name=source['package_name'], arch='amd64', platform='linux',
              version=subprocess.check_output(['sh', 'scripts/build-ldflags.sh', '--version'], text=True).strip().lstrip('v')
                      + '~ci.' + os.environ['GITHUB_RUN_ID'], version_schema='none')
for fmt in ('deb', 'rpm'):
    config['contents'] = [{k: v for k, v in entry.items() if k != 'packager'}
                          for entry in source['contents'] if entry.get('packager', fmt) == fmt]
    config['contents'].append({'src': 'bin/olivares', 'dst': '/usr/bin/olivares',
                               'file_info': {'mode': 0o755}})
    Path(os.environ['RUNNER_TEMP'], f'package-service/nfpm-{fmt}.json').write_text(json.dumps(config))
PY
for fmt in deb rpm; do
  "$nfpm" package --config "$RUNNER_TEMP/package-service/nfpm-$fmt.json" \
    --packager "$fmt" --target "$RUNNER_TEMP/package-service/candidate.$fmt"
done
