# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Injected controls only: no pidfd acquisition, process launch or signal."""
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("k3_lifecycle_helper", HERE / "engine-lifecycle-helper.py")
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
CORPUS = json.loads((HERE / "engine-lifecycle-corpus.json").read_text())


class CorpusControls(unittest.TestCase):
    pass


def corpus_control(case):
    def control(self):
        value = copy.deepcopy(CORPUS["bases"][case["kind"]])
        def parent(keys):
            current = value
            for key in keys[:-1]:
                current = current[key]
            return current
        for keys, replacement in case["changes"]:
            parent(keys)[keys[-1]] = replacement
        for keys in case["remove"]:
            del parent(keys)[keys[-1]]
        parse = {"lease": helper.validate_lease, "pointer": helper.validate_pointer, "stop": helper.validate_stop}[case["kind"]]
        if case["valid"]:
            self.assertEqual(parse(value), value)
        else:
            with self.assertRaises(helper.Refusal):
                parse(value)
    return control


for index, case in enumerate(CORPUS["cases"]):
    setattr(CorpusControls, f"test_{index:02d}_{case['name'].replace('-', '_')}", corpus_control(case))


class FakeAdapter:
    def __init__(self, lease, polls=None):
        self.lease = copy.deepcopy(lease)
        self.polls = polls if polls is not None else [(0, 0), (5, 1)]
        self.events = []
        self.clock = 0
        self.outcome = "sent"
        self.open_failure = None
        self.send_failure = None
        self.final_ticks = lease["start_ticks"]

    def now(self):
        return self.clock

    def open_pidfd(self, pid):
        self.events.append(("open", pid))
        if self.open_failure:
            raise helper.Refusal(self.open_failure)
        return 901

    def capture(self, pid, expected_start):
        self.events.append(("capture", pid))
        helper.require(self.lease["start_ticks"] == expected_start, "changed_generation")
        return copy.deepcopy(self.lease)

    def start_ticks(self, pid):
        self.events.append(("recheck", pid))
        return self.final_ticks

    def poll(self, fd, budget):
        self.events.append(("poll", fd, budget))
        delta, event = self.polls.pop(0)
        self.clock += delta
        return event

    def send(self, fd, signal):
        self.events.append(("send", fd, signal))
        if self.send_failure:
            raise helper.Refusal(self.send_failure)
        return self.outcome

    def close(self, fd):
        self.events.append(("close", fd))


