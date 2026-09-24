#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Serial supervisor for cedar-measure: one bounded child process at a time.

Run as root on a disposable CI runner through sudo. Every child runs as the
invoking user in its own fresh cgroup v2 group with hard memory, swap, task and
time limits, and prints one JSON record that this supervisor checks
independently of the child's own verdict.

Commands:
  probe  --out DIR                              establish isolation, then remove it
  setup  --out DIR --build DIR --go GO          download, verify and build, bounded
  run    --out DIR --packet matrix|oframe ...   run the packet with its controls

One outcome policy for every child, every packet and the supervisor itself:
0 coherent (every child observed, every control refused), 1 measured mismatch
(a readable result, identity, pin or reading order differs, or a deliberate
defect was accepted), 2 inability (isolation, setup, timeout, out of memory,
abnormal exit, missing or unreadable record, budget, or a supervisor fault).
A failure never becomes a pass, and nothing is retried.
"""

import argparse
import hashlib
import json
import os
import secrets
import subprocess
import sys
import time

# Limits and clocks. This file is their only owner.
CHILD_SECONDS = 60                # hard kill of a child, setup included
WAIT_GRACE_SECONDS = 2            # wait beyond the kill before the backstop kill
REAP_SECONDS = 3                  # bounded wait for a backstop-killed child to be reaped
CLEANUP_SECONDS = 3               # bounded wait for an emptied group, polled every 0.1 s
SLOT_MARGIN_SECONDS = 1           # group creation, readback and evidence writing
CHILD_SLOT_SECONDS = CHILD_SECONDS + WAIT_GRACE_SECONDS + REAP_SECONDS + CLEANUP_SECONDS + SLOT_MARGIN_SECONDS
CHILD_MEMORY_MAX = 2 * 1024 ** 3
CHILD_SWAP_MAX = 0
CHILD_PIDS_MAX = 64
CHILD_ENV = {"GOMAXPROCS": "2", "GODEBUG": "gcshrinkstackoff=1"}
SETUP_SECONDS = 360               # the setup steps' own clock, inside the 7-minute step
SETUP_MEMORY_MAX = 4 * 1024 ** 3
SETUP_PIDS_MAX = 1024
SETUP_DISK_OBSERVED_MAX = 2 * 1024 ** 3   # observed after the build; not a bound on the download
RUN_BUDGET_SECONDS = 4920         # inner clock of the run step
EVIDENCE_MARGIN_SECONDS = 60      # run step timeout minus the inner clock: isolation and summary
OFRAME_BATCHES = 8
OFRAME_CORPUS_DOCUMENTS = 7750
OUTPUT_CAP = 1 << 20              # bytes kept from a child's stdout or stderr

EXIT_COHERENT, EXIT_MISMATCH, EXIT_INABILITY = 0, 1, 2
RECORD_SCHEMA = "cedar-measure/record/v1"
MATRIX_SCHEMA = "cedar-measure/matrix/v1"
CGROUP_ROOT = "/sys/fs/cgroup"
GROUP_PARENT = "cedar-measure"
CARRIER_ID = "R4a/authorize/512"  # the child the control phase reuses
PHASES = ("setup", "measure", "check", "emit", "validate", "oframe")
PHASE_PREFIX = b"cedar-measure phase: "
RUNTIME_FATAL_MARKERS = (b"fatal error: ", b"runtime: goroutine stack exceeds")
EVENT_VARIABLES = ("GITHUB_EVENT_NAME", "GITHUB_REF", "GITHUB_SHA", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
                   "GITHUB_WORKFLOW_REF", "ImageOS", "ImageVersion", "RUNNER_OS", "RUNNER_ARCH")


class Inability(Exception):
    """A condition under which nothing can be measured. Carries a fixed code."""


# ---------------------------------------------------------------- isolation --

def read_text(path):
    with open(path, encoding="ascii") as f:
        return f.read()


def write_text(path, value):
    with open(path, "w", encoding="ascii") as f:
        f.write(value)


def check_cgroup_root(root):
    """Refuse unless root is a cgroup v2 hierarchy that delegates memory and pids."""
    try:
        controllers = read_text(os.path.join(root, "cgroup.controllers")).split()
        delegated = read_text(os.path.join(root, "cgroup.subtree_control")).split()
    except OSError:
        raise Inability("cgroup_v2_missing")
    for name in ("memory", "pids"):
        if name not in controllers or name not in delegated:
            raise Inability("cgroup_controller_missing:" + name)


def invoking_user():
    """The unprivileged user sudo ran as, or an inability. No default, no fallback."""
    uid, gid = os.environ.get("SUDO_UID", ""), os.environ.get("SUDO_GID", "")
    if not uid.isdigit() or not gid.isdigit() or int(uid) == 0:
        raise Inability("no_unprivileged_user")
    return int(uid), int(gid)


def establish(root=CGROUP_ROOT):
    """Create the parent group and prove a child group can carry every limit.

    It either returns a verified parent group or raises Inability; there is no
    other path.
    """
    if os.geteuid() != 0:
        raise Inability("not_root")
    invoking_user()
    check_cgroup_root(root)
    parent = os.path.join(root, GROUP_PARENT)
    try:
        os.makedirs(parent, exist_ok=True)
        write_text(os.path.join(parent, "cgroup.subtree_control"), "+memory +pids")
    except OSError:
        raise Inability("cgroup_parent")
    probe = new_group(parent, "probe")
    remove_group(probe)
    return parent


def new_group(parent, name):
    """Create a fresh group with every child limit, read back and verified."""
    path = os.path.join(parent, name)
    try:
        os.mkdir(path)
        write_text(os.path.join(path, "memory.max"), str(CHILD_MEMORY_MAX))
        write_text(os.path.join(path, "memory.swap.max"), str(CHILD_SWAP_MAX))
        write_text(os.path.join(path, "pids.max"), str(CHILD_PIDS_MAX))
        limits = (
            read_text(os.path.join(path, "memory.max")).strip(),
            read_text(os.path.join(path, "memory.swap.max")).strip(),
            read_text(os.path.join(path, "pids.max")).strip(),
        )
        peak = int(read_text(os.path.join(path, "memory.peak")).strip())
        read_text(os.path.join(path, "memory.events"))
    except (OSError, ValueError):
        raise Inability("cgroup_child")
    if limits != (str(CHILD_MEMORY_MAX), str(CHILD_SWAP_MAX), str(CHILD_PIDS_MAX)):
        raise Inability("cgroup_limits_not_applied")
    if peak != 0:
        raise Inability("cgroup_not_fresh")
    return path


def set_group_limits(path, memory_max, pids_max):
    try:
        write_text(os.path.join(path, "memory.max"), str(memory_max))
        write_text(os.path.join(path, "pids.max"), str(pids_max))
    except OSError:
        raise Inability("cgroup_limits_not_applied")


def group_stats(path):
    """memory.peak and the OOM kill count, or None where a file cannot be read."""
    stats = {"memory_peak": None, "oom_kill": None}
    try:
        stats["memory_peak"] = int(read_text(os.path.join(path, "memory.peak")).strip())
        for line in read_text(os.path.join(path, "memory.events")).splitlines():
            key, _, value = line.partition(" ")
            if key == "oom_kill":
                stats["oom_kill"] = int(value)
    except (OSError, ValueError):
        pass
    return stats


def remove_group(path):
    """Kill whatever is left, wait a bounded time until the group is empty, remove it.

    A group that cannot be emptied or removed is an inability, never ignored.
    """
    try:
        write_text(os.path.join(path, "cgroup.kill"), "1")
        for _ in range(CLEANUP_SECONDS * 10):
            if "populated 0" in read_text(os.path.join(path, "cgroup.events")):
                break
            time.sleep(0.1)
        os.rmdir(path)
    except OSError:
        raise Inability("cgroup_cleanup")


def run_in_group(group, argv, env, cwd, kill_seconds):
    """Run argv as the invoking user inside group; every wait is bounded."""
    uid, gid = invoking_user()
    procs = os.path.join(group, "cgroup.procs")

    def enter():
        with open(procs, "w", encoding="ascii") as f:
            f.write(str(os.getpid()))
        os.setgroups([])
        os.setgid(gid)
        os.setuid(uid)

    pre = group_stats(group)
    start = time.monotonic()
    proc = subprocess.Popen(
        ["timeout", "-s", "KILL", "%ds" % kill_seconds] + argv,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, cwd=cwd,
        preexec_fn=enter, close_fds=True)
    backstop = False
    try:
        out, err = proc.communicate(timeout=kill_seconds + WAIT_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        backstop = True
        try:
            write_text(os.path.join(group, "cgroup.kill"), "1")
        except OSError:
            raise Inability("cgroup_kill")
        try:
            out, err = proc.communicate(timeout=REAP_SECONDS)
        except subprocess.TimeoutExpired:
            raise Inability("child_unreaped")
    elapsed = time.monotonic() - start
    post = group_stats(group)
    return {
        "returncode": proc.returncode,
        "stdout": out[:OUTPUT_CAP],
        "stderr": err[:OUTPUT_CAP],
        "elapsed_s": round(elapsed, 3),
        "pre_peak": pre["memory_peak"],
        "memory_peak": post["memory_peak"],
        "oom_kill": post["oom_kill"],
        "backstop": backstop,
    }


# ------------------------------------------- one verifier, one outcome policy --

def parse_record(stdout):
    """Return the single JSON object a child printed, or None."""
    try:
        lines = [line for line in stdout.decode("utf-8").splitlines() if line.strip()]
    except UnicodeDecodeError:
        return None
    if len(lines) != 1:
        return None
    try:
        rec = json.loads(lines[0])
    except ValueError:
        return None
    return rec if isinstance(rec, dict) else None


def last_phase(stderr):
    """The last fixed phase code the child wrote, or "none"."""
    phase = "none"
    for line in stderr.splitlines():
        if line.startswith(PHASE_PREFIX):
            name = line[len(PHASE_PREFIX):].decode("ascii", "replace").strip()
            if name in PHASES:
                phase = name
    return phase


def process_fate(run):
    """The inability of a process that did not end by itself with 0, 1 or 2, or None."""
    phase = last_phase(run["stderr"])
    if run["pre_peak"] is None or run["oom_kill"] is None or run["memory_peak"] is None:
        return ["cgroup_stats_unreadable"]
    if run["pre_peak"] != 0:
        return ["cgroup_not_fresh"]
    if run["oom_kill"]:
        return ["out_of_memory:" + phase]
    if run["backstop"] or (run["returncode"] in (124, 137, -9) and run["elapsed_s"] >= CHILD_SECONDS - 0.5):
        return ["timeout:" + phase]
    if run["returncode"] not in (0, 1, 2):
        return ["abnormal_exit:%s:%s" % (run["returncode"], phase)]
    return None


def verify_runtime(rec, token, pins, mode):
    """The shared part of every record check: schema, mode, token, build and runtime pins.

    Returns (unreadable, mismatches). Unreadable means the observation cannot be
    read; mismatches are readable facts that differ from what the packet requires.
    """
    unreadable, mismatches = [], []
    if rec.get("schema") != RECORD_SCHEMA or rec.get("mode") != mode:
        unreadable.append("record_schema")
    env = rec.get("env")
    if not isinstance(env, dict):
        return unreadable + ["env_missing"], mismatches
    if rec.get("token") != token:
        mismatches.append("token")
    if env.get("go_version") != pins["go"]:
        mismatches.append("go_version")
    if env.get("cedar_go") != pins["cedar_go"] or env.get("cedar_go_sum") != pins["cedar_go_sum"]:
        mismatches.append("cedar_go_pin")
    if env.get("gomaxprocs") != int(CHILD_ENV["GOMAXPROCS"]):
        mismatches.append("gomaxprocs")
    if env.get("godebug") != CHILD_ENV["GODEBUG"] or env.get("gogc") or env.get("gomemlimit"):
        mismatches.append("runtime_environment")
    return unreadable, mismatches


CHILD_IDENTITY = ("id", "cell", "op", "point", "input_sha256", "input_bytes")


def child_identity(entry, rec, present_only=False):
    """Identity mismatches of a child record. An early record carries only the
    fields the child had set when it stopped; present_only compares just those."""
    return ["identity:" + key for key in CHILD_IDENTITY
            if (key in rec or not present_only) and rec.get(key) != entry[key]]


def child_problems(entry, rec):
    """Checks specific to a measured child: identity, reading order, semantic result."""
    unreadable, mismatches = [], child_identity(entry, rec)
    meas = rec.get("measurement")
    try:
        chain = [meas["baseline"], meas["op_start"], meas["op_end"], meas["after"]]
        if entry["op"] == "compile":
            chain.append(meas["live"])
        seqs = [int(x["seq"]) for x in chain]
        times = [int(x["at_ns"]) for x in chain]
        for r in (meas["baseline"], meas["after"]) + ((meas["live"],) if entry["op"] == "compile" else ()):
            for k in ("total_alloc_bytes", "mallocs", "stacks_bytes", "live_heap_bytes"):
                int(r[k])
    except (KeyError, TypeError, ValueError):
        return unreadable + ["measurement_incomplete"], mismatches
    if seqs != sorted(seqs) or len(set(seqs)) != len(seqs):
        mismatches.append("reading_out_of_order")
    if times != sorted(times):
        mismatches.append("clock_out_of_order")
    if entry["op"] != "compile" and meas.get("live") is not None:
        mismatches.append("unexpected_live_reading")
    mismatches += result_problems(entry, rec.get("result"))
    return unreadable, mismatches


def result_problems(entry, res):
    """Compare the child's semantic result with the matrix, independently."""
    e, op, problems = entry["expected"], entry["op"], []
    if not isinstance(res, dict) or res.get("ran") is not True:
        return ["operation_not_executed"]
    if op == "parse":
        if res.get("steps") != e["steps"]:
            problems.append("result_steps")
        if e.get("type_len") and res.get("type_len") != e["type_len"]:
            problems.append("result_type_len")
        if e.get("annotations") and res.get("annotations") != e["annotations"]:
            problems.append("result_annotations")
    elif op in ("compile", "authorize"):
        if sorted(res.get("reasons") or []) != sorted(e.get("reasons", [])):
            problems.append("result_reasons")
        if res.get("errors") != e["errors"]:
            problems.append("result_errors")
        if e.get("discriminates") and res.get("discriminates") is not True:
            problems.append("result_not_discriminating")
    elif op == "inspect":
        if res.get("nodes") != e["nodes"]:
            problems.append("result_nodes")
    elif op == "walk":
        if res.get("steps") != e["steps"] or res.get("depth") != e["depth"]:
            problems.append("result_walk")
    return problems


