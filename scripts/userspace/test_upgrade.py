#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Stored rows and API reads must survive upgrades, including disabled modules."""
import contextlib
import copy
from http.server import BaseHTTPRequestHandler, HTTPServer
import io
import json
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from upgrade import Upgrade, row_diff, snapshot_rows


class ModuleReads(unittest.TestCase):
    paths = ('/v1/m/recording/sessions', '/v1/m/redteam/targets',
             '/v1/m/redteam/runs', '/v1/m/redteam/catalog')

    def run_upgrade(self, database: str = 'sqlite', published: dict | None = None,
                    candidate: dict | None = None) -> tuple:
        disabled = (404, {'error': {'code': 'module_not_enabled'}})
        responses = {'published': published or {path: disabled for path in self.paths},
                     'candidate': candidate or {path: disabled for path in self.paths}}
        phase = 'published'
        reads = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                reads.append((phase, self.path))
                status, body = responses.get(phase, {}).get(self.path, (200, []))
                self.respond(status, body)

            def do_POST(self) -> None:
                self.rfile.read(int(self.headers.get('Content-Length', 0)))
                self.respond(200 if 'work-items' in self.path else 201, {'id': 'workspace'})

            def respond(self, status: int, body: object) -> None:
                self.send_response(status)
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

            def log_message(self, *args: object) -> None:
                pass

        with tempfile.TemporaryDirectory() as directory, HTTPServer(('127.0.0.1', 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                fixture = Upgrade(SimpleNamespace(output=Path(directory) / 'upgrade', database=database,
                                                  published=Path(__file__), candidate=Path(__file__)))
                fixture.origin = 'http://127.0.0.1:' + str(server.server_port)

                def start(binary: Path, label: str, seed: bool = False) -> None:
                    nonlocal phase
                    phase = 'candidate' if label == 'candidate' else 'published'

                ceremony = Mock(returncode=0, pid=12345)
                ceremony.communicate.return_value = ('{"aal":3}', '')
                rows = snapshot_rows([('preferences', ['id'], [{'id': 1, 'value': 'keep'}])])
                with patch.object(fixture, 'start', side_effect=start) as starts, \
                        patch.object(fixture, 'stop'), patch.object(fixture, 'provision'), \
                        patch.object(fixture, 'login', return_value={'user_id': 'owner'}), \
                        patch.object(fixture, 'command', return_value={'exit': 0, 'json': []}), \
                        patch.object(fixture, 'rows', return_value=rows) as snapshots, \
                        patch('upgrade.subprocess.Popen', return_value=ceremony), \
                        patch('upgrade.os.killpg'), contextlib.redirect_stdout(io.StringIO()):
                    rc = fixture.run()
                result = json.loads((fixture.out / 'result.json').read_text())
                return rc, result, reads, [call.args[1] for call in starts.call_args_list], snapshots.call_count
            finally:
                server.shutdown()
                thread.join(timeout=5)

    def test_disabled_modules_are_compared_and_upgrade_reaches_rollback(self) -> None:
        for database in ('sqlite', 'postgres'):
            with self.subTest(database=database):
                rc, result, reads, starts, snapshots = self.run_upgrade(database)
                self.assertEqual(result['failures'], [])
                self.assertEqual(rc, 0)
                self.assertTrue(result['complete'])
                self.assertIn('candidate', starts)
                self.assertIn('rollback', starts)
                self.assertEqual(snapshots, 3)
                self.assertEqual(result['preserved_rows'], 1)
                for path in self.paths:
                    self.assertIn(('published', path), reads)
                    self.assertIn(('candidate', path), reads)

    def test_enabled_modules_remain_required_to_return_200(self) -> None:
        enabled = {path: (200, []) for path in self.paths}
        rc, result, _, _, _ = self.run_upgrade(published=enabled, candidate=enabled)
        self.assertEqual((rc, result['complete'], result['failures']), (0, True, []))

    def test_module_status_changes_fail_in_both_directions(self) -> None:
        # Keep the body identical so only the status comparison can catch this.
        body = {'error': {'code': 'module_not_enabled'}}
        for path in self.paths:
            for old, new in ((404, 200), (200, 404)):
                with self.subTest(path=path, old=old, new=new):
                    published = {path: (old, body)}
                    candidate = {path: (new, body)}
                    rc, result, _, _, _ = self.run_upgrade(published=published, candidate=candidate)
                    self.assertEqual(rc, 1)
                    self.assertIn(f'{path}: HTTP status changed from {old} to {new}', result['failures'])

    def test_redteam_edition_refusal_preserves_upgrade_and_rollback(self) -> None:
        enabled = {path: (200, []) for path in self.paths}
        candidate = dict(enabled)
        for path in self.paths[1:]:
            candidate[path] = (501, {'error': 'Red team is a Business feature: https://olivares.ai/pricing'})
        rc, result, _, starts, snapshots = self.run_upgrade(published=enabled, candidate=candidate)
        self.assertEqual((rc, result['complete'], result['failures']), (0, True, []))
        self.assertIn('rollback', starts)
        self.assertEqual(snapshots, 3)
        self.assertEqual(result['preserved_rows'], 1)

    def test_edition_refusal_does_not_hide_unrelated_or_disabled_module_changes(self) -> None:
        refusal = (501, {'error': 'Red team is a Business feature'})
        for path in self.paths:
            with self.subTest(path=path):
                rc, result, _, _, _ = self.run_upgrade(candidate={path: refusal})
                self.assertEqual(rc, 1)
                self.assertTrue(result['failures'])
        rc, result, _, _, _ = self.run_upgrade(candidate={self.paths[1]: (501, {'error': 'unavailable'})})
        self.assertEqual(rc, 1)
        self.assertFalse(result['complete'])

    def test_other_errors_still_abort(self) -> None:
        errors = [(401, {'error': {'code': 'unauthorized'}}),
                  (403, {'error': {'code': 'forbidden'}}),
                  (404, {'error': {'code': 'not_found'}}),
                  (500, {'error': {'code': 'module_not_enabled'}}),
                  (404, None), (404, []), (404, {}), (404, {'error': None}),
                  (404, {'error': 'module_not_enabled'})]
        for phase in ('published', 'candidate'):
            for status, body in errors:
                with self.subTest(phase=phase, status=status, body=body):
                    rc, result, _, starts, _ = self.run_upgrade(**{phase: {self.paths[0]: (status, body)}})
                    self.assertEqual(rc, 1)
                    self.assertFalse(result['complete'])
                    self.assertEqual(result['failures'], [f'GET {self.paths[0]}: HTTP {status}'])
                    if phase == 'candidate':
                        self.assertIn('candidate', starts)

    def test_disabled_error_on_core_route_still_aborts(self) -> None:
        rc, result, _, _, _ = self.run_upgrade(published={
            '/v1/users': (404, {'error': {'code': 'module_not_enabled'}})})
        self.assertEqual(rc, 1)
        self.assertEqual(result['failures'], ['GET /v1/users: HTTP 404'])


class StoredData(unittest.TestCase):
    def deployment_snapshot(self, doc, table='deployment_settings'):
        value = json.dumps(doc) if isinstance(doc, dict) else doc
        return snapshot_rows([(table, ['id'], [{'id': 'settings', 'doc': value}])])

    def module_documents(self, version=None, selected=None):
        before = {'version': 'olivares.deployment.settings.v1',
                  'modules': {'selected': ['identity', 'knowledge'] if selected is None else selected,
                              'updated_at': 'old'},
                  'tracing': {'enabled': False}, 'previews_hidden': True}
        if version is not None:
            before['modules']['version'] = version
        after = copy.deepcopy(before)
        after['modules'].update(version='olivares.module.profile.v2',
                                selected=sorted(set(before['modules']['selected']) | {'liveingest'}),
                                updated_at='new')
        return before, after

    def test_module_profile_v1_upgrade_preserves_settings(self):
        for version in (None, '', 'olivares.module.profile.v1'):
            for selected in ([], ['identity', 'knowledge'], ['identity', 'liveingest']):
                with self.subTest(version=version, selected=selected):
                    before, after = self.module_documents(version, selected)
                    self.assertEqual(row_diff(self.deployment_snapshot(before),
                                              self.deployment_snapshot(after)), [])

    def test_module_profile_upgrade_rejects_lost_or_unexpected_values(self):
        before, migrated = self.module_documents()
        variants = []
        for selected in (['identity', 'liveingest'], ['identity', 'knowledge'],
                         ['identity', 'knowledge', 'liveingest', 'voice']):
            after = copy.deepcopy(migrated)
            after['modules']['selected'] = selected
            variants.append(after)
        for key in ('tracing', 'previews_hidden'):
            after = copy.deepcopy(migrated)
            del after[key]
            variants.append(after)
        after = copy.deepcopy(migrated)
        after['tracing']['enabled'] = True
        variants.append(after)
        after = copy.deepcopy(migrated)
        after['modules']['custom_value'] = 'unexpected'
        variants.append(after)
        for after in variants:
            with self.subTest(after=after):
                failures = row_diff(self.deployment_snapshot(before), self.deployment_snapshot(after))
                self.assertEqual(len(failures), 1)
                self.assertIn('deployment_settings.doc: stored value changed', failures[0])

    def test_module_profile_exception_is_only_v1_to_v2(self):
        for version in ('olivares.module.profile.v2', 'olivares.module.profile.v3'):
            before, after = self.module_documents(version)
            self.assertTrue(row_diff(self.deployment_snapshot(before), self.deployment_snapshot(after)))
        before, after = self.module_documents()
        after['modules']['version'] = 'olivares.module.profile.v3'
        self.assertTrue(row_diff(self.deployment_snapshot(before), self.deployment_snapshot(after)))
        before, after = self.module_documents()
        self.assertTrue(row_diff(self.deployment_snapshot(after), self.deployment_snapshot(before)))
        self.assertTrue(row_diff(self.deployment_snapshot(before, table='preferences'),
                                 self.deployment_snapshot(after, table='preferences')))

    def test_invalid_module_documents_still_fail_closed(self):
        before, after = self.module_documents()
        invalid = ['invalid JSON', '[]', 'null', '{}']
        for modules in (None, {'selected': 'identity'}, {'selected': [{}]}):
            invalid.append({'version': 'olivares.deployment.settings.v1', 'modules': modules})
        for doc in invalid:
            with self.subTest(doc=doc):
                self.assertTrue(row_diff(self.deployment_snapshot(doc), self.deployment_snapshot(after)))
                self.assertTrue(row_diff(self.deployment_snapshot(before), self.deployment_snapshot(doc)))

    def test_module_profile_upgrade_does_not_hide_row_loss(self):
        before, after = self.module_documents()
        snapshot = self.deployment_snapshot(after)
        self.assertTrue(row_diff(self.deployment_snapshot(before), {'deployment_settings': {}}))
        self.assertTrue(row_diff(self.deployment_snapshot(before), {}))
        next(iter(snapshot['deployment_settings'].values()))['count'] = 0
        self.assertTrue(row_diff(self.deployment_snapshot(before), snapshot))

    def test_composite_key_rows_are_not_overwritten(self):
        rows = [{'tenant': 'a', 'key': 'theme', 'value': 'dark'},
                {'tenant': 'b', 'key': 'theme', 'value': 'light'}]
        before = snapshot_rows([('preferences', ['tenant', 'key'], rows)])
        after = snapshot_rows([('preferences', ['tenant', 'key'], rows[:1])])
        self.assertEqual(len(row_diff(before, after)), 1)

    def test_no_primary_key_duplicates_are_preserved(self):
        row = {'message': 'keep this record'}
        before = snapshot_rows([('records', [], [row, row])])
        after = snapshot_rows([('records', [], [row])])
        self.assertEqual(len(row_diff(before, after)), 1)

    def test_user_setting_change_fails(self):
        before = snapshot_rows([('preferences', ['id'], [{'id': 1, 'value': 'dark'}])])
        after = snapshot_rows([('preferences', ['id'], [{'id': 1, 'value': 'light'}])])
        self.assertTrue(row_diff(before, after))

    def test_timestamp_maintenance_keeps_custom_value(self):
        before = snapshot_rows([('preferences', ['id'], [{'id': 1, 'value': 'dark', 'updated_at': 'old', 'version': 1}])])
        after = snapshot_rows([('preferences', ['id'], [{'id': 1, 'value': 'dark', 'updated_at': 'new', 'version': 2}])])
        self.assertEqual(row_diff(before, after), [])

    def test_empty_published_table_is_not_silently_removed(self):
        self.assertEqual(row_diff(snapshot_rows([('records', [], [])]), {}), ['records: table removed'])

    def test_audit_append_updates_head_but_cannot_rewrite_ledger(self):
        before = snapshot_rows([('audit_heads', ['tenant'], [{'tenant': 'a', 'seq': 1, 'hash': 'old'}]),
                                ('audit_events', ['seq'], [{'seq': 1, 'hash': 'keep'}])])
        after = snapshot_rows([('audit_heads', ['tenant'], [{'tenant': 'a', 'seq': 2, 'hash': 'new'}]),
                               ('audit_events', ['seq'], [{'seq': 1, 'hash': 'changed'}, {'seq': 2, 'hash': 'new'}])])
        self.assertEqual(len(row_diff(before, after)), 1)
        self.assertIn('audit_events.hash', row_diff(before, after)[0])

    def test_connection_tenant_context_is_not_a_persisted_record(self):
        self.assertEqual(snapshot_rows([('_scope_tenant', [], [{'tenant_id': 'last connection'}])]), {})
