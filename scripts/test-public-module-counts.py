#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Public module counts distinguish selectable namespaces from packages."""
import ast
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parent.parent
BODY = (ROOT / "scripts/check-public-counts.sh").read_text().split(
    "python3 - <<'PY'\n", 1
)[1].split("\nPY\n", 1)[0]
SPANISH_SELECTABLE = "{n} módulos seleccionables"  # language-data: selectable-count wording
FRENCH_SELECTABLE = "{n} modules sélectionnables"  # language-data: selectable-count wording
SPANISH_MODULES = "{n} módulos"  # language-data: package-count wording
SELECTABLE = {
    "en": "{n} selectable modules",
    "de": "{n} auswählbare Module · {n} auswählbaren Module · {n} auswählbaren Modulen",
    "es": SPANISH_SELECTABLE,
    "fr": FRENCH_SELECTABLE,
    "ja": "{n} 個の選択可能なモジュール",
    "ru": "{n} выбираемых модулей",
    "zh": "{n} 个可选模块",
}


class PublicModuleCountsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.original_cwd = Path.cwd()
        cls.original_selftest = os.environ.get("CPC_SELFTEST")
        cls.addClassCleanup(cls.restore_process_state)
        os.chdir(ROOT)
        os.environ["CPC_SELFTEST"] = "0"
        cls.gate = {}
        # The real Python gate and its real census inputs; unrelated Go
        # metadata delegates are outside this count-specific test seam.
        exec(compile(BODY.split("# ── selftest:", 1)[0],
                     "check-public-counts.sh", "exec"), cls.gate)

    @classmethod
    def restore_process_state(cls) -> None:
        os.chdir(cls.original_cwd)
        if cls.original_selftest is None:
            os.environ.pop("CPC_SELFTEST", None)
        else:
            os.environ["CPC_SELFTEST"] = cls.original_selftest

    def findings(self, text: str) -> list[str]:
        self.gate["failures"].clear()
        self.gate["check_units"]("docs-site", "overview.md", text)
        return list(self.gate["failures"])

    def test_selectable_claims_use_namespace_count_in_every_locale(self) -> None:
        self.assertGreater(self.gate["SELECTABLE_MODULES"], self.gate["MODULES"])
        for locale, wording in SELECTABLE.items():
            with self.subTest(locale=locale):
                self.assertEqual(self.findings(wording.format(
                    n=self.gate["SELECTABLE_MODULES"])), [])

    def test_stale_selectable_claims_fail_in_every_locale(self) -> None:
        for locale, wording in SELECTABLE.items():
            with self.subTest(locale=locale):
                findings = self.findings(wording.format(
                    n=self.gate["SELECTABLE_MODULES"] - 1))
                self.assertEqual(len(findings), wording.count("{n}"), findings)
                self.assertTrue(all("selectable_modules" in finding for finding in findings),
                                findings)

    def test_package_count_remains_independent(self) -> None:
        for wording in ("{n} modules", SPANISH_MODULES, "{n} Module",
                        "{n} モジュール", "{n} модулей", "{n} 个模块"):
            with self.subTest(wording=wording):
                self.assertEqual(self.findings(wording.format(
                    n=self.gate["MODULES"])), [])
                self.assertTrue(self.findings(wording.format(
                    n=self.gate["SELECTABLE_MODULES"])))

    def count_body_findings(self, path: str = "", text: str = "") -> list[str]:
        def fixture_open(filename: str, *args, **kwargs) -> io.IOBase:
            # Replace only the selected page's input, never a gate verdict.
            if filename == path:
                mode = args[0] if args else kwargs.get("mode", "r")
                return io.BytesIO(text.encode()) if "b" in mode else io.StringIO(text)
            return open(filename, *args, **kwargs)

        gate = {"open": fixture_open}
        with contextlib.redirect_stdout(io.StringIO()):
            try:
                exec(compile(BODY, "check-public-counts.sh", "exec"), gate)
            except SystemExit as error:
                self.assertEqual(error.code, 1)
        return gate["failures"]

    def test_live_overviews_have_no_numeric_or_table_findings(self) -> None:
        overview_findings = [finding for finding in self.count_body_findings()
                             if "reference/modules/overview.md" in finding]
        self.assertEqual(overview_findings, [])

    def test_trust_architecture_can_omit_count_but_cannot_state_a_stale_count(self) -> None:
        path = "docs/trust/reference-architecture.md"
        modules = self.gate["MODULES"]
        for text, refused in (("core + modules", False),
                              (f"core + {modules} modules", False),
                              (f"core + {modules - 1} modules", True),
                              ("core only", True)):
            with self.subTest(text=text):
                findings = [finding for finding in self.count_body_findings(path, text)
                            if "reference-architecture.md" in finding]
                self.assertEqual(bool(findings), refused, findings)

    def test_missing_selectable_row_fails_in_every_translation(self) -> None:
        for locale in sorted(SELECTABLE.keys() - {"en"}):
            with self.subTest(locale=locale):
                path = f"docs-site/src/content/docs/{locale}/reference/modules/overview.md"
                lines = (ROOT / path).read_text().splitlines()
                row = next(line for line in lines if line.startswith("| ["))
                lines.remove(row)
                findings = self.count_body_findings(path, "\n".join(lines))
                expected = self.gate["SELECTABLE_MODULES"]
                table_findings = [finding for finding in findings
                                  if f"{path} lists" in finding]
                self.assertEqual(len(table_findings), 1, table_findings)
                self.assertIn(f"lists {expected - 1} module rows", table_findings[0])
                self.assertIn(f"(expected: {expected})", table_findings[0])

    def test_existing_count_selftests_pass(self) -> None:
        result = subprocess.run([sys.executable, "-c", BODY], cwd=ROOT,
                                env={**os.environ, "CPC_SELFTEST": "1"},
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("selftest OK", result.stdout)

    @unittest.skipIf((ROOT / ".olivares-public-export").is_file(),
                     "retired reel is excluded from the curated public export")
    def test_retired_reel_uses_its_frozen_census_without_video_findings(self) -> None:
        self.assertEqual([finding for finding in self.count_body_findings()
                          if finding.startswith("video:")], [])

    @unittest.skipIf((ROOT / ".olivares-public-export").is_file(),
                     "retired reel is excluded from the curated public export")
    def test_removing_retirement_policy_restores_live_count_checks(self) -> None:
        path = "design/launch-video/README.md"
        text = (ROOT / path).read_text().replace(
            "<!-- counts-gate: retired-launch-reel-v1 -->", "")
        findings = self.count_body_findings(path, text)
        self.assertTrue(any("video: reel.html scene 13 shows" in finding
                            for finding in findings), findings)

    @unittest.skipIf((ROOT / ".olivares-public-export").is_file(),
                     "retired reel is excluded from the curated public export")
    def test_unknown_or_mixed_retirement_versions_are_rejected(self) -> None:
        path = "design/launch-video/README.md"
        text = (ROOT / path).read_text()
        marker = "<!-- counts-gate: retired-launch-reel-v1 -->"
        unknown = marker.replace("v1", "v2")
        for policy in (unknown, marker + "\n" + unknown):
            with self.subTest(policy=policy):
                findings = self.count_body_findings(path, text.replace(marker, policy))
                self.assertTrue(any("retirement exemption requires policy v1" in finding
                                    for finding in findings), findings)

    @unittest.skipIf((ROOT / ".olivares-public-export").is_file(),
                     "retired reel is excluded from the curated public export")
    def test_retirement_policy_requires_the_original_publication_ban(self) -> None:
        path = "design/launch-video/README.md"
        text = (ROOT / path).read_text()
        banner = "> # ⛔ EL REEL DEL 28-08 NO SE PUBLICA · RETIRADO EL 2026-08-29"
        self.assertIn(banner, text.splitlines())
        findings = self.count_body_findings(path, text.replace(banner, ""))
        self.assertTrue(any("retirement exemption requires policy v1" in finding
                            for finding in findings), findings)

    @unittest.skipIf((ROOT / ".olivares-public-export").is_file(),
                     "retired reel is excluded from the curated public export")
    def test_retirement_preserves_real_manifest_input_and_output_hash_checks(self) -> None:
        for path, diagnosis in (
            ("design/launch-video/reel.html", "changed since the last render"),
            ("design/launch-video/out/olivares-launch-reel.en.srt",
             "does not match its recorded hash"),
        ):
            with self.subTest(path=path):
                text = (ROOT / path).read_text() + "\n"
                findings = self.count_body_findings(path, text)
                self.assertTrue(any("video: manifest[" in finding and path in finding
                                    and diagnosis in finding for finding in findings), findings)


class MovedLocaleDataTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.addClassCleanup(os.chdir, Path.cwd())
        os.chdir(ROOT)

    @staticmethod
    def reader_source(body: str) -> str:
        # Execute the production readers without the unrelated enforcement census.
        # Missing or changed source anchors fail extraction; no verdict is replaced.
        tree = ast.parse(body)
        functions = {node.name: ast.get_source_segment(body, node)
                     for node in tree.body if isinstance(node, ast.FunctionDef)}
        locale_rosters = [node for node in tree.body if isinstance(node, ast.Assign)
                          and any(isinstance(target, ast.Name) and
                                  target.id == 'SIDEBAR_LOCALES' for target in node.targets)]
        if len(locale_rosters) != 1:
            raise AssertionError('the real sidebar locale roster must be unique')
        return '\n'.join([
            functions['rd'], functions['blind'],
            body[body.index('def locale_data(path):'):body.index('JOIN =')],
            ast.get_source_segment(body, locale_rosters[0]),
            body[body.index('sidebar = {}'):body.index('check_sidebar_block("docs-site", json.dumps(sidebar')],
        ])

    SIDEBAR_PROBE = """
import assert from 'node:assert/strict';
const { SIDEBAR_LABELS, localizeSidebar } = await import(process.argv[1]);
const languages = ['de', 'es', 'fr', 'ja', 'ru', 'zh-CN'];
const labels = Object.keys(SIDEBAR_LABELS);
assert.ok(labels.length > 0, 'the real sidebar dictionary must be populated');
for (const label of labels) {
  assert.deepEqual(Object.keys(SIDEBAR_LABELS[label]).sort(), languages, label);
  assert.ok(SIDEBAR_LABELS[label]['zh-CN'], 'Chinese uses the BCP-47 language key');
}
const [first] = labels;
const last = labels.at(-1);
const plugin = { autogenerate: { directory: 'reference' } };
const items = [{ label: first, items: [{ label: last }] }, { label: 'Unknown' }, plugin];
const rendered = localizeSidebar(items);
assert.deepEqual(rendered, [
  { label: first, translations: SIDEBAR_LABELS[first],
    items: [{ label: last, translations: SIDEBAR_LABELS[last] }] },
  { label: 'Unknown' }, plugin,
]);
assert.equal(rendered[2], plugin);
"""

    def test_real_sidebar_preserves_language_keys_and_recursive_output(self) -> None:
        result = subprocess.run(
            ['node', '--input-type=module', '-e', self.SIDEBAR_PROBE,
             (ROOT / 'docs-site/src/sidebar-i18n.mjs').as_uri()],
            cwd=ROOT, capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_moved_locale_data_failures_are_cannot_look(self) -> None:
        vocabulary = ['scripts/locales/es/public-counts.json',
                      'scripts/locales/fr/public-counts.json']
        sidebars = ['docs-site/src/locales/zh.json']
        for path in vocabulary + sidebars:
            shapes = [None, '{malformed', '[]']
            shapes.append('{}' if path in vocabulary else '{"Overview": []}')
            for payload in shapes:
                with self.subTest(path=path, payload=payload):
                    def fixture_open(filename: str, *args, **kwargs) -> io.IOBase:
                        # Replace one real data input, never the comparison verdict.
                        if filename == path:
                            if payload is None:
                                raise FileNotFoundError(path)
                            return io.StringIO(payload)
                        return open(filename, *args, **kwargs)

                    output = io.StringIO()
                    with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                        with self.assertRaises(SystemExit) as error:
                            exec(compile(self.reader_source(BODY), 'check-public-counts.sh', 'exec'),
                                 {'open': fixture_open, 'json': json, 'sys': sys})
                    self.assertEqual(error.exception.code, 2, output.getvalue())
                    self.assertIn(path, output.getvalue())


if __name__ == "__main__":
    unittest.main()