def validate_problems(expect, rec):
    detail = rec.get("detail")
    if not isinstance(detail, dict) or not isinstance(detail.get("failures"), list):
        return ["detail_missing"], []
    if detail.get("children_checked") != expect["children"] or detail.get("low_points_checked") != expect["low_points"]:
        return [], ["validate_coverage"]
    return [], []


def oframe_problems(expect, rec):
    detail = rec.get("detail")
    if not isinstance(detail, dict) or not isinstance(detail.get("agreement"), dict):
        return ["detail_missing"], []
    mismatches = []
    for key in ("batch", "batches", "edges"):
        if detail.get(key) != expect[key]:
            mismatches.append("identity:" + key)
    if not isinstance(detail["agreement"].get("documents"), int):
        return ["detail_missing"], mismatches
    return [], mismatches


# The part of a record that only a completed child carries, per mode.
MODE_DATA = {"child": "measurement", "validate": "detail", "oframe": "detail"}


def assess(run, token, pins, mode, specific, early_identity=None):
    """The one outcome policy, for every child of every packet.

    Returns (class, reasons, record). class is observed, mismatch or inability.
    Only observed is a measurement.

    An early record is readable, agrees with its exit code, passes the shared
    checks' readability, reports mismatch or inability, and has no measurement
    (child) or detail (validate, O-frame): the child stopped before that phase
    and said so. It keeps its own outcome and child:<reason>, plus any shared
    token, pin or identity evidence; no measurement is ever fabricated. An
    observed claim without its measurement or detail stays an inability.
    """
    fate = process_fate(run)
    if fate:
        return "inability", fate, None
    phase = last_phase(run["stderr"])
    rec = parse_record(run["stdout"])
    if rec is None:
        if run["returncode"] == 2 and any(m in run["stderr"] for m in RUNTIME_FATAL_MARKERS):
            return "inability", ["runtime_fatal:" + phase], None
        return "inability", ["no_record:exit%d:%s" % (run["returncode"], phase)], None
    if {"observed": 0, "mismatch": 1, "inability": 2}.get(rec.get("outcome")) != run["returncode"]:
        return "inability", ["record_exit_inconsistent"], rec
    unreadable, mismatches = verify_runtime(rec, token, pins, mode)
    if unreadable:
        return "inability", unreadable, rec
    if rec["outcome"] in ("mismatch", "inability") and rec.get(MODE_DATA[mode]) is None:
        evidence = mismatches + (early_identity(rec) if early_identity else [])
        return rec["outcome"], ["child:" + str(rec.get("reason"))] + evidence, rec
    more_unreadable, more = specific(rec)
    unreadable, mismatches = more_unreadable, mismatches + more
    if unreadable:
        return "inability", unreadable, rec
    if rec["outcome"] == "inability":
        return "inability", ["child:" + str(rec.get("reason"))], rec
    if mismatches or rec["outcome"] == "mismatch":
        return "mismatch", mismatches or ["child:" + str(rec.get("reason"))], rec
    return "observed", [], rec


