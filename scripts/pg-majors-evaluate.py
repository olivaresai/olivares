# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# pg-majors evaluator (spec an internal design note (not shipped) §2.6): the
# anti-empty-green net for .github/workflows/pg-majors.yml. Reads, from CWD:
#   ci/pg-majors-packages.txt      the package list (source of truth for scope)
#   ci/pg-majors-expectations.json PASS floor per package, all > 0
#   pg-majors-receipts.txt         PG_MAJOR_DSN_VERIFIED|major=N|... lines ×4
#   pg-majors-exits.txt            "<major> <exit-status>" per pass
#   gotest-{15,16,17,18}.json      `go test -json` stream per pass
# Exits 1 (with ::error:: lines) unless: receipts cover exactly {15,16,17,18},
# every pass exited 0, every package meets its floor on every major, and ZERO
# skips appear in matrix packages. A t.Skip IS an executed, reported test — a
# run counter cannot tell it from a PASS, which is the empty green this traps.
# Tested by scripts/test-pg-majors-evaluate.sh (mutation battery); the workflow
# runs that battery in the same job before trusting this file.
import collections
import json
import os
import sys

MAJORS = ("15", "16", "17", "18")


def main() -> int:
    floors = json.load(open("ci/pg-majors-expectations.json"))["floors"]
    exits = dict(
        line.split() for line in open("pg-majors-exits.txt").read().split("\n") if line
    )
    receipts = [
        line
        for line in open("pg-majors-receipts.txt").read().splitlines()
        if line.startswith("PG_MAJOR_DSN_VERIFIED|")
    ]
    errors, summary = [], []

    # The package list and the floor table must be the SAME set, every floor
    # positive: a test-less package in the list would otherwise be a
    # guaranteed-empty green riding along (measured 2026-07-31: core/store and
    # core/engine/enginetest have no test files on main).
    listed = {
        line.strip()
        for line in open("ci/pg-majors-packages.txt")
        if line.strip() and not line.startswith("#")
    }
    if listed != set(floors):
        errors.append(
            "packages.txt vs expectations floors mismatch: only-listed=%s only-floored=%s"
            % (sorted(listed - set(floors)), sorted(set(floors) - listed))
        )
    for pkg, floor in floors.items():
        if floor <= 0:
            errors.append(f"floor for {pkg} is {floor}; every floor must be > 0")

    majors_seen = sorted(l.split("|")[1].split("=")[1] for l in receipts)
    if majors_seen != list(MAJORS):
        errors.append(f"receipts do not cover exactly {{15,16,17,18}}: {majors_seen}")

    for m in MAJORS:
        if exits.get(m) != "0":
            errors.append(f"pass pg{m}: go test exit {exits.get(m, 'MISSING')}")
        passc, skipc = collections.Counter(), collections.Counter()
        skips = []
        try:
            stream = open(f"gotest-{m}.json")
        except FileNotFoundError:
            errors.append(f"pass pg{m}: gotest-{m}.json missing — the pass never ran")
            continue
        for line in stream:
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            pkg = (ev.get("Package") or "?").replace(
                "github.com/olivaresai/olivares/", ""
            )
            if ev.get("Test") and ev.get("Action") == "pass":
                passc[pkg] += 1
            if ev.get("Test") and ev.get("Action") == "skip":
                skipc[pkg] += 1
                skips.append(f"{pkg}#{ev.get('Test')}")
        for pkg, floor in floors.items():
            got = passc.get(pkg, 0)
            if got < floor:
                errors.append(f"pass pg{m}: {pkg} passed {got} < floor {floor}")
            summary.append(
                f"pg{m} {pkg}: {got} pass (floor {floor}), {skipc.get(pkg, 0)} skip"
            )
        matrix_skips = [s for s in skips if any(s.startswith(p + "#") for p in floors)]
        if matrix_skips:
            errors.append(
                f"pass pg{m}: {len(matrix_skips)} SKIP in matrix packages: {matrix_skips[:5]}"
            )

    out_path = os.environ.get("GITHUB_STEP_SUMMARY")
    out = open(out_path, "a") if out_path else sys.stdout
    print("## pg-majors", file=out)
    for line in summary:
        print("- " + line, file=out)
    for e in errors:
        print("- ❌ " + e, file=out)
    if errors:
        print("\n".join("::error::" + e for e in errors))
        return 1
    print(
        f"all four majors measured: {len(summary)} package-passes, floors held, zero skips"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
