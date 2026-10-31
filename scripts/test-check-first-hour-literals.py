#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Mutants for the first-hour literal gate, on hermetic fixture repositories.

The 0.4 candidate failed the first-hour journey on run 40081585476 (2026-10-24)
because a merged console PR had removed strings the spec still read, and nothing
before merge had looked. These tests hold the cheap stand-in to the behavior
the regression asked for: a string removed from the console turns the check red, the
same rename in the spec in the same change keeps it green, and locators that are
not literals are listed as not covered instead of passing silently.
"""
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

CHECK = Path(__file__).resolve().parent / "check-first-hour-literals.py"
ROOT = Path(__file__).resolve().parents[1]

# Every locator shape the real spec uses, with its own strings; the fixture
# proves the parser on multi-line calls, regex names, ids and dynamic names
# without reading the 525 MB console tree.
SPEC = """// fixture spec: every locator kind the real first-hour spec uses
// await page.getByText('Old label')
/* await page.getByLabel('Other label') */
const name = `First-hour ${tool.name} stub`
await page.getByRole('button', { name: 'Add provider', exact: true }).click()
await page.getByText('Add ' + 'provider')
await page.getByRole('button', { name: 'Install ' + kind, exact: true })
const dialog = page.getByRole('dialog', { name: 'New session', exact: true })
await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(name)
await dialog.getByRole('button', { name: /advanced: custom endpoint/i }).click()
await page.getByRole('combobox').click()
await page.getByRole('radio', { name: tool.kind, exact: true }).click()
await page.getByText('Ready to start a session.', { exact: true })
await page.getByText('Decision recorded — approved')
await page.getByLabel('API key').fill(keys[0])
await page.locator('#token').fill(token)
await page.getByTestId('session-conversation')
await page.getByRole('row').filter({ hasText: `${tool.name}: run this command` })
await page.getByRole('row').filter(rowOptions).click()
await page
  .getByRole('button', {
    name: 'Install approved version',
    exact: true,
  })
  .click()
"""

CONSOLE = {
    "web/src/features/providers/page.tsx":
        "export const add = <button>Add provider</button>\n",
    "web/src/features/sessions/new.tsx":
        "export const dialog = ['New session', <label>Name</label>]\n",
    "web/src/features/sessions/i18n/en.json":
        '{"ready": "Ready to start a session.",'
        ' "decision": "decision recorded — APPROVED"}\n',
    "web/src/features/keys/label.tsx":
        "export const key = <label>API key</label>\n",
    "web/src/features/tools/install.tsx":
        "export const install = 'Install approved version'\n",
}


class Fixture(unittest.TestCase):
    """A committed git repository shaped like the console tree the check reads."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="first-hour-literals-"))
        self.repo = self.tmp / "repo"
        (self.repo / "web/e2e-release").mkdir(parents=True)
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True,
                       capture_output=True)
        self.commit(SPEC, CONSOLE)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def commit(self, spec, console):
        spec_path = self.repo / "web/e2e-release/first-hour.spec.ts"
        if spec is None:
            spec_path.unlink()
        else:
            spec_path.write_text(spec)
        for path, text in console.items():
            target = self.repo / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text)
        self.git("add", "-A")
        self.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid",
                 "commit", "-q", "-m", "fixture", "--allow-empty")

    def env(self):
        # GIT_DIR and friends outrank -C; a hook or a linked worktree that
        # exports them must not point these commits at the live repository.
        return {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True,
                       capture_output=True, env=self.env())

    def check(self, *args):
        return subprocess.run(
            ["python3", str(CHECK), "--repo", str(self.repo), *args],
            capture_output=True, text=True, env=self.env())

    def head(self):
        return subprocess.run(["git", "-C", self.repo, "rev-parse", "HEAD"],
                              check=True, capture_output=True,
                              text=True).stdout.strip()


class GreenWhenConsoleReadsEveryLiteral(Fixture):
    def test_every_literal_found_and_limit_stated(self):
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertIn("7 distinct literal", done.stdout)
        # The limit of the stand-in is stated on every run, green included
        # (the regression: roles and structure are proved only by the journey).
        self.assertIn("proved only by the release-first-hour journey", done.stdout)


class NotCoveredLocatorsAreListed(Fixture):
    def test_regex_role_dynamic_id_and_testid_are_never_silent(self):
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        for needle in ("/advanced: custom endpoint/i (regex)",
                       "getByRole('combobox') (role without a literal name)",
                       "name: tool.kind (dynamic)",
                       "locator('#token') (selector)",
                       "getByTestId('session-conversation') (test id)",
                       "hasText: `${tool.name}: run this command` (dynamic)",
                       "filter(rowOptions) (dynamic filter)",
                       "getByText('Add ' + 'provider') (dynamic)",
                       "name: 'Install ' + kind (dynamic)"):
            self.assertIn(needle, done.stdout)

    def test_commented_out_locators_are_not_read(self):
        # The journey does not run a commented-out line, so it must not require
        # the console to keep that string (the regression: the literals the spec reads).
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertNotIn("Old label", done.stdout)
        self.assertNotIn("Other label", done.stdout)
        self.assertIn("7 distinct literal", done.stdout)

    def test_missing_literal_cannot_forge_a_workflow_command(self):
        # The red path prints the literal too; a newline in it must not reach
        # stdout as a line the runner reads as a command.
        spec = "await page.getByText('gone\n::stop-commands::abc')\n"
        self.commit(spec, CONSOLE)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertNotIn("\n::stop-commands::", done.stdout)
        self.assertIn("::stop-commands::", done.stdout)

    def test_spec_text_cannot_forge_a_workflow_command(self):
        # A locator argument is PR-authored text. A newline in it must not reach
        # stdout as a line the runner reads as a command.
        spec = SPEC + "await page.locator('sel\n::stop-commands::abc')\n"
        self.commit(spec, CONSOLE)
        done = self.check("--head", self.head())
        self.assertNotIn("\n::stop-commands::", done.stdout)
        self.assertIn("::stop-commands::", done.stdout)


