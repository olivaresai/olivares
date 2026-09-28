# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Bounded commands and narrowly owned cleanup. No discovery grants ownership."""
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import select
import signal
import subprocess
import time

LIMIT = 262144
LABEL = "org.olivares.appliance.attempt"


@dataclass
class Result:
    code: int
    output: str
    timed_out: bool = False
    joined: bool = True


def run(argv, timeout=10, metadata=None):
    """Drain a finite output tail; terminate only this newly owned process group."""
    environment = {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
                   "LANG": "C.UTF-8", "DOCKER_BUILDKIT": "1"}
    if metadata:
        allowed = {"GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_JOB", "ImageOS", "ImageVersion", "HOME"}
        environment.update({key: value for key, value in metadata.items() if key in allowed})
    try:
        child = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                 env=environment, start_new_session=True)
    except OSError as error:
        return Result(127, str(error))
    output = b""
    deadline = time.monotonic() + timeout
    expired = False
    try:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                expired = True
                break
            ready, _, _ = select.select([child.stdout], [], [], min(remaining, 0.1))
            if ready:
                chunk = os.read(child.stdout.fileno(), 8192)
                output = (output + chunk)[-LIMIT:]
                if not chunk and child.poll() is not None:
                    break
            elif child.poll() is not None:
                break
    finally:
        if child.poll() is None or expired:
            for sig in (signal.SIGTERM, signal.SIGKILL):
                try:
                    os.killpg(child.pid, sig)
                except ProcessLookupError:
                    pass
                try:
                    child.wait(timeout=2)
                    break
                except subprocess.TimeoutExpired:
                    pass
        child.stdout.close()
    return Result(124 if expired else child.returncode, output.decode("utf-8", "replace"),
                  expired, child.poll() is not None)


def build_terminal(result):
    # A cancelled client is no proof that the Docker daemon ended a build.
    return result.joined and not result.timed_out and result.code == 0


def read_json(result):
    if result.code != 0 or result.timed_out or not result.joined:
        raise ValueError("observation failed")
    return json.loads(result.output)


def loop_inventory(run_command):
    return read_json(run_command(["sudo", "-n", "losetup", "--json", "--list", "--output", "NAME,BACK-FILE"]))["loopdevices"]


def release_loop(run_command, device, backing, mount):
    """Keep the file until owned detachment and absence are both observed."""
    try:
        inventory = loop_inventory(run_command)
        owned = [row for row in inventory if row.get("name") == device and row.get("back-file") == str(backing)]
        if len(owned) != 1:
            return False
        mounts = read_json(run_command(["findmnt", "--json", "--list", "--output", "TARGET,SOURCE"]))["filesystems"]
        at_mount = [row for row in mounts if row.get("target") == str(mount)]
        if at_mount:
            if len(at_mount) != 1 or at_mount[0].get("source") != device:
                return False
            if run_command(["sudo", "-n", "umount", str(mount)]).code != 0:
                return False
            mounts = read_json(run_command(["findmnt", "--json", "--list", "--output", "TARGET,SOURCE"]))["filesystems"]
            if any(row.get("target") == str(mount) or row.get("source") == device for row in mounts):
                return False
        # Ownership is checked again immediately before detach.
        if not any(row.get("name") == device and row.get("back-file") == str(backing)
                   for row in loop_inventory(run_command)):
            return False
        if run_command(["sudo", "-n", "losetup", "--detach", device]).code != 0:
            return False
        if any(row.get("name") == device or row.get("back-file") == str(backing)
               for row in loop_inventory(run_command)):
            return False
        backing.unlink()
        if mount.exists():
            mount.rmdir()
        return True
    except (ValueError, KeyError, TypeError, OSError):
        return False


def release_image(run_command, image_id, attempt):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
        return False
    try:
        rows = read_json(run_command(["docker", "image", "inspect", image_id]))
        if len(rows) != 1 or rows[0].get("Id") != image_id or rows[0].get("Config", {}).get("Labels", {}).get(LABEL) != attempt:
            return False
        result = run_command(["docker", "image", "rm", image_id])
        if result.code != 0 or result.timed_out:
            return False
        # A successful list, not a failed inspect, is evidence of absence.
        result = run_command(["docker", "image", "ls", "--no-trunc", "--quiet"])
        return result.code == 0 and not result.timed_out and image_id not in result.output.splitlines()
    except (ValueError, KeyError, TypeError):
        return False


def release_container(run_command, container_id, attempt):
    if not re.fullmatch(r"[0-9a-f]{64}", container_id):
        return False
    try:
        rows = read_json(run_command(["docker", "container", "inspect", container_id]))
        if len(rows) != 1 or rows[0].get("Id") != container_id or rows[0].get("Config", {}).get("Labels", {}).get(LABEL) != attempt:
            return False
        if rows[0].get("State", {}).get("Running") is True:
            stopped = run_command(["docker", "container", "stop", "--time", "3", container_id])
            if stopped.code != 0 or stopped.timed_out:
                return False
        rows = read_json(run_command(["docker", "container", "inspect", container_id]))
        if rows[0].get("State", {}).get("Running") is not False:
            return False
        removed = run_command(["docker", "container", "rm", container_id])
        listed = run_command(["docker", "container", "ls", "--all", "--no-trunc", "--quiet"])
        return removed.code == 0 and listed.code == 0 and container_id not in listed.output.splitlines()
    except (ValueError, KeyError, TypeError):
        return False
