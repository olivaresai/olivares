#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""LT1 · the measurement Module behind scripts/lib/overlay-measurement.sh.

WHY THIS EXISTS, AND IT IS A MEASURED PROBLEM, NOT A TIDINESS ONE. An ordinary
Enterprise main advance forced ten versioned metadata replacements across seven
Community records even when every measured product property stayed true: after
ent#182 and again after ent#183, the hub's overlay re-measure tool rewrote
`overlay_main_sha` in seven actas and `prNN_behind_overlay_main` in three,
and every original semantic predicate held before and after. Reproduced here with
an actual same-tree child (`git commit-tree <main^{tree}> -p <main>`, identical
tree object): all seven readers answer 1 while the tree they judge is byte-identical
to the one they passed on. That serialises an independent Enterprise merge with
Community publication and manufactures Community commits.

WHAT CHANGES, AND — MORE IMPORTANTLY — WHAT DOES NOT. The historical records stay
IMMUTABLE. Their recorded identity and distance metadata is validated against the
actual OLD objects that metadata names, in the store that can resolve each field.
A NEW complete observation is then made on the freshly fetched, sealed current
source, and the reader's own semantic predicates run against that captured 40-hex.
A different current SHA is acceptable ONLY with all of: valid historical metadata,
normal descendant history, no registered behind count decreasing, fixed PR identity
intact, and every existing predicate proved on the exact current inputs.

THE FIVE REFUSALS THIS MODULE MAY NEVER TRADE AWAY:
  · a stale, wrong-act or wrong-generation seal is 2, never a current success;
  · a required object that is not in its store is 2 with its dependency named —
    the baseline blob is NEVER a substitute for a current one;
  · a non-descendant current main, or a decreasing behind count, is 1 — a
    force-pushed history that preserves content is not a harmless advance;
  · a static-only observation (no sibling clone) keeps today's exit code and is
    recorded as `observation: static-only` with its unobserved dependencies; no
    caller may read it as a current measurement;
  · a failed result write, or a source/seal identity that moved during the
    invocation, is 2 — never a success for a mixed set.

OUTCOMES. 0 current predicates verified on the named complete observation ·
1 demonstrated contract/schema/property/history finding · 2 could not observe
required inputs or maintain capture/result custody.

INTERFACE. Two subcommands, both crossed by the seven adapters and by the tests:

  open  --check <registered-id> --record <path> --overlay <dir> --community <dir>
        --current-sha <40hex> --act <id> --seal-epoch <n> --seal-digest <sha256>
        --mode current|static-only --state <path>
  close --state <path> --reader-exit <n> --reader-detail <text>
        --observations <dir>
  refuse --check <id> --record <path> --mode <m> --act <id> --code <1|2>
        --predicate <name> --detail <text> --unobserved <a,b> --observations <dir>

A refused invocation is published as a terminal record too: `observation: refused`,
its own 1 or 2, the context that was established and the inputs that were not. It
never carries a fabricated captured identity.

