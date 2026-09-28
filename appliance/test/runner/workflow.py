#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Finalize bounded evidence even when the collector refuses or is interrupted."""
import hashlib
import json
import os
from pathlib import Path
import signal
import sys

from collector import atomic_json, now
from owned_resources import Result, run


def execute(evidence, argv):
    directory = Path(evidence)
    directory.mkdir(parents=True, exist_ok=True)
    if (directory / "workflow-result.json").exists():
        print("refusing to overwrite a previous workflow attempt", file=sys.stderr)
        return 1
    result = Result(1, "collector did not start", joined=False)
    interrupted = False
    try:
        result = run(argv, timeout=1500, metadata=os.environ)
    except KeyboardInterrupt:
        interrupted = True
        result = Result(143, "collector interrupted; resource release unconfirmed", joined=False)
    finally:
        (directory / "preflight.log").write_text(result.output[-262144:], encoding="utf-8")
        receipt_path = directory / "receipt.json"
        valid_receipt = False
        try:
            state = json.loads(receipt_path.read_bytes())["judgement"]["state"]
            valid_receipt = {"eligible": 0, "defect": 1, "unavailable": 2}.get(state) == result.code
        except (OSError, ValueError, KeyError, TypeError):
            pass
        if not receipt_path.exists():
            atomic_json(receipt_path, {"schema": "olivares-appliance-interrupted-receipt/v1",
                "observed_at": now(), "judgement": {"state": "defect", "reasons": ["collector.receipt_absent"]},
                "cleanup": "unknown; work and daemon resources require inspection on the owning disposable runner"})
        status = result.code if valid_receipt and result.code in (0, 1, 2) and result.joined else 1
        atomic_json(directory / "workflow-result.json", {"collector_exit": result.code, "exit": status,
            "interrupted": interrupted, "timed_out": result.timed_out, "collector_joined": result.joined,
            "receipt_classification_matches": valid_receipt, "finished_at": now()})
        names = ["preflight.log", "receipt.json", "workflow-result.json"]
        sums = "".join(hashlib.sha256((directory / name).read_bytes()).hexdigest() + "  " + name + "\n" for name in sorted(names))
        (directory / "SHA256SUMS").write_text(sums, encoding="utf-8")
        # Work may still back a live loop device. It is never recursively deleted here.
    return status


def main():
    evidence = os.environ["EVIDENCE"]
    here = Path(__file__).resolve().parent
    root = here.parents[2]
    return execute(evidence, [sys.executable, str(here / "collector.py"), "--lock",
                             str(root / "appliance/images/toolchain/input-lock.json"), "--evidence", evidence])


if __name__ == "__main__":
    def interrupted(signum, frame):
        raise KeyboardInterrupt("workflow interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())
