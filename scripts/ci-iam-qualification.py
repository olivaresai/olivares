#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a): see DISCLAIMER.md
"""Fixed public IAM qualification families. Runtime acceptance requires the artifact.

The registry declares tests before execution. This owner binds source, launches
one isolated producer, grades terminal events, and restores its disposable tree.
It does not accept a package, command, patch, ref or output path from a caller.
Only the families in FAMILIES are registered; any other name refuses at entry.
"""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import selectors
import signal
import stat
import subprocess
import sys
import time

# A family is added with its own registry, patches and review. Until then its
# name refuses at entry, and no evidence or result is written for it.
FAMILIES = ("ldap",)
SCHEMA = "olivares.public-iam-qualification/v2"
# The registry is public case data. It pins each input to the SHA-256 of the
# bytes this tree carries; any other key, such as provenance, refuses.
REGISTRY_KEYS = {"schema", "family", "engines", "packages", "commands", "production", "mutants",
                 "inputs", "maximum_phase_seconds", "test_owners", "oracle_owners"}
COMMAND_KEYS = {"argv", "wall_seconds", "package", "expected_tests", "allowed_skips"}
MUTANT_KEYS = {"id", "patch", "sha256", "red"}
ROOT = Path(__file__).resolve().parents[1]
CAPTURE_LIMIT = 32 * 1024 * 1024
ENVIRONMENT = ("PATH", "HOME", "TMPDIR", "GOTMPDIR", "GOCACHE", "GOMODCACHE",
               "OLIVARES_TEST_POSTGRES_DSN", "OLIVARES_TEST_POSTGRES_ADMIN_DSN",
               "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN", "OLIVARES_TEST_POSTGRES_OTHER_DSN",
               "OLIVARES_TEST_VECTOR_DSN")


class Inability(Exception):
    """Only fixed, non-sensitive stage names may cross this boundary."""


def require(condition, stage):
    if not condition:
        raise Inability(stage)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def regular(root, relative):
    parts = Path(relative).parts
    require(parts and not Path(relative).is_absolute() and
            all(p not in (".", "..") for p in parts), "unsafe-path")
    current = root
    require(current.is_dir() and not current.is_symlink(), "unsafe-root")
    try:
        for part in parts[:-1]:
            current /= part
            require(stat.S_ISDIR(current.lstat().st_mode), "unsafe-ancestor")
        path = current / parts[-1]
        require(stat.S_ISREG(path.lstat().st_mode), "non-regular-input")
    except FileNotFoundError:
        raise Inability("missing-input") from None
    return path


def write_json(path, value):
    with path.open("x", encoding="utf-8") as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write("\n")


