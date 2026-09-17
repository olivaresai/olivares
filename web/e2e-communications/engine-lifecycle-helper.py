# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Linux fixture stop adapter. Importing this module performs no process action."""
from __future__ import annotations

import hashlib
import json
import math
import os
import re
import select
import signal
import stat
import sys
import time
from typing import Any

LIMIT = 64 * 1024
LEASE = "olivares.k3.engine-lease.v1"
POINTER = "olivares.k3.engine-current.v1"
STOP = "olivares.k3.engine-stop.v1"
UUID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"


class Refusal(Exception):
    def __init__(self, code: str):
        super().__init__(code)
        self.code = code


def require(condition: bool, code: str = "invalid_lease") -> None:
    if not condition:
        raise Refusal(code)


def obj(value: Any, keys: str) -> dict:
    require(type(value) is dict and set(value) == set(keys.split()))
    return value


def text(value: Any, pattern: str | None = None) -> None:
    require(type(value) is str and (pattern is None or re.fullmatch(pattern, value) is not None))


def decimal(value: Any) -> None:
    text(value, r"0|[1-9][0-9]*")


def positive(value: Any) -> None:
    require(type(value) is int and 0 < value <= 2**53 - 1)


def elapsed(value: Any) -> None:
    require(type(value) in (int, float) and math.isfinite(value) and value >= 0)


def absolute(value: Any) -> None:
    text(value)
    require(value.startswith("/") and not value.startswith("//") and os.path.normpath(value) == value and "\0" not in value)


def file_identity(value: Any, artifact: bool = False) -> None:
    obj(value, "canonical_path device inode sha256" if artifact else "canonical_path device inode")
    absolute(value["canonical_path"])
    decimal(value["device"])
    decimal(value["inode"])
    if artifact:
        text(value["sha256"], r"[0-9a-f]{64}")


def record_ref(value: Any, kind: str) -> dict:
    obj(value, "name sha256")
    pattern = (r"process-observations/engine\.(seed|activated|restarted)\.[1-9][0-9]*\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.json"
               if kind == "observation" else rf"{kind}\.{UUID}\.json")
    text(value["name"], pattern)
    text(value["sha256"], r"[0-9a-f]{64}")
    return value


def generation(value: dict) -> None:
    positive(value["pid"])
    decimal(value["start_ticks"])
    text(value["boot_id"], UUID)
    text(value["pid_namespace"], r"pid:\[[1-9][0-9]*\]")


