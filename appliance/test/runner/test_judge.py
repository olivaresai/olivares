# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# The judgement contract of the image toolchain prerequisite check, and its negative
# controls. Every case starts from a complete fixture and changes exactly one field, so a
# red names the rule that stopped holding. Each control asserts the reason the judge must
# report, not only that the exit code was nonzero: an invocation error or a syntax error
# does not satisfy these assertions.
#
# States and exits under test: eligible -> 0, unavailable -> 2, defect -> 1.
# eligible states measured prerequisites for the next attempt only. It is not a statement
# that an image builds, boots or installs.

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

import fixtures
import judge

HERE = pathlib.Path(__file__).resolve().parent
JUDGE = HERE / "judge.py"


class JudgeCase(unittest.TestCase):
    """Helpers that keep each case to one mutation and one named expectation."""

    def verdict(self, lock=None, receipt=None, lock_digest=None):
        lock = fixtures.lock() if lock is None else lock
        receipt = fixtures.receipt() if receipt is None else receipt
        if lock_digest is None:
            lock_digest = receipt.get("attempt", {}).get("lock_digest")
        return judge.judge(lock, receipt, lock_digest)

    def assertEligible(self, verdict):
        self.assertEqual(
            "eligible", verdict.state, f"expected eligible, reasons={verdict.reasons}"
        )
        self.assertEqual(0, verdict.exit_code)

    def assertDefect(self, verdict, reason):
        self.assertEqual(
            "defect", verdict.state, f"expected defect {reason!r}, reasons={verdict.reasons}"
        )
        self.assertEqual(1, verdict.exit_code)
        self.assertIn(reason, verdict.reasons)

    def assertUnavailable(self, verdict, reason):
        self.assertEqual(
            "unavailable",
            verdict.state,
            f"expected unavailable {reason!r}, reasons={verdict.reasons}",
        )
        self.assertEqual(2, verdict.exit_code)
        self.assertIn(reason, verdict.reasons)


class EligibleBaseline(JudgeCase):
    def test_complete_receipt_is_eligible(self):
        self.assertEligible(self.verdict())

    def test_exit_codes_are_eligible_zero_unavailable_two_defect_one(self):
        self.assertEqual(0, judge.EXIT_BY_STATE["eligible"])
        self.assertEqual(2, judge.EXIT_BY_STATE["unavailable"])
        self.assertEqual(1, judge.EXIT_BY_STATE["defect"])

    def test_eligible_is_not_a_build_or_boot_claim(self):
        # The verdict carries the explicit limit, so a reader of the receipt cannot take
        # eligibility for image support.
        verdict = self.verdict()
        self.assertTrue(any("next attempt" in note for note in verdict.notes))


class LockValidity(JudgeCase):
    def test_unknown_lock_schema_is_defect(self):
        lock = fixtures.lock()
        lock["schema"] = "some-other-lock/v9"
        self.assertDefect(self.verdict(lock=lock), "lock.schema")

    def test_each_mandatory_lock_field_is_required(self):
        for pointer in judge.MANDATORY_LOCK_FIELDS:
            with self.subTest(pointer=pointer):
                lock = fixtures.lock()
                judge.delete_pointer(lock, pointer)
                self.assertDefect(self.verdict(lock=lock), f"lock.missing:{pointer}")

    def test_removed_base_image_digest_is_defect(self):
        # Named negative control: remove the digest.
        lock = fixtures.lock()
        del lock["base_image"]["index_digest"]
        self.assertDefect(self.verdict(lock=lock), "lock.missing:base_image.index_digest")

    def test_malformed_base_image_digest_is_defect(self):
        lock = fixtures.lock()
        lock["base_image"]["index_digest"] = "sha256:not-a-digest"
        self.assertDefect(
            self.verdict(lock=lock), "lock.digest_format:base_image.index_digest"
        )

    def test_unprefixed_base_image_digest_is_defect(self):
        lock = fixtures.lock()
        lock["base_image"]["index_digest"] = "1" * 64
        self.assertDefect(
            self.verdict(lock=lock), "lock.digest_format:base_image.index_digest"
        )

    def test_malformed_package_digest_is_defect(self):
        lock = fixtures.lock()
        lock["toolchain"]["kiwi"]["sha256"] = "3" * 63
        self.assertDefect(
            self.verdict(lock=lock), "lock.digest_format:toolchain.kiwi.sha256"
        )

    def test_mutable_kiwi_pin_is_defect(self):
        for mutable in ("latest", "", "11.0", "*"):
            with self.subTest(version=mutable):
                lock = fixtures.lock()
                lock["toolchain"]["kiwi"]["upstream_version"] = mutable
                self.assertDefect(self.verdict(lock=lock), "lock.version_not_exact")

    def test_zero_declared_budget_is_defect(self):
        lock = fixtures.lock()
        lock["budget"]["declared_build_bytes"] = 0
        self.assertDefect(self.verdict(lock=lock), "lock.budget_not_positive")

    def test_negative_declared_budget_is_defect(self):
        lock = fixtures.lock()
        lock["budget"]["declared_build_bytes"] = -1
        self.assertDefect(self.verdict(lock=lock), "lock.budget_not_positive")

    def test_declared_budget_labeled_measured_is_defect(self):
        # Named negative control: an unmeasured capacity supplied as measured. The lock
        # may declare a budget; it must never claim the build was measured.
        lock = fixtures.lock()
        lock["budget"]["measured"] = True
        self.assertDefect(self.verdict(lock=lock), "lock.budget_labeled_measured")

    def test_architecture_must_be_the_supported_pair(self):
        lock = fixtures.lock()
        lock["architecture"] = {"deb": "arm64", "uname_machine": "aarch64"}
        receipt = fixtures.receipt()
        self.assertDefect(
            self.verdict(lock=lock, receipt=receipt), "architecture.mismatch"
        )


