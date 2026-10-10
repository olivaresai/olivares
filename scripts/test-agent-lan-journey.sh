#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

# The engine and CLI are real. The default Docker target is a loopback protocol
# fixture. Catalog mode can use a real daemon via OLIVARES_JOURNEY_DOCKER_SOCKET;
# only that mode qualifies actual containers. Run under the heavy-job wrapper.
set -euo pipefail
if [[ $# -lt 2 || $# -gt 3 || ! -x "$1" ]]; then
	printf 'Usage: %s /absolute/engine /absolute/evidence-directory [deploy|catalog]\n' "$0" >&2
	exit 2
fi
python3 - "$1" "$2" "${3:-deploy}" <<'PY'
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

mode = sys.argv[3]
if mode not in ('deploy', 'catalog'):
    raise SystemExit('journey mode must be deploy or catalog')
binary = Path(sys.argv[1]).resolve(strict=True)
evidence = Path(sys.argv[2]).resolve()
evidence.mkdir(parents=True, exist_ok=False)
os.chmod(evidence, 0o700)
home, data = evidence / 'home', evidence / 'data'
home.mkdir(mode=0o700)
data.mkdir(mode=0o700)
target = {'container': None, 'requests': []}
lock = threading.Lock()

class Docker(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def handle_request(self):
        path = urllib.parse.urlparse(self.path).path
        body = self.rfile.read(int(self.headers.get('content-length', '0')))
        with lock:
            target['requests'].append(self.command + ' ' + path)
            container = target['container']
            status, result = 404, {'message': 'not found'}
            if self.command == 'GET' and path == '/containers/json':
                status, result = 200, [container] if container else []
            elif self.command == 'GET' and path.endswith('/json') and container:
                status, result = 200, {'Id': container['Id'], 'Config': {
                    'Image': container['Image'], 'Labels': container['Labels']},
                    'State': {'Status': container['State']}}
            elif self.command == 'POST' and path == '/containers/create':
                spec = json.loads(body)
                name = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)['name'][0]
                target['container'] = {'Id': 'fixture-container', 'Names': ['/' + name],
                    'Image': spec['Image'], 'Labels': spec.get('Labels', {}), 'State': 'created'}
                status, result = 201, {'Id': 'fixture-container', 'Warnings': []}
            elif self.command == 'POST' and path.endswith('/start') and container:
                container['State'] = 'running'
                status, result = 204, None
            elif self.command == 'POST' and path.endswith('/stop') and container:
                container['State'] = 'exited'
                status, result = 204, None
            elif self.command == 'DELETE' and container:
                target['container'] = None
                status, result = 204, None
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        if result is not None:
            self.wfile.write(json.dumps(result).encode())

    do_GET = do_POST = do_DELETE = handle_request

docker = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Docker)
thread = threading.Thread(target=docker.serve_forever, daemon=True)
thread.start()
credential = evidence / 'fixture-credential'
credential.write_text(secrets.token_urlsafe(32))
credential.chmod(0o600)
config = evidence / 'deploy-config.json'
docker_socket = os.environ.get('OLIVARES_JOURNEY_DOCKER_SOCKET', '') if mode == 'catalog' else ''
docker_config = {'socket_path': docker_socket} if docker_socket else {
    'remote_base_url': f'http://127.0.0.1:{docker.server_port}', 'remote_insecure': True}
config.write_text(json.dumps({'docker': docker_config,
    'credential': {'kind': 'file', 'path_template': str(credential), 'ttl_seconds': 120},
    'blast_radius': {'allow_destroy': True, 'max_destroy_items': 1}}))

def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

port, grpc_port = free_port(), free_port()
base = f'http://127.0.0.1:{port}'
env = {k: v for k, v in os.environ.items() if not k.startswith('OLIVARES_')}
env.update(HOME=str(home), OLIVARES_DEPLOY_EXECUTOR_CONFIG=str(config))
store_args = []
if os.environ.get('IMPLINT_JOURNEY_DSN'):
    env['IMPLINT_JOURNEY_DSN'] = os.environ['IMPLINT_JOURNEY_DSN']
    store_args = ['--engine', 'postgres', '--dsn', 'env:IMPLINT_JOURNEY_DSN']
    if os.environ.get('IMPLINT_JOURNEY_ADMIN_DSN'):
        env['IMPLINT_JOURNEY_ADMIN_DSN'] = os.environ['IMPLINT_JOURNEY_ADMIN_DSN']
        store_args += ['--admin-dsn', 'env:IMPLINT_JOURNEY_ADMIN_DSN']
engine = None
token, tenant = '', ''
steps = []

def record(step, status):
    steps.append({'step': step, 'status': status})
    (evidence / 'steps.json').write_text(json.dumps(steps, indent=2) + '\n')
    print(step + ': ' + status, flush=True)

def call(method, path, body=None, bearer=None):
    headers = {'Content-Type': 'application/json'}
    if bearer is None:
        bearer = token
    if bearer:
        headers['Authorization'] = 'Bearer ' + bearer
    if tenant:
        headers['X-Olivares-Tenant'] = tenant
    request = urllib.request.Request(base + path, method=method, headers=headers,
        data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    return status, json.loads(raw) if raw else {}

def require(step, response, expected=(200, 201)):
    status, body = response
    if status not in expected:
        record(step, 'FAIL HTTP ' + str(status))
        # Response bodies may contain issued credentials. Retain only safe error codes.
        error = body.get('error', {})
        code = error.get('code', '') if isinstance(error, dict) else ''
        raise RuntimeError(step + ': HTTP ' + str(status) + ' ' + str(code))
    return body

def cli(step, args, expect=0, bearer=None):
    child_env = env.copy()
    child_env.update(OLIVARES_SERVER_URL=base, OLIVARES_TENANT=tenant,
        OLIVARES_TOKEN=token if bearer is None else bearer)
    result = subprocess.run([str(binary), ('catalog' if mode == 'catalog' and args[0] == 'instances' else 'deploy'), *args, '-o', 'json'],
        env=child_env, capture_output=True, text=True, timeout=30)
    if result.returncode != expect:
        record(step, 'FAIL exit ' + str(result.returncode))
        # CLI output for this family contains references, never minted credentials.
        (evidence / (step + '.stderr')).write_text(result.stderr)
        raise RuntimeError(step + ': unexpected CLI exit ' + str(result.returncode))
    if expect == 0 or expect == 7:
        body = json.loads(result.stdout)
        (evidence / (step + '.json')).write_text(json.dumps(body, indent=2) + '\n')
    else:
        body = {}
    record(step, 'PASS')
    return body

def approve(ref, approvers):
    for approver in approvers:
        require('approve', call('POST', '/v1/m/governance/approvals/' + ref + '/decisions',
            {'decision': 'approve'}, approver))

try:
    if os.environ.get('IMPLINT_JOURNEY_SUPERUSER_DSN'):
        # Provision through the product, with separate least-privilege owner and
        # application roles. Only the disposable PostgreSQL fixture is affected.
        env['IMPLINT_JOURNEY_SUPERUSER_DSN'] = os.environ['IMPLINT_JOURNEY_SUPERUSER_DSN']
        result = subprocess.run([str(binary), 'db', 'init', '--superuser-dsn',
            'env:IMPLINT_JOURNEY_SUPERUSER_DSN', '--data-dir', str(data),
            '--database', 'implint_c35', '-o', 'json'], env=env,
            capture_output=True, text=True, timeout=60)
        if result.returncode != 0:
            record('postgres-provision', 'FAIL exit ' + str(result.returncode))
            raise RuntimeError('disposable PostgreSQL provisioning failed')
        provision = json.loads(result.stdout)
        (evidence / 'postgres-verification.json').write_text(json.dumps(
            {'verification': provision.get('verification', [])}, indent=2) + '\n')
        store_args = ['--engine', 'postgres', '--dsn', 'file:' + str(data / 'postgres/app.dsn'),
            '--owner-dsn', 'file:' + str(data / 'postgres/owner.dsn')]
        record('postgres-provision', 'PASS')
    with (evidence / 'engine.out').open('w') as out, (evidence / 'engine.log').open('w') as log:
        engine = subprocess.Popen([str(binary), 'serve', '--insecure', '--data-dir', str(data),
            '--listen', f'127.0.0.1:{port}', '--grpc-listen', f'127.0.0.1:{grpc_port}', *store_args],
            env=env, stdout=out, stderr=log, start_new_session=True)
    (evidence / 'engine-process.json').write_text(json.dumps(
        {'pid': engine.pid, 'pgid': os.getpgid(engine.pid)}, indent=2) + '\n')
    started = Path(f'/proc/{engine.pid}/exe')
    digest = hashlib.sha256(started.read_bytes()).hexdigest()
    (evidence / 'binary.json').write_text(json.dumps({'binary': str(binary),
        'started_sha256': digest, 'target': 'local Docker daemon' if docker_socket else 'loopback Docker Engine protocol fixture',
        'source_sha': os.environ.get('OLIVARES_JOURNEY_SOURCE_SHA', ''),
        'backend': 'postgres' if store_args else 'sqlite',
        'remote_effects': bool(docker_socket)}, indent=2) + '\n')
    for _ in range(120):
        if engine.poll() is not None:
            raise RuntimeError('engine exited before readiness')
        try:
            with urllib.request.urlopen(base + '/healthz', timeout=1):
                break
        except OSError:
            time.sleep(0.5)
    else:
        raise RuntimeError('engine readiness timeout')
    setup_output = (evidence / 'engine.out').read_text()
    match = re.search(r'olst_[A-Za-z0-9]+', setup_output)
    if not match:
        raise RuntimeError('setup token not printed')
    password = secrets.token_urlsafe(24)
    setup = require('setup', call('POST', '/v1/setup', {'token': match.group(),
        'email': 'admin@implint.invalid', 'password': password, 'organization': 'IMPLINT fixture'}))
    # Erase the consumed one-time token from retained output.
    (evidence / 'engine.out').write_text(setup_output.replace(match.group(), '[consumed setup token]'))
    tenant = setup['organization']['tenant_id']
    token = require('login', call('POST', '/v1/auth/login', {
        'email': 'admin@implint.invalid', 'password': password}))['token']
    modules = require('module settings', call('GET', '/v1/console/modules'))
    selected = [item['name'] for item in modules['modules'] if item['selected']]
    for needed in (('catalog', 'deploy') if mode == 'catalog' else ('deploy',)):
        if needed not in selected:
            selected.append(needed)
    require('enable deploy', call('PUT', '/v1/console/modules', {'selected': selected}))
    for _ in range(120):
        try:
            status, _ = call('GET', '/v1/m/deploy/definitions')
            if status == 200:
                break
        except OSError:
            pass
        if engine.poll() is not None:
            raise RuntimeError('engine exited while applying the module selection')
        time.sleep(0.25)
    else:
        raise RuntimeError('deploy module did not become ready')
    record('enable-deploy', 'PASS')
    approvers = []
    for i in range(2):
        email = f'approver-{i}@implint.invalid'
        require('create approver', call('POST', '/v1/users', {'email': email,
            'password': password, 'tenant': tenant, 'role': 'admin'}))
        approvers.append(require('approver login', call('POST', '/v1/auth/login', {
            'email': email, 'password': password}))['token'])
    viewer = require('viewer token', call('POST', '/v1/tokens', {
        'name': 'journey-viewer', 'tenant': tenant, 'role': 'viewer'}))['token']
    if mode == 'catalog':
        subject = 'catalog-journey-' + secrets.token_hex(4)
        approved_spec = {'image': os.environ.get('OLIVARES_JOURNEY_IMAGE', 'fixture-agent:1'),
            'command': 'sleep 1200' if docker_socket else 'agent'}
        entry = require('catalog-create', call('POST', '/v1/m/catalog/entries', {
            'kind': 'agent', 'name': subject, 'slug': subject, 'version': '1.0.0', 'spec': approved_spec}))
        require('catalog-approve', call('POST', '/v1/m/catalog/entries/' + entry['id'] + '/approve'))
        integrity = require('catalog-verify', call('GET', '/v1/m/catalog/entries/' + entry['id'] + '/verify'))
        if integrity.get('verified') is not True:
            raise RuntimeError('approved entry did not verify')
        definition = require('declare catalog source', call('POST', '/v1/m/deploy/definitions', {
            'name': subject, 'subject_kind': 'agent', 'subject_ref': subject, 'environment': 'test',
            'target': 'docker.host/catalog-journey', 'runtime': 'docker',
            'source_ref': 'catalog entry ' + entry['id'], 'spec': approved_spec}))['id']
        instance = require('catalog-instantiate', call('POST', '/v1/m/catalog/entries/' + entry['id'] + '/instantiate',
            {'name': subject, 'target_ref': 'deployment:' + definition}))
        ident = instance['id']
        transition = '/v1/m/catalog/instances/' + ident + '/transition'
        require('catalog-approve-instance', call('POST', transition, {'status': 'approved'}))
        def require_unprovisioned(step):
            if docker_socket:
                containers = subprocess.check_output(['docker', 'ps', '-aq', '--filter', 'name=^/' + subject + '$'], text=True).strip()
                if containers:
                    raise RuntimeError(step + ': target actuated before approval')
            elif target['container'] is not None:
                raise RuntimeError(step + ': fixture actuated before approval')
            retained = require(step, call('GET', '/v1/m/catalog/instances/' + ident))
            if retained.get('status') != 'approved':
                raise RuntimeError(step + ': approved instance was changed')
            record(step, 'PASS no container; approved instance retained')
        require('catalog-authority-refusal', call('POST', transition, {'status': 'active'}, viewer), (403,))
        record('catalog-authority-refusal', 'PASS HTTP 403')
        require_unprovisioned('catalog-authority-no-effects')
        proposal = cli('catalog-request-activation', ['instances', 'transition', ident, '--status', 'active', '--yes'], expect=7)
        if proposal.get('status') != 'approved' or proposal.get('requires_approval') is not True:
            raise RuntimeError('pending activation falsely reported active')
        require_unprovisioned('catalog-proposal-no-effects')
        require('catalog-pending-refusal', call('POST', transition, {'status': 'active',
            'approval_ref': proposal['approval_ref']}), (403,))
        record('catalog-pending-refusal', 'PASS HTTP 403; instance approved')
        require_unprovisioned('catalog-pending-no-effects')
        approve(proposal['approval_ref'], approvers)
        activated = cli('catalog-activate', ['instances', 'transition', ident, '--status', 'active',
            '--approval-ref', proposal['approval_ref'], '--yes'])
        if activated.get('status') != 'active':
            raise RuntimeError('completed deployment did not activate the instance')
        if cli('catalog-deploy-verify', ['verify', definition]).get('in_sync') is not True:
            raise RuntimeError('catalog target is not in sync')
        if docker_socket:
            state = subprocess.check_output(['docker', 'inspect', '--format', '{{.State.Status}}', subject], text=True).strip()
            if state != 'running':
                raise RuntimeError('catalog target container is not running')
            record('catalog-real-target', 'PASS Docker container running')
        elif not target['container'] or target['container']['State'] != 'running':
            raise RuntimeError('catalog fixture target is not running')
        # Restart the exact executable on retained data before testing module off/on.
        os.killpg(engine.pid, signal.SIGTERM)
        engine.wait(timeout=20)
        with (evidence / 'engine.out').open('a') as out, (evidence / 'engine.log').open('a') as log:
            engine = subprocess.Popen([str(binary), 'serve', '--insecure', '--data-dir', str(data),
                '--listen', f'127.0.0.1:{port}', '--grpc-listen', f'127.0.0.1:{grpc_port}', *store_args],
                env=env, stdout=out, stderr=log, start_new_session=True)
        def wait_catalog(expected):
            for _ in range(120):
                try:
                    code, body = call('GET', '/v1/m/catalog/instances/' + ident)
                    if code == expected:
                        return body
                except OSError:
                    pass
                if engine.poll() is not None:
                    raise RuntimeError('engine exited during catalog restart')
                time.sleep(0.25)
            raise RuntimeError('catalog readiness timed out')
        retained = wait_catalog(200)
        if hashlib.sha256(Path(f'/proc/{engine.pid}/exe').read_bytes()).hexdigest() != digest:
            raise RuntimeError('restart used another executable')
        if retained != activated:
            raise RuntimeError('instance data changed across restart')
        record('catalog-retained-restart', 'PASS exact executable; provenance and active instance retained')
        require('catalog-off', call('PUT', '/v1/console/modules', {'selected': [name for name in selected if name != 'catalog']}))
        disabled = wait_catalog(404)
        if disabled.get('error', {}).get('code') != 'module_not_enabled':
            raise RuntimeError('disabled catalog did not refuse cleanly')
        record('catalog-off', 'PASS HTTP 404 module_not_enabled')
        require('catalog-on', call('PUT', '/v1/console/modules', {'selected': selected}))
        if wait_catalog(200) != activated:
            raise RuntimeError('module off/on reset the retained instance')
        record('catalog-on', 'PASS HTTP 200; active instance retained')
        audit = require('catalog-audit', call('GET', '/v1/audit?limit=100'))
        actions = {item['action'] for item in audit.get('items', [])}
        if not {'catalog.instance.instantiate', 'catalog.instance.transition', 'deploy.apply'}.issubset(actions):
            raise RuntimeError('catalog deployment audit trail incomplete')
        (evidence / 'catalog-result.json').write_text(json.dumps({'entry': entry['id'],
            'instance': activated, 'definition': definition, 'signed': integrity.get('signed'),
            'surfaces': ['catalog API agent request/approve/activate', 'catalog CLI instances transition', 'deploy CLI verify'],
            'console': 'preview; not exercised', 'other_kinds': 'preview; not exercised'}, indent=2) + '\n')
        record('catalog-provisioning', 'PASS ' + ('real Docker target' if docker_socket else 'labelled protocol fixture'))
        raise SystemExit(0)
    spec = evidence / 'spec.json'
    spec.write_text(json.dumps({'image': 'fixture-agent:1', 'command': 'agent'}))
    definition = cli('declare', ['definitions', 'create', '--name', 'implint-loopback',
        '--environment', 'test', '--subject-ref', 'implint-agent', '--runtime', 'docker',
        '--target', 'docker.host/loopback', '--spec-file', str(spec)])['id']
    plan = cli('plan', ['plan', definition])
    if not plan.get('changes') or target['container'] is not None:
        raise RuntimeError('plan failed to report a change without actuation')
    request = cli('request-apply', ['apply', definition], expect=7)
    if target['container'] is not None:
        raise RuntimeError('approval request actuated a container')
    approve(request['approval_ref'], approvers)
    cli('apply', ['apply', definition, '--approval-ref', request['approval_ref']])
    if not target['container'] or target['container']['State'] != 'running':
        raise RuntimeError('apply did not start the target')
    if cli('connection-verify', ['verify', definition]).get('in_sync') is not True:
        raise RuntimeError('deployed target is not in sync')
    before_denied = len(target['requests'])
    cli('unauthorized-target', ['apply', definition], expect=3, bearer=viewer)
    if len(target['requests']) != before_denied:
        raise RuntimeError('denied caller reached the target')
    stop = cli('request-stop', ['retire', definition, '--yes'], expect=7)
    approve(stop['approval_ref'], approvers)
    cli('stop', ['retire', definition, '--yes', '--approval-ref', stop['approval_ref']])
    if target['container'] is not None:
        raise RuntimeError('retire did not remove the target')
    cli('reactivate', ['rollback', definition, '--to-version', '1', '--yes'])
    restart = cli('request-restart', ['apply', definition], expect=7)
    approve(restart['approval_ref'], approvers)
    cli('restart', ['apply', definition, '--approval-ref', restart['approval_ref']])
    if cli('restart-verify', ['verify', definition]).get('in_sync') is not True:
        raise RuntimeError('restarted target is not in sync')
    if not target['container'] or target['container']['State'] != 'running':
        raise RuntimeError('restart did not restore the target')
    operations = cli('operations', ['operations'])
    statuses = {item['status'] for item in operations.get('items', [])}
    if not {'applied', 'retired', 'verified'}.issubset(statuses):
        raise RuntimeError('deployment operation ledger does not agree with target state')
    applied = [item for item in operations['items'] if item['status'] == 'applied']
    if len(applied) != 2 or any(not item.get('approval_ref') for item in applied):
        raise RuntimeError('each actual deployment must retain its approval reference')
    (evidence / 'target.json').write_text(json.dumps(target, indent=2) + '\n')
    record('loopback-deployment', 'PASS (Docker protocol fixture; no LAN host qualified)')
finally:
    if engine is not None and engine.poll() is None:
        os.killpg(engine.pid, signal.SIGTERM)
        try:
            engine.wait(timeout=20)
        except subprocess.TimeoutExpired:
            os.killpg(engine.pid, signal.SIGKILL)
            engine.wait()
    if mode == 'catalog' and docker_socket and 'subject' in locals():
        owned = subprocess.check_output(['docker', 'ps', '-aq', '--filter', 'name=^/' + subject + '$'], text=True).strip()
        if owned:
            subprocess.run(['docker', 'rm', '-f', subject], check=True, stdout=subprocess.DEVNULL)
    docker.shutdown()
    docker.server_close()
    thread.join(timeout=5)
    credential.unlink(missing_ok=True)
    output = evidence / 'engine.out'
    if output.exists():
        output.write_text(re.sub(r'olst_[A-Za-z0-9]+', '[setup token removed]', output.read_text()))
PY
