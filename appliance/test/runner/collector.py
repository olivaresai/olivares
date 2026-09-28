#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Own one runner prerequisite attempt and preserve its observed failure state."""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import pwd
import re
import signal
import sys
import time
import uuid

import accelerator_probe
import judge
from owned_resources import LABEL, build_terminal, loop_inventory, read_json, release_container, release_image, release_loop, run

ROOT = Path(__file__).resolve().parents[3]


def now():
    return datetime.datetime.now(datetime.timezone.utc).strftime(judge.TIMESTAMP)


def atomic_json(path, value):
    temporary = path.with_suffix(path.suffix + ".new")
    with temporary.open("w", encoding="utf-8") as output:
        json.dump(value, output, indent=2, sort_keys=True)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    temporary.replace(path)


def build_digest(root=ROOT):
    digest = hashlib.sha256()
    for path in sorted((root / "appliance/images/toolchain").rglob("*")):
        if path.is_file() and "__pycache__" not in path.parts:
            digest.update(str(path.relative_to(root)).encode() + b"\0")
            digest.update(path.read_bytes() + b"\0")
    return digest.hexdigest()


def qualified(lock, qualification):
    """The builder's own qualification report holds what this lock requires: the locked KIWI version, the three checks,
    the package inventory of its base (rpm on the Fedora builder, dpkg on the Debian one), the Python inventory, and
    build provenance naming this lock's canonical digest."""
    inventory = "rpm_inventory" if lock.get("distribution", {}).get("id") == "fedora" else "dpkg_inventory"
    return (qualification.get("kiwi_version") == lock["toolchain"]["kiwi"]["upstream_version"]
            and all(qualification.get(key) is True for key in ("dependency_check", "cli_help_check", "imports_check"))
            and bool(qualification.get(inventory)) and bool(qualification.get("python_inventory"))
            and qualification.get("build_provenance", {}).get("lock_sha256")
            == hashlib.sha256(json.dumps(lock, indent=2, sort_keys=True).encode() + b"\n").hexdigest())


def observation():
    return {"performed": False, "proved": False, "timed_out": False, "cleaned": False, "detail": "not started"}


def container_probe(lock, work, attempt, retain=False, command=run):
    result = observation()
    result.update(performed=True, image_tag=lock["invocation"]["image_tag"], image_id="", image_removed=False)
    iidfile = work / "builder.iid"
    container_id = ""
    image_id = ""
    build_finished = False
    creation_started = False
    try:
        # The builder the lock names: its Containerfile, with that directory as the build context. A lock that names
        # none is the Debian builder's, as before the Fedora successor existed.
        containerfile = ROOT / lock["invocation"].get("containerfile", "appliance/images/toolchain/Containerfile")
        built = command(["docker", "build", "--iidfile", str(iidfile), "--label", LABEL + "=" + attempt,
                         "--file", str(containerfile), str(containerfile.parent)],
                        lock["probe"]["container_timeout_seconds"])
        build_finished = build_terminal(built)
        result.update(timed_out=built.timed_out, detail=built.output[-2048:])
        if not build_finished:
            return result
        image_id = iidfile.read_text().strip()
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
            raise ValueError("build produced no immutable image ID")
        image = read_json(command(["docker", "image", "inspect", image_id]))[0]
        if image.get("Id") != image_id or image.get("Architecture") != "amd64" or image.get("Config", {}).get("Labels", {}).get(LABEL) != attempt:
            raise ValueError("image identity, architecture or ownership differs")
        result["image_id"] = image_id
        # Create and retain the exact ID before starting; a timed-out start remains cleanable.
        creation_started = True
        created = command(["docker", "create", "--network", "none", "--label", LABEL + "=" + attempt,
                           "--name", "appliance-probe-" + attempt, image_id, "python3", "/toolchain/install.py", "report"])
        if created.code != 0 or created.timed_out:
            result["detail"] = "container creation outcome unknown: " + created.output[-1024:]
            build_finished = False
            return result
        container_id = created.output.strip()
        if not re.fullmatch(r"[0-9a-f]{64}", container_id):
            raise ValueError("container create returned no immutable ID")
        reported = command(["docker", "start", "--attach", container_id], lock["probe"]["qemu_timeout_seconds"])
        result["timed_out"] = reported.timed_out
        state = read_json(command(["docker", "container", "inspect", container_id]))[0]["State"]
        lines = reported.output.strip().splitlines()
        matched = re.search(r"\b\d+\.\d+\.\d+\b", lines[0] if lines else "")
        qualification = json.loads(lines[1]) if len(lines) == 2 else {}
        if not qualified(lock, qualification):
            raise ValueError("toolchain qualification inventory or locked provenance missing")
        result["qualification"] = qualification
        result["reported_version"] = matched.group(0) if matched else ""
        result["proved"] = (reported.code == 0 and not reported.timed_out and state.get("Running") is False
                            and state.get("ExitCode") == 0 and result["reported_version"] == lock["toolchain"]["kiwi"]["upstream_version"])
        result["detail"] = reported.output[-2048:]
    except (ValueError, OSError, KeyError, IndexError, TypeError) as error:
        result["detail"] = str(error)[:2048]
    finally:
        container_clean = release_container(command, container_id, attempt) if container_id else (build_finished and not creation_started)
        keep = retain and result["proved"] and container_clean and build_finished
        image_clean = release_image(command, image_id, attempt) if image_id and not keep else False
        result["image_removed"] = image_clean
        result["image_retained"] = keep
        result["cleaned"] = build_finished and container_clean and (image_clean or keep)
        result["cleanup_scope"] = "live containers released; immutable image retained for same-job custody" if keep else "container and image release observed"
        if not result["cleaned"]:
            result["proved"] = False
    return result


