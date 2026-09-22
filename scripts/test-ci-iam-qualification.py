#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a): see DISCLAIMER.md
"""Command-level synthetic protocol controls. Never invokes a real Go tool.

Each fixture has a closed PATH with fake Go, no task/network tools, and its own
Git repository, event, output and process group. No test override enters the
production runner. Synthetic test events are not product qualification.
"""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("iam", ROOT / "scripts/ci-iam-qualification.py")
IAM = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(IAM)

FAKE_GO = r'''#!PYTHON
import hashlib,json,os,signal,sys,time
from pathlib import Path
settings=json.loads(Path(CONFIG).read_text())
root=Path.cwd(); data=json.loads((root/('ci/iam-qualification/'+settings['family']+'.json')).read_text())
command=next(c for c in data['commands'].values() if c['argv'][1:]==sys.argv[1:])
fingerprint=hashlib.sha256(b''.join((root/p).read_bytes() for p in data['production'])).hexdigest()
mutant=settings['mutated'].get(fingerprint)
red=next((m['red'] for m in data['mutants'] if m['id']==mutant),{})
mode=settings['mode']; package=command['package']; tests=command['expected_tests']
wire=next((t for t in tests if t.startswith('TestLDAPWireCertificateTransport/')),None)
if mode=='bench_timeout':
    child=os.fork()
    if child==0:time.sleep(10);sys.exit(0)
    Path(settings['ready']).write_text(str(child))
    time.sleep(10)
if mode=='cancel_mutant' and mutant:
    os.kill(int(Path(settings['controller']).read_text()),signal.SIGTERM)
    time.sleep(10)
def emit(action,test=None,output=None):
    event={'Action':action,'Package':package}
    if test is not None:event['Test']=test
    if output is not None:event['Output']=output
    print(json.dumps(event),flush=True)
if mode=='timeout':
    signal.signal(signal.SIGTERM,signal.SIG_IGN)
    # A same-group descendant must also be gone before restoration/removal.
    child=os.fork()
    if child==0:time.sleep(10);sys.exit(0)
    time.sleep(10);sys.exit(0)
if mode=='compiler' or mode=='compiler_mutant' and mutant:
    print('synthetic compiler error',file=sys.stderr);sys.exit(1)
if mode=='drift':
    target=next(p for p in data['inputs'] if p.endswith('_test.go'))
    (root/target).write_text('synthetic changed test input\n')
if mode=='restore' and mutant:
    (root/data['production'][0]).unlink()
if mode=='missing_artifact':
    (root.parent/('iam-qualification-'+settings['family'])/'identity.json').unlink(missing_ok=True)
emit('start')
if mode=='secret':print(os.environ['OLIVARES_TEST_POSTGRES_DSN'],file=sys.stderr)
for name in tests:
    if mode=='missing' and command is data['commands']['baseline'] and name==tests[-1]:continue
    if mode=='missing_wire' and name==wire:continue
    emit('run',name)
    markers=red.get(name,[])
    if mode=='unrelated' and name==tests[-1] or mode=='unrelated_mutant' and mutant and name==tests[0]:markers=['unrelated assertion']
    if mode=='wrong_assertion' and markers:markers=['different failure']
    if mode=='survived':markers=[]
    if markers:emit('output',name,'\n'.join(markers)+'\n')
    failed=any(leaf==name or leaf.startswith(name+'/') for leaf in red) and mode!='survived'
    if mode=='unrelated' and name==tests[-1] or mode=='unrelated_mutant' and mutant and name==tests[0]:failed=True
    skip=name in command['allowed_skips'] or (mode=='skip' and name==tests[-1]) or (mode=='skip_wire' and name==wire)
    emit('skip' if skip else 'fail' if failed else 'pass',name)
failed=bool(red) and mode!='survived' or mode=='unrelated' or (mode=='package_fail' and command is data['commands']['baseline'])
emit('fail' if failed else 'pass');sys.exit(1 if failed else 0)
'''


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class Fixture:
    def __init__(self, family="ldap", mode="valid", alteration=None, variant_inputs=None):
        # The workspace filesystem permits scripts; /tmp may be mounted noexec.
        # No automatic finalizer may erase evidence with unobserved children.
        self.base = Path(tempfile.mkdtemp(prefix=".iam-fake-", dir=ROOT))
        self.process = None
        self.custody = True
        self.timed_out = False
        self.mode = mode
        self.producer_marker = self.base / "producer-entry"
        self.repo = self.base / "checkout"
        self.repo.mkdir()
        self.bin = self.base / "bin"
        self.bin.mkdir()
        # The runner and any descendant only reach these approved executables.
        # Even deleting its hosted guard cannot expose the real compiler/network.
        for name in ("git", "bash", "python3"):
            (self.bin / name).symlink_to(shutil.which(name))
        self.env = {"PATH": str(self.bin), "HOME": str(self.base), "LC_ALL": "C.UTF-8",
                    "PYTHONDONTWRITEBYTECODE": "1", "GIT_CONFIG_NOSYSTEM": "1",
                    "GIT_CONFIG_GLOBAL": "/dev/null"}
        self.family = family
        data = json.loads((ROOT / ("ci/iam-qualification/" + family + ".json")).read_text())
        names = set(data["inputs"]) | {m["patch"] for m in data["mutants"]}
        names |= {"scripts/ci-iam-qualification.py", "scripts/lib/preverify-capture.py"}
        for name in names:
            target = self.repo / name
            target.parent.mkdir(parents=True, exist_ok=True)
            source = variant_inputs / name if variant_inputs is not None and name in data["inputs"] else ROOT / name
            target.write_bytes(source.read_bytes())
        (self.repo / "scripts/with-pg-env.sh").write_text('#!/bin/bash\nexec "$@"\n')
        (self.repo / "go.work").write_text("go 1.26.6\n")
        for command in data["commands"].values():
            command["wall_seconds"] = 1
        data["maximum_phase_seconds"] = len(data["commands"]) + 2 * len(data["mutants"])
        if alteration == "patch_drift":
            (self.repo / data["mutants"][0]["patch"]).write_text("different patch\n")
        if alteration == "input_drift":
            (self.repo / data["production"][0]).write_text("different source\n")
        if alteration == "owner_drift":
            owner = next(iter(data["test_owners"].values()))
            with (self.repo / owner).open("a") as output:
                output.write("\n// unknown oracle bytes\n")
        if alteration == "missing_owner_pin":
            del data["inputs"][next(iter(data["test_owners"].values()))]
        if alteration == "missing_owner":
            del data["test_owners"][next(iter(data["test_owners"]))]
        # Public-boundary controls: one exact digest per input, no provenance keys.
        if alteration == "wrong_input_pin":
            data["inputs"][data["production"][0]] = "0" * 64
        if alteration == "legacy_pin_pairs":
            data["inputs"] = {p: {"source": pin, "public": pin} for p, pin in data["inputs"].items()}
        if alteration == "provenance_key":
            data["source_head"] = "0" * 40
        if alteration == "command_provenance_key":
            data["commands"]["baseline"]["source_head"] = "0" * 40
        if alteration == "mutant_provenance_key":
            data["mutants"][0]["source_head"] = "0" * 40
        if alteration == "missing_input":
            (self.repo / next(p for p in data["inputs"] if p not in data["production"])).unlink()
        case = self.repo / ("ci/iam-qualification/" + family + ".json")
        case.write_text(json.dumps(data))
        if alteration == "remove_missing_guard":
            runner = self.repo / "scripts/ci-iam-qualification.py"
            runner.write_text(runner.read_text().replace(
                'if package_start is None or set(terminal) != expected or not package_terminal:',
                'if package_start is None or not package_terminal:'))
        if alteration == "remove_package_guard":
            runner = self.repo / "scripts/ci-iam-qualification.py"
            runner.write_text(runner.read_text().replace(
                'if rc != 0 or package_terminal != "pass":', 'if False:'))
        if alteration in ("stop_in_restore", "remove_phase_stop_guards"):
            runner = self.repo / "scripts/ci-iam-qualification.py"
            before = '        self.restored = True\n\n    def run(self):'
            after = '        self.restored = True\n        os.kill(os.getpid(), signal.SIGTERM)\n\n    def run(self):'
            self.assert_replacement(runner, before, after)
            if alteration == "remove_phase_stop_guards":
                for stage in ("interrupted-between-phases", "interrupted-before-capture"):
                    source = runner.read_text()
                    start = source.index("    def phase(")
                    tail = source[start:].replace(
                        '        require(not self.interrupted, "' + stage + '")\n', "", 1)
                    runner.write_text(source[:start] + tail)
        if alteration in ("stop_at_handoff", "remove_handoff_transfer"):
            runner = self.repo / "scripts/ci-iam-qualification.py"
            self.assert_replacement(runner, '        self.custody = False\n        try:\n',
                                    '        os.kill(os.getpid(), signal.SIGTERM)\n'
                                    '        self.custody = False\n        try:\n')
            self.assert_replacement(runner, 'def producer(family, name):\n',
                                    'def producer(family, name):\n    Path(' +
                                    repr(str(self.producer_marker)) + ').write_text("launched\\n")\n')
        if alteration in ("remove_handoff_transfer", "default_capture_owner"):
            self.assert_replacement(self.repo / "scripts/ci-iam-qualification.py",
                                    'name], cancellation=self)', 'name])')
        self.git("init", "-q")
        self.git("config", "user.name", "Synthetic fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture base")
        self.git("branch", "-M", "main")
        self.git("checkout", "-qb", "head")
        (self.repo / "head-marker").write_text("head\n")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture head")
        pr_head = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", "-q", "main")
        self.git("merge", "--no-ff", "-qm", "fixture merge", "head")
        self.head = self.git("rev-parse", "HEAD").strip()
        self.mutated = {}
        originals = {p: (self.repo / p).read_bytes() for p in data["production"]}
        if alteration not in ("patch_drift", "input_drift"):
            for mutant in data["mutants"]:
                self.git("apply", str(self.repo / mutant["patch"]))
                fingerprint = hashlib.sha256(b"".join((self.repo / p).read_bytes()
                                                     for p in data["production"])).hexdigest()
                self.mutated[fingerprint] = mutant["id"]
                for p, raw in originals.items():
                    (self.repo / p).write_bytes(raw)
        config = self.base / "fake.json"
        self.controller = self.base / "controller.pid"
        self.ready = self.base / "fake-ready"
        config.write_text(json.dumps({"family": family, "mode": mode, "mutated": self.mutated,
                                      "controller": str(self.controller), "ready": str(self.ready)}))
        fake = FAKE_GO.replace("#!PYTHON", "#!" + sys.executable, 1).replace("CONFIG", repr(str(config)))
        (self.bin / "go").write_text(fake)
        (self.bin / "go").chmod(0o755)
        payload = {"number": 1, "pull_request": {"merge_commit_sha": self.head,
            "head": {"sha": pr_head}, "base": {"ref": "main", "repo": {"full_name": "olivaresai/olivares"}}}}
        if alteration == "wrong_pr_head":
            payload["pull_request"]["head"]["sha"] = "f" * 40
        if alteration == "wrong_base_repo":
            payload["pull_request"]["base"]["repo"]["full_name"] = "example/fork"
        event = self.base / "event.json"
        event.write_text(json.dumps(payload))
        runner_temp = self.base / "runner"
        runner_temp.mkdir()
        self.env.update(GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted",
                        GITHUB_REPOSITORY="olivaresai/olivares", GITHUB_EVENT_NAME="pull_request",
                        GITHUB_WORKSPACE=str(self.repo), GITHUB_SHA=self.head,
                        GITHUB_EVENT_PATH=str(event), GITHUB_REF="refs/pull/1/merge",
                        GITHUB_RUN_ID="1", GITHUB_RUN_ATTEMPT="1", RUNNER_TEMP=str(runner_temp),
                        OLIVARES_TEST_POSTGRES_DSN="postgres://synthetic:fixture-password@invalid/db")
        self.evidence = runner_temp / ("iam-qualification-" + family)
        if alteration == "wrong_head":
            self.env["GITHUB_SHA"] = "0" * 40
        if alteration == "goflags":
            self.env["GOFLAGS"] = "-run=NoSuchTest"
        if alteration == "local":
            self.env.pop("GITHUB_ACTIONS")
        if alteration == "wrong_event":
            self.env["GITHUB_EVENT_NAME"] = "workflow_dispatch"

    def git(self, *args):
        result = subprocess.run([str(self.bin / "git"), *args], cwd=self.repo, env=self.env,
                                capture_output=True, text=True, timeout=10)
        if result.returncode:
            raise RuntimeError("synthetic git setup failed: " + result.stderr)
        return result.stdout

    @staticmethod
    def assert_replacement(path, before, after):
        source = path.read_text()
        if source.count(before) != 1:
            raise RuntimeError("synthetic barrier source drift")
        path.write_text(source.replace(before, after))

    def run(self):
        command = [sys.executable, "-B", "scripts/ci-iam-qualification.py", self.family]
        process = self.process = subprocess.Popen(command, cwd=self.repo, env=self.env,
                                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.custody = False
        self.controller.write_text(str(process.pid))
        timeout = 35
        if self.mode == "bench_timeout":
            until = time.monotonic() + 5
            while not self.ready.exists() and process.poll() is None and time.monotonic() < until:
                time.sleep(0.005)
            timeout = 0.01
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            self.timed_out = True
            # Only this Popen owns the PID. Its runner owns the anchor and must
            # report descendant custody; waiting for this PID alone is not proof.
            process.terminate()
            try:
                stdout, stderr = process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                try:
                    stdout, stderr = process.communicate(timeout=2)
                except subprocess.TimeoutExpired:
                    stdout, stderr = "", "synthetic controller custody unobserved\n"
        path = self.evidence / "RESULT.json"
        record = json.loads(path.read_text()) if path.exists() else None
        self.custody = process.returncode is not None and bool(record and record["custody_terminal"])
        receipt = {"controller_pid": process.pid, "controller_rc": process.returncode,
                   "timeout_expired": self.timed_out, "custody_terminal": self.custody}
        (self.base / "FIXTURE-CUSTODY.json").write_text(json.dumps(receipt, indent=2) + "\n")
        result = subprocess.CompletedProcess(command, 2 if self.timed_out or not self.custody
                                             else process.returncode, stdout, stderr)
        return result, record

    def close(self):
        if not self.custody:
            raise RuntimeError("synthetic custody unobserved; retained " + str(self.base))
        if self.timed_out:
            # Preserve incomplete observations even after terminal reaping.
            parent = Path(os.environ.get("IAM_FAKE_EVIDENCE", tempfile.gettempdir()))
            parent.mkdir(parents=True, exist_ok=True)
            retained = parent / self.base.name
            retained.mkdir()
            shutil.copy2(self.base / "FIXTURE-CUSTODY.json", retained)
            if self.evidence.exists():
                shutil.copytree(self.evidence, retained / "evidence")
        shutil.rmtree(self.base)


class CommandControls(unittest.TestCase):
    def run_case(self, mode="valid", alteration=None, expected=2, family="ldap"):
        fixture = Fixture(family, mode, alteration)
        try:
            result, record = fixture.run()
            self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
            if record is not None:
                self.assertEqual(record["outcome"], expected)
                self.assertTrue(record["custody_terminal"])
            return copy.deepcopy(record)
        finally:
            fixture.close()

    def test_registered_families_pass(self):
        for family in IAM.FAMILIES:
            with self.subTest(family=family):
                record = self.run_case(expected=0, family=family)
                self.assertTrue(record["restored"])
                self.assertFalse(record["owned_tree_retained"])
                self.assertEqual(len(record["phases"]), {"scim":18,"workspace":11,"ldap":8}[family])

    def test_admission(self):
        for change in ("wrong_head", "goflags", "local", "patch_drift", "input_drift",
                       "owner_drift", "missing_owner_pin", "missing_owner"):
            with self.subTest(change=change):
                record = self.run_case(alteration=change)
                self.assertEqual(record["phases"], [])

    def test_public_input_and_event_binding(self):
        # Each refusal names its own check, so a weaker check cannot pass as another.
        reasons = {"wrong_input_pin": "case-input-drift", "legacy_pin_pairs": "case-input-pin",
                   "provenance_key": "case-schema", "command_provenance_key": "case-schema",
                   "mutant_provenance_key": "case-schema", "missing_input": "missing-input",
                   "wrong_event": "public-pr-only", "wrong_pr_head": "pr-parent-identity",
                   "wrong_base_repo": "pr-event-identity"}
        for change, reason in reasons.items():
            with self.subTest(change=change):
                record = self.run_case(alteration=change)
                self.assertEqual(record["reason"], reason)
                self.assertEqual(record["phases"], [])

    def test_named_wire_leaf_is_required(self):
        # A wire case that is absent or skipped is never credited to the family.
        for mode, reason in (("missing_wire", "missing-terminal"), ("skip_wire", "skip-contract")):
            with self.subTest(mode=mode):
                record = self.run_case(mode)
                self.assertEqual([(p["label"], p["reason"]) for p in record["phases"]],
                                 [("baseline", reason)])

    def test_unregistered_family_refuses_at_entry(self):
        # Only a family with its own reviewed registry runs; no other is reported.
        self.assertEqual(IAM.FAMILIES, ("ldap",))
        for family in ("scim", "workspace"):
            with self.subTest(family=family):
                fixture = Fixture()
                try:
                    result = subprocess.run([sys.executable, "-B", "scripts/ci-iam-qualification.py", family],
                                            cwd=fixture.repo, env=fixture.env, capture_output=True,
                                            text=True, timeout=10)
                    self.assertEqual(result.returncode, 2)
                    self.assertIn("stage=entry-unavailable", result.stderr)
                    self.assertFalse((fixture.evidence.parent / ("iam-qualification-" + family)).exists())
                finally:
                    fixture.close()

    def test_protocol_inability(self):
        for mode in ("missing", "skip", "compiler", "compiler_mutant", "package_fail", "wrong_assertion", "unrelated_mutant"):
            with self.subTest(mode=mode):
                self.run_case(mode)

    def test_measured_failures(self):
        self.run_case("unrelated", expected=1)  # clean real assertion, not mutant kill
        self.run_case("survived", expected=1)

    def test_drift_and_failed_restore(self):
        for mode in ("drift", "restore"):
            with self.subTest(mode=mode):
                record = self.run_case(mode)
                self.assertTrue(record["owned_tree_retained"])

    def test_timeout_custody(self):
        record = self.run_case("timeout")
        self.assertEqual(record["phases"][0]["reason"], "capture-incomplete")
        self.assertFalse(record["owned_tree_retained"])

    def test_external_cancellation_stops_successors(self):
        record = self.run_case("cancel_mutant")
        self.assertEqual(record["reason"], "interrupted")
        self.assertEqual(record["phases"][-1]["label"], "m0-mutated")
        self.assertIn("capture=interrupted", record["phases"][-1]["capture"])
        self.assertTrue(record["restored"])
        self.assertFalse(record["owned_tree_retained"])

    def test_stop_during_restore_stops_successors(self):
        record = self.run_case(alteration="stop_in_restore")
        self.assertEqual(record["reason"], "interrupted")
        self.assertEqual(record["phases"][-1]["label"], "m0-mutated")
        self.assertTrue(record["restored"])
        self.assertFalse(record["owned_tree_retained"])

    def test_stop_at_phase_entry_refuses(self):
        qualification = IAM.Qualification("ldap", Path("unused-no-write"))
        qualification.stop()
        with self.assertRaisesRegex(IAM.Inability, "interrupted-between-phases"):
            qualification.phase("baseline", "baseline")
        self.assertTrue(qualification.custody)
        self.assertEqual(qualification.phases, [])

    def test_stop_at_capture_handoff_refuses_producer(self):
        fixture = Fixture(alteration="stop_at_handoff")
        try:
            result, record = fixture.run()
            self.assertFalse(fixture.producer_marker.exists(), "producer launched after observed stop")
            self.assertEqual(result.returncode, 2)
            self.assertEqual(record["reason"], "interrupted")
            self.assertTrue(record["custody_terminal"])
            self.assertTrue(record["restored"])
            self.assertFalse(record["owned_tree_retained"])
            self.assertEqual([p["label"] for p in record["phases"]], ["baseline"])
            self.assertIn("producer=unknown capture=interrupted", record["phases"][0]["capture"])
        finally:
            fixture.close()

    def test_handoff_transfer_is_causal(self):
        fixture = Fixture(alteration="remove_handoff_transfer")
        try:
            result, record = fixture.run()
            self.assertTrue(fixture.producer_marker.exists(), "weakened transfer did not launch")
            self.assertEqual(result.returncode, 2)  # a final refusal cannot undo launch
            self.assertEqual(record["reason"], "interrupted")
            self.assertTrue(record["custody_terminal"])
            self.assertFalse(record["owned_tree_retained"])
            self.assertEqual(record["phases"][0]["outcome"], 0)
        finally:
            fixture.close()

    def test_default_capture_owner_keeps_protocol(self):
        record = self.run_case(alteration="default_capture_owner", expected=0)
        self.assertEqual(len(record["phases"]), 8)
        self.assertTrue(record["restored"])
        self.assertFalse(record["owned_tree_retained"])

    def test_phase_stop_guards_are_causal(self):
        record = self.run_case(alteration="remove_phase_stop_guards")
        self.assertEqual(record["phases"][-1]["label"], "m0-restored")

    def test_bench_timeout_reaps_and_retains(self):
        fixture = Fixture(mode="bench_timeout")
        try:
            result, record = fixture.run()
            self.assertTrue(fixture.ready.exists(), "fake child never reached timeout barrier")
            self.assertTrue(fixture.timed_out)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(record["reason"], "interrupted")
            self.assertTrue(record["custody_terminal"])
            self.assertTrue(record["restored"])
            self.assertFalse(record["owned_tree_retained"])
            self.assertIsNotNone(fixture.process.returncode)
        finally:
            fixture.close()
        retained = Path(os.environ.get("IAM_FAKE_EVIDENCE", tempfile.gettempdir())) / fixture.base.name
        self.assertTrue((retained / "FIXTURE-CUSTODY.json").is_file())
        self.assertTrue((retained / "evidence/RESULT.json").is_file())
        self.assertFalse(fixture.base.exists())

    def test_unknown_custody_preserves_fixture(self):
        fixture = Fixture()
        try:
            # No process is launched: simulate an unobserved custody receipt.
            fixture.custody = False
            with self.assertRaisesRegex(RuntimeError, "custody unobserved"):
                fixture.close()
            self.assertTrue(fixture.base.exists())
        finally:
            fixture.custody = True  # this control never launched a process
            fixture.close()

    def test_custody_guard_is_causal(self):
        source = Path(__file__).read_text().split("    def close(self):\n", 1)[1]
        source = textwrap.dedent("    def close(self):\n" +
                                 source.split("\n\nclass CommandControls", 1)[0])
        guard = ('    if not self.custody:\n'
                 '        raise RuntimeError("synthetic custody unobserved; retained " + str(self.base))\n')
        self.assertEqual(source.count(guard), 1)
        scope = dict(globals())
        exec(compile(source.replace(guard, ""), "synthetic-custody-guard-removal", "exec"), scope)
        fixture = Fixture()
        try:
            fixture.custody = False  # no child launched in this causal control
            scope["close"](fixture)
            self.assertFalse(fixture.base.exists())
        finally:
            if fixture.base.exists():
                fixture.custody = True
                fixture.close()

    def test_required_artifact(self):
        fixture = Fixture(mode="missing_artifact")
        try:
            result, record = fixture.run()
            self.assertEqual(result.returncode, 2)
            self.assertEqual(record["outcome"], 2)
            self.assertEqual(record["reason"], "required-artifact-missing")
            self.assertTrue((fixture.evidence / "SHA256.json").exists())
        finally:
            fixture.close()

    def test_secret_redaction(self):
        fixture = Fixture(mode="secret")
        try:
            result, _ = fixture.run()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            for path in fixture.evidence.rglob("*"):
                if path.is_file():
                    self.assertNotIn(b"fixture-password", path.read_bytes())
            self.assertIn("[REDACTED_DSN]", (fixture.evidence / "baseline/stderr.txt").read_text())
        finally:
            fixture.close()

    def test_missing_terminal_guard_is_causal(self):
        # Original rejects; removing precisely its terminal census admits the
        # same synthetic missing clean case. Stop at baseline via a unit oracle
        # too, since later mutant parent failures remain an independent gate.
        command = json.loads((ROOT / "ci/iam-qualification/ldap.json").read_text())["commands"]["baseline"]
        events = [{"Action":"start","Package":command["package"]}]
        for test in command["expected_tests"][:-1]:
            events += [{"Action":"run","Package":command["package"],"Test":test},
                       {"Action":"skip" if test in command["allowed_skips"] else "pass",
                        "Package":command["package"],"Test":test}]
        events.append({"Action":"pass","Package":command["package"]})
        raw = "\n".join(json.dumps(event) for event in events)
        self.assertEqual(IAM.grade(command, raw, 0)[0], 2)
        source = (ROOT / "scripts/ci-iam-qualification.py").read_text().replace(
            'if package_start is None or set(terminal) != expected or not package_terminal:',
            'if package_start is None or not package_terminal:')
        scope = {"__file__":str(ROOT / "scripts/ci-iam-qualification.py"),"__name__":"mutant"}
        exec(compile(source, "synthetic-guard-removal", "exec"), scope)
        self.assertEqual(scope["grade"](command, raw, 0)[0], 0)
        self.run_case("missing", "remove_missing_guard", expected=0)

    def test_package_exit_guard_is_causal(self):
        fixture = Fixture(mode="package_fail", alteration="remove_package_guard")
        try:
            result, record = fixture.run()
            # The same terminal package fail/nonzero is now falsely green at
            # baseline. Subsequent mutation gates are deliberately not weakened.
            self.assertEqual(record["phases"][0]["outcome"], 0)
            self.assertEqual(result.returncode, 0)
        finally:
            fixture.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
