#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""secrets-report-attribution.py — classify every finding commit ONCE, with ONE ref census
and ONE shared walk of the commit graph.

WHY THIS EXISTS. On 2026-09-09 the `secrets` job (run 34299976570, job 102304614840) died
inside the REPORTING phase: the scan itself had finished at 03:03:45.373Z and TERM arrived
at 03:03:57.526Z. What was running in between is `scripts/check-secrets.sh`'s per-finding
attribution, and it did two things that scale with the number of findings:

  · it classified every finding TWICE — once in the counting loop, once in the printing one;
  · for every finding not reachable from HEAD it ran `git for-each-ref --contains <commit>`,
    and each of those walks history once per ref in the clone. Measured on this box against
    the commit the job's own log names: 22 558 ms over 11 834 refs. Twelve findings is a
    dozen full ref sweeps for an answer that depends on the SAME frozen graph every time.

So the sweep is done ONCE, here, and the answers are handed back per finding:

  freeze HEAD  ->  one `git for-each-ref` census  ->  one `git rev-list --topo-order
  --parents` over the census tips plus HEAD  ->  propagate, from the tips towards the roots,
  which refs and whether HEAD reach each commit.

WHAT IS NOT CHANGED, and this is the load-bearing part. The four states the gate prints are
the same four states, decided in the same order as the shell it replaces:

  worktree  the finding has no commit (uncommitted working tree)   -> counted as YOURS
  absent    `<commit>^{commit}` does not resolve in this clone     -> counted as NOT yours
  head      the commit is an ancestor of (or is) the frozen HEAD   -> counted as YOURS
  other     the commit is here, and HEAD does not reach it         -> counted as NOT yours

and for `other` the refs reported are EXACTLY the first three `git for-each-ref --contains`
would have printed, in the same order. That equivalence is not an assumption; it rests on
three properties measured with git 2.39.5 (see scripts/test-secrets-report-attribution.py,
which asserts all three against the real command on every run):

  1. `for-each-ref` sorts by refname and `--contains` only FILTERS, so the surviving refs
     keep the census order — the first three of the filtered list are the three smallest
     census indices.
  2. `--contains` peels tags all the way down (a tag on a tag on a commit is reported), and
     a ref that peels to a blob or a tree is dropped, never treated as an ancestor. The
     census does the same: `%(*objectname)` peels only ONE level on git 2.39.5 and all the
     way on git 2.55.0, so a deref that is still a tag is resolved explicitly rather than
     trusted, and a deref that is already a commit is taken as it is.
  3. HEAD itself is never in `for-each-ref` output, so a commit reachable only from a
     detached HEAD keeps an EMPTY carrier list instead of borrowing a ref that does not
     carry it.

Keeping only the three smallest indices per commit is exact, not an approximation: if x is
among the three smallest of A u B then fewer than three elements of A u B are below x, so
fewer than three elements of A are below x, so x survives in A's own top three.

FAIL CLOSED. Any git invocation that fails, any answer that cannot be read, any input line
whose shape cannot be checked -> non-zero exit and a message on stderr. The caller turns
that into `COULD NOT LOOK` (exit 2). "I could not attribute it" is never "it is clean", and
a present commit that no ref carries keeps its `other` state rather than inventing a ref.

INPUT   one finding per line on stdin: the Commit field, ESCAPED, empty for the working tree.
        Nothing else is read — no rule, no path, no author, and no scanned content, so no
        credential material can reach this process, its output or its errors.

        The escape is the one `check-secrets.sh` applies to every field, and the two tables
        must stay identical (the battery crosses them end to end, through the real gate):

            \\ -> \\\\     TAB -> \\t     LF -> \\n     CR -> \\r
            any other control byte (<0x20) and DEL (0x7f) -> \\xNN
            everything else verbatim, UTF-8 included

        It exists because a VALID Git path may contain 0x1f, a tab or a newline, so no byte
        can be reserved as a separator: root returned exactly that counterexample against the
        first version of this phase, where one HEAD finding with `File = odd<US>path.txt`
        shifted the columns and produced a clean verdict. Framing is by POSITION — one field
        per line — and the escape is what makes that position exact. A line this reader cannot
        decode is a framing failure, not a commit: it refuses rather than guesses.