def loop_probe(lock, work, command=run):
    result = observation()
    result.update(performed=True, image_bytes=lock["probe"]["loop_image_bytes"], owner_uid=os.getuid(), mounted=False)
    backing = work / "loop-probe.img"
    mount = work / "loop-probe-mount"
    device = ""
    try:
        with backing.open("xb") as output:
            output.truncate(result["image_bytes"])
        mount.mkdir()
        attached = command(["sudo", "-n", "losetup", "--find", "--show", str(backing)])
        result["timed_out"] = attached.timed_out
        device = attached.output.strip()
        result["loop_device"] = device
        if attached.code != 0 or not re.fullmatch(r"/dev/loop[0-9]+", device):
            raise ValueError("loop allocation outcome unavailable; backing file retained")
        if not any(row.get("name") == device and row.get("back-file") == str(backing) for row in loop_inventory(command)):
            raise ValueError("loop ownership not established; no format or detach allowed")
        for argv in (["sudo", "-n", "mkfs.ext4", "-q", "-F", device],
                     ["sudo", "-n", "mount", device, str(mount)]):
            step = command(argv)
            if step.code != 0 or step.timed_out:
                result["timed_out"] = step.timed_out
                raise ValueError("owned filesystem operation failed: " + step.output[-1024:])
        result["mounted"] = True
        # The mount is root-owned. No shell expression or caller path enters this write.
        step = command(["sudo", "-n", "touch", str(mount / "probe")])
        result["proved"] = step.code == 0 and not step.timed_out
        result["detail"] = step.output[-2048:]
    except (ValueError, KeyError, OSError, TypeError) as error:
        result["detail"] = str(error)[:2048]
    finally:
        result["cleaned"] = release_loop(command, device, backing, mount) if device else False
        if not result["cleaned"]:
            result["proved"] = False
    return result


def accelerator_trials(lock, probe=accelerator_probe.probe):
    trials = []
    selected = observation()
    for accelerator in lock["probe"]["accelerator_preference"]:
        start = time.monotonic()
        selected = probe(["qemu-system-x86_64"], accelerator, "q35", "prelaunch", lock["probe"]["qemu_timeout_seconds"])
        selected["elapsed_ms"] = max(1, int((time.monotonic() - start) * 1000))
        trials.append(dict(selected))
        if selected["proved"] or not selected["cleaned"]:
            break
    selected["attempts"] = trials
    selected["requested"] = lock["probe"]["accelerator_preference"]
    return selected


