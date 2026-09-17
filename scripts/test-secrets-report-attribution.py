#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""test-secrets-report-attribution.py — the unit battery for the attribution helper.

It builds throwaway repositories under a temporary directory and asks ONE question in three
ways: does the helper give the answer `scripts/check-secrets.sh` used to compute per finding,
for the same repository, including WHICH refs it names and IN WHICH ORDER?

  · GROUND TRUTH (group 1) — the three properties of git the equivalence rests on, asserted
    against the real commands rather than assumed. If a future git changes any of them this
    battery goes red before the gate starts lying.
  · ORACLE (groups 2-6) — every finding is classified twice: once by the helper, once by the
    exact shell the helper replaced (`cat-file -e`, then `merge-base --is-ancestor`, then
    `for-each-ref --contains ... | head -3 | tr '\n' ' '`). Any disagreement is a failure,
    and the report says which finding and both answers.
  · COST (group 7) — git invocations are counted with GIT_TRACE. The claim under test is not
    "it is faster": it is that the number of git processes does NOT grow with the number of
    findings, and that no `--contains`, `name-rev` or `merge-base` is issued per finding.

Exit 0 = every check passed · 1 = a check failed (named) · 2 = could not run the battery.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
HELPER = os.path.join(HERE, "secrets-report-attribution.py")

FAILURES = []
CHECKS = 0


def check(name, ok, detail=""):
    global CHECKS
    CHECKS += 1
    if ok:
        sys.stdout.write("  ok   %s\n" % name)
    else:
        sys.stdout.write("  FAIL %s\n" % name)
        if detail:
            sys.stdout.write("       %s\n" % detail)
        FAILURES.append(name)


def git(repo, args, stdin=None, env=None):
    return subprocess.run(
        ["git"] + args,
        cwd=repo,
        input=stdin,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
    )


def git_ok(repo, args, stdin=None):
    proc = git(repo, args, stdin=stdin)
    if proc.returncode != 0:
        raise RuntimeError(
            "git %s failed in %s: %s" % (" ".join(args), repo, proc.stderr.strip())
        )
    return proc.stdout.strip()


def run_helper(repo, commits, env=None, trace=None, raw=False):
    """The helper under test. Returns (rc, stdout lines, stderr).

    `raw=True` sends the given strings through untouched, which is how the escape-decoding
    contract is exercised; otherwise the values are plain object names, for which the gate's
    escape is a no-op and the two forms coincide.
    """
    child = dict(os.environ)
    # The ambient git environment outranks cwd: with GIT_DIR/GIT_WORK_TREE exported — which
    # git does from every LINKED worktree, i.e. from every parallel session — the throwaway
    # repositories would be driven into the live repository instead. Same fail-closed
    # reasoning as scripts/lib/git-env.sh in the shell battery.
    for var in (
        "GIT_DIR",
        "GIT_WORK_TREE",
        "GIT_INDEX_FILE",
        "GIT_OBJECT_DIRECTORY",
        "GIT_ALTERNATE_OBJECT_DIRECTORIES",
        "GIT_COMMON_DIR",
        "GIT_CEILING_DIRECTORIES",
    ):
        child.pop(var, None)
    if trace:
        child["GIT_TRACE"] = trace
    else:
        child.pop("GIT_TRACE", None)
    if env:
        child.update(env)
    proc = subprocess.run(
        [sys.executable, HELPER],
        cwd=repo,
        input="".join("%s\n" % c for c in commits),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=child,
    )
    lines = proc.stdout.split("\n")
    if lines and lines[-1] == "":
        lines.pop()
    return proc.returncode, lines, proc.stderr


def oracle(repo, commit):
    """The method being replaced, transcribed from check-secrets.sh:293-316.

    Order of questions is load-bearing and preserved: no commit -> working tree; the object
    is not here -> absent; HEAD reaches it -> yours; otherwise the first three carriers, in
    the order `for-each-ref --contains` prints them, joined the way `head -3 | tr '\n' ' '`
    joined them (trailing space included).
    """
    if commit == "":
        return ("worktree", "")
    if git(repo, ["cat-file", "-e", "%s^{commit}" % commit]).returncode != 0:
        return ("absent", "")
    if git(repo, ["merge-base", "--is-ancestor", commit, "HEAD"]).returncode == 0:
        return ("head", "")
    proc = git(repo, ["for-each-ref", "--contains", commit, "--format=%(refname)"])
    refs = [r for r in proc.stdout.split("\n") if r][:3]
    return ("other", "".join("%s " % r for r in refs))