def validate_lease(value: Any) -> dict:
    obj(value, "schema lease_id work_dir data_dir data_directory phase pid start_ticks boot_id pid_namespace executable cwd listen grpc_listen observation published_at predecessor")
    require(value["schema"] == LEASE and value["phase"] in ("seed", "activated", "restarted"))
    text(value["lease_id"], UUID)
    for key in ("work_dir", "data_dir", "cwd"):
        absolute(value[key])
    require(value["data_dir"].startswith(value["work_dir"] + "/"))
    generation(value)
    file_identity(value["executable"])
    file_identity(value["data_directory"])
    require(value["data_directory"]["canonical_path"] == value["data_dir"])
    for key in ("listen", "grpc_listen"):
        endpoint(value[key])
    record_ref(value["observation"], "observation")
    text(value["published_at"], r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z")
    if value["predecessor"] is not None:
        obj(value["predecessor"], "lease stop")
        record_ref(value["predecessor"]["lease"], "lease")
        record_ref(value["predecessor"]["stop"], "stop")
    return value


def validate_pointer(value: Any) -> dict:
    require(type(value) is dict)
    state = value.get("state")
    obj(value, "schema state lease stop" if state == "stopped" else "schema state lease")
    require(value["schema"] == POINTER and state in ("live", "stopped"))
    record_ref(value["lease"], "lease")
    if state == "stopped":
        record_ref(value["stop"], "stop")
    return value


def validate_stop(value: Any) -> dict:
    obj(value, "schema lease purpose helper validation signals terminal elapsed_ms")
    require(value["schema"] == STOP and value["validation"] == "matched" and value["purpose"] in ("activation", "restart", "teardown"))
    record_ref(value["lease"], "lease")
    h = obj(value["helper"], "pid start_ticks boot_id pid_namespace executable script")
    generation(h)
    file_identity(h["executable"], True)
    file_identity(h["script"], True)
    require(type(value["signals"]) is list and 1 <= len(value["signals"]) <= 2)
    previous = 0
    for index, row in enumerate(value["signals"]):
        obj(row, "signal outcome elapsed_ms")
        require(row["signal"] == ("SIGTERM" if index == 0 else "SIGKILL") and row["outcome"] in ("sent", "esrch"))
        elapsed(row["elapsed_ms"])
        require(row["elapsed_ms"] >= previous)
        previous = row["elapsed_ms"]
    t = obj(value["terminal"], "observation events elapsed_ms")
    require(t["observation"] == "pidfd" and type(t["events"]) is int and t["events"] in (1, 16, 17))
    elapsed(t["elapsed_ms"])
    elapsed(value["elapsed_ms"])
    require(t["elapsed_ms"] >= previous and value["elapsed_ms"] >= t["elapsed_ms"])
    return value


def endpoint(value: Any) -> None:
    text(value, r"127\.0\.0\.1:[1-9][0-9]*")
    require(1 <= int(value.rsplit(":", 1)[1]) <= 65535)


def parse_stat(raw: str, pid: int) -> str:
    match = re.match(r"^([0-9]+) \(", raw)
    require(match is not None, "unavailable_process")
    require(int(match[1]) == pid, "changed_generation")
    end = raw.rfind(")")
    require(end >= match.end(), "unavailable_process")
    fields = raw[end + 1:].split()
    require(len(fields) >= 20 and re.fullmatch("[A-Za-z]", fields[0]) is not None and fields[0] not in ("Z", "X", "x"), "unavailable_process")
    for token in fields[1:4]:
        require(re.fullmatch(r"[0-9]+", token) is not None and int(token) <= 2**53 - 1, "unavailable_process")
    decimal(fields[19])
    return fields[19]


def parse_argv(argv: list[str]) -> dict:
    require(argv.count("serve") == 1, "ownership_mismatch")
    result = {}
    for flag, key in (("--data-dir", "data_dir"), ("--listen", "listen"), ("--grpc-listen", "grpc_listen")):
        require(not any(arg.startswith(flag + "=") for arg in argv) and argv.count(flag) == 1, "ownership_mismatch")
        index = argv.index(flag) + 1
        require(index < len(argv) and bool(argv[index]) and not argv[index].startswith("-"), "ownership_mismatch")
        result[key] = argv[index]
    return result


def identity(target: str, artifact: bool = False) -> dict:
    canonical = os.path.realpath(target, strict=True)
    st = os.stat(canonical)
    result = {"canonical_path": canonical, "device": str(st.st_dev), "inode": str(st.st_ino)}
    if artifact:
        digest = hashlib.sha256()
        with open(canonical, "rb") as stream:
            for block in iter(lambda: stream.read(65536), b""):
                digest.update(block)
        result["sha256"] = digest.hexdigest()
    return result


class LinuxAdapter:
    def __init__(self) -> None:
        require(sys.platform == "linux" and sys.version_info[:2] == (3, 11) and hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal"), "capability_refusal")

    def now(self) -> float:
        return time.monotonic() * 1000

    def open_pidfd(self, pid: int) -> int:
        try:
            return os.pidfd_open(pid, 0)
        except ProcessLookupError:
            raise Refusal("terminal_unobserved") from None
        except OSError:
            raise Refusal("capability_refusal") from None

    def start_ticks(self, pid: int) -> str:
        with open(f"/proc/{pid}/stat", encoding="utf-8") as stream:
            return parse_stat(stream.read(LIMIT + 1), pid)

    def capture(self, pid: int, expected_start: str) -> dict:
        first = self.start_ticks(pid)
        require(first == expected_start, "changed_generation")
        executable = identity(f"/proc/{pid}/exe")
        cwd = os.path.realpath(f"/proc/{pid}/cwd", strict=True)
        with open(f"/proc/{pid}/cmdline", "rb") as stream:
            raw = stream.read(LIMIT + 1)
        require(len(raw) <= LIMIT, "ownership_mismatch")
        argv = raw.decode("utf-8", errors="strict").split("\0")
        if argv and argv[-1] == "":
            argv.pop()
        args = parse_argv(argv)
        data_dir = os.path.realpath(os.path.join(cwd, args["data_dir"]), strict=True)
        require(stat.S_ISDIR(os.stat(data_dir).st_mode), "ownership_mismatch")
        with open("/proc/sys/kernel/random/boot_id", encoding="ascii") as stream:
            boot = stream.read(128).strip()
        namespace = os.readlink(f"/proc/{pid}/ns/pid")
        require(namespace == os.readlink("/proc/self/ns/pid"), "ownership_mismatch")
        require(first == self.start_ticks(pid), "changed_generation")
        return {"pid": pid, "start_ticks": first, "executable": executable, "cwd": cwd, "data_dir": data_dir,
                "data_directory": identity(data_dir), "boot_id": boot, "pid_namespace": namespace,
                "listen": args["listen"], "grpc_listen": args["grpc_listen"]}

    def poll(self, fd: int, milliseconds: int) -> int:
        p = select.poll()
        p.register(fd, select.POLLIN)
        rows = p.poll(milliseconds)
        require(all(row[0] == fd for row in rows), "signal_failure")
        result = 0
        for _, events in rows:
            result |= events
        return result

    def send(self, fd: int, name: str) -> str:
        try:
            signal.pidfd_send_signal(fd, getattr(signal, name), None, 0)
            return "sent"
        except ProcessLookupError:
            return "esrch"
        except PermissionError:
            raise Refusal("capability_refusal") from None
        except OSError:
            raise Refusal("signal_failure") from None

    def close(self, fd: int) -> None:
        os.close(fd)


def terminal_event(events: int) -> bool:
    require(events in (0, 1, 16, 17), "signal_failure")
    return events != 0


def stop_generation(lease: dict, adapter: Any, record: Any) -> dict:
    """Pure orchestration with an injected descriptor/process adapter and recorder."""
    validate_lease(lease)
    started = adapter.now()
    elapsed_ms = lambda: max(0, round(adapter.now() - started, 3))
    fd = adapter.open_pidfd(lease["pid"])
    try:
        record({"event": "descriptor_acquired", "elapsed_ms": elapsed_ms()})
        try:
            current = adapter.capture(lease["pid"], lease["start_ticks"])
            require(current["start_ticks"] == lease["start_ticks"] and current["pid"] == lease["pid"], "changed_generation")
            for key in ("executable", "cwd", "data_dir", "data_directory", "boot_id", "pid_namespace", "listen", "grpc_listen"):
                require(current[key] == lease[key], "ownership_mismatch")
            require(adapter.start_ticks(lease["pid"]) == lease["start_ticks"], "changed_generation")
        except (OSError, UnicodeError):
            raise Refusal("unavailable_process") from None
        require(not terminal_event(adapter.poll(fd, 0)), "unavailable_process")
        record({"event": "generation_validated", "elapsed_ms": elapsed_ms()})
        signals = []
        for name, duration in (("SIGTERM", 20_000), ("SIGKILL", 5_000)):
            record({"event": "signal_attempt", "signal": name, "elapsed_ms": elapsed_ms()})
            outcome = adapter.send(fd, name)
            signals.append({"signal": name, "outcome": outcome, "elapsed_ms": elapsed_ms()})
            record({"event": "signal_result", **signals[-1]})
            deadline = adapter.now() + duration
            while True:
                remaining = max(0, math.ceil(deadline - adapter.now()))
                events = adapter.poll(fd, remaining)
                if terminal_event(events):
                    terminal = {"observation": "pidfd", "events": events, "elapsed_ms": elapsed_ms()}
                    record({"event": "terminal_observed", **terminal})
                    return {"validation": "matched", "signals": signals, "terminal": terminal, "elapsed_ms": elapsed_ms()}
                if adapter.now() >= deadline:
                    break
        raise Refusal("deadline_exceeded")
    except OSError:
        raise Refusal("signal_failure") from None
    finally:
        adapter.close(fd)


class OwnedDirectory:
    def __init__(self, work: str):
        absolute(work)
        for target in (work, os.path.join(work, "engine-lifecycle")):
            st = os.lstat(target)
            require(stat.S_ISDIR(st.st_mode) and st.st_uid == os.getuid() and stat.S_IMODE(st.st_mode) == 0o700 and os.path.realpath(target) == target, "invalid_custody")
        target = os.path.join(work, "engine-lifecycle")
        self.fd = os.open(target, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        opened = os.fstat(self.fd)
        require(opened.st_dev == st.st_dev and opened.st_ino == st.st_ino, "invalid_custody")

    def read(self, name: str) -> bytes:
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=self.fd)
        try:
            st = os.fstat(fd)
            require(stat.S_ISREG(st.st_mode) and st.st_uid == os.getuid() and st.st_nlink == 1 and stat.S_IMODE(st.st_mode) == 0o600 and st.st_size <= LIMIT, "invalid_custody")
            data = b""
            while len(data) <= LIMIT:
                chunk = os.read(fd, LIMIT + 1 - len(data))
                if not chunk:
                    break
                data += chunk
            require(len(data) <= LIMIT, "invalid_custody")
            return data
        finally:
            os.close(fd)

    def create(self, name: str) -> int:
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self.fd)
        os.fchmod(fd, 0o600)
        st = os.fstat(fd)
        require(stat.S_ISREG(st.st_mode) and st.st_uid == os.getuid() and st.st_nlink == 1 and stat.S_IMODE(st.st_mode) == 0o600, "invalid_custody")
        return fd

    def write(self, fd: int, value: dict) -> None:
        raw = (json.dumps(value, separators=(",", ":"), ensure_ascii=False) + "\n").encode("utf-8")
        require(os.fstat(fd).st_size + len(raw) <= LIMIT, "result_publication_failure")
        try:
            offset = 0
            while offset < len(raw):
                n = os.write(fd, raw[offset:])
                require(n > 0, "result_publication_failure")
                offset += n
            os.fsync(fd)
            os.fsync(self.fd)
        except OSError:
            raise Refusal("result_publication_failure") from None


def helper_identity(adapter: LinuxAdapter) -> dict:
    with open("/proc/sys/kernel/random/boot_id", encoding="ascii") as stream:
        boot = stream.read(128).strip()
    return {"pid": os.getpid(), "start_ticks": adapter.start_ticks(os.getpid()), "boot_id": boot,
            "pid_namespace": os.readlink("/proc/self/ns/pid"), "executable": identity(sys.executable, True), "script": identity(__file__, True)}


def main(argv: list[str]) -> int:
    directory = None
    journal = None
    if len(argv) != 5:
        print(json.dumps({"ok": False, "code": "invalid_lease"}), flush=True)
        return 1
    work, name, digest, result_name, purpose = argv
    try:
        ref = record_ref({"name": name, "sha256": digest}, "lease")
        text(result_name, rf"stop\.{UUID}\.json")
        require(purpose in ("activation", "restart", "teardown"))
        directory = OwnedDirectory(work)
        raw = directory.read(name)
        require(hashlib.sha256(raw).hexdigest() == digest)
        lease = validate_lease(json.loads(raw))
        require(lease["work_dir"] == work and name == f"lease.{lease['lease_id']}.json")
        # Parent holds the exclusive operation lock; never create or steal it here.
        obj(json.loads(directory.read("operation.lock")), "operation_id")
        adapter = LinuxAdapter()
        owner = helper_identity(adapter)
        journal = directory.create(result_name.removesuffix(".json") + ".progress.jsonl")
        directory.write(journal, {"event": "helper_started", "helper": owner, "lease": ref})
        result = stop_generation(lease, adapter, lambda event: directory.write(journal, event))
        receipt = validate_stop({"schema": STOP, "lease": ref, "purpose": purpose, "helper": owner, **result})
        try:
            fd = directory.create(result_name)
            try:
                directory.write(fd, receipt)
            finally:
                os.close(fd)
            os.fsync(directory.fd)
        except OSError:
            raise Refusal("result_publication_failure") from None
        print(json.dumps({"ok": True}), flush=True)
        return 0
    except Refusal as exc:
        print(json.dumps({"ok": False, "code": exc.code}), flush=True)
        return 1
    except (OSError, ValueError, TypeError, KeyError):
        print(json.dumps({"ok": False, "code": "invalid_custody"}), flush=True)
        return 1
    finally:
        if journal is not None:
            os.close(journal)
        if directory is not None:
            os.close(directory.fd)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
