#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Run inside the release container; no provider login or model request.

Exercise the console's setup, plan, install, inventory and sign-in status APIs.
Only timings and verified tool identities leave the container. Temporary login
state stays in its private data volume and is removed with the Compose project.
"""

import json
import os
import re
import secrets
import shutil
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

DATA = Path('/var/lib/olivares')
STATE = DATA / 'home' / 'managed-tools-journey.json'
TOOLS = '/v1/m/agenttools'


class API:
    def __init__(self) -> None:
        self.token = ''
        self.tenant = ''
        self.timings: list[dict] = []
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(
                cafile=str(DATA / 'tls.crt'))),
        )

    def call(self, method: str, path: str, body: dict | None = None) -> dict:
        headers = {'Content-Type': 'application/json'}
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        if self.tenant:
            headers['X-Olivares-Tenant'] = self.tenant
        request = urllib.request.Request(
            'https://127.0.0.1:8443' + path,
            data=json.dumps(body).encode() if body is not None else None,
            method=method, headers=headers,
        )
        start = time.monotonic()
        try:
            with self.opener.open(request, timeout=60) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            # Never emit response bodies: setup/login and jobs can carry secrets.
            raise RuntimeError(f'{method} {path}: HTTP {error.code}') from None
        finally:
            self.timings.append({'method': method, 'path': path,
                                 'seconds': round(time.monotonic() - start, 3)})


def require_installed(inventory: dict, driver: str, executable: str) -> dict:
    if inventory.get('root') != str(DATA / 'tools'):
        raise RuntimeError('managed inventory is outside the data volume')
    matches = [item for item in inventory.get('installed', [])
               if item.get('driver') == driver and item.get('executable') == executable]
    if len(matches) != 1:
        raise RuntimeError(f'{driver}: expected one managed installation')
    item = matches[0]
    if item.get('state') != 'installed' or not item.get('is_executable'):
        raise RuntimeError(f'{driver}: managed installation is not verified and executable')
    if not Path(executable).resolve().is_relative_to(DATA / 'tools'):
        raise RuntimeError(f'{driver}: executable is outside the data volume')
    if not item.get('sha256'):
        raise RuntimeError(f'{driver}: inventory has no verified digest')
    return item


def install(api: API, driver: str, version: str) -> str:
    plan = api.call('POST', TOOLS + '/plans', {'driver': driver, 'version': version})
    job = api.call('POST', TOOLS + '/installs', {
        'plan_digest': plan['digest'], 'request_id': str(uuid.uuid4()),
    })
    deadline = time.monotonic() + 600
    while job['state'] == 'running' and time.monotonic() < deadline:
        time.sleep(1)
        job = api.call('GET', TOOLS + '/jobs/' + job['id'])
    if job['state'] != 'succeeded' or job.get('error') or job.get('audit_error'):
        raise RuntimeError(f'{driver}: managed install did not succeed cleanly')
    return plan['executable']


def ready(api: API, driver: str, executable: str, digest: str = '') -> dict:
    inventory = api.call('GET', TOOLS + '/inventory')['inventory']
    item = require_installed(inventory, driver, executable)
    if digest and item['sha256'] != digest:
        raise RuntimeError(f'{driver}: installed bytes changed after recreation')
    if Path(executable).stat().st_uid != 65532:
        raise RuntimeError(f'{driver}: installer did not run as UID 65532')
    # This is the cold tool-status path that exceeded five seconds in first hour.
    # Sign-in is a system route: the tenant header does not select its login home.
    query = urllib.parse.urlencode({'driver': driver, 'tenant_id': api.tenant})
    status = api.call('GET', TOOLS + '/sign-in?' + query)
    # Claude's API status can mask native command/JSON failures. This checks the
    # API response and latency, not successful native CLI execution or a session.
    if not status.get('installed') or status.get('signed_in'):
        raise RuntimeError(f'{driver}: expected an installed tool with no provider login')
    return {'driver': driver, 'version': item['version'],
            'executable': executable, 'sha256': item['sha256']}


def main() -> None:
    mode, profile, opencode_version = sys.argv[1:]
    if mode not in ('install', 'recreated') or profile not in ('baseline', 'defaults'):
        raise RuntimeError('invalid journey mode or resource profile')
    if os.getuid() != 65532:
        raise RuntimeError('journey must run as UID 65532')
    api = API()
    if mode == 'install':
        if any(shutil.which(tool) for tool in ('claude', 'codex', 'opencode', 'grok')):
            raise RuntimeError('the image bundles an agent CLI')
        initial = subprocess.run(
            ['olivares', 'first-boot', '--data-dir', str(DATA), '--new-token'],
            capture_output=True, text=True, timeout=30, check=True,
        )
        match = re.search(r'olst_[A-Za-z0-9]+', initial.stdout)
        if not match:
            raise RuntimeError('first boot produced no setup token')
        state = {'email': 'runtime@example.invalid', 'password': secrets.token_urlsafe(32)}
        setup = api.call('POST', '/v1/setup', dict(state, token=match.group()))
        state['tenant'] = setup['organization']['tenant_id']
    else:
        state = json.loads(STATE.read_text())
    api.tenant = state['tenant']
    api.token = api.call('POST', '/v1/auth/login', {
        'email': state['email'], 'password': state['password'],
    })['token']
    if mode == 'install':
        inventory = api.call('GET', TOOLS + '/inventory')['inventory']
        if inventory.get('installed'):
            raise RuntimeError('expected an empty managed tools inventory')
        state['tools'] = []
        for driver, version in (('opencode', opencode_version), ('claude', 'stable')):
            executable = install(api, driver, version)
            state['tools'].append(ready(api, driver, executable))
        with open(STATE, 'x', opener=lambda path, flags: os.open(path, flags, 0o600)) as out:
            json.dump(state, out)
    else:
        for tool in state['tools']:
            ready(api, tool['driver'], tool['executable'], tool['sha256'])
    slow = [sample for sample in api.timings
            if sample['path'].startswith(TOOLS + '/sign-in?') and sample['seconds'] > 5]
    print(json.dumps({'profile': profile, 'stage': mode, 'tools': state['tools'],
                      'requests': api.timings, 'status_under_five_seconds': not slow}), flush=True)
    if profile == 'defaults' and slow:
        raise RuntimeError('default resource limits exceeded five seconds for tool status')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Exception strings from networking/subprocess/JSON can include response
        # data. Emit only our own bounded diagnostics; never the engine log.
        detail = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        print('managed-tools journey FAIL: ' + detail, file=sys.stderr)
        sys.exit(1)