def derived_metrics(entry, run, rec):
    """The measurements of an observed child, computed here from raw readings."""
    m = rec["measurement"]
    row = {
        "op_ns": m["op_end"]["at_ns"] - m["op_start"]["at_ns"],
        "alloc_bytes": m["after"]["total_alloc_bytes"] - m["baseline"]["total_alloc_bytes"],
        "alloc_objects": m["after"]["mallocs"] - m["baseline"]["mallocs"],
        "stacks_bytes_after_aggregate": m["after"]["stacks_bytes"],
        "stacks_bytes_baseline_aggregate": m["baseline"]["stacks_bytes"],
        "memory_peak_bytes_whole_child": run["memory_peak"],
        "setup_ns": rec.get("setup_ns"),
    }
    if entry["op"] == "compile":
        row["live_heap_bytes_result_reachable"] = m["live"]["live_heap_bytes"]
        row["live_heap_bytes_baseline"] = m["baseline"]["live_heap_bytes"]
    return row


def control_verdict(expected, compatible, klass, reasons):
    """One control's verdict under the one outcome policy.

    The positive carrier: observed passes; a readable mismatch is mismatch; an
    inability stays an inability. A defect control passes only on its named
    refusal with a compatible class. A readable acceptance (observed) is an
    accepted defect: mismatch. A readable mismatch without the named refusal is a
    mismatched control, never a refusal: mismatch. Anything else (timeout, out of
    memory, no record, abnormal exit, another refusal code) is an inability.
    The verdict uses the classes of packet_exit: observed, mismatch, inability.
    """
    row = {"expected": expected or "observed", "class": klass, "got": reasons}
    if expected is None:
        row["verdict"], row["as"] = klass, {"observed": "observed", "mismatch": "carrier_mismatch"}.get(
            klass, "carrier_unobserved")
    elif klass == compatible and expected in reasons:
        row["verdict"], row["as"] = "observed", "refused"
    elif klass == "observed":
        row["verdict"], row["as"] = "mismatch", "accepted_defect"
    elif klass == "mismatch":
        row["verdict"], row["as"] = "mismatch", "mismatched_control"
    else:
        row["verdict"], row["as"] = "inability", "unobserved"
    return row


