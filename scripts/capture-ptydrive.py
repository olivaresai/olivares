#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Drive a REAL interactive shell under a PTY and record it as asciicast v2.

Every byte in the resulting cast is a byte the shell actually wrote to its
terminal. The driver only supplies keystrokes, the way a person would; it never
writes to the transcript itself.

Script grammar (one directive per line, '#' comments ignored):
    type <text>       send <text> keystroke by keystroke (human-ish cadence)
    line <text>       type <text> then Enter
    key <name>        enter | ctrl-c | ctrl-d | tab
    wait <seconds>    idle, letting the shell's own output arrive
    expect <regex>    block until the regex matches the output seen so far
    comment <text>    type a shell '# text' comment line (real, harmless, shown)

Usage: capture-ptydrive.py --script S.txt [--cols N --rows N] OUT.cast [-- CMD [ARGS...]]
       (options BEFORE the positional: the trailing CMD is argparse.REMAINDER and would
        otherwise swallow them)
"""
import argparse
import fcntl
import json
import os
import pty
import re
import select
import signal
import struct
import sys
import termios
import time

TYPE_DELAY = 0.032
TYPE_JITTER = 0.030


def set_winsize(fd, rows, cols):
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


class Recorder:
    def __init__(self, master, t0):
        self.master = master
        self.t0 = t0
        self.events = []
        self.text = ""

    def pump(self, seconds):
        """Read whatever the shell emits for `seconds`, recording it."""
        end = time.time() + seconds
        while True:
            remain = end - time.time()
            if remain <= 0:
                return
            try:
                r, _, _ = select.select([self.master], [], [], min(remain, 0.05))
            except InterruptedError:
                continue
            if self.master not in r:
                continue
            try:
                data = os.read(self.master, 65536)
            except OSError:
                return
            if not data:
                return
            s = data.decode("utf-8", "replace")
            self.events.append([round(time.time() - self.t0, 6), "o", s])
            self.text += s

    def send(self, data: bytes):
        os.write(self.master, data)

    def expect(self, pattern, timeout=60.0):
        rx = re.compile(pattern)
        end = time.time() + timeout
        start_len = 0
        while time.time() < end:
            if rx.search(self.text[start_len:]):
                return True
            self.pump(0.05)
        return False


KEYS = {"enter": b"\r", "ctrl-c": b"\x03", "ctrl-d": b"\x04", "tab": b"\t"}


def run_script(rec, lines, jitter_seed=0, prompt=None):
    import random

    rnd = random.Random(1234 + jitter_seed)
    prompt_rx = re.escape(prompt) if prompt else None
    # The prompt carries SGR colour, so the raw stream never contains its plain
    # text. Matching has to be done on the stripped view or the wait never ends.
    ansi = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][A-Z0-9]|\x1b[=>]")

    def prompt_count():
        return len(re.findall(prompt_rx, ansi.sub("", rec.text))) if prompt_rx else 0

    def await_prompt(before):
        # A command that talks to the network finishes AFTER the driver would have
        # typed the next line, and the transcript then shows the next command above
        # the previous one's output. Waiting for the prompt to come back is what a
        # person does; without it the recording is a race.
        #
        # It COUNTS prompts rather than looking for one after a byte offset: a fast
        # command's prompt can arrive inside the settle pump, and an offset-based
        # wait then blocks 60s for a prompt that already came — which is how the
        # first version of this managed to be both slow AND still out of order.
        if not prompt_rx:
            return
        end = time.time() + 60.0
        while time.time() < end:
            if prompt_count() > before:
                return
            rec.pump(0.05)
        print("ptydrive: WARNING prompt never returned; the take may be out of order",
              file=sys.stderr)

    for raw in lines:
        line = raw.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        verb, _, rest = line.partition(" ")
        verb = verb.strip()
        if verb in ("type", "line", "comment"):
            text = rest if verb != "comment" else "# " + rest
            for chunk in text:
                rec.send(chunk.encode())
                rec.pump(max(0.008, TYPE_DELAY + rnd.uniform(-TYPE_JITTER, TYPE_JITTER) * 0.5))
            if verb in ("line", "comment"):
                rec.pump(0.18)
                before = prompt_count()
                rec.send(b"\r")
                rec.pump(0.12)
                await_prompt(before)
        elif verb == "key":
            rec.send(KEYS[rest.strip()])
            rec.pump(0.12)
        elif verb == "wait":
            rec.pump(float(rest.strip()))
        elif verb == "expect":
            ok = rec.expect(rest.strip())
            if not ok:
                print(f"ptydrive: EXPECT TIMEOUT: {rest.strip()!r}", file=sys.stderr)
                return 3
        else:
            print(f"ptydrive: unknown directive {verb!r}", file=sys.stderr)
            return 2
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--script", required=True)
    ap.add_argument("--cols", type=int, default=100)
    ap.add_argument("--rows", type=int, default=30)
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--transcript", default="")
    ap.add_argument("--prompt", default="olivares:~$ ")
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    args = ap.parse_args()

    cmd = args.cmd
    if cmd and cmd[0] == "--":
        cmd = cmd[1:]
    if not cmd:
        cmd = ["bash", "--norc", "--noprofile", "-i"]

    with open(args.script, encoding="utf-8") as fh:
        script_lines = fh.readlines()

    pid, master = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["COLUMNS"] = str(args.cols)
        os.environ["LINES"] = str(args.rows)
        os.environ["LC_ALL"] = "C.UTF-8"
        os.environ["PS1"] = "\\[\\033[38;5;75m\\]olivares\\[\\033[0m\\]:\\[\\033[38;5;245m\\]~\\[\\033[0m\\]$ "
        os.environ.pop("PROMPT_COMMAND", None)
        os.execvp(cmd[0], cmd)
        os._exit(127)

    set_winsize(master, args.rows, args.cols)
    t0 = time.time()
    rec = Recorder(master, t0)
    rec.pump(0.6)  # let the shell paint its first prompt

    rc = 0
    t_end = None
    try:
        rc = run_script(rec, script_lines, args.seed, args.prompt)
        # The demo ends when the script ends. Everything after this instant is the
        # driver closing the shell (bash echoing "exit"), which is not part of what is
        # being shown, so it is not kept.
        t_end = time.time() - t0
    finally:
        try:
            rec.send(b"\x04")
            rec.pump(0.5)
        except OSError:
            pass
        try:
            os.close(master)
        except OSError:
            pass
        try:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
        except (ProcessLookupError, ChildProcessError):
            pass

    header = {
        "version": 2,
        "width": args.cols,
        "height": args.rows,
        "timestamp": int(t0),
        "env": {"TERM": "xterm-256color", "SHELL": "/bin/bash"},
    }
    kept = rec.events if t_end is None else [e for e in rec.events if e[0] <= t_end]
    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write(json.dumps(header) + "\n")
        for ev in kept:
            fh.write(json.dumps(ev, ensure_ascii=False) + "\n")
    if args.transcript:
        with open(args.transcript, "w", encoding="utf-8") as fh:
            fh.write(rec.text)

    dur = kept[-1][0] if kept else 0.0
    print(f"ptydrive: {args.out} · {len(kept)} events · {dur:.2f}s · rc={rc}", file=sys.stderr)
    return rc


if __name__ == "__main__":
    sys.exit(main())
