#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# build-decision-index.py — MEM-03. The DERIVED decision index: a diffable NDJSON that
# travels in `main`, and a local FTS5 database that is never committed.
#
# WHY PYTHON AND NOT THE sqlite3 CLI. Measured in this container (claude-code-3, 2026-08-16):
# `command -v sqlite3` finds NOTHING — the binary is not installed — while python3 3.11.2
# carries the sqlite3 module against sqlite 3.40.1 WITH FTS5 compiled in. A builder shelled
# out to `sqlite3` would have been dead on arrival on all three containers. This is the
# measurement MEM-03's row already carried; it is re-taken here rather than trusted.
#
# THE ONE RULE THAT SHAPES EVERYTHING: THE INDEX IS DERIVED.
# It is generated, never edited. Two consequences that are enforced, not just documented:
#   * `check` rebuilds from the tree and compares BYTE FOR BYTE with the committed NDJSON.
#     A decision added, edited or deleted without regenerating makes it exit 1 and NAME the
#     file. There is nothing to hand-maintain, so there is nothing to corrupt.
#   * Two runs over the same tree must produce identical bytes. Records are sorted by path
#     under a byte comparison (never locale collation, never readdir order) and nothing that
#     changes between two runs of the same tree — no clock, no hostname, no run counter —
#     may enter the NDJSON. The determinism self-test is what keeps that true.
#
# WHY THE COMMIT DATE IS NOT IN THE NDJSON, AND WHERE IT DID GO.
# FASE 1 §P1 fixed the date source: `git log -1 --format=%cI -- <file>`, NEVER mtime. That
# rule is obeyed exactly — see commit_date() — but the value lives in the FTS5 database and
# in `query` output, NOT in the committed NDJSON, and that is a deliberate correction to the
# sketch rather than a shortcut. A file's commit date does not exist until the commit exists,
# so an NDJSON carrying it can never be correct in the same commit that introduces the
# decision: you would write the index (date null), commit both, and the index would be stale
# the instant the commit landed — the gate would redden on the very push that fed it, and the
# only way out would be a second empty commit forever. An artifact that cannot be committed
# correctly in one commit teaches people to bypass the gate that checks it. So the NDJSON
# carries only what is knowable from the TREE alone (`decided:` as declared by the author,
# plus a content hash), which has no such fixpoint, and the commit date is derived locally at
# index time where staleness costs nothing.
#
# THREE ANSWERS, NEVER TWO. Exit 0 = the index is current. Exit 1 = BROKEN, and every reason
# is named. Exit 2 = UNVERIFIED: git unavailable, the enumeration returned nothing, a file
# unreadable, a front-matter block malformed. "I could not look" is never reported as clean —
# the shape scripts/check-migrations.sh already uses in this repository.

