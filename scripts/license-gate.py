#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Check a build's third-party Go licenses and optionally generate its NOTICE.

Run from the assembled build root. Enterprise supplies its build tags and package
paths to this same script. Install github.com/google/go-licenses/v2@v2.0.1 first.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

# The same policy applies to both editions. Unknown licenses fail closed.
ALLOWED = {'MIT', 'BSD-2-Clause', 'BSD-3-Clause', 'ISC', 'Apache-2.0', 'MPL-2.0'}


def check_license(name: str, license_name: str) -> None:
    if license_name not in ALLOWED:
        raise ValueError(f'{name}: disallowed license {license_name!r}')


def check_mpl(module: dict) -> None:
    if (module.get('Replace') or module.get('Main') or not module.get('Version')
            or 'vendor' in Path(module.get('Dir', '')).parts):
        raise ValueError(f'{module["Path"]}: MPL-2.0 must be an unmodified, versioned module; '
                         'local, vendored and replaced copies are refused')


def parse_report(text: str) -> list[list[str]]:
    rows = [line.split('\t') for line in text.splitlines() if line]
    if not rows or any(len(row) != 4 or not all(row) for row in rows):
        raise ValueError('empty or malformed go-licenses report')
    return rows


def dependency_notice(name: str, version: str, license_path: Path, module_dir: Path) -> str:
    license_path = license_path.resolve()
    module_dir = module_dir.resolve()
    license_path.relative_to(module_dir)
    text = f'\n=== {name} {version} ===\n\n' + license_path.read_text()
    # A module may ship package-specific Apache notices below its shared license.
    for notice in sorted(module_dir.rglob('*')):
        if notice.is_file() and notice.name.upper() in ('NOTICE', 'NOTICE.TXT', 'NOTICE.MD'):
            notice.resolve().relative_to(module_dir)
            text += f'\n\n--- {notice.relative_to(module_dir)} ---\n' + notice.read_text()
    return text + '\n'


def console_notice(path: Path) -> str:
    entries = json.loads(path.read_text())
    if not isinstance(entries, list) or not entries:
        raise ValueError('empty console license output; build the console first')
    text = ''
    for entry in entries:
        name, version = entry['name'], entry['version']
        if not name or '..' in name.split('/') or name.startswith('/'):
            raise ValueError('invalid console dependency name')
        # Vite's text is optional; preserve an absent text field explicitly.
        # Preserve that fact in the notice rather than inventing license text.
        text += f'\n=== {name} {version} ===\nLicense: {entry.get("identifier", "see text")}\n\n'
        text += entry.get('text') or '[No license text included in the Vite report.]'
        text += '\n'
        # Vite includes LICENSE text but not Apache NOTICE files. Resolve the
        # exact bundled version in pnpm's installed tree before collecting them.
        node_modules = Path('web/node_modules')
        candidates = [node_modules / name]
        candidates += list((node_modules / '.pnpm').glob('*/node_modules/' + name))
        matched = False
        for candidate in candidates:
            package = candidate / 'package.json'
            if package.is_file() and json.loads(package.read_text()).get('version') == version:
                matched = True
                for notice in sorted(candidate.iterdir()):
                    if notice.is_file() and notice.name.upper() in ('NOTICE', 'NOTICE.TXT', 'NOTICE.MD'):
                        notice.resolve().relative_to(candidate.resolve())
                        text += f'\n--- {name}/{notice.name} ---\n' + notice.read_text() + '\n'
                break
        if not matched:
            raise ValueError(f'{name} {version}: bundled package unavailable for NOTICE collection')
    return text


def run(args: list[str], env: dict[str, str]) -> str:
    return subprocess.run(args, env=env, check=True, text=True, stdout=subprocess.PIPE).stdout