class ReceiptBinding(JudgeCase):
    def test_unknown_receipt_schema_is_defect(self):
        receipt = fixtures.receipt()
        receipt["schema"] = "olivares-appliance-runner-receipt/v0"
        self.assertDefect(self.verdict(receipt=receipt), "receipt.schema")

    def test_missing_attempt_is_defect(self):
        receipt = fixtures.receipt()
        del receipt["attempt"]
        self.assertDefect(self.verdict(receipt=receipt), "receipt.missing:attempt")

    def test_each_mandatory_receipt_field_is_required(self):
        for pointer in judge.MANDATORY_RECEIPT_FIELDS:
            with self.subTest(pointer=pointer):
                receipt = fixtures.receipt()
                judge.delete_pointer(receipt, pointer)
                self.assertDefect(
                    self.verdict(receipt=receipt), f"receipt.missing:{pointer}"
                )

    def test_unrelated_lock_digest_is_defect(self):
        # Named negative control: an unrelated lock. The receipt was produced against
        # different lock bytes than the one being judged.
        receipt = fixtures.receipt()
        receipt["attempt"]["lock_digest"] = fixtures.OTHER_LOCK_DIGEST
        self.assertDefect(
            self.verdict(receipt=receipt, lock_digest=fixtures.FIXTURE_LOCK_DIGEST),
            "binding.lock_digest_mismatch",
        )

    def test_unrelated_lock_id_is_defect(self):
        receipt = fixtures.receipt()
        receipt["attempt"]["lock_id"] = "appliance-image-toolchain-someone-else"
        self.assertDefect(self.verdict(receipt=receipt), "binding.lock_id_mismatch")

    def test_attempt_finishing_before_it_started_is_defect(self):
        receipt = fixtures.receipt()
        receipt["attempt"]["finished_at"] = "2026-09-22T13:59:00Z"
        self.assertDefect(self.verdict(receipt=receipt), "binding.attempt_stale")

    def test_unparsable_attempt_timestamp_is_defect(self):
        receipt = fixtures.receipt()
        receipt["attempt"]["started_at"] = "yesterday"
        self.assertDefect(self.verdict(receipt=receipt), "binding.attempt_time_format")

    def test_nonpositive_elapsed_time_is_defect(self):
        for elapsed in (0, -5):
            with self.subTest(elapsed=elapsed):
                receipt = fixtures.receipt()
                receipt["attempt"]["elapsed_ms"] = elapsed
                self.assertDefect(self.verdict(receipt=receipt), "binding.elapsed_not_positive")

    def test_empty_attempt_id_is_defect(self):
        receipt = fixtures.receipt()
        receipt["attempt"]["id"] = ""
        self.assertDefect(self.verdict(receipt=receipt), "binding.attempt_id_missing")

    def test_runner_architecture_disagreeing_with_lock_is_defect(self):
        receipt = fixtures.receipt()
        receipt["runner"]["uname_machine"] = "aarch64"
        self.assertDefect(self.verdict(receipt=receipt), "architecture.mismatch")