def packet_exit(classes):
    """Aggregate: any measured mismatch is 1; any other gap is 2; else 0."""
    if "mismatch" in classes:
        return EXIT_MISMATCH
    if any(c != "observed" for c in classes):
        return EXIT_INABILITY
    return EXIT_COHERENT


def skip_after_failure(entry, failed_at):
    """A failed point stops every larger point of the same cell."""
    limit = failed_at.get(entry["cell"])
    return limit is not None and entry["point"] > limit


def module_pins(payload_dir):
    """Read the Go and cedar-go pins from go.mod and go.sum, their only owners."""
    pins = {}
    with open(os.path.join(payload_dir, "go.mod"), encoding="utf-8") as f:
        for line in f:
            words = line.split()
            if words[:1] == ["toolchain"]:
                pins["go"] = words[1]
            elif words[:2] == ["require", "github.com/cedar-policy/cedar-go"]:
                pins["cedar_go"] = words[2]
    with open(os.path.join(payload_dir, "go.sum"), encoding="utf-8") as f:
        for line in f:
            words = line.split()
            if words[:2] == ["github.com/cedar-policy/cedar-go", pins.get("cedar_go")]:
                pins["cedar_go_sum"] = words[2]
    if set(pins) != {"go", "cedar_go", "cedar_go_sum"}:
        raise Inability("module_pins")
    return pins