No arbitrary command, no eval, no global mutable override file, no silent
fallback. The check id must be one of the seven REGISTERED below; an unknown id
is 2, not a permissive default.
"""

import argparse
import errno
import hashlib
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import time

SCHEMA = "overlay-measurement/v1"
IMPLEMENTATION_VERSION = 1
HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")

# ── THE REGISTERED SEVEN, AND THE STORE THAT RESOLVES EACH FIELD ──────────────────────
#
# ⛔ ONE REPOSITORY IDENTITY WAS NOT ENOUGH, and the measurement says so: in the overlay
# store `0d72040d`, `01e7bb6b`, `d0a0cf94`, `78986faa`, `a6f5e7f1`, `30346464` and
# `4ff1c350` all resolve and `32d53da9`, `dbc4ef3a`, `899dcf24`, `f4405914` do NOT; in the
# Community store the reverse holds, exactly. So every registered field names its store,
# and a field resolved in the wrong one is a dependency that was never observed.
#
# `behind` and `ahead` are BOTH validated against the baseline. `ahead` was recorded
# distance metadata that no reader validated at all, live or historical, although it is
# exactly as validatable (measured true of the baseline: 1, 8, 2).
REGISTERED = {
    "c02-70-no-land-snapshot": {
        "baseline_main": "overlay_main_sha",
        "public_pin": None,
        "prs": {
            "70": {
                "sha": "pr70_sha",
                "ref": "refs/remotes/origin/hub-comercio/c02-conjur-entitlement-gates",
                "behind": "pr70_behind_overlay_main",
                "ahead": "pr70_ahead_of_overlay_main",
            },
        },
        "community_objects": ["hub_sha"],
    },
    "c02-hold-key-until-producer": {
        "baseline_main": "overlay_main_sha",
        "public_pin": None,
        "prs": {
            "75": {
                "sha": "pr75_sha",
                "ref": "refs/remotes/origin/hub-comercio/c02-producer-by-set",
                "behind": "pr75_behind_overlay_main",
                "ahead": "pr75_ahead_of_overlay_main",
            },
        },
        "community_objects": ["hub_sha", "hub_pr_sha"],
    },
    "c03-01-overlay-main-term": {
        "baseline_main": "overlay_main_sha",
        "public_pin": "overlay_main_public_pin",
        "prs": {},
        "community_objects": ["hub_sha"],
    },
    "c03-41-plugin-census": {
        "baseline_main": "overlay_main_sha",
        "public_pin": None,
        "prs": {},
        "community_objects": ["hub_sha"],
    },
    "c03-76-no-land-snapshot": {
        "baseline_main": "overlay_main_sha",
        "public_pin": None,
        "prs": {
            "76": {
                "sha": "pr76_sha",
                "ref": "refs/remotes/origin/hub-comercio/c03-snapshot-grants",
                "behind": "pr76_behind_overlay_main",
                "ahead": "pr76_ahead_of_overlay_main",
            },
            # PR 83 is a fixed identity the record pins and the reader reads; it carries
            # no recorded distance, so none is validated or derived for it.
            "83": {
                "sha": "pr83_sha",
                "ref": "refs/remotes/origin/hub-comercio/c03-federation-grants",
                "behind": None,
                "ahead": None,
            },
        },
        "community_objects": ["hub_sha"],
    },
    "overlay-live-facts": {
        "baseline_main": "overlay_main_sha",
        "public_pin": "overlay_main_public_pin",
        "prs": {},
        "community_objects": [],
    },
    "ver-01-build-tree": {
        "baseline_main": "overlay_main_sha",
        "public_pin": None,
        "prs": {},
        "community_objects": ["hub"],
        # ⛔ `assembled_is_ancestor_of_overlay_main` was asserted statically and FOREVER
        # (check-ver-01-build-tree.sh:86-87) while the live half compared only the SHA.
        # The ancestry claim about the CURRENT main was measured by nothing. It is one
        # command, and it is now one.
        "assembled": "assembled_tree_sha",
        "assembled_ancestor_claim": "assembled_is_ancestor_of_overlay_main",
    },
}


class Outcome(Exception):
    """A terminal answer with its named predicate and its reason."""

    def __init__(self, code, predicate, detail, dependency=None):
        super().__init__(detail)
        self.code = code
        self.predicate = predicate
        self.detail = detail
        self.dependency = dependency


def look(predicate, detail, dependency=None):
    raise Outcome(2, predicate, detail, dependency)


def finding(predicate, detail):
    raise Outcome(1, predicate, detail)


def git(repo, *args):
    """git with replacement objects disabled, so a 40-hex names its stored content."""
    p = subprocess.run(
        ["git", "--no-replace-objects", "-C", repo, *args],
        capture_output=True,
        text=True,
        errors="replace",
    )
    return p.returncode, p.stdout, p.stderr


def resolve_commit(repo, oid, store, predicate, field):
    rc, _, err = git(repo, "rev-parse", "--verify", "--quiet", "%s^{commit}" % oid)
    if rc != 0:
        look(
            predicate,
            "%s = %s is not a commit in the %s object store: %s"
            % (field, oid, store, (err or "").strip()[:160] or "missing object"),
            dependency="%s:%s@%s" % (store, field, oid),
        )


def count(repo, spec, predicate, what):
    rc, out, err = git(repo, "rev-list", "--count", spec)
    if rc != 0:
        look(predicate, "could not count %s (%s): %s" % (what, spec, (err or "").strip()[:160]))
    try:
        return int(out.strip() or "0")
    except ValueError:
        look(predicate, "the count of %s was not an integer: %r" % (what, out.strip()[:80]))


def is_ancestor(repo, older, newer, predicate, what):
    """git's THREE answers, and the third must not collapse into the second: 0 yes,
    1 no, anything else means the question was not answered."""
    rc, out, err = git(repo, "merge-base", "--is-ancestor", older, newer)
    if rc == 0:
        return True
    if rc == 1:
        return False
    look(predicate, "could not decide %s: git exited %s %s" % (what, rc, (err or out).strip()[:160]))


def gitlink(repo, commit, predicate):
    rc, out, err = git(repo, "ls-tree", commit, "public")
    if rc != 0 or "commit" not in out:
        look(
            predicate,
            "could not read the public gitlink at %s: %s"
            % (commit[:9], (err or out).strip()[:160] or "no gitlink"),
            dependency="overlay:public-gitlink@%s" % commit,
        )
    parts = out.split()
    if len(parts) < 3 or not HEX40.match(parts[2]):
        look(predicate, "the public gitlink at %s is not a 40-hex id: %r" % (commit[:9], out[:80]))
    return parts[2]


def repo_identity(repo, tip, predicate, name):
    """Canonical repository identity: its root commit(s). Immutable, non-secret, and
    not a filesystem path — a path identifies a checkout, never a repository, and this
    record must not carry the private sibling's directory name."""
    rc, out, err = git(repo, "rev-list", "--max-parents=0", tip)
    if rc != 0:
        look(predicate, "could not derive the %s repository identity: %s" % (name, (err or "").strip()[:160]))
    roots = sorted(ln.strip() for ln in out.splitlines() if HEX40.match(ln.strip()))
    if not roots:
        look(predicate, "the %s repository has no root commit reachable from %s" % (name, tip[:9]))
    return roots


