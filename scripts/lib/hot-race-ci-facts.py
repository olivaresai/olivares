#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Emit `key=value` facts about the race-hot-manifest job of a workflow.

The battery asserts on THIS job and THESE steps. A whole-file grep cannot: several jobs and
several comments in mainline-ci.yml carry `if: always()` and `continue-on-error: true`, so a
property removed from one step is still matched elsewhere and the mutation survives.

Exit 1 with a diagnostic when the job, a required step, or the aggregate is absent; an
unparseable workflow is a failure here, never an empty fact set.
"""
import sys

import yaml

JOB = "race-hot-manifest"
AGG = "race-hot"
STEP_ALIASES = {
    "partition-control": "partition-control",
    "race-hot-manifest-build": "race-hot-manifest-build",
    "race-hot-manifest": "race-hot-manifest",
}


def flat(value):
    """One line, so a fact is greppable by key."""
    return " ".join(str(value).split())


def main(path):
    with open(path, encoding="utf-8") as handle:
        doc = yaml.safe_load(handle)
    jobs = doc.get("jobs") or {}
    job = jobs.get(JOB)
    if job is None:
        sys.exit(f"job {JOB!r} is absent from {path}")
    out = []

    env = job.get("env") or {}
    for name in ("OLIVARES_HOT_RACE_PARTITION", "OLIVARES_HOT_RACE_PARTITIONS"):
        out.append(f"job.env.{name}={flat(env.get(name, ''))}")
    strategy = job.get("strategy") or {}
    # PyYAML reads the unquoted key `fail-fast` as a string and `false` as a bool; the
    # battery compares against the Python spelling on purpose, so a value of `"false"` (a
    # string, which GitHub also accepts) is still distinguishable from the bool.
    out.append(f"job.strategy.fail-fast={strategy.get('fail-fast')}")
    matrix = (strategy.get("matrix") or {}).get("partition") or []
    out.append("job.matrix.partition=" + " ".join(str(x) for x in matrix))

    steps = job.get("steps") or []
    ids = [s.get("id") for s in steps]
    for wanted in STEP_ALIASES:
        if wanted not in ids:
            sys.exit(f"step id {wanted!r} is absent from job {JOB!r}")
    for key, step_id in STEP_ALIASES.items():
        step = steps[ids.index(step_id)]
        out.append(f"step.{key}.run={flat(step.get('run', ''))}")
        out.append(f"step.{key}.if={flat(step.get('if', ''))}")
    out.append(
        "order.partition-control-before-prebuild="
        + ("yes" if ids.index("partition-control") < ids.index("race-hot-manifest-build") else "no")
    )

    # The census and the retention step have no id; they are identified by what they do, so
    # renaming one does not silently drop its assertions — an absent one exits nonzero here.
    census = next((s for s in steps if "check-test-timeout-headroom.sh" in str(s.get("run", ""))), None)
    if census is None:
        sys.exit(f"no headroom census step in job {JOB!r}")
    out.append(f"step.census.run={flat(census.get('run', ''))}")
    out.append(f"step.census.if={flat(census.get('if', ''))}")
    out.append(f"step.census.continue-on-error={census.get('continue-on-error')}")

    retain = next((s for s in steps if "upload-artifact" in str(s.get("uses", ""))), None)
    if retain is None:
        sys.exit(f"no artifact retention step in job {JOB!r}")
    out.append(f"step.retain.uses={flat(retain.get('uses', ''))}")
    out.append(f"step.retain.if={flat(retain.get('if', ''))}")
    out.append(f"step.retain.path={flat((retain.get('with') or {}).get('path', ''))}")

    agg = jobs.get(AGG)
    if agg is None:
        sys.exit(f"job {AGG!r} is absent from {path}")
    out.append("aggregate.needs=" + " ".join(agg.get("needs") or []))
    verdict = next((s for s in (agg.get("steps") or []) if "verdict" in str(s.get("run", ""))), None)
    if verdict is None:
        sys.exit(f"no verdict step in job {AGG!r}")
    out.append("aggregate.env=" + " ".join((verdict.get("env") or {}).keys()))
    out.append(f"aggregate.run={flat(verdict.get('run', ''))}")

    print("\n".join(out))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: hot-race-ci-facts.py <workflow.yml>")
    main(sys.argv[1])