class ConsoleRemovalTurnsRed(Fixture):
    def test_removed_literal_fails_and_names_it(self):
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Something else</button>\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)
        self.assertIn("::error file=web/e2e-release/first-hour.spec.ts,line=5::",
                      done.stdout)
        self.assertIn("1 missing", done.stdout)

    def test_exact_literal_is_case_sensitive(self):
        # exact: true is Playwright's case-sensitive full match; renaming the
        # console text to different case must fail even though a plain grep
        # would look similar.
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>add provider</button>\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)

    def test_exact_literal_is_a_whole_value(self):
        # exact: true is the full accessible name; a longer label is a rename,
        # not a substring match that keeps the old string alive.
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Add provider extended</button>\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)

    def test_exact_literal_is_not_a_punctuation_suffix(self):
        # Playwright compares the whole name, so 'Add provider (beta)' is a
        # different name even though a word boundary would accept it.
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Add provider (beta)</button>\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)

    def test_exact_literal_is_not_a_prefix(self):
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>All Add provider</button>\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)


class SpecRenamedInTheSameChangeStaysGreen(Fixture):
    def test_console_and_spec_renamed_together(self):
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Add a provider</button>\n"
        spec = SPEC.replace("name: 'Add provider'", "name: 'Add a provider'")
        self.commit(spec, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertIn("7 distinct literal", done.stdout)


class SpecGoneIsNotAVacuousPass(Fixture):
    def test_missing_spec_fails(self):
        self.commit(None, CONSOLE)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("first-hour.spec.ts", done.stdout)


class SpecWithoutLiteralsIsNotAVacuousPass(Fixture):
    def test_spec_that_parses_no_literal_fails(self):
        # A spec whose locators are all non-literal would otherwise report
        # "0 distinct ... all found" and exit 0.
        spec = ("await page.getByRole('combobox').click()\n"
                "await page.getByTestId('session-conversation')\n")
        self.commit(spec, CONSOLE)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("no literal locators", done.stdout)


class UnreadableInputIsNotAPass(Fixture):
    def test_unknown_revision_fails_loudly(self):
        done = self.check("--head", "0" * 40)
        self.assertEqual(done.returncode, 2, done.stdout + done.stderr)

    def test_no_web_src_at_all_fails(self):
        # A tree where every literal is missing is red, not vacuously green.
        for path in list((self.repo / "web/src").rglob("*")):
            if path.is_file():
                path.unlink()
        self.git("add", "-A")
        self.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid",
                 "commit", "-q", "-m", "empty console")
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("7 missing", done.stdout)


class ConsoleSearchScopeIsHonest(Fixture):
    """Tests and translations quote UI copy but render none of it."""

    def test_literal_only_in_tests_does_not_count(self):
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Something else</button>\n"
        console["web/src/features/providers/page.test.tsx"] = \
            "export const copy = 'Add provider'\n"
        console["web/src/test/helper.ts"] = "export const copy = 'Add provider'\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)

    def test_literal_only_in_translations_does_not_count(self):
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Something else</button>\n"
        console["web/src/features/providers/i18n/ja.json"] = \
            '{"add": "Add provider"}\n'
        console["web/src/lib/i18n/locales/ja/common.json"] = \
            '{"add": "Add provider"}\n'
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)

    def test_literal_only_in_fixtures_or_dev_demos_does_not_count(self):
        # Fixtures and the dev components page quote copy but are not the
        # product surface the journey drives.
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Something else</button>\n"
        console["web/src/features/providers/fixtures/seed.json"] = \
            '{"add": "Add provider"}\n'
        console["web/src/dev/components-page/demo.tsx"] = \
            "export const copy = 'Add provider'\n"
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertIn("Add provider", done.stdout)

    def test_shared_english_catalog_counts(self):
        console = dict(CONSOLE)
        console["web/src/features/providers/page.tsx"] = \
            "export const add = <button>Something else</button>\n"
        console["web/src/lib/i18n/locales/en/common.json"] = \
            '{"add": "Add provider"}\n'
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertIn("7 distinct literal", done.stdout)


class CaseSensitivityFollowsExact(Fixture):
    def test_non_exact_literal_ignores_case(self):
        console = dict(CONSOLE)
        console["web/src/features/sessions/i18n/en.json"] = (
            '{"ready": "Ready to start a session.",'
            ' "decision": "DECISION RECORDED — APPROVED"}\n')
        self.commit(SPEC, console)
        done = self.check("--head", self.head())
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)


class RealSpecShapesAreParsed(unittest.TestCase):
    """The parser reads the real spec's shapes, not only the fixture's.

    The gate step owns the verdict on the tree; this test owns parser coverage.
    If a spec rewrite adds a shape the parser skips, the literal count or the
    not-covered kinds change here before the gate could pass silently.
    """

    def test_real_spec_literals_and_non_literals(self):
        done = subprocess.run(
            ["python3", str(CHECK), "--repo", str(ROOT), "--head", "HEAD"],
            capture_output=True, text=True)
        match = re.search(r"(\d+) distinct literal", done.stdout)
        self.assertIsNotNone(match, done.stdout + done.stderr)
        self.assertGreaterEqual(int(match.group(1)), 20, done.stdout)
        for needle in ("(regex)", "(role without a literal name)",
                       "(dynamic)", "(selector)", "(test id)",
                       "proved only by the release-first-hour journey"):
            self.assertIn(needle, done.stdout)


if __name__ == "__main__":
    unittest.main()