def modules_from_json(text: str) -> list[dict]:
    decoder = json.JSONDecoder()
    modules = []
    while text.strip():
        module, end = decoder.raw_decode(text.lstrip())
        modules.append(module)
        text = text.lstrip()[end:]
    return modules


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tags', default='release')
    parser.add_argument('--target', action='append', default=[], metavar='GOOS/GOARCH')
    parser.add_argument('--notice', type=Path, help='write generated edition NOTICE here')
    parser.add_argument('--edition', default='Community')
    parser.add_argument('--web-license', type=Path, default=Path('core/internal/webui/dist/licenses/console.json'))
    parser.add_argument('packages', nargs='*')
    args = parser.parse_args()
    env = dict(os.environ)
    env['GOFLAGS'] = env.get('GOFLAGS', '') + ' -tags=' + args.tags
    env.setdefault('CGO_ENABLED', '0')
    if not args.packages:
        args.packages = ['./cmd/olivares'] + run(['bash', 'scripts/build-connectors.sh', '--list'], env).splitlines()
    module_map = {}
    with tempfile.TemporaryDirectory(prefix='license-gate-') as tmp:
        template = Path(tmp) / 'report.tpl'
        template.write_text('{{range .}}{{printf "%s\\t%s\\t%s\\t%s\\n" .Name .Version .LicenseName .LicensePath}}{{end}}')
        rows = []
        for target in args.target or ['']:
            target_env = dict(env)
            if target:
                parts = target.split('/')
                if len(parts) != 2 or not all(part.isalnum() for part in parts):
                    raise ValueError(f'invalid build target: {target}')
                target_env.update(GOOS=parts[0], GOARCH=parts[1])
            rows.extend(parse_report(run(['go-licenses', 'report', '--template', str(template), *args.packages], target_env)))
            packages = modules_from_json(run(['go', 'list', '-deps', '-json=Module', *args.packages], target_env))
            module_map.update({p['Module']['Path']: p['Module'] for p in packages if p.get('Module')})
        rows = sorted(set(tuple(row) for row in rows))
    modules = list(module_map.values())
    notices = []
    mpl = False
    checked = 0
    for name, version, license_name, license_file in rows:
        matches = [m for m in modules if name == m['Path'] or name.startswith(m['Path'] + '/')]
        if not matches:
            raise ValueError(f'{name}: no module metadata')
        module = max(matches, key=lambda m: len(m['Path']))
        if module.get('Main'):
            # go.work modules are first-party; they must also be source in this tree.
            Path(module['Dir']).resolve().relative_to(Path.cwd().resolve())
            continue
        check_license(name, license_name)
        if license_name == 'MPL-2.0':
            check_mpl(module)
            mpl = True
        directory = module.get('Replace', module).get('Dir')
        if not directory:
            raise ValueError(f'{name}: module directory unavailable')
        notices.append(dependency_notice(name, version, Path(license_file), Path(directory)))
        checked += 1
    if not checked:
        raise ValueError('no third-party dependencies inspected')
    if mpl:
        # Verify downloaded source against Go's recorded module hashes, not an
        # assertion that a replacement or vendored MPL tree is unchanged.
        subprocess.run(['go', 'mod', 'verify'],
                       cwd=next(m['Dir'] for m in modules if m.get('Main')), env=env, check=True)
    if args.notice:
        console = console_notice(args.web_license)
        text = Path('NOTICE').read_text() + f'\n{args.edition} distribution dependencies\n'
        text += ''.join(sorted(set(notices))) + '\nConsole dependencies\n\n' + console
        args.notice.parent.mkdir(parents=True, exist_ok=True)
        # Do not leave a partial artifact if license collection fails.
        with tempfile.NamedTemporaryFile(mode='w', dir=args.notice.parent, delete=False) as out:
            temporary = Path(out.name)
            out.write(text)
        temporary.chmod(0o644)
        temporary.replace(args.notice)
    print(f'license-gate: {checked} third-party libraries allowed (tags={args.tags})')


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        print(f'license-gate: {error}', file=sys.stderr)
        sys.exit(1)
