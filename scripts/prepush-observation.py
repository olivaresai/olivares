# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""GATE-O1 recorder and read-only summarizer for feature-hook task occurrences.

WHAT THIS IS. An opt-in, observation-only instrument for `.githooks/pre-push`. It
records ONE top-level go-task occurrence per literal `task ...` call the hook makes
on the actual `fast` ref class, including whatever descendants go-task owns, without
attributing duration or status to any inner script. It changes no gate verdict, skips
nothing, caches nothing, schedules nothing and never retries a task.

WHAT IT IS NOT, and each line is a boundary the contract draws on purpose:

  * NOT a gate. Every failure here is an OBSERVATION failure. The shell adapter runs
    the task once and returns the task's own status whatever happens in this file.
  * NOT proof of the executed bytes. The hook copies itself to a snapshot, execs it
    and UNLINKS it (see the INSTANTANEA block of the hook). By the time this recorder
    runs, the executing bytes have no name in the filesystem. Repository HEAD/tree and
    the tracked blob of `.githooks/pre-push` are INITIALIZATION CONTEXT; the executing
    snapshot identity is reported as `unverified` and never conflated with them.
  * NOT a push receipt. The parent push outcome comes from the orchestrator's own
    receipt, which this file only READS and only in one pinned schema. A successful
    last task is not completion evidence, and this summarizer refuses to say it is.
  * NOT a place for secrets. Only an allowlisted, bounded, typed field set is
    persisted: task name by grammar, ordinal, caller line, UTC/monotonic instants and
    the shell status. Never argv, environment, tool output, remote URLs or contents.
    Extra unexpected arguments become a COUNT and a stated limitation, never values.

FOUR OPERATIONS, FIXED ARGV, NO SHELL TEXT.

  init    <parent> <class> <commit> <tree> <tracked> <bash_version>  -> attempt token
  start   <parent> <attempt> <task_name> <extra_args> <caller_line>  -> occurrence token
  end     <parent> <attempt> <occurrence> <shell_status>             -> no data
  summary <parent> <attempt> <parent_receipt_path>                   -> JSON report

Every operation is STATELESS and revalidates the whole boundary from the privately
held parent path plus PATH-FREE tokens: no inherited descriptor, no exported setting,
no global registry, no ambient process state. Directory descriptors are opened
component by component with O_NOFOLLOW so a symlink anywhere on the output boundary
is a refusal rather than a traversal.

