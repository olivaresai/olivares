#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Capture existing generators and literal commands in a fresh account home."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.parse


def generic_json_operations(root):
    # Read the native generated operations, never a separately maintained route list.
    layouts = (
        ('clients/go/operations.gen.go', r'func \(c \*Client\)[^\n]+\(map\[string\]any, error\) \{\n\s*return c\.do\w*\(ctx, "([A-Z]+)", "([^\"]+)"',
         'clients/go/client.go', 'var out map[string]any', 'json.Unmarshal(raw, &out)', 'return out, nil'),
        ('clients/python/src/olivares_client/_operations.py', r'return self\._do\w*\("([A-Z]+)", "([^\"]+)"',
         'clients/python/src/olivares_client/_core.py', 'out = json.loads(raw)', 'if not isinstance(out, dict):', 'return out'),
        ('clients/typescript/src/operations.gen.ts', r'\): Promise<Json> \{\n\s*return this\.do\w*\("([A-Z]+)", "([^\"]+)"',
         'clients/typescript/src/core.ts', 'parsed = JSON.parse(raw)', 'typeof parsed !== "object"', 'return parsed as Json;'),
        ('clients/java/src/main/java/ai/olivares/client/Client.java', r'public Map<String, Object>[^\n]+\{\n\s*return doJson\w*\("([A-Z]+)", "([^\"]+)"',
         'clients/java/src/main/java/ai/olivares/client/ClientCore.java', 'parsed = Json.parse(raw)', 'if (!(parsed instanceof Map))', 'return out;'),
    )
    common = None
    for operations, pattern, decoder, *markers in layouts:
        try:
            source, core = (root / operations).read_text(), (root / decoder).read_text()
        except OSError:
            return []
        if not all(marker in core for marker in markers):
            return []
        found = {method + ' ' + path for method, path in re.findall(pattern, source)}
        common = found if common is None else common & found
    return sorted(common or [])


def record_db_defaults(snapshot, preview):
    # Classic db init and the opt-in data-directory flow have different metadata.
    # Measure omitted inputs using the binary's own password-free provisioning result.
    dsn = urllib.parse.urlsplit(preview['app_dsn_hint'])
    values = {'database': preview['database'], 'app-role': dsn.username,
              'sslmode': urllib.parse.parse_qs(dsn.query)['sslmode'][0]}
    if not all(values.values()):
        raise ValueError('native db init default preview is incomplete')
    for command in snapshot['cli']['commands']:
        if command['path'] == 'olivares db init':
            for flag in command['flags']:
                if flag['name'] in values:
                    flag['effective_default'] = values[flag['name']]


def execute(argv, root, env):
    return subprocess.run([str(a) for a in argv], cwd=root, env=env,
                          capture_output=True, text=True, timeout=600)


def checked(argv, root, env):
    result = execute(argv, root, env)
    if result.returncode:
        raise RuntimeError('capture command failed: ' + str(argv[0]) + '\n' + result.stderr)
    return result.stdout


