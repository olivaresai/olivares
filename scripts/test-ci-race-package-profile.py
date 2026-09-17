#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Finite tests for scripts/ci-race-package-profile.py. Six groups. No product suites."""

from __future__ import annotations

import ast
import contextlib
import copy
import io
import unittest
from unittest import mock
import importlib.util
import json
import os
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
INST_PATH = Path(os.environ.get("RPP_INSTRUMENT_PATH", ROOT / "scripts/ci-race-package-profile.py"))
WF = Path(os.environ.get("RPP_WORKFLOW_PATH", ROOT / ".github/workflows/race-package-profile.yml"))
MAIN = ROOT / ".github/workflows/mainline-ci.yml"
RELEASE = ROOT / ".github/workflows/release.yml"
RACE_FULL = ROOT / ".github/workflows/race-full.yml"

pass_n = 0
fail_n = 0
skip_n = 0


def could_not_look(msg):
    print(f"test-ci-race-package-profile: NO HE PODIDO MIRAR — {msg}", file=sys.stderr)
    raise SystemExit(2)


def check(label, ok):
    global pass_n, fail_n
    if ok:
        pass_n += 1
        print(f"  ok    {label}")
    else:
        fail_n += 1
        print(f"  FAIL  {label}")


def load_inst():
    spec = importlib.util.spec_from_file_location("ci_race_package_profile", INST_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def pick_exec_dir():
    for base in (os.environ.get("RPP_EVIDENCE_DIR"), os.environ.get("RUNNER_TEMP"), os.environ.get("TMPDIR"), "/workspace/.olivares-tmptest", "/tmp"):
        if not base:
            continue
        try:
            os.makedirs(base, exist_ok=True)
            d = tempfile.mkdtemp(prefix="rpp.", dir=base)
        except OSError:
            continue
        probe = Path(d) / "probe"
        probe.write_text("#!/bin/sh\nexit 0\n")
        probe.chmod(probe.stat().st_mode | stat.S_IXUSR)
        r = subprocess.run([str(probe)], check=False)
        if r.returncode == 0:
            probe.unlink()
            return Path(d)
        print(f"retained override fixture: {d}")
    return None


def write_exec(path, body):
    path.write_text(body)
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def main():
    if not INST_PATH.is_file():
        could_not_look(f"instrument absent: {INST_PATH}")
    for p in (WF, MAIN, RELEASE, RACE_FULL):
        if not p.is_file():
            could_not_look(f"absent {p}")
    work = pick_exec_dir()
    if work is None:
        could_not_look("no executable work directory")
    try:
        inst = load_inst()
        group1_closed_matrix(inst)
        group3_adapter(inst, work)
        group4_parser(inst, work)
        if "--microfixture" in sys.argv:
            group6_microfixture(inst, work)
    finally:
        print(f"retained fixture artifacts: {work}")
    print()
    print(f"test-ci-race-package-profile: {pass_n} passed, {fail_n} failed, {skip_n} skipped")
    return 1 if fail_n else 0


def group1_closed_matrix(inst):
    print("group 1 — closed matrix and consumers")
    pkgs = inst.ALLOWED_PACKAGES
    check("exactly three closed packages", len(pkgs) == 3)
    check(
        "keys are core/api, core/auth, core/internal/store/sqlstore",
        set(pkgs) == {"core/api", "core/auth", "core/internal/store/sqlstore"},
    )
    check("go_dir for api is ./api", pkgs["core/api"]["go_dir"] == "./api")
    check("go_dir for auth is ./auth", pkgs["core/auth"]["go_dir"] == "./auth")
    check(
        "go_dir for sqlstore is ./internal/store/sqlstore",
        pkgs["core/internal/store/sqlstore"]["go_dir"] == "./internal/store/sqlstore",
    )
    src = INST_PATH.read_text(encoding="utf-8")
    check("instrument does not contain go list ./...", "./..." not in src.split("ALLOWED_PACKAGES", 1)[-1] or "go list ./..." not in src)
    check("instrument does not invoke go list ./...", "go list ./..." not in src and "go-work-each" not in src)
    tree = ast.parse(src)
    imported = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            imported.extend(a.name.split(".")[0] for a in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            imported.append(node.module.split(".")[0])
    std = {
        "argparse",
        "ast",
        "collections",
        "datetime",
        "errno",
        "hashlib",
        "json",
        "os",
        "pathlib",
        "re",
        "shlex",
        "shutil",
        "signal",
        "stat",
        "subprocess",
        "sys",
        "tempfile",
        "threading",
        "time",
        "typing",
        "__future__",
    }
    extra = sorted(set(imported) - std)
    check(f"instrument imports only the standard library {extra}", extra == [])
    wf = WF.read_text(encoding="utf-8")
    check("workflow is workflow_dispatch only", "pull_request:" not in wf and "\n  push:" not in wf and "schedule:" not in wf)
    check("workflow name is not race-full", "name: race-full" not in wf)
    check("max-parallel: 1", "max-parallel: 1" in wf)
    check("fail-fast: false", "fail-fast: false" in wf)
    check("job timeout 175", "timeout-minutes: 175" in wf)
    check("measure step timeout 90", "timeout-minutes: 90" in wf)
    check("go timeout is 60m inside the instrument", '"-timeout=60m"' in src or "'-timeout=60m'" in src)
    check("helper resolve is called", "ci-postgres-service.sh resolve" in wf)
    check("helper provision is called", "ci-postgres-service.sh provision" in wf)
    artifacts = re.findall(r"artifact: (\S+)", wf)
    packages = re.findall(r"package: (\S+)", wf)
    check("matrix artifacts are the three ids", artifacts == ["core-api", "core-auth", "core-sqlstore"])
    check(
        "matrix packages are the three paths",
        packages == ["core/api", "core/auth", "core/internal/store/sqlstore"],
    )
    check("no ./... in the profile workflow", "./..." not in wf)
    check("no -short in the profile workflow", "-short" not in wf)
    check("no -run filter in the profile workflow", "-run" not in wf)
    rel = RELEASE.read_text(encoding="utf-8")
    rf = RACE_FULL.read_text(encoding="utf-8")
    check("release.yml does not name race-package-profile", "race-package-profile" not in rel)
    check("race-full.yml does not name race-package-profile", "race-package-profile" not in rf)
    check("mainline-ci race-rest calls the helper", "ci-postgres-service.sh resolve" in MAIN.read_text(encoding="utf-8"))
    out = Path(tempfile.mkdtemp(prefix="rpp-prep.", dir=str(pick_exec_dir() or "/tmp")))
    try:
        rc = inst.cmd_run("./...", str(out / "x"))
        check("run refuses ./...", rc == inst.RC_PREPARE)
        rc = inst.cmd_run("core/runtime", str(out / "y"))
        check("run refuses a fourth module", rc == inst.RC_PREPARE)
    finally:
        print(f"retained selection fixture: {out}")
    env_save = os.environ.get("GOFLAGS")
    os.environ["GOFLAGS"] = "-run=TestFoo"
    try:
        d = Path(tempfile.mkdtemp(prefix="rpp-flags."))
        rc = inst.cmd_run("core/api", str(d))
        check("incompatible GOFLAGS is prepare, zero tests", rc == inst.RC_PREPARE)
        exit_obj = json.loads((d / "exit.json").read_text())
        check("GOFLAGS prepare does not start go", exit_obj.get("go_started") is False)
    finally:
        if env_save is None:
            os.environ.pop("GOFLAGS", None)
        else:
            os.environ["GOFLAGS"] = env_save
        print(f"retained override fixture: {d}")
    check("instrument never calls go env GOMAXPROCS", "go env GOMAXPROCS" not in src)
    saved_gmp = os.environ.pop("GOMAXPROCS", None)
    try:
        gmp = inst.gomaxprocs_inherited()
        check("unset GOMAXPROCS is unset, not 0", gmp == {"state": "unset"})
        os.environ["GOMAXPROCS"] = "4"
        gmp = inst.gomaxprocs_inherited()
        check(
            "positive inherited GOMAXPROCS is recorded as inherited, not inferred",
            gmp == {"state": "inherited_positive", "value": "4"},
        )
    finally:
        if saved_gmp is None:
            os.environ.pop("GOMAXPROCS", None)
        else:
            os.environ["GOMAXPROCS"] = saved_gmp


def group3_adapter(inst, work):
    print("group 3 — exec adapter")
    out = work / "exec1"
    out.mkdir()
    child = work / "child.sh"
    counter = work / "child.launches"
    write_exec(
        child,
        f"""#!/bin/sh
echo launch >> "{counter}"
echo STDOUT-LINE
echo STDERR-LINE >&2
exit 7
""",
    )
    rc = inst.cmd_exec(str(out), [str(child)])
    check("exec preserves nonzero child RC", rc == 7)
    ident = json.loads((out / "binary.identity.json").read_text())
    check("exec records a direct child PID", isinstance(ident.get("pid"), int) and ident["pid"] > 0)
    check("child PID is not the supervisor", ident.get("pid") != os.getpid())
    check("stdout bytes preserved", (out / "binary.stdout.raw").read_text() == "STDOUT-LINE\n")
    check("stderr bytes preserved", (out / "binary.stderr.raw").read_text() == "STDERR-LINE\n")
    check("single launch", counter.read_text().count("launch") == 1)
    check("argv is the fixture, not reconstructed", ident.get("argv") == [str(child)])

    out2 = work / "exec-term"
    out2.mkdir()
    sleepy = work / "sleepy.sh"
    write_exec(
        sleepy,
        """#!/bin/sh
trap 'echo GOT-TERM >&2; exit 143' TERM
# Keep a child alive until signalled.
while :; do sleep 0.1; done
""",
    )
    proc = subprocess.Popen(
        [sys.executable, str(INST_PATH), "exec", "--out", str(out2), "--", str(sleepy)],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    time.sleep(0.4)
    proc.send_signal(signal.SIGTERM)
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)
        check("signal/cancel returned (killed after timeout)", False)
        return
    err = (out2 / "binary.stderr.raw").read_text() if (out2 / "binary.stderr.raw").is_file() else ""
    ident2 = json.loads((out2 / "binary.identity.json").read_text()) if (out2 / "binary.identity.json").is_file() else {}
    check("signal path preserved GOT-TERM or a signal cause", "GOT-TERM" in err or ident2.get("signal") is not None or proc.returncode != 0)
    check("cancel/signal is not a silent success", proc.returncode != 0)


def group4_parser(inst, work):
    print("group 4 — parser")
    pass_json = """
{"Action":"start","Package":"example.com/p"}
{"Action":"run","Package":"example.com/p","Test":"TestParent"}
{"Action":"run","Package":"example.com/p","Test":"TestParent/parallel"}
{"Action":"pause","Package":"example.com/p","Test":"TestParent/parallel"}
{"Action":"cont","Package":"example.com/p","Test":"TestParent/parallel"}
{"Action":"pass","Package":"example.com/p","Test":"TestParent/parallel","Elapsed":0.05}
{"Action":"run","Package":"example.com/p","Test":"TestParent/skip-real"}
{"Action":"output","Package":"example.com/p","Test":"TestParent/skip-real","Output":"    fixture.go:3: optional producer not in this increment\\n"}
{"Action":"skip","Package":"example.com/p","Test":"TestParent/skip-real","Elapsed":0}
{"Action":"pass","Package":"example.com/p","Test":"TestParent","Elapsed":0.06}
{"Action":"pass","Package":"example.com/p","Elapsed":0.07}
""".lstrip()
    events, errors = inst.parse_json_stream(pass_json)
    parsed = inst.summarize_events(events, errors)
    check("PASS fixture has no parse errors", errors == [])
    check("parent is top_level", any(t["test"] == "TestParent" and t["kind"] == "top_level" for t in parsed["tests"]))
    check("parallel child is a subtest", any(t["test"] == "TestParent/parallel" and t["kind"] == "subtest" for t in parsed["tests"]))
    check("SKIP is recorded with a real reason", any(t["terminal"] == "skip" and t["skip_reason"] for t in parsed["tests"]))
    check("top_level and subtests are separate counters", parsed["counters"]["top_level"] == 1 and parsed["counters"]["subtests"] == 2)
    check("package PASS is not censored", parsed["censored"] is False)
    check("elapsed is not summed across parallel tests as a CPU figure", parsed["packages"][0]["elapsed"] == 0.07)

    epi = """
{"Action":"start","Package":"example.com/p"}
{"Action":"run","Package":"example.com/p","Test":"TestOk"}
{"Action":"pass","Package":"example.com/p","Test":"TestOk","Elapsed":0.01}
{"Action":"fail","Package":"example.com/p","Elapsed":0.02}
""".lstrip()
    events, errors = inst.parse_json_stream(epi)
    parsed = inst.summarize_events(events, errors)
    check("package FAIL with tests PASS is epilogue_nonzero", parsed["epilogue_nonzero"] is True)
    check("epilogue FAIL is not a complete false PASS", parsed["tests_all_observed_pass"] is True and parsed["any_fail"] is True)

    panic = """
{"Action":"start","Package":"example.com/p"}
{"Action":"run","Package":"example.com/p","Test":"TestBoom"}
{"Action":"output","Package":"example.com/p","Test":"TestBoom","Output":"panic: boom\\n"}
{"Action":"fail","Package":"example.com/p","Elapsed":0.01}
""".lstrip()
    events, errors = inst.parse_json_stream(panic)
    parsed = inst.summarize_events(events, errors)
    check("panic leaves the test without a terminal", parsed["censored"] is True)
    check("started_without_terminal names TestBoom", parsed["counters"]["started_without_terminal"] == 1)

    trunc = '{"Action":"start","Package":"example.com/p"}\n{"Action":"run","Package":"example.com/p","Test":"TestX"\n'
    events, errors = inst.parse_json_stream(trunc)
    parsed = inst.summarize_events(events, errors)
    check("truncated JSON is an error, not dropped", len(errors) == 1 and errors[0]["kind"] == "invalid_json")
    check("truncated stream is not a false complete PASS", parsed["tests_all_observed_pass"] is False)

    build = """
{"ImportPath":"example.com/p","Action":"build-output","Output":"# example.com/p\\n"}
{"Action":"start","Package":"example.com/p"}
{"Action":"run","Package":"example.com/p","Test":"TestX"}
{"Action":"pass","Package":"example.com/p","Test":"TestX","Elapsed":0.01}
{"Action":"pass","Package":"example.com/p","Elapsed":0.02}
""".lstrip()
    events, errors = inst.parse_json_stream(build)
    parsed = inst.summarize_events(events, errors)
    check("Go 1.26 build-output is counted as a build event", parsed["counters"]["build_events"] == 1)
    check("build events are not tests", parsed["counters"]["top_level"] == 1)

    cited = (
        "not a sched line\n"
        "    SCHED 0ms: gomaxprocs=2 idleprocs=2 threads=3 spinningthreads=0 needspinning=0 idlethreads=1 runqueue=0 [ 0 0 ]\n"
        't.Fatal output SCHED 5ms: gomaxprocs=2 idleprocs=1 threads=3 spinningthreads=0 needspinning=0 idlethreads=1 runqueue=1 [ 0 1 ]\n'
        "SCHED 0ms: gomaxprocs=8 idleprocs=8 threads=5 spinningthreads=0 needspinning=0 idlethreads=4 runqueue=0 [ 0 0 0 0 0 0 0 0 ]\n"
        "SCHED 5000ms: gomaxprocs=8 idleprocs=7 threads=6 spinningthreads=0 needspinning=0 idlethreads=3 runqueue=1 [ 1 0 0 0 0 0 0 0 ]\n"
    )
    sched = inst.parse_scheduler_text(cited)
    check("indented SCHED is not parent runtime", all(r.get("gomaxprocs") == 8 for r in sched["lines"] if r.get("attributable") and "gomaxprocs" in r) or sched["gomaxprocs_observed"] == [8])
    check("direct SCHED gomaxprocs comes from stderr text, not JSON Output", sched["gomaxprocs_observed"] == [8])
    check("go env GOMAXPROCS was not used", sched["go_env_gomaxprocs_used"] is False)

    # JSON Output must not be a scheduler source.
    json_sched = """
{"Action":"output","Package":"example.com/p","Test":"TestX","Output":"SCHED 0ms: gomaxprocs=1 idleprocs=1 threads=1 spinningthreads=0 needspinning=0 idlethreads=0 runqueue=0\\n"}
{"Action":"pass","Package":"example.com/p","Test":"TestX","Elapsed":0.01}
""".lstrip()
    events, errors = inst.parse_json_stream(json_sched)
    parsed = inst.summarize_events(events, errors)
    text = "".join((e["event"].get("Output") or "") for e in events if e["event"].get("Action") == "output")
    sched_from_json = inst.parse_scheduler_text(text)
    # Contract: do not count JSON Output as parent runtime. summarize uses binary.stderr.raw only.
    empty = inst.parse_scheduler_text("")
    check("empty stderr yields no parent gomaxprocs", empty["gomaxprocs_observed"] == [])
    check("JSON Output SCHED is not mixed into empty stderr", empty["runtime_attributable"] is False)


def group6_microfixture(inst, work):
    """One real Go package through the whole corrected flow; PG is a silent local fixture."""
    print("group 6 — retained Go microfixture; no product package and no Postgres")
    root = work / "fixture-repo"
    (root / "scripts").mkdir(parents=True)
    (root / "core").mkdir()
    (root / "core/go.mod").write_text("module raceprofmicro\n\ngo 1.26.6\n")
    packages = {
        "fixture/api": {"artifact": "fixture-api", "go_dir": "./api", "import_path": "raceprofmicro/api"},
        "fixture/auth": {"artifact": "fixture-auth", "go_dir": "./auth", "import_path": "raceprofmicro/auth"},
        "fixture/store": {"artifact": "fixture-store", "go_dir": "./store", "import_path": "raceprofmicro/store"},
    }
    # go list's physical package path is checked. Use aliases pointing at the same files.
    (root / "fixture").symlink_to("core", target_is_directory=True)
    for name in ("api", "auth", "store"):
        directory = root / "core" / name
        directory.mkdir()
        (directory / "fixture.go").write_text("package " + name + "\n")
    (root / "core/api/fixture_test.go").write_text("""package api
import ("testing"; "time")
func TestParent(t *testing.T) {
    t.Run("parallel", func(t *testing.T) {t.Parallel(); time.Sleep(20*time.Millisecond)})
    t.Run("optional", func(t *testing.T) {t.Skip("finite fixture optional prerequisite")})
}
var Sink uint64
func TestCPU(t *testing.T) {
    deadline := time.Now().Add(250*time.Millisecond)
    var n uint64 = 1
    for time.Now().Before(deadline) {for i:=0; i<100000; i++ {n = n*1664525 + 1013904223}}
    Sink = n
}
""")
    write_exec(root / "scripts/ci-postgres-service.sh", "#!/bin/sh\n# local no-PG fixture\nexit 0\n")
    write_exec(root / "scripts/with-pg-env.sh", "#!/bin/sh\n# Simulated completed probe, never contacts PG.\nexport GOFLAGS=-p=2\nexec \"$@\"\n")
    evidence = work / "micro-out"
    command_receipts = work / "fixture-setup"
    command_receipts.mkdir()
    rec, _, _ = inst.run_receipt(["git", "init", "-q", str(root)], work, command_receipts, "git-init")
    check("fixture Git init succeeded", rec["rc"] == 0)
    # The fixture has no repository commit; the SHA guard's input is an explicitly mocked
    # identity. Working bytes, inventory, all executable processes and profiles are real.
    fixture_sha = "f" * 40
    original_identity = inst.git_identity
    def fixture_identity(path):
        ident = original_identity(path)
        ident.update(commit=fixture_sha, tree=None, fixture_identity=True)
        return ident
    env = {"EXPECTED_SHA": fixture_sha, "GITHUB_SHA": fixture_sha, "GOWORK": "off",
           "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "",
           "GORACE": "", "GODEBUG": "schedtrace=0", "SECRET_CANARY": "private-micro-canary"}
    saved = {k: os.environ.pop(k, None) for k in ("OLIVARES_PG_PROBE", "OLIVARES_PG_LOCAL_DEFAULTS")}
    try:
        with mock.patch.object(inst, "repo_root", return_value=root), \
             mock.patch.object(inst, "ALLOWED_PACKAGES", packages), \
             mock.patch.object(inst, "git_identity", side_effect=fixture_identity), \
             mock.patch.dict(os.environ, env):
            for phase in ("allocate", "resolve", "provision", "inventory"):
                rc = inst.cmd_run("fixture/api", evidence, phase=phase)
                check("microfixture preparation " + phase, rc == 0)
                if rc != 0:
                    return
            rc = inst.cmd_run("fixture/api", evidence)
            summary = json.loads((evidence / "summary.json").read_text())
            # This local namespace may hide CPU ancestors: never require a fabricated
            # complete resource observation just to call the mounting check successful.
            check("mount returns pass or honestly incomplete resource observation", rc in (0, inst.RC_INCOMPLETE))
            check("microfixture package actually passed", summary["tests"] == "pass")
            check("microfixture package is never a product package", summary["phases"]["package_events"] and
                  all(t["package"].startswith("raceprofmicro/") for t in inst.derive_summary(evidence)[1]["tests"]))
            go = json.loads((evidence / "go.identity.json").read_text())
            binary = json.loads((evidence / "binary.identity.json").read_text())
            inv = json.loads((evidence / "invocations.json").read_text())
            check("wrapper, Go and test have three distinct PIDs", len({inv["wrapper"]["pid"], go["pid"], binary["pid"]}) == 3)
            check("wrapper-derived GOFLAGS captured after probe", go["effective_environment"]["GOFLAGS"].get("value") == "-p=2")
            check("scheduler overrides inherited zero only for test", binary["godebug_schedtrace"] == "5000" and
                  go["effective_environment"]["GODEBUG"]["numeric_settings"].get("schedtrace") == "0")
            check("real scheduler is attributable", summary["runtime_attributable"])
            check("two offline pprof analyses succeeded", summary["cpu_profile"] == "ok")
            check("optional skip retained", summary["counters"]["skips"] == 1)
            check("one Go invocation and one test invocation", go["rc"] == 0 and binary["rc"] == 0)
            for path in evidence.rglob("*"):
                if path.is_file() and path.name not in ("package.test", "cpu.pprof"):
                    check("no canary in " + path.name, b"private-micro-canary" not in path.read_bytes())
            inst.write_json(evidence / "FIXTURE.json", {"kind": "finite Go mounting check only",
                            "mocked": ["repository commit identity", "PG provisioning", "PG probe wrapper"],
                            "real": ["working bytes", "Go list", "Go test", "test binary", "CPU profile", "pprof", "resource reads"],
                            "product_suites_run": False})
            inst.write_sha256sums(evidence)
    finally:
        for key, value in saved.items():
            if value is not None:
                os.environ[key] = value


# Regression receipts in this group are explicitly synthetic inputs to pure functions.
# They never describe a historical or real Go execution.
class CorrectionTests(unittest.TestCase):
    def setUp(self):
        self.inst = load_inst()
        base = os.environ.get('RPP_EVIDENCE_DIR')
        if base:
            Path(base).mkdir(parents=True, exist_ok=True)
        self.work = Path(tempfile.mkdtemp(prefix=self._testMethodName + '.', dir=base))

    def cgroup(self, version='v2', mount_root='/', membership='/job/leaf'):
        proc, fs = self.work / 'proc', self.work / 'fs'
        pid = proc / '10'
        pid.mkdir(parents=True)
        (pid / 'stat').write_text('10 (fixture) R 1 0 0 0 0 0 0 0 0 10 2 0 0 0 0 0 0 0 12345 0 100\n')
        (pid / 'status').write_text('Cpus_allowed_list:\t0-3\n')
        (pid / 'io').write_text('read_bytes: 100\nwrite_bytes: 10\n')
        (pid / 'schedstat').write_text('100 10 1\n')
        if version == 'v2':
            (pid / 'cgroup').write_text('0::' + membership + '\n')
            (pid / 'mountinfo').write_text(f'36 29 0:32 {mount_root} /cgroup\\040space rw - cgroup2 cgroup rw\n')
            mount = fs / 'cgroup space'
            suffix = membership[len(mount_root.rstrip('/')):].lstrip('/')
            leaf = mount / suffix
            leaf.mkdir(parents=True)
            current = leaf
            while True:
                (current / 'cpu.max').write_text('max 100000\n')
                (current / 'cpu.stat').write_text('usage_usec 100\nuser_usec 80\nsystem_usec 20\nnr_periods 3\nnr_throttled 1\nthrottled_usec 7\n')
                (current / 'cpuset.cpus.effective').write_text('0-3\n')
                (current / 'memory.max').write_text('max\n')
                (current / 'memory.current').write_text('1000\n')
                (current / 'memory.events').write_text('oom 0\noom_kill 0\n')
                for name in ('cpu.pressure', 'io.pressure', 'memory.pressure'):
                    (current / name).write_text('some avg10=0.00 avg60=0.00 avg300=0.00 total=12\n')
                if current == mount:
                    break
                current = current.parent
        else:
            (pid / 'cgroup').write_text('2:cpu,cpuacct:/batch/job/leaf\n3:cpuset:/set/job\n4:memory:/mem/job\n')
            (pid / 'mountinfo').write_text('36 29 0:32 /batch /combined rw - cgroup cgroup rw,cpu,cpuacct\n37 29 0:33 /set /sets rw - cgroup cgroup rw,cpuset\n38 29 0:34 /mem /memory rw - cgroup cgroup rw,memory\n')
            mount, leaf = fs / 'combined', fs / 'combined/job/leaf'
            leaf.mkdir(parents=True)
            for node, quota in ((mount, '-1'), (leaf.parent, '125000'), (leaf, '300000')):
                (node / 'cpu.cfs_quota_us').write_text(quota)
                (node / 'cpu.cfs_period_us').write_text('100000')
                (node / 'cpu.stat').write_text('nr_periods 3\nnr_throttled 1\nthrottled_time 1000\n')
                (node / 'cpuacct.usage').write_text('10000')
            for folder in ('sets', 'sets/job', 'memory', 'memory/job'):
                (fs / folder).mkdir(parents=True, exist_ok=True)
            (fs / 'sets/job/cpuset.effective_cpus').write_text('1')
            (fs / 'memory/job/memory.limit_in_bytes').write_text('2048')
            (fs / 'memory/memory.limit_in_bytes').write_text('1024')
        return proc, fs, leaf

    def tree(self, proc, fs):
        return self.inst.walk_cgroup(10, proc_root=str(proc), sysfs_root=str(fs))

    def test_r1_mount_ancestor_and_affinity(self):
        proc, fs, leaf = self.cgroup()
        (leaf / 'cpu.max').write_text('300000 100000\n')
        (leaf.parent / 'cpu.max').write_text('125000 100000\n')
        tree = self.tree(proc, fs)
        self.assertEqual(tree['effective']['cpus'], 1.25)
        self.assertFalse(tree['host_limit_complete'])
        (leaf / 'cpuset.cpus.effective').write_text('2\n')
        self.assertEqual(self.tree(proc, fs)['effective']['cpus'], 1)
        (proc / '10/status').write_text('Cpus_allowed_list:\t3\n')
        self.assertEqual(self.tree(proc, fs)['effective']['cpus'], 0)

    def test_r1_namespace_mount_root(self):
        proc, fs, leaf = self.cgroup(mount_root='/hidden/job', membership='/hidden/job/child')
        (leaf / 'cpu.max').write_text('75000 100000\n')
        tree = self.tree(proc, fs)
        self.assertEqual(tree['effective']['cpus'], .75)
        self.assertEqual(len(tree['ancestors']), 2)
        self.assertFalse(tree['host_limit_complete'])
        (proc / '10/cgroup').write_text('0::/incompatible\n')
        self.assertFalse(self.tree(proc, fs)['visible_limit_complete'])

    def test_r1_v1_separate_memberships(self):
        proc, fs, _ = self.cgroup(version='v1')
        tree = self.tree(proc, fs)
        self.assertEqual(tree['quota_cpus'], 1.25)
        self.assertEqual(tree['effective']['cpus'], 1)
        self.assertEqual(len(tree['hierarchies']), 3)
        self.assertTrue(any('memory.limit_in_bytes' in n for n in tree['ancestors']))

    def test_r1_absent_unreadable_unlimited(self):
        proc, fs, leaf = self.cgroup()
        self.assertEqual(self.tree(proc, fs)['ancestors'][0]['quota']['state'], 'unlimited')
        (leaf / 'cpu.max').unlink()
        self.assertEqual(self.tree(proc, fs)['ancestors'][0]['quota']['state'], 'absent')
        read = self.inst.read_text_state
        def denied(path):
            return {'state': 'unreadable'} if Path(path) == leaf / 'cpu.max' else read(path)
        with mock.patch.object(self.inst, 'read_text_state', side_effect=denied):
            tree = self.tree(proc, fs)
        self.assertEqual(tree['ancestors'][0]['quota']['state'], 'unreadable')
        self.assertFalse(tree['visible_limit_complete'])
        self.assertFalse(tree['host_limit_complete'])

    def test_r1_sampler_reset_migration_and_reuse(self):
        proc, fs, leaf = self.cgroup()
        sampler = self.inst.Sampler(self.work, 10, proc_root=str(proc), sysfs_root=str(fs))
        sampler.sample('initial')
        (leaf / 'cpu.stat').write_text('usage_usec 200\nuser_usec 160\nsystem_usec 40\nnr_periods 4\nnr_throttled 2\nthrottled_usec 9\n')
        sampler.sample()
        rows = [json.loads(l) for l in (self.work / 'resources.jsonl').read_text().splitlines()]
        self.assertEqual(rows[-1]['delta']['cgroup']['nodes'][0]['counters']['cpu.stat']['values']['usage_usec'], 100)
        (leaf / 'cpu.stat').write_text('usage_usec 1\nuser_usec 1\nsystem_usec 0\nnr_periods 1\nnr_throttled 0\nthrottled_usec 0\n')
        sampler.sample()
        rows = [json.loads(l) for l in (self.work / 'resources.jsonl').read_text().splitlines()]
        self.assertEqual(rows[-1]['delta']['cgroup']['nodes'][0]['counters']['cpu.stat']['reason'], 'counter_reset')
        (proc / '10/cgroup').write_text('0::/job\n')
        sampler.sample()
        rows = [json.loads(l) for l in (self.work / 'resources.jsonl').read_text().splitlines()]
        self.assertEqual(rows[-1]['delta']['cgroup']['reason'], 'identity_changed_or_migrated')
        old = (proc / '10/stat').read_text()
        (proc / '10/stat').write_text(old.replace('12345', '12346'))
        self.assertEqual(sampler._match(10, '12345')['reason'], 'pid_reused')

    def complete_fixture(self):
        number = getattr(self, 'summary_fixture_number', 0)
        self.summary_fixture_number = number + 1
        out = self.work / ('summary-' + str(number))
        out.mkdir()
        inst = self.inst
        package = 'github.com/olivaresai/olivares/core/api'
        stream = [{'Action': 'start', 'Package': package, 'Time': '2026-09-08T00:00:00Z'},
                  {'Action': 'run', 'Package': package, 'Test': 'TestFixture', 'Time': '2026-09-08T00:00:01Z'},
                  {'Action': 'pass', 'Package': package, 'Test': 'TestFixture', 'Elapsed': 0, 'Time': '2026-09-08T00:00:02Z'},
                  {'Action': 'pass', 'Package': package, 'Elapsed': 2, 'Time': '2026-09-08T00:00:03Z'}]
        for name in ('go.stdout.raw.jsonl', 'wrapper.stdout.raw.jsonl'):
            (out / name).write_text(''.join(json.dumps(e)+'\n' for e in stream))
        (out / 'binary.stderr.raw').write_text('SCHED 0ms: gomaxprocs=4 idleprocs=4 threads=3 spinningthreads=0 needspinning=0 idlethreads=1 runqueue=0 [0 0 0 0]\n')
        for name in ('go.stderr.raw', 'wrapper.stderr.raw', 'binary.stdout.raw'):
            (out / name).touch()
        for name in ('package.test', 'cpu.pprof'):
            (out / name).write_bytes(b'synthetic pure summary fixture; not a Go artifact\n')
        common = {'synthetic': True, 'started': True, 'starttime': '100', 'rc': 0,
                  'start_utc': '2026-09-08T00:00:00Z', 'end_utc': '2026-09-08T00:00:04Z',
                  'elapsed_monotonic_ns': 4000000000, 'streams_complete': True,
                  'sampler': {'errors': 0, 'finished': True}}
        go = {**common, 'pid': 102, 'stream_sha256': {k: inst.sha256_file(out / n) for k,n in (('stdout','go.stdout.raw.jsonl'), ('stderr','go.stderr.raw'))}}
        binary = {**common, 'pid': 103, 'sha256': inst.sha256_file(out / 'package.test'),
                  'godebug_schedtrace': '5000', 'godebug_scheddetail': '0',
                  'stream_sha256': {k: inst.sha256_file(out / ('binary.' + k + '.raw')) for k in ('stdout', 'stderr')}}
        wrapper = {**common, 'pid': 101, 'identity': {'pid': 101, 'starttime': '100'},
                   'stream_sha256': {k: inst.sha256_file(out / n) for k,n in (
                       ('stdout', 'wrapper.stdout.raw.jsonl'), ('stderr', 'wrapper.stderr.raw'))}}
        inst.write_json(out / 'go.identity.json', go)
        inst.write_json(out / 'binary.identity.json', binary)
        inst.write_json(out / 'invocations.json', {'wrapper': wrapper, 'go': go, 'binary': binary})
        inst.write_json(out / 'prepare.json', {'ok': True})
        inst.write_json(out / 'exit.json', {'go_started': True, 'go_rc': 0, 'binary_rc': 0,
                        'wrapper_rc': 0, 'sampler_errors': 0, 'pprof': 'ok', 'cancelled': False})
        snap = {'ok': True, 'files': {'core/api/fixture_test.go': 'synthetic-hash'}}
        inventory = {'ok': True, 'packages': [
            {'import_path': spec['import_path'], 'package': pkg, 'files': {'TestGoFiles': [
                {'path': 'core/api/fixture_test.go', 'sha256': 'synthetic-hash'}]}}
            for pkg, spec in inst.ALLOWED_PACKAGES.items()]}
        inst.write_json(out / 'inventory.json', inventory)
        inst.write_json(out / 'source.json', {'package': 'core/api', 'before': snap, 'after': snap,
                                             'source_changed_during_measure': False, 'inventory': inventory})
        # Realistic, explicitly synthetic counter snapshots and their recorded deltas.
        # R1 arithmetic is unchanged; the summary must require an observed window.
        for role, pid in (('wrapper', 101), ('go', 102), ('binary', 103)):
            previous = None
            for i, kind in enumerate(('initial', 'final')):
                psi = {'state': 'ok', 'value': f'some avg10=0.00 total={12 + i*5}\n'}
                node = {'leaf': True, 'version': 'v2', 'path': '/synthetic/cgroup', 'identity': [1, 2, 3],
                        'cpu.stat': {'state': 'ok', 'value': f'usage_usec {100+i*100}\nuser_usec {80+i*80}\nsystem_usec {20+i*20}\nnr_periods {3+i}\nnr_throttled {1+i}\nthrottled_usec {7+i*2}\n'},
                        'memory.current': {'state': 'ok', 'value': '1000'},
                        'memory.events': {'state': 'ok', 'value': 'oom 0\noom_kill 0\n'},
                        **{key: psi for key in ('cpu.pressure', 'io.pressure', 'memory.pressure')}}
                resource = {'state': 'ok', 'pid': pid, 'starttime': '100',
                            'stat': {'utime': str(10+i*10), 'stime': str(2+i*2)},
                            'io': {'state': 'ok'}, 'schedstat': {'state': 'ok'},
                            'cgroup_tree': {'identity': ['synthetic-cgroup'],
                                           'visible_limit_complete': True, 'ancestors': [node]}}
                host = {'identity': {'state': 'ok', 'sha256': 'synthetic-boot-id'},
                        'stat': {'state': 'ok', 'value': f'cpu {10+i*10} 0 {2+i*2} {30+i*30} 0 0 0 0\n'},
                        'loadavg': {'state': 'ok'},
                        **{key: psi for key in ('pressure_cpu', 'pressure_io', 'pressure_memory')}}
                row = {'producer': role, 'kind': kind, 'mono_ns': 1000000000 + i*5000000000,
                       'process': resource, 'host': host,
                       'delta': inst.resource_delta(previous['process'] if previous else None, resource),
                       'host_delta': inst.host_delta(previous['host'] if previous else None, host)}
                inst.append_jsonl(out / 'resources.jsonl', row)
                previous = row
        receipts = []
        for label in ('flat', 'cum'):
            stdout, stderr = 'pprof-' + label + '.stdout', 'pprof-' + label + '.stderr'
            (out / stdout).write_text('synthetic parsed profile')
            (out / stderr).touch()
            receipts.append({**common, 'pid': 104, 'label': label, 'inputs_unchanged': True,
                             'input_sha256': {n: inst.sha256_file(out / n) for n in ('package.test', 'cpu.pprof')},
                             'stdout': stdout, 'stderr': stderr,
                             'stream_sha256': {'stdout': inst.sha256_file(out / stdout), 'stderr': inst.sha256_file(out / stderr)}})
        inst.write_json(out / 'pprof-receipts.json', {'status': 'ok', 'receipts': receipts})
        return out

    def change_json(self, path, fn):
        value = json.loads(path.read_text())
        fn(value)
        self.inst.write_json(path, value)

    def assert_incomplete(self, out, allow_test_pass=True):
        summary, _, _ = self.inst.derive_summary(out)
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertNotEqual(self.inst.cmd_summarize(out, require_complete=True), 0)
        self.assertNotEqual(summary['observation'], 'complete', summary)
        self.assertFalse(summary['integrity_ok'])
        if not allow_test_pass:
            self.assertNotEqual(summary['tests'], 'pass')

    def residual_rows(self, out, transform):
        rows = [json.loads(line) for line in (out / 'resources.jsonl').read_text().splitlines()]
        transform(rows)
        (out / 'resources.jsonl').write_text(''.join(json.dumps(r)+'\n' for r in rows))

    def residual_gate(self, out):
        # Exercise the real argparse/main --require-complete path; no producer may run.
        with mock.patch.object(self.inst.subprocess, 'Popen', side_effect=AssertionError('offline only')), \
             contextlib.redirect_stdout(io.StringIO()):
            return self.inst.main(['summarize', '--out', str(out), '--require-complete'])

    def test_r2_residual_positive_window_and_wrapper_streams(self):
        out = self.complete_fixture()
        self.assertEqual(self.residual_gate(out), 0)
        summary = self.inst.derive_summary(out)[0]
        self.assertEqual(summary['tests'], 'pass')
        self.assertTrue(summary['components']['resources'])
        self.assertTrue(summary['components']['streams'])
        for windows in summary['resources']['windows'].values():
            self.assertEqual(len(windows), 1)
            self.assertTrue(windows[0]['compatible'])
            self.assertEqual(windows[0]['elapsed_monotonic_ns'], 5000000000)

    def test_r2_residual_initial_ok_final_gone(self):
        out = self.complete_fixture()
        def gone(rows):
            for row in rows:
                if row['kind'] == 'final':
                    row['process'] = {'state': 'gone', 'pid': row['process']['pid']}
                    row['delta'] = {'state': 'unavailable', 'reason': 'process_not_observed'}
        self.residual_rows(out, gone)
        self.assertEqual(self.residual_gate(out), self.inst.RC_INCOMPLETE)
        summary = self.inst.derive_summary(out)[0]
        self.assertEqual(summary['tests'], 'pass')
        self.assertTrue(summary['components']['streams'])  # F1 isolated from F2
        self.assertFalse(summary['components']['resources'])
        self.assertEqual(summary['resources']['point_samples'], {'wrapper': 1, 'go': 1, 'binary': 1})
        for windows in summary['resources']['windows'].values():
            self.assertFalse(windows[0]['compatible'])
            self.assertEqual(windows[0]['metrics']['process_cpu_ticks']['state'], 'unavailable')

    def test_r2_residual_gaps_resets_and_migration(self):
        for case in ('delta_absent', 'counter_reset', 'migration', 'clock_unknown', 'partial_metric'):
            with self.subTest(case=case):
                out = self.complete_fixture()
                def mutate(rows):
                    before, row = rows[0], rows[1]
                    if case == 'delta_absent':
                        row.pop('delta')
                    elif case == 'clock_unknown':
                        row.pop('mono_ns')
                    elif case == 'partial_metric':
                        row['delta']['cgroup']['nodes'][0]['counters']['cpu.pressure'] = {'state': 'unavailable'}
                    else:
                        if case == 'counter_reset':
                            row['process']['stat']['utime'] = '1'
                        else:
                            row['process']['cgroup_tree']['identity'] = ['other-cgroup']
                        row['delta'] = self.inst.resource_delta(before['process'], row['process'])
                self.residual_rows(out, mutate)
                self.assertEqual(self.residual_gate(out), self.inst.RC_INCOMPLETE)
                summary = self.inst.derive_summary(out)[0]
                self.assertFalse(summary['components']['resources'])
                self.assertEqual(summary['tests'], 'pass')
                # Preserve every subcase, including its incomplete metadata.
                out.rename(self.work / case)

    def test_r2_residual_window_does_not_hide_internal_gap(self):
        out = self.complete_fixture()
        def gap(rows):
            end = rows[1]
            end['kind'] = 'interval'
            last = copy.deepcopy(end)
            last.update(kind='final', mono_ns=end['mono_ns']+5000000000)
            last['process']['stat']['utime'] = '1'
            last['delta'] = self.inst.resource_delta(end['process'], last['process'])
            last['host_delta'] = self.inst.host_delta(end['host'], last['host'])
            rows.insert(2, last)
        self.residual_rows(out, gap)
        self.assertEqual(self.residual_gate(out), self.inst.RC_INCOMPLETE)
        windows = self.inst.derive_summary(out)[0]['resources']['windows']['wrapper']
        self.assertTrue(windows[0]['compatible'])
        self.assertEqual(windows[1]['metrics']['process_cpu_ticks']['reason'], 'counter_reset')

    def test_r2_residual_observed_window_preserves_gone_tail(self):
        out = self.complete_fixture()
        def tail(rows):
            end = rows[1]
            end['kind'] = 'interval'
            last = copy.deepcopy(end)
            last.update(kind='final', mono_ns=end['mono_ns']+1000000,
                        process={'state': 'gone', 'pid': 101},
                        delta={'state': 'unavailable', 'reason': 'process_not_observed'})
            last['host_delta'] = self.inst.host_delta(end['host'], last['host'])
            rows.insert(2, last)
        self.residual_rows(out, tail)
        self.assertEqual(self.residual_gate(out), 0)
        resources = self.inst.derive_summary(out)[0]['resources']
        self.assertTrue(resources['windows']['wrapper'][0]['compatible'])
        self.assertFalse(resources['windows']['wrapper'][1]['compatible'])
        self.assertEqual(resources['windows']['wrapper'][1]['to_state'], 'gone')
        self.assertIn('not whole-lifetime', resources['window_scope'])

    def test_r2_residual_wrapper_hash_missing_or_truncated(self):
        for case in ('missing_hashes', 'stdout', 'stderr'):
            with self.subTest(case=case):
                out = self.complete_fixture()
                if case == 'missing_hashes':
                    self.change_json(out / 'invocations.json', lambda obj: obj['wrapper'].pop('stream_sha256'))
                elif case == 'stdout':
                    (out / 'wrapper.stdout.raw.jsonl').write_bytes(b'')
                else:
                    (out / 'wrapper.stderr.raw').write_bytes(b'[wrapper stderr redacted]\n')
                receipt_before = (out / 'invocations.json').read_bytes()
                self.assertEqual(self.residual_gate(out), self.inst.RC_INCOMPLETE)
                summary = self.inst.derive_summary(out)[0]
                self.assertTrue(summary['components']['resources'])  # F2 isolated from F1
                self.assertFalse(summary['components']['streams'])
                self.assertEqual(summary['tests'], 'pass')
                self.assertEqual((out / 'invocations.json').read_bytes(), receipt_before)
                # A derived seal must not rehabilitate an unauthenticated stream on a second read.
                self.assertEqual(self.residual_gate(out), self.inst.RC_INCOMPLETE)
                out.rename(self.work / case)

    def test_r2_residual_wrapper_capture_closes_with_sanitized_hashes(self):
        out = self.complete_fixture()
        inst = self.inst
        source = inst.read_json(out / 'source.json')
        snapshot = source['before']
        source['inventory_inputs'] = snapshot
        inst.write_json(out / 'source.json', source)
        inst.write_json(out / 'prepare.json', {'ok': True, 'utc': '2026-09-08T00:00:00Z',
                        'mono_ns': 1, 'stages': {'provision': {'rc': 0}}})
        (out / 'invocations.json').unlink()
        (out / 'exit.json').unlink()
        raw_private_payload = b'private-wrapper-fixture-payload\n'
        child = mock.Mock(pid=101, stdout=io.BytesIO((out / 'go.stdout.raw.jsonl').read_bytes()),
                          stderr=io.BytesIO(raw_private_payload))
        child.wait.return_value = 0
        sampler = mock.Mock()
        sampler.finish.return_value = {'errors': 0, 'finished': True}
        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.dict(os.environ, {'EXPECTED_SHA': 'a'*40, 'GITHUB_SHA': 'a'*40}))
            for name, value in {
                'prepare_overrides': {'ok': True}, 'git_identity': {'commit': 'a'*40},
                'source_snapshot': snapshot, 'go_tool_facts': {'path': '/synthetic/go'},
                'tmpdir_facts': {}, 'cache_volumes': {}, 'cpu_quota_aux': {}, 'inspect_postgres': {},
                'proc_identity': {'pid': 101, 'starttime': '100', 'state': 'ok'},
                'pprof_once': inst.read_json(out / 'pprof-receipts.json'), 'Sampler': sampler,
            }.items():
                stack.enter_context(mock.patch.object(inst, name, return_value=value))
            launch = stack.enter_context(mock.patch.object(inst.subprocess, 'Popen', return_value=child))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            rc = inst.cmd_run('core/api', out)
        self.assertEqual(rc, 0)
        launch.assert_called_once()  # fake process; no Go, wrapper or PG executable was started
        wrapper = inst.read_json(out / 'invocations.json')['wrapper']
        self.assertEqual((out / 'wrapper.stderr.raw').read_bytes(), b'[wrapper stderr redacted]\n')
        self.assertNotEqual(wrapper['stream_sha256']['stderr'], inst.sha256_bytes(raw_private_payload))
        for stream, filename in (('stdout', 'wrapper.stdout.raw.jsonl'), ('stderr', 'wrapper.stderr.raw')):
            self.assertEqual(wrapper['stream_sha256'][stream], inst.sha256_file(out / filename))
        self.assertEqual(self.residual_gate(out), 0)

    def test_r2_valid_summary_and_sampler_failure(self):
        out = self.complete_fixture()
        summary, _, _ = self.inst.derive_summary(out)
        self.assertEqual((summary['observation'], summary['tests']), ('complete', 'pass'))
        self.change_json(out / 'exit.json', lambda obj: obj.update(sampler_errors=1))
        self.assert_incomplete(out)

    def test_r2_pprof_failure_is_not_nonempty_success(self):
        out = self.complete_fixture()
        self.change_json(out / 'pprof-receipts.json', lambda obj: obj['receipts'][1].update(rc=2))
        self.change_json(out / 'exit.json', lambda obj: obj.update(pprof='incomplete'))
        self.assert_incomplete(out)

    def test_r2_missing_resources(self):
        out = self.complete_fixture()
        (out / 'resources.jsonl').unlink()
        self.assert_incomplete(out)

    def test_r2_invalid_producer(self):
        out = self.complete_fixture()
        self.change_json(out / 'binary.identity.json', lambda obj: obj.update(started=False, pid=None, starttime=None))
        self.assert_incomplete(out, allow_test_pass=False)

    def test_r2_build_only_and_wrong_package(self):
        out = self.complete_fixture()
        for text in ('{"Action":"build-output","ImportPath":"p","Output":"build"}\n',
                     '{"Action":"future-event","ImportPath":"p"}\n',
                     '{"Action":"start","Package":"other"}\n{"Action":"pass","Package":"other"}\n'):
            with self.subTest(stream=text):
                (out / 'go.stdout.raw.jsonl').write_text(text)
                self.assert_incomplete(out, allow_test_pass=False)

    def test_r2_gate_rejects_explained_noncompletion(self):
        out = self.complete_fixture()
        for obs in ('prepare_failed', 'probe_not_started', 'censored'):
            with self.subTest(observation=obs):
                for p in ('exit.json', 'prepare.json', 'go.identity.json'):
                    (out / p).unlink(missing_ok=True)
                self.inst.write_json(out / 'exit.json', {'go_started': False, 'go_rc': None})
                self.inst.write_json(out / 'prepare.json', {'ok': obs != 'prepare_failed'})
                if obs == 'censored':
                    self.inst.write_json(out / 'go.identity.json', {'started': True, 'pid': 12, 'rc': 1})
                    self.inst.write_json(out / 'exit.json', {'go_started': True, 'go_rc': 1})
                    (out / 'go.stdout.raw.jsonl').write_text('{"Action":"start","Package":"p"}\n{"Action":"run","Package":"p","Test":"TestOpen"}\n')
                self.assert_incomplete(out)

    def test_r2_cancellation_and_source_mismatch(self):
        out = self.complete_fixture()
        self.change_json(out / 'exit.json', lambda obj: obj.update(cancelled=True))
        self.assert_incomplete(out)
        self.change_json(out / 'exit.json', lambda obj: obj.update(cancelled=False))
        self.change_json(out / 'source.json', lambda obj: obj['after']['files'].update({'core/api/fixture_test.go': 'changed'}))
        self.assert_incomplete(out)

    def test_r2_inventory_mismatch_and_missing_stream(self):
        out = self.complete_fixture()
        self.change_json(out / 'inventory.json', lambda obj: obj['packages'].pop())
        self.assert_incomplete(out)
        (out / 'binary.stdout.raw').unlink()
        self.assert_incomplete(out)

    def test_r3_scheduler_override_zero_and_duplicates(self):
        merged = self.inst.merge_godebug('schedtrace=0,gctrace=1,schedtrace=17,scheddetail=1')
        self.assertEqual(merged.count('schedtrace='), 1)
        self.assertIn('schedtrace=5000', merged)
        self.assertIn('scheddetail=0', merged)
        self.assertIn('gctrace=1', merged)

    def test_r3_timestamps_and_rounded_zero(self):
        rows = [{'Action': a, 'Package': 'p', 'Test': 'TestParallel', 'Elapsed': 0,
                 'Time': f'2026-09-08T00:00:0{i}Z'} for i, a in enumerate(('run', 'pause', 'cont', 'pass'))]
        events, errors = self.inst.parse_json_stream(''.join(json.dumps(e)+'\n' for e in rows))
        test = self.inst.summarize_events(events, errors)['tests'][0]
        self.assertEqual([e['action'] for e in test['events']], ['run', 'pause', 'cont', 'pass'])
        self.assertEqual(test['timing']['parked_intervals'][0]['wall_seconds'], 1)
        self.assertIn('rounded', test['elapsed_semantics'])
        self.assertEqual(test['elapsed'], 0)

    def test_r3_go_before_any_json_and_exec_failure(self):
        child = self.work / 'go-fixture'
        write_exec(child, '#!/bin/sh\nprintf "go failed before JSON\\n" >&2\nexit 9\n')
        with mock.patch.dict(os.environ, {'GOFLAGS': '-p=2', 'GODEBUG': 'schedtrace=0'}):
            rc = self.inst.cmd_exec(self.work, [str(child)], producer='go')
        self.assertEqual(rc, 9)
        ident = json.loads((self.work / 'go.identity.json').read_text())
        self.assertTrue(ident['started'])
        self.assertEqual(ident['rc'], 9)
        self.assertEqual(ident['effective_environment']['GOFLAGS']['value'], '-p=2')
        self.assertEqual((self.work / 'go.stdout.raw.jsonl').read_bytes(), b'')
        self.assertFalse((self.work / 'binary.identity.json').exists())
        self.assertNotEqual(ident['pid'], os.getpid())

    def test_r3_pprof_receipts_two_real_processes(self):
        (self.work / 'package.test').write_bytes(b'synthetic-input')
        (self.work / 'cpu.pprof').write_bytes(b'synthetic-profile')
        fake_go = self.work / 'go'
        write_exec(fake_go, '#!/bin/sh\necho finite-offline-fixture\nexit 0\n')
        with mock.patch.object(self.inst.shutil, 'which', return_value=str(fake_go)):
            result = self.inst.pprof_once(self.work)
            again = self.inst.pprof_once(self.work)
        self.assertEqual(result, again)
        self.assertEqual(len(result['receipts']), 2)
        for receipt in result['receipts']:
            self.assertGreater(receipt['pid'], 0)
            self.assertIsNotNone(receipt['start_utc'])
            self.assertIsNotNone(receipt['end_utc'])
            self.assertEqual(receipt['rc'], 0)
            self.assertTrue((self.work / receipt['stderr']).is_file())

    def test_r4_allocation_and_sanitized_failure(self):
        root = self.work / 'repo'
        (root / 'scripts').mkdir(parents=True)
        helper = root / 'scripts/ci-postgres-service.sh'
        write_exec(helper, '#!/bin/sh\necho "$SECRET_CANARY"\necho "$SECRET_CANARY" >&2\nexit 17\n')
        out = self.work / 'out'
        snap = {'ok': True, 'git': {'commit': 'a'*40}, 'files': {'fixture.go': 'x'}}
        with mock.patch.object(self.inst, 'repo_root', return_value=root), \
             mock.patch.object(self.inst, 'source_snapshot', return_value=snap), \
             mock.patch.object(self.inst, 'git_identity', return_value={'commit': 'a'*40}), \
             mock.patch.dict(os.environ, {'EXPECTED_SHA': 'a'*40, 'GITHUB_SHA': 'a'*40,
                                         'SECRET_CANARY': 'private-canary-do-not-record', 'GOFLAGS': ''}):
            self.assertEqual(self.inst.cmd_run('core/api', out, phase='allocate'), 0)
            self.assertTrue((out / 'source.json').is_file())
            self.assertEqual(self.inst.cmd_run('core/api', out, phase='resolve'), 17)
        prep = json.loads((out / 'prepare.json').read_text())
        self.assertFalse(prep['ok'])
        self.assertEqual(prep['stages']['resolve']['rc'], 17)
        self.assertGreater(prep['stages']['resolve']['pid'], 0)
        self.assertFalse(json.loads((out / 'exit.json').read_text())['go_started'])
        for path in out.rglob('*'):
            if path.is_file():
                self.assertNotIn(b'private-canary-do-not-record', path.read_bytes())
        self.assert_incomplete(out, allow_test_pass=False)

    def test_r4_working_bytes_and_inventory_validation(self):
        root = self.work / 'repo'
        root.mkdir()
        self.inst.run_receipt(['git', 'init', '-q', str(root)], self.work, self.work, 'fixture-git-init')
        (root / 'fixture.go').write_text('package fixture\n')
        before = self.inst.source_snapshot(root)
        (root / 'fixture.go').write_text('package changed\n')
        after = self.inst.source_snapshot(root)
        self.assertNotEqual(before['files'], after['files'])
        self.assertEqual(before['git']['tree'], after['git']['tree'])
        (root / 'core').mkdir()
        out = self.work / 'inventory'
        out.mkdir()
        fake_go = self.work / 'go'
        write_exec(fake_go, '#!/bin/sh\necho \'{"Dir":"/wrong","ImportPath":"wrong"}\'\nexit 0\n')
        with mock.patch.object(self.inst.shutil, 'which', return_value=str(fake_go)):
            inv = self.inst.go_list_inventory(root, out)
        self.assertFalse(inv['ok'])
        self.assertEqual(inv['receipt']['argv'][-3:], ['./api', './auth', './internal/store/sqlstore'])
        self.assertTrue((out / 'inventory.json').is_file())

    def test_r2_wrapper_failure_and_mutated_stream(self):
        out = self.complete_fixture()
        self.change_json(out / 'invocations.json', lambda obj: obj['wrapper'].update(rc=7))
        self.change_json(out / 'exit.json', lambda obj: obj.update(wrapper_rc=7))
        self.assert_incomplete(out)
        self.change_json(out / 'invocations.json', lambda obj: obj['wrapper'].update(rc=0))
        self.change_json(out / 'exit.json', lambda obj: obj.update(wrapper_rc=0))
        with (out / 'binary.stderr.raw').open('a') as f:
            f.write('unaccounted bytes\n')
        self.assert_incomplete(out)

    def test_r3_persistent_goflags_after_probe(self):
        child = self.work / 'go-fixture'
        write_exec(child, '#!/bin/sh\nif [ "$1" = env ]; then echo -p=3; exit 0; fi\nexit 9\n')
        with mock.patch.dict(os.environ, {'GOFLAGS': '', 'GODEBUG': 'schedtrace=0'}):
            rc = self.inst.cmd_exec(self.work, [str(child), 'test'], producer='go')
        self.assertEqual(rc, 9)
        ident = json.loads((self.work / 'go.identity.json').read_text())
        self.assertEqual(ident['effective_environment']['GOFLAGS']['value'], '-p=3')
        self.assertEqual(ident['effective_environment']['GOFLAGS']['origin'], 'go_env_after_probe')

    def test_r4_finalize_preserves_started_but_interrupted_go(self):
        out = self.complete_fixture()
        (out / 'exit.json').unlink()
        with mock.patch.dict(os.environ, {'RPP_MEASURE': 'cancelled'}):
            self.assertEqual(self.inst.cmd_run('core/api', out, phase='finalize-prepare'), 0)
        result = json.loads((out / 'exit.json').read_text())
        self.assertTrue(result['go_started'])
        self.assertTrue(result['cancelled'])
        self.assertIsNone(result['wrapper_rc'])
        self.assertIsNone(result['termination_utc'])
        self.assert_incomplete(out)

    def test_r2_malformed_metadata_remains_inspectable(self):
        out = self.complete_fixture()
        (out / 'exit.json').write_text('{"cancelled":')
        self.assert_incomplete(out)
        self.assertIn('exit', self.inst.derive_summary(out)[0]['metadata_errors'])

    def test_r3_ambiguous_direct_scheduler_is_unattributable(self):
        line = 'SCHED 0ms: gomaxprocs=4 idleprocs=4 threads=3 spinningthreads=0 needspinning=0 idlethreads=1 runqueue=0\n'
        self.assertFalse(self.inst.parse_scheduler_text(line + line)['runtime_attributable'])
        self.assertFalse(self.inst.parse_scheduler_text(line + 'SCHED malformed\n')['runtime_attributable'])

    def test_r4_workflow_custody_order(self):
        workflow = WF.read_text()
        self.assertLess(workflow.index('--phase allocate'), workflow.index('--phase resolve'))
        self.assertLess(workflow.index('id: sha-guard'), workflow.index('--phase resolve'))
        self.assertIn('--phase inventory', workflow)
        cleanup = workflow.split("- name: remove this assignment's scratch", 1)[1]
        self.assertNotIn('always()', cleanup)
        self.assertIn("steps.upload.outcome == 'success'", cleanup)
        self.assertIn("steps.upload.outputs.artifact-id != ''", cleanup)
        self.assertIn('if-no-files-found: error', workflow)
        self.assertIn('max-parallel: 1', workflow)
        self.assertIn('timeout-minutes: 175', workflow)
        self.assertIn('timeout-minutes: 90', workflow)


def correction_main():
    selected = [arg for arg in sys.argv[1:] if arg.startswith("test_r")]
    suite = (unittest.TestSuite(CorrectionTests(name) for name in selected) if selected
             else unittest.defaultTestLoader.loadTestsFromTestCase(CorrectionTests))
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    return 0 if result.wasSuccessful() else 1


if __name__ == "__main__":
    if "--corrections" in sys.argv:
        raise SystemExit(correction_main())
    if "--microfixture" in sys.argv:
        inst = load_inst()
        work = pick_exec_dir()
        if work is None:
            could_not_look("no executable work directory")
        group6_microfixture(inst, work)
        print(f"retained microfixture: {work}; {pass_n} passed, {fail_n} failed")
        raise SystemExit(1 if fail_n else 0)
    result = main()
    raise SystemExit(max(result, correction_main()))