THE PROTOCOL IS BOUNDED BECAUSE THE READER IS BASH. init/start print exactly one
ASCII line, at most 128 bytes, matching `[a-z0-9_-]{1,80}` and containing no path.
Diagnostics are one fixed code on stderr, chosen from a closed set below; they carry
no path, no errno text, no argv and no content. Exit codes belong to observation and
never replace a task result.
"""

from __future__ import annotations

import datetime
import errno
import fcntl
import json
import math
import os
import re
import secrets
import stat
import sys
import time

# ---------------------------------------------------------------------------------
# Fixed vocabulary. The exit code IS the diagnostic; the stderr token repeats it so a
# human reading a transcript does not have to memorise numbers. Nothing else is ever
# written to stderr.
# ---------------------------------------------------------------------------------
E_OK = 0
E_ARGV = 2          # argument count/grammar
E_BOUNDARY = 3      # output parent rejected (location, symlink, mode, filesystem)
E_OWNERSHIP = 4     # wrong owner or too-broad mode on an owned directory
E_ATTEMPT = 5       # attempt token does not resolve to an owned attempt
E_OCCURRENCE = 6    # occurrence token does not resolve inside that attempt
E_LOCK_BUSY = 7     # nonblocking bookkeeping lock was already held
E_WRITE = 8         # create/write/close/rename failure
E_PYTHON = 9        # interpreter below the supported minimum
E_RECEIPT = 10      # parent receipt absent or not the pinned schema (summary only)
E_RECORD = 11       # persisted record malformed (summary only)
E_INTERNAL = 12     # unexpected internal condition
E_NAME = 13         # task name outside the attempt's fixed reviewed allowlist

_CODE_NAME = {
    E_ARGV: "E_ARGV",
    E_BOUNDARY: "E_BOUNDARY",
    E_OWNERSHIP: "E_OWNERSHIP",
    E_ATTEMPT: "E_ATTEMPT",
    E_OCCURRENCE: "E_OCCURRENCE",
    E_LOCK_BUSY: "E_LOCK_BUSY",
    E_WRITE: "E_WRITE",
    E_PYTHON: "E_PYTHON",
    E_RECEIPT: "E_RECEIPT",
    E_RECORD: "E_RECORD",
    E_INTERNAL: "E_INTERNAL",
    E_NAME: "E_NAME",
}

SCHEMA = 1
RECORDER_MECHANISM = "olivares-gate-observation/file-recorder"
RECORDER_VERSION = "1"
MIN_PYTHON = (3, 11)
MAX_RECORD_BYTES = 64 * 1024
MAX_PROTOCOL_BYTES = 128
MAX_OCCURRENCES = 100000

# Token grammar. A token starts with a lowercase letter ON PURPOSE: that makes `.`,
# `..` and a leading `-` unrepresentable, so a token can never name the parent
# directory, the attempt's own entry or an option to a later command.
TOKEN_RE = re.compile(r"\A[a-z][a-z0-9_-]{7,79}\Z")
TASK_NAME_RE = re.compile(r"\A[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}\Z")
OID_RE = re.compile(r"\A(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")
BASH_VERSION_RE = re.compile(r"\A[0-9]{1,3}\.[0-9]{1,3}(?:\.[0-9]{1,3})?\Z")
COUNT_RE = re.compile(r"\A(?:0|[1-9][0-9]{0,4})\Z")
LINE_RE = re.compile(r"\A[1-9][0-9]{0,6}\Z")
STATUS_RE = re.compile(r"\A(?:0|[1-9][0-9]{0,2})\Z")
UNAVAILABLE = "unavailable"

# ⛔ THE OUTPUT FILESYSTEM IS ADMITTED BY AN ALLOWLIST, NOT REFUSED BY A DENYLIST.
#
# The first revision rejected a list of network types and enabled everything else,
# including `unavailable` — the value this file returns when /proc/self/mountinfo cannot
# be read or no mount matches. Independent review returned that as O1-F3: inability to
# prove the target local is not permission to enable timing and locking on it. The
# contract qualifies the enabled path for a local filesystem, so only a positively
# identified local type qualifies and everything else — unknown, malformed, network, or
# unavailable — declines.
SUPPORTED_LOCAL_FSTYPES = frozenset(
    {"ext2", "ext3", "ext4", "xfs", "btrfs", "f2fs", "jfs", "reiserfs", "tmpfs"}
)

# Kept only so a refusal can say WHICH kind of unsupported target it saw. It has no
# effect on the decision: membership in SUPPORTED_LOCAL_FSTYPES is the whole rule.
NETWORK_FSTYPES = frozenset(
    {
        "nfs", "nfs4", "cifs", "smb3", "smbfs", "afs", "9p", "ceph",
        "glusterfs", "lustre", "fuse.sshfs", "fuse.s3fs", "fuse.rclone",
        "gfs2", "ocfs2", "beegfs", "davfs",
    }
)

# Formats used by the strict record validators below.
ATTEMPT_TOKEN_RE = re.compile(r"\Aatt-[0-9a-f]{32}\Z")
OCCURRENCE_TOKEN_RE = re.compile(r"\Aocc-[0-9a-f]{32}\Z")
SHA256_RE = re.compile(r"\A[0-9a-f]{64}\Z")
PYTHON_VERSION_RE = re.compile(r"\A[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\Z")
DIGITS_RE = re.compile(r"\A[0-9]{1,20}\Z")
UTC_RE = re.compile(r"\A\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?\+00:00\Z")

# Fixed prose that belongs to the record contract. Each is written and validated from
# the same constant, so the writer and the validator cannot drift apart.
CONTEXT_MEANING = "initialization context; not proof of working-tree inputs"
SNAPSHOT_IDENTITY = "unverified"
SNAPSHOT_REASON = "the hook execs an unlinked snapshot; its bytes were not identified"
CALLER_LINE_MEANING = "the executing instrumented hook version"
SHELL_STATUS_NOTE = (
    "top-level go-task shell status; >128 is a shell status, not proof of a signal"
)
EXTRA_ARG_NOTE = "unexpected arguments are counted, never recorded"
ATTEMPT_LIMITATIONS = [
    "one top-level go-task occurrence per call; inner scripts are not attributed",
    "repository context is not the executing hook snapshot",
    "telemetry integrity is not a gate or product verdict",
]
DERIVATION_DERIVED = "repository_hook_file"
DERIVATION_UNAVAILABLE = UNAVAILABLE
COMPLETION_STARTED = "started"
COMPLETION_COMPLETED = "completed"
DURATION_ERRORS = frozenset({"nonmonotonic", "missing_start"})

ATTEMPT_FILE = "attempt.json"
OCCURRENCE_DIR = "occurrences"
BOOKKEEPING_FILE = "bookkeeping.json"

# The instrument's own source paths are FIXED and derived from this file, never from a
# caller argument: a caller cannot point the recorder at another code path.
RECORDER_PATH = os.path.realpath(__file__)
REPO_ROOT = os.path.dirname(os.path.dirname(RECORDER_PATH))
ADAPTER_PATH = os.path.join(REPO_ROOT, "scripts", "lib", "prepush-observation.sh")
HOOK_PATH = os.path.join(REPO_ROOT, ".githooks", "pre-push")
FAST_CUT_MARKER = "task lint:prepush-refclass"


class ObservationError(Exception):
    """An observation failure. It carries a code and nothing a caller supplied."""

    def __init__(self, code: int) -> None:
        super().__init__(_CODE_NAME.get(code, "E_INTERNAL"))
        self.code = code


# ---------------------------------------------------------------------------------
# STRICT RECORD SCHEMAS.
#
# ⛔ KNOWN FIELD NAMES ARE NOT A SCHEMA, and the first revision proved it. It checked
# that a record was an owned, bounded JSON object and then trusted its contents:
# independent review (O1-F1) showed that an attempt carrying an unknown field, an
# invalid derivation or a nonsense source context still reached
# `full_fast_sequence_observed`, that an occurrence could carry an arbitrary JSON value
# as its elapsed duration and still count as complete, and that a receipt with
# `stdout: false` still qualified as external completion evidence.
#
# So every persisted object is validated against an EXACT key set with a per-key
# predicate, on the way in and on the way out. The rules that matter and are easy to get
# wrong:
#
#   · `isinstance(True, int)` is true in Python, so every integer field rejects bool
#     explicitly. A `shell_status: true` is not a status.
#   · `json.loads` accepts NaN, Infinity and -Infinity by default. `parse_constant`
#     refuses all three, so a nonfinite duration cannot enter a record or a verdict.
#   · duplicate keys are refused by `_no_duplicate_keys` before any value is seen.
#   · fixed prose is compared against the constant the writer used, so a record that
#     merely has the right SHAPE is still refused if it means something else.
# ---------------------------------------------------------------------------------
def _is_int(value, low, high):
    return isinstance(value, int) and not isinstance(value, bool) and low <= value <= high


def _is_bool(value):
    return isinstance(value, bool)


def _is_finite(value):
    return (
        isinstance(value, float)
        and not isinstance(value, bool)
        and math.isfinite(value)
    )


def _is_text(value, pattern=None, maxlen=4096):
    if not isinstance(value, str) or len(value) > maxlen:
        return False
    return pattern is None or bool(pattern.match(value))


def _is_utc(value):
    if not _is_text(value, UTC_RE, 64):
        return False
    try:
        parsed = datetime.datetime.fromisoformat(value)
    except ValueError:
        return False
    return parsed.utcoffset() == datetime.timedelta(0)


def _exactly(constant):
    return lambda value: value == constant and type(value) is type(constant)


def _one_of(*allowed):
    return lambda value: isinstance(value, str) and value in allowed


def _optional(inner):
    return lambda value: value is None or inner(value)


def _object(spec, cross=None):
    return lambda value: _valid_object(value, spec, cross)


def _valid_object(value, spec, cross=None):
    if not isinstance(value, dict) or set(value) != set(spec):
        return False
    for key, predicate in spec.items():
        if not predicate(value[key]):
            return False
    return cross is None or bool(cross(value))


RECORDER_SPEC = {
    "mechanism": _exactly(RECORDER_MECHANISM),
    "version": _exactly(RECORDER_VERSION),
    "python_version": lambda v: _is_text(v, PYTHON_VERSION_RE, 16),
    "bash_version": lambda v: _is_text(v, BASH_VERSION_RE, 16) or v == UNAVAILABLE,
    "monotonic_resolution": lambda v: _is_finite(v) and v > 0,
    "monotonic_adjustable": _is_bool,
}

CONTEXT_SPEC = {
    "source_commit": _optional(lambda v: _is_text(v, OID_RE, 64)),
    "source_tree": _optional(lambda v: _is_text(v, OID_RE, 64)),
    "hook_file_sha256": _optional(lambda v: _is_text(v, SHA256_RE, 64)),
    "adapter_sha256": _optional(lambda v: _is_text(v, SHA256_RE, 64)),
    "recorder_sha256": _optional(lambda v: _is_text(v, SHA256_RE, 64)),
    "tracked_source_differs": _optional(_is_bool),
    "meaning": _exactly(CONTEXT_MEANING),
}

SNAPSHOT_SPEC = {
    "identity": _exactly(SNAPSHOT_IDENTITY),
    "reason": _exactly(SNAPSHOT_REASON),
}

SEQUENCE_SPEC = {
    "derivation": _one_of(DERIVATION_DERIVED, DERIVATION_UNAVAILABLE),
    "count": lambda v: _is_int(v, 0, MAX_OCCURRENCES),
    "names": lambda v: (
        isinstance(v, list)
        and len(v) <= MAX_OCCURRENCES
        and all(_is_text(n, TASK_NAME_RE, 64) for n in v)
    ),
}


def _sequence_cross(value):
    """count must equal names, and a derivation must agree with what it produced."""
    if value["count"] != len(value["names"]):
        return False
    if value["derivation"] == DERIVATION_UNAVAILABLE:
        return value["names"] == []
    return len(value["names"]) > 0


OUTPUT_SPEC = {"filesystem_type": lambda v: isinstance(v, str) and v in SUPPORTED_LOCAL_FSTYPES}

ATTEMPT_SPEC = {
    "schema": _exactly(SCHEMA),
    "kind": _exactly("attempt"),
    "telemetry_attempt_id": lambda v: _is_text(v, ATTEMPT_TOKEN_RE, 80),
    "started_utc": _is_utc,
    "selected_class": _exactly("fast"),
    "input_coverage": _exactly("incomplete"),
    "cache_eligible": _exactly(False),
    "recorder": _object(RECORDER_SPEC),
    "repository_context": _object(CONTEXT_SPEC),
    "executing_snapshot": _object(SNAPSHOT_SPEC),
    "expected_sequence": _object(SEQUENCE_SPEC, _sequence_cross),
    "output": _object(OUTPUT_SPEC),
    "limitations": _exactly(ATTEMPT_LIMITATIONS),
}

BOOKKEEPING_SPEC = {
    "schema": _exactly(SCHEMA),
    "next_ordinal": lambda v: _is_int(v, 1, MAX_OCCURRENCES + 1),
}

OCCURRENCE_SPEC = {
    "schema": _exactly(SCHEMA),
    "kind": _exactly("occurrence"),
    "telemetry_attempt_id": lambda v: _is_text(v, ATTEMPT_TOKEN_RE, 80),
    "occurrence_id": lambda v: _is_text(v, OCCURRENCE_TOKEN_RE, 80),
    "ordinal": lambda v: _is_int(v, 1, MAX_OCCURRENCES),
    "task_name": lambda v: _is_text(v, TASK_NAME_RE, 64),
    "in_expected_sequence": _exactly(True),
    "extra_arg_count": lambda v: _is_int(v, 0, 99999),
    "extra_arg_note": _optional(_exactly(EXTRA_ARG_NOTE)),
    "caller_line": lambda v: _is_int(v, 1, 9999999),
    "caller_line_refers_to": _exactly(CALLER_LINE_MEANING),
    "start_utc": _is_utc,
    "start_monotonic": _is_finite,
    "end_utc": _optional(_is_utc),
    "end_monotonic": _optional(_is_finite),
    "elapsed_seconds": _optional(lambda v: _is_finite(v) and v >= 0.0),
    "duration_error": _optional(_one_of(*sorted(DURATION_ERRORS))),
    "completion": _one_of(COMPLETION_STARTED, COMPLETION_COMPLETED),
    "shell_status": _optional(lambda v: _is_int(v, 0, 255)),
    "shell_status_note": _exactly(SHELL_STATUS_NOTE),
}


def _occurrence_cross(value):
    """The completion state and every field that depends on it must agree.

    A `started` record that already carries an end, and a `completed` record with no
    status, are both incoherent evidence even though every individual field is
    well typed. This is the check that stops a half-written record from counting.
    """
    note_required = EXTRA_ARG_NOTE if value["extra_arg_count"] else None
    if value["extra_arg_note"] != note_required:
        return False
    if value["completion"] == COMPLETION_STARTED:
        return (
            value["end_utc"] is None
            and value["end_monotonic"] is None
            and value["elapsed_seconds"] is None
            and value["duration_error"] is None
            and value["shell_status"] is None
        )
    if value["end_utc"] is None or value["end_monotonic"] is None:
        return False
    if value["shell_status"] is None:
        return False
    if value["duration_error"] != "nonmonotonic" and (
        value["end_monotonic"] < value["start_monotonic"]
    ):
        return False
    # Exactly one of a measured duration or a named duration error. A backwards clock is
    # the one case where end precedes start and the record is still the truth, which is
    # why it is named rather than clamped into an invented positive duration.
    return (value["elapsed_seconds"] is None) != (value["duration_error"] is None)


def _attempt_cross(value):
    """A derived sequence implies the file it was derived from was readable and hashed.

    Nothing else in the record ties those two together, and an attempt claiming a
    derivation while reporting no hook digest is describing two different runs.
    """
    if value["expected_sequence"]["derivation"] != DERIVATION_DERIVED:
        return True
    return value["repository_context"]["hook_file_sha256"] is not None


def _require_valid(record, spec, cross, code):
    if not _valid_object(record, spec, cross):
        raise ObservationError(code)
    return record


# ---------------------------------------------------------------------------------
# Boundary: descriptor-relative, no-follow, owned, private.
# ---------------------------------------------------------------------------------
def _open_dir_nofollow_chain(path: str) -> int:
    """Open an absolute directory path one component at a time with O_NOFOLLOW.

    Opening the final component with O_NOFOLLOW alone is NOT the property the contract
    asks for: `/a/b/c` with `b` a symlink still resolves. Descending component by
    component from `/` is what makes "no symlink traversal at the output boundary"
    true rather than nearly true.
    """
    if not path.startswith("/"):
        raise ObservationError(E_BOUNDARY)
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | getattr(os, "O_CLOEXEC", 0)
    try:
        fd = os.open("/", flags)
    except OSError:
        raise ObservationError(E_BOUNDARY)
    for part in path.split("/"):
        if part == "" or part == ".":
            continue
        if part == "..":
            os.close(fd)
            raise ObservationError(E_BOUNDARY)
        try:
            nxt = os.open(part, flags, dir_fd=fd)
        except OSError:
            os.close(fd)
            raise ObservationError(E_BOUNDARY)
        os.close(fd)
        fd = nxt
    return fd


def _require_private_dir(fd: int, code: int) -> os.stat_result:
    st = os.fstat(fd)
    if not stat.S_ISDIR(st.st_mode):
        raise ObservationError(code)
    if st.st_uid != os.getuid():
        raise ObservationError(E_OWNERSHIP)
    if stat.S_IMODE(st.st_mode) & 0o077:
        raise ObservationError(E_OWNERSHIP)
    return st


def _fstype_for(path: str) -> str:
    """Longest mountpoint match in /proc/self/mountinfo. Linux-only by design."""
    best_len, best_type = -1, UNAVAILABLE
    try:
        with open("/proc/self/mountinfo", "r", encoding="utf-8", errors="replace") as fh:
            for line in fh:
                fields = line.rstrip("\n").split(" ")
                try:
                    sep = fields.index("-")
                except ValueError:
                    continue
                mountpoint = fields[4].replace("\\040", " ").replace("\\011", "\t")
                fstype = fields[sep + 1]
                if path == mountpoint or path.startswith(
                    mountpoint if mountpoint.endswith("/") else mountpoint + "/"
                ):
                    if len(mountpoint) > best_len:
                        best_len, best_type = len(mountpoint), fstype
    except OSError:
        return UNAVAILABLE
    return best_type


def _reject_repository_output(path: str) -> None:
    """The output parent must be outside the worktree and any Git administrative area.

    Two independent reasons, both measured elsewhere in this repository: a gate that
    writes into the worktree trips `check-tree-untouched.sh`, and anything under a
    shared clone is picked up by the next lane's `git add`.
    """
    root = os.path.realpath(REPO_ROOT)
    target = os.path.realpath(path)
    if target == root or target.startswith(root + os.sep):
        raise ObservationError(E_BOUNDARY)
    for part in target.split(os.sep):
        if part == ".git":
            raise ObservationError(E_BOUNDARY)


def _open_parent(parent: str) -> int:
    if not parent or "\0" in parent or len(parent) > 4096 or not parent.startswith("/"):
        raise ObservationError(E_ARGV)
    _reject_repository_output(parent)
    fd = _open_dir_nofollow_chain(parent)
    try:
        _require_private_dir(fd, E_BOUNDARY)
    except Exception:
        os.close(fd)
        raise
    return fd


def _open_attempt(parent_fd: int, token: str) -> int:
    if not TOKEN_RE.match(token):
        raise ObservationError(E_ARGV)
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | getattr(os, "O_CLOEXEC", 0)
    try:
        fd = os.open(token, flags, dir_fd=parent_fd)
    except OSError:
        raise ObservationError(E_ATTEMPT)
    try:
        _require_private_dir(fd, E_ATTEMPT)
    except Exception:
        os.close(fd)
        raise
    return fd


# ---------------------------------------------------------------------------------
# Atomic, bounded, strict record I/O inside the owned attempt.
# ---------------------------------------------------------------------------------
def _encode(record: dict) -> bytes:
    blob = json.dumps(
        record, allow_nan=False, ensure_ascii=True, sort_keys=True, separators=(",", ":")
    ).encode("ascii")
    if len(blob) > MAX_RECORD_BYTES:
        raise ObservationError(E_WRITE)
    return blob


def _write_atomic(dir_fd: int, name: str, blob: bytes) -> None:
    """Write-then-rename inside the owned directory, by descriptor.

    An interrupted write cannot leave a valid record: the reader only ever sees the
    old name or the fully written one. The temporary carries a random suffix so two
    recorder processes in the same attempt cannot collide on it.
    """
    tmp = "." + name + "." + secrets.token_hex(8) + ".tmp"
    fd = -1
    try:
        fd = os.open(
            tmp,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0),
            0o600,
            dir_fd=dir_fd,
        )
        written = 0
        while written < len(blob):
            # A short write is normal and is resumed. A write that reports ZERO progress
            # is not: resuming it forever would hang the observer inside the gate, which
            # is the one failure mode an observation-only instrument must never have.
            step = os.write(fd, blob[written:])
            if step <= 0:
                raise ObservationError(E_WRITE)
            written += step
        os.fsync(fd)
        os.close(fd)
        fd = -1
        os.rename(tmp, name, src_dir_fd=dir_fd, dst_dir_fd=dir_fd)
        tmp = None
    except OSError:
        raise ObservationError(E_WRITE)
    finally:
        if fd >= 0:
            try:
                os.close(fd)
            except OSError:
                pass
        if tmp is not None:
            try:
                os.unlink(tmp, dir_fd=dir_fd)
            except OSError:
                pass


def _reject_constant(name):
    """NaN, Infinity and -Infinity are accepted by json.loads unless something says no.

    They are valid JavaScript and invalid evidence: a nonfinite elapsed duration would
    compare, serialize and print like a number while meaning nothing.
    """
    raise ValueError("nonfinite JSON constant: %s" % name)


def _write_record(dir_fd: int, name: str, record: dict, spec, cross=None) -> None:
    """Validate against the schema, then write atomically.

    Validation happens on the way OUT as well as the way in. A recorder that produced a
    record its own reader would refuse is a recorder defect, and it is better to fail the
    observation here than to persist evidence that cannot be read back.
    """
    if not _valid_object(record, spec, cross):
        raise ObservationError(E_INTERNAL)
    _write_atomic(dir_fd, name, _encode(record))


def _no_duplicate_keys(pairs):
    seen = {}
    for key, value in pairs:
        if key in seen:
            raise ValueError("duplicate key")
        seen[key] = value
    return seen


def _read_json(dir_fd: int, name: str, code: int) -> dict:
    try:
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | getattr(os, "O_CLOEXEC", 0), dir_fd=dir_fd)
    except OSError:
        raise ObservationError(code)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid():
            raise ObservationError(code)
        if st.st_size > MAX_RECORD_BYTES:
            raise ObservationError(code)
        blob = os.read(fd, MAX_RECORD_BYTES + 1)
    except OSError:
        raise ObservationError(code)
    finally:
        os.close(fd)
    if len(blob) > MAX_RECORD_BYTES:
        raise ObservationError(code)
    try:
        parsed = json.loads(
            blob.decode("ascii"),
            object_pairs_hook=_no_duplicate_keys,
            parse_constant=_reject_constant,
        )
    except (ValueError, UnicodeDecodeError):
        raise ObservationError(code)
    if not isinstance(parsed, dict):
        raise ObservationError(code)
    return parsed


# ---------------------------------------------------------------------------------
# Bookkeeping: ordinal allocation under a NONBLOCKING per-attempt advisory lock.
# ---------------------------------------------------------------------------------
def _allocate_ordinal(attempt_fd: int) -> int:
    """Serialize only this: read a counter, add one, write it back.

    NONBLOCKING on purpose. Contention makes THIS observation unavailable; it never
    waits, never retries and never delays the task child. The kernel releases the lock
    when the recorder exits or crashes, so no stale-age liveness rule is needed — and
    none is used, because a lock file's mtime is not evidence that its holder is alive.
    """
    try:
        fd = os.open(
            BOOKKEEPING_FILE,
            os.O_RDWR | os.O_NOFOLLOW | getattr(os, "O_CLOEXEC", 0),
            dir_fd=attempt_fd,
        )
    except OSError:
        raise ObservationError(E_ATTEMPT)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid():
            raise ObservationError(E_ATTEMPT)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            if exc.errno in (errno.EWOULDBLOCK, errno.EAGAIN, errno.EACCES):
                raise ObservationError(E_LOCK_BUSY)
            raise ObservationError(E_WRITE)
        os.lseek(fd, 0, os.SEEK_SET)
        blob = os.read(fd, MAX_RECORD_BYTES)
        try:
            state = json.loads(
                blob.decode("ascii"),
                object_pairs_hook=_no_duplicate_keys,
                parse_constant=_reject_constant,
            )
        except (ValueError, UnicodeDecodeError):
            raise ObservationError(E_RECORD)
        if not _valid_object(state, BOOKKEEPING_SPEC):
            raise ObservationError(E_RECORD)
        nxt = state["next_ordinal"]
        if nxt > MAX_OCCURRENCES:
            raise ObservationError(E_RECORD)
        following = {"schema": SCHEMA, "next_ordinal": nxt + 1}
        if not _valid_object(following, BOOKKEEPING_SPEC):
            raise ObservationError(E_INTERNAL)
        payload = _encode(following)
        os.lseek(fd, 0, os.SEEK_SET)
        os.ftruncate(fd, 0)
        written = 0
        while written < len(payload):
            # ⛔ THE SAME PROGRESS RULE AS THE ATOMIC WRITER, AND FOR A SHARPER REASON.
            # Revision 2 guarded `_write_atomic` and left this sibling loop unguarded,
            # so a write reporting no progress spun HERE — under the bookkeeping lock,
            # on the `op_start` path, before the task had run. Measured on the returned
            # source (revision3 probe-02): `op_start` never returned and no occurrence
            # was written. The `finally` below still closes the descriptor, so the lock
            # is released, and refusing here means no ordinal is claimed for a record
            # that will not exist.
            step = os.write(fd, payload[written:])
            if step <= 0:
                raise ObservationError(E_WRITE)
            written += step
        os.fsync(fd)
        return nxt
    except OSError:
        raise ObservationError(E_WRITE)
    finally:
        # Closing releases the flock. Nothing here is held across the task child.
        try:
            os.close(fd)
        except OSError:
            pass


# ---------------------------------------------------------------------------------
# Source context. Hashes are of REPOSITORY files, which is initialization context and
# is labelled as such; the executing hook snapshot is unlinked and stays `unverified`.
# ---------------------------------------------------------------------------------
def _sha256_file(path: str):
    import hashlib

    try:
        digest = hashlib.sha256()
        with open(path, "rb") as fh:
            while True:
                chunk = fh.read(65536)
                if not chunk:
                    break
                digest.update(chunk)
        return digest.hexdigest()
    except OSError:
        return None


def _expected_fast_sequence():
    """Derive the ordered top-level task names of the FAST lane from the hook FILE.

    Same cut and the same invocation shape the repository's own gates use
    (`check-taskfile-graph.sh`, `scripts/test-prepush-refclass.sh`): strip indentation
    and any `VAR=val` prefixes, then require the line to BEGIN with `task`, and stop
    after the literal cut marker. A mention inside a comment or an `echo` is not an
    invocation and must not enter the list.

    Returns (names, derivation). `derivation` is `unavailable` whenever the hook file
    is absent or the cut marker is missing — a guessed sequence would let a later
    reconciliation claim coverage it never observed.
    """
    try:
        with open(HOOK_PATH, "r", encoding="utf-8", errors="replace") as fh:
            lines = fh.read().split("\n")
    except OSError:
        return None, UNAVAILABLE
    prefix = re.compile(r"\A(?:[A-Za-z_][A-Za-z0-9_]*=(?:\"[^\"]*\"|'[^']*'|\S*)\s+)*task\s+(\S+)")
    names = []
    saw_cut = False
    for line in lines:
        body = line.strip()
        if not body or body.startswith("#"):
            continue
        match = prefix.match(body)
        if not match:
            continue
        name = match.group(1)
        if name == "--list-all":
            continue
        if not TASK_NAME_RE.match(name):
            continue
        names.append(name)
        if body == FAST_CUT_MARKER:
            saw_cut = True
            break
    if not saw_cut or not names:
        return None, UNAVAILABLE
    return names, "repository_hook_file"


# ---------------------------------------------------------------------------------
# Operations.
# ---------------------------------------------------------------------------------
def _emit_token(token: str) -> None:
    line = token + "\n"
    if len(line) > MAX_PROTOCOL_BYTES or not TOKEN_RE.match(token):
        raise ObservationError(E_INTERNAL)
    sys.stdout.write(line)
    sys.stdout.flush()


def op_init(argv) -> int:
    if len(argv) != 6:
        raise ObservationError(E_ARGV)
    parent, klass, commit, tree, tracked, bash_version = argv
    if klass != "fast":
        raise ObservationError(E_ARGV)
    if not (OID_RE.match(commit) or commit == UNAVAILABLE):
        raise ObservationError(E_ARGV)
    if not (OID_RE.match(tree) or tree == UNAVAILABLE):
        raise ObservationError(E_ARGV)
    if tracked not in ("clean", "differs", UNAVAILABLE):
        raise ObservationError(E_ARGV)
    if not (BASH_VERSION_RE.match(bash_version) or bash_version == UNAVAILABLE):
        raise ObservationError(E_ARGV)

    parent_fd = _open_parent(parent)
    try:
        # Positive identification only. `unavailable` (unreadable or unmatched
        # mountinfo), a network type and any type this build has not qualified all take
        # the same exit: no attempt is created and no timing or locking is claimed.
        fstype = _fstype_for(os.path.realpath(parent))
        if fstype not in SUPPORTED_LOCAL_FSTYPES:
            raise ObservationError(E_BOUNDARY)
        token = "att-" + secrets.token_hex(16)
        try:
            os.mkdir(token, 0o700, dir_fd=parent_fd)
        except OSError:
            raise ObservationError(E_WRITE)
        attempt_fd = _open_attempt(parent_fd, token)
        try:
            try:
                os.mkdir(OCCURRENCE_DIR, 0o700, dir_fd=attempt_fd)
            except OSError:
                raise ObservationError(E_WRITE)
            names, derivation = _expected_fast_sequence()
            clock = time.get_clock_info("monotonic")
            record = {
                "schema": SCHEMA,
                "kind": "attempt",
                "telemetry_attempt_id": token,
                "started_utc": _utc_now(),
                "selected_class": klass,
                "input_coverage": "incomplete",
                "cache_eligible": False,
                "recorder": {
                    "mechanism": RECORDER_MECHANISM,
                    "version": RECORDER_VERSION,
                    "python_version": "%d.%d.%d" % sys.version_info[:3],
                    "bash_version": bash_version,
                    "monotonic_resolution": clock.resolution,
                    "monotonic_adjustable": bool(clock.adjustable),
                },
                "repository_context": {
                    "source_commit": None if commit == UNAVAILABLE else commit,
                    "source_tree": None if tree == UNAVAILABLE else tree,
                    "hook_file_sha256": _sha256_file(HOOK_PATH),
                    "adapter_sha256": _sha256_file(ADAPTER_PATH),
                    "recorder_sha256": _sha256_file(RECORDER_PATH),
                    "tracked_source_differs": (
                        None if tracked == UNAVAILABLE else tracked == "differs"
                    ),
                    "meaning": CONTEXT_MEANING,
                },
                "executing_snapshot": {
                    "identity": SNAPSHOT_IDENTITY,
                    "reason": SNAPSHOT_REASON,
                },
                "expected_sequence": {
                    "derivation": derivation,
                    "count": 0 if names is None else len(names),
                    "names": [] if names is None else list(names),
                },
                "output": {"filesystem_type": fstype},
                "limitations": list(ATTEMPT_LIMITATIONS),
            }
            _write_record(attempt_fd, ATTEMPT_FILE, record, ATTEMPT_SPEC, _attempt_cross)
            _write_record(
                attempt_fd,
                BOOKKEEPING_FILE,
                {"schema": SCHEMA, "next_ordinal": 1},
                BOOKKEEPING_SPEC,
            )
        finally:
            os.close(attempt_fd)
    finally:
        os.close(parent_fd)
    _emit_token(token)
    return E_OK


def op_start(argv) -> int:
    if len(argv) != 5:
        raise ObservationError(E_ARGV)
    parent, attempt, task_name, extra_args, caller_line = argv
    if not TASK_NAME_RE.match(task_name):
        raise ObservationError(E_ARGV)
    if not COUNT_RE.match(extra_args) or not LINE_RE.match(caller_line):
        raise ObservationError(E_ARGV)

    parent_fd = _open_parent(parent)
    try:
        attempt_fd = _open_attempt(parent_fd, attempt)
        try:
            # ⛔ THE ATTEMPT IS VALIDATED IN FULL BEFORE ANY OF IT IS BELIEVED. The
            # allowlist below is read out of this record, so trusting the record's
            # identity alone would let a tampered expected list admit any name.
            meta = _require_valid(
                _read_json(attempt_fd, ATTEMPT_FILE, E_ATTEMPT),
                ATTEMPT_SPEC,
                _attempt_cross,
                E_ATTEMPT,
            )
            if meta["telemetry_attempt_id"] != attempt:
                raise ObservationError(E_ATTEMPT)

            # ⛔ THE FIXED REVIEWED FAST NAMES ARE THE ALLOWLIST. A grammar bounds the
            # ENCODING of a name; it does not restrict the VALUE to a reviewed task, and
            # the first revision recorded any grammar-valid name with
            # `in_expected_sequence: false` (returned as O1-F4). A name outside the
            # derived list now produces no record at all. Nothing about the task itself
            # changes: the wrapper still runs it once and still returns its own status.
            names = meta["expected_sequence"]["names"]
            if meta["expected_sequence"]["derivation"] != DERIVATION_DERIVED or not names:
                raise ObservationError(E_NAME)
            if task_name not in names:
                raise ObservationError(E_NAME)
            ordinal = _allocate_ordinal(attempt_fd)
            token = "occ-" + secrets.token_hex(16)
            record = {
                "schema": SCHEMA,
                "kind": "occurrence",
                "telemetry_attempt_id": attempt,
                "occurrence_id": token,
                "ordinal": ordinal,
                "task_name": task_name,
                "in_expected_sequence": True,
                "extra_arg_count": int(extra_args),
                "extra_arg_note": EXTRA_ARG_NOTE if int(extra_args) else None,
                "caller_line": int(caller_line),
                "caller_line_refers_to": CALLER_LINE_MEANING,
                "start_utc": _utc_now(),
                "start_monotonic": time.monotonic(),
                "end_utc": None,
                "end_monotonic": None,
                "elapsed_seconds": None,
                "duration_error": None,
                "completion": COMPLETION_STARTED,
                "shell_status": None,
                "shell_status_note": SHELL_STATUS_NOTE,
            }
            occ_fd = _open_occurrence_dir(attempt_fd)
            try:
                _write_record(
                    occ_fd, token + ".json", record, OCCURRENCE_SPEC, _occurrence_cross
                )
            finally:
                os.close(occ_fd)
        finally:
            os.close(attempt_fd)
    finally:
        os.close(parent_fd)
    _emit_token(token)
    return E_OK


def op_end(argv) -> int:
    if len(argv) != 4:
        raise ObservationError(E_ARGV)
    parent, attempt, occurrence, shell_status = argv
    if not TOKEN_RE.match(occurrence) or not occurrence.startswith("occ-"):
        raise ObservationError(E_ARGV)
    if not STATUS_RE.match(shell_status) or not 0 <= int(shell_status) <= 255:
        raise ObservationError(E_ARGV)

    parent_fd = _open_parent(parent)
    try:
        attempt_fd = _open_attempt(parent_fd, attempt)
        try:
            occ_fd = _open_occurrence_dir(attempt_fd)
            try:
                record = _require_valid(
                    _read_json(occ_fd, occurrence + ".json", E_OCCURRENCE),
                    OCCURRENCE_SPEC,
                    _occurrence_cross,
                    E_OCCURRENCE,
                )
                if (
                    record["occurrence_id"] != occurrence
                    or record["telemetry_attempt_id"] != attempt
                ):
                    raise ObservationError(E_OCCURRENCE)
                if record["completion"] != COMPLETION_STARTED:
                    raise ObservationError(E_OCCURRENCE)
                started = record["start_monotonic"]
                now = time.monotonic()
                elapsed = None
                duration_error = None
                candidate = now - started
                # No clamping. A negative or non-finite interval is an OBSERVATION
                # error, reported as one; inventing a positive duration would be the
                # single most useless thing this file could do.
                if math.isfinite(candidate) and candidate >= 0:
                    elapsed = candidate
                else:
                    duration_error = "nonmonotonic"
                record["end_utc"] = _utc_now()
                record["end_monotonic"] = now
                record["elapsed_seconds"] = elapsed
                record["duration_error"] = duration_error
                record["completion"] = COMPLETION_COMPLETED
                record["shell_status"] = int(shell_status)
                record["shell_status_note"] = SHELL_STATUS_NOTE
                _write_record(
                    occ_fd, occurrence + ".json", record, OCCURRENCE_SPEC, _occurrence_cross
                )
            finally:
                os.close(occ_fd)
        finally:
            os.close(attempt_fd)
    finally:
        os.close(parent_fd)
    return E_OK


def _open_occurrence_dir(attempt_fd: int) -> int:
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | getattr(os, "O_CLOEXEC", 0)
    try:
        fd = os.open(OCCURRENCE_DIR, flags, dir_fd=attempt_fd)
    except OSError:
        raise ObservationError(E_ATTEMPT)
    try:
        _require_private_dir(fd, E_ATTEMPT)
    except Exception:
        os.close(fd)
        raise
    return fd


def _utc_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


# ---------------------------------------------------------------------------------
# The parent receipt reader, PINNED to the one schema this implementation has actually
# seen (assessments/github/dev-sync-20260909-r34/attempt-01/normal-push.receipt.json).
#
# It reads `exit` and the two instants, and NOTHING ELSE crosses into the summary:
# `argv` and `cwd` are execution context that would put a ref name and an absolute path
# into a record this contract keeps free of both. A receipt that does not match this
# shape exactly is REFUSED — "a free-form assertion of success is not completion
# evidence", and an unknown key is exactly how a different instrument's file would be
# read as if it were this one.
#
# AND IT IS NOT A SOURCE PROOF. This schema records no commit and no tree, so it cannot
# bind the push to the source the attempt recorded. The summary says so in a field
# rather than leaving a reader to assume otherwise.
# ---------------------------------------------------------------------------------
RECEIPT_SPEC = {
    "argv": lambda v: (
        isinstance(v, list)
        and 2 <= len(v) <= 64
        and all(_is_text(a, None, 4096) for a in v)
        and v[0] == "git"
        and "push" in v
    ),
    "cwd": lambda v: _is_text(v, None, 4096),
    "started_at": _is_utc,
    "pid": lambda v: _is_int(v, 1, 2 ** 31),
    "start_ticks": lambda v: _is_text(v, DIGITS_RE, 20),
    "runner_sha256": lambda v: _is_text(v, SHA256_RE, 64),
    "exit": lambda v: _is_int(v, -255, 255),
    "ended_at": _is_utc,
    "sha256": _object(
        {
            "stdout": lambda v: _is_text(v, SHA256_RE, 64),
            "stderr": lambda v: _is_text(v, SHA256_RE, 64),
        }
    ),
}


def _read_parent_receipt(path: str) -> dict:
    """Read the ONE pinned orchestrator receipt schema, strictly, and export almost none of it.

    ⛔ THE NESTED VALUES ARE PART OF THE SCHEMA. The first revision checked that
    `sha256` was an object with the two expected KEY NAMES and never looked at what they
    held, so `{"stdout": false, "stderr": []}` qualified as external completion evidence
    (returned as O1-F5/O1-F1). Both digests are now required to be lowercase 64-hex, and
    every scalar has its own format.

    ⛔ AND IT IS STILL NOT A SOURCE PROOF. This schema records no commit and no tree, so
    it cannot bind a push to the source an attempt recorded. Only `exit` and the two
    instants cross into the summary: `argv` carries a ref name and `cwd` an absolute
    path, and this record set is kept free of both.
    """
    if not path or "\0" in path or len(path) > 4096:
        raise ObservationError(E_ARGV)
    try:
        with open(path, "rb") as fh:
            blob = fh.read(MAX_RECORD_BYTES + 1)
    except OSError:
        raise ObservationError(E_RECEIPT)
    if len(blob) > MAX_RECORD_BYTES:
        raise ObservationError(E_RECEIPT)
    try:
        parsed = json.loads(
            blob.decode("utf-8"),
            object_pairs_hook=_no_duplicate_keys,
            parse_constant=_reject_constant,
        )
    except (ValueError, UnicodeDecodeError):
        raise ObservationError(E_RECEIPT)
    if not _valid_object(parsed, RECEIPT_SPEC):
        raise ObservationError(E_RECEIPT)
    return {
        "schema": "orchestrator/normal-push.receipt.json/pinned-2026-09-09",
        "supported": True,
        "parent_exit": parsed["exit"],
        "started_at": parsed["started_at"],
        "ended_at": parsed["ended_at"],
        "source_relation": "not_established",
        "source_relation_reason": (
            "this receipt schema records no commit or tree, so it cannot bind the push "
            "to the source the attempt recorded"
        ),
    }


def op_summary(argv) -> int:
    if len(argv) != 3:
        raise ObservationError(E_ARGV)
    parent, attempt, receipt_path = argv

    findings = []
    report = {
        "schema": SCHEMA,
        "kind": "summary",
        "generated_utc": _utc_now(),
        "telemetry_attempt_id": attempt,
        "verdict": "unavailable",
        "telemetry_integrity": "unavailable",
        "observed_prefix_length": 0,
        "expected_count": 0,
        "findings": findings,
    }

    parent_fd = _open_parent(parent)
    try:
        attempt_fd = _open_attempt(parent_fd, attempt)
        try:
            # A malformed attempt is REPORTED, not merely refused with an exit code: the
            # orchestrator asked what this attempt shows, and "unverified, and here is why"
            # is an answer. Only a boundary this process cannot even open goes out as a
            # bare code, because then there is nothing to report about.
            try:
                meta = _require_valid(
                    _read_json(attempt_fd, ATTEMPT_FILE, E_RECORD),
                    ATTEMPT_SPEC,
                    _attempt_cross,
                    E_RECORD,
                )
            except ObservationError:
                report["verdict"] = "malformed_or_unverified"
                findings.append("malformed_attempt_record")
                _print_report(report)
                return E_OK
            if meta["telemetry_attempt_id"] != attempt:
                report["verdict"] = "malformed_or_unverified"
                findings.append("attempt_identity_mismatch")
                _print_report(report)
                return E_OK

            # The bookkeeping counter is evidence too: if it does not agree with the
            # records beside it, one of them is wrong and neither can support a full
            # verdict. It is read here rather than trusted by absence.
            try:
                book = _require_valid(
                    _read_json(attempt_fd, BOOKKEEPING_FILE, E_RECORD),
                    BOOKKEEPING_SPEC,
                    None,
                    E_RECORD,
                )
            except ObservationError:
                book = None
                findings.append("malformed_bookkeeping_record")

            # Anything else in the owned attempt directory is unexplained evidence — a
            # leftover `.attempt.json.<hex>.tmp` is the signature of an interrupted
            # write, and a stranger's file is a reason to stop trusting the set.
            try:
                stray = sorted(
                    e for e in os.listdir(attempt_fd)
                    if e not in (ATTEMPT_FILE, BOOKKEEPING_FILE, OCCURRENCE_DIR)
                )
            except OSError:
                stray = []
                findings.append("attempt_directory_unreadable")
            if stray:
                findings.append("extraneous_attempt_entries")
            report["extraneous_attempt_entry_count"] = len(stray)
            report["attempt"] = {
                "started_utc": meta.get("started_utc"),
                "selected_class": meta.get("selected_class"),
                "input_coverage": meta.get("input_coverage"),
                "cache_eligible": meta.get("cache_eligible"),
                "repository_context": meta.get("repository_context"),
                "executing_snapshot": meta.get("executing_snapshot"),
                "recorder": meta.get("recorder"),
                "output": meta.get("output"),
            }
            expected = meta["expected_sequence"]
            names = list(expected["names"])
            derivation = expected["derivation"]
            report["expected_count"] = len(names)
            report["expected_sequence_derivation"] = derivation

            try:
                occurrences, occ_findings = _load_occurrences(attempt_fd, attempt)
            except ObservationError:
                occurrences, occ_findings = [], ["occurrence_directory_unreadable"]
            findings.extend(occ_findings)
        finally:
            os.close(attempt_fd)
    finally:
        os.close(parent_fd)

    try:
        report["parent_push"] = _read_parent_receipt(receipt_path)
    except ObservationError:
        report["parent_push"] = {"supported": False, "parent_exit": None}
        findings.append("parent_receipt_unsupported_or_absent")

    completed = [o for o in occurrences if o["completion"] == "completed"]
    started_only = [o for o in occurrences if o["completion"] != "completed"]
    report["occurrence_counts"] = {
        "recorded": len(occurrences),
        "completed": len(completed),
        "started_without_end": len(started_only),
    }
    report["started_without_end"] = [
        {"ordinal": o["ordinal"], "task_name": o["task_name"], "completion": "started"}
        for o in started_only
    ]
    # A passed advisory wrapper hides a nonzero task from the hook's status. It must not
    # be hidden here too: `task lint:test-hook-parallelism || true` is exactly this case.
    report["nonzero_task_statuses"] = [
        {"ordinal": o["ordinal"], "task_name": o["task_name"], "shell_status": o["shell_status"]}
        for o in completed
        if o["shell_status"] != 0
    ]
    report["duration_errors"] = [
        {"ordinal": o["ordinal"], "task_name": o["task_name"], "duration_error": o["duration_error"]}
        for o in completed
        if o["duration_error"]
    ]
    report["measured_prefix_durations"] = [
        {
            "ordinal": o["ordinal"],
            "task_name": o["task_name"],
            "elapsed_seconds": o["elapsed_seconds"],
        }
        for o in completed
        if o["elapsed_seconds"] is not None
    ]

    observed = [o["task_name"] for o in sorted(completed, key=lambda o: o["ordinal"])]
    report["observed_prefix_length"] = len(observed)

    ordinals = [o["ordinal"] for o in occurrences]
    contiguous = sorted(ordinals) == list(range(1, len(ordinals) + 1))
    if occurrences and not contiguous:
        findings.append("ordinals_not_contiguous")

    # ⛔ ANY OF THESE MEANS THE EVIDENCE ITSELF IS SUSPECT, so nothing downstream may be
    # computed from it. They are listed once, here, because the first revision decided
    # completeness from names and ordinals alone and let malformed neighbours through.
    malformed_findings = (
        "duplicate_occurrence_id",
        "duplicate_ordinal",
        "malformed_occurrence_record",
        "malformed_attempt_record",
        "malformed_bookkeeping_record",
        "attempt_identity_mismatch",
        "unexpected_status_type",
        "occurrence_directory_unreadable",
        "attempt_directory_unreadable",
        "extraneous_attempt_entries",
        "extraneous_occurrence_entries",
    )
    # The bookkeeping counter must account for exactly the records that exist. A gap
    # means an ordinal was allocated and its record never landed.
    if book is not None and book["next_ordinal"] != len(occurrences) + 1:
        findings.append("bookkeeping_disagrees_with_records")

    if any(f in malformed_findings for f in findings):
        report["verdict"] = "malformed_or_unverified"
        report["telemetry_integrity"] = "partial" if completed else "unavailable"
    elif derivation != DERIVATION_DERIVED or not names:
        # An expected sequence that was never derived cannot certify anything whole.
        report["verdict"] = "prefix_observed" if completed else "unavailable"
        report["telemetry_integrity"] = "partial" if completed else "unavailable"
        findings.append("expected_sequence_unavailable")
    elif observed == names and started_only == [] and contiguous:
        if (
            report["parent_push"].get("supported")
            and report["parent_push"].get("parent_exit") == 0
            and "bookkeeping_disagrees_with_records" not in findings
            and book is not None
        ):
            report["verdict"] = "full_fast_sequence_observed"
            report["telemetry_integrity"] = "complete"
        else:
            report["verdict"] = "prefix_observed"
            report["telemetry_integrity"] = "partial"
            if not report["parent_push"].get("supported") or report["parent_push"].get(
                "parent_exit"
            ) != 0:
                findings.append("no_external_completion_evidence")
    elif observed == names[: len(observed)]:
        report["verdict"] = "prefix_observed"
        report["telemetry_integrity"] = "partial"
    else:
        report["verdict"] = "malformed_or_unverified"
        report["telemetry_integrity"] = "partial" if completed else "unavailable"
        findings.append("observed_sequence_diverges_from_expected")

    report["qualifications"] = [
        "telemetry integrity is not a gate or product verdict",
        "the executing hook snapshot identity is unverified",
        "a completed parent push does not complete an occurrence that has no end record",
        "no task was replayed and no performance verdict is offered",
    ]
    _print_report(report)
    return E_OK


def _load_occurrences(attempt_fd: int, attempt: str):
    """Read every occurrence as HOSTILE structured input.

    Each record is validated against the exact schema-1 occurrence allowlist and its
    completion cross-check before a single field of it is used. A record that merely
    carries the right key NAMES — the first revision's standard — is refused here.
    """
    findings = []
    occurrences = []
    occ_fd = _open_occurrence_dir(attempt_fd)
    try:
        entries = sorted(os.listdir(occ_fd))
        seen_ids = set()
        seen_ordinals = set()
        stray = 0
        for entry in entries:
            if not entry.endswith(".json"):
                stray += 1
                continue
            token = entry[: -len(".json")]
            if not OCCURRENCE_TOKEN_RE.match(token):
                stray += 1
                continue
            try:
                record = _require_valid(
                    _read_json(occ_fd, entry, E_RECORD),
                    OCCURRENCE_SPEC,
                    _occurrence_cross,
                    E_RECORD,
                )
            except ObservationError:
                findings.append("malformed_occurrence_record")
                continue
            if (
                record["telemetry_attempt_id"] != attempt
                or record["occurrence_id"] != token
            ):
                findings.append("malformed_occurrence_record")
                continue
            if token in seen_ids:
                findings.append("duplicate_occurrence_id")
                continue
            if record["ordinal"] in seen_ordinals:
                findings.append("duplicate_ordinal")
                continue
            seen_ids.add(token)
            seen_ordinals.add(record["ordinal"])
            occurrences.append(
                {
                    "ordinal": record["ordinal"],
                    "task_name": record["task_name"],
                    "completion": record["completion"],
                    "shell_status": (
                        record["shell_status"]
                        if record["completion"] == COMPLETION_COMPLETED
                        else None
                    ),
                    "elapsed_seconds": record["elapsed_seconds"],
                    "duration_error": record["duration_error"],
                }
            )
        if stray:
            findings.append("extraneous_occurrence_entries")
    finally:
        os.close(occ_fd)
    return occurrences, findings


def _print_report(report: dict) -> None:
    sys.stdout.write(json.dumps(report, allow_nan=False, indent=2, sort_keys=True) + "\n")
    sys.stdout.flush()


OPERATIONS = {"init": op_init, "start": op_start, "end": op_end, "summary": op_summary}


def main(argv) -> int:
    # Permission changes belong to THIS process, never to the hook or the task child:
    # the umask the gate runs under is not touched by observation.
    os.umask(0o077)
    if sys.version_info < MIN_PYTHON:
        return E_PYTHON
    if len(argv) < 2 or argv[1] not in OPERATIONS:
        return E_ARGV
    for item in argv[1:]:
        if "\0" in item or len(item) > 4096:
            return E_ARGV
    try:
        return OPERATIONS[argv[1]](argv[2:])
    except ObservationError as exc:
        return exc.code
    except OSError:
        return E_WRITE
    except Exception:  # noqa: BLE001 — an observer must not raise into the gate.
        return E_INTERNAL


if __name__ == "__main__":
    _rc = main(sys.argv)
    if _rc != E_OK:
        sys.stderr.write("olivares-gate-obs: %s\n" % _CODE_NAME.get(_rc, "E_INTERNAL"))
    raise SystemExit(_rc)