class ToolchainProof(JudgeCase):
    def test_wrong_kiwi_version_is_defect(self):
        # Named negative control: report a wrong KIWI version.
        receipt = fixtures.receipt()
        receipt["observations"]["container_toolchain"]["reported_version"] = "10.3.11"
        self.assertDefect(self.verdict(receipt=receipt), "toolchain.version_mismatch")

    def test_absent_reported_version_is_defect(self):
        receipt = fixtures.receipt()
        del receipt["observations"]["container_toolchain"]["reported_version"]
        self.assertDefect(self.verdict(receipt=receipt), "toolchain.version_missing")

    def test_image_tag_disagreeing_with_lock_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"]["container_toolchain"]["image_tag"] = "kiwi:whatever"
        self.assertDefect(self.verdict(receipt=receipt), "toolchain.image_tag_mismatch")


class MissingEvidence(JudgeCase):
    def test_each_mandatory_observation_is_required(self):
        for name in judge.MANDATORY_OBSERVATIONS:
            with self.subTest(observation=name):
                receipt = fixtures.receipt()
                del receipt["observations"][name]
                self.assertDefect(
                    self.verdict(receipt=receipt), f"observation.missing:{name}"
                )

    def test_omitted_container_observation_is_defect(self):
        # Named negative control: omit the successful container observation.
        receipt = fixtures.receipt()
        del receipt["observations"]["container_toolchain"]
        self.assertDefect(
            self.verdict(receipt=receipt), "observation.missing:container_toolchain"
        )

    def test_omitted_loop_observation_is_defect(self):
        # Named negative control: omit the successful loop observation.
        receipt = fixtures.receipt()
        del receipt["observations"]["loop_privilege"]
        self.assertDefect(
            self.verdict(receipt=receipt), "observation.missing:loop_privilege"
        )

    def test_not_performed_observation_is_defect_not_a_silent_skip(self):
        for name in judge.MANDATORY_OBSERVATIONS:
            with self.subTest(observation=name):
                receipt = fixtures.receipt()
                receipt["observations"][name]["performed"] = False
                receipt["observations"][name]["proved"] = False
                self.assertDefect(
                    self.verdict(receipt=receipt), f"observation.not_performed:{name}"
                )

    def test_empty_observations_object_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"] = {}
        verdict = self.verdict(receipt=receipt)
        self.assertEqual("defect", verdict.state)
        for name in judge.MANDATORY_OBSERVATIONS:
            self.assertIn(f"observation.missing:{name}", verdict.reasons)


class Contradictions(JudgeCase):
    def test_proved_without_being_performed_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"]["loop_privilege"]["performed"] = False
        self.assertDefect(
            self.verdict(receipt=receipt), "observation.contradiction:loop_privilege"
        )

    def test_proved_while_timed_out_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"]["timed_out"] = True
        self.assertDefect(
            self.verdict(receipt=receipt), "observation.contradiction:accelerator"
        )

    def test_proved_without_cleanup_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"]["loop_privilege"]["cleaned"] = False
        self.assertDefect(
            self.verdict(receipt=receipt), "observation.contradiction:loop_privilege"
        )

    def test_uncleaned_resource_after_failure_is_defect(self):
        # A failed probe still owns its cleanup. An uncleaned failure is a defect, not a
        # mere environmental shortage.
        receipt = fixtures.receipt()
        receipt["observations"]["loop_privilege"]["proved"] = False
        receipt["observations"]["loop_privilege"]["cleaned"] = False
        self.assertDefect(
            self.verdict(receipt=receipt), "observation.not_cleaned:loop_privilege"
        )

    def test_capacity_probe_allocation_disagreeing_with_lock_is_defect(self):
        receipt = fixtures.receipt()
        receipt["capacity"]["probe_allocation_bytes"] = 1234
        self.assertDefect(self.verdict(receipt=receipt), "capacity.probe_allocation_mismatch")

    def test_receipt_declared_budget_disagreeing_with_lock_is_defect(self):
        receipt = fixtures.receipt()
        receipt["capacity"]["declared_build_bytes"] = 999
        self.assertDefect(self.verdict(receipt=receipt), "capacity.declared_budget_mismatch")

    def test_declared_capacity_labeled_measured_is_defect(self):
        # Named negative control: supply an unmeasured capacity as measured.
        receipt = fixtures.receipt()
        receipt["capacity"]["declared_build_measured"] = True
        self.assertDefect(
            self.verdict(receipt=receipt), "capacity.declared_labeled_measured"
        )


