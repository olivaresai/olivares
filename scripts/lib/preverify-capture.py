#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
"""Bound an optional diagnostic, including its ordinary process group (Linux).

The group anchor stays alive until ECHILD, or is killed with its group BEFORE
being reaped. No signal targets a remembered/reusable PGID. A separate pipe
reports producer status; reaching the byte cap never claims complete output.
Intentional session/group escape and uninterruptible kernel tasks are outside
this contract. Failure to verify custody is reported, never a measured success.
"""
import ctypes
import os
import select
import signal
import sys
import time

LIMIT = 8192
DEADLINE = 5.0
GRACE = 2.0


def subreaper():
    if sys.platform != "linux" or sys.version_info < (3, 9):
        raise RuntimeError("Linux/Python 3.9 subreaper unavailable")
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
        raise RuntimeError("subreaper unavailable")
    value = ctypes.c_int()
    if libc.prctl(37, ctypes.byref(value), 0, 0, 0) or value.value != 1:
        raise RuntimeError("subreaper not verified")


def anchor(output, control, release, directory, command):
    os.setsid()
    subreaper()
    signal.signal(signal.SIGTERM, lambda *_: None)
    signal.signal(signal.SIGINT, lambda *_: None)
    os.write(control, b"ready\n")
    if os.read(release, 1) != b"g":
        os._exit(125)
    producer = os.fork()
    if producer == 0:
        for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGPIPE):
            signal.signal(sig, signal.SIG_DFL)
        os.dup2(output, 1)
        os.dup2(output, 2)
        os.close(output)
        os.close(control)
        os.close(release)
        try:
            os.chdir(directory)
            os.execvp(command[0], command)
        except OSError:
            os.write(2, b"sonda: no puedo ejecutar la orden en el directorio\n")
            os._exit(126)
    os.close(output)
    idle = False
    while True:
        try:
            child, status = os.waitpid(-1, os.WNOHANG)
            if child == producer:
                rc = os.waitstatus_to_exitcode(status)
                os.write(control, ("producer=%d\n" % (rc if rc >= 0 else 128 - rc)).encode())
            if child:
                continue
        except ChildProcessError:
            if not idle:
                os.write(control, b"idle\n")
                idle = True
        if select.select([release], [], [], 0.01)[0]:
            os.read(release, 1)
            # Only the parent releases us after observing ECHILD. A lost parent
            # instead tears down this still-owned group, including this anchor.
            if idle:
                os._exit(0)
            os.killpg(os.getpid(), signal.SIGKILL)


class _Cancellation:
    def __init__(self):
        self.interrupted = False

    def stop(self, *_):
        self.interrupted = True