# ------------------------------------------------------------------ running --

def child_env(workdir):
    env = {"PATH": "/usr/bin:/bin", "HOME": workdir, "LANG": "C"}
    env.update(CHILD_ENV)
    return env


class Evidence:
    """Writes evidence under out; nothing here is ever printed as policy text.

    A write that fails raises; the caller's one handler turns it into an
    inability with a fixed code.
    """

    def __init__(self, out):
        self.out = out
        self.logs = os.path.join(out, "logs")
        os.makedirs(self.logs, exist_ok=True)
        self.count = 0

    def child(self, label, run, row):
        self.count += 1
        stem = "%03d-%s" % (self.count, label.replace("/", "_"))
        with open(os.path.join(self.logs, stem + ".stdout"), "wb") as f:
            f.write(run["stdout"])
        with open(os.path.join(self.logs, stem + ".stderr"), "wb") as f:
            f.write(run["stderr"])
        with open(os.path.join(self.out, "children.jsonl"), "a", encoding="utf-8") as f:
            f.write(json.dumps(row, sort_keys=True) + "\n")

    def write(self, name, obj):
        with open(os.path.join(self.out, name), "w", encoding="utf-8") as f:
            json.dump(obj, f, indent=1, sort_keys=True)
            f.write("\n")


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 16), b""):
            h.update(chunk)
    return h.hexdigest()


def load_matrix(path):
    with open(path, encoding="utf-8") as f:
        m = json.load(f)
    if m.get("schema") != MATRIX_SCHEMA:
        raise Inability("matrix_schema")
    return m