OUTPUT  TWO lines per input line, same order: the state, then the carrier list — empty unless
        the state is `other`, and otherwise the space-joined refs WITH the trailing space the
        shell it replaces produced (`... | head -3 | tr '\n' ' '`). Two lines rather than one
        tab-joined line for the same reason as the input: no byte is a separator.
EXIT    0 answered every line · 3 could not answer (nothing usable on stdout).
"""

import re
import subprocess
import sys

WORKTREE, ABSENT, HEAD, OTHER = "worktree", "absent", "head", "other"
SIMPLE = {"\\": "\\", "t": "\t", "n": "\n", "r": "\r"}
TOP_REFS = 3
# git resolves an abbreviated object name; the gate's own scanner writes full ones. Anything
# outside this shape is not fed to the batch protocol (a newline in it would desynchronise
# the stream) and is resolved on its own instead.
HEX_OID = re.compile(r"\A[0-9a-fA-F]{4,64}\Z")


def die(message):
    """Refuse to answer. The caller must read this as COULD NOT LOOK, never as clean."""
    sys.stderr.write("secrets-report-attribution: %s\n" % message)
    raise SystemExit(3)


def run_git(args, stdin=None, keep_stderr=True):
    """One git invocation. A non-zero status is fatal: a half-read graph is not an answer."""
    try:
        proc = subprocess.run(
            ["git"] + args,
            input=stdin,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
    except OSError as exc:
        die("cannot run `git %s`: %s" % (" ".join(args), exc))
    if proc.returncode != 0:
        detail = proc.stderr.strip().splitlines()
        die(
            "`git %s` exited %d%s"
            % (
                " ".join(args),
                proc.returncode,
                (": " + detail[-1]) if (keep_stderr and detail) else "",
            )
        )
    return proc.stdout


def safe_name(value):
    """Echo an object name only when it looks like one; never echo unvalidated input."""
    return value if HEX_OID.match(value) else "<%d-byte non-hex commit field>" % len(value)


def unescape(token):
    """Undo check-secrets.sh's field escape. Refuses anything it cannot decode exactly."""
    out = []
    i = 0
    while i < len(token):
        ch = token[i]
        if ch != "\\":
            out.append(ch)
            i += 1
            continue
        if i + 1 >= len(token):
            die("a Commit line ends in a lone backslash: the record framing is broken")
        nxt = token[i + 1]
        if nxt in SIMPLE:
            out.append(SIMPLE[nxt])
            i += 2
        elif nxt == "x":
            digits = token[i + 2:i + 4]
            if len(digits) != 2 or any(d not in "0123456789abcdefABCDEF" for d in digits):
                die("a Commit line carries a malformed \\xNN escape: the record framing is broken")
            out.append(chr(int(digits, 16)))
            i += 4
        else:
            die("a Commit line carries an unknown escape '\\%s': the record framing is broken" % nxt)
    return "".join(out)


def read_findings():
    """The Commit column, one ESCAPED value per line, order preserved.

    Empty means the working tree. Decoding is part of the framing check: a line that does not
    decode is a broken record, and a broken record is COULD NOT LOOK — never a classification.
    """
    data = sys.stdin.read()
    lines = data.split("\n")
    if lines and lines[-1] == "":
        lines.pop()
    return [unescape(line) for line in lines]


