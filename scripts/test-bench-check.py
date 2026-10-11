#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the smoke script with actual Go benchmark discovery and execution."""

from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
FAMILIES = {
    "core/bench": ["AuditAppend", "WriteScaling"],
    "modules/finops": ["ReserveBudget", "CheckBudget"],
    "modules/inferenceproxy": ["ProxyPolicyDLPDecide", "ProxyDLPDecideLatency"],
    "cmd/olivares": ["HookDecideEndToEnd", "ProxyAuthorizeEndToEnd"],
    "modules/knowledge": ["RetrievalEndToEnd", "CosineIndexRankCurve"],
}


class BenchmarkEditionTest(unittest.TestCase):
    def test_community_keeps_read_loop_without_business_export(self):
        result = subprocess.run(['go', 'test', './core/bench', '-run', '^$', '-list',
                                 '^Benchmark(ExportCost|ReadLoopCost)$'], cwd=ROOT,
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                timeout=120, check=True)
        names = result.stdout.splitlines()
        self.assertIn('BenchmarkReadLoopCost', names)
        self.assertNotIn('BenchmarkExportCost', names)


class BenchSmokeTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="bench-smoke-")
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        (self.root / "go.mod").write_text("module smoke.test\n\ngo 1.26.8\n")
        (self.root / "scripts").mkdir()
        shutil.copyfile(ROOT / "scripts/bench-check.sh", self.root / "scripts/bench-check.sh")
        for package in FAMILIES:
            (self.root / package).mkdir(parents=True)
            self.write_benchmarks(package)

    def write_benchmarks(self, package, *, renamed=None, skipped=None, panic=None):
        source = ['package smoke\nimport "testing"\n']
        for name in FAMILIES[package]:
            body = "for range b.N {}"
            if name == skipped:
                body = 'b.Skip("missing benchmark execution")'
            elif name == panic:
                body = 'panic("broken benchmark setup")'
            if name == "CosineIndexRankCurve":
                body = 'b.Run("candidates=10000", func(b *testing.B) {' + body + '})'
            actual_name = "Unselected" if name == renamed else name
            source.append(f"func Benchmark{actual_name}(b *testing.B) {{ {body} }}\n")
        (self.root / package / "smoke_test.go").write_text("".join(source))

    def run_smoke(self, full=True, cpu=None):
        env = {**os.environ, "GOWORK": "off", "BENCHTIME": "1x"}
        if cpu is not None:
            env["GOMAXPROCS"] = cpu
        return subprocess.run(
            ["bash", "scripts/bench-check.sh", *(["--full"] if full else [])],
            cwd=self.root, env=env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=90,
        )

    def test_full_and_fast_execute_all_selected_families(self):
        for full in (False, True):
            with self.subTest(full=full):
                result = self.run_smoke(full)
                self.assertEqual(result.returncode, 0, result.stdout)
                for package, names in FAMILIES.items():
                    for name in names:
                        if not full and name == "RetrievalEndToEnd":
                            self.assertNotIn("Benchmark" + name, result.stdout)
                        else:
                            self.assertIn("Benchmark" + name, result.stdout)
                self.assertIn("== bench smoke OK", result.stdout)

    def test_single_cpu_rows_without_suffix_are_completed(self):
        result = self.run_smoke(full=False, cpu="1")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, r"BenchmarkAuditAppend\s+1\s")

    def test_renamed_family_fails_instead_of_matching_zero(self):
        for package, name in (
            ("modules/finops", "ReserveBudget"),
            ("cmd/olivares", "HookDecideEndToEnd"),
            ("modules/knowledge", "RetrievalEndToEnd"),
        ):
            with self.subTest(package=package, name=name):
                # Rename every member of the Budget family, or the single named family.
                original = (self.root / package / "smoke_test.go").read_text()
                if package == "modules/finops":
                    (self.root / package / "smoke_test.go").write_text(original.replace("Budget", "Limit"))
                else:
                    self.write_benchmarks(package, renamed=name)
                result = self.run_smoke()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn("== bench smoke OK", result.stdout)
                (self.root / package / "smoke_test.go").write_text(original)

    def test_skipped_benchmark_fails_even_when_its_sibling_runs(self):
        self.write_benchmarks("modules/finops", skipped="ReserveBudget")
        result = self.run_smoke()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("== bench smoke OK", result.stdout)

    def test_benchmark_panic_propagates(self):
        self.write_benchmarks("modules/inferenceproxy", panic="ProxyPolicyDLPDecide")
        result = self.run_smoke()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("broken benchmark setup", result.stdout)
        self.assertNotIn("== bench smoke OK", result.stdout)


if __name__ == "__main__":
    unittest.main()