def current_binding(lock_path, attempt):
    source = run(["git", "-C", str(ROOT), "rev-parse", "HEAD"])
    changed = run(["git", "-C", str(ROOT), "status", "--porcelain", "--untracked-files=normal"])
    if source.code != 0 or changed.code != 0 or changed.output.strip():
        raise ValueError("source must be a clean pinned checkout")
    values = {key: os.environ.get(variable, "") for key, variable in
              (("run_id", "GITHUB_RUN_ID"), ("run_attempt", "GITHUB_RUN_ATTEMPT"), ("job", "GITHUB_JOB"))}
    if not all(values.values()):
        raise ValueError("runner run, attempt and job identity are required")
    values.update(source_commit=source.output.strip(), build_input_digest=build_digest())
    return values


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--lock", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--retain-builder", action="store_true", help="transfer passive image custody within this job; never a cross-job receipt")
    args = parser.parse_args(argv)
    evidence = Path(args.evidence).resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    if (evidence / "receipt.json").exists() or (evidence / "work").exists():
        print("refusing an existing attempt directory", file=sys.stderr)
        return 1
    start = time.monotonic()
    attempt = uuid.uuid4().hex
    receipt = {"schema": judge.RECEIPT_SCHEMA, "attempt": {"id": attempt, "started_at": now()},
               "observations": {name: observation() for name in judge.MANDATORY_OBSERVATIONS}, "refusals": []}
    lock = {}
    status = 1
    try:
        lock_path = Path(args.lock).resolve()
        lock = json.loads(lock_path.read_bytes())
        defects = judge.validate_lock(lock)
        if defects:
            raise ValueError("invalid lock: " + ",".join(defects))
        installer_path = ROOT / "appliance/images/toolchain/install.py"
        spec = importlib.util.spec_from_file_location("appliance_installer", installer_path)
        installer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(installer)
        installer.validate(lock)
        receipt["attempt"].update(lock_id=lock["id"], lock_digest=judge.sha256_file(lock_path))
        receipt["binding"] = current_binding(lock_path, attempt)
        unresolved = lock.get("resolution", {}).get("unresolved", [])
        if unresolved:
            raise ValueError("toolchain.dependency_unresolved:" + json.dumps(unresolved, sort_keys=True))
        work = evidence / "work"
        work.mkdir()
        memory = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
        receipt["runner"] = {"image_os": os.environ.get("ImageOS", ""), "image_version": os.environ.get("ImageVersion", ""),
                             "os_pretty_name": platform.platform(), "kernel": platform.release(), "uname_machine": platform.machine(),
                             "uid": os.getuid(), "user": pwd.getpwuid(os.getuid()).pw_name, "home": str(Path.home()),
                             "passwordless_sudo": run(["sudo", "-n", "true"]).code == 0,
                             "cgroup_fs": run(["stat", "-fc", "%T", "/sys/fs/cgroup"]).output.strip()}
        receipt["mounts"] = read_json(run(["findmnt", "--json", "--list", "--output", "TARGET,FSTYPE,OPTIONS"]))["filesystems"]
        receipt["tools"] = {name: {"version": run(command).output[:1024]} for name, command in (
            ("docker", ["docker", "--version"]), ("qemu-system-x86_64", ["qemu-system-x86_64", "--version"]),
            ("losetup", ["losetup", "--version"]))}
        receipt["observations"]["container_toolchain"] = container_probe(lock, work, attempt, args.retain_builder)
        receipt["observations"]["accelerator"] = accelerator_trials(lock)
        receipt["observations"]["loop_privilege"] = loop_probe(lock, work)
        disk = os.statvfs(evidence)
        receipt["capacity"] = {"processors": os.cpu_count(), "memory_total_bytes": int(memory["MemTotal"].split()[0])*1024,
            "memory_available_bytes": int(memory["MemAvailable"].split()[0])*1024, "evidence_avail_bytes": disk.f_bavail*disk.f_frsize,
            "probe_allocation_bytes": lock["probe"]["loop_image_bytes"], "declared_build_bytes": lock["budget"]["declared_build_bytes"],
            "declared_build_measured": False, "after_toolchain": True}
    except (OSError, ValueError, KeyError, TypeError, KeyboardInterrupt) as error:
        receipt["refusals"].append({"name": "collection_incomplete", "reason": str(error)[:4096]})
    finally:
        receipt["attempt"].update(finished_at=now(), elapsed_ms=max(1, int((time.monotonic()-start)*1000)))
        verdict = judge.judge(lock, receipt, receipt["attempt"].get("lock_digest"))
        builder = receipt["observations"]["container_toolchain"]
        if verdict.state != "eligible" and builder.get("image_retained"):
            removed = release_image(run, builder["image_id"], attempt)
            builder.update(image_removed=removed, image_retained=not removed)
            if not removed:
                builder["cleaned"] = False
            verdict = judge.judge(lock, receipt, receipt["attempt"].get("lock_digest"))
        if receipt["refusals"]:
            verdict = judge.Verdict("defect", verdict.reasons + ["collection.incomplete"], verdict.notes)
        receipt["judgement"] = verdict.payload()
        atomic_json(evidence / "receipt.json", receipt)
        print(json.dumps(verdict.payload(), sort_keys=True))
        status = verdict.exit_code
    return status


if __name__ == "__main__":
    def interrupted(signum, frame):
        raise KeyboardInterrupt("runner interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())