class AcceleratorRules(JudgeCase):
    def test_wrong_qmp_status_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"]["qmp_status"] = "running"
        self.assertDefect(self.verdict(receipt=receipt), "accelerator.state_mismatch")

    def test_absent_qmp_status_is_defect(self):
        receipt = fixtures.receipt()
        del receipt["observations"]["accelerator"]["qmp_status"]
        self.assertDefect(self.verdict(receipt=receipt), "accelerator.state_missing")

    def test_running_vcpus_contradict_a_prelaunch_probe(self):
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"]["qmp_running"] = True
        self.assertDefect(self.verdict(receipt=receipt), "accelerator.vcpus_running")

    def test_selection_outside_the_locked_preference_is_defect(self):
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"]["selected"] = "whpx"
        self.assertDefect(self.verdict(receipt=receipt), "accelerator.unknown_selection")

    def test_absent_kvm_with_an_explicit_tcg_choice_is_eligible(self):
        # Named case: KVM absence may select TCG explicitly.
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"]["selected"] = "tcg"
        self.assertEligible(self.verdict(receipt=receipt))

    def test_a_tcg_selection_states_it_sets_no_boot_budget(self):
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"]["selected"] = "tcg"
        verdict = self.verdict(receipt=receipt)
        self.assertTrue(
            any("tcg" in note.lower() and "budget" in note.lower() for note in verdict.notes),
            f"notes={verdict.notes}",
        )

    def test_an_implicit_selection_is_defect(self):
        for selected in ("", None):
            with self.subTest(selected=selected):
                receipt = fixtures.receipt()
                receipt["observations"]["accelerator"]["selected"] = selected
                self.assertDefect(
                    self.verdict(receipt=receipt), "accelerator.unknown_selection"
                )

    def test_accelerator_timeout_is_unavailable(self):
        # Named negative control: make a QEMU start time out.
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"].update(
            {"proved": False, "timed_out": True, "selected": "tcg", "qmp_status": None}
        )
        self.assertUnavailable(
            self.verdict(receipt=receipt), "observation.unproved:accelerator"
        )

    def test_no_accelerator_initialized_is_unavailable(self):
        receipt = fixtures.receipt()
        receipt["observations"]["accelerator"].update(
            {"proved": False, "selected": None, "qmp_status": None}
        )
        self.assertUnavailable(
            self.verdict(receipt=receipt), "observation.unproved:accelerator"
        )


class EnvironmentalShortage(JudgeCase):
    def test_absent_passwordless_sudo_is_unavailable(self):
        receipt = fixtures.receipt()
        receipt["runner"]["passwordless_sudo"] = False
        self.assertUnavailable(self.verdict(receipt=receipt), "runner.no_passwordless_sudo")

    def test_insufficient_declared_capacity_is_unavailable(self):
        # Named negative control: insufficient declared capacity. The lock declares the
        # budget; the receipt measures the bytes actually free.
        receipt = fixtures.receipt()
        receipt["capacity"]["evidence_avail_bytes"] = 1000000
        self.assertUnavailable(
            self.verdict(receipt=receipt), "capacity.insufficient_for_declared_budget"
        )

    def test_capacity_exactly_at_the_declared_budget_is_eligible(self):
        receipt = fixtures.receipt()
        receipt["capacity"]["evidence_avail_bytes"] = fixtures.lock()["budget"][
            "declared_build_bytes"
        ]
        self.assertEligible(self.verdict(receipt=receipt))

    def test_capacity_one_byte_below_the_declared_budget_is_unavailable(self):
        receipt = fixtures.receipt()
        receipt["capacity"]["evidence_avail_bytes"] = (
            fixtures.lock()["budget"]["declared_build_bytes"] - 1
        )
        self.assertUnavailable(
            self.verdict(receipt=receipt), "capacity.insufficient_for_declared_budget"
        )

    def test_unproved_container_check_is_unavailable(self):
        receipt = fixtures.receipt()
        receipt["observations"]["container_toolchain"].update(
            {"proved": False, "reported_version": None}
        )
        self.assertUnavailable(
            self.verdict(receipt=receipt), "observation.unproved:container_toolchain"
        )

    def test_unproved_loop_check_is_unavailable(self):
        receipt = fixtures.receipt()
        receipt["observations"]["loop_privilege"].update({"proved": False, "mounted": False})
        self.assertUnavailable(
            self.verdict(receipt=receipt), "observation.unproved:loop_privilege"
        )

    def test_a_recorded_refusal_is_never_silently_dropped(self):
        receipt = fixtures.receipt()
        receipt["refusals"] = [{"name": "container_toolchain", "reason": "pull refused"}]
        receipt["observations"]["container_toolchain"].update(
            {"proved": False, "reported_version": None}
        )
        verdict = self.verdict(receipt=receipt)
        self.assertEqual("unavailable", verdict.state)
        self.assertTrue(any("pull refused" in note for note in verdict.notes))


