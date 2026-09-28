# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# The bounded accelerator probe: its timeout, its owned cleanup and its refusal to call a
# wrong machine state a proof. These cases drive the probe against stub programs that speak
# the QMP handshake, so the contract is exercised without a hypervisor, a guest or an image.
# A stub is never evidence about a real accelerator; it is evidence about this probe.
#
# The stubs are started through the current interpreter, so the suite runs on a host whose
# temporary directory is mounted noexec.

import io
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import textwrap
import unittest

import accelerator_probe
import fixtures

HERE = pathlib.Path(__file__).resolve().parent
PROBE = HERE / "accelerator_probe.py"

# A stub that completes the handshake and reports the machine stopped before its first
# instruction, which is what -S produces.
GOOD_STUB = """
import json, sys
def send(obj):
    sys.stdout.write(json.dumps(obj) + "\\n"); sys.stdout.flush()
send({"QMP": {"version": {"qemu": {"major": 9, "minor": 2, "micro": 0}}, "capabilities": []}})
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    command = json.loads(line).get("execute")
    if command == "qmp_capabilities":
        send({"return": {}})
    elif command == "query-status":
        send({"return": {"running": %(running)s, "status": "%(status)s"}})
    elif command == "quit":
        send({"return": {}})
        break
sys.exit(0)
"""

# A stub that never speaks: the probe must give up on its own deadline and reap it.
HANG_STUB = """
import time
time.sleep(600)
"""

# A stub that refuses to initialize, the way QEMU exits when the requested accelerator is
# unavailable.
FAIL_STUB = """
import sys
sys.stderr.write("qemu-system-x86_64: -accel kvm: failed to initialize kvm: No such file or directory\\n")
sys.exit(1)
"""


class ProbeCase(unittest.TestCase):
    def setUp(self):
        self._temporary = tempfile.TemporaryDirectory()
        self.directory = pathlib.Path(self._temporary.name)
        self.addCleanup(self._temporary.cleanup)
        self.lock_path, _ = fixtures.write_pair(self.directory)

    def stub(self, body, name="stub_qemu.py", **substitutions):
        path = self.directory / name
        text = textwrap.dedent(body)
        if substitutions:
            text = text % substitutions
        path.write_text(text, encoding="utf-8")
        return f"{sys.executable} {path}"

    def run_probe(self, qemu, accelerator="tcg", timeout=None):
        argv = [
            sys.executable,
            str(PROBE),
            "--lock",
            str(self.lock_path),
            "--accelerator",
            accelerator,
            "--qemu",
            qemu,
        ]
        if timeout is not None:
            argv += ["--timeout", str(timeout)]
        done = subprocess.run(argv, capture_output=True, text=True, timeout=120)
        observation = json.loads(done.stdout) if done.stdout.strip() else {}
        return done, observation


class Success(ProbeCase):
    def test_a_completed_handshake_in_prelaunch_is_proved(self):
        qemu = self.stub(GOOD_STUB, running="False", status="prelaunch")
        done, observation = self.run_probe(qemu)
        self.assertEqual(0, done.returncode, done.stderr)
        self.assertTrue(observation["performed"])
        self.assertTrue(observation["proved"])
        self.assertEqual("prelaunch", observation["qmp_status"])
        self.assertFalse(observation["qmp_running"])
        self.assertFalse(observation["timed_out"])
        self.assertTrue(observation["cleaned"])

    def test_the_selected_accelerator_is_recorded_explicitly(self):
        qemu = self.stub(GOOD_STUB, running="False", status="prelaunch")
        _, observation = self.run_probe(qemu, accelerator="tcg")
        self.assertEqual("tcg", observation["selected"])

    def test_the_probe_owns_and_releases_its_process(self):
        qemu = self.stub(GOOD_STUB, running="False", status="prelaunch")
        _, observation = self.run_probe(qemu)
        pid = observation["pid"]
        self.assertIsInstance(pid, int)
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)

    def test_the_probe_reports_it_proves_initialization_only(self):
        qemu = self.stub(GOOD_STUB, running="False", status="prelaunch")
        _, observation = self.run_probe(qemu)
        self.assertIn("limit", observation)
        self.assertIn("initialization", observation["limit"].lower())


class WrongState(ProbeCase):
    def test_a_running_machine_is_not_a_prelaunch_proof(self):
        qemu = self.stub(GOOD_STUB, running="True", status="running")
        done, observation = self.run_probe(qemu)
        self.assertNotEqual(0, done.returncode)
        self.assertFalse(observation["proved"])
        self.assertEqual("running", observation["qmp_status"])

    def test_a_paused_machine_is_not_a_prelaunch_proof(self):
        qemu = self.stub(GOOD_STUB, running="False", status="paused")
        done, observation = self.run_probe(qemu)
        self.assertNotEqual(0, done.returncode)
        self.assertFalse(observation["proved"])