def record_digest(path):
    try:
        with open(path, "rb") as fh:
            raw = fh.read()
    except OSError as exc:
        look("historical.record_readable", "historical record %s is unreadable (%s)" % (path, exc))
    try:
        data = json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeDecodeError) as exc:
        # A malformed record is a demonstrated shape finding, not an unavailable input:
        # the bytes were read, and they are not the contract.
        finding("historical.record_readable", "historical record %s is not readable JSON (%s)" % (path, exc))
    if not isinstance(data, dict):
        finding("historical.record_readable", "historical record %s is not a JSON object" % path)
    return data, hashlib.sha256(raw).hexdigest()


def hex40_field(data, field, predicate):
    val = data.get(field)
    if not isinstance(val, str) or not HEX40.match(val):
        finding(predicate, "%s is not a 40-hex object id (got %r)" % (field, val))
    return val


def int_field(data, field, predicate):
    val = data.get(field)
    # `isinstance(True, int)` is True in Python; a boolean is not a count, and saying so
    # here is the same refusal the readers already make in their static halves.
    if not isinstance(val, int) or isinstance(val, bool) or val < 0:
        finding(predicate, "%s is not a non-negative integer (got %r)" % (field, val))
    return val


def seal_digest_of(path):
    try:
        with open(path, "rb") as fh:
            return hashlib.sha256(fh.read()).hexdigest()
    except OSError:
        return None


# ── open ──────────────────────────────────────────────────────────────────────────────