class Precedence(JudgeCase):
    def test_a_defect_outranks_an_environmental_shortage(self):
        receipt = fixtures.receipt()
        receipt["runner"]["passwordless_sudo"] = False           # would be unavailable
        receipt["observations"]["container_toolchain"]["reported_version"] = "9.9.9"
        verdict = self.verdict(receipt=receipt)
        self.assertEqual("defect", verdict.state)
        self.assertEqual(1, verdict.exit_code)
        self.assertIn("toolchain.version_mismatch", verdict.reasons)

    def test_missing_evidence_never_becomes_eligible(self):
        receipt = fixtures.receipt()
        receipt["observations"] = {}
        receipt["capacity"]["evidence_avail_bytes"] = 10 ** 12
        self.assertNotEqual("eligible", self.verdict(receipt=receipt).state)


class CommandLine(unittest.TestCase):
    """The judge as D runs it: two paths in, a classified exit out."""

    def run_judge(self, lock_obj=None, receipt_obj=None, bind=True):
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            lock_path, receipt_path = fixtures.write_pair(
                directory, lock_obj=lock_obj, receipt_obj=receipt_obj, bind=bind
            )
            return subprocess.run(
                [
                    sys.executable,
                    str(JUDGE),
                    "--lock",
                    str(lock_path),
                    "--receipt",
                    str(receipt_path),
                ],
                capture_output=True,
                text=True,
                timeout=60,
            )

    def test_eligible_receipt_exits_zero_and_names_the_state(self):
        done = self.run_judge()
        self.assertEqual(0, done.returncode, done.stderr)
        self.assertIn("eligible", done.stdout)

    def test_unavailable_receipt_exits_two(self):
        receipt = fixtures.receipt()
        receipt["runner"]["passwordless_sudo"] = False
        done = self.run_judge(receipt_obj=receipt)
        self.assertEqual(2, done.returncode, done.stderr)
        self.assertIn("unavailable", done.stdout + done.stderr)

    def test_defect_receipt_exits_one(self):
        receipt = fixtures.receipt()
        receipt["observations"]["container_toolchain"]["reported_version"] = "10.3.11"
        done = self.run_judge(receipt_obj=receipt)
        self.assertEqual(1, done.returncode, done.stderr)
        self.assertIn("toolchain.version_mismatch", done.stdout + done.stderr)

    def test_the_digest_binding_is_computed_from_the_lock_file_bytes(self):
        # No implicit mutable lookup: an unbound receipt is a defect even though every
        # other field is complete.
        done = self.run_judge(bind=False)
        self.assertEqual(1, done.returncode, done.stderr)
        self.assertIn("binding.lock_digest_mismatch", done.stdout + done.stderr)

    def test_an_absent_receipt_is_a_defect_not_a_pass(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            lock_path, _ = fixtures.write_pair(directory)
            done = subprocess.run(
                [
                    sys.executable,
                    str(JUDGE),
                    "--lock",
                    str(lock_path),
                    "--receipt",
                    str(directory / "absent.json"),
                ],
                capture_output=True,
                text=True,
                timeout=60,
            )
        self.assertEqual(1, done.returncode)
        self.assertNotIn("eligible", done.stdout)

    def test_malformed_json_is_a_defect(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = pathlib.Path(raw)
            lock_path, receipt_path = fixtures.write_pair(directory)
            receipt_path.write_text("{ not json", encoding="utf-8")
            done = subprocess.run(
                [
                    sys.executable,
                    str(JUDGE),
                    "--lock",
                    str(lock_path),
                    "--receipt",
                    str(receipt_path),
                ],
                capture_output=True,
                text=True,
                timeout=60,
            )
        self.assertEqual(1, done.returncode)

    def test_the_judge_emits_a_machine_readable_verdict(self):
        done = self.run_judge()
        payload = json.loads(done.stdout)
        self.assertEqual("eligible", payload["state"])
        self.assertIn("reasons", payload)
        self.assertIn("notes", payload)


if __name__ == "__main__":
    unittest.main()
