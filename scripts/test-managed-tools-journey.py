#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Failure checks for the managed-install journey; no Docker or network needed."""

import contextlib
import copy
import importlib.util
import io
import json
import tempfile
import unittest
import urllib.parse
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    'journey', Path(__file__).with_name('managed-tools-journey.py'))
journey = importlib.util.module_from_spec(spec)
spec.loader.exec_module(journey)


class ManagedToolsJourneyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.executable = '/var/lib/olivares/tools/opencode/1/bin/opencode'
        self.item = {'driver': 'opencode', 'executable': self.executable,
                     'state': 'installed', 'is_executable': True,
                     'sha256': 'a' * 64, 'version': '1'}
        self.inventory = {'root': '/var/lib/olivares/tools', 'installed': [self.item]}

    def test_verified_tool_is_accepted(self) -> None:
        self.assertEqual(journey.require_installed(self.inventory, 'opencode',
                                                  self.executable), self.item)

    def test_missing_damaged_unverified_nonexecutable_and_duplicate_fail(self) -> None:
        for field, value in (('state', 'damaged'), ('state', 'unverified'),
                             ('is_executable', False), ('sha256', ''),
                             ('driver', 'claude')):
            with self.subTest(field=field, value=value):
                inventory = copy.deepcopy(self.inventory)
                inventory['installed'][0][field] = value
                with self.assertRaises(RuntimeError):
                    journey.require_installed(inventory, 'opencode', self.executable)
        self.inventory['installed'].append(self.item)
        with self.assertRaises(RuntimeError):
            journey.require_installed(self.inventory, 'opencode', self.executable)

    def test_installation_outside_volume_fails(self) -> None:
        self.inventory['root'] = '/usr/local'
        with self.assertRaises(RuntimeError):
            journey.require_installed(self.inventory, 'opencode', self.executable)
        self.inventory['root'] = '/var/lib/olivares/tools'
        for executable in ('/usr/local/bin/opencode',
                           '/var/lib/olivares/tools/../../bin/opencode'):
            self.item['executable'] = executable
            with self.assertRaises(RuntimeError):
                journey.require_installed(self.inventory, 'opencode', executable)

    def test_failed_or_unaudited_install_is_not_success(self) -> None:
        for outcome in ({'state': 'failed'}, {'state': 'interrupted'},
                        {'state': 'succeeded', 'error': 'journal failure'},
                        {'state': 'succeeded', 'audit_error': 'ledger failure'}):
            with self.subTest(outcome=outcome):
                api = unittest.mock.Mock()
                api.call.side_effect = [dict(digest='abc', executable=self.executable), outcome]
                with self.assertRaises(RuntimeError):
                    journey.install(api, 'opencode', '1')

    def test_install_waits_for_real_job_and_returns_plan_path(self) -> None:
        api = unittest.mock.Mock()
        api.call.side_effect = [dict(digest='abc', executable=self.executable),
                                dict(id='job', state='running'), dict(state='succeeded')]
        with patch.object(journey.time, 'sleep'):
            self.assertEqual(journey.install(api, 'opencode', '1'), self.executable)
        self.assertEqual(api.call.call_args.args, ('GET', '/v1/m/agenttools/jobs/job'))

    def test_recreated_tool_with_changed_digest_fails(self) -> None:
        api = unittest.mock.Mock()
        api.call.return_value = {'inventory': self.inventory}
        with self.assertRaisesRegex(RuntimeError, 'bytes changed'):
            journey.ready(api, 'opencode', self.executable, 'b' * 64)

    def test_ready_sends_tenant_in_sign_in_query(self) -> None:
        for driver in ('opencode', 'claude'):
            for tenant in ('11111111-1111-4111-8111-111111111111',
                           '22222222-2222-4222-8222-222222222222'):
                with self.subTest(driver=driver, tenant=tenant):
                    with patch.object(journey.ssl, 'create_default_context', return_value=None):
                        api = journey.API()
                    api.tenant = tenant
                    api.token = 'fixture-token'
                    self.item['driver'] = driver
                    with patch.object(api.opener, 'open', side_effect=[
                        io.StringIO(json.dumps({'inventory': self.inventory})),
                        io.StringIO(json.dumps({'installed': True, 'signed_in': False})),
                    ]) as opened, patch.object(Path, 'stat') as stat:
                        stat.return_value.st_uid = 65532
                        result = journey.ready(api, driver, self.executable)
                    request = opened.call_args.args[0]
                    url = urllib.parse.urlsplit(request.full_url)
                    self.assertEqual(request.get_method(), 'GET')
                    self.assertEqual(url.path, '/v1/m/agenttools/sign-in')
                    self.assertEqual(urllib.parse.parse_qs(url.query),
                                     {'driver': [driver], 'tenant_id': [tenant]})
                    self.assertEqual(request.get_header('Authorization'), 'Bearer fixture-token')
                    self.assertEqual(result['sha256'], self.item['sha256'])

    def test_cold_status_budget_and_output_never_expose_login_state(self) -> None:
        for profile, seconds, fails in (('defaults', 5, False),
                                        ('defaults', 5.001, True),
                                        ('baseline', 8, False)):
            with self.subTest(profile=profile, seconds=seconds), tempfile.TemporaryDirectory() as temp:
                state = Path(temp) / 'state.json'
                state.write_text(json.dumps({'email': 'fixture@example.invalid',
                                             'password': 'private-fixture-password',
                                             'tenant': 'tenant', 'tools': [self.item]}))
                api = unittest.mock.Mock()
                api.call.return_value = {'token': 'private-fixture-token'}
                api.timings = [{'path': '/v1/m/agenttools/sign-in?driver=opencode',
                                'seconds': seconds}]
                output = io.StringIO()
                with patch.object(journey, 'STATE', state), patch.object(journey, 'API', return_value=api), \
                        patch.object(journey.os, 'getuid', return_value=65532), \
                        patch.object(journey.sys, 'argv', ['journey', 'recreated', profile, '1']), \
                        patch.object(journey, 'ready'), contextlib.redirect_stdout(output):
                    if fails:
                        with self.assertRaisesRegex(RuntimeError, 'exceeded five seconds'):
                            journey.main()
                    else:
                        journey.main()
                report = json.loads(output.getvalue())
                self.assertEqual(report['status_under_five_seconds'], seconds <= 5)
                self.assertNotIn('private-fixture', output.getvalue())
                self.assertNotIn('fixture@example.invalid', output.getvalue())


if __name__ == '__main__':
    unittest.main()
