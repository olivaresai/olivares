#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the check through its CLI and complete fixture snapshots."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

CHECK = Path(__file__).with_name('check-userspace.py')


def snapshot():
    document = {'openapi': '3.1.0', 'paths': {'/v1/items': {'post': {
        'parameters': [{'in': 'query', 'name': 'limit', 'schema': {'type': 'integer', 'default': 20}}],
        'requestBody': {'content': {'application/json': {'schema': {'type': 'object', 'required': ['name'],
            'properties': {'name': {'type': 'string'}}}}}},
        'responses': {'200': {'content': {'application/json': {'schema': {'type': 'object',
            'required': ['id'], 'properties': {'id': {'type': 'string'}}}}}}}}}}}
    return {'schema': 'olivares.userspace/1', 'openapi': document, 'openapi_beta': copy.deepcopy(document),
            'cli': {'schema': 'olivares.cli-ref/1', 'commands': [{'path': 'olivares items ls', 'runnable': True,
                'aliases': ['list'], 'flags': [{'name': 'limit', 'type': 'int', 'default': '20', 'shorthand': 'n'}]}]},
            'config': {'listen': '127.0.0.1:9696'}, 'environment': {'OLIVARES_ENGINE': 'sqlite'},
            'paths': ['/usr/bin/olivares', 'olivares.service'],
            'migrations': {'sqlite': {'versions': [1, 2], 'files': {'001.sql': 'old-digest', '002.sql': 'second-digest'}},
                           'postgres': {'versions': [1, 2], 'files': {'001.sql': 'old-digest', '002.sql': 'second-digest'}}},
            'runtime': {'config validate': {'exit': 0, 'json': {'valid': True}}}}


