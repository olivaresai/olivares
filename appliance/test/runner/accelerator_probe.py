#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# The bounded accelerator check of the appliance image prerequisite. It starts one machine
# with one explicitly named accelerator, completes the monitor handshake, confirms the
# machine is stopped before its first instruction, and then quits and releases the process
# it started.
#
# What this proves: the named accelerator initialized on this host. What it does not prove:
# that a guest boots, that firmware or a bootloader works, or how long a real image boot
# takes. A machine that never leaves the stopped state executes no guest instruction, so an
# initialization time is not a boot budget.
#
# Exit 0 the accelerator initialized and the machine reported the required state.
# Exit 2 the attempt ran and did not prove it: no accelerator, no program, a wrong state or
#        the deadline passed. That is an environmental condition, not a defect.
# Exit 1 the input is invalid: an unreadable lock, an accelerator the lock does not offer or
#        a deadline that is not positive.
#
# The observation is printed to stdout as one JSON object; diagnostics go to stderr.

import argparse
import json
import os
import io
import math
import select
import shlex
import subprocess
import sys
import time

LIMIT = (
    "This observation proves accelerator initialization only. It is not a guest boot, a "
    "firmware check or a boot-time budget."
)
DETAIL_LIMIT = 2048
RELEASE_GRACE_SECONDS = 5


class Timeout(Exception):
    """The deadline passed before the monitor answered."""


class Protocol(Exception):
    """The process ended or answered outside the monitor protocol."""


def observation_template(accelerator):
    return {
        "performed": False,
        "proved": False,
        "timed_out": False,
        "cleaned": False,
        "requested": [accelerator],
        "selected": accelerator,
        "pid": None,
        "qmp_status": None,
        "qmp_running": None,
        "detail": "",
        "limit": LIMIT,
    }


def read_line(process, buffer, deadline):
    """Return one line from the process stdout before *deadline*, or raise."""
    if time.monotonic() >= deadline:
        raise Timeout("the monitor did not answer before the deadline")
    while b"\n" not in buffer["data"]:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise Timeout("the monitor did not answer before the deadline")
        ready, _, _ = select.select([process.stdout, process.stderr], [], [], remaining)
        if not ready:
            raise Timeout("the monitor did not answer before the deadline")
        if process.stderr in ready:
            chunk = os.read(process.stderr.fileno(), 4096)
            buffer["stderr"] = (buffer.get("stderr", b"") + chunk)[-DETAIL_LIMIT:]
        if process.stdout not in ready:
            continue
        chunk = os.read(process.stdout.fileno(), 4096)
        if not chunk:
            raise Protocol("the process ended before the handshake completed")
        buffer["data"] += chunk
        if len(buffer["data"]) > 65536:
            raise Protocol("the monitor exceeded the 65536-byte message limit")
    line, _, rest = buffer["data"].partition(b"\n")
    buffer["data"] = rest
    return line.decode("utf-8", "replace").strip()


def read_reply(process, buffer, deadline):
    """Read monitor lines until one carries a result, skipping asynchronous events."""
    while True:
        line = read_line(process, buffer, deadline)
        if not line:
            continue
        try:
            message = json.loads(line)
        except ValueError:
            continue
        if "return" in message or "error" in message:
            return message


def send(process, command):
    process.stdin.write((json.dumps({"execute": command}) + "\n").encode("utf-8"))
    process.stdin.flush()


def release(process, observation):
    """End and reap the process this probe started, then record the remaining output."""
    if process.poll() is None:
        try:
            process.terminate()
            process.wait(timeout=RELEASE_GRACE_SECONDS)
        except subprocess.TimeoutExpired:
            process.kill()
            try:
                process.wait(timeout=RELEASE_GRACE_SECONDS)
            except subprocess.TimeoutExpired:
                pass
        except OSError:
            pass

    # The program's own diagnostic is the evidence an operator needs, so it leads the
    # detail even when this probe already described the protocol failure it saw.
    try:
        remaining = b""
        if isinstance(process.stderr, io.BytesIO):
            remaining = process.stderr.read(DETAIL_LIMIT)
        # Never wait for EOF: another process may still own the pipe writer.
        end = time.monotonic() + 0.1
        while not isinstance(process.stderr, io.BytesIO) and len(remaining) < DETAIL_LIMIT and time.monotonic() < end:
            ready, _, _ = select.select([process.stderr], [], [], 0)
            if not ready:
                break
            chunk = os.read(process.stderr.fileno(), min(4096, DETAIL_LIMIT - len(remaining)))
            if not chunk:
                break
            remaining += chunk
    except (OSError, ValueError, AttributeError, TypeError):
        remaining = b""
    text = remaining.decode("utf-8", "replace").strip()
    if text:
        if observation["detail"]:
            text = f"{text} ({observation['detail']})"
        observation["detail"] = text[:DETAIL_LIMIT]

    for stream in (process.stdin, process.stdout, process.stderr):
        try:
            if stream is not None:
                stream.close()
        except OSError:
            pass

    observation["cleaned"] = process.poll() is not None