def resolve_commits(values):
    """value -> commit oid, or None when this clone does not carry it.

    Same question `git cat-file -e "<value>^{commit}"` answered per finding in the shell,
    asked once per DISTINCT value and in one batch for the well-shaped ones.
    """
    resolved = {}
    batched = [v for v in values if HEX_OID.match(v)]
    if batched:
        out = run_git(
            ["cat-file", "--batch-check"],
            stdin="".join("%s^{commit}\n" % v for v in batched),
            keep_stderr=False,
        )
        answers = out.split("\n")
        if answers and answers[-1] == "":
            answers.pop()
        if len(answers) != len(batched):
            die(
                "git cat-file --batch-check answered %d of %d commit(s)"
                % (len(answers), len(batched))
            )
        for value, answer in zip(batched, answers):
            fields = answer.split(" ")
            if len(fields) >= 2 and fields[1] == "missing":
                resolved[value] = None
            elif len(fields) >= 2 and fields[1] == "commit" and HEX_OID.match(fields[0]):
                resolved[value] = fields[0]
            else:
                die(
                    "git cat-file --batch-check gave an unreadable answer for %s"
                    % safe_name(value)
                )
    for value in values:
        if value in resolved:
            continue
        # Not object-name shaped. It cannot go through the batch stream, so it is asked on
        # its own — the same one-at-a-time cost the shell always paid, bounded by how many
        # such oddities a report carries (the pinned scanner writes none).
        proc = subprocess.run(
            ["git", "rev-parse", "--verify", "--quiet", "%s^{commit}" % value],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        if proc.returncode == 0:
            oid = proc.stdout.strip()
            resolved[value] = oid if HEX_OID.match(oid) else None
        elif proc.returncode == 1:
            resolved[value] = None
        else:
            die(
                "git rev-parse exited %d for %s"
                % (proc.returncode, safe_name(value))
            )
    return resolved


def frozen_head():
    """The HEAD this phase is judged against, resolved ONCE.

    An unborn HEAD is a state, not a failure: `git merge-base --is-ancestor <c> HEAD` failed
    there too, and every commit was therefore reported as not reachable. Same answer here.
    """
    proc = subprocess.run(
        ["git", "rev-parse", "--verify", "--quiet", "HEAD^{commit}"],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if proc.returncode == 0:
        oid = proc.stdout.strip()
        if not HEX_OID.match(oid):
            die("git rev-parse HEAD gave an unreadable object name")
        return oid
    if proc.returncode == 1:
        return None
    die("git rev-parse exited %d resolving HEAD" % proc.returncode)


def ref_census():
    """The frozen ref census: (refname, tip commit or None) in `for-each-ref` order.

    ONE invocation, no `--contains`. How far `%(*objectname)` dereferences a tag depends on
    the git, so a deref that is still a tag is peeled explicitly below — measured, not
    assumed: on git 2.39.5 a tag->tag->commit ref reports `*objecttype` = tag, on git 2.55.0
    it reports the commit, and `for-each-ref --contains` reports that ref on both.
    """
    out = run_git(
        [
            "for-each-ref",
            "--format=%(objectname)\t%(objecttype)\t%(*objectname)\t%(*objecttype)\t%(refname)",
        ]
    )
    names, tips, deep = [], [], []
    for row in out.split("\n"):
        if not row:
            continue
        fields = row.split("\t")
        if len(fields) != 5:
            die("git for-each-ref emitted a row with %d fields, expected 5" % len(fields))
        objectname, objecttype, deref_name, deref_type, refname = fields
        if any(c < " " or c == "\x7f" or c == " " for c in refname):
            # `git check-ref-format` forbids these, so seeing one means the ref store is not
            # what git says it is. The carrier list is emitted one line at a time; a refname
            # carrying a newline would break that framing, so this fails closed instead.
            die("git for-each-ref emitted a refname carrying a control character or a space")
        index = len(names)
        names.append(refname)
        if objecttype == "commit":
            tips.append(objectname)
        elif objecttype == "tag" and deref_type == "commit":
            tips.append(deref_name)
        elif objecttype == "tag":
            # tag on a tag (on a tag...): peel the whole way, in one batch, below.
            tips.append(None)
            deep.append((index, objectname))
        else:
            # A ref on a blob or a tree is not an ancestor of anything, and `--contains`
            # drops it. Measured: it is dropped silently, with status 0.
            tips.append(None)
    if deep:
        out = run_git(
            ["cat-file", "--batch-check"],
            stdin="".join("%s^{commit}\n" % oid for _, oid in deep),
            keep_stderr=False,
        )
        answers = out.split("\n")
        if answers and answers[-1] == "":
            answers.pop()
        if len(answers) != len(deep):
            die(
                "git cat-file --batch-check answered %d of %d nested tag(s)"
                % (len(answers), len(deep))
            )
        for (index, _oid), answer in zip(deep, answers):
            fields = answer.split(" ")
            if len(fields) >= 2 and fields[1] == "commit" and HEX_OID.match(fields[0]):
                tips[index] = fields[0]
            elif len(fields) >= 2 and fields[1] == "missing":
                tips[index] = None  # peels to something that is not a commit
            else:
                die("git cat-file --batch-check gave an unreadable answer peeling a tag")
    return names, tips


def merge_top(current, incoming):
    """The <=3 smallest census indices of the union. Exact, per the proof in the header."""
    if not incoming:
        return current
    if not current:
        return incoming
    out, i, j = [], 0, 0
    while len(out) < TOP_REFS and (i < len(current) or j < len(incoming)):
        if j >= len(incoming) or (i < len(current) and current[i] < incoming[j]):
            out.append(current[i])
            i += 1
        elif i >= len(current) or incoming[j] < current[i]:
            out.append(incoming[j])
            j += 1
        else:
            out.append(current[i])
            i += 1
            j += 1
    return tuple(out)


def propagate(tips, head_oid):
    """ONE walk. Returns (commits reachable from HEAD, commit -> top-3 carrier indices).

    `git rev-list --topo-order` shows no parent before all of its children, so a single pass
    over its output in order completes every commit's marks before they are handed to its
    parents. Seeding a ref's index at its tip and OR-ing towards the roots means an index
    reaches a commit exactly when that ref's tip reaches it — which is what `--contains`
    answers, one ref at a time.
    """
    seeds = {}
    for index, tip in enumerate(tips):
        if tip is None:
            continue
        seeds[tip] = merge_top(seeds.get(tip, ()), (index,))
    starts = sorted(seeds)
    if head_oid is not None and head_oid not in seeds:
        starts.append(head_oid)
    if not starts:
        return set(), {}
    out = run_git(
        ["rev-list", "--topo-order", "--parents", "--stdin"],
        stdin="".join("%s\n" % oid for oid in starts),
    )
    from_head = set()
    if head_oid is not None:
        from_head.add(head_oid)
    carriers = dict(seeds)
    for row in out.split("\n"):
        if not row:
            continue
        names = row.split(" ")
        commit, parents = names[0], names[1:]
        inherited = carriers.get(commit)
        reached = commit in from_head
        if not inherited and not reached:
            continue
        for parent in parents:
            if reached:
                from_head.add(parent)
            if inherited:
                merged = merge_top(carriers.get(parent, ()), inherited)
                if merged:
                    carriers[parent] = merged
    return from_head, carriers


def main():
    findings = read_findings()
    distinct = list(dict.fromkeys(v for v in findings if v != ""))
    resolved = resolve_commits(distinct) if distinct else {}
    present = {v: oid for v, oid in resolved.items() if oid is not None}

    from_head, carriers, refnames = set(), {}, []
    if present:
        head_oid = frozen_head()
        refnames, tips = ref_census()
        from_head, carriers = propagate(tips, head_oid)

    verdicts = {}
    for value, oid in resolved.items():
        if oid is None:
            verdicts[value] = (ABSENT, "")
        elif oid in from_head:
            verdicts[value] = (HEAD, "")
        else:
            carried = carriers.get(oid, ())
            # `head -3 | tr '\n' ' '` left a trailing space when it printed anything; the
            # gate's line is reproduced byte for byte, trailing space included.
            refs = "".join("%s " % refnames[i] for i in carried)
            verdicts[value] = (OTHER, refs)

    write = sys.stdout.write
    for value in findings:
        state, refs = (WORKTREE, "") if value == "" else verdicts[value]
        write("%s\n%s\n" % (state, refs))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