class UserspaceCheck(unittest.TestCase):
    def compare(self, old, new, expected):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value in [('old', old), ('new', new)]:
                (root/name).write_text(json.dumps(value))
            result = subprocess.run([sys.executable, str(CHECK), str(root/'old'), str(root/'new')],
                                    text=True, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        return result

    def test_unchanged(self):
        old = snapshot()
        self.compare(old, copy.deepcopy(old), 0)

    def test_deprecation_warns_and_preserves_the_published_command(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['cli']['commands'][0]['deprecated'] = 'Use items list instead.'
        result = self.compare(old, new, 0)
        self.assertEqual(json.loads(result.stdout)['warnings'], ['olivares items ls: Use items list instead.'])
        new['cli']['commands'][0]['flags'][0]['deprecated'] = 'Use count instead.'
        new['openapi']['paths']['/v1/items']['post']['deprecated'] = True
        result = self.compare(old, new, 0)
        self.assertEqual(json.loads(result.stdout)['warnings'], [
            'olivares items ls --limit: Use count instead.', 'olivares items ls: Use items list instead.',
            'openapi: POST /v1/items: deprecated'])
        new['cli']['commands'][0]['runnable'] = False
        self.compare(old, new, 1)

    def test_retained_bearer_response_in_nested_cookie_union(self):
        old = snapshot()
        response = old['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']
        response['schema'] = {'oneOf': [{'$ref': '#/components/schemas/LoginResponse'}]}
        old['openapi']['components'] = {'schemas': {'LoginResponse': {'type': 'object', 'required': ['token'],
            'properties': {'token': {'type': 'string'}}}}}
        new = copy.deepcopy(old)
        new['openapi']['components']['schemas']['SessionResponse'] = {'oneOf': [
            {'$ref': '#/components/schemas/LoginResponse'}, {'type': 'object', 'properties': {'csrf': {'type': 'string'}}}]}
        new['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema'] = {
            'oneOf': [{'$ref': '#/components/schemas/SessionResponse'}]}
        self.compare(old, new, 0)

    def test_optional_secret_environment_input(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema']['properties']['secret_env'] = {
            'type': 'array', 'items': {'type': 'object', 'properties': {'name': {'type': 'string'}, 'secret_ref': {'type': 'string'}}}}
        self.compare(old, new, 0)

    def test_removed_operation(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['openapi']['paths']['/v1/items'] = {}
        self.compare(old, new, 1)

    def test_removed_response_field(self):
        old = snapshot()
        new = copy.deepcopy(old)
        del new['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']['properties']['id']
        self.compare(old, new, 1)

    def test_new_required_input(self):
        old = snapshot()
        new = copy.deepcopy(old)
        schema = new['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema']
        schema['properties']['owner'] = {'type': 'string'}
        schema['required'].append('owner')
        self.compare(old, new, 1)

    def test_null_required_counts_as_no_required_fields(self):
        old = snapshot()
        for schema in (old['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema'],
                       old['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']):
            schema['required'] = None
        self.compare(old, copy.deepcopy(old), 0)
        new = copy.deepcopy(old)
        schema = new['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema']
        schema['properties']['owner'] = {'type': 'string'}
        schema['required'] = ['name', 'owner']
        result = self.compare(old, new, 1)
        self.assertIn('new required inputs', result.stdout)

    def test_null_required_matches_empty_or_missing(self):
        old = snapshot()
        for schema in (old['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema'],
                       old['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']):
            schema['required'] = None
        for empty_required in ([], None):
            with self.subTest(empty_required=empty_required):
                new = copy.deepcopy(old)
                for schema in (new['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema'],
                               new['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']):
                    if empty_required is None:
                        del schema['required']
                    else:
                        schema['required'] = empty_required
                self.compare(old, new, 0)
                self.compare(new, old, 0)

    def test_path_level_required_parameter(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['openapi']['paths']['/v1/items']['parameters'] = [{'in': 'header', 'name': 'X-New-Required',
            'required': True, 'schema': {'type': 'string'}}]
        self.compare(old, new, 1)

    def test_parameter_reference_default_change(self):
        old = snapshot()
        old['openapi']['components'] = {'parameters': {'Limit': old['openapi']['paths']['/v1/items']['post']['parameters'][0]}}
        old['openapi']['paths']['/v1/items']['post']['parameters'] = [{'$ref': '#/components/parameters/Limit'}]
        new = copy.deepcopy(old)
        new['openapi']['components']['parameters']['Limit']['schema']['default'] = 10
        self.compare(old, new, 1)

    def test_response_required_field_weakened(self):
        old = snapshot()
        for weakening in ([], None):
            with self.subTest(weakening=weakening):
                new = copy.deepcopy(old)
                new['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']['required'] = weakening
                self.compare(old, new, 1)

    def test_removed_cli_alias(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['cli']['commands'][0]['aliases'] = []
        self.compare(old, new, 1)

    def test_flag_default_change(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['cli']['commands'][0]['flags'][0]['default'] = '0'
        self.compare(old, new, 1)

    def test_flag_now_required(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['cli']['commands'][0]['flags'][0]['required'] = True
        self.compare(old, new, 1)

    def test_config_environment_or_path_break(self):
        for key in ['config', 'environment', 'paths']:
            with self.subTest(key=key):
                old = snapshot()
                new = copy.deepcopy(old)
                if key == 'paths':
                    new[key].remove('olivares.service')
                else:
                    name = next(iter(new[key]))
                    new[key][name] = 'new default'
                self.compare(old, new, 1)

    def test_missing_inventory_is_unavailable(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['environment'] = {}
        self.compare(old, new, 2)

    def test_migration_history_cannot_be_rewritten(self):
        old = snapshot()
        for change in ('remove', 'rewrite', 'prepend'):
            with self.subTest(change=change):
                new = copy.deepcopy(old)
                history = new['migrations']['sqlite']
                if change == 'remove':
                    del history['files']['001.sql']
                elif change == 'rewrite':
                    history['files']['001.sql'] = 'rewritten-digest'
                else:
                    history['versions'] = [1, 3]
                self.compare(old, new, 1)

    def test_forward_migration_and_json_fields_are_additive(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['migrations']['sqlite']['versions'].append(3)
        new['migrations']['sqlite']['files']['003.sql'] = 'new-digest'
        new['runtime']['config validate']['json']['extra'] = True
        self.compare(old, new, 0)

    def test_cli_exit_and_json_contracts_are_preserved(self):
        old = snapshot()
        for change in ('case', 'exit', 'key', 'type'):
            with self.subTest(change=change):
                new = copy.deepcopy(old)
                if change == 'case':
                    new['runtime'] = {'another case': {'exit': 0}}
                elif change == 'exit':
                    new['runtime']['config validate']['exit'] = 1
                elif change == 'key':
                    new['runtime']['config validate']['json'] = {}
                else:
                    new['runtime']['config validate']['json']['valid'] = 'yes'
                self.compare(old, new, 1)

    def test_added_output_enum_requires_published_sdk_tolerance(self):
        old = snapshot()
        old['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']['properties']['id']['enum'] = ['old']
        new = copy.deepcopy(old)
        new['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']['properties']['id']['enum'].append('new')
        self.compare(old, new, 1)
        old['open_json_operations'] = ['POST /v1/items']
        result = self.compare(old, new, 0)
        self.assertIn('enum expanded', json.loads(result.stdout)['warnings'][0])
        new['openapi']['paths']['/v1/items']['post']['responses']['200']['content']['application/json']['schema']['properties']['id']['enum'] = ['renamed']
        self.compare(old, new, 1)

    def test_effective_default_remains_required_when_metadata_changes(self):
        old = snapshot()
        old['cli']['commands'][0]['flags'][0]['effective_default'] = '20'
        new = copy.deepcopy(old)
        new['cli']['commands'][0]['flags'][0]['default'] = ''
        result = self.compare(old, new, 0)
        self.assertIn('metadata default changed', json.loads(result.stdout)['warnings'][0])
        new['cli']['commands'][0]['flags'][0]['effective_default'] = '0'
        self.compare(old, new, 1)

    def test_unknown_reference_is_unavailable(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['openapi']['paths']['/v1/items']['post']['requestBody']['content']['application/json']['schema'] = {'$ref': '#/missing'}
        self.compare(old, new, 2)

    def test_classified_metadata_requires_exact_values_and_finding(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['openapi']['paths']['/v1/items']['post']['parameters'][0]['schema']['default'] = 100
        row = {
            'finding': "openapi: POST /v1/items ('query', 'limit'): changed default",
            'path': ['openapi', 'paths', '/v1/items', 'post', 'parameters',
                     {'in': 'query', 'name': 'limit'}, 'schema'],
            'published': {'type': 'integer', 'default': 20},
            'candidate': {'type': 'integer', 'default': 100},
            'reason': 'Documentation corrected to the unchanged native page size.',
            'evidence': 'The published and candidate list handler both default to 100.'}
        self.compare(old, new, 1)
        new['classifications'] = [row]
        result = json.loads(self.compare(old, new, 0).stdout)
        self.assertEqual(result['findings'], [])
        self.assertEqual(result['classified'], [row])
        for change in ('value', 'baseline', 'other-path', 'same-parameter-break'):
            with self.subTest(change=change):
                before, after = copy.deepcopy(old), copy.deepcopy(new)
                if change == 'value':
                    after['openapi']['paths']['/v1/items']['post']['parameters'][0]['schema']['default'] = 101
                elif change == 'baseline':
                    before['openapi']['paths']['/v1/items']['post']['parameters'][0]['schema']['default'] = 21
                elif change == 'other-path':
                    after['openapi_beta']['paths']['/v1/items']['post']['parameters'][0]['schema']['default'] = 100
                else:
                    after['openapi']['paths']['/v1/items']['post']['parameters'][0]['required'] = True
                self.compare(before, after, 1)

    def test_classified_sql_comments_do_not_allow_new_hashes_or_removal(self):
        old = snapshot()
        new = copy.deepcopy(old)
        new['migrations']['sqlite']['files']['001.sql'] = 'comment-only-digest'
        new['classifications'] = [{
            'finding': 'sqlite: published migration removed or rewritten: 001.sql',
            'path': ['migrations', 'sqlite', 'files', '001.sql'],
            'published': 'old-digest', 'candidate': 'comment-only-digest',
            'reason': 'The SQL statement bytes are unchanged.',
            'evidence': 'Compared complete published and candidate SQL, only comments differ.'}]
        self.compare(old, new, 0)
        for change in ('rewrite', 'remove', 'rename', 'other-file', 'versions'):
            with self.subTest(change=change):
                after = copy.deepcopy(new)
                files = after['migrations']['sqlite']['files']
                if change == 'rewrite':
                    files['001.sql'] = 'unclassified-digest'
                elif change in ('remove', 'rename'):
                    value = files.pop('001.sql')
                    if change == 'rename':
                        files['renamed.sql'] = value
                elif change == 'other-file':
                    files['002.sql'] = 'comment-only-digest'
                else:
                    after['migrations']['sqlite']['versions'] = [1, 3]
                self.compare(old, after, 1)

    def test_classification_requires_reason_evidence_and_unique_finding(self):
        old = snapshot()
        row = {'finding': 'example', 'path': ['config'], 'published': old['config'],
               'candidate': old['config'], 'reason': 'Metadata only.', 'evidence': 'Native output.'}
        for missing in ('reason', 'evidence', 'path', 'published', 'candidate'):
            with self.subTest(missing=missing):
                new = copy.deepcopy(old)
                new['classifications'] = [dict(row)]
                del new['classifications'][0][missing]
                self.compare(old, new, 2)
        new['classifications'] = [row, row]
        self.compare(old, new, 2)


if __name__ == '__main__':
    unittest.main()
