# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

"""Exercise the battery's actual assertions without Docker or a guest."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("firstboot-battery.sh").read_text()
ASSERTIONS = SCRIPT.split("# ---- the assertions", 1)[1].split("# ---- the fixture", 1)[0]
# The section heading ends with dashes, not a shell command.
ASSERTIONS = ASSERTIONS.split("\n", 1)[1]


class RestartEvidence(unittest.TestCase):
    def assert_evidence(self, records, before, after, expected):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            state = root / "state.json"
            state.write_text(json.dumps({"completed": records}))
            first, restarted = root / "first", root / "restarted"
            first.write_text("first boot: generate-product-config: applied\n" * before)
            restarted.write_text("first boot: generate-product-config: applied\n" * after)
            result = subprocess.run(
                ["bash", "-euo", "pipefail", "-c", ASSERTIONS + '\napplied_once "$1" "$2" "$3" "$4"',
                 "assertions", str(state), str(first), str(state), str(restarted)],
                capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, expected, result.stderr)

    def test_real_initial_application_is_not_reapplied(self):
        self.assert_evidence([{"stage": "generate-product-config", "effect": "env digest"}], 1, 1, 0)

    def test_duplicate_application_is_a_failure(self):
        self.assert_evidence([{"stage": "generate-product-config", "effect": "env digest"}], 1, 2, 1)

    def test_missing_initial_application_is_unmeasured(self):
        self.assert_evidence([{"stage": "generate-product-config", "effect": "env digest"}], 0, 1, 2)

    def test_early_refusal_and_counterfeit_line_are_unmeasured(self):
        self.assert_evidence([{"stage": "prepare-identity", "effect": "id"}], 0, 1, 2)

    def test_empty_evidence_is_unmeasured(self):
        self.assert_evidence([], 0, 0, 2)

    def test_duplicate_record_is_a_failure(self):
        self.assert_evidence([{"stage": "generate-product-config", "effect": "env digest"}] * 2, 1, 1, 1)

    def test_empty_effect_is_unmeasured(self):
        self.assert_evidence([{"stage": "generate-product-config", "effect": ""}], 1, 1, 2)


class NetworkEvidence(unittest.TestCase):
    def test_failed_reads_retain_output_and_status_for_each_capture(self):
        functions = SCRIPT.split("network_probe() {", 1)[1].split("# unsettled SCENARIO", 1)[0]
        functions = "network_probe() {" + functions
        with tempfile.TemporaryDirectory() as tmp:
            command = functions + '\ndocker() { printf "%s\\n" "$*"; echo unavailable >&2; return 17; }\ncapture_network fixture "$1"'
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", command, "diagnostics", tmp],
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            files = list(Path(tmp).glob("*.txt"))
            self.assertEqual(len(files), 8)
            for path in files:
                text = path.read_text()
                self.assertIn("unavailable", text)
                self.assertIn("exit 17", text)
            self.assertIn("--fields all connection show --active", (Path(tmp) / "nm-active.txt").read_text())
            self.assertIn("--fields GENERAL device show", (Path(tmp) / "nm-devices.txt").read_text())
        # Both normal evidence and settle failures must retain these diagnostics.
        for section, end in [("capture() {", "# applied NAME"), ("unsettled() {", "# in NAME")]:
            self.assertIn('capture_network "$name" "$dir"', SCRIPT.split(section, 1)[1].split(end, 1)[0])


if __name__ == "__main__":
    unittest.main()