def do_open(args):
    spec = REGISTERED.get(args.check)
    if spec is None:
        look(
            "interface.registered_check",
            "%r is not one of the seven registered checks (%s)"
            % (args.check, ", ".join(sorted(REGISTERED))),
        )
    if args.mode not in ("current", "static-only"):
        look("interface.mode", "unknown observation mode %r" % args.mode)

    predicates = []

    def ok(name, detail):
        predicates.append({"name": name, "ok": True, "detail": detail})

    data, digest = record_digest(args.record)
    ok("historical.record_readable", "sha256 %s" % digest)

    baseline_main = hex40_field(data, spec["baseline_main"], "historical.baseline_main_shape")
    ok("historical.baseline_main_shape", "%s = %s" % (spec["baseline_main"], baseline_main))

    state = {
        "schema": SCHEMA,
        "check_implementation_version": IMPLEMENTATION_VERSION,
        "check_id": args.check,
        "observation": args.mode,
        "act_id": args.act,
        "seal": {"epoch": args.seal_epoch, "digest": args.seal_digest, "sha": args.current_sha},
        "seal_path": args.seal_path,
        "observed_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "record_path": args.record,
        "baseline": {
            "record_digest": digest,
            "main": baseline_main,
            "public_pin": None,
            "pr_identities": {},
            "behind": {},
            "ahead": {},
        },
        "current": {"main": None, "tree": None, "public_pin": None, "behind": {}, "ahead": {}},
        "repositories": {"overlay": None, "community": None},
        "unobserved": [],
        "predicates": predicates,
    }

    if args.mode == "static-only":
        # ⛔ BASELINE VALIDATION BELONGS TO THE CURRENT PATH, because it needs the sibling
        # clone. Six of the seven readers exit 0 with no clone named, and Community-only CI
        # runs `lint:addon-sets` in exactly that state. Requiring the validation here would
        # turn that CI red on every run; pretending the schema check IS the validation would
        # make a static-only 0 indistinguishable from a current success. So: today's checks,
        # today's exit code, and the record says what was NOT read.
        deps = ["overlay:%s@%s" % (spec["baseline_main"], baseline_main)]
        for num, pr in sorted(spec["prs"].items()):
            deps.append("overlay:%s" % pr["sha"])
        if spec["public_pin"]:
            deps.append("community:%s" % spec["public_pin"])
        for field in spec["community_objects"]:
            deps.append("community:%s" % field)
        if spec.get("assembled"):
            deps.append("overlay:%s" % spec["assembled"])
        deps.append("overlay:current-main")
        state["unobserved"] = deps
        ok(
            "observation.static_only",
            "no sibling overlay clone was named: schema and shape only; %d dependencies unobserved"
            % len(deps),
        )
        write_state(args.state, state)
        return 0, state

    overlay, community = args.overlay, args.community
    if not overlay or not os.path.isdir(overlay):
        # The DIRECTORY NAME of the private sibling never enters a durable record: a
        # predicate detail is stored, and `lint:export` already rejected that name once
        # when it sat in a source comment. What is wrong is said without naming it.
        look("capture.overlay_store", "the overlay store the caller named is not a directory")
    if not community or not os.path.isdir(community):
        look("capture.community_store", "the Community store the caller named is not a directory")

    current = args.current_sha
    if not HEX40.match(current or ""):
        look("capture.current_sha", "the captured current main is not a 40-hex id: %r" % current)

    # ── capture: the ref must STILL resolve to the SHA the caller sealed ──────────────
    rc, out, err = git(overlay, "rev-parse", "refs/remotes/origin/main")
    if rc != 0:
        look("capture.ref_resolves", "cannot resolve the overlay origin/main: %s" % (err or out).strip()[:160])
    if out.strip() != current:
        look(
            "capture.ref_stable_at_entry",
            "the overlay ref resolves %s and the seal captured %s: the source moved before this "
            "invocation began, so nothing here is one observation" % (out.strip()[:9], current[:9]),
        )
    ok("capture.ref_stable_at_entry", "origin/main == captured %s" % current)

    # ⛔ THE OVERLAY'S IDENTITY IS DERIVED FROM THE CAPTURED 40-HEX, NEVER FROM A MOVING NAME.
    # The Community half is derived further down, from the first Community object this record
    # BINDS and this store resolved — `HEAD` was the first thing written here and it is wrong
    # twice over: it is exactly the kind of moving name LT1 exists to stop reading, and it
    # certifies nothing about the store that answered for the bound objects. It also made the
    # Module refuse in any caller whose OLIVARES_ROOT is a staged tree rather than a checkout,
    # which is how the existing batteries found it.
    state["repositories"]["overlay"] = repo_identity(
        overlay, current, "capture.overlay_identity", "overlay"
    )
    ok("capture.overlay_identity", "overlay root %s" % state["repositories"]["overlay"][0][:9])

    # ── 1 · HISTORICAL VALIDATION, against the OLD objects the metadata names ─────────
    resolve_commit(overlay, baseline_main, "overlay", "historical.baseline_main_resolves", spec["baseline_main"])
    ok("historical.baseline_main_resolves", "%s is a commit in the overlay store" % baseline_main[:9])

    for num, pr in sorted(spec["prs"].items()):
        pr_sha = hex40_field(data, pr["sha"], "historical.pr_identity_shape")
        resolve_commit(overlay, pr_sha, "overlay", "historical.pr_identity_resolves", pr["sha"])
        state["baseline"]["pr_identities"][num] = {"sha": pr_sha, "ref": pr["ref"]}
        if pr["behind"]:
            recorded = int_field(data, pr["behind"], "historical.behind_shape")
            measured = count(
                overlay,
                "%s..%s" % (pr_sha, baseline_main),
                "historical.behind_true_of_baseline",
                "PR %s behind the baseline" % num,
            )
            if measured != recorded:
                finding(
                    "historical.behind_true_of_baseline",
                    "%s records %d and PR %s is %d behind its OWN baseline %s: the historical "
                    "record is not true of the commit it names"
                    % (pr["behind"], recorded, num, measured, baseline_main[:9]),
                )
            state["baseline"]["behind"][num] = recorded
        if pr["ahead"]:
            recorded = data.get(pr["ahead"])
            if not isinstance(recorded, int) or isinstance(recorded, bool) or recorded < 0:
                finding("historical.ahead_shape", "%s is not a non-negative integer (got %r)" % (pr["ahead"], recorded))
            measured = count(
                overlay,
                "%s..%s" % (baseline_main, pr_sha),
                "historical.ahead_true_of_baseline",
                "PR %s ahead of the baseline" % num,
            )
            if measured != recorded:
                finding(
                    "historical.ahead_true_of_baseline",
                    "%s records %d and PR %s is %d ahead of its OWN baseline %s"
                    % (pr["ahead"], recorded, num, measured, baseline_main[:9]),
                )
            state["baseline"]["ahead"][num] = recorded
    ok(
        "historical.distances_true_of_baseline",
        "behind %s · ahead %s at baseline %s"
        % (state["baseline"]["behind"], state["baseline"]["ahead"], baseline_main[:9]),
    )

    # The Community identity, bound to an immutable object this record names and this store
    # resolved. Every registered check has at least one: a public pin, a `hub_sha`/`hub_pr_sha`
    # or `hub`.
    community_anchor = None

    if spec["public_pin"]:
        recorded_pin = hex40_field(data, spec["public_pin"], "historical.public_pin_shape")
        baseline_pin = gitlink(overlay, baseline_main, "historical.public_pin_true_of_baseline")
        if baseline_pin != recorded_pin:
            finding(
                "historical.public_pin_true_of_baseline",
                "%s records %s and the baseline %s declares Community %s"
                % (spec["public_pin"], recorded_pin[:9], baseline_main[:9], baseline_pin[:9]),
            )
        resolve_commit(
            community, recorded_pin, "Community", "historical.public_pin_resolves", spec["public_pin"]
        )
        state["baseline"]["public_pin"] = recorded_pin
        community_anchor = community_anchor or recorded_pin
        ok("historical.public_pin_true_of_baseline", "baseline gitlink %s" % recorded_pin[:9])

    for field in spec["community_objects"]:
        val = hex40_field(data, field, "historical.community_object_shape")
        resolve_commit(community, val, "Community", "historical.community_objects_resolve", field)
        community_anchor = community_anchor or val
    if spec["community_objects"]:
        ok(
            "historical.community_objects_resolve",
            "%s resolve in the Community store" % ", ".join(spec["community_objects"]),
        )

    if community_anchor is None:
        look(
            "capture.community_identity",
            "this record binds no object the Community store can be identified by; a current "
            "measurement cannot name the second repository it read",
        )
    state["repositories"]["community"] = repo_identity(
        community, community_anchor, "capture.community_identity", "Community"
    )
    ok(
        "capture.community_identity",
        "Community root %s, derived from the bound object %s"
        % (state["repositories"]["community"][0][:9], community_anchor[:9]),
    )

    assembled = None
    if spec.get("assembled"):
        assembled = hex40_field(data, spec["assembled"], "historical.assembled_shape")
        resolve_commit(overlay, assembled, "overlay", "historical.assembled_resolves", spec["assembled"])
        ok("historical.assembled_resolves", "%s is a commit in the overlay store" % assembled[:9])

    # ── 2 · CURRENT DERIVATION, from the captured immutable SHA ───────────────────────
    resolve_commit(overlay, current, "overlay", "current.main_resolves", "captured current main")
    rc, out, err = git(overlay, "rev-parse", "--verify", "--quiet", "%s^{tree}" % current)
    if rc != 0 or not HEX40.match(out.strip()):
        look("current.tree_resolves", "the captured current main has no readable tree: %s" % (err or "").strip()[:160])
    state["current"]["main"] = current
    state["current"]["tree"] = out.strip()
    ok("current.main_resolves", "%s tree %s" % (current[:9], out.strip()[:9]))

    for num, pr in sorted(spec["prs"].items()):
        pr_sha = state["baseline"]["pr_identities"][num]["sha"]
        # FIXED PR identity keeps its CURRENT behaviour: the ref must still be that commit.
        rc, out, err = git(overlay, "rev-parse", "--verify", "--quiet", pr["ref"])
        if rc != 0:
            look(
                "current.pr_ref_resolves",
                "could not resolve the overlay PR %s ref: %s" % (num, (err or "").strip()[:160]),
                dependency="overlay:%s" % pr["ref"],
            )
        if out.strip() != pr_sha:
            finding(
                "current.pr_identity_unchanged",
                "the overlay PR %s ref is %s and the record pins %s: a fixed identity moved"
                % (num, out.strip()[:9], pr_sha[:9]),
            )
        if pr["behind"]:
            now = count(
                overlay,
                "%s..%s" % (pr_sha, current),
                "current.behind_measured",
                "PR %s behind the current main" % num,
            )
            state["current"]["behind"][num] = now
        if pr["ahead"]:
            state["current"]["ahead"][num] = count(
                overlay,
                "%s..%s" % (current, pr_sha),
                "current.ahead_measured",
                "PR %s ahead of the current main" % num,
            )
    ok("current.pr_identity_unchanged", "fixed PR identities still resolve to their recorded commits")

    if spec["public_pin"]:
        current_pin = gitlink(overlay, current, "current.public_pin_readable")
        # ⛔ A CURRENT GITLINK WHOSE COMMUNITY OBJECT IS MISSING IS 2 WITH ITS DEPENDENCY
        # NAMED. Nothing in the seal, the fetch leg or these inputs makes a NEWLY pinned
        # Community commit present locally — the seal covers only the overlay clone's
        # origin/main. The baseline pin's blob is never a substitute: that is precisely
        # how a static rewrite would buy a current success.
        resolve_commit(
            community, current_pin, "Community", "current.public_pin_resolves", "current public gitlink"
        )
        state["current"]["public_pin"] = current_pin
        ok(
            "current.public_pin_resolves",
            "current gitlink %s is present in the Community store" % current_pin[:9],
        )

    # ── 3 · THE ACCEPTANCE RULE, bound rather than prose ──────────────────────────────
    #
    # Falsified before it was written, through the real reader: an ORPHAN main with an
    # identical tree and a re-measured behind count exited 0 as a "harmless advancement",
    # and so did a behind count that DECREASED on a branch the record marks do_not_restack.
    # A rewritten history that preserves content is not an advance, and a decreasing count
    # is a history-rewrite signal.
    if not is_ancestor(overlay, baseline_main, current, "current.baseline_is_ancestor", "descendancy"):
        finding(
            "current.baseline_is_ancestor",
            "the recorded baseline %s is NOT an ancestor of the captured current main %s: "
            "identical content over a rewritten history is not a normal advance"
            % (baseline_main[:9], current[:9]),
        )
    ok("current.baseline_is_ancestor", "%s..%s is normal descendant history" % (baseline_main[:9], current[:9]))

    # ⚠ AND THIS ONE IS IMPLIED BY THE RULE ABOVE, WHICH IS SAID BECAUSE A MUTATION CONTROL
    # SAID IT FIRST. If the baseline is an ancestor of the captured main, then the commits in
    # `pr..current` are a superset of those in `pr..baseline`, so a validated recorded count
    # can never exceed the current one: this guard CANNOT fire while descendancy holds, and
    # its mutant survives `scripts/test-overlay-measurement.sh`. It is kept as a second belt
    # for the day descendancy is relaxed, and the battery states the survival rather than
    # dressing it in a case that would pass for another reason. A DECREASED count reaches
    # `historical.behind_true_of_baseline` instead — that one is observable, and its mutant dies.
    for num, recorded in sorted(state["baseline"]["behind"].items()):
        now = state["current"]["behind"][num]
        if now < recorded:
            finding(
                "current.behind_nondecreasing",
                "PR %s was %d behind the recorded baseline and is %d behind the current main: "
                "a decreasing distance on a do-not-restack branch is a history rewrite, not an advance"
                % (num, recorded, now),
            )
    ok(
        "current.behind_nondecreasing",
        "no registered behind count decreased (%s -> %s)"
        % (state["baseline"]["behind"], state["current"]["behind"]),
    )

    if assembled is not None:
        claim = data.get(spec["assembled_ancestor_claim"])
        if not isinstance(claim, bool):
            finding(
                "current.assembled_ancestor_claim",
                "%s is not a boolean (got %r)" % (spec["assembled_ancestor_claim"], claim),
            )
        measured = is_ancestor(
            overlay, assembled, current, "current.assembled_ancestor_claim", "the assembled-tree ancestry"
        )
        if measured != claim:
            finding(
                "current.assembled_ancestor_claim",
                "%s records %s and %s is %san ancestor of the captured current main %s"
                % (
                    spec["assembled_ancestor_claim"],
                    claim,
                    assembled[:9],
                    "" if measured else "NOT ",
                    current[:9],
                ),
            )
        ok(
            "current.assembled_ancestor_claim",
            "%s ancestor-of %s == %s, as the record claims" % (assembled[:9], current[:9], claim),
        )

    write_state(args.state, state)
    return 0, state


