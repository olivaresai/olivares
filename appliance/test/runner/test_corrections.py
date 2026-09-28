# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Discriminating source controls, never hosted-runner qualification."""
import copy
import importlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

import accelerator_probe
import fixtures
import judge


class TypedEvidence(unittest.TestCase):
    def test_missing_capacity_is_not_measurement(self):
        for key in ("processors", "memory_total_bytes", "memory_available_bytes", "evidence_avail_bytes"):
            with self.subTest(key=key):
                receipt = fixtures.receipt()
                receipt["capacity"][key] = None
                self.assertEqual(judge.judge(fixtures.lock(), receipt).state, "defect")

    def test_proof_flags_are_booleans(self):
        for name in judge.MANDATORY_OBSERVATIONS:
            for key in ("performed", "proved", "timed_out", "cleaned"):
                with self.subTest(name=name, key=key):
                    receipt = fixtures.receipt()
                    receipt["observations"][name][key] = "false"
                    self.assertEqual(judge.judge(fixtures.lock(), receipt).state, "defect")

    def test_running_is_required_false_boolean(self):
        for value in (None, 0, "false"):
            receipt = fixtures.receipt()
            receipt["observations"]["accelerator"]["qmp_running"] = value
            self.assertEqual(judge.judge(fixtures.lock(), receipt).state, "defect")

    def test_invalid_lock_limits_refuse_before_effects(self):
        for key, value in (("qemu_timeout_seconds", float("inf")), ("container_timeout_seconds", -1), ("loop_image_bytes", 2**50)):
            lock = fixtures.lock()
            lock["probe"][key] = value
            self.assertTrue(judge.validate_lock(lock))

    def test_current_context_is_not_historical_rejudgement(self):
        receipt = fixtures.receipt()
        context = {"attempt_id": "a-different-current-attempt"}
        self.assertEqual(judge.judge(fixtures.lock(), receipt, expected_context=context).state, "defect")


class MonitorEvidence(unittest.TestCase):
    def run_monitor(self, reply, capabilities='{"return":{}}'):
        program = "import sys,json; print(json.dumps({'QMP':{}}),flush=True); sys.stdin.readline(); print(" + repr(capabilities) + ",flush=True); sys.stdin.readline(); print(" + repr(json.dumps({"return":reply})) + ",flush=True); sys.stdin.readline()"
        return accelerator_probe.probe([sys.executable, "-c", program], "tcg", "q35", "prelaunch", 0.4)

    def test_missing_running_is_not_a_proof(self):
        self.assertFalse(self.run_monitor({"status":"prelaunch"})["proved"])

    def test_capabilities_refusal_is_not_a_proof(self):
        self.assertFalse(self.run_monitor({"status":"prelaunch","running":False}, '{"error":{"class":"GenericError"}}')["proved"])

    def test_valid_monitor_is_reaped(self):
        result = self.run_monitor({"status":"prelaunch","running":False})
        self.assertTrue(result["proved"])
        self.assertTrue(result["cleaned"])

    def test_monitor_output_is_bounded(self):
        program = "import sys,time; print('x'*131072,flush=True); time.sleep(5)"
        started = time.monotonic()
        result = accelerator_probe.probe([sys.executable,"-c",program], "tcg", "q35", "prelaunch", 0.1)
        self.assertFalse(result["proved"])
        self.assertTrue(result["cleaned"])
        self.assertLess(time.monotonic()-started, 3)
        self.assertLessEqual(len(result["detail"]), accelerator_probe.DETAIL_LIMIT)


if __name__ == "__main__":
    unittest.main()