def capture(destination, directory, command, cancellation=None):
    # An enclosing owner may already have observed TERM/INT. Reuse its monotonic
    # interrupted/stop pair instead of resetting state when signal ownership moves.
    if cancellation is None:
        cancellation = _Cancellation()
    subreaper()  # no producer exists unless this succeeds
    # Ignored SIGCHLD can auto-reap children and release the reserved leader PID.
    # Set this only in the dedicated helper, before either fork.
    signal.signal(signal.SIGCHLD, signal.SIG_DFL)
    started = time.monotonic()
    signal.signal(signal.SIGTERM, cancellation.stop)
    signal.signal(signal.SIGINT, cancellation.stop)
    target = open(destination, "wb")  # failure here must not launch a producer
    output_r, output_w = os.pipe()
    control_r, control_w = os.pipe()
    release_r, release_w = os.pipe()
    leader = os.fork()
    if leader == 0:
        os.close(output_r)
        os.close(control_r)
        os.close(release_w)
        try:
            anchor(output_w, control_w, release_r, directory, command)
        except BaseException:
            os._exit(125)
    ready = False
    signals_allowed = True
    try:
        os.close(output_w)
        os.close(control_w)
        os.close(release_r)
        os.set_blocking(output_r, False)
        os.set_blocking(control_r, False)
        idle = eof = False
        producer = "unknown"
        pending = b""
        reason = "eof"
        cleanup = "none"
        count = 0
        terminating = None
        with target:
            while True:
                now = time.monotonic()
                try:
                    chunk = os.read(control_r, 256)
                    pending += chunk
                    while b"\n" in pending:
                        line, pending = pending.split(b"\n", 1)
                        if line == b"ready":
                            ready = True
                            # Serialize signal observation with producer admission.
                            # Signals arriving after this decision are delivered on
                            # unmask and start withdrawal under the same owner.
                            mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM, signal.SIGINT})
                            try:
                                if not cancellation.interrupted and terminating is None:
                                    os.write(release_w, b"g")
                            finally:
                                signal.pthread_sigmask(signal.SIG_SETMASK, mask)
                        elif line == b"idle":
                            idle = True
                        elif line.startswith(b"producer="):
                            producer = str(int(line[9:]))
                except BlockingIOError:
                    pass
                if not eof and count < LIMIT and terminating is None:
                    try:
                        chunk = os.read(output_r, LIMIT - count)
                        if chunk:
                            target.write(chunk)
                            count += len(chunk)
                        else:
                            eof = True
                    except BlockingIOError:
                        pass
                if terminating is None:
                    if count == LIMIT:
                        reason = "limit"
                    elif cancellation.interrupted:
                        reason = "interrupted"
                    elif now - started >= DEADLINE:
                        reason = "deadline"
                    elif not eof:
                        time.sleep(0.005)
                        continue
                    if idle:
                        break
                    if ready:
                        # Anchor cannot exit while children remain. Its PID is not
                        # reaped anywhere before the LAST group signal below.
                        os.killpg(leader, signal.SIGTERM)
                        cleanup = "term"
                    terminating = now
                if idle:
                    break
                if now - terminating >= GRACE:
                    if ready:
                        os.killpg(leader, signal.SIGKILL)
                    else:
                        os.kill(leader, signal.SIGKILL)
                    cleanup = "kill"
                    break
                time.sleep(0.005)
        os.close(output_r)
        os.close(control_r)
        if idle:
            os.write(release_w, b"x")
        os.close(release_w)
        # No more group signals after this point. Reap the anchor and any children
        # adopted on KILL. Kernel scheduling adds a bounded 200ms reap allowance;
        # if it is exhausted, report unknown custody rather than claiming success.
        signals_allowed = False
        reaped = False
        until = time.monotonic() + 0.2
        while time.monotonic() < until:
            try:
                pid, _ = os.waitpid(-1, os.WNOHANG)
                if not pid:
                    time.sleep(0.005)
            except ChildProcessError:
                reaped = True
                break
        if cancellation.interrupted:
            reason = "interrupted"
        complete = "complete" if eof and reason == "eof" else "unknown"
        custody = cleanup if reaped else "unknown"
        print("producer=%s capture=%s bytes=%d completeness=%s custody=%s" %
              (producer, reason, count, complete, custody))
        if not reaped or not ready or reason == "interrupted":
            return 125
        if reason == "limit":
            return 141  # auxiliary capture outcome, NOT producer status
        if reason == "deadline":
            return 137 if cleanup == "kill" else 124
        return int(producer) if producer != "unknown" else 125
    except BaseException:
        # The producer starts only after the ready/go handshake. Prior to ready
        # there can be no producer; after ready the unreaped anchor reserves PGID.
        if signals_allowed:
            try:
                if ready:
                    os.killpg(leader, signal.SIGKILL)
                else:
                    os.kill(leader, signal.SIGKILL)
            except ProcessLookupError:
                pass
        until = time.monotonic() + 0.2
        while time.monotonic() < until:
            try:
                if not os.waitpid(-1, os.WNOHANG)[0]:
                    time.sleep(0.005)
            except ChildProcessError:
                break
        print("producer=unknown capture=unmeasured bytes=unknown completeness=unknown custody=unknown")
        return 125


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["--check"]:
            subreaper()
            sys.exit(0)
        sys.exit(capture(sys.argv[1], sys.argv[2], sys.argv[3:]))
    except (OSError, RuntimeError, ValueError, IndexError):
        print("producer=unknown capture=unmeasured bytes=0 completeness=unknown custody=unknown")
        sys.exit(125)