def probe(qemu_argv, accelerator, machine, required_status, timeout):
    """Start one machine, confirm the required state and release the process."""
    observation = observation_template(accelerator)
    argv = list(qemu_argv) + [
        "-accel", accelerator,
        "-machine", machine,
        "-display", "none",
        "-nodefaults",
        "-no-user-config",
        "-S",
        "-qmp", "stdio",
    ]

    try:
        process = subprocess.Popen(
            argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except OSError as error:
        # Nothing was started, so nothing is owned. An absent program is an
        # environmental condition, not a proof and not a defect.
        observation["performed"] = True
        observation["cleaned"] = True
        observation["detail"] = str(error)[:DETAIL_LIMIT]
        return observation

    observation["performed"] = True
    observation["pid"] = process.pid
    buffer = {"data": b""}
    deadline = time.monotonic() + timeout

    try:
        read_reply_or_greeting(process, buffer, deadline)
        send(process, "qmp_capabilities")
        capabilities = read_reply(process, buffer, deadline)
        if capabilities.get("return") != {} or "error" in capabilities:
            raise Protocol("the monitor refused capability negotiation")
        send(process, "query-status")
        reply = read_reply(process, buffer, deadline)

        result = reply.get("return")
        if not isinstance(result, dict):
            raise Protocol(f"the monitor refused the status query: {reply}")
        observation["qmp_status"] = result.get("status")
        observation["qmp_running"] = result.get("running")
        observation["proved"] = (
            observation["qmp_status"] == required_status
            and observation["qmp_running"] is False
        )
        send(process, "quit")
    except Timeout as error:
        observation["timed_out"] = True
        observation["detail"] = str(error)[:DETAIL_LIMIT]
    except (Protocol, OSError, ValueError) as error:
        observation["detail"] = str(error)[:DETAIL_LIMIT]
    finally:
        diagnostic = buffer.get("stderr", b"").decode("utf-8", "replace")
        observation["detail"] = (diagnostic + observation["detail"])[-DETAIL_LIMIT:]
        release(process, observation)
        if not observation["cleaned"]:
            observation["proved"] = False

    return observation


def read_reply_or_greeting(process, buffer, deadline):
    """Consume the monitor greeting, which carries no result field."""
    line = read_line(process, buffer, deadline)
    try:
        message = json.loads(line)
    except ValueError as error:
        raise Protocol(f"the monitor greeting was not JSON: {error}") from error
    if "QMP" not in message:
        raise Protocol("the process did not present a monitor greeting")
    return message


def refuse(message):
    print(json.dumps({"performed": False, "proved": False, "detail": message}, indent=2))
    print(f"refused: {message}", file=sys.stderr)
    return 1


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Prove that one named accelerator initializes on this host."
    )
    parser.add_argument("--lock", required=True, help="path to the checked-in input lock")
    parser.add_argument("--accelerator", required=True, help="the accelerator to name")
    parser.add_argument(
        "--qemu",
        default=None,
        help="the machine program to start; defaults to the binary the lock names. The "
             "harness uses this to drive the probe against a stub program.",
    )
    parser.add_argument("--timeout", type=float, default=None, help="deadline in seconds")
    arguments = parser.parse_args(argv)

    try:
        with open(arguments.lock, "rb") as handle:
            lock = json.loads(handle.read().decode("utf-8"))
    except (OSError, ValueError, UnicodeDecodeError) as error:
        return refuse(f"the lock could not be read as JSON: {error}")

    settings = lock.get("probe")
    if not isinstance(settings, dict):
        return refuse("the lock states no probe settings")

    preference = settings.get("accelerator_preference") or []
    if arguments.accelerator not in preference:
        return refuse(
            f"the lock does not offer the accelerator {arguments.accelerator!r}; "
            f"it names {preference}"
        )

    timeout = arguments.timeout
    if timeout is None:
        timeout = settings.get("qemu_timeout_seconds")
    if type(timeout) not in (int, float) or not math.isfinite(timeout) or not 0 < timeout <= 60:
        return refuse(f"the deadline must be positive, not {timeout!r}")

    qemu = arguments.qemu or settings.get("qemu_binary")
    if not qemu:
        return refuse("the lock names no machine program")
    qemu_argv = shlex.split(qemu)

    machine = settings.get("qemu_machine") or "q35"
    required_status = settings.get("required_qmp_status")
    if not required_status:
        return refuse("the lock states no required monitor state")

    observation = probe(qemu_argv, arguments.accelerator, machine, required_status, timeout)
    print(json.dumps(observation, indent=2, sort_keys=True))
    if not observation["proved"]:
        print(
            f"not proved: accelerator={arguments.accelerator} "
            f"status={observation['qmp_status']} timed_out={observation['timed_out']} "
            f"detail={observation['detail'][:200]}",
            file=sys.stderr,
        )
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