def write_state(path, state):
    tmp = path + ".tmp"
    with io.open(tmp, "w", encoding="utf-8") as fh:
        fh.write(json.dumps(state, indent=2, sort_keys=True, ensure_ascii=False) + "\n")
    os.replace(tmp, path)


# ── publication of a terminal record ──────────────────────────────────────────────────
#
# ⛔ THE NAME IS DERIVED FROM THE INVOCATION, NOT FROM THE PROCESS. A pid would make every
# name unique and the O_EXCL refusal below unreachable — a guard that cannot fire is
# decoration. Derived from (kind, check, act, seal generation, record path and digest,
# captured main, observed second), two DIFFERENT invocations get two names and a DUPLICATE
# invocation collides, which is what "no overwrite of a terminal result" is about.
# `remeasure-overlay-actas.sh` runs a check twice in one act — once on the real record, once
# on a candidate file — and those differ in record path and digest, so they do not collide.
#
# A REFUSAL is published through this same writer and carries `refused` in its name, so failed
# publication evidence stays distinguishable from an accepted current record on sight.
def publish_terminal(state, observations):
    """Write the terminal record atomically. Returns (path, None) or (None, reason)."""
    kind = "refused" if state.get("observation") == "refused" else "obs"
    try:
        os.makedirs(observations, exist_ok=True)
        ident = hashlib.sha256(
            "\0".join(
                [
                    kind,
                    str(state.get("check_id")),
                    str(state.get("act_id")),
                    str((state.get("seal") or {}).get("epoch")),
                    str((state.get("seal") or {}).get("digest")),
                    str(state.get("record_path")),
                    str((state.get("baseline") or {}).get("record_digest")),
                    str((state.get("current") or {}).get("main")),
                    str(state.get("refused_predicate")),
                    str(state.get("observed_utc")),
                ]
            ).encode("utf-8")
        ).hexdigest()[:16]
        name = "%s.%s.%s.%s%s.json" % (
            state.get("check_id") or "unknown-check",
            (state.get("act_id") or "noact")[:24],
            (state.get("seal") or {}).get("epoch") or "0",
            "refused-" if kind == "refused" else "",
            ident,
        )
        final = os.path.join(observations, name)
        fd, tmp = tempfile.mkstemp(prefix=".obs.", suffix=".json", dir=observations)
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(json.dumps(state, indent=2, sort_keys=True, ensure_ascii=False) + "\n")
        try:
            os.link(tmp, final)
        except OSError as exc:
            os.unlink(tmp)
            if exc.errno == errno.EEXIST:
                return None, ("a terminal result already exists at %s: this invocation will "
                              "not overwrite it" % name)
            raise
        os.unlink(tmp)
        return final, None
    except OSError as exc:
        return None, "the terminal result could not be written (%s)" % exc