class Runner:
    def __init__(self, args, parent, evidence, matrix):
        self.args = args
        self.parent = parent
        self.evidence = evidence
        self.matrix = matrix
        self.pins = module_pins(os.path.dirname(os.path.abspath(__file__)))
        self.workdir = os.path.join(args.out, "work")
        os.makedirs(self.workdir, exist_ok=True)
        os.chmod(self.workdir, 0o777)
        self.deadline = time.monotonic() + RUN_BUDGET_SECONDS
        self.serial = 0

    def budget_left(self):
        return self.deadline - time.monotonic() >= CHILD_SLOT_SECONDS

    def spawn(self, argv, group=None, keep=False):
        """Run one child in a fresh group (or the given one); return the run and group."""
        self.serial += 1
        if group is None:
            group = new_group(self.parent, "child-%03d" % self.serial)
        run = run_in_group(group, argv, child_env(self.workdir), self.workdir, CHILD_SECONDS)
        run["group"] = os.path.basename(group)
        if not keep:
            remove_group(group)
        return run, group

    def record_row(self, label, run, klass, reasons, rec, extra=None):
        row = {"id": label, "class": klass, "reasons": reasons, "group": run.get("group"),
               "exit": run["returncode"], "elapsed_s": run["elapsed_s"], "last_phase": last_phase(run["stderr"]),
               "memory_peak_bytes_whole_child": run["memory_peak"], "oom_kill": run["oom_kill"], "record": rec}
        row.update(extra or {})
        self.evidence.child(label, run, row)
        return row

    def measure(self, entry, control=None, verify_token=None, group=None, keep=False):
        token = secrets.token_hex(16)
        argv = [self.args.bin, "child", "-matrix", self.args.matrix, "-id", entry["id"], "-token", token]
        if control:
            argv += ["-control", control]
        run, group = self.spawn(argv, group, keep)
        klass, reasons, rec = assess(run, verify_token or token, self.pins, "child",
                                     lambda r: child_problems(entry, r),
                                     lambda r: child_identity(entry, r, present_only=True))
        extra = {"ordinal": entry.get("ordinal"), "control": control}
        if klass == "observed":
            extra["metrics"] = derived_metrics(entry, run, rec)
        self.record_row(entry["id"] + ("-" + control if control else ""), run, klass, reasons, rec, extra)
        return klass, reasons, group

    def validate(self):
        token = secrets.token_hex(16)
        run, _ = self.spawn([self.args.bin, "validate", "-matrix", self.args.matrix, "-token", token])
        expect = {"children": len(self.matrix["children"]), "low_points": len(self.matrix["low_points"])}
        klass, reasons, rec = assess(run, token, self.pins, "validate", lambda r: validate_problems(expect, r))
        self.record_row("validate", run, klass, reasons, rec)
        return klass, reasons

    def controls(self):
        """Run the positive carrier, the four defect carriers and the isolation guard.

        Every control keeps its class and reasons and gets one verdict under the
        one outcome policy (see control_verdict).
        """
        carrier = next(c for c in self.matrix["children"] if c["id"] == CARRIER_ID)
        results = {}
        klass, reasons, kept = self.measure(carrier, keep=True)
        results["positive-carrier"] = control_verdict(None, None, klass, reasons)
        klass, reasons, _ = self.measure(carrier, group=kept, keep=False)
        results["wrong-group"] = control_verdict("cgroup_not_fresh", "inability", klass, reasons)
        klass, reasons, _ = self.measure(carrier, control="skip-op")
        results["operation-not-executed"] = control_verdict("operation_not_executed", "mismatch", klass, reasons)
        klass, reasons, _ = self.measure(carrier, control="read-before")
        results["read-before-operation"] = control_verdict("reading_out_of_order", "mismatch", klass, reasons)
        klass, reasons, _ = self.measure(carrier, verify_token=secrets.token_hex(16))
        results["wrong-child"] = control_verdict("token", "mismatch", klass, reasons)
        # The real establish path on a root that is not a cgroup hierarchy. Its
        # refusal is the positive control of the isolation guard; a fallback that
        # returns a group is a readable acceptance of the defect.
        try:
            establish(root=self.workdir)
            klass, reasons = "observed", ["accepted"]
        except Inability as e:
            klass, reasons = "inability", [str(e)]
        results["missing-isolation"] = control_verdict("cgroup_v2_missing", "inability", klass, reasons)
        return results

    def run_matrix(self):
        klass, reasons = self.validate()
        if klass != "observed":
            code = EXIT_MISMATCH if klass == "mismatch" else EXIT_INABILITY
            self.evidence.write("summary.json", {"exit": code, "stopped": "validate", "validate": [klass, reasons]})
            return code
        controls = self.controls()
        code = packet_exit([c["verdict"] for c in controls.values()])
        self.evidence.write("controls.json", {"exit": code, "controls": controls})
        if code != EXIT_COHERENT:
            # Any control-phase failure stops the measurement matrix.
            self.evidence.write("summary.json", {"exit": code, "stopped": "controls"})
            return code
        classes, failed_at, rows = [], {}, []
        for entry in sorted(self.matrix["children"], key=lambda c: c["ordinal"]):
            if skip_after_failure(entry, failed_at):
                klass = "skipped_after_failure"
            elif not self.budget_left():
                klass = "not_run_budget"
            else:
                klass, _, _ = self.measure(entry)
            if klass != "observed":
                failed_at[entry["cell"]] = min(failed_at.get(entry["cell"], entry["point"]), entry["point"])
            classes.append(klass)
            rows.append({"id": entry["id"], "class": klass})
        code = packet_exit(classes)
        self.evidence.write("summary.json", {"exit": code, "children": rows,
                                             "counts": {k: classes.count(k) for k in sorted(set(classes))}})
        return code

    def run_oframe(self):
        classes, rows, documents = [], [], 0
        batches = [({"batch": i, "batches": OFRAME_BATCHES, "edges": False},
                    ["-batch", str(i), "-batches", str(OFRAME_BATCHES), "-corpus", self.args.corpus])
                   for i in range(OFRAME_BATCHES)] + [({"batch": 0, "batches": 1, "edges": True}, ["-edges"])]
        for expect, extra in batches:
            if not self.budget_left():
                classes.append("not_run_budget")
                rows.append({"id": "oframe-edges" if expect["edges"] else "oframe-%d" % expect["batch"],
                             "class": "not_run_budget"})
                continue
            token = secrets.token_hex(16)
            run, _ = self.spawn([self.args.bin, "oframe", "-token", token] + extra)
            klass, reasons, rec = assess(run, token, self.pins, "oframe", lambda r, x=expect: oframe_problems(x, r))
            label = "oframe-edges" if expect["edges"] else "oframe-%d" % expect["batch"]
            if klass == "observed" and not expect["edges"]:
                documents += rec["detail"]["agreement"]["documents"]
            classes.append(klass)
            rows.append(self.record_row(label, run, klass, reasons, rec))
        # Coverage is judged only when every corpus batch was observed; a batch that
        # was not observed is an inability, never a coverage mismatch.
        coverage = None
        if all(c == "observed" for c in classes):
            coverage = documents == OFRAME_CORPUS_DOCUMENTS
            if not coverage:
                classes.append("mismatch")
        code = packet_exit(classes)
        self.evidence.write("summary.json", {"exit": code, "packet": "oframe", "documents": documents,
                                             "expected_documents": OFRAME_CORPUS_DOCUMENTS, "coverage_ok": coverage,
                                             "children": [{"id": r["id"], "class": r["class"]} for r in rows]})
        return code


