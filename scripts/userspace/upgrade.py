#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Create published data, upgrade it, read it through API/CLI, and test rollback."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import socket
import sqlite3
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

spec = importlib.util.spec_from_file_location('userspace_check', Path(__file__).with_name('check-userspace.py'))
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

# Approved edition move; all stored rows remain covered by row_diff.
REDTEAM_EDITION_ROUTES = frozenset(('/v1/m/redteam/targets', '/v1/m/redteam/runs', '/v1/m/redteam/catalog'))


def digest(value):
    if isinstance(value, bytes):
        value = {'bytes': value.hex()}
    return hashlib.sha256(json.dumps(value, sort_keys=True, default=str).encode()).hexdigest()


def module_profile_snapshot(value):
    """Hash the complete document and its reviewed v1-to-v2 module migration."""
    if not isinstance(value, str):
        return {}
    try:
        doc = json.loads(value)
    except json.JSONDecodeError:
        return {}  # Unrecognized documents keep the ordinary whole-value check.
    if not isinstance(doc, dict) or doc.get('version') != 'olivares.deployment.settings.v1':
        return {}
    modules = doc.get('modules')
    if not isinstance(modules, dict):
        return {}
    selected = modules.get('selected')
    if not isinstance(selected, list) or not all(isinstance(name, str) for name in selected):
        return {}
    # reconcileModules changes only this timestamp, the profile version, and
    # selectedModules' sorted union with liveingest (forced on before v2).
    modules.pop('updated_at', None)
    result = {'doc': digest(doc)}
    if modules.get('version', '') in ('', 'olivares.module.profile.v1'):
        modules['version'] = 'olivares.module.profile.v2'
        modules['selected'] = sorted(set(selected) | {'liveingest'})
        result['v2'] = digest(doc)
    return result


def snapshot_rows(tables):
    result = {}
    for table, keys, rows in tables:
        # SQLite's RLS connection context is not persisted customer state.
        if table == '_scope_tenant':
            continue
        entries = {}
        for row in rows:
            # These timestamps/counters are engine-maintained, not user settings.
            ignored = {'updated_at', 'version'} - set(keys)
            if table == 'leader_epoch':
                ignored |= {'epoch', 'holder', 'acquired_at'}
            # Auth/read requests append audit events; the ledger rows remain checked.
            if table == 'audit_heads':
                ignored |= {'seq', 'hash'}
            values = {k: digest(v) for k, v in row.items() if k not in ignored}
            identity = digest([row[k] for k in keys]) if keys else digest(values)
            entry = entries.setdefault(identity, {'count': 0, 'values': values})
            if table == 'deployment_settings' and 'doc' in row:
                entry['module_profile'] = module_profile_snapshot(row['doc'])
            entry['count'] += 1
        result[table] = entries
    return result


def row_diff(before, after):
    failures = []
    for table, rows in before.items():
        if table not in after:
            failures.append(table + ': table removed')
            continue
        for key, row in rows.items():
            other = after[table].get(key)
            if other is None or other['count'] < row['count']:
                failures.append(table + ': stored row removed ' + key)
                continue
            for field, value in row['values'].items():
                if other['values'].get(field) != value:
                    upgraded = row.get('module_profile', {}).get('v2')
                    if (table == 'deployment_settings' and field == 'doc' and upgraded is not None
                            and upgraded == other.get('module_profile', {}).get('doc')):
                        continue
                    failures.append(table + '.' + field + ': stored value changed ' + key)
    return failures