# ⛔ A REFUSED INVOCATION IS STILL AN OBSERVATION, and until this existed it was the one
# outcome LT1 did not keep: a failed `open` wrote only per-invocation scratch, the adapter
# exited, and the act's retained evidence held nothing at all — so a "could not look" became
# indistinguishable from a check that never ran. SDD06 §1.1 asks for the failed attempt to be
# preserved. The record carries the context that WAS established and NAMES what was not; it
# never fabricates a captured identity, so `current`, `repositories` and the derived distances
# stay absent rather than being filled with plausible values. Its exit is 1 or 2, never 0.
def refusal_state(check, record_path, mode, act, seal, code, predicate, detail, unobserved):
    return {
        "schema": SCHEMA,
        "check_implementation_version": IMPLEMENTATION_VERSION,
        "check_id": check or None,
        "observation": "refused",
        "requested_observation": mode or None,
        "refused_predicate": predicate,
        "refused_detail": detail,
        "act_id": act or None,
        "seal": seal,
        "observed_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "completed_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "record_path": record_path or None,
        "baseline": {},
        "current": {},
        "repositories": {},
        "unobserved": unobserved,
        "predicates": [{"name": predicate, "ok": False, "detail": detail}],
        "exit": code,
    }


# ── close ─────────────────────────────────────────────────────────────────────────────