def environment_record(matrix_path):
    def text(path):
        try:
            return read_text(path).strip()
        except OSError:
            return None
    return {
        "kernel": os.uname().release,
        "machine": os.uname().machine,
        "cgroup_controllers": text(os.path.join(CGROUP_ROOT, "cgroup.controllers")),
        "cgroup_subtree_control": text(os.path.join(CGROUP_ROOT, "cgroup.subtree_control")),
        "python": sys.version.split()[0],
        "event": {k: os.environ.get(k) for k in EVENT_VARIABLES},
        "matrix_sha256": sha256_file(matrix_path) if matrix_path else None,
        "supervisor_sha256": sha256_file(os.path.abspath(__file__)),
        "limits": {"child_seconds": CHILD_SECONDS, "child_slot_seconds": CHILD_SLOT_SECONDS,
                   "child_memory_max": CHILD_MEMORY_MAX, "child_swap_max": CHILD_SWAP_MAX,
                   "child_pids_max": CHILD_PIDS_MAX, "child_env": CHILD_ENV,
                   "run_budget_seconds": RUN_BUDGET_SECONDS},
    }


# -------------------------------------------------------------------- setup --

def directory_bytes(path):
    total = 0
    for base, _, files in os.walk(path):
        for name in files:
            try:
                total += os.lstat(os.path.join(base, name)).st_size
            except OSError:
                pass
    return total