class Timeout(ProbeCase):
    def test_a_silent_process_times_out_rather_than_hanging(self):
        qemu = self.stub(HANG_STUB, name="hang_qemu.py")
        done, observation = self.run_probe(qemu, timeout=3)
        self.assertEqual(2, done.returncode)
        self.assertTrue(observation["timed_out"])
        self.assertFalse(observation["proved"])

    def test_a_timed_out_process_is_still_cleaned(self):
        qemu = self.stub(HANG_STUB, name="hang_qemu.py")
        _, observation = self.run_probe(qemu, timeout=3)
        self.assertTrue(observation["cleaned"])
        with self.assertRaises(ProcessLookupError):
            os.kill(observation["pid"], 0)

    def test_a_timeout_never_reports_a_machine_state(self):
        qemu = self.stub(HANG_STUB, name="hang_qemu.py")
        _, observation = self.run_probe(qemu, timeout=3)
        self.assertIsNone(observation["qmp_status"])


class FailedInitialization(ProbeCase):
    def test_an_accelerator_that_will_not_initialize_is_unproved(self):
        qemu = self.stub(FAIL_STUB, name="fail_qemu.py")
        done, observation = self.run_probe(qemu, accelerator="kvm")
        self.assertEqual(2, done.returncode)
        self.assertFalse(observation["proved"])
        self.assertTrue(observation["performed"])
        self.assertTrue(observation["cleaned"])

    def test_the_failure_detail_is_recorded_without_the_whole_log(self):
        qemu = self.stub(FAIL_STUB, name="fail_qemu.py")
        _, observation = self.run_probe(qemu, accelerator="kvm")
        self.assertIn("failed to initialize", observation["detail"])
        self.assertLessEqual(len(observation["detail"]), 2048)

    def test_an_absent_program_is_unproved_not_a_crash(self):
        done, observation = self.run_probe(
            f"{sys.executable} {self.directory / 'does-not-exist.py'}", accelerator="kvm"
        )
        self.assertEqual(2, done.returncode)
        self.assertFalse(observation["proved"])


class Release(unittest.TestCase):
    """cleaned must be derived from the process state, never asserted.

    A constant would let an uncleaned resource be reported as released, which is the
    failure these cases exist to prevent, so they drive the release directly with a
    process that does end and one that refuses to.
    """

    class FakeProcess:
        def __init__(self, ends):
            self.ends = ends
            self.terminated = False
            self.killed = False
            self.stdin = io.BytesIO()
            self.stdout = io.BytesIO()
            self.stderr = io.BytesIO(b"")

        def poll(self):
            if self.ends and (self.terminated or self.killed):
                return 0
            return None

        def terminate(self):
            self.terminated = True

        def kill(self):
            self.killed = True

        def wait(self, timeout=None):
            if self.poll() is None:
                raise subprocess.TimeoutExpired("fake", timeout)
            return 0

    def observation(self):
        return accelerator_probe.observation_template("tcg")

    def test_a_process_that_ends_is_recorded_as_cleaned(self):
        process = self.FakeProcess(ends=True)
        observation = self.observation()
        accelerator_probe.release(process, observation)
        self.assertTrue(process.terminated)
        self.assertTrue(observation["cleaned"])

    def test_a_process_that_never_ends_is_not_recorded_as_cleaned(self):
        process = self.FakeProcess(ends=False)
        observation = self.observation()
        accelerator_probe.release(process, observation)
        self.assertFalse(
            observation["cleaned"],
            "an unreleased process must never be reported as cleaned",
        )

    def test_release_escalates_from_terminate_to_kill(self):
        process = self.FakeProcess(ends=False)
        accelerator_probe.release(process, self.observation())
        self.assertTrue(process.terminated)
        self.assertTrue(process.killed, "release must escalate when terminate is ignored")

    def test_release_records_the_programs_own_diagnostic(self):
        process = self.FakeProcess(ends=True)
        process.stderr = io.BytesIO(b"failed to initialize kvm\n")
        observation = self.observation()
        accelerator_probe.release(process, observation)
        self.assertIn("failed to initialize kvm", observation["detail"])


class Refusals(ProbeCase):
    def test_an_accelerator_outside_the_lock_preference_is_refused(self):
        qemu = self.stub(GOOD_STUB, running="False", status="prelaunch")
        done, _ = self.run_probe(qemu, accelerator="whpx")
        self.assertEqual(1, done.returncode)

    def test_an_absent_lock_is_refused(self):
        done = subprocess.run(
            [
                sys.executable,
                str(PROBE),
                "--lock",
                str(self.directory / "absent.json"),
                "--accelerator",
                "tcg",
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(1, done.returncode)

    def test_a_nonpositive_timeout_is_refused(self):
        qemu = self.stub(GOOD_STUB, running="False", status="prelaunch")
        done, _ = self.run_probe(qemu, timeout=0)
        self.assertEqual(1, done.returncode)


if __name__ == "__main__":
    unittest.main()
