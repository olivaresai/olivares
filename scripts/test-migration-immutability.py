# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the real migration gate against a Git baseline."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ImmutableMigrations(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
        self.env['OLIVARES_MIGRATION_BASE'] = 'HEAD'
        (self.root / 'scripts').mkdir()
        shutil.copy(ROOT / 'scripts/check-migrations.sh', self.root / 'scripts')
        for name in ('check-migration-hashes.py', 'migrationhash'):
            src = ROOT / 'scripts' / name
            if src.is_dir():
                shutil.copytree(src, self.root / 'scripts' / name)
            elif src.exists():
                shutil.copy(src, self.root / 'scripts')
        self.sql = self.write('modules/example/migrations/sqlite/0001_create.sql',
                              'CREATE TABLE example (id TEXT PRIMARY KEY);\n')
        self.go = self.write('core/internal/store/sqlstore/schema.go', '''package sqlstore
import "example/migrate"
const firstVersion = 1
func seed() string { return "CREATE TABLE core (id TEXT)" }
func coreSeedMigration() migrate.Migration {
 return migrate.Migration{Version: firstVersion, Name: "seed", Exec: func() { execute(seed()) }}
}
func buildCoreMigrations() []migrate.Migration {
 return append([]migrate.Migration{{Version: 2, Name: "direct", Stmts: []string{"CREATE TABLE other (id TEXT)"}}}, coreSeedMigration())
}
''')
        self.git('init', '-q')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'baseline')

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)
        return target

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.root, env=self.env,
                              check=True, capture_output=True, text=True)

    def gate(self):
        return subprocess.run(['bash', 'scripts/check-migrations.sh'], cwd=self.root,
                              env=self.env, capture_output=True, text=True)

    def assert_rejected(self, name):
        result = self.gate()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn(name, result.stdout + result.stderr)
        self.assertIn('immutable', result.stdout + result.stderr)

    def test_unchanged_and_new_version_pass(self):
        self.assertEqual(self.gate().returncode, 0)
        self.write('modules/example/migrations/sqlite/0002_add.sql',
                   'ALTER TABLE example ADD COLUMN name TEXT;\n')
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_external_package_call_does_not_hash_unrelated_local_method(self):
        self.go.write_text(self.go.read_text().replace('import "example/migrate"', 'import "example/migrate"\nimport "database/sql"').replace('execute(seed())', 'sql.Open("sqlite", seed())'))
        path = self.write('core/internal/store/sqlstore/boot.go', 'package sqlstore\ntype local struct{}\nfunc (*local) Open() { unrelatedBootWork() }\nfunc unrelatedBootWork() {}\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'external call baseline')
        path.write_text(path.read_text().replace('func unrelatedBootWork() {}', 'func unrelatedBootWork() { addedBootStep() }'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def runner_plan(self):
        path = self.write('core/api/ratelimit/pgstore/schema.go', 'package pgstore\nimport "github.com/olivaresai/olivares/core/migrate"\nconst tableDDL = "CREATE TABLE buckets (id TEXT)"\nfunc install() {\n stmts := []string{tableDDL}\n migrate.Apply(ctx, db, dia, "schema_migrations_ratelimit", []migrate.Migration{{Version: 1, Name: "buckets", Stmts: stmts}})\n}\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'runner baseline')
        return path

    def test_auxiliary_runner_dependency_is_immutable(self):
        path = self.runner_plan()
        path.write_text(path.read_text().replace('buckets (id TEXT)', 'buckets (id TEXT, label TEXT)'))
        self.assert_rejected('runner/')

    def test_auxiliary_runner_allows_new_version(self):
        path = self.runner_plan()
        path.write_text(path.read_text().replace('Stmts: stmts}})', 'Stmts: stmts}, {Version: 2, Name: "next", Stmts: []string{"CREATE TABLE other (id TEXT)"}}})'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_auxiliary_runner_binding_cannot_disappear(self):
        path = self.runner_plan()
        path.write_text(path.read_text().replace('migrate.Apply(ctx, db, dia, "schema_migrations_ratelimit",', 'discard('))
        self.assert_rejected('runner/')

    def test_auxiliary_runner_reassignment_cannot_discard_the_plan(self):
        path = self.runner_plan()
        source = path.read_text().replace('migrate.Apply(ctx, db, dia, "schema_migrations_ratelimit", []migrate.Migration{{Version: 1, Name: "buckets", Stmts: stmts}})',
                                          'plan := []migrate.Migration{{Version: 1, Name: "buckets", Stmts: stmts}}\n migrate.Apply(ctx, db, dia, "schema_migrations_ratelimit", plan)')
        path.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'bound auxiliary plan')
        path.write_text(source.replace('migrate.Apply(', 'plan = nil\n migrate.Apply('))
        self.assert_rejected('runner/')

    def test_declared_epoch_restarts_history_once(self):
        self.write('core/migrate/EPOCH.txt', '0.1\n')
        self.sql.write_text('CREATE TABLE example (id TEXT PRIMARY KEY, label TEXT);\n')
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('epoch 0.1', result.stdout + result.stderr)

    def test_epoch_in_the_baseline_keeps_history_immutable(self):
        self.write('core/migrate/EPOCH.txt', '0.1\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'epoch')
        self.sql.write_text('CREATE TABLE example (id TEXT PRIMARY KEY, label TEXT);\n')
        self.assert_rejected('0001_create.sql')

    def test_edited_sql_is_rejected_even_when_additive(self):
        self.sql.write_text('CREATE TABLE example (id TEXT PRIMARY KEY, label TEXT);\n')
        self.assert_rejected('0001_create.sql')

    def test_removed_sql_is_rejected(self):
        self.git('rm', str(self.sql.relative_to(self.root)))
        self.assert_rejected('0001_create.sql')

    def test_edited_core_go_is_rejected(self):
        self.go.write_text(self.go.read_text().replace('CREATE TABLE other (id TEXT)',
                                                      'CREATE TABLE other (id TEXT, label TEXT)'))
        self.assert_rejected('core/2')

    def test_edited_callback_helper_is_rejected(self):
        self.go.write_text(self.go.read_text().replace('CREATE TABLE core (id TEXT)',
                                                      'CREATE TABLE core (id TEXT, label TEXT)'))
        self.assert_rejected('core/1')

    def test_d1_is_immutable_too(self):
        path = self.write('commercial/license-worker/migrations/0001_initial.sql',
                          'CREATE TABLE licenses (id TEXT);\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'd1 baseline')
        path.write_text('CREATE TABLE licenses (id TEXT, name TEXT);\n')
        self.assert_rejected('0001_initial.sql')

    def test_never_applied_exemption_is_bound_to_its_content(self):
        path = self.write('commercial/license-worker/migrations/0063_instance_lease.sql',
                          'CREATE TABLE other_content (id TEXT);\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'd1 baseline')
        self.git('rm', '-q', str(path.relative_to(self.root)))
        self.assert_rejected('0063_instance_lease.sql')

    def test_new_go_version_passes(self):
        self.go.write_text(self.go.read_text().replace(
            '}, coreSeedMigration())',
            '}, coreSeedMigration(), migrate.Migration{Version: 3, Name: "next"})'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_unregistered_constructor_is_rejected(self):
        self.go.write_text(self.go.read_text().replace(', coreSeedMigration())', ')'))
        self.assert_rejected('core/1')

    def test_new_descriptor_does_not_rewrite_prior_versions(self):
        self.go.write_text(self.go.read_text() + '\nvar newDescriptor = "new table"\n')
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_changed_module_transition_is_rejected(self):
        path = self.write('modules/example/schema.go', '''package example
import "example/store"
func transitions() {
 _ = []store.SchemaTriggerTransition{{MigrationVersion: 1, PreviousDefinitionSHA256: "old"}}
}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'module transition')
        path.write_text(path.read_text().replace('"old"', '"rewritten"'))
        self.assert_rejected('modules/example/schema.go/transition/1')

    def test_blank_identifier_is_not_a_migration_dependency(self) -> None:
        self.write('modules/example/schema.go', """package example
import "example/store"
func transitions() {
 _ = []store.SchemaTriggerTransition{{MigrationVersion: 1, PreviousDefinitionSHA256: digest()}}
}
func digest() string { return "old" }
""")
        unrelated = self.write('modules/example/runtime.go', """package example
var _ = runtimeName()
func runtimeName() string { return "original runtime" }
""")
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'transition with unrelated blank declaration')
        unrelated.write_text(unrelated.read_text().replace('original runtime', 'new runtime'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

        # A real dependency in the same transition must still be immutable.
        schema = self.root / 'modules/example/schema.go'
        schema.write_text(schema.read_text().replace('return "old"', 'return "rewritten"'))
        self.assert_rejected('modules/example/schema.go/transition/1')

    def test_export_ignore_cannot_hide_baseline(self):
        self.write('.gitattributes', 'modules/** export-ignore\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'export attributes')
        self.sql.write_text('CREATE TABLE rewritten (id TEXT);\n')
        self.assert_rejected('0001_create.sql')

    def test_module_callback_helper_is_immutable(self):
        path = self.write('core/internal/store/sqlstore/modulemig_transition.go', '''package sqlstore
func attachSchemaTransitionHooks() { verifyPrevious() }
func verifyPrevious() string { return "old" }
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'transition hooks')
        path.write_text(path.read_text().replace('"old"', '"new"'))
        self.assert_rejected('modules/transition-hooks')

    def test_existing_descriptor_is_immutable(self):
        self.go.write_text(self.go.read_text() + '\nvar oldDescriptor = "original table"\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'descriptor')
        self.go.write_text(self.go.read_text().replace('"original table"', '"changed table"'))
        self.assert_rejected('core/descriptor/oldDescriptor')

    def test_duplicate_sql_version_refuses(self):
        self.write('modules/example/migrations/sqlite/001_duplicate.sql',
                   'CREATE TABLE duplicate (id TEXT);\n')
        result = self.gate()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn('duplicate migration version', result.stdout + result.stderr)

    def test_descriptor_registration_cannot_disappear(self):
        self.go.write_text(self.go.read_text() + '''
var oldDescriptor = "original table"
func coreDescriptors() []model.EntityDescriptor {
 return []model.EntityDescriptor{oldDescriptor, newDescriptor}
}
var newDescriptor = "second table"
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'registered descriptors')
        self.go.write_text(self.go.read_text().replace('{oldDescriptor, newDescriptor}', '{newDescriptor}'))
        self.assert_rejected('core/descriptor-registration')

    def test_module_transition_binding_is_immutable(self):
        path = self.write('modules/example/schema.go', '''package example
import "example/store"
func transitions(name string) {
 if name == "original_trigger" {
  _ = []store.SchemaTriggerTransition{{MigrationVersion: 1, PreviousDefinitionSHA256: "old"}}
 }
}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'transition binding')
        path.write_text(path.read_text().replace('"original_trigger"', '"wrong_trigger"'))
        self.assert_rejected('modules/example/schema.go/transition/1')

    def test_descriptor_catalog_control_flow_is_immutable(self):
        self.go.write_text(self.go.read_text() + '''
var oldDescriptor = "original table"
func coreDescriptors() []model.EntityDescriptor {
 ds := []model.EntityDescriptor{oldDescriptor}
 return append(ds, oldDescriptor)
}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'descriptor catalog control')
        self.go.write_text(self.go.read_text().replace('return append(ds, oldDescriptor)',
                                                      'return append([]model.EntityDescriptor{}, oldDescriptor)'))
        self.assert_rejected('core/descriptor-registration/control')

    def test_plan_cannot_discard_constructed_migrations(self):
        source = self.go.read_text().replace('return append([]migrate.Migration',
                                            'migrations := []migrate.Migration')
        source = source.replace('}, coreSeedMigration())', '}\n return append(migrations, coreSeedMigration())')
        self.go.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'plan with local migration list')
        self.go.write_text(source.replace('return append(migrations,',
                                          'return append([]migrate.Migration{},'))
        self.assert_rejected('core/plan/buildCoreMigrations')

    def boot_bindings(self):
        path = self.write('core/internal/store/sqlstore/store.go', '''package sqlstore
func guardBootstrapExec(dia, manifest string) string { return manifest }
func originalManifest() string { return "original" }
func replacementManifest() string { return "replacement" }
func coreDescriptors() []model.EntityDescriptor {
 return []model.EntityDescriptor{oldDescriptor}
}
var oldDescriptor = "table"
func Open() {
 manifest := originalManifest()
 callback := guardBootstrapExec("sqlite", manifest)
 _ = buildCoreMigrations("sqlite", coreDescriptors(), callback, nil)
 _ = buildCoreMigrationPlan("sqlite", coreDescriptors(), callback, nil)
}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'boot callback bindings')
        return path

    def test_boot_callback_injection_is_immutable(self):
        path = self.boot_bindings()
        source = path.read_text()
        for plan in ('buildCoreMigrations', 'buildCoreMigrationPlan'):
            with self.subTest(plan=plan):
                path.write_text(source.replace(
                    plan + '("sqlite", coreDescriptors(), callback, nil)',
                    plan + '("sqlite", coreDescriptors(), nil, nil)'))
                self.assert_rejected('core/binding/')

    def test_boot_plan_call_cannot_disappear(self):
        path = self.boot_bindings()
        path.write_text(path.read_text().replace(
            '_ = buildCoreMigrations("sqlite", coreDescriptors(), callback, nil)', ''))
        self.assert_rejected('core/binding/')

    def test_boot_callback_variable_initializer_is_immutable(self):
        path = self.boot_bindings()
        path.write_text(path.read_text().replace('callback :=', 'var callback ='))
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'variable callback initializer')
        path.write_text(path.read_text().replace(
            'var callback = guardBootstrapExec("sqlite", manifest)', 'var callback = nil'))
        self.assert_rejected('core/binding/')

    def test_boot_callback_initializer_is_immutable(self):
        path = self.boot_bindings()
        path.write_text(path.read_text().replace(
            'callback := guardBootstrapExec("sqlite", manifest)', 'callback := nil'))
        self.assert_rejected('core/binding/')

    def test_boot_callback_dependency_is_immutable(self):
        path = self.boot_bindings()
        path.write_text(path.read_text().replace('return manifest', 'return "changed"'))
        self.assert_rejected('core/binding/')

    def test_boot_callback_input_is_immutable(self):
        path = self.boot_bindings()
        path.write_text(path.read_text().replace(
            'manifest := originalManifest()', 'manifest := replacementManifest()'))
        self.assert_rejected('core/binding/')

    def test_boot_reassignment_is_immutable(self):
        path = self.boot_bindings()
        source = path.read_text()
        for statement in (
                'callback = nil',
                'callback, manifest = nil, "changed"',
                'manifest = replacementManifest()',
                'manifest += "changed"',
                'manifest.Value = "changed"',
                'manifest[0] = "changed"',
                'manifest.Count++',
                'if enabled { callback = nil }',
                'for enabled { callback = nil }',
                'for _, callback = range replacements {}'):
            with self.subTest(statement=statement):
                path.write_text(source.replace(
                    'callback := guardBootstrapExec("sqlite", manifest)',
                    'callback := guardBootstrapExec("sqlite", manifest)\n ' + statement))
                self.assert_rejected('core/binding/')

    def test_boot_second_assignment_change_is_immutable(self):
        path = self.boot_bindings()
        source = path.read_text().replace(
            'callback := guardBootstrapExec("sqlite", manifest)',
            'callback := guardBootstrapExec("sqlite", manifest)\n callback = replacement')
        path.write_text(source + '\nvar replacement = guardBootstrapExec("sqlite", "original")\n')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'second callback assignment')
        path.write_text(path.read_text().replace('callback = replacement', 'callback = nil'))
        self.assert_rejected('core/binding/')

    def test_boot_reassignment_dependency_and_condition_are_immutable(self):
        path = self.boot_bindings()
        source = path.read_text().replace(
            'manifest := originalManifest()',
            'manifest := originalManifest()\n if enabled { manifest = replacementManifest() }')
        path.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'conditional input assignment')
        for old, new in (('if enabled', 'if !enabled'),
                         ('return "replacement"', 'return "changed"')):
            with self.subTest(change=old):
                path.write_text(source.replace(old, new))
                self.assert_rejected('core/binding/')

    def test_boot_unrelated_and_shadowed_assignments_pass(self):
        path = self.boot_bindings()
        path.write_text(path.read_text().replace(
            'manifest := originalManifest()',
            'other := "original"\n other = "changed"\n _ = other\n'
            ' { callback := "shadow"; callback = "changed"; _ = callback }\n'
            ' manifest := originalManifest()'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_boot_tuple_destination_does_not_import_unrelated_error_writes(self) -> None:
        path = self.boot_bindings()
        source = path.read_text().replace(
            'manifest := originalManifest()',
            'manifest := originalManifest()\n var err error\n manifest, err = loadManifest()')
        source += '\nfunc loadManifest() (string, error) { return "original", nil }\n'
        source = source.replace('callback :=', 'err = runtimeError()\n callback :=')
        source += '\nfunc runtimeError() error { return nil }\n'
        path.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'tuple migration input with unrelated error destination')
        path.write_text(source.replace('err = runtimeError()\n ', ''))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

        # The tuple-producing call and actual input writes remain immutable.
        for old, new in (('return "original", nil', 'return "changed", nil'),
                         ('manifest, err = loadManifest()', 'manifest, err = replacementManifest(), nil')):
            with self.subTest(change=old):
                path.write_text(source.replace(old, new))
                self.assert_rejected('core/binding/')

    def test_boot_tuple_error_read_still_tracks_its_writes(self) -> None:
        path = self.boot_bindings()
        source = path.read_text().replace(
            'manifest := originalManifest()',
            'manifest := originalManifest()\n var err error\n manifest, err = loadManifest()\n'
            ' err = runtimeError()\n if err != nil { manifest = replacementManifest() }')
        source += '\nfunc loadManifest() (string, error) { return "original", nil }\n'
        source += '\nfunc runtimeError() error { return nil }\n'
        path.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'tuple error controls migration input')
        path.write_text(source.replace('err = runtimeError()', 'err = nil'))
        self.assert_rejected('core/binding/')

    def test_boot_assignment_order_is_immutable(self):
        path = self.boot_bindings()
        source = path.read_text().replace(
            'manifest := originalManifest()',
            'manifest := originalManifest()\n manifest = replacementManifest()')
        path.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'input assigned before callback construction')
        self.assertEqual(self.gate().returncode, 0)
        path.write_text(source.replace('\n manifest = replacementManifest()', '').replace(
            'callback := guardBootstrapExec("sqlite", manifest)',
            'callback := guardBootstrapExec("sqlite", manifest)\n manifest = replacementManifest()'))
        self.assert_rejected('core/binding/')

    def test_boot_assignment_branch_is_immutable(self):
        path = self.boot_bindings()
        source = path.read_text().replace(
            'manifest := originalManifest()',
            'manifest := originalManifest()\n if enabled { manifest = replacementManifest() }')
        path.write_text(source)
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'input assigned in then branch')
        self.assertEqual(self.gate().returncode, 0)
        path.write_text(source.replace(
            'if enabled { manifest = replacementManifest() }',
            'if enabled {} else { manifest = replacementManifest() }'))
        self.assert_rejected('core/binding/')

    def test_boot_bindings_allow_new_version_and_descriptor(self):
        path = self.boot_bindings()
        self.go.write_text(self.go.read_text().replace(
            '}, coreSeedMigration())',
            '}, coreSeedMigration(), migrate.Migration{Version: 3, Name: "next"})'))
        path.write_text(path.read_text().replace(
            '{oldDescriptor}', '{oldDescriptor, newDescriptor}') +
            '\nvar newDescriptor = "new table"\n')
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def cited_descriptor(self):
        # model.None's reason only cites the reader lines (file.go:line) that show no principal;
        # nothing generates schema from it. pdeclStrings forwards its parameter to model.None.
        path = self.write('core/internal/store/sqlstore/cited.go', '''package sqlstore
import "github.com/olivaresai/olivares/core/model"
var citedDescriptor = model.EntityDescriptor{Fields: []model.FieldSpec{
 pdecl(field("owner", model.KindUUID, false), model.None("compared: core/auth/binding.go:10")),
 pdecl(field("scopes", model.KindJSON, false), pdeclStrings("closed set: core/auth/binding.go:20")),
}}
func pdeclStrings(reason string) *model.ColumnDecl {
 return model.Nested([]string(nil), model.ClassEvidence, model.Leaf("[]", model.None(reason)))
}
func coreDescriptors() []model.EntityDescriptor { return []model.EntityDescriptor{citedDescriptor} }
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'cited descriptor')
        return path

    def test_none_citation_line_numbers_are_not_part_of_the_hash(self):
        path = self.cited_descriptor()
        path.write_text(path.read_text().replace('binding.go:10', 'binding.go:12')
                        .replace('binding.go:20', 'other.go:23'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_none_reason_through_an_import_alias_and_a_forwarder_in_another_file_is_blanked(self):
        self.cited_descriptor()
        path = self.write('core/internal/store/sqlstore/aliased.go', '''package sqlstore
import m "github.com/olivaresai/olivares/core/model"
var aliasedDescriptor = m.EntityDescriptor{Fields: []m.FieldSpec{
 pdecl(field("a", m.KindUUID, false), m.None("compared: core/auth/a.go:1")),
 pdecl(field("b", m.KindJSON, false), pdeclStrings("closed set: core/auth/a.go:2")),
}}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'aliased descriptor')
        path.write_text(path.read_text().replace('a.go:1', 'a.go:11').replace('a.go:2', 'a.go:22'))
        result = self.gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_none_reason_that_is_not_a_literal_is_still_part_of_the_hash(self):
        self.cited_descriptor()
        path = self.write('core/internal/store/sqlstore/named.go', '''package sqlstore
import "github.com/olivaresai/olivares/core/model"
const why = "compared: core/auth/a.go:1"
var namedDescriptor = model.EntityDescriptor{Fields: []model.FieldSpec{
 pdecl(field("a", model.KindUUID, false), model.None(why)),
}}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'named reason')
        path.write_text(path.read_text().replace('a.go:1', 'a.go:11'))
        self.assert_rejected('core/descriptor/namedDescriptor')

    def test_function_that_also_uses_its_parameter_is_not_a_reason_forwarder(self):
        self.cited_descriptor()
        path = self.write('core/internal/store/sqlstore/guarded.go', '''package sqlstore
import "github.com/olivaresai/olivares/core/model"
func guarded(reason string) *model.ColumnDecl {
 if reason == "" {
  return nil
 }
 return model.None(reason)
}
var guardedDescriptor = model.EntityDescriptor{Fields: []model.FieldSpec{
 pdecl(field("a", model.KindUUID, false), guarded("compared: core/auth/a.go:1")),
}}
''')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                 'commit', '-qm', 'guarded forwarder')
        path.write_text(path.read_text().replace('a.go:1', 'a.go:11'))
        self.assert_rejected('core/descriptor/guardedDescriptor')

    def test_none_declaration_is_still_part_of_the_hash(self):
        path = self.cited_descriptor()
        path.write_text(path.read_text().replace(
            'model.None("compared: core/auth/binding.go:10")', 'model.Scan(model.ClassEvidence)'))
        self.assert_rejected('core/descriptor/citedDescriptor')

    def test_citation_edit_does_not_hide_a_sibling_change(self):
        path = self.cited_descriptor()
        path.write_text(path.read_text().replace('binding.go:10', 'binding.go:12')
                        .replace('field("owner"', 'field("holder"'))
        self.assert_rejected('core/descriptor/citedDescriptor')

    def test_missing_baseline_refuses(self):
        self.env['OLIVARES_MIGRATION_BASE'] = 'missing-ref'
        result = self.gate()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn('UNVERIFIED', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
