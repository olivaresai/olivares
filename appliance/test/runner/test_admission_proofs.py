# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the real admission boundary with no runtime or process calls."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import admit
import collector
import fixtures
import judge
from owned_resources import LABEL, Result


IMAGE = "sha256:" + "e" * 64
BINDING = {"source_commit": "c" * 40, "build_input_digest": "d" * 64,
           "run_id": "11", "run_attempt": "2", "job": "image"}
ENVIRONMENT = {"GITHUB_RUN_ID": "11", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_JOB": "image"}
MISSING = object()


def trial(accelerator="kvm", proved=True):
    return {"performed": True, "proved": proved, "timed_out": not proved,
            "cleaned": True, "selected": accelerator, "requested": [accelerator],
            "qmp_status": "prelaunch" if proved else None,
            "qmp_running": False if proved else None, "elapsed_ms": 10}


def receipt(trials=None):
    value = fixtures.receipt()
    value["binding"] = dict(BINDING)
    value["capacity"]["after_toolchain"] = True
    value["observations"]["container_toolchain"].update(
        image_id=IMAGE, image_retained=True, image_removed=False)
    trials = [trial()] if trials is None else trials
    value["observations"]["accelerator"] = dict(trials[-1], attempts=copy.deepcopy(trials),
                                                requested=["kvm", "tcg"])
    return value


def expected(accelerator="kvm"):
    return dict(BINDING, attempt_id="fixture-attempt-0001", image_id=IMAGE,
                lock_digest=fixtures.FIXTURE_LOCK_DIGEST, architecture="x86_64",
                accelerator=accelerator)


class AdmissionProofs(unittest.TestCase):
    def invoke(self, value=MISSING, lock=MISSING, accelerator="kvm", release=False):
        value = receipt() if value is MISSING else copy.deepcopy(value)
        lock = fixtures.lock() if lock is MISSING else copy.deepcopy(lock)
        calls = []

        def command(argv, *args, **kwargs):
            calls.append(argv)
            if argv[1:3] == ["image", "inspect"]:
                return Result(0, json.dumps([{"Id": IMAGE, "Architecture": "amd64",
                    "Config": {"Labels": {LABEL: "fixture-attempt-0001"}}}]))
            if argv[1:3] == ["container", "ls"]:
                return Result(0, "")
            raise AssertionError("unexpected runtime adapter: " + repr(argv))

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            lock_path = root / "input-lock.json"
            lock_path.write_text(json.dumps(lock))
            if isinstance(value, dict) and isinstance(value.get("attempt"), dict):
                value["attempt"]["lock_digest"] = judge.sha256_file(lock_path)
            receipt_path = root / "receipt.json"
            receipt_path.write_text(json.dumps(value))
            args = ["--lock", str(lock_path), "--receipt", str(receipt_path),
                    "--attempt", "fixture-attempt-0001", "--image-id", IMAGE]
            if accelerator is not None:
                args += ["--accelerator", accelerator]
            if release:
                args.append("--release")
            with patch.dict(os.environ, ENVIRONMENT), \
                    patch.object(collector, "current_binding", return_value=dict(BINDING)) as current, \
                    patch.object(admit.platform, "machine", return_value="x86_64"), \
                    patch.object(admit, "run", side_effect=command), \
                    patch.object(admit.os, "statvfs", return_value=SimpleNamespace(f_bavail=60000000000, f_frsize=1)) as capacity, \
                    patch.object(admit, "release_image", return_value=True) as release_image, \
                    contextlib.redirect_stdout(io.StringIO()):
                exit_code = admit.main(args)
            output = root / ("builder-release.json" if release else "admission.json")
            outcome = json.loads(output.read_text())
        return exit_code, outcome, calls, current.call_count, capacity.call_count, release_image.call_count

    def assert_refused_before_runtime(self, result, reason):
        code, outcome, commands, current, capacity, release = result
        self.assertEqual(code, 1)
        self.assertEqual(outcome["state"], "defect")
        self.assertIn(reason, ",".join(outcome["reasons"]))
        self.assertEqual((commands, current, capacity, release), ([], 0, 0, 0))

    def test_admit_positive_kvm_returns_exact_consumer_values(self):
        code, outcome, commands, current, capacity, release = self.invoke()
        self.assertEqual(code, 0)
        self.assertEqual(outcome["state"], "admitted")
        self.assertEqual(outcome["accelerator"], "kvm")
        self.assertEqual(outcome["image_id"], IMAGE)
        self.assertEqual((len(commands), current, capacity, release), (2, 1, 1, 0))

    def test_admit_cleaned_failed_kvm_then_proved_tcg(self):
        value = receipt([trial(proved=False), trial("tcg")])
        code, outcome, *_ = self.invoke(value, accelerator="tcg")
        self.assertEqual(code, 0)
        self.assertEqual(outcome["accelerator"], "tcg")

    def test_admit_single_tcg_with_tcg_only_lock(self):
        lock = fixtures.lock()
        lock["probe"]["accelerator_preference"] = ["tcg"]
        value = receipt([trial("tcg")])
        value["observations"]["accelerator"]["requested"] = ["tcg"]
        self.assertEqual(self.invoke(value, lock, "tcg")[0], 0)

    def test_admit_release_does_not_require_consumer_choice_or_live_source(self):
        code, outcome, commands, current, capacity, release = self.invoke(accelerator=None, release=True)
        self.assertEqual(code, 0)
        self.assertEqual(outcome["state"], "released")
        self.assertEqual((commands, current, capacity, release), ([], 0, 0, 1))

    def test_admit_consumer_choice_is_required(self):
        self.assert_refused_before_runtime(self.invoke(accelerator=None), "admission.accelerator_required")

    def test_admit_consumer_choice_must_match_proof(self):
        self.assert_refused_before_runtime(self.invoke(accelerator="tcg"), "admission.mismatch:accelerator")

    def test_admit_selected_summary_cannot_substitute_for_trial(self):
        value = receipt()
        value["observations"]["accelerator"]["selected"] = "tcg"
        self.assert_refused_before_runtime(self.invoke(value, accelerator="tcg"), "accelerator.summary_mismatch:selected")

    def test_admit_every_terminal_summary_proof_matches_trial(self):
        mutations = {"proved": False, "performed": False, "timed_out": True,
                     "cleaned": False, "qmp_status": "running", "qmp_running": True,
                     "elapsed_ms": 11}
        for field, changed in mutations.items():
            with self.subTest(field=field):
                value = receipt()
                value["observations"]["accelerator"]["attempts"][-1][field] = changed
                self.assert_refused_before_runtime(self.invoke(value), "accelerator.")

    def test_admit_history_is_ordered_and_stops_after_success_or_uncertain_cleanup(self):
        bad_order = receipt([trial("tcg", False), trial("kvm")])
        prior_success = receipt([trial(), trial("tcg")])
        uncertain_cleanup = receipt([trial(proved=False), trial("tcg")])
        uncertain_cleanup["observations"]["accelerator"]["attempts"][0]["cleaned"] = False
        skipped_preference = receipt([trial("tcg")])
        excessive = receipt([trial(proved=False), trial("tcg", False), trial()])
        for value in (bad_order, prior_success, uncertain_cleanup, skipped_preference, excessive):
            with self.subTest(history=value["observations"]["accelerator"]["attempts"]):
                selected = value["observations"]["accelerator"]["selected"]
                self.assert_refused_before_runtime(self.invoke(value, accelerator=selected), "accelerator.")

    def test_admit_recorded_success_cannot_be_relabelled_as_failure(self):
        prior = trial()
        prior["proved"] = False
        value = receipt([prior, trial("tcg")])
        self.assert_refused_before_runtime(self.invoke(value, accelerator="tcg"),
                                           "accelerator.trial_proof_contradiction")

    def test_admit_history_types_are_exact(self):
        mutations = [("performed", 1), ("proved", "true"), ("timed_out", 0),
                     ("cleaned", "true"), ("qmp_running", 0), ("elapsed_ms", True),
                     ("elapsed_ms", 0), ("elapsed_ms", -1), ("elapsed_ms", "10")]
        for field, changed in mutations:
            with self.subTest(field=field, value=changed):
                value = receipt()
                value["observations"]["accelerator"]["attempts"][0][field] = changed
                self.assert_refused_before_runtime(self.invoke(value), "accelerator.")

    def test_admit_mounted_requires_actual_true(self):
        for changed in (MISSING, None, "false", "true", 1, 0, False):
            with self.subTest(value=changed):
                value = receipt()
                if changed is MISSING:
                    del value["observations"]["loop_privilege"]["mounted"]
                else:
                    value["observations"]["loop_privilege"]["mounted"] = changed
                self.assert_refused_before_runtime(self.invoke(value), "loop_privilege")

    def test_admit_nested_lock_shapes_refuse_normally(self):
        for pointer in ("architecture", "toolchain", "toolchain.kiwi", "invocation", "probe", "budget"):
            for changed in (MISSING, None, [], 1, "invalid"):
                with self.subTest(pointer=pointer, value=changed):
                    lock = fixtures.lock()
                    parent = lock
                    parts = pointer.split(".")
                    for segment in parts[:-1]:
                        parent = parent[segment]
                    if changed is MISSING:
                        del parent[parts[-1]]
                    else:
                        parent[parts[-1]] = changed
                    self.assert_refused_before_runtime(self.invoke(lock=lock), "lock.")

    def test_admit_nested_receipt_shapes_refuse_normally(self):
        for pointer in ("attempt", "binding", "runner", "capacity", "observations",
                        "observations.container_toolchain", "observations.accelerator",
                        "observations.loop_privilege"):
            for changed in (MISSING, None, [], 1, "invalid"):
                with self.subTest(pointer=pointer, value=changed):
                    value = receipt()
                    parent = value
                    parts = pointer.split(".")
                    for segment in parts[:-1]:
                        parent = parent[segment]
                    if changed is MISSING:
                        del parent[parts[-1]]
                    else:
                        parent[parts[-1]] = changed
                    self.assert_refused_before_runtime(self.invoke(value), "receipt.")

    def test_admit_top_level_shapes_and_refusals_refuse_normally(self):
        for value in (None, [], 1, "invalid"):
            with self.subTest(value=value):
                self.assert_refused_before_runtime(self.invoke(value), "receipt.")
                self.assert_refused_before_runtime(self.invoke(lock=value), "lock.")
        for changed in (None, {}, 1, "invalid"):
            value = receipt()
            value["refusals"] = changed
            self.assert_refused_before_runtime(self.invoke(value), "receipt.type:refusals")

    def test_admit_all_consumed_proof_flags_are_exact_booleans(self):
        for name in judge.MANDATORY_OBSERVATIONS:
            for field in ("performed", "proved", "timed_out", "cleaned"):
                for changed in (None, "false", 0, 1):
                    with self.subTest(name=name, field=field, value=changed):
                        value = receipt()
                        value["observations"][name][field] = changed
                        self.assert_refused_before_runtime(self.invoke(value), "observation.type:")

    def test_judge_selected_summary_cannot_substitute_for_trial(self):
        value = receipt()
        value["observations"]["accelerator"]["selected"] = "tcg"
        verdict = judge.judge(fixtures.lock(), value, expected_context=expected("tcg"))
        self.assertEqual(verdict.state, "defect")
        self.assertIn("accelerator.summary_mismatch:selected", verdict.reasons)

    def test_judge_mounted_string_is_not_a_proof(self):
        value = receipt()
        value["observations"]["loop_privilege"]["mounted"] = "false"
        verdict = judge.judge(fixtures.lock(), value, expected_context=expected())
        self.assertEqual(verdict.state, "defect")
        self.assertIn("observation.type:loop_privilege.mounted", verdict.reasons)

    def test_judge_null_kiwi_is_named_defect(self):
        lock = fixtures.lock()
        lock["toolchain"]["kiwi"] = None
        verdict = judge.judge(lock, receipt(), expected_context=expected())
        self.assertEqual(verdict.state, "defect")
        self.assertIn("lock.type:toolchain.kiwi", verdict.reasons)
