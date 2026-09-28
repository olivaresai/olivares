# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise CI lookup through its CLI without a YAML package or a runner."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


PEEK = Path(os.environ.get("OLIVARES_CI_PEEK_TEST_SUBJECT", Path(__file__).parent / "lib/ci-yaml-peek.py"))


def job(name, value="success()", ident="provider"):
    return (f"  {name}:\n    runs-on: ubuntu-latest\n    steps:\n"
            f"      - name: prepare tools\n        id: {ident}\n        if: {value}\n"
            "        run: echo ready\n")


class LookupTests(unittest.TestCase):
    def query(self, text, command, *args):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "subject.yml"
            path.write_text(text, encoding="utf-8")
            result = subprocess.run([sys.executable, str(PEEK), command, str(path), *args],
                                    capture_output=True, text=True, timeout=5)
            self.assertNotIn("Traceback", result.stderr)
            return result.returncode, result.stdout

    def test_job_relocation_keeps_value(self):
        self.assertEqual(self.query("jobs:\n" + job("build"), "step-field-anyjob", "provider", "if"),
                         (0, "success()"))

    def test_equal_values_in_distinct_jobs_are_unambiguous(self):
        self.assertEqual(self.query("jobs:\n" + job("build") + job("docs"),
                                    "step-field-anyjob", "provider", "if"), (0, "success()"))

    def test_different_values_are_ambiguous(self):
        self.assertEqual(self.query("jobs:\n" + job("build") + job("docs", "always()"),
                                    "step-field-anyjob", "provider", "if"), (3, ""))

    def test_job_inventory_preserves_both_owners(self):
        self.assertEqual(self.query("jobs:\n" + job("build") + job("docs"),
                                    "step-jobs", "provider"), (0, "build\ndocs\n"))

    def test_provider_value_is_scoped_to_its_consumer_jobs(self):
        text = ("jobs:\n" + job("build") + "      - id: consumer\n        run: echo consume\n"
                + job("other", "always()"))
        self.assertEqual(self.query(text, "step-field-for-consumer", "provider", "consumer", "if"),
                         (0, "success()"))

    def test_another_job_cannot_supply_the_consumers_provider(self):
        text = "jobs:\n" + job("build", ident="consumer") + job("other")
        self.assertEqual(self.query(text, "step-field-for-consumer", "provider", "consumer", "if"), (3, ""))

    def test_name_lookup_works_after_relocation(self):
        self.assertEqual(self.query("jobs:\n" + job("build"), "step-if-byname-anyjob", "prepare tools"),
                         (0, "success()"))

    def test_missing_step_is_distinct_from_malformed_input(self):
        self.assertEqual(self.query("jobs:\n" + job("build"), "step-jobs", "absent"), (3, ""))

    def test_reusable_workflow_job_has_no_local_steps(self):
        # export-closure: fixture .github/workflows/reuse.yml — an isolated parser input, never executed.
        text = "jobs:\n  reuse:\n    uses: ./.github/workflows/reuse.yml\n" + job("build")
        self.assertEqual(self.query(text, "step-jobs", "provider"), (0, "build\n"))

    def test_duplicate_job_is_not_silently_skipped(self):
        text = "jobs:\n" + job("build") + job("build") + job("docs")
        self.assertEqual(self.query(text, "step-jobs", "provider"), (2, ""))

    def test_duplicate_steps_region_is_not_silently_skipped(self):
        text = "jobs:\n" + job("build") + "    steps:\n      - run: echo duplicate\n" + job("docs")
        self.assertEqual(self.query(text, "step-jobs", "provider"), (2, ""))

    def test_duplicate_id_within_one_job_is_malformed(self):
        text = "jobs:\n" + job("build") + "      - id: provider\n        run: echo duplicate\n"
        self.assertEqual(self.query(text, "step-jobs", "provider"), (2, ""))

    def test_duplicate_requested_field_is_malformed(self):
        text = "jobs:\n" + job("build") + "        if: always()\n"
        self.assertEqual(self.query(text, "step-field-anyjob", "provider", "if"), (2, ""))

    def test_unsupported_steps_shape_is_not_absence(self):
        text = "jobs:\n  bad:\n    steps: []\n" + job("docs")
        self.assertEqual(self.query(text, "step-jobs", "provider"), (2, ""))

    def test_unsupported_job_shape_is_not_skipped(self):
        text = 'jobs:\n  "quoted":\n    steps:\n      - id: provider\n' + job("docs")
        self.assertEqual(self.query(text, "step-jobs", "provider"), (2, ""))

    def test_tabs_in_indentation_are_refused(self):
        text = "jobs:\n" + job("build").replace("    steps:", "\tsteps:")
        self.assertEqual(self.query(text, "step-jobs", "provider"), (2, ""))

    def test_named_job_command_is_preserved(self):
        self.assertEqual(self.query("jobs:\n" + job("build"), "step-field", "build", "provider", "if"),
                         (0, "success()"))

    def test_task_command_is_preserved(self):
        text = "tasks:\n  generate:\n    cmds:\n      - cmd: |\n          echo ready\n"
        self.assertEqual(self.query(text, "task-cmd", "generate", "ready"), (0, "echo ready\n"))


if __name__ == "__main__":
    unittest.main()