def do_close(args):
    try:
        with io.open(args.state, encoding="utf-8") as fh:
            state = json.load(fh)
    except (OSError, ValueError) as exc:
        look("custody.state_readable", "the invocation state could not be read (%s)" % exc)
    if state.get("schema") != SCHEMA:
        look("custody.state_readable", "the invocation state is not %s" % SCHEMA)

    predicates = state["predicates"]

    def ok(name, detail):
        predicates.append({"name": name, "ok": True, "detail": detail})

    def bad(name, code, detail):
        predicates.append({"name": name, "ok": False, "detail": detail})
        state["exit"] = max(state.get("exit", 0), code)
        return code

    state["reader"] = {"exit": args.reader_exit, "detail": args.reader_detail}
    predicates.append(
        {
            "name": "reader.semantic_predicates",
            "ok": args.reader_exit == 0,
            "detail": args.reader_detail or ("exit %s" % args.reader_exit),
        }
    )
    state["exit"] = args.reader_exit

    worst = 0
    if state.get("observation") == "current":
        # ⛔ THE INVARIANCE IS SCOPED TO ONE INVOCATION, NOT TO THE ACT. The hook
        # deliberately fetches TWICE under the same act id (the preceding lints can exceed
        # the reader's 900s limit), so the second seal legitimately carries a different SHA
        # under the same act: act id alone is NOT a generation identity. What must not move
        # is the source this invocation read, so entry and completion are compared on the
        # SEAL GENERATION (epoch + content digest) and on the ref itself.
        rc, out, err = git(args.overlay, "rev-parse", "refs/remotes/origin/main")
        if rc != 0:
            worst = max(worst, bad(
                "custody.source_stable_at_completion", 2,
                "the overlay ref could not be re-read at completion: %s" % (err or out).strip()[:160],
            ))
        elif out.strip() != state["current"]["main"]:
            worst = max(worst, bad(
                "custody.source_stable_at_completion", 2,
                "the overlay ref moved from %s to %s during this invocation: this is not one "
                "observation, and a mixed set is not a success"
                % (state["current"]["main"][:9], out.strip()[:9]),
            ))
        else:
            ok("custody.source_stable_at_completion", "origin/main still %s" % state["current"]["main"][:9])

        now_digest = seal_digest_of(state.get("seal_path") or "")
        if now_digest is None:
            worst = max(worst, bad(
                "custody.seal_stable_at_completion", 2,
                "the freshness seal could not be re-read at completion",
            ))
        elif now_digest != state["seal"]["digest"]:
            worst = max(worst, bad(
                "custody.seal_stable_at_completion", 2,
                "the freshness seal generation changed during this invocation (digest %s -> %s)"
                % (state["seal"]["digest"][:12], now_digest[:12]),
            ))
        else:
            ok("custody.seal_stable_at_completion", "seal generation %s unchanged" % state["seal"]["digest"][:12])

    # ── result custody ───────────────────────────────────────────────────────────────
    #
    # Atomic per-invocation name under the CALLER's observation directory, created with
    # O_EXCL through a link: a terminal result is never overwritten, and a result from
    # another act or another repository cannot be mistaken for this one because the act,
    # the seal generation and both repository identities are inside it.
    written, why = publish_terminal(state, args.observations)
    if written:
        ok("custody.result_written", os.path.basename(written))
    else:
        worst = max(worst, bad("custody.result_written", 2, why))

    state["exit"] = max(state.get("exit", 0), worst)
    state["result_path"] = written
    state["completed_utc"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    # The complete structured result goes to the invocation's ordinary captured output.
    # If the file write failed, this is the only surviving copy — and the exit is 2, so
    # nothing reads it as a current success.
    if written:
        write_state(written + ".tmp2", state)
        os.replace(written + ".tmp2", written)
    # The caller must be able to say WHICH fact drove a 2 — the reader's own outcome or the
    # loss of custody — because both have to survive in the message. One line, then the JSON.
    sys.stdout.write("OLIVARES_OVERLAY_CUSTODY=%s\n" % ("failed" if worst else "ok"))
    sys.stdout.write(json.dumps(state, sort_keys=True, ensure_ascii=False) + "\n")
    return state["exit"], state


def do_refuse(args):
    """Publish a terminal refusal for an invocation that never reached a measurement."""
    if args.code not in (1, 2):
        args.code = 2
    seal = {"epoch": args.seal_epoch or None, "digest": args.seal_digest or None,
            "sha": args.current_sha or None}
    unobserved = [u for u in (args.unobserved or "").split(",") if u]
    state = refusal_state(args.check, args.record, args.mode, args.act, seal,
                          args.code, args.predicate, args.detail, unobserved)
    written, why = publish_terminal(state, args.observations)
    state["result_path"] = written
    if not written:
        state["predicates"].append(
            {"name": "custody.result_written", "ok": False, "detail": why})
        state["refusal_exit"] = args.code
        state["exit"] = 2
        sys.stderr.write("overlay-measure: the refusal record could not be retained — %s\n" % why)
    sys.stdout.write(json.dumps(state, sort_keys=True, ensure_ascii=False) + "\n")
    # Failed retention outranks a semantic refusal; both facts remain in the output.
    return state["exit"], state


def main(argv):
    ap = argparse.ArgumentParser(add_help=True)
    sub = ap.add_subparsers(dest="cmd", required=True)

    o = sub.add_parser("open")
    o.add_argument("--check", required=True)
    o.add_argument("--record", required=True)
    o.add_argument("--overlay", default="")
    o.add_argument("--community", default="")
    o.add_argument("--current-sha", default="")
    o.add_argument("--act", default="")
    o.add_argument("--seal-epoch", default="")
    o.add_argument("--seal-digest", default="")
    o.add_argument("--seal-path", default="")
    o.add_argument("--mode", required=True)
    o.add_argument("--state", required=True)
    o.add_argument("--observations", default="")

    r = sub.add_parser("refuse")
    r.add_argument("--check", default="")
    r.add_argument("--record", default="")
    r.add_argument("--mode", default="")
    r.add_argument("--act", default="")
    r.add_argument("--seal-epoch", default="")
    r.add_argument("--seal-digest", default="")
    r.add_argument("--current-sha", default="")
    r.add_argument("--code", type=int, required=True)
    r.add_argument("--predicate", required=True)
    r.add_argument("--detail", default="")
    r.add_argument("--unobserved", default="")
    r.add_argument("--observations", required=True)

    c = sub.add_parser("close")
    c.add_argument("--state", required=True)
    c.add_argument("--overlay", default="")
    c.add_argument("--reader-exit", type=int, required=True)
    c.add_argument("--reader-detail", default="")
    c.add_argument("--observations", required=True)

    args = ap.parse_args(argv)
    try:
        if args.cmd == "open":
            code, state = do_open(args)
            # ── THE PROTOCOL, AND IT IS A GRAMMAR, NOT AN `eval` ──────────────────
            # Exactly these keys, exactly one line each, values that are 40-hex or
            # empty. The shell half validates each one again before it assigns, so a
            # malformed implementation cannot inject a value into the caller's shell.
            sys.stdout.write(
                "OLIVARES_OVERLAY_CURRENT_SHA=%s\n" % (state["current"].get("main") or "")
            )
            sys.stdout.write(
                "OLIVARES_OVERLAY_CURRENT_PIN=%s\n" % (state["current"].get("public_pin") or "")
            )
            return code
        if args.cmd == "refuse":
            code, _ = do_refuse(args)
            return code
        code, _ = do_close(args)
        return code
    except Outcome as out:
        final_code = out.code
        kind = "COULD NOT LOOK" if out.code == 2 else "FINDING"
        sys.stderr.write("overlay-measure: %s — %s: %s\n" % (kind, out.predicate, out.detail))
        if out.dependency:
            sys.stderr.write("overlay-measure: unobserved dependency: %s\n" % out.dependency)
        if args.cmd == "open":
            one_line = " ".join(("%s: %s" % (out.predicate, out.detail)).split())
            # The refusal is itself an observation and is RETAINED, not just noted in the
            # per-invocation scratch the adapter is about to discard.
            if args.observations:
                seal = {"epoch": args.seal_epoch or None, "digest": args.seal_digest or None,
                        "sha": args.current_sha or None}
                deps = [out.dependency] if out.dependency else []
                rec = refusal_state(args.check, args.record, args.mode, args.act, seal,
                                    out.code, out.predicate, out.detail, deps)
                _w, _why = publish_terminal(rec, args.observations)
                if not _w:
                    final_code = 2
                    one_line += "; refusal result custody failed: " + " ".join(_why.split())
                    sys.stderr.write(
                        "overlay-measure: the refusal record could not be retained — %s\n" % _why)
            sys.stdout.write("OLIVARES_OVERLAY_WHY=%s\n" % one_line)
            # Scratch, for the shell half to read the reason from. The DURABLE copy is the
            # terminal record published just above; this comment used to claim the durability
            # on its own, and the state file it writes is discarded with the invocation.
            try:
                write_state(
                    args.state,
                    {
                        "schema": SCHEMA,
                        "check_implementation_version": IMPLEMENTATION_VERSION,
                        "check_id": args.check,
                        "observation": args.mode,
                        "act_id": args.act,
                        "seal": {
                            "epoch": args.seal_epoch,
                            "digest": args.seal_digest,
                            "sha": args.current_sha,
                        },
                        "seal_path": args.seal_path,
                        "observed_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                        "record_path": args.record,
                        "baseline": {},
                        "current": {"main": args.current_sha or None},
                        "repositories": {},
                        "unobserved": [out.dependency] if out.dependency else [],
                        "predicates": [
                            {"name": out.predicate, "ok": False, "detail": out.detail}
                        ],
                        "open_exit": final_code,
                    },
                )
            except OSError:
                pass
        return final_code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
