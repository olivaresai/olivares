# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Fixture builders for the image toolchain prerequisite tests. Each builder returns a
# complete, internally consistent object, so a test states its subject by mutating exactly
# one field and naming the classification that must follow. Nothing here reads the host or
# the checked-in lock: these are literals, so a test failure names a judgement rule rather
# than a property of the machine that ran it.

import hashlib
import json

LOCK_SCHEMA = "olivares-appliance-image-toolchain-lock/v1"
RECEIPT_SCHEMA = "olivares-appliance-runner-receipt/v1"

# A digest that is well formed and belongs to no real artifact. Tests that need the
# receipt to disagree with the lock file use a second one.
FIXTURE_LOCK_DIGEST = "a" * 64
OTHER_LOCK_DIGEST = "b" * 64


def lock():
    """Return a valid toolchain lock."""
    return {
        "schema": LOCK_SCHEMA,
        "id": "appliance-image-toolchain-debian13-amd64",
        "recorded_at": "2026-09-22T14:02:46Z",
        "architecture": {"deb": "amd64", "uname_machine": "x86_64"},
        "base_image": {
            "reference": "debian:13",
            "registry": "registry-1.docker.io/library/debian",
            "index_digest": "sha256:" + "1" * 64,
            "manifest_digest_amd64": "sha256:" + "2" * 64,
        },
        "distribution": {
            "id": "debian",
            "codename": "trixie",
            "version": "13.7",
            "archive": "https://deb.debian.org/debian",
            "snapshot_basis": "https://snapshot.debian.org/archive/debian/20260922T000000Z/",
        },
        "toolchain": {
            "kiwi": {
                "upstream_version": "11.0.3",
                "package": "python3-kiwi",
                "package_version": "11.0.3-1.1",
                "sha256": "3" * 64,
            },
            "boxed_plugin": {"used": False, "reason": "fixture"},
        },
        "invocation": {
            "container_runtime": "docker",
            "image_tag": "olivares-appliance-image-toolchain:fixture",
            "version_command": ["kiwi-ng", "--version"],
            "required_capabilities": ["SYS_ADMIN", "MKNOD", "AUDIT_WRITE", "AUDIT_CONTROL"],
        },
        "budget": {
            "declared_build_bytes": 15000000000,
            "declared_build_minutes": 90,
            "measured": False,
            "basis": "fixture",
        },
        "probe": {
            "loop_image_bytes": 67108864,
            "accelerator_preference": ["kvm", "tcg"],
            "required_qmp_status": "prelaunch",
            "qemu_timeout_seconds": 30,
            "container_timeout_seconds": 120,
        },
    }


def receipt(lock_digest=FIXTURE_LOCK_DIGEST):
    """Return a receipt that the judge must call eligible against lock()."""
    return {
        "schema": RECEIPT_SCHEMA,
        "attempt": {
            "id": "fixture-attempt-0001",
            "lock_id": "appliance-image-toolchain-debian13-amd64",
            "lock_digest": lock_digest,
            "started_at": "2026-09-22T14:00:00Z",
            "finished_at": "2026-09-22T14:01:40Z",
            "elapsed_ms": 100000,
        },
        "runner": {
            "image_os": "ubuntu24",
            "image_version": "24.04.0",
            "os_pretty_name": "Ubuntu 24.04.3 LTS",
            "kernel": "Linux 6.11.0-1018-azure x86_64",
            "uname_machine": "x86_64",
            "uid": 1001,
            "user": "runner",
            "home": "/home/runner",
            "passwordless_sudo": True,
            "cgroup_fs": "cgroup2fs",
        },
        "capacity": {
            "processors": 4,
            "memory_total_bytes": 16768208896,
            "memory_available_bytes": 15300000000,
            "evidence_avail_bytes": 60000000000,
            "probe_allocation_bytes": 67108864,
            "declared_build_bytes": 15000000000,
            "declared_build_measured": False,
        },
        "mounts": [{"target": "/", "fstype": "ext4", "options": "rw,relatime"}],
        "tools": {
            "docker": {"path": "/usr/bin/docker", "version": "28.0.4"},
            "qemu-system-x86_64": {"path": "/usr/bin/qemu-system-x86_64", "version": "9.2.0"},
            "losetup": {"path": "/sbin/losetup", "version": "2.39.3"},
        },
        "observations": {
            "container_toolchain": {
                "performed": True,
                "proved": True,
                "timed_out": False,
                "cleaned": True,
                "image_tag": "olivares-appliance-image-toolchain:fixture",
                "reported_version": "11.0.3",
            },
            "accelerator": {
                "performed": True,
                "proved": True,
                "timed_out": False,
                "cleaned": True,
                "requested": ["kvm", "tcg"],
                "selected": "kvm",
                "qmp_status": "prelaunch",
                "qmp_running": False,
            },
            "loop_privilege": {
                "performed": True,
                "proved": True,
                "timed_out": False,
                "cleaned": True,
                "image_bytes": 67108864,
                "owner_uid": 1001,
                "loop_device": "/dev/loop7",
                "mounted": True,
            },
        },
        "refusals": [],
    }


def write_pair(directory, lock_obj=None, receipt_obj=None, bind=True):
    """Write a lock and receipt to *directory* and return their two paths.

    When *bind* is true the receipt's attempt.lock_digest is rewritten to the sha256 of
    the bytes actually written, which is what a faithful collector records.
    """
    lock_obj = lock() if lock_obj is None else lock_obj
    receipt_obj = receipt() if receipt_obj is None else receipt_obj
    lock_path = directory / "input-lock.json"
    receipt_path = directory / "receipt.json"
    lock_bytes = (json.dumps(lock_obj, indent=2, sort_keys=True) + "\n").encode("utf-8")
    lock_path.write_bytes(lock_bytes)
    if bind:
        receipt_obj["attempt"]["lock_digest"] = hashlib.sha256(lock_bytes).hexdigest()
    receipt_path.write_bytes(
        (json.dumps(receipt_obj, indent=2, sort_keys=True) + "\n").encode("utf-8")
    )
    return lock_path, receipt_path