def capture(root, binary, output):
    env = {k: v for k, v in os.environ.items() if k in
           ('PATH', 'TMPDIR', 'GOCACHE', 'GOPATH', 'GOMODCACHE', 'GOTOOLCHAIN', 'GOFLAGS',
            'GOMAXPROCS', 'LD_LIBRARY_PATH', 'SSL_CERT_FILE', 'SSL_CERT_DIR', 'HOME')}
    # Resolve HOME-dependent tool caches before isolating the product's settings.
    env.update(json.loads(checked(['go', 'env', '-json', 'GOPATH', 'GOMODCACHE', 'GOCACHE'], root, env)))
    with tempfile.TemporaryDirectory(prefix='userspace-', dir=os.environ.get('TMPDIR')) as temporary:
        home = Path(temporary)
        env.update(HOME=str(home), XDG_CONFIG_HOME=str(home / '.config'))
        dump = home / 'cli.json'
        checked(['go', 'test', '-p', '1', '-parallel', '2', '-count=1',
                 '-run', '^TestCLIRefDump$', './cmd/olivares'], root,
                dict(env, OLIVARES_CLIREF_DUMP_OUT=str(dump)))
        snapshot = {'schema': 'olivares.userspace/1', 'cli': json.loads(dump.read_text())}
        snapshot['classifications'] = json.loads(Path(__file__).with_name('classifications.json').read_text())
        snapshot['open_json_operations'] = generic_json_operations(root)
        for name, arguments in (('openapi', ['openapi']), ('openapi_beta', ['openapi', '--beta'])):
            snapshot[name] = json.loads(checked([binary, *arguments], home, env))
        roster = checked(['go', 'run', '.', '-root', root, '-list'],
                         root / 'scripts/config-env-docs', dict(env, GOWORK='off'))
        names = {line.split('\t')[0] for line in roster.splitlines()
                 if line.split('\t')[1] != 'test-only'}
        environment = {}
        for line in (root / 'scripts/config-env-catalog.tsv').read_text().splitlines():
            if line.startswith('#') or not line:
                continue
            name, required, default, _ = line.split('\t', 3)
            if name in names:
                environment[name] = {'required': required, 'documented_default': default}
        if set(environment) != names:
            raise RuntimeError('environment catalog does not cover the generated roster')
        snapshot['environment'] = environment
        # Native config generation measures defaults; the catalog above is documentation.
        config = checked([binary, 'config', 'generate', '--profile', 'eval'], home, env)
        snapshot['config'] = {}
        for line in config.splitlines():
            if re.match(r'^OLIVARES_[A-Z0-9_]+=', line):
                key, value = line.split('=', 1)
                snapshot['config'][key] = value.strip()
        # Retain the published on-disk CLI configuration field names and their Go types.
        for kind, body in re.findall(r'type (cliConfig|cliContext) struct\s*\{(.*?)\n\}',
                                     (root / 'cmd/olivares/cliconfig.go').read_text(), re.S):
            for fieldtype, key in re.findall(r'\w+\s+(\S+)\s+`yaml:"([^",]+)(?:,[^"]*)?"`', body):
                snapshot['config'][kind + '.' + key] = {'type': fieldtype}
        source = json.loads(checked(['go', 'run', Path(__file__).with_name('source.go')], root, env))
        snapshot['paths'] = sorted(set(source['paths']))
        manifest = json.loads(checked([binary, 'migrate', 'manifest', '-o', 'json'], home, env))['manifest']
        files = {m['namespace'] + '/' + f['path']: f['sha256']
                 for m in manifest['migrations'] for f in m['files']}
        snapshot['migrations'] = {engine: {'versions': plan, 'files': {
                                      name: digest for name, digest in files.items()
                                      if name.split('/')[1] == engine}}
                                  for engine, plan in source['versions'].items()}
        snapshot['runtime'] = {}
        for argv in (['config', 'validate', '-o', 'json'],
                     ['config', 'effective', '--strict', '-o', 'json'],
                     ['db', 'init', '--print-sql', '-o', 'json'],
                     ['agent', 'managed-settings', '--timeout', '5']):
            result = execute([binary, *argv], home, env)
            item = {'exit': result.returncode}
            if not result.returncode:
                item['json'] = json.loads(result.stdout)
            snapshot['runtime'][' '.join(argv)] = item
        password = home / 'preview-password'
        password.write_text('userspace-preview-only')
        password.chmod(0o600)
        maintenance = os.environ['USERSPACE_POSTGRES_DSN']
        if urllib.parse.urlsplit(maintenance).hostname not in ('localhost', '127.0.0.1', '::1'):
            raise ValueError('default probe requires a disposable loopback PostgreSQL service')
        env['USERSPACE_MAINTENANCE_DSN'] = maintenance
        preview = json.loads(checked([binary, 'db', 'init', '--superuser-dsn',
                                      'env:USERSPACE_MAINTENANCE_DSN', '--app-password-file',
                                      password, '-o', 'json'], home, env))
        record_db_defaults(snapshot, preview)
        snapshot['binary_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
        output.write_text(json.dumps(snapshot, indent=2) + '\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    capture(args.root.resolve(), args.binary.resolve(), args.output.resolve())