"""Build and verify the derived decision index (MEM-02/MEM-03/MEM-07)."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path

# The sqlite3 module is imported TOLERANTLY, and that is the whole reason the two outputs are
# separate. FTS5 is the searchable convenience; the NDJSON register is the artifact that must
# survive a stripped interpreter, because it is the half git carries to the other containers.
# A builder that died on `import sqlite3` would lose the register to protect the cache.
try:
    import sqlite3
except ImportError:  # pragma: no cover - measured present here (3.40.1, FTS5 on)
    sqlite3 = None  # type: ignore[assignment]

# --- The CLOSED front-matter schema (MEM-02) --------------------------------------------
#
# Five fields, and the closure is the point: an unknown key is an ERROR, not something to
# ignore. A parser that skips what it does not recognise reads `superceded-by:` (a typo that
# a 205-file mechanical sweep will produce) as "this document has no successor" and publishes
# a retired decision as current — the exact failure this register exists to make impossible.
REQUIRED = ("decision", "status", "decided", "authority")
OPTIONAL = ("superseded-by",)
ALLOWED = set(REQUIRED) | set(OPTIONAL)

STATUSES = ("vigente", "superseded", "propuesta", "retirada")
AUTHORITIES = ("fran", "sesion")

KEY_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")

# The subject of the index. design/ is where decisions in prose live; the pathspec is
# explicit so the census is a property of this file and not of whatever is lying in the tree.
DOC_PATHSPEC = "design/*.md"
NDJSON_PATH = "design/DECISIONES.ndjson"

FTS_TOKENIZE = "unicode61 remove_diacritics 2"


def _is_public_export(root: Path) -> bool:
    """Does scripts/hub-leg.sh classify this tree as the stamped public export?

    Fail-closed by construction: any failure to ask (script absent, non-zero, unreadable)
    answers False, so the caller keeps the Unverified path and the exit stays 2.
    """
    try:
        out = subprocess.run(
            ["bash", str(root / "scripts" / "hub-leg.sh"), "--classify", "--root", str(root)],
            capture_output=True, text=True, timeout=30, check=False,
        )
    except Exception:
        return False
    return out.stdout.strip() == "public"


class CuratedAbsence(Exception):
    """The census input is absent BECAUSE the curated public export drops it.

    Distinct from Unverified on purpose: an empty census in the hub is a broken checkout
    and must stay exit 2, but in the exported tree design/ is gone BY DESIGN and there is
    nothing to verify. The discriminator is the marker the curation pipeline writes and
    never tracks in the hub — the same one check-public-counts.sh reads. Measured
    2026-08-31: without this, `task lint:decision-index` answers 2 from an exported tree,
    and the canon's fail-closed rule turns that into a rejected push in public.
    """


class Unverified(Exception):
    """Could not look. Exit 2 — never reported as clean."""


class Broken(Exception):
    """Looked, and it is wrong. Exit 1 — every reason named."""


# --- git -------------------------------------------------------------------------------

def git(root: Path, *args: str) -> bytes:
    """Run git and return stdout, or raise Unverified naming the failure."""
    try:
        proc = subprocess.run(
            ("git", "-C", str(root)) + args,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
    except OSError as exc:  # git absent entirely
        raise Unverified(f"cannot execute git ({exc}); the census is UNVERIFIED") from exc
    if proc.returncode != 0:
        detail = proc.stderr.decode("utf-8", "replace").strip()
        raise Unverified(f"git {' '.join(args)} failed rc={proc.returncode}: {detail}")
    return proc.stdout


def enumerate_docs(root: Path) -> list[str]:
    """The census: tracked AND untracked-but-not-ignored design docs, byte-sorted.

    Untracked files are INCLUDED on purpose: a decision written but not yet committed is
    still a decision, and an index that only saw committed files would go stale silently
    between writing and committing — which is the window the author is actually in.
    """
    out = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard",
              "--", DOC_PATHSPEC)
    paths = [p.decode("utf-8") for p in out.split(b"\0") if p]
    for p in paths:
        # A newline in a path would desynchronise every downstream tool that reads lines.
        if "\n" in p or "\r" in p:
            raise Unverified(f"path contains a newline, refusing to index it: {p!r}")
    # Deduplicate: a path can be reported once even when both --cached and --others are
    # given, but the set operation is cheap and makes the invariant explicit.
    unique = sorted(set(paths), key=lambda s: s.encode("utf-8"))
    if not unique:
        # Zero is COULD NOT LOOK, not "there are no decisions". On 2026-08-16 this same
        # enumeration found 887 files under design/; an empty result means the pathspec
        # stopped matching, not that the decisions evaporated.
        #
        # 887 and not 205: a git pathspec is NOT a shell glob — `*` crosses `/`, so
        # `design/*.md` reaches an internal design note (not shipped)** and design-PASE-B/** too. `ls
        # design/*.md` answers 205 and would have made this floor a decoy that only fires
        # when design/ disappears entirely. That is the recursive reading ON PURPOSE: a
        # decision recorded in an internal design note (not shipped) is still a decision.
        # The hardened classifier, not a bare marker file: hub-leg.sh keys on the
        # sentence the generator stamps AND the absence of every hub-only path, because
        # adversarial review X-07 showed a lone marker is a password a stray copy can type.
        if _is_public_export(root):
            raise CuratedAbsence(
                f"{DOC_PATHSPEC} is curated out of the public export; there is no "
                "decision census to take here."
            )
        raise Unverified(
            f"ZERO files matched {DOC_PATHSPEC}; the decision census is UNVERIFIED. "
            "On 2026-08-16 the same enumeration found 887. Check the pathspec, the repo "
            "root, and whether design/ moved."
        )
    return unique


_HAS_COMMITS: dict[str, bool] = {}


def has_commits(root: Path) -> bool:
    """Does this repository have any commit at all?

    Asked separately because `git log` on an unborn HEAD exits 128 — a rc that is otherwise
    indistinguishable from a broken repository. Collapsing the two turned "this tree has no
    commits yet" into UNVERIFIED and refused to index a freshly initialised checkout; the
    self-test fixture caught it. Three answers again: has commits / has none / could not look.
    """
    key = str(root)
    if key not in _HAS_COMMITS:
        proc = subprocess.run(
            ("git", "-C", str(root), "rev-parse", "--verify", "--quiet", "HEAD"),
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        if proc.returncode not in (0, 1):
            raise Unverified(
                "cannot determine whether the repository has commits: "
                + proc.stderr.decode("utf-8", "replace").strip()
            )
        _HAS_COMMITS[key] = proc.returncode == 0
    return _HAS_COMMITS[key]


def commit_date(root: Path, path: str) -> str | None:
    """The date rule of FASE 1 §P1: the COMMIT date, never the mtime.

    mtime is a property of when a file was last touched on THIS disk — a checkout, a rebase
    or a `cp -r` rewrites it, so it says nothing about when anything was decided and differs
    between the three containers for identical content. Returns None for a file with no
    commit yet, which is a real state and is reported as such.
    """
    if not has_commits(root):
        return None
    out = git(root, "log", "-1", "--format=%cI", "--", path).decode("utf-8").strip()
    return out or None


# --- front-matter ------------------------------------------------------------------------

def parse_frontmatter(path: str, raw: bytes) -> dict[str, str] | None:
    """Strict parse of the closed schema. None = no front-matter (not a decision document).

    Hand-written rather than PyYAML because PyYAML is NOT installed in this container
    (measured: ModuleNotFoundError) and because the schema is five flat scalars: a strict
    mini-parser can REJECT a nested block or a list, where a real YAML parser would happily
    accept structure the rest of this pipeline cannot represent.
    """
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise Unverified(f"{path}: not valid UTF-8 ({exc}); UNVERIFIED") from exc

    if not text.startswith("---\n"):
        return None

    end = text.find("\n---", 3)
    if end == -1:
        raise Unverified(f"{path}: front-matter opens with '---' and never closes; UNVERIFIED")
    block = text[4:end + 1]

    fields: dict[str, str] = {}
    for lineno, line in enumerate(block.splitlines(), start=2):
        if not line.strip():
            continue
        if line[:1] in (" ", "\t"):
            raise Broken(
                f"{path}:{lineno}: indented line in decision front-matter "
                f"({line.strip()!r}). The schema is five flat scalars; nested structure is "
                "not representable in the index."
            )
        if ":" not in line:
            raise Broken(f"{path}:{lineno}: front-matter line without a colon: {line!r}")
        key, _, value = line.partition(":")
        key = key.strip()
        value = value.strip()
        if key not in ALLOWED:
            raise Broken(
                f"{path}:{lineno}: unknown front-matter key {key!r}. The schema is CLOSED: "
                f"{sorted(ALLOWED)}. A typo silently ignored is how a superseded decision "
                "gets published as current."
            )
        if key in fields:
            raise Broken(f"{path}:{lineno}: duplicate front-matter key {key!r}")
        fields[key] = value
    return fields


def validate(path: str, fields: dict[str, str], root: Path) -> None:
    """Every rule that can be decided from ONE document. Cross-document rules live in build."""
    for key in REQUIRED:
        if key not in fields or not fields[key]:
            raise Broken(f"{path}: front-matter is missing required key {key!r}")

    if not KEY_RE.match(fields["decision"]):
        raise Broken(
            f"{path}: decision key {fields['decision']!r} is not a slug "
            "(lowercase, digits and hyphens)"
        )
    if fields["status"] not in STATUSES:
        raise Broken(
            f"{path}: status {fields['status']!r} is not one of {list(STATUSES)}"
        )
    if fields["authority"] not in AUTHORITIES:
        raise Broken(
            f"{path}: authority {fields['authority']!r} is not one of {list(AUTHORITIES)}"
        )
    if not DATE_RE.match(fields["decided"]):
        raise Broken(f"{path}: decided {fields['decided']!r} is not YYYY-MM-DD")
    try:
        y, m, d = (int(x) for x in fields["decided"].split("-"))
        import datetime
        datetime.date(y, m, d)
    except ValueError as exc:
        raise Broken(f"{path}: decided {fields['decided']!r} is not a real date ({exc})") from exc

    sb = fields.get("superseded-by", "")
    if fields["status"] == "superseded":
        if not sb:
            raise Broken(
                f"{path}: status is 'superseded' with no superseded-by. A document that "
                "says it was replaced without saying by what is worse than one that says "
                "nothing: the reader stops looking."
            )
        if not (root / sb).is_file():
            raise Broken(f"{path}: superseded-by points at a file that does not exist: {sb}")
    elif sb:
        raise Broken(
            f"{path}: superseded-by is set on a document whose status is "
            f"{fields['status']!r}. A successor on a document that is not superseded is a "
            "contradiction the index would have to resolve by guessing."
        )


def title_of(raw: bytes) -> str:
    """First markdown H1, or empty. Purely a convenience column; never an identity."""
    for line in raw.decode("utf-8", "replace").splitlines():
        if line.startswith("# "):
            return line[2:].strip()
    return ""


# --- records ------------------------------------------------------------------------------

def build_records(root: Path) -> list[dict[str, object]]:
    """The whole derivation. Raises Broken/Unverified rather than emitting a partial index."""
    records: list[dict[str, object]] = []
    for rel in enumerate_docs(root):
        p = root / rel
        try:
            raw = p.read_bytes()
        except OSError as exc:
            raise Unverified(f"{rel}: cannot read ({exc}); the index is UNVERIFIED") from exc
        fields = parse_frontmatter(rel, raw)
        if fields is None:
            continue
        validate(rel, fields, root)
        records.append({
            "authority": fields["authority"],
            "decided": fields["decided"],
            "decision": fields["decision"],
            "path": rel,
            # A content fingerprint of the BYTES ON DISK. This is what lets `check` catch an
            # edited decision whose front-matter did not change: without it the index would
            # only notice files appearing and disappearing.
            "sha256": hashlib.sha256(raw).hexdigest(),
            "status": fields["status"],
            "superseded_by": fields.get("superseded-by") or None,
            "title": title_of(raw),
        })

    records.sort(key=lambda r: str(r["path"]).encode("utf-8"))

    # --- cross-document rules -------------------------------------------------------------
    # TWO VIGENTES UNDER ONE KEY is the measured failure #1 ("eight entitlement documents and
    # none says which one rules"). It is refused at BUILD time, so an ambiguous tree cannot
    # even produce an index — the ambiguity is impossible in writing rather than mitigated in
    # reading.
    by_key: dict[str, list[str]] = {}
    for r in records:
        if r["status"] == "vigente":
            by_key.setdefault(str(r["decision"]), []).append(str(r["path"]))
    dupes = {k: v for k, v in by_key.items() if len(v) > 1}
    if dupes:
        lines = [f"  {k}: " + ", ".join(sorted(v)) for k, v in sorted(dupes.items())]
        raise Broken(
            "two or more VIGENTE documents share one decision key:\n" + "\n".join(lines)
        )

    # A successor must be a document the index knows, or the chain dead-ends outside the
    # register and the reader is back to grepping.
    known = {str(r["path"]) for r in records}
    for r in records:
        sb = r["superseded_by"]
        if sb and sb not in known:
            raise Broken(
                f"{r['path']}: superseded-by {sb} exists as a file but carries no decision "
                "front-matter, so the chain leaves the register. Annotate the successor."
            )
    return records


def ndjson_bytes(records: list[dict[str, object]]) -> bytes:
    """Byte-exact serialisation. sort_keys makes column order independent of insertion."""
    out = bytearray()
    for r in records:
        out += json.dumps(
            r, sort_keys=True, ensure_ascii=False, separators=(",", ":")
        ).encode("utf-8")
        out += b"\n"
    return bytes(out)


# --- FTS5 ---------------------------------------------------------------------------------

def fts5_available() -> bool:
    if sqlite3 is None:
        return False
    con = sqlite3.connect(":memory:")
    try:
        con.execute("CREATE VIRTUAL TABLE t USING fts5(a)")
        return True
    except sqlite3.OperationalError:
        return False
    finally:
        con.close()


def write_db(root: Path, db_path: Path, records: list[dict[str, object]],
             memory_rows: list[dict[str, str]]) -> None:
    """The LOCAL half: full text + the commit date. Never committed, rebuilt in seconds."""
    db_path.parent.mkdir(parents=True, exist_ok=True)
    if db_path.exists():
        db_path.unlink()
    con = sqlite3.connect(str(db_path))
    try:
        con.execute(
            f"CREATE VIRTUAL TABLE decisions USING fts5("
            f"path, decision, status, authority, decided, committed, superseded_by, "
            f"title, body, tokenize='{FTS_TOKENIZE}')"
        )
        con.execute(
            f"CREATE VIRTUAL TABLE memory USING fts5("
            f"path, name, container, project, description, body, "
            f"tokenize='{FTS_TOKENIZE}')"
        )
        for r in records:
            rel = str(r["path"])
            body = (root / rel).read_text("utf-8", "replace")
            con.execute(
                "INSERT INTO decisions VALUES (?,?,?,?,?,?,?,?,?)",
                (rel, r["decision"], r["status"], r["authority"], r["decided"],
                 commit_date(root, rel) or "", r["superseded_by"] or "", r["title"], body),
            )
        for m in memory_rows:
            con.execute(
                "INSERT INTO memory VALUES (?,?,?,?,?,?)",
                (m["path"], m["name"], m["container"], m["project"],
                 m["description"], m["body"]),
            )
        con.commit()
    finally:
        con.close()


# --- MEM-07: ingest of the auto-memory notes ----------------------------------------------
#
# INGESTING IS NOT REWRITING. These notes belong to other sessions and other lanes; the body
# goes into the index BYTE FOR BYTE, with no trimming, no case folding, no reflowing and no
# truncation. The only thing derived from them is metadata (name/description) read out of the
# front-matter Claude Code already writes; the note file itself is opened read-only and never
# touched. A "normalised" note is a note whose author can no longer recognise their own words.

def memory_dirs() -> list[Path]:
    base = Path.home() / ".claude" / "projects"
    if not base.is_dir():
        return []
    return sorted((d for d in base.glob("*/memory") if d.is_dir()),
                  key=lambda p: str(p).encode("utf-8"))


def read_memory(dirs: list[Path], container: str) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    for d in dirs:
        project = d.parent.name
        for f in sorted(d.glob("*.md"), key=lambda p: p.name.encode("utf-8")):
            raw = f.read_bytes()
            body = raw.decode("utf-8", "surrogateescape")  # verbatim, lossless round-trip
            name, description = "", ""
            text = body
            if text.startswith("---\n"):
                end = text.find("\n---", 3)
                if end != -1:
                    for line in text[4:end + 1].splitlines():
                        if line.startswith("name:"):
                            name = line[5:].strip()
                        elif line.startswith("description:"):
                            description = line[12:].strip().strip('"')
            rows.append({
                "path": str(f),
                "name": name or f.stem,
                "container": container,
                "project": project,
                "description": description,
                "body": body,
            })
    return rows


# --- commands -------------------------------------------------------------------------------

def cmd_build(root: Path, args: argparse.Namespace) -> int:
    records = build_records(root)
    payload = ndjson_bytes(records)
    (root / NDJSON_PATH).write_bytes(payload)

    total = len(enumerate_docs(root))
    print(f"build-decision-index: {len(records)} decision(s) of {total} {DOC_PATHSPEC} "
          f"-> {NDJSON_PATH}")

    if args.no_db:
        print("build-decision-index: FTS5 database skipped (--no-db)")
        return 0
    if not fts5_available():
        # The NDJSON is the half that does not depend on FTS5, and it is already written.
        why = ("the sqlite3 module is missing from this python3"
               if sqlite3 is None else "this sqlite3 was built WITHOUT FTS5")
        print(f"build-decision-index: {why}; the NDJSON was written and the local "
              "full-text database was NOT. Search is UNVERIFIED here; the register itself "
              "is intact.", file=sys.stderr)
        return 0

    # ⛔ MEMORY IS INGESTED BY DEFAULT, and that is a fix, not a preference. It used to be
    # opt-in via --with-memory, which meant a later plain `build` — the determinism check, say —
    # rewrote the database with ZERO notes and wiped the MEM-07 ingest without a word. The
    # notes then simply stopped being findable, which is indistinguishable from "there is no
    # such note": the silent-empty failure this whole register exists to refuse. Rebuilding
    # them costs about a second, so there is no reason to make forgetting possible.
    mem_rows: list[dict[str, str]] = []
    if not args.no_memory:
        dirs = memory_dirs()
        if not dirs:
            print("build-decision-index: NO memory directory under "
                  "~/.claude/projects/*/memory; no note was ingested (MEM-07 is UNVERIFIED "
                  "here, not empty).", file=sys.stderr)
        mem_rows = read_memory(dirs, args.container)
    write_db(root, Path(args.db), records, mem_rows)
    print(f"build-decision-index: FTS5 -> {args.db} "
          f"({len(records)} decision(s), {len(mem_rows)} memory note(s))")
    return 0


def cmd_check(root: Path, args: argparse.Namespace) -> int:
    """The gate. Rebuild from the tree, compare bytes, name every difference."""
    records = build_records(root)
    want = ndjson_bytes(records)
    target = root / NDJSON_PATH
    if not target.is_file():
        raise Broken(
            f"{NDJSON_PATH} does not exist but the tree holds {len(records)} annotated "
            "decision(s). Run: scripts/build-decision-index.sh"
        )
    have = target.read_bytes()
    if have == want:
        print(f"✓ decision index is current ({len(records)} decision(s))")
        return 0

    # Not just "differs" — WHICH decisions, by path. A gate that says only "regenerate" makes
    # the reader diff the artifact by hand and teaches them to regenerate blindly.
    def by_path(blob: bytes) -> dict[str, dict]:
        out = {}
        for i, line in enumerate(blob.decode("utf-8", "replace").splitlines(), start=1):
            if not line.strip():
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                raise Broken(
                    f"{NDJSON_PATH}:{i} is not valid JSON. The index is DERIVED and must "
                    "never be hand-edited; regenerate it."
                )
            out[str(rec.get("path", f"?line{i}"))] = rec
        return out

    old, new = by_path(have), by_path(want)
    reasons: list[str] = []
    for p in sorted(set(new) - set(old)):
        reasons.append(f"  + {p}: decision in the tree, ABSENT from the index (not regenerated)")
    for p in sorted(set(old) - set(new)):
        reasons.append(f"  - {p}: in the index, no longer an annotated decision in the tree")
    for p in sorted(set(old) & set(new)):
        if old[p] != new[p]:
            diffs = sorted(k for k in set(old[p]) | set(new[p])
                           if old[p].get(k) != new[p].get(k))
            for k in diffs:
                reasons.append(
                    f"  ~ {p}: {k} indexed as {old[p].get(k)!r}, tree says {new[p].get(k)!r}"
                )
    if not reasons:
        reasons.append("  ordering or serialisation differs; the file was hand-edited")
    raise Broken(
        "the decision index is STALE — the tree and " + NDJSON_PATH + " disagree:\n"
        + "\n".join(reasons)
        + "\n  The index is DERIVED: run scripts/build-decision-index.sh and commit it."
    )


def cmd_query(root: Path, args: argparse.Namespace) -> int:
    """Three answers, never two: VIGENTE / SUPERSEDED por X / NO HE PODIDO MIRAR."""
    if sqlite3 is None:
        print("NO HE PODIDO MIRAR: the sqlite3 module is missing from this python3, so "
              "there is no full-text index to consult. The NDJSON register is still "
              "readable by hand: " + NDJSON_PATH, file=sys.stderr)
        return 2
    db = Path(args.db)
    if not db.is_file():
        print("NO HE PODIDO MIRAR: no local index at " + str(db)
              + " — run scripts/build-decision-index.sh", file=sys.stderr)
        return 2
    con = sqlite3.connect(str(db))
    try:
        # ⛔ The user's words are DATA, not FTS5 syntax. Passed raw, `front-matter` made sqlite
        # answer `no such column: matter` — a syntax error that a caller reading only the exit
        # code would file under "no decision found". Every token becomes a quoted literal
        # phrase, ANDed: hyphens, colons, accents and stray punctuation stop being operators.
        toks = [t for t in re.split(r"\s+", args.text.strip()) if t]
        if not toks:
            print("NO HE PODIDO MIRAR: empty query", file=sys.stderr)
            return 2
        match = " AND ".join('"' + t.replace('"', '""') + '"' for t in toks)
        table = "memory" if args.memory else "decisions"
        cols = ("path, name, description" if args.memory
                else "path, decision, status, superseded_by, decided, committed")
        try:
            rows = con.execute(
                f"SELECT {cols} FROM {table} WHERE {table} MATCH ? "
                f"ORDER BY rank LIMIT ?", (match, args.limit)
            ).fetchall()
        except sqlite3.OperationalError as exc:
            print(f"NO HE PODIDO MIRAR: {exc}", file=sys.stderr)
            return 2
    finally:
        con.close()
    if not rows:
        # An empty result is never "there is no decision" — the index may simply be blind to
        # the wording. FTS5 is lexical; that limit is FASE 1's, restated where it bites.
        print("NO HE PODIDO MIRAR: no lexical match for " + repr(args.text)
              + " (FTS5 is lexical; a paraphrase without the technical term will miss)",
              file=sys.stderr)
        return 2
    for row in rows:
        if args.memory:
            print(f"{row[0]}\t{row[1]}\t{row[2]}")
        else:
            verdict = "VIGENTE" if row[2] == "vigente" else (
                f"SUPERSEDED por {row[3]}" if row[2] == "superseded" else row[2].upper())
            print(f"{row[0]}\t{row[1]}\t{verdict}\tdecided={row[4]}\tcommitted={row[5]}")
    return 0


def cmd_ingest_memory(root: Path, args: argparse.Namespace) -> int:
    """MEM-07 alone: refresh the memory half of the local index without rebuilding decisions."""
    if not fts5_available():
        raise Unverified(
            "FTS5 is not available in this python3, so ingested notes would have nowhere "
            "searchable to land; the ingest is UNVERIFIED (the NDJSON register is "
            "unaffected — it never carried note bodies)"
        )
    dirs = memory_dirs()
    if not dirs:
        raise Unverified(
            "no memory directory under ~/.claude/projects/*/memory; the ingest is "
            "UNVERIFIED (this is the state of a container whose notes live elsewhere, not "
            "evidence that there are none)"
        )
    rows = read_memory(dirs, args.container)
    if not rows:
        raise Unverified(
            f"{len(dirs)} memory director(y/ies) found and ZERO notes read; UNVERIFIED"
        )
    records = build_records(root)
    write_db(root, Path(args.db), records, rows)
    per_dir = ", ".join(f"{d.parent.name}={len(list(d.glob('*.md')))}" for d in dirs)
    print(f"build-decision-index: ingested {len(rows)} memory note(s) from "
          f"{len(dirs)} director(y/ies) of container {args.container} [{per_dir}] "
          f"-> {args.db} (verbatim; sources opened read-only)")
    return 0


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="build-decision-index.py")
    ap.add_argument("mode", choices=("build", "check", "query", "ingest-memory"))
    ap.add_argument("text", nargs="?", default="", help="query text (mode=query)")
    ap.add_argument("--root", default=".")
    ap.add_argument("--db", default=os.environ.get(
        "OLIVARES_DECISION_DB",
        str(Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache")))
            / "olivares-ai" / "decision-index.sqlite3")))
    ap.add_argument("--with-memory", action="store_true",
                    help="accepted and now the DEFAULT; kept so existing callers keep working")
    ap.add_argument("--no-memory", action="store_true",
                    help="build the FTS5 index WITHOUT the memory notes (MEM-07 opt-out)")
    ap.add_argument("--no-db", action="store_true", help="NDJSON only, no FTS5")
    ap.add_argument("--memory", action="store_true", help="query the memory table")
    ap.add_argument("--limit", type=int, default=5)
    ap.add_argument("--container", default=os.environ.get("OLIVARES_CONTAINER", os.uname().nodename))
    # parse_intermixed_args, not parse_args: `query --memory "texto"` puts a flag between the
    # mode and the query text, and plain parse_args rejects the text as an unrecognised
    # argument. Measured against the self-test, which is where a CLI that only its author can
    # drive stops being usable by the hooks MEM-04 will hang off it.
    args = ap.parse_intermixed_args(argv)

    root = Path(args.root).resolve()
    try:
        if args.mode == "build":
            return cmd_build(root, args)
        if args.mode == "check":
            return cmd_check(root, args)
        if args.mode == "query":
            return cmd_query(root, args)
        return cmd_ingest_memory(root, args)
    except Broken as exc:
        print(f"✗ build-decision-index: {exc}", file=sys.stderr)
        return 1
    except CuratedAbsence as exc:
        # 0, but it SAYS what it did not look at: a scoped verdict is not a clean one.
        print(f"build-decision-index: SCOPED — {exc}", file=sys.stderr)
        return 0
    except Unverified as exc:
        print(f"build-decision-index: {exc}", file=sys.stderr)
        print("build-decision-index: UNVERIFIED (exit 2) — not knowing is not the same as "
              "being clean.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
