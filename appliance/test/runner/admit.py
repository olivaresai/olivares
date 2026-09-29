#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Admit or release the exact retained builder in its current owning job."""
import argparse
import json
import os
from pathlib import Path
import platform
import sys

import collector
import judge
from owned_resources import LABEL, read_json, release_image, run


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--lock", required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("--attempt", required=True)
    parser.add_argument("--image-id", required=True)
    parser.add_argument("--accelerator", help="consumer choice required for admission; release needs no choice")
    parser.add_argument("--release", action="store_true")
    args = parser.parse_args(argv)
    path = Path(args.receipt)
    outcome = {"state": "defect", "reasons": []}
    try:
        receipt = json.loads(path.read_bytes())
        lock = json.loads(Path(args.lock).read_bytes())
        if not isinstance(receipt, dict):
            raise ValueError("receipt.type")
        if not isinstance(lock, dict):
            raise ValueError("lock.type")
        # Release needs custody, not a still-valid qualification or clean source.
        containers = ("attempt", "binding", "observations", "observations.container_toolchain")
        if not args.release:
            containers += ("runner", "capacity", "observations.accelerator", "observations.loop_privilege")
        for pointer in containers:
            if not isinstance(judge.walk(receipt, pointer)[1], dict):
                raise ValueError("receipt.type:" + pointer)
        if receipt["attempt"].get("id") != args.attempt or receipt["observations"]["container_toolchain"].get("image_id") != args.image_id:
            raise ValueError("caller attempt or image differs")
        for key, variable in (("run_id", "GITHUB_RUN_ID"), ("run_attempt", "GITHUB_RUN_ATTEMPT"), ("job", "GITHUB_JOB")):
            if not os.environ.get(variable) or receipt["binding"].get(key) != os.environ[variable]:
                raise ValueError("cross-job or cross-attempt builder custody refused")
        if args.release:
            released = release_image(run, args.image_id, args.attempt)
            outcome = {"state": "released" if released else "defect", "image_id": args.image_id,
                       "attempt_id": args.attempt, "image_removed": released, "observed_at": collector.now()}
            collector.atomic_json(path.parent / "builder-release.json", outcome)
            print(json.dumps(outcome))
            return 0 if released else 1
        if args.accelerator not in ("kvm", "tcg"):
            raise ValueError("admission.accelerator_required")
        lock_digest = judge.sha256_file(args.lock)
        # Refuse invalid evidence before current_binding performs any live lookup.
        verdict = judge.judge(lock, receipt, lock_digest)
        history_defects = judge.check_accelerator_history(lock, receipt)
        if verdict.state != "eligible" or history_defects:
            raise ValueError("current admission refused: " + ",".join(verdict.reasons + history_defects))
        if receipt["observations"]["accelerator"].get("selected") != args.accelerator:
            raise ValueError("admission.mismatch:accelerator")
        toolchain = receipt["observations"]["container_toolchain"]
        if toolchain.get("image_retained") is not True or toolchain.get("image_removed") is not False:
            raise ValueError("standalone or released builder is not admissible")
        if receipt["capacity"].get("after_toolchain") is not True:
            raise ValueError("admission.capacity_not_current")
        current = collector.current_binding(Path(args.lock), args.attempt)
        expected = dict(current, attempt_id=args.attempt, image_id=args.image_id,
                        lock_digest=lock_digest, architecture=platform.machine(),
                        accelerator=args.accelerator)
        verdict = judge.judge(lock, receipt, lock_digest, expected)
        if verdict.state != "eligible":
            raise ValueError("current admission refused: " + ",".join(verdict.reasons))
        image = read_json(run(["docker", "image", "inspect", args.image_id]))[0]
        if image.get("Id") != args.image_id or image.get("Architecture") != "amd64" or image.get("Config", {}).get("Labels", {}).get(LABEL) != args.attempt:
            raise ValueError("current immutable image identity or custody differs")
        containers = run(["docker", "container", "ls", "--all", "--quiet", "--filter", "label=" + LABEL + "=" + args.attempt])
        if containers.code != 0 or containers.timed_out or containers.output.strip():
            raise ValueError("container cleanup is not currently confirmed")
        disk = os.statvfs(path.parent)
        free = disk.f_bavail * disk.f_frsize
        if free < lock["budget"]["declared_build_bytes"]:
            raise ValueError("current capacity is below the declared budget")
        outcome = {"state": "admitted", "image_id": args.image_id, "accelerator": args.accelerator,
                   "expected_context": expected, "current_available_bytes": free,
                   "observed_at": collector.now(), "image_custody": "retained; consumer owes exact-ID release in finally/always"}
    except (OSError, ValueError, KeyError, IndexError, TypeError) as error:
        outcome["reasons"] = [str(error)]
    collector.atomic_json(path.parent / "admission.json", outcome)
    print(json.dumps(outcome))
    return 0 if outcome["state"] == "admitted" else 1


if __name__ == "__main__":
    sys.exit(main())