class DescriptorControls(unittest.TestCase):
    def setUp(self):
        self.lease = copy.deepcopy(CORPUS["bases"]["lease"])
        self.records = []

    def run_stop(self, adapter):
        return helper.stop_generation(self.lease, adapter, self.records.append)

    def refused(self, adapter, code):
        with self.assertRaises(helper.Refusal) as raised:
            self.run_stop(adapter)
        self.assertEqual(raised.exception.code, code)

    def test_term_uses_acquired_validated_descriptor_and_observes_terminal(self):
        adapter = FakeAdapter(self.lease)
        result = self.run_stop(adapter)
        self.assertEqual(adapter.events[:4], [("open", 4242), ("capture", 4242), ("recheck", 4242), ("poll", 901, 0)])
        self.assertEqual([e for e in adapter.events if e[0] == "send"], [("send", 901, "SIGTERM")])
        self.assertEqual(result["terminal"]["observation"], "pidfd")
        self.assertEqual(adapter.events[-1], ("close", 901))
        self.assertEqual([e["event"] for e in self.records], ["descriptor_acquired", "generation_validated", "signal_attempt", "signal_result", "terminal_observed"])

    def test_escalation_never_reopens_pid(self):
        adapter = FakeAdapter(self.lease, [(0, 0), (20_000, 0), (4, 17)])
        result = self.run_stop(adapter)
        self.assertEqual([e for e in adapter.events if e[0] == "open"], [("open", 4242)])
        self.assertEqual([e for e in adapter.events if e[0] == "send"], [("send", 901, "SIGTERM"), ("send", 901, "SIGKILL")])
        self.assertEqual([e["signal"] for e in result["signals"]], ["SIGTERM", "SIGKILL"])

    def test_stale_pid_occupant_is_refused_after_acquisition(self):
        adapter = FakeAdapter(self.lease)
        adapter.lease["start_ticks"] = "999"
        self.refused(adapter, "changed_generation")
        self.assertFalse(any(e[0] == "send" for e in adapter.events))
        self.assertEqual(adapter.events[-1], ("close", 901))

    def test_generation_changes_during_identity_reads(self):
        adapter = FakeAdapter(self.lease)
        adapter.final_ticks = "998"
        self.refused(adapter, "changed_generation")
        self.assertFalse(any(e[0] == "send" for e in adapter.events))

    def test_each_ownership_mismatch_refuses_signal(self):
        for key in ("executable", "cwd", "data_dir", "data_directory", "boot_id", "pid_namespace", "listen", "grpc_listen"):
            with self.subTest(field=key):
                adapter = FakeAdapter(self.lease)
                if key in ("executable", "data_directory"):
                    adapter.lease[key]["inode"] = "9876"
                else:
                    adapter.lease[key] = "different"
                self.refused(adapter, "ownership_mismatch")
                self.assertFalse(any(e[0] == "send" for e in adapter.events))

    def test_exited_before_validation_is_not_success(self):
        adapter = FakeAdapter(self.lease, [(0, 1)])
        self.refused(adapter, "unavailable_process")
        self.assertFalse(any(e[0] == "send" for e in adapter.events))

    def test_absence_before_acquisition_is_terminal_unobserved(self):
        adapter = FakeAdapter(self.lease)
        adapter.open_failure = "terminal_unobserved"
        self.refused(adapter, "terminal_unobserved")
        self.assertEqual(adapter.events, [("open", 4242)])

    def test_missing_capability_has_no_fallback(self):
        adapter = FakeAdapter(self.lease)
        adapter.open_failure = "capability_refusal"
        self.refused(adapter, "capability_refusal")
        self.assertEqual(adapter.events, [("open", 4242)])

    def test_esrch_requires_the_same_descriptor_terminal_observation(self):
        adapter = FakeAdapter(self.lease)
        adapter.outcome = "esrch"
        result = self.run_stop(adapter)
        self.assertEqual(result["signals"][0]["outcome"], "esrch")
        self.assertEqual(result["terminal"]["events"], 1)

    def test_esrch_without_terminal_evidence_exhausts_deadline(self):
        adapter = FakeAdapter(self.lease, [(0, 0), (20_000, 0), (5_000, 0)])
        adapter.outcome = "esrch"
        self.refused(adapter, "deadline_exceeded")
        self.assertEqual(adapter.events[-1], ("close", 901))

    def test_poll_error_is_not_terminal_readiness(self):
        for event in (8, 32, 33):
            with self.subTest(event=event):
                adapter = FakeAdapter(self.lease, [(0, 0), (1, event)])
                self.refused(adapter, "signal_failure")

    def test_signal_failure_does_not_become_absence(self):
        adapter = FakeAdapter(self.lease)
        adapter.send_failure = "signal_failure"
        self.refused(adapter, "signal_failure")
        self.assertEqual(self.records[-1]["event"], "signal_attempt")

    def test_publication_failure_after_signal_keeps_known_attempt(self):
        adapter = FakeAdapter(self.lease)
        def record(row):
            if row["event"] == "signal_result":
                raise helper.Refusal("result_publication_failure")
            self.records.append(row)
        with self.assertRaises(helper.Refusal) as raised:
            helper.stop_generation(self.lease, adapter, record)
        self.assertEqual(raised.exception.code, "result_publication_failure")
        self.assertEqual(self.records[-1]["event"], "signal_attempt")
        self.assertEqual(adapter.events[-1], ("close", 901))

    def test_publication_failure_after_terminal_is_still_failed(self):
        adapter = FakeAdapter(self.lease)
        def record(row):
            if row["event"] == "terminal_observed":
                raise helper.Refusal("result_publication_failure")
        with self.assertRaises(helper.Refusal) as raised:
            helper.stop_generation(self.lease, adapter, record)
        self.assertEqual(raised.exception.code, "result_publication_failure")
        self.assertTrue(any(e[0] == "send" for e in adapter.events))

    def test_helper_entry_and_parent_argument_order_without_os_adapter(self):
        raw = json.dumps(self.lease).encode()
        digest = hashlib.sha256(raw).hexdigest()
        name = f"lease.{self.lease['lease_id']}.json"
        result_name = f"stop.{self.lease['lease_id']}.json"
        writes = []
        class FakeDirectory:
            fd = 888
            def __init__(self, work):
                self.work = work
            def read(self, target):
                return raw if target == name else b'{"operation_id":"owned"}'
            def create(self, target):
                return 701 if target == result_name else 702
            def write(self, fd, record):
                writes.append((fd, record))
        adapter = FakeAdapter(self.lease)
        output = io.StringIO()
        with mock.patch.object(helper, "OwnedDirectory", FakeDirectory), mock.patch.object(helper, "LinuxAdapter", lambda: adapter), mock.patch.object(helper, "helper_identity", lambda a: CORPUS["bases"]["stop"]["helper"]), mock.patch.object(helper.os, "close"), mock.patch.object(helper.os, "fsync"), contextlib.redirect_stdout(output):
            status = helper.main([self.lease["work_dir"], name, digest, result_name, "restart"])
        self.assertEqual(status, 0)
        self.assertEqual(json.loads(output.getvalue()), {"ok": True})
        receipt = next(record for fd, record in writes if fd == 701)
        self.assertEqual(receipt["lease"], {"name": name, "sha256": digest})
        self.assertEqual(receipt["purpose"], "restart")


if __name__ == "__main__":
    unittest.main(verbosity=2)