def git(root, *args):
    # Local plumbing only. Never fetch, execute a hook or inherit a Git override.
    env = {k: os.environ[k] for k in ("PATH", "HOME") if k in os.environ}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null")
    try:
        result = subprocess.run(["git", "-c", "core.hooksPath=/dev/null", *args],
                                cwd=root, env=env, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=30, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise Inability("git-command-unobserved") from None
    require(result.returncode == 0, "git-command-failed")
    return result.stdout


def census(root):
    result = {}
    for entry in git(root, "ls-files", "--stage", "-z").split(b"\0"):
        if not entry:
            continue
        metadata, name = entry.split(b"\t", 1)
        mode, blob, stage = metadata.decode().split()
        require(mode in ("100644", "100755") and stage == "0", "non-regular-index")
        relative = name.decode("utf-8")
        require(relative not in result, "duplicate-index-path")
        path = regular(root, relative)
        result[relative] = {"sha256": digest(path.read_bytes()), "git_blob": blob,
                            "executable": bool(path.stat().st_mode & 0o111)}
    require(result, "empty-index")
    return result


def check_census(root, expected, changed=()):
    actual = census(root)
    require(actual.keys() == expected.keys(), "source-census-drift")
    for name, record in actual.items():
        if name not in changed:
            require(record == expected[name], "source-byte-drift")
    require(not git(root, "ls-files", "--others", "--exclude-standard").strip(),
            "untracked-source")


def registry(root, family, production_mutated=False):
    data = json.loads(regular(root, "ci/iam-qualification/" + family + ".json").read_text())
    require(set(data) == REGISTRY_KEYS and data["schema"] == SCHEMA and
            data["family"] == family, "case-schema")
    require(isinstance(data["mutants"], list) and
            all(isinstance(m, dict) and set(m) == MUTANT_KEYS for m in data["mutants"]),
            "case-schema")
    ids = [m["id"] for m in data["mutants"]]
    require(ids == ["m" + str(i) for i in range({"scim": 5, "workspace": 3, "ldap": 2}[family])],
            "case-mutant-census")
    require(set(data["commands"]) == set(ids + ["baseline", "race"] +
                                          (["compatibility"] if family == "scim" else [])),
            "case-command-census")
    for command in data["commands"].values():
        require(isinstance(command, dict) and set(command) == COMMAND_KEYS, "case-schema")
        argv = command["argv"]
        require(argv[:4] == ["go", "test", "-json", "-count=1"] and
                re.fullmatch(r"-timeout=[1-9][0-9]*m", argv[4]) and
                argv[-2] == "-run" and argv[-3][2:] in data["packages"] and
                argv[5:-3] in ([], ["-race"]) and argv[-1].startswith("^(") and
                argv[-1].endswith("$"), "case-argv")
        require(command["package"] == "github.com/olivaresai/olivares/" + argv[-3][2:],
                "case-package")
        tests = command["expected_tests"]
        require(tests and len(tests) == len(set(tests)) and
                all(re.match(r"^Test[A-Za-z0-9_]+(?:/.*)?$", t) for t in tests),
                "case-test-census")
        require(set(command["allowed_skips"]) <= set(tests) and
                set(command["allowed_skips"]) <= {"TestLDAPWireChild"}, "case-skips")
        require(0 < command["wall_seconds"] <= 2400, "case-ceiling")
    roots = {command["package"] + "::" + test.split("/", 1)[0]
             for command in data["commands"].values() for test in command["expected_tests"]}
    require(set(data["test_owners"]) == roots, "case-test-owner-census")
    for key, path in data["test_owners"].items():
        package, test = key.split("::")
        require(path.endswith("_test.go") and
                "github.com/olivaresai/olivares/" + str(Path(path).parent) == package and
                re.search(r"^func " + re.escape(test) + r"\(", regular(root, path).read_text(), re.M),
                "case-test-owner")
    require(all(path.endswith("_test.go") and symbols and
                all(isinstance(name, str) and name.isidentifier() for name in symbols)
                for path, symbols in data["oracle_owners"].items()), "case-oracle-owner")
    require(set(data["inputs"]) == set(data["production"]) |
            set(data["test_owners"].values()) | set(data["oracle_owners"]), "case-owner-input-census")
    for path, pinned in data["inputs"].items():
        require(isinstance(pinned, str) and re.fullmatch(r"[0-9a-f]{64}", pinned), "case-input-pin")
        if production_mutated and path in data["production"]:
            continue  # the parent checked the exact production-only patch
        require(digest(regular(root, path).read_bytes()) == pinned, "case-input-drift")
    require(set(data["production"]) <= set(data["inputs"]) and
            all(not p.endswith("_test.go") for p in data["production"]), "case-production")
    for mutant in data["mutants"]:
        patch = regular(root, mutant["patch"]).read_bytes()
        require(digest(patch) == mutant["sha256"], "patch-byte-drift")
        # Reversed git diffs retain an a/ new-file prefix. Both are ordinary
        # strip-one patches; the post-apply census corroborates their scope.
        paths = re.findall(rb"^\+\+\+ [ab]/([^\n]+)$", patch, re.M)
        require(paths and all(p.decode() in data["production"] for p in paths), "patch-scope")
        tests = set(data["commands"][mutant["id"]]["expected_tests"])
        red = mutant["red"]
        require(red and set(red) < tests and all(markers and all(isinstance(m, str) and m
                for m in markers) for markers in red.values()), "case-red-contract")
    maximum = sum(c["wall_seconds"] for k, c in data["commands"].items() if k not in ids)
    maximum += sum(3 * data["commands"][name]["wall_seconds"] for name in ids)
    require(maximum == data["maximum_phase_seconds"] <= 330 * 60, "case-total-ceiling")
    return data


COMMIT = r"[0-9a-f]{40}"


def typed(value, pattern, name, unrecognized):
    """The value when it is a string of the declared shape; None when absent or not."""
    if value is None:
        return None
    if isinstance(value, str) and re.fullmatch(pattern, value):
        return value
    unrecognized.append(name)
    return None


def member(value, *keys):
    for key in keys:
        value = value.get(key) if isinstance(value, dict) else None
    return value


def observe_identity(root):
    """Read the run's identity once, as a closed set of typed public identifiers.

    GITHUB_SHA is the run's merge commit on refs/pull/<number>/merge and
    pull_request.head.sha the PR head. The payload merge_commit_sha is the
    asynchronous test-merge candidate, so it is kept as advisory evidence only.
    The record keeps six typed GITHUB_* values (event name, repository, SHA,
    ref, run id and attempt), local commit IDs, typed event fields and
    comparison outcomes. Other environment inputs are reduced to booleans or
    only locate the event file; no event text or user field is kept.
    """
    env, unrecognized = os.environ, []
    try:
        event_bytes = Path(env["GITHUB_EVENT_PATH"]).read_bytes()
        event = json.loads(event_bytes)
    except (KeyError, OSError, ValueError, RecursionError):
        # RecursionError: nesting beyond the decoder's depth is a malformed event shape.
        event_bytes = event = None
    if not isinstance(event, dict):
        event = None
        unrecognized.append("event")
    number = member(event, "number")
    if number is not None and not (type(number) is int and 0 < number < 2 ** 31):
        number = None
        unrecognized.append("event.number")
    pr = member(event, "pull_request")
    mergeable = member(pr, "mergeable")
    if mergeable is not None and type(mergeable) is not bool:
        mergeable = None
        unrecognized.append("event.mergeable")
    fields = {
        "number": number,
        "head_sha": typed(member(pr, "head", "sha"), COMMIT, "event.head_sha", unrecognized),
        "base_ref": typed(member(pr, "base", "ref"), r"[A-Za-z0-9._/-]{1,255}", "event.base_ref", unrecognized),
        "base_repository": typed(member(pr, "base", "repo", "full_name"), r"[A-Za-z0-9._-]{1,100}/[A-Za-z0-9._-]{1,100}",
                                 "event.base_repository", unrecognized),
        "base_sha": typed(member(pr, "base", "sha"), COMMIT, "event.base_sha", unrecognized),
        "merge_commit_sha": typed(member(pr, "merge_commit_sha"), COMMIT, "event.merge_commit_sha", unrecognized),
        "mergeable": mergeable,
    }
    checkout = typed(git(root, "rev-parse", "HEAD").decode().strip(), COMMIT, "checkout", unrecognized)
    parents = git(root, "rev-list", "--parents", "-n", "1", "HEAD").decode().split()[1:]
    parents = [typed(p, COMMIT, "checkout_parents", unrecognized) for p in parents]
    observation = {
        "schema": "olivares.public-iam-identity-observation/v1",
        "hosted": env.get("GITHUB_ACTIONS") == "true" and env.get("RUNNER_ENVIRONMENT") == "github-hosted",
        "event_name": typed(env.get("GITHUB_EVENT_NAME"), r"[a-z_]{1,64}", "event_name", unrecognized),
        "repository": typed(env.get("GITHUB_REPOSITORY"), r"[A-Za-z0-9._-]{1,100}/[A-Za-z0-9._-]{1,100}",
                            "repository", unrecognized),
        "goflags_set": bool(env.get("GOFLAGS")),
        "workspace_is_checkout": "GITHUB_WORKSPACE" in env and Path(env["GITHUB_WORKSPACE"]).resolve() == root,
        "github_sha": typed(env.get("GITHUB_SHA"), COMMIT, "github_sha", unrecognized),
        "ref": typed(env.get("GITHUB_REF"), r"refs/pull/[1-9][0-9]{0,9}/merge", "ref", unrecognized),
        "run_id": typed(env.get("GITHUB_RUN_ID"), r"[0-9]{1,20}", "run_id", unrecognized),
        "run_attempt": typed(env.get("GITHUB_RUN_ATTEMPT"), r"[0-9]{1,20}", "run_attempt", unrecognized),
        "checkout": checkout,
        "checkout_clean": not git(root, "status", "--porcelain").strip(),
        "checkout_parents": parents,
        "event_sha256": None if event_bytes is None else digest(event_bytes),
        "event": fields,
        "unrecognized": sorted(set(unrecognized)),
    }
    two = len(parents) == 2 and None not in parents and parents[0] != parents[1]
    observation["comparisons"] = {
        "checkout_is_github_sha": checkout is not None and checkout == observation["github_sha"],
        "ref_is_event_merge_ref": number is not None and observation["ref"] == "refs/pull/%d/merge" % number,
        "two_distinct_parents": two,
        "second_parent_is_pr_head": two and fields["head_sha"] is not None and parents[1] == fields["head_sha"],
        "base_is_main_of_repository": fields["base_ref"] == "main" and
                                      fields["base_repository"] is not None and
                                      fields["base_repository"] == observation["repository"],
        # Advisory only: neither value is documented as the run's checkout or merge parent.
        "candidate_is_checkout": (None if fields["merge_commit_sha"] is None
                                  else fields["merge_commit_sha"] == checkout),
        "first_parent_is_event_base_sha": (None if fields["base_sha"] is None or not two
                                           else parents[0] == fields["base_sha"]),
    }
    return observation


def admit_identity(observation):
    """Admit only from the recorded observation; each refusal names its own stage."""
    o, event, compare = observation, observation["event"], observation["comparisons"]
    require(o["hosted"], "hosted-only")
    require(o["event_name"] == "pull_request" and o["repository"] == "olivaresai/olivares", "public-pr-only")
    require(not o["goflags_set"], "caller-goflags")
    require(o["workspace_is_checkout"], "checkout-path")
    require(compare["checkout_is_github_sha"], "checkout-identity")
    require(o["checkout_clean"], "checkout-dirty")
    require(not any(n == "event" or n.startswith("event.") for n in o["unrecognized"]) and
            None not in (event["number"], event["head_sha"], event["base_ref"], event["base_repository"]),
            "pr-event-shape")
    require(compare["base_is_main_of_repository"], "pr-base-identity")
    require(compare["ref_is_event_merge_ref"], "pr-merge-ref")
    # Inferred, fail-closed merge topology: GitHub documents GITHUB_SHA and the PR
    # head, not the parent order of the merge commit.
    require(compare["two_distinct_parents"], "pr-merge-topology")
    require(compare["second_parent_is_pr_head"], "pr-parent-identity")
    require(o["run_id"] is not None and o["run_attempt"] is not None, "run-identity")
    return {"checkout": o["checkout"], "event_sha256": o["event_sha256"], "pull_request": event["number"],
            "pr_head": event["head_sha"], "repository": o["repository"],
            "run_id": o["run_id"], "run_attempt": o["run_attempt"]}


def safe_environment():
    env = {k: os.environ[k] for k in ENVIRONMENT if k in os.environ}
    env.update(GOENV="off", GOTOOLCHAIN="local", GOFLAGS="-p=2", GOMAXPROCS="2",
               GOWORK=str(ROOT / "go.work"), OLIVARES_TEST_POSTGRES_REQUIRED="1",
               PYTHONDONTWRITEBYTECODE="1", LC_ALL="C.UTF-8")
    return env


def producer(family, name):
    """Only entered by the owned anchor; multiplex without losing either pipe.

    Complete lines are redacted before writing frames. A bounded oversized line
    fails as inability rather than splitting a credential across redaction calls.
    """
    require(os.environ.get("GITHUB_ACTIONS") == "true" and
            os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted" and
            os.environ.get("GITHUB_REPOSITORY") == "olivaresai/olivares" and
            ROOT == Path(os.environ["RUNNER_TEMP"]) / ("iam-qualification-" + family + "-source"),
            "producer-owned-hosted-tree")
    data = registry(ROOT, family, production_mutated=True)
    require(name in data["commands"], "producer-case")
    env = safe_environment()
    secrets = [v for k, v in env.items() if k.endswith("_DSN") and v]
    secrets += [json.dumps(v)[1:-1] for v in list(secrets)]

    def emit(stream, raw):
        value = raw.decode("utf-8", "strict")
        for secret in sorted(set(secrets), key=len, reverse=True):
            value = value.replace(secret, "[REDACTED_DSN]")
        value = re.sub(r"postgres(?:ql)?://[^\s\"']+", "[REDACTED_DSN]", value)
        print(json.dumps({"stream": stream, "text": value}), flush=True)

    command = ["bash", "scripts/with-pg-env.sh", *data["commands"][name]["argv"]]
    process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    monitor = selectors.DefaultSelector()
    for stream, pipe in (("stdout", process.stdout), ("stderr", process.stderr)):
        os.set_blocking(pipe.fileno(), False)
        monitor.register(pipe, selectors.EVENT_READ, (stream, bytearray()))
    while monitor.get_map():
        for key, _ in monitor.select(0.2):
            stream, pending = key.data
            chunk = os.read(key.fd, 65536)
            if not chunk:
                if pending:
                    emit(stream, bytes(pending))
                monitor.unregister(key.fileobj)
                key.fileobj.close()
                continue
            pending.extend(chunk)
            require(len(pending) <= 2 * 1024 * 1024, "producer-line-limit")
            while b"\n" in pending:
                line, _, remaining = pending.partition(b"\n")
                pending[:] = remaining
                emit(stream, bytes(line) + b"\n")
    rc = process.wait()
    print(json.dumps({"producer_exit": rc}), flush=True)
    return rc if rc >= 0 else 128 - rc


def grade(command, events, rc, red=None):
    """0 expected outcome; 1 measured finding; 2 incomplete/unrelated inability."""
    expected = set(command["expected_tests"])
    skips = set(command["allowed_skips"])
    runs, terminal, output = set(), {}, {}
    package_start = package_terminal = None
    try:
        for line in events.splitlines():
            event = json.loads(line)
            if not isinstance(event, dict) or event.get("Package") != command["package"]:
                return 2, "foreign-or-build-event"
            action, test = event.get("Action"), event.get("Test")
            if test is not None:
                if test not in expected or test in terminal:
                    return 2, "unexpected-test-or-after-terminal"
                if action == "run":
                    if test in runs:
                        return 2, "duplicate-run"
                    runs.add(test)
                elif action == "output":
                    output[test] = output.get(test, "") + event.get("Output", "")
                elif action in ("pass", "fail", "skip"):
                    if test not in runs:
                        return 2, "terminal-without-run"
                    terminal[test] = action
                elif action not in ("pause", "cont"):
                    return 2, "unknown-test-event"
            elif action == "start":
                if package_start is not None:
                    return 2, "duplicate-package-start"
                package_start = True
            elif action in ("pass", "fail", "skip"):
                if package_terminal is not None:
                    return 2, "duplicate-package-terminal"
                package_terminal = action
            elif action != "output" or package_terminal is not None:
                return 2, "unknown-package-event"
        if package_start is None or set(terminal) != expected or not package_terminal:
            return 2, "missing-terminal"
        if {t for t, state in terminal.items() if state == "skip"} != skips:
            return 2, "skip-contract"
        failures = {t for t, state in terminal.items() if state == "fail"}
        if not failures:
            if rc != 0 or package_terminal != "pass":
                return 2, "package-failure-without-test-failure"
            return (1, "mutant-survived") if red else (0, "all-expected-pass")
        if rc != 1 or package_terminal != "fail":
            return 2, "terminal-exit-disagreement"
        if not red:
            return 1, "measured-test-failure"
        expected_failures = {test for test in expected if any(
            leaf == test or leaf.startswith(test + "/") for leaf in red)}
        if failures != expected_failures:
            return 2, "unrelated-or-missing-failure"
        if any(not all(marker in output.get(test, "") for marker in markers)
               for test, markers in red.items()):
            return 2, "causal-assertion-missing"
        return 0, "declared-assertions-rejected-mutant"
    except (ValueError, TypeError, KeyError):
        return 2, "unreadable-test-events"


class Qualification:
    def __init__(self, family, evidence):
        self.family, self.evidence = family, evidence
        self.tree = evidence.parent / (evidence.name + "-source")
        self.custody = True
        self.created = False
        self.restored = True
        self.phases = []
        self.saved = {}
        self.data = self.source = None
        self.interrupted = False

    def stop(self, *_):
        self.interrupted = True

    def unchanged(self, changed=()):
        require(not self.interrupted, "interrupted-between-phases")
        check_census(self.tree, self.source, changed)

    def phase(self, label, name, red=None):
        require(not self.interrupted, "interrupted-between-phases")
        require(self.custody, "previous-custody-unobserved")
        command = self.data["commands"][name]
        directory = self.evidence / label
        directory.mkdir()
        spec = importlib.util.spec_from_file_location("iam_capture", self.tree / "scripts/lib/preverify-capture.py")
        capture = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(capture)
        capture.DEADLINE = command["wall_seconds"]
        capture.LIMIT = CAPTURE_LIMIT
        handlers = {s: signal.getsignal(s) for s in (signal.SIGTERM, signal.SIGINT, signal.SIGCHLD)}
        summary = io.StringIO()
        started = time.monotonic()
        require(not self.interrupted, "interrupted-before-capture")
        self.custody = False
        try:
            with contextlib.redirect_stdout(summary):
                rc = capture.capture(str(directory / "streams.jsonl"), str(self.tree),
                                     [sys.executable, "-B", "scripts/ci-iam-qualification.py",
                                      "_producer", self.family, name], cancellation=self)
        finally:
            for sig, handler in handlers.items():
                signal.signal(sig, handler)
        elapsed = time.monotonic() - started
        match = re.fullmatch(r"producer=(\d+|unknown) capture=(\w+) bytes=(\d+|unknown) "
                             r"completeness=(\w+) custody=(\w+)\n", summary.getvalue())
        self.custody = bool(match and match[5] in ("none", "term", "kill"))
        if match and match[2] == "interrupted":
            self.interrupted = True  # restore safely, but launch no successor
        outcome, reason = 2, "capture-incomplete"
        terminal = None
        valid = True
        with (directory / "go.jsonl").open("x") as stdout, (directory / "stderr.txt").open("x") as stderr:
            for line in (directory / "streams.jsonl").read_text().splitlines():
                try:
                    frame = json.loads(line)
                    if set(frame) == {"stream", "text"} and frame["stream"] in ("stdout", "stderr") and terminal is None:
                        (stdout if frame["stream"] == "stdout" else stderr).write(frame["text"])
                    elif set(frame) == {"producer_exit"} and terminal is None and type(frame["producer_exit"]) is int:
                        terminal = frame["producer_exit"]
                    else:
                        valid = False
                except (ValueError, TypeError):
                    valid = False
        if valid and self.custody and match[2] == "eof" and match[4] == "complete" and terminal == rc:
            outcome, reason = grade(command, (directory / "go.jsonl").read_text(), rc, red)
        result = {"label": label, "command": ["bash", "scripts/with-pg-env.sh", *command["argv"]],
                  "effective_goflags": "-p=2", "ceiling_seconds": command["wall_seconds"],
                  "elapsed_seconds": elapsed, "capture_rc": rc, "producer_rc": terminal,
                  "capture": summary.getvalue().strip(), "custody_terminal": self.custody,
                  "outcome": outcome, "reason": reason,
                  "expected_tests": command["expected_tests"], "allowed_skips": command["allowed_skips"],
                  "source_hashes": {p: digest(regular(self.tree, p).read_bytes()) for p in self.data["inputs"]}}
        write_json(directory / "status.json", result)
        self.phases.append(result)
        return outcome

    def restore(self):
        require(self.custody, "restore-blocked-by-custody")
        # Only the disposable checkout is writable; lstat checks precede each open.
        for path, data in self.saved.items():
            target = regular(self.tree, path)
            fd = os.open(target, os.O_WRONLY | os.O_TRUNC | os.O_NOFOLLOW)
            with os.fdopen(fd, "wb") as output:
                output.write(data)
        check_census(self.tree, self.source)
        self.restored = True

    def run(self):
        # Retained before admission, so a refusal still shows which comparison failed.
        observation = observe_identity(ROOT)
        write_json(self.evidence / "identity-observation.json", observation)
        identity_record = admit_identity(observation)
        self.data = registry(ROOT, self.family)
        self.source = census(ROOT)
        write_json(self.evidence / "identity.json", identity_record)
        write_json(self.evidence / "inputs.json", self.source)
        write_json(self.evidence / "case.json", self.data)
        require(not self.tree.exists() and not self.tree.is_symlink(), "owned-tree-exists")
        git(ROOT, "worktree", "add", "--detach", str(self.tree), identity_record["checkout"])
        self.created = True
        self.unchanged()
        self.saved = {p: regular(self.tree, p).read_bytes() for p in self.data["production"]}
        for name in ("baseline", "race", "compatibility"):
            if name in self.data["commands"]:
                self.unchanged()
                result = self.phase(name, name)
                self.unchanged()
                if result:
                    return result, "clean-qualification-rejected"
        for mutant in self.data["mutants"]:
            name = mutant["id"]
            self.unchanged()
            result = self.phase(name + "-before", name)
            self.unchanged()
            if result:
                return result, "pre-mutation-control-rejected"
            patch = regular(self.tree, mutant["patch"])
            require(digest(patch.read_bytes()) == mutant["sha256"], "patch-byte-drift")
            git(self.tree, "apply", "--check", str(patch))
            self.restored = False
            try:
                git(self.tree, "apply", str(patch))
                self.unchanged(self.data["production"])
                changed = git(self.tree, "diff", "--name-only").decode().splitlines()
                require(changed and set(changed) <= set(self.data["production"]), "applied-patch-scope")
                result = self.phase(name + "-mutated", name, mutant["red"])
                self.unchanged(self.data["production"])
            finally:
                if self.custody:
                    self.restore()
            if not self.custody:
                return 2, "owned-processes-not-terminal"
            restored = self.phase(name + "-restored", name)
            self.unchanged()
            if result or restored:
                return (2 if 2 in (result, restored) else 1), "mutation-or-restored-control-rejected"
        return 0, "fixed-family-qualified"

    def close(self):
        if self.created and self.custody:
            if not self.restored:
                self.restore()
            check_census(self.tree, self.source)
            git(ROOT, "worktree", "remove", str(self.tree))
            require(not self.tree.exists(), "owned-tree-removal-unobserved")
            self.created = False


def finalize(evidence, result):
    hashes = {}
    for path in sorted(evidence.rglob("*")):
        require(not path.is_symlink(), "artifact-symlink")
        if path.is_file():
            hashes[path.relative_to(evidence).as_posix()] = digest(path.read_bytes())
    complete = result["outcome"] != 0 or all(p in hashes for p in
               ("identity-observation.json", "identity.json", "inputs.json", "case.json",
                "baseline/status.json"))
    for phase in result["phases"]:
        complete = complete and all(phase["label"] + "/" + name in hashes for name in
                                    ("streams.jsonl", "go.jsonl", "stderr.txt", "status.json"))
    if not complete:
        result["outcome"], result["reason"] = 2, "required-artifact-missing"
    write_json(evidence / "RESULT.json", result)
    hashes["RESULT.json"] = digest((evidence / "RESULT.json").read_bytes())
    write_json(evidence / "SHA256.json", hashes)
    return result["outcome"], result["reason"]


def main(family):
    parent = Path(os.environ["RUNNER_TEMP"])
    require(parent.is_dir() and not parent.is_symlink() and parent.resolve() == parent, "runner-temp-path")
    evidence = parent / ("iam-qualification-" + family)
    evidence.mkdir(mode=0o700)  # no overwrite, including a prior attempt's evidence
    qualification = Qualification(family, evidence)
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, qualification.stop)
    outcome, reason = 2, "unobserved"
    try:
        outcome, reason = qualification.run()
    except Inability as error:
        reason = str(error)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        reason = "unreadable-or-unavailable-input"
    finally:
        try:
            qualification.close()
        except (Inability, OSError, ValueError):
            outcome, reason = 2, "restore-or-cleanup-unobserved"
        if qualification.interrupted:
            outcome, reason = 2, "interrupted"
        result = {"schema": "olivares.public-iam-result/v1", "family": family,
                  "outcome": outcome, "reason": reason, "phases": qualification.phases,
                  "custody_terminal": qualification.custody, "restored": qualification.restored,
                  "owned_tree_retained": qualification.created,
                  "qualification_scope": "fixed cases only; not complete IAM or release acceptance"}
        outcome, reason = finalize(evidence, result)
    print("iam-qualification: family=%s outcome=%d stage=%s" % (family, outcome, reason))
    return outcome


if __name__ == "__main__":
    try:
        if len(sys.argv) == 4 and sys.argv[1] == "_producer" and sys.argv[2] in FAMILIES:
            sys.exit(producer(sys.argv[2], sys.argv[3]))
        require(len(sys.argv) == 2 and sys.argv[1] in FAMILIES, "usage-fixed-family")
        sys.exit(main(sys.argv[1]))
    except (Inability, OSError, ValueError, KeyError, TypeError):
        print("iam-qualification: outcome=2 stage=entry-unavailable", file=sys.stderr)
        sys.exit(2)