class Upgrade:
    def __init__(self, args):
        self.args = args
        self.out = args.output.resolve()
        self.out.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.home = self.out / 'fixture'
        self.home.mkdir(mode=0o700)
        self.env = {k: v for k, v in os.environ.items() if k in
                    ('PATH', 'TMPDIR', 'LD_LIBRARY_PATH', 'NODE_PATH', 'PLAYWRIGHT_BROWSERS_PATH',
                     'PLAYWRIGHT_CHROMIUM_EXECUTABLE', 'ARCHIFY_CHROME_NO_SANDBOX', 'CHROME', 'RUNNER_TRACKING_ID')}
        self.env.update(HOME=str(self.home), XDG_CONFIG_HOME=str(self.home / '.config'), GOMAXPROCS='4')
        with socket.socket() as server:
            server.bind(('127.0.0.1', 0))
            self.origin = 'http://localhost:' + str(server.getsockname()[1])
        self.token = self.tenant = ''
        self.password = os.urandom(24).hex()
        self.provider_key = os.urandom(32).hex()
        self.secrets = [self.password, self.provider_key]
        self.process = None
        self.result = {'database': args.database, 'steps': [], 'complete': False, 'failures': [],
                       'published_sha256': hashlib.sha256(args.published.read_bytes()).hexdigest(),
                       'candidate_sha256': hashlib.sha256(args.candidate.read_bytes()).hexdigest()}

    def scrub(self, text):
        for secret in self.secrets:
            if secret:
                text = text.replace(secret, '<redacted>')
        return re.sub(r'olst_[A-Za-z0-9]+', '<redacted>', text)

    def call(self, method, path, body=None, expected=None):
        headers = {'Content-Type': 'application/json'}
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        if self.tenant:
            headers['X-Olivares-Tenant'] = self.tenant
        if method == 'POST' and 'work-items' in path:
            headers['Idempotency-Key'] = str(uuid.uuid4())
        request = urllib.request.Request(self.origin + path, method=method, headers=headers,
                                         data=None if body is None else json.dumps(body).encode())
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                status, data = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, data = error.code, error.read()
        self.result['steps'].append({'method': method, 'path': path, 'status': status})
        if expected is not None and status != expected:
            raise RuntimeError(method + ' ' + path + ': HTTP ' + str(status))
        return status, json.loads(data) if data else None

    def read(self, path: str) -> tuple[int, object]:
        status, data = self.call('GET', path)
        error = data.get('error') if isinstance(data, dict) else None
        edition_refusal = (status == 501 and path in REDTEAM_EDITION_ROUTES
                           and isinstance(error, str) and 'Business' in error)
        if status != 200 and not edition_refusal and not (
                status == 404 and path.startswith('/v1/m/') and
                isinstance(error, dict) and error.get('code') == 'module_not_enabled'):
            raise RuntimeError('GET ' + path + ': HTTP ' + str(status))
        return status, data

    def start(self, binary, label, seed=False):
        self.label = label
        raw = self.out / (label + '.raw')
        argv = [str(binary), 'serve', '--data-dir', str(self.home / 'data'),
                '--engine', self.args.database, '--listen', '127.0.0.1:' + self.origin.rsplit(':', 1)[1],
                '--grpc-listen', '127.0.0.1:0', '--insecure', '--checkpoint-interval', '0']
        if self.args.database == 'postgres':
            argv += ['--dsn', 'env:USERSPACE_APP_DSN', '--owner-dsn', 'env:USERSPACE_OWNER_DSN']
            if seed:
                argv += ['--admin-dsn', 'env:USERSPACE_ADMIN_DSN']
        if seed:
            argv += ['--seed-demo']
        with raw.open('wb') as log:
            self.process = subprocess.Popen(argv, env=self.env, stdout=log, stderr=log, start_new_session=True)
        (self.out / 'process.json').write_text(json.dumps({'pid': self.process.pid, 'pgid': self.process.pid}))
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(label + ': engine exited ' + str(self.process.returncode))
            try:
                if self.call('GET', '/v1/server-info')[0] == 200:
                    actual = hashlib.sha256(Path('/proc/' + str(self.process.pid) + '/exe').read_bytes()).hexdigest()
                    if actual != hashlib.sha256(binary.read_bytes()).hexdigest():
                        raise RuntimeError('running executable digest differs')
                    self.result.setdefault('running_sha256', {})[label] = actual
                    return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(.2)
        raise RuntimeError(label + ': startup timeout')

    def stop(self):
        if self.process is not None:
            if self.process.poll() is None:
                os.killpg(self.process.pid, signal.SIGTERM)
                try:
                    self.process.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    os.killpg(self.process.pid, signal.SIGKILL)
                    self.process.wait(timeout=10)
            self.process = None
            raw = self.out / (self.label + '.raw')
            (self.out / (self.label + '.log')).write_text(self.scrub(raw.read_text(errors='replace')))
            raw.unlink()
            (self.out / 'process.json').unlink(missing_ok=True)

    def login(self):
        self.token = ''
        self.token = self.call('POST', '/v1/auth/login',
                              {'email': 'demo@olivares.local', 'password': 'olivares-demo-estate'}, 200)[1]['token']
        self.secrets.append(self.token)
        who = self.call('GET', '/v1/auth/whoami', expected=200)[1]
        self.tenant = who['grants'][0]['tenant']
        return who

    def command(self, binary, argv):
        run = subprocess.run([str(binary), *argv], env=dict(self.env, OLIVARES_TOKEN=self.token),
                             cwd=self.home, capture_output=True, text=True, timeout=120)
        if run.returncode:
            raise RuntimeError('CLI ' + argv[0] + ': exit ' + str(run.returncode))
        return {'exit': run.returncode, 'json': json.loads(run.stdout)}

    def pg_query(self, sql):
        url = urllib.parse.urlsplit(os.environ['USERSPACE_POSTGRES_DSN'])
        env = dict(self.env, PGHOST=url.hostname, PGPORT=str(url.port or 5432),
                   PGUSER=urllib.parse.unquote(url.username or ''),
                   PGPASSWORD=urllib.parse.unquote(url.password or ''), PGDATABASE=self.database)
        run = subprocess.run(['psql', '-XAt', '-v', 'ON_ERROR_STOP=1', '-c', sql],
                             env=env, capture_output=True, text=True, timeout=60)
        if run.returncode:
            raise RuntimeError('fixture PostgreSQL snapshot failed')
        return run.stdout.splitlines()

    def provision(self):
        url = urllib.parse.urlsplit(os.environ['USERSPACE_POSTGRES_DSN'])
        if url.hostname not in ('localhost', '127.0.0.1', '::1'):
            raise ValueError('upgrade fixture requires a disposable loopback PostgreSQL service')
        self.secrets.append(urllib.parse.unquote(url.password or ''))
        suffix = uuid.uuid4().hex[:12]
        self.database = 'userspace_' + suffix
        roles = ['userspace_' + suffix + '_' + role for role in ('app', 'owner', 'admin')]
        password = self.home / 'db-password'
        password.write_text(self.password)
        password.chmod(0o600)
        self.env['USERSPACE_MAINTENANCE_DSN'] = os.environ['USERSPACE_POSTGRES_DSN']
        for role, key in zip(roles, ('APP', 'OWNER', 'ADMIN')):
            host = '[' + url.hostname + ']' if ':' in url.hostname else url.hostname
            self.env['USERSPACE_' + key + '_DSN'] = urllib.parse.urlunsplit(
                (url.scheme, role + ':' + self.password + '@' + host + ':' + str(url.port or 5432),
                 '/' + self.database, url.query, ''))
        self.command(self.args.published, ['db', 'init', '--superuser-dsn', 'env:USERSPACE_MAINTENANCE_DSN',
                     '--database', self.database, '--app-role', roles[0], '--app-password-file', str(password),
                     '--owner-role', roles[1], '--owner-password-file', str(password),
                     '--admin-role', roles[2], '--admin-password-file', str(password), '-o', 'json'])

    def rows(self):
        tables = []
        if self.args.database == 'sqlite':
            with sqlite3.connect('file:' + str(self.home / 'data/olivares.db') + '?mode=ro', uri=True) as db:
                db.row_factory = sqlite3.Row
                for (name,) in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").fetchall():
                    quoted = '"' + name.replace('"', '""') + '"'
                    info = db.execute('PRAGMA table_info(' + quoted + ')').fetchall()
                    keys = [row['name'] for row in sorted(info, key=lambda r: r['pk']) if row['pk']]
                    tables.append((name, keys, [dict(row) for row in db.execute('SELECT * FROM ' + quoted)]))
        else:
            for name in self.pg_query("SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"):
                quoted = '"' + name.replace('"', '""') + '"'
                keys = self.pg_query("SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid=i.indrelid "
                                     "AND a.attnum=ANY(i.indkey) WHERE i.indrelid=('public.' || "
                                     "quote_ident('" + name.replace("'", "''") + "'))::regclass AND i.indisprimary "
                                     "ORDER BY array_position(i.indkey,a.attnum)")
                rows = [json.loads(line) for line in self.pg_query('SELECT row_to_json(r) FROM (SELECT * FROM public.' + quoted + ') r')]
                tables.append((name, keys, rows))
        return snapshot_rows(tables)

    def run(self):
        try:
            if self.args.database == 'postgres':
                self.provision()
            self.start(self.args.published, 'published', seed=True)
            who = self.login()
            ceremony = subprocess.Popen(['node', str(Path(__file__).with_name('passkey.cjs'))],
                                        env=self.env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, text=True, start_new_session=True)
            try:
                output, error = ceremony.communicate(json.dumps({'origin': self.origin, 'token': self.token,
                                                            'tenant': self.tenant}), timeout=90)
                if ceremony.returncode or json.loads(output)['aal'] != 3:
                    # Never retain arbitrary browser stderr, even after token scrubbing.
                    stage = re.fullmatch(r'WebAuthn upgrade fixture failed at '
                                         r'(browser-launch|browser-setup|webauthn)\n', error)
                    raise RuntimeError('published passkey ceremony failed' +
                                       (' at ' + stage[1] if stage else ''))
            finally:
                try:
                    os.killpg(ceremony.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    ceremony.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(ceremony.pid, signal.SIGKILL)
                    ceremony.wait(timeout=10)
            self.call('POST', '/v1/users', {'email': 'upgrade-viewer@example.invalid', 'password': self.password,
                      'display_name': 'Upgrade viewer', 'tenant': self.tenant, 'role': 'viewer'}, 201)
            workspace = self.call('POST', '/v1/workspaces', {'name': 'Upgrade workspace', 'slug': 'upgrade',
                                  'settings': {'custom_value': 'keep-this-setting', 'credential_ref': 'store:upgrade-test'}}, 201)[1]
            self.call('POST', '/v1/m/governance/rbac/permission-groups', {'name': 'upgrade-readers',
                      'display_name': 'Upgrade readers', 'permissions': ['sessions:run:read']}, 201)
            for path in ('account/config', 'account/user'):
                (self.home / path).mkdir(parents=True)
            self.call('POST', '/v1/m/sessions/provider-profiles', {'driver': 'claude',
                      'config_home': str(self.home / 'account/config'), 'user_home': str(self.home / 'account/user'),
                      'display_name': 'Upgrade profile', 'auth_source': 'provider_account_home'}, 201)
            self.call('POST', '/v1/m/sessions/templates', {'name': 'Upgrade template', 'description': 'Keep this template',
                      'body': {'settings': {'effort': 'high', 'custom_instructions': 'Keep this value'},
                               'policies': {'allowed_tools': [], 'record_io': False}}}, 201)
            self.call('POST', '/v1/m/governance/policies', {'name': 'Upgrade policy', 'kind': 'abac',
                      'enabled': False, 'spec': {'rules': [{'deny': True, 'permission': 'sessions:run:write'}]}}, 201)
            self.call('POST', '/v1/m/sessions/providers', {'kind': 'anthropic', 'display_name': 'Upgrade fixture key',
                      'base_url': 'https://127.0.0.1:1', 'api_key': self.provider_key}, 201)
            self.call('POST', '/v1/m/sessions/work-items?mode=apply', {'command': 'item.create',
                      'workspace_id': workspace['id'], 'work_kind': 'task', 'title': 'Keep this work',
                      'brief_md': 'Upgrade data', 'priority': 'p1', 'owner_kind': 'user', 'owner_ref': who['user_id'],
                      'provenance_kind': 'human', 'provenance_ref': 'fixture:upgrade', 'context_refs': [],
                      'acceptance': [{'criterion_key': 'retain', 'ordinal': 0, 'statement': 'Retain data', 'required': True}]}, 200)
            if self.args.database == 'postgres':
                self.stop()
                self.start(self.args.published, 'published-app-owner')
            paths = ['/v1/users', '/v1/workspaces', '/v1/m/governance/rbac/permission-groups',
                     '/v1/m/sessions/provider-profiles', '/v1/m/sessions/templates', '/v1/m/governance/policies',
                     '/v1/m/sessions/work-items', '/v1/m/sessions/providers', '/v1/console/secrets',
                     '/v1/m/recording/sessions', '/v1/m/redteam/targets', '/v1/m/redteam/runs', '/v1/m/redteam/catalog']
            before_reads = {path: self.read(path) for path in paths}
            argv = ['users', 'ls', '--server', self.origin, '--tenant', self.tenant, '--limit', '0', '-o', 'json']
            old_cli = self.command(self.args.published, argv)
            self.stop()
            before = self.rows()
            self.start(self.args.candidate, 'candidate')
            self.login()
            for path, (old_status, old) in before_reads.items():
                status, new = self.read(path)
                if path in REDTEAM_EDITION_ROUTES and old_status in (200, 501) and status == 501:
                    continue
                if status != old_status:
                    self.result['failures'].append(f'{path}: HTTP status changed from {old_status} to {status}')
                self.result['failures'] += check.json_diff(old, new, path)
            self.result['failures'] += check.json_diff(old_cli, self.command(self.args.candidate, argv), 'users ls')
            self.stop()
            after = self.rows()
            self.result['failures'] += row_diff(before, after)
            # An older binary may start successfully or refuse ahead-of-version state cleanly.
            try:
                self.start(self.args.published, 'rollback')
                self.login()
                self.command(self.args.published, argv)
                self.result['rollback'] = 'published binary starts and reads'
            except RuntimeError:
                self.stop()
                log = (self.out / 'rollback.log').read_text()
                if not re.search(r'(schema version.*(newer|ahead)|newer.*schema|migration.*(ahead|newer))', log, re.I):
                    raise RuntimeError('published rollback failed without a measured schema-ahead refusal')
                self.result['rollback'] = 'clean schema-ahead refusal'
            finally:
                self.stop()
            self.result['failures'] += row_diff(after, self.rows())
            self.result['preserved_rows'] = sum(entry['count'] for rows in before.values() for entry in rows.values())
            self.result['complete'] = True
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            self.result['failures'].append(self.scrub(str(error)))
        finally:
            self.stop()
            (self.out / 'result.json').write_text(self.scrub(json.dumps(self.result, indent=2)) + '\n')
        print(json.dumps({'database': self.args.database, 'complete': self.result['complete'],
                          'failures': self.result['failures'], 'preserved_rows': self.result.get('preserved_rows')}))
        return 0 if self.result['complete'] and not self.result['failures'] else 1


def interrupted(signum, frame):
    raise SystemExit(128 + signum)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--published', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--database', choices=('sqlite', 'postgres'), required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    args.published, args.candidate = args.published.resolve(), args.candidate.resolve()
    raise SystemExit(Upgrade(args).run())