def run_setup(args, evidence):
    """Download, verify and build as the invoking user in a 4 GiB group.

    Module and build caches live under --build and are never evidence; the
    evidence keeps the commands, exits, times and `go version -m`.
    """
    parent = establish()
    build = args.build
    os.makedirs(build, exist_ok=True)
    os.chmod(build, 0o777)
    go_env = {"PATH": os.path.dirname(args.go) + ":/usr/bin:/bin", "HOME": build, "LANG": "C",
              "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=readonly", "GOWORK": "off", "CGO_ENABLED": "0",
              "GOPATH": os.path.join(build, "gopath"), "GOMODCACHE": os.path.join(build, "gomodcache"),
              "GOCACHE": os.path.join(build, "gocache")}
    binary = os.path.join(build, "bin", "cedar-measure")
    os.makedirs(os.path.dirname(binary), exist_ok=True)
    os.chmod(os.path.dirname(binary), 0o777)
    steps = [["go", "version"], ["go", "mod", "download"], ["go", "mod", "verify"],
             ["go", "build", "-trimpath", "-o", binary, "."], ["go", "version", "-m", binary]]
    started, log, code = time.monotonic(), [], EXIT_COHERENT
    for step in steps:
        left = int(SETUP_SECONDS - (time.monotonic() - started))
        if left < 5:
            code = EXIT_INABILITY
            log.append({"step": step, "class": "setup_budget"})
            break
        group = new_group(parent, "setup-%d" % len(log))
        set_group_limits(group, SETUP_MEMORY_MAX, SETUP_PIDS_MAX)
        run = run_in_group(group, [args.go] + step[1:], go_env, os.path.dirname(os.path.abspath(__file__)), left)
        remove_group(group)
        entry = {"step": step, "exit": run["returncode"], "elapsed_s": run["elapsed_s"],
                 "memory_peak_bytes": run["memory_peak"], "oom_kill": run["oom_kill"],
                 "stdout": run["stdout"].decode("utf-8", "replace"), "stderr": run["stderr"].decode("utf-8", "replace")}
        log.append(entry)
        if run["returncode"] != 0 or run["oom_kill"] or run["backstop"]:
            code = EXIT_INABILITY
            break
    disk = directory_bytes(build)
    if disk > SETUP_DISK_OBSERVED_MAX:
        code = EXIT_INABILITY
    evidence.write("setup.json", {"exit": code, "steps": log,
                                  "disk_bytes_observed_after_build": disk,
                                  "disk_note": "an observation after the build, not a bound on the download"})
    return code


# --------------------------------------------------------------------- main --

def inability_exit(evidence, reason):
    """Write the inability receipt if the sink works; otherwise a fixed code on stderr."""
    try:
        if evidence is None:
            raise OSError("no evidence sink")
        evidence.write("inability.json", {"exit": EXIT_INABILITY, "reason": reason})
    except Exception:  # the sink itself failed: only a fixed code remains
        print("cedar-measure supervisor: inability: evidence_unwritable", file=sys.stderr)
    print("cedar-measure supervisor: inability: %s" % reason, file=sys.stderr)
    return EXIT_INABILITY


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    p_probe = sub.add_parser("probe")
    p_probe.add_argument("--out", required=True)
    p_setup = sub.add_parser("setup")
    p_setup.add_argument("--out", required=True)
    p_setup.add_argument("--build", required=True)
    p_setup.add_argument("--go", required=True)
    p_run = sub.add_parser("run")
    p_run.add_argument("--out", required=True)
    p_run.add_argument("--packet", choices=["matrix", "oframe"], required=True)
    p_run.add_argument("--bin", required=True)
    p_run.add_argument("--matrix", required=True)
    p_run.add_argument("--corpus")
    args = parser.parse_args(argv)
    evidence = None
    try:
        os.makedirs(args.out, exist_ok=True)
        evidence = Evidence(args.out)
        if args.command == "probe":
            establish()
            evidence.write("environment.json", environment_record(None))
            return EXIT_COHERENT
        if args.command == "setup":
            return run_setup(args, evidence)
        matrix = load_matrix(args.matrix)
        if args.packet == "oframe" and not args.corpus:
            raise Inability("corpus_missing")
        parent = establish()
        evidence.write("environment.json", environment_record(args.matrix))
        runner = Runner(args, parent, evidence, matrix)
        return runner.run_matrix() if args.packet == "matrix" else runner.run_oframe()
    except Inability as e:
        return inability_exit(evidence, str(e))
    except Exception as e:  # a supervisor fault: its type only, never its message
        return inability_exit(evidence, "harness_fault:" + type(e).__name__)


if __name__ == "__main__":
    sys.exit(main())
