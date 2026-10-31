# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# pg-majors evaluator (spec an internal design note (not shipped) §2.6): the
# anti-empty-green net for .github/workflows/pg-majors.yml. Reads, from CWD:
#   ci/pg-majors-packages.txt      the package list (source of truth for scope)
#   ci/pg-majors-expectations.json PASS floor per package, all > 0
#   pg-majors-receipts.txt         PG_MAJOR_DSN_VERIFIED|major=N|... lines ×4
#   pg-majors-exits.txt            "<major> <exit-status>" per pass
#   gotest-{15,16,17,18}.json      `go test -json` stream per pass; each must
#                                  carry a PG_MAJOR_ASSERTED receipt for its major
# Exits 1 (with ::error:: lines) unless: receipts cover exactly {15,16,17,18},
# every pass exited 0, every package meets its floor on every major, every
# stream carries its own major's PG_MAJOR_ASSERTED receipt, and ZERO skips
# appear in matrix packages. A t.Skip IS an executed, reported test — a
# run counter cannot tell it from a PASS, which is the empty green this traps.
# Tested by scripts/test-pg-majors-evaluate.sh (mutation battery); the workflow
# runs that battery in the same job before trusting this file.
import argparse
import collections
import json
import os
import re
import sys

MAJORS = ("15", "16", "17", "18")


def main() -> int:
    parser = argparse.ArgumentParser(description="Evaluate the declared PostgreSQL qualification scope.")
    parser.add_argument("--skip-pattern", choices=MAJORS, help="Print the native Go selector before running tests.")
    args = parser.parse_args()
    contract = json.load(open("ci/pg-majors-expectations.json"))
    exclusions = contract.get("exclusions", [])
    for exclusion in exclusions:
        if (not re.fullmatch(r"Test[A-Za-z0-9_]+(?:/[A-Za-z0-9_ -]+)*", exclusion["test"])
                or not exclusion["reason"] or not exclusion["majors"]
                or any(str(m) not in MAJORS for m in exclusion["majors"])):
            parser.error("exclusions must name exact tests, supported majors and a reason")
    if args.skip_pattern:
        patterns = [
            "/".join("^" + re.escape(part) + "$" for part in exclusion["test"].split("/"))
            for exclusion in exclusions if int(args.skip_pattern) in exclusion["majors"]
        ]
        print("|".join(patterns) or "^$")
        return 0
    floors = contract["floors"]
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
        skips, failures = [], []
        try:
            stream = open(f"gotest-{m}.json")
        except FileNotFoundError:
            errors.append(f"pass pg{m}: gotest-{m}.json missing — the pass never ran")
            continue
        # A stream the writer never finished still clears every floor on its
        # surviving prefix: a torn last line used to be dropped in silence and
        # a package that never reached a terminal event was never required to.
        # Each leg's stream is the per-major evidence now, so both are errors.
        # The pass must also carry the harness's own PG_MAJOR_ASSERTED receipt
        # (spec §5.4): the leg's DSN is not proof that the tests used it.
        torn, terminal, asserted = 0, set(), []
        for line in stream:
            try:
                ev = json.loads(line)
            except ValueError:
                torn += 1
                continue
            pkg = (ev.get("Package") or "?").replace(
                "github.com/olivaresai/olivares/", ""
            )
            if ev.get("Action") == "output":
                proof = re.search(
                    r"PG_MAJOR_ASSERTED\|expected=(\d+)\|connected=(\d+)",
                    ev.get("Output") or "",
                )
                if proof:
                    asserted.append(proof.groups())
            if not ev.get("Test") and ev.get("Action") in ("pass", "fail", "skip"):
                terminal.add(pkg)
            if ev.get("Test") and ev.get("Action") == "pass":
                passc[pkg] += 1
            if ev.get("Test") and ev.get("Action") == "skip":
                skipc[pkg] += 1
                skips.append(f"{pkg}#{ev.get('Test')}")
            if ev.get("Action") == "fail" and pkg in floors:
                failures.append(f"{pkg}#{ev.get('Test', '(package)')}")
        if torn:
            errors.append(
                f"pass pg{m}: {torn} unparsable line(s) in gotest-{m}.json — the stream is torn"
            )
        if not asserted:
            errors.append(
                f"pass pg{m}: no PG_MAJOR_ASSERTED receipt in gotest-{m}.json — the pass never proved which server it measured"
            )
        for want, got in sorted(set(asserted)):
            if want != m or got != want:
                errors.append(
                    f"pass pg{m}: PG_MAJOR_ASSERTED expected={want} connected={got} in gotest-{m}.json — the stream does not prove pg{m}"
                )
        unfinished = sorted(set(floors) - terminal)
        if unfinished:
            errors.append(
                f"pass pg{m}: no terminal package event for {unfinished[:3]} — the stream is truncated"
            )
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
        if failures:
            errors.append(f"pass pg{m}: {len(failures)} FAIL in matrix packages: {failures[:5]}")
        for exclusion in exclusions:
            if int(m) in exclusion["majors"]:
                summary.append(f"pg{m} outside qualification scope: {exclusion['test']} — {exclusion['reason']}")

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
        f"all four majors measured: {len(MAJORS) * len(floors)} package-passes, floors held, zero failures or skips in the declared scope"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