def parse(lines):
    """The helper answers TWO lines per finding: the state, then the carrier list.

    Two lines rather than one tab-joined line because a Git refname and a Git path may carry
    bytes that any chosen separator would collide with — the defect root returned on
    2026-09-09, where a path containing 0x1f shifted the columns and produced a clean verdict.
    An odd line count is a framing failure, and this helper says so instead of pairing blindly.
    """
    if len(lines) % 2:
        raise RuntimeError("the helper emitted %d line(s): not two per finding" % len(lines))
    return [(lines[i], lines[i + 1]) for i in range(0, len(lines), 2)]


# ── fixture construction ──────────────────────────────────────────────────────────────
def new_repo(work, name):
    repo = os.path.join(work, name)
    os.makedirs(repo)
    git_ok(repo, ["init", "-q", "-b", "main"])
    git_ok(repo, ["config", "user.email", "battery@example.invalid"])
    git_ok(repo, ["config", "user.name", "battery"])
    git_ok(repo, ["config", "commit.gpgsign", "false"])
    return repo


def commit(repo, path, text, message, when=None):
    full = os.path.join(repo, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8") as fh:
        fh.write(text)
    git_ok(repo, ["add", path])
    args = ["commit", "-qm", message]
    if when:
        args = ["-c", "user.name=battery"] + args
    env = None
    if when:
        env = dict(os.environ)
        env["GIT_AUTHOR_DATE"] = when
        env["GIT_COMMITTER_DATE"] = when
    proc = git(repo, args, env=env)
    if proc.returncode != 0:
        raise RuntimeError("commit failed in %s: %s" % (repo, proc.stderr.strip()))
    return git_ok(repo, ["rev-parse", "HEAD"])


def mktag(repo, refname, target, tagname, target_type):
    payload = (
        "object %s\ntype %s\ntag %s\ntagger battery <battery@example.invalid> 0 +0000\n\n%s\n"
        % (target, target_type, tagname, tagname)
    )
    oid = git_ok(repo, ["mktag"], stdin=payload)
    git_ok(repo, ["update-ref", refname, oid])
    return oid


def build_fixture(work):
    """One repository carrying every state and every carrier shape the contract names.

    main:  b0 -> m1 -> m2(HEAD)
    four abandoned chains of three commits each = the twelve findings on twelve commits;
    chain one additionally carries six extra branch refs so its oldest commit has MORE than
    three carriers and the top-three truncation is exercised in refname order.
    """
    repo = new_repo(work, "fixture")
    base = commit(repo, "readme.md", "base\n", "base")
    m1 = commit(repo, "a.txt", "a\n", "m1")
    m2 = commit(repo, "b.txt", "b\n", "m2")
    chains = {}
    for n in range(1, 5):
        git_ok(repo, ["checkout", "-q", "-b", "chain%d" % n, base])
        chains["chain%d" % n] = [
            commit(repo, "chain%d/%d.txt" % (n, k), "c%d-%d\n" % (n, k), "chain%d c%d" % (n, k))
            for k in range(1, 4)
        ]
    git_ok(repo, ["checkout", "-q", "main"])
    # six more refs on chain1's tip: chain1's OLDEST commit now has 1 + 6 = 7 carriers, so
    # the answer must be the three smallest by refname and nothing else.
    for label in ("aaa", "bbb", "ccc", "mmm", "yyy", "zzz"):
        git_ok(repo, ["update-ref", "refs/heads/extra-%s" % label, chains["chain1"][2]])
    # annotated tag, and a tag on that tag, on two different chains
    mktag(repo, "refs/tags/annotated", chains["chain2"][1], "annotated", "commit")
    inner = mktag(repo, "refs/tags/inner", chains["chain3"][0], "inner", "commit")
    nested_payload_target = inner
    mktag(repo, "refs/tags/nested", nested_payload_target, "nested", "tag")
    # refs that must NEVER be reported as carrying anything: a blob, and a tag on a blob
    blob = git_ok(repo, ["hash-object", "-w", "--stdin"], stdin="not a commit\n")
    git_ok(repo, ["update-ref", "refs/tags/blob-light", blob])
    mktag(repo, "refs/tags/blob-annotated", blob, "blob-annotated", "blob")
    # a commit no ref carries: committed on a detached HEAD and abandoned
    git_ok(repo, ["checkout", "-q", "--detach", m2])
    dangling = commit(repo, "dangling.txt", "d\n", "dangling")
    git_ok(repo, ["checkout", "-q", "main"])
    return {
        "repo": repo,
        "base": base,
        "m1": m1,
        "m2": m2,
        "chains": chains,
        "dangling": dangling,
        "absent": "0" * 40,
    }


# ── group 1 · the three git properties the equivalence rests on ───────────────────────
def group_ground_truth(fx):
    repo = fx["repo"]
    census = [
        row.split("\t")[-1]
        for row in git_ok(repo, ["for-each-ref", "--format=%(refname)"]).split("\n")
        if row
    ]
    target = fx["chains"]["chain1"][0]
    contains = [
        r
        for r in git_ok(
            repo, ["for-each-ref", "--contains", target, "--format=%(refname)"]
        ).split("\n")
        if r
    ]
    positions = [census.index(r) for r in contains if r in census]
    check(
        "1a `--contains` keeps the census order (it filters, it does not re-sort)",
        len(positions) == len(contains) and positions == sorted(positions),
        "contains=%s positions=%s" % (contains, positions),
    )
    nested_target = fx["chains"]["chain3"][0]
    nested_contains = [
        r
        for r in git_ok(
            repo, ["for-each-ref", "--contains", nested_target, "--format=%(refname)"]
        ).split("\n")
        if r
    ]
    check(
        "1b a tag on a tag on a commit IS a carrier; a ref on a blob never is",
        "refs/tags/nested" in nested_contains
        and "refs/tags/blob-light" not in nested_contains
        and "refs/tags/blob-annotated" not in nested_contains,
        "carriers of the nested-tagged commit: %s" % nested_contains,
    )
    check(
        "1c HEAD is not a ref `for-each-ref` lists, so it can never be named as a carrier",
        "HEAD" not in census,
        "census head: %s" % census[:5],
    )
    # How deep `%(*objectname)` peels a tag on a tag depends on the git: ONE level on 2.39.5
    # (Debian 12, our boxes) and ALL the way on 2.55.0 (the ubuntu-24.04 runner of CI run
    # 34731376655, where this check used to demand "one level only" and went red while every
    # helper verdict stayed equal to the oracle). The census has a branch for each shape — the
    # inner tag is peeled again explicitly, the commit is taken as it is — so the check pins
    # exactly those two, object name included, and fails on anything else.
    deref = git_ok(
        repo,
        [
            "for-each-ref",
            "refs/tags/nested",
            "--format=%(*objecttype) %(*objectname)",
        ],
    )
    shapes = {
        "tag %s" % git_ok(repo, ["rev-parse", "refs/tags/inner"]): "one level",
        "commit %s" % git_ok(repo, ["rev-parse", "refs/tags/nested^{commit}"]): "fully",
    }
    check(
        "1d `%%(*objectname)` of a tag on a tag is the inner tag or its commit (this git: %s)"
        % shapes.get(deref, "neither"),
        deref in shapes,
        "git dereferenced refs/tags/nested to %r; the census handles only %r"
        % (deref, sorted(shapes)),
    )


# ── groups 2-6 · the helper against the oracle, state by state ────────────────────────
def group_states(fx):
    repo = fx["repo"]
    twelve = [c for n in range(1, 5) for c in fx["chains"]["chain%d" % n]]
    cases = [
        ("2 twelve findings on twelve distinct commits", twelve),
        ("3 the same commit repeated is answered identically every time",
         [twelve[0]] * 4 + [twelve[5]] * 3 + [twelve[0]]),
        ("4 a mixture of HEAD, another ref, absent, dangling and the working tree",
         [fx["m2"], twelve[0], fx["absent"], fx["dangling"], "", fx["base"], fx["m1"],
          twelve[7], "", fx["dangling"]]),
        ("5 annotated and nested tags name their tag refs",
         [fx["chains"]["chain2"][1], fx["chains"]["chain3"][0], fx["chains"]["chain3"][2]]),
        ("6 a commit with seven carriers keeps exactly the first three",
         [fx["chains"]["chain1"][0], fx["chains"]["chain1"][2]]),
    ]
    for name, commits in cases:
        rc, lines, err = run_helper(repo, commits)
        if rc != 0:
            check(name, False, "helper exited %d: %s" % (rc, err.strip()))
            continue
        if len(lines) != 2 * len(commits):
            check(name, False, "%d line(s) for %d findings (two per finding)" % (len(lines), len(commits)))
            continue
        got = parse(lines)
        want = [oracle(repo, c) for c in commits]
        if got != want:
            diff = [
                "#%d %s: helper=%r oracle=%r" % (i, c[:12] or "<worktree>", g, w)
                for i, (c, g, w) in enumerate(zip(commits, got, want))
                if g != w
            ]
            check(name, False, " | ".join(diff))
        else:
            check(name, True)

    # the states must be DISTINCT, not merely equal to the oracle: an implementation that
    # answered `other` to everything would agree with an oracle that did the same.
    rc, lines, err = run_helper(
        repo, [fx["m2"], fx["chains"]["chain1"][0], fx["absent"], fx["dangling"], ""]
    )
    got = parse(lines) if rc == 0 else []
    check(
        "7 the four commit states are told apart, and the dangling one names no ref",
        rc == 0
        and [g[0] for g in got] == ["head", "other", "absent", "other", "worktree"]
        and got[1][1] != ""
        and got[3][1] == "",
        "rc=%d answers=%r %s" % (rc, got, err.strip()),
    )

    # the exact carrier list for the seven-carrier commit, spelled out rather than derived
    rc, lines, _ = run_helper(repo, [fx["chains"]["chain1"][0]])
    expected = "refs/heads/chain1 refs/heads/extra-aaa refs/heads/extra-bbb "
    check(
        "8 the three carriers are the first three by refname, with the trailing space",
        rc == 0 and lines == ["other", expected],
        "got %r want %r" % (lines, ["other", expected]),
    )


# ── group 7 · cost: the git invocation count must not follow the finding count ─────────
def group_cost(fx, work):
    repo = fx["repo"]
    twelve = [c for n in range(1, 5) for c in fx["chains"]["chain%d" % n]]
    counts = {}
    for label, commits in (("12", twelve), ("60", twelve * 5)):
        trace = os.path.join(work, "trace-%s.log" % label)
        if os.path.exists(trace):
            os.remove(trace)
        rc, lines, err = run_helper(repo, commits, trace=trace)
        if rc != 0 or len(lines) != 2 * len(commits):
            check("9 GIT_TRACE count for %s findings" % label, False,
                  "rc=%d lines=%d %s" % (rc, len(lines), err.strip()))
            return
        with open(trace, encoding="utf-8", errors="replace") as fh:
            invocations = [
                m.group(1)
                for m in (re.search(r"trace: built-in: git (.*)$", l) for l in fh)
                if m
            ]
        counts[label] = invocations
    a, b = counts["12"], counts["60"]
    check(
        "9 five times the findings costs the SAME number of git invocations",
        len(a) == len(b),
        "12 findings -> %d invocations, 60 -> %d" % (len(a), len(b)),
    )
    per_finding = [
        i for i in b if re.search(r"(--contains|\bname-rev\b|\bmerge-base\b)", i)
    ]
    check(
        "10 no --contains, name-rev or merge-base is issued at all",
        per_finding == [],
        "found: %s" % per_finding,
    )
    censuses = [i for i in b if i.startswith("for-each-ref")]
    walks = [i for i in b if i.startswith("rev-list")]
    check(
        "11 at most ONE ref census and ONE walk of the commit graph",
        len(censuses) <= 1 and len(walks) <= 1,
        "censuses=%s walks=%s" % (censuses, walks),
    )
    check(
        "12 the whole attribution costs a handful of git processes, not one per finding",
        len(b) <= 8,
        "%d invocations for 60 findings: %s" % (len(b), b),
    )


# ── group 8 · fail closed ─────────────────────────────────────────────────────────────
def group_fail_closed(fx, work):
    repo = fx["repo"]
    twelve = [c for n in range(1, 5) for c in fx["chains"]["chain%d" % n]]
    notrepo = os.path.join(work, "not-a-repo")
    os.makedirs(notrepo, exist_ok=True)
    rc, lines, err = run_helper(repo, twelve, env={"GIT_DIR": notrepo})
    check(
        "13 a git that cannot answer is an ERROR, never a silent classification",
        rc != 0 and lines == [] and "secrets-report-attribution" in err,
        "rc=%d lines=%r err=%r" % (rc, lines, err.strip()),
    )

    # a repository whose graph cannot be walked: the object of a needed parent is removed.
    broken = new_repo(work, "broken")
    first = commit(broken, "one.txt", "1\n", "one")
    second = commit(broken, "two.txt", "2\n", "two")
    git_ok(broken, ["checkout", "-q", "-b", "side", second])
    third = commit(broken, "three.txt", "3\n", "three")
    git_ok(broken, ["checkout", "-q", "main"])
    git_ok(broken, ["reset", "-q", "--hard", first])
    loose = os.path.join(broken, ".git", "objects", second[:2], second[2:])
    if os.path.exists(loose):
        os.remove(loose)
        rc, lines, err = run_helper(broken, [third])
        check(
            "14 a broken object store is an ERROR, never a false clean answer",
            rc != 0 and lines == [],
            "rc=%d lines=%r err=%r" % (rc, lines, err.strip()),
        )
    else:
        check("14 a broken object store is an ERROR", False,
              "could not stage the fault: %s is not a loose object" % loose)

    # a DETACHED HEAD: `for-each-ref` does not list HEAD, so a commit only it reaches has no
    # carrier — and it must still be reported as reachable, not as somebody else's.
    detached = new_repo(work, "detached")
    root = commit(detached, "one.txt", "1\n", "one")
    git_ok(detached, ["checkout", "-q", "--detach", root])
    only_head = commit(detached, "two.txt", "2\n", "two")
    side = git_ok(detached, ["rev-parse", "main"])
    rc, lines, err = run_helper(detached, [only_head, root, side])
    want = [oracle(detached, c) for c in (only_head, root, side)]
    check(
        "15 a detached HEAD reaches its own commits and borrows no ref for them",
        rc == 0 and parse(lines) == want and parse(lines)[0] == ("head", ""),
        "rc=%d helper=%r oracle=%r %s" % (rc, parse(lines), want, err.strip()),
    )

    empty = new_repo(work, "unborn")
    rc, lines, err = run_helper(empty, ["", "0" * 40])
    check(
        "16 an unborn HEAD answers working tree and absent, exactly as the shell did",
        rc == 0 and parse(lines) == [("worktree", ""), ("absent", "")],
        "rc=%d lines=%r err=%r" % (rc, lines, err.strip()),
    )

    # ── the mutant that only a DATE-SKEWED graph can see ───────────────────────────────
    # `git rev-list` without --topo-order serves the newest commit first, and a commit that is
    # BOTH a ref tip and an ancestor of another tip sits in that queue from the start. Date it
    # AFTER its own descendants and it is handed out before the children that must feed it: it
    # then passes its marks to its parents while still incomplete, and everything below it
    # loses the refs that arrive late. Committer dates are attacker- and clock-controlled, so
    # this is not a curiosity.
    #
    # The shape matters, and the first draft of this case did NOT have it: the skewed commit
    # must have a PARENT. A skewed ROOT loses nothing, because the late marks still land in
    # its own entry — there is simply nobody left to hand them to. Measured: dropping
    # --topo-order left the whole battery green until this case had the right shape.
    #
    #   s0 <- s1 <- s2 <- s3      dates: s0 2000 · s1 2030 · s2 2010 · s3 2020
    #   refs: chain and skew-tip on s3, skew-mid on s1
    #   the finding is on s0, whose true carriers are all three.
    skew = new_repo(work, "skew")
    commit(skew, "base.txt", "base\n", "base")  # so `main` exists to come back to
    git_ok(skew, ["checkout", "-q", "--orphan", "chain"])
    s0 = commit(skew, "s0.txt", "0\n", "s0", when="2000-01-01T00:00:00+0000")
    s1 = commit(skew, "s1.txt", "1\n", "s1", when="2030-01-01T00:00:00+0000")
    commit(skew, "s2.txt", "2\n", "s2", when="2010-01-01T00:00:00+0000")
    s3 = commit(skew, "s3.txt", "3\n", "s3", when="2020-01-01T00:00:00+0000")
    git_ok(skew, ["update-ref", "refs/heads/skew-tip", s3])
    git_ok(skew, ["update-ref", "refs/heads/skew-mid", s1])
    git_ok(skew, ["checkout", "-q", "main"])
    rc, lines, err = run_helper(skew, [s0])
    want = [oracle(skew, s0)]
    check(
        "17 an ancestor dated AFTER its descendants still collects every carrier",
        rc == 0 and parse(lines) == want and len(want[0][1].split()) == 3,
        "rc=%d helper=%r oracle=%r (three carriers expected) %s"
        % (rc, parse(lines), want, err.strip()),
    )

    # ── the framing contract with check-secrets.sh, crossed from this side ─────────────
    # The gate escapes every field before it frames it by position; this reader undoes exactly
    # that escape. The two tables have to agree, so they are exercised, not asserted by reading.
    twelve = [c for n in range(1, 5) for c in fx["chains"]["chain%d" % n]]
    head_oid = git_ok(repo, ["rev-parse", "HEAD"])
    # The discriminator: HEAD's own object name with its first byte written as an escape. Decoded
    # it is HEAD and the answer is `head`; treated as literal text it is not an object name at
    # all and the answer would be `absent`. One character tells the two implementations apart.
    disguised = "\\x%02x%s" % (ord(head_oid[0]), head_oid[1:])
    rc, lines, err = run_helper(repo, [disguised], raw=True)
    check(
        "19 an escaped Commit is DECODED before it is judged, not matched as text",
        rc == 0 and parse(lines) == [("head", "")],
        "rc=%d sent=%r lines=%r %s" % (rc, disguised, lines, err.strip()),
    )
    for bad, why in (("\\", "a lone trailing backslash"),
                     ("\\q", "an unknown escape"),
                     ("\\xZZ", "a malformed hex escape")):
        rc, lines, err = run_helper(repo, [bad], raw=True)
        check(
            "20 %s in a Commit line is a framing failure, not a commit" % why,
            rc != 0 and lines == [] and "framing" in err,
            "rc=%d lines=%r err=%r" % (rc, lines, err.strip()),
        )
    rc, lines, err = run_helper(repo, [head_oid, "", twelve[0]], raw=True)
    check(
        "21 an empty line is still the working tree once decoding is in the way",
        rc == 0 and parse(lines)[1] == ("worktree", "") and parse(lines)[0][0] == "head",
        "rc=%d lines=%r %s" % (rc, lines, err.strip()),
    )

    rc, lines, err = run_helper(repo, [])
    check(
        "22 no findings is an empty answer with status 0",
        rc == 0 and lines == [],
        "rc=%d lines=%r err=%r" % (rc, lines, err.strip()),
    )


def main():
    if not os.path.exists(HELPER):
        sys.stderr.write("test-secrets-report-attribution: %s not found\n" % HELPER)
        return 2
    if shutil.which("git") is None:
        sys.stderr.write("test-secrets-report-attribution: git is not on PATH\n")
        return 2
    sys.stdout.write("test-secrets-report-attribution: the attribution battery\n")
    work = tempfile.mkdtemp(prefix="secrets-attribution-tests.")
    try:
        fx = build_fixture(work)
        group_ground_truth(fx)
        group_states(fx)
        group_cost(fx, work)
        group_fail_closed(fx, work)
    except RuntimeError as exc:
        # stdout is block-buffered into a file and stderr is not: without the flush the verdict
        # lands ABOVE the checks it summarises, which is how CI run 34731376655 read it.
        sys.stdout.flush()
        sys.stderr.write("test-secrets-report-attribution: could not run — %s\n" % exc)
        return 2
    finally:
        shutil.rmtree(work, ignore_errors=True)
    sys.stdout.write("\n")
    if FAILURES:
        sys.stdout.flush()
        sys.stderr.write(
            "test-secrets-report-attribution: %d of %d check(s) failed\n"
            % (len(FAILURES), CHECKS)
        )
        return 1
    sys.stdout.write("test-secrets-report-attribution: OK — %d/%d\n" % (CHECKS, CHECKS))
    return 0


if __name__ == "__main__":
    sys.exit(main())
