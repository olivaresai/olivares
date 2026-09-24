#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-release-version.sh — one release version, stated everywhere or nowhere.
#
# The public surfaces disagreed for weeks (README ×7 promised v26.7.0, CHANGELOG said
# v26.6.0, a launch draft carried v0.1.0) and no job noticed. The version CHOICE is the
# owner's; this gate owns the MECHANICS: RELEASE-VERSION at the repo root is the single
# source of truth, and every product-version-shaped token (CalVer vYY.M.PATCH, YY >= 26)
# on a release-bearing surface must equal it — or its one derived form: the -fips / -stig
# image variants, where image tags are documented. The next-patch upgrade example is gone
# (2026-09-23): the canon is the published baseline, and its same-month patch names a release
# nobody cut, so no document may state it as an example.
#
# RELEASE-VERSION always holds a version: the current published install baseline. The report
# mode this gate once had for UNDECIDED (print the census, exit 0 with a PENDING banner) was
# removed on 2026-09-23, because it was a green with no canon behind it: UNDECIDED, or any
# record that is not vYY.M.PATCH, is refused before any surface is read, and every judge
# refuses it again. A divergent surface is red, and the FAIL listing is the census.
#
# Exemptions, always explicit and context-bound: the -fips/-stig variants are allowed ONLY
# at the named doc paths (DERIVED_ALLOW);
# dated ADRs and the frozen 2026-06 docs snapshot are out of scope by directory. There is
# no line-level waiver: a second opinion measured a general marker-line skip hiding a
# foreign version, and nothing that legitimately needs a waiver matches the CalVer shape.
#
# ── DOCUMENTATION SCOPE: docs/ IS WALKED, NOT LISTED (added 2026-08-14) ───────────────
# The documentary census enumerated its members by hand — CHANGELOG, the seven READMEs,
# docs/trust/*.md, docs/launch/*.md, the docs-site tree and INSTALL.md — and TOP-LEVEL
# docs/*.md was in none of them. The artifact roots below do not reach it either, so a
# whole class of release-bearing prose was scanned by NEITHER arm. Measured on origin/main
# with the canon already at v26.8.0 and this gate printing OK: 46 stale tokens in 7 files,
# among them the runbooks that CUT THE TAG (`git tag -s v26.7.0`) and the upgrade guide a
# customer follows. A stale number in a runbook is not cosmetic — it is an instruction.
#
# The fix is the same shape as the artifact one, for the same reason: docs/ is WALKED
# whole (binaries skipped by the same reader), never listed. Listing the two files that
# happened to be reported would have rebuilt the precise blind spot this closes — the
# eighth declarant would be born invisible. Two directory prunes carry over the rule this
# gate already applied to the docs-site tree: dated ADRs and frozen snapshots (adr/,
# archive/, 2026-06) are historical records, and sweeping a record to the canon falsifies
# it. An empty or shrunken walk is UNVERIFIED + exit 2 (DOCS_LAST_GOOD), never green.
#
# The floor rule below is NOT artifact-specific and is applied here too: docs/HA-LEADER-
# ROUTING.md states `spec.image` must be `≥ 26.7.0`, which is a compatibility bound, not a
# claim about what ships. Widening the census without carrying the floor rule across would
# have swept that bound to the canon and silently narrowed the supported range — the exact
# defect this gate was preferred for avoiding. So a floor stays a floor in prose too, and
# is still checked against the invariant that matters: it may never EXCEED the canon.
#
# ── ARTIFACT SURFACES (added 2026-08-14) ──────────────────────────────────────────────
# The gate above scans DOCUMENTATION and nothing else, and that scope was never written
# down as a decision — it was inherited from the bug that prompted it (README ×7 vs
# CHANGELOG). The cost was measured: with the canon at v26.8.0 this script printed OK
# while deploy/, packaging/ and operator/ shipped 26.7.0 in 15 places. Those are not
# prose. They are the coordinates a customer's cluster actually resolves:
#
#   deploy/helm/olivares/Chart.yaml appVersion -> templates/_helpers.tpl:71
#       `default .Chart.AppVersion .Values.image.tag` — the tag a default `helm install`
#       pulls, and (via _helpers.tpl:47) every app.kubernetes.io/version label.
#   deploy/manifests/install.yaml — the Helm-free `kubectl apply -f` path, GENERATED from
#       the chart by scripts/gen-install-manifest.sh, so it inherits the chart's value.
#   packaging/docker/dockerhub-overview.md — uploaded VERBATIM as the Docker Hub
#       repository description by .github/workflows/release.yml (the `full_description`
#       PATCH). The same job mirrors ${ver}, ${ver}-fips and ${ver}-stig, so a stale page
#       tells every visitor to pull a tag that release never pushed.
#
# Why a WALK and not a list: a fixed list of six files would rebuild the exact blind spot
# this closes — a seventh declarant would be born invisible. The three roots are walked
# whole, binaries skipped, and an empty enumeration is UNVERIFIED + exit 2 (the
# check-migrations.sh contract: zero units is COULD NOT LOOK, never "nothing to do").
#
# Two artifact exemptions, same discipline as DERIVED_ALLOW — exact path, named kind,
# written reason:
#   variants    — the -fips/-stig tag forms, only where image tags are documented.
#   min-version — a COMPATIBILITY FLOOR ("olivares >= 26.7.0"). A floor is not a claim
#       about what ships and must NOT be swept to the canon: doing so would silently
#       narrow the supported range to the newest release every time one is cut. It is
#       still checked, against the invariant that actually matters — a floor may never
#       exceed the canon, because you cannot require an engine newer than the one you
#       ship. Allowed only where the text states the bound IMMEDIATELY before the token
#       (`>=`, `≥`, `--min-version`, an OSV `introduced` key); anywhere else in the same
#       file the canon is required exactly. That is a NARROWER allowance granted by
#       context, not the general line-skip the paragraph above refuses — and it is scoped
#       to the TOKEN, not the line: an advisory range writes `introduced` and `fixed` on
#       one line, and a line-scoped test was measured handing the floor's amnesty to the
#       `fixed` value, i.e. passing a stale shipped version off as a bound.
#
# One of those floors sits in the operator's CRD API types, whose filename is the CRD Kind
# — the pre-rename product word. This script ships, and the export leak gate refuses that
# word outside operator/, so that path is DISCOVERED from the operator's API package
# (CRD_TYPES_GLOB) rather than written here. Same allowance, same narrowness, no literal;
# an empty discovery is UNVERIFIED + exit 2, so the exemption cannot be lost in silence.
#
# ── TWO CLASSES THAT ARE NOT RELEASE-BEARING SURFACES (added 2026-09-05) ──────────────────
# Measured on the post-console preflight with the canon at v26.8.0 and the mandate at v26.9.0:
# the gate went red on docs/ai-context/CURRENT-MANDATE.md ("v26.9.0 as the delivery
# objective") and on cmd/olivares/cmd_upgrade_releasev1_bridge_test.go ("olivares_26.9.0",
# the object key a multi-version upgrade regression must NOT be able to fetch with a stale
# token). Neither is a claim about what ships, and both are exactly what they look like:
#
#   internal development context — docs/ai-context/ is the AI-agent operating context: the
#       mandate that names the NEXT target, the working state, the session rules. It is curated
#       OUT of every export (export-public.sh DOCS_BLOCK), so no customer reads it, and a
#       version there is a target, never an instruction. The prune is NOT taken on this file's
#       word: it holds only while the export curation, READ from the export script, lists that
#       exact path. If the curation stops removing it, the prune stops with it and every token
#       inside is judged again (fail-closed toward scanning). Selftest: INTERNAL_CONTEXT.
#   Go test fixtures — a `_test.go` file never enters a non-test build (the Go toolchain's own
#       rule), so a version literal there is test data. Production Go stays in arm B's walk:
#       a pin in any other .go file is still a shipped coordinate. Selftest: go_test_fixture.
#
# Both are the narrowest class that names the case, not a skip of docs/ or of source: a
# docs/*.md runbook, a deploy manifest and a non-test .go pin at the wrong version stay red,
# and the battery keeps those as its positive controls beside the two exemptions.
#
# ── TWO PROFILES, ONE BATTERY (added 2026-09-06) ──────────────────────────────────────────
# This script SHIPS and the export script does not, and the battery below used to demand the
# full source tree's shape unconditionally: TOP_ALLOW read from the export script, DOCS_BLOCK
# read from it,
# the internal context present on disk so the prune could be seen removing it. Measured on a
# real export of this tree, from the export: six assertions red and the published
# `task lint:release-version` red with them. A gate that can only pass where the export script
# lives fails everywhere the product is read. The battery now asks scripts/hub-leg.sh
# --classify which tree it is in — the sentence the export stamps into its marker AND the
# absence of every hub-only path; not an environment variable, not the absence of one file —
# and proves the facts each tree can prove:
#   hub     — the census and the curation are READ from the export script; the internal
#             context is on disk, and the walk keeps it out only while the curation says so.
#   public  — the export script is absent, so the census is the tree itself and the verdict
#             names it; the fallback reaches every artefact root and walks to the shipped pins;
#             no internal-context directory exists; the walk is identical with and without a
#             curation. Real evidence of the exported profile, never a skipped assertion. And
#             the docs enumeration floor is the PUBLIC population's own record: the export's
#             curation makes docs/ smaller by design (DOCS_LAST_GOOD_PUBLIC, measured there).
#   unknown — refused, by the battery AND by the run (require_known_profile): neither
#             profile's facts can be established, so nothing is certified, whatever the volume.
# The parser controls (TOP_ALLOW and DOCS_BLOCK shapes, comments, emptiness, unreadability) run
# in BOTH profiles on an in-memory export script, so the public tree does not lose the full source tree's
# coverage of the parsers. One verdict changed with this: a PRESENT but unreadable export
# script sat in `except OSError`, the same branch as absent — in the full source tree that enumerated the
# private tree and called its private documents divergent. Unreadable is COULD NOT LOOK:
# UNVERIFIED, exit 2. Selftest: `unreadable`, and the `public export:` cases.
#
# ── ENUMERATION AND READ FAILURES ARE COULD NOT LOOK, NEVER A SMALLER CENSUS (2026-09-06) ────
# Measured by independent review on a real export, and reproduced: a stale token appended to
# docs/integrations/grok-guide.md turned the run red (exit 1, the file named); the SAME bytes with
# the containing directory at mode 000 turned the whole task GREEN. os.walk without `onerror`
# skips a directory it cannot list, the six documents inside left the census without a word, and
# the 78 that remained cleared the floor. The same shape was then found in every other enumerator
# of this file: the docs-site walk, the deploy/packaging/examples/oscap walk, the artifact walk and
# the pin census all skipped an unlistable directory in silence; `os.path.isfile()` answers False
# to a directory that is listable but not searchable (mode 444), so those entries vanished too;
# and a listed file that could not be OPENED was either skipped (the pin census) or a Python
# traceback with exit 1 — the code of "I looked and it diverges".
#
# A floor is a coarse VOLUME alarm. It cannot say the walk saw everything, and it cannot be made
# to say so by raising it. So every walk and every read in this file now goes through walk(),
# regular_file() and read_surface(), which REFUSE: an unlistable directory, an entry that cannot
# be examined, a file that cannot be read, or a census source that cannot be decoded is
# UNVERIFIED + exit 2, naming the path. A root that does not exist at all is still "nothing to
# walk" (a partial tree may lack oscap/ or go.work.sum): only what exists and cannot be looked at
# refuses. And the run, not just the battery, refuses a tree of unknown profile and a tree whose
# census source contradicts its profile — the battery already did, and a run that certified what
# the battery refused was measured printing OK over 367 documents with the classifier missing.
# Selftest: `walk:`, `read_surface:`, `regular_file:`, the wiring witness, and `run:`.
#
# And the ROOT ENTRIES (R1-TOP, same review, one round later): the three helpers refuse what
# reaches them, but README.md, CHANGELOG.md and INSTALL.md were admitted to surface_files() by
# os.path.isfile(), and every top-level census entry to scan_pins() by isfile()/isdir() — both
# answer False to a permission error and to a dangling link, so a README that was THERE, as a link
# whose target sat behind a directory at mode 000 (or had been moved away), vanished from both
# censuses before any helper saw it, and the task stayed green over the stale token the readable
# target had just been rejected for. root_entry() now tells the two apart: no directory entry at
# all is "absent" and keeps the optional-surface semantics (INSTALL.md, go.work.sum may not exist);
# an entry that exists and cannot be examined — dangling, unsearchable target, any stat error — is
# UNVERIFIED + exit 2 naming it. Selftest: `root_entry:`, and the staged `surface_files:` /
# `scan_pins:` cases, which exercise the real surfaces from a staged working directory.
#
# Usage: check-release-version.sh [--selftest]
set -eu
cd "$(dirname "$0")/.."

CRV_SELFTEST=0
[ "${1:-}" = "--selftest" ] && CRV_SELFTEST=1
export CRV_SELFTEST

python3 - <<'PY'
import glob, io, os, re, shutil, stat, subprocess, sys, tempfile

SELFTEST = os.environ.get("CRV_SELFTEST") == "1"


def unverified(msg):
    """La TERCERA respuesta, con su propio código de salida.

    ⛔ Este gate ya decía UNVERIFIED, pero salía con `sys.exit(<str>)`, que es **1** — el mismo
    código que «he mirado y está roto». Es decir, decía la palabra correcta y devolvía el
    veredicto equivocado, que es peor que no decirla: quien automatiza sobre el código no puede
    distinguir «la cifra diverge» de «no pude leer el canon». Medido el 2026-08-16 por el censo
    de veredictos ciegos, que lo clasificó RECHAZA-1 junto a los defectos reales.

    Sigue bloqueando el push: 2 no es 0. Lo que cambia es que ahora se puede saber POR QUÉ.
    """
    print(msg, file=sys.stderr)
    sys.exit(2)


# ── The three helpers every enumerator and reader goes through (see header) ────────────────────
def enumeration_failed(exc):
    """os.walk's `onerror`, and the answer for a root that exists but cannot be examined: the
    directory could not be listed, so nothing below it was seen. Never continue past it."""
    unverified(f"UNVERIFIED check-release-version: COULD NOT LOOK — enumeration failed at "
               f"{exc.filename!r} ({exc.strerror or exc}); a walk that skips a directory certifies "
               "nothing about what it skipped.")


def walk(root):
    """-> os.walk(root) that REFUSES instead of skipping.

    A root that does not exist yields nothing — a partial tree may legitimately lack it, and the
    floors already say when a walk came back too small. A root that exists but cannot be examined
    (an unsearchable parent), or any directory under it that cannot be listed, is UNVERIFIED."""
    try:
        os.lstat(root)
    except FileNotFoundError:
        return iter(())
    except OSError as exc:
        enumeration_failed(exc)
    return os.walk(root, onerror=enumeration_failed)


def regular_file(path):
    """-> True iff `path` is a regular file. An entry the walk LISTED but cannot examine (a
    directory listable but not searchable, a dangling link) is UNVERIFIED — os.path.isfile()
    answers False to a permission error, and False here is a silent drop from the census."""
    try:
        return stat.S_ISREG(os.stat(path).st_mode)
    except OSError as exc:
        unverified(f"UNVERIFIED check-release-version: COULD NOT LOOK — {path!r} was enumerated "
                   f"but cannot be examined ({exc.strerror or exc}).")


def read_surface(path):
    """-> the text of a walked file, or '' for a binary (a NUL in the first 8 KiB).

    Both walks enumerate whole trees, so binaries are skipped by CONTENT, never by name. And a
    file the walk listed but this reader cannot open is UNVERIFIED — never a file with no tokens
    in it (the pin census used to `continue`), never a traceback with exit 1 (the docs reader
    used to raise), because neither of those is "I looked"."""
    try:
        with open(path, "rb") as fh:
            raw = fh.read()
    except OSError as exc:
        unverified(f"UNVERIFIED check-release-version: COULD NOT LOOK — {path!r} was enumerated "
                   f"but cannot be read ({exc.strerror or exc}); an unread surface is not a clean one.")
    if b"\0" in raw[:8192]:
        return ""
    return raw.decode("utf-8", errors="replace")


def root_entry(name):
    """-> "absent" | "file" | "dir" | "other" for a root-level entry, REFUSING what is present but
    cannot be examined.

    "absent" is only the total absence of a directory entry (lstat says ENOENT): that keeps the
    optional-surface semantics this file always had. An entry that IS there — a regular file, a
    directory, or a link — must be examinable; a link whose target is gone or unreachable, or any
    other stat error, is COULD NOT LOOK, never a surface that happens not to exist. This is what
    os.path.isfile()/isdir() cannot say: they answer False to both, and False was a silent drop."""
    try:
        os.lstat(name)
    except FileNotFoundError:
        return "absent"
    except OSError as exc:
        unverified(f"UNVERIFIED check-release-version: COULD NOT LOOK — root entry {name!r} cannot "
                   f"be examined ({exc.strerror or exc}).")
    try:
        st = os.stat(name)
    except OSError as exc:
        unverified(f"UNVERIFIED check-release-version: COULD NOT LOOK — root entry {name!r} is present "
                   f"but its target cannot be examined ({exc.strerror or exc}); a surface that is there "
                   "and cannot be looked at is not a surface that is absent.")
    if stat.S_ISREG(st.st_mode):
        return "file"
    if stat.S_ISDIR(st.st_mode):
        return "dir"
    return "other"


# CalVer product versions: vYY.M.PATCH with YY>=26 — dependency versions (go1.26.5,
# chi v5.3.1, node 24) do not match this shape.
VERSION = re.compile(r"\bv?(2[6-9]|[3-9][0-9])\.([1-9]|1[0-2])\.([0-9]+)(-fips|-stig)?\b")

# Exact allowlist records: (path suffix, exemption kind, reason). Kinds:
#   variants   — the -fips/-stig image-tag forms of the exact canon
# (A second kind, next-patch, admitted the canon's next patch as an upgrade example. It was
# removed on 2026-09-23: with the canon at the published v26.9.0 it admitted a v26.9.1 that
# nobody cut. An example of the next release names it in its two-part form or a placeholder.)
# Nothing else is ever exempt; there is no line-level waiver (a readiness marker or an
# illustrative SemVer example never matches the CalVer shape, so neither needs one — and
# a general line-skip was measured hiding a foreign version on a marker line).
DERIVED_ALLOW = [
    # INSTALL.md documents the SAME thing its allowlisted siblings do — the FIPS/STIG image
    # tags — and was simply never listed. It only surfaced when a canon was finally set: while
    # RELEASE-VERSION said UNDECIDED the gate printed a census instead of judging, so the
    # omission could not show.
    ("INSTALL.md", "variants", "the FIPS/STIG image tags the install guide documents"),
    ("how-to/docker-deployment.md", "variants", "image tags for the FIPS/STIG builds"),
    ("how-to/air-gap-install.md", "variants", "air-gap bundle filenames for the hardened builds"),
    # The Docker Hub overview IS the artefact matrix — the same class as its two siblings above,
    # and it never surfaced because `packaging/` was outside the census entirely (see below).
    ("packaging/docker/dockerhub-overview.md", "variants", "the published FIPS/STIG image tags"),
    # Top-level docs/, reachable only since docs/ became a walk. Same discipline as every
    # record above: exact path, named kind, written reason — the CENSUS is derived, the
    # EXEMPTIONS are exact, so a doc nobody exempted is required to state the canon.
    ("docs/UPGRADE-AND-ROLLBACK.md", "variants",
     "the :TAG, :TAG-fips and :TAG-stig image coordinates the upgrade guide documents"),
    ("docs/HA-LEADER-ROUTING.md", "min-version",
     "the /pod-readyz precondition is a compatibility FLOOR, not a shipped coordinate"),
    ("docs/PSIRT-RUNBOOK.md", "min-version",
     "the advisory's affected-range START (--min-version / introduced), a bound not a claim"),
    # The release-key ledger's first column is the FIRST release a signing pair covers, which is
    # a lower bound and stays true across cuts that do not rotate the keys. Every other version
    # token in that file is a live command or the tag contract, and those are required to state
    # the canon exactly — the allowance is scoped to the TOKEN on the bound, as everywhere else.
    ("docs/RELEASE-VERIFICATION.md", "min-version",
     "the key ledger's first column is the first release a signing pair covers, a coverage FLOOR"),
]

# ── ARM B: an artefact PIN, anywhere the export publishes ──────────────────────────────────
# A pin is a version a user COPIES: a container image tag or a bundle filename. It is not
# prose. That distinction is the whole point, and it is measurable:
#
#   operator/config/samples/….yaml:22   image: …/olivares:26.7.0   ← a PIN. Stale, and copied.
#   operator/api/v1alpha1/…_types.go:55 (olivares >= 26.7.0)       ← a compatibility FLOOR:
#   operator/README.md:78               (olivares ≥ 26.7.0)          correct, and must stay.
#
# A floor states WHEN a feature appeared; rewriting it to the current version would falsify
# history, and this gate has no line-level waiver by design. So arm B judges only pins, and can
# therefore afford to walk EVERY published tree — including Go source, where the bare CalVer
# shape collides with test fixtures and third-party versions (measured 2026-08-13: 124 shaped
# tokens in core/, 980 in connectors/, one of them the date 27.12.2022).
PIN = re.compile(
    r"(?:olivares(?:ai)?/olivares:|olivares[-_])v?(2[6-9]|[3-9][0-9])\.([1-9]|1[0-2])\.([0-9]+)(-fips|-stig)?\b"
)

SKIP_DIRS = {"node_modules", ".git", "vendor", "dist", ".astro"}
TEXTY = (".md", ".mdx", ".yaml", ".yml", ".json", ".txt", ".sh", ".go", ".ts", ".tsx", ".toml", ".tf")

# export-closure: absent-by-design scripts/export-public.sh — this gate ships and the export
# script does not: it carries the private-token denylist verbatim and would trip the export's own
# leak gate. The path is READ to derive the published census, never run, and its absence is not a
# fallback that guesses — in a tree produced BY the export it is that tree's correct census.
EXPORT_SCRIPT = "scripts/export-public.sh"


def read_export_script(read=None):
    """-> the export script's text, or None when it is ABSENT — a tree produced BY the export.

    Absent is the one sanctioned fallback, and only absent: FileNotFoundError. Present but
    unreadable — permissions, a directory in its place, an I/O error — is COULD NOT LOOK, and the
    answer is UNVERIFIED with exit 2. Measured 2026-09-06 with the file present at mode 000: the
    old `except OSError` took the public fallback INSIDE THE HUB, enumerated the private
    directories the export removes and reported their documents as divergent — the wrong verdict
    class, with the wrong files in it. Both readers below share this so they cannot disagree."""
    read = read or (lambda p: open(p, encoding="utf-8").read())
    try:
        return read(EXPORT_SCRIPT)
    except FileNotFoundError:
        return None
    except OSError as exc:
        unverified(f"UNVERIFIED check-release-version: {EXPORT_SCRIPT} is present but unreadable "
                   f"({exc}); neither the published census nor the docs curation can be read.")
    except UnicodeDecodeError as exc:
        # Present, readable, and not text: the same class as unreadable, and it used to be a
        # Python traceback with exit 1 (measured by independent review, on the base as well).
        unverified(f"UNVERIFIED check-release-version: {EXPORT_SCRIPT} is present but not "
                   f"decodable as UTF-8 ({exc}); neither the published census nor the docs "
                   "curation can be read.")


def published_tops(read=None):
    """-> (tops, census_source). The top-level entries the export publishes, READ FROM THE EXPORT.

    Two declarations of one fact, tied together: add a published directory to export-public.sh
    and arm B walks it on the next run without anyone remembering this file.

    ⛔ THIS GATE SHIPS AND THE EXPORT SCRIPT DOES NOT — measured on the export's own output: the
    public tree gets scripts/check-release-version.sh and NOT scripts/export-public.sh, which
    encodes what stays private. A census that simply refused when the file was missing would
    hand every public clone a permanently UNVERIFIED gate.

    So the absent case is not a fallback that guesses — it is the OTHER tree's correct census:
    in a tree produced BY the export, everything present is published, by definition. It is
    named in the verdict, never silent. Present-but-unparseable stays UNVERIFIED, because there
    the shape changed under us and neither census can be trusted.

    Direction of failure, stated: the tree census is WIDER than the allowlist (measured on this
    repo, it adds exactly one non-published hit), so losing the allowlist over-reports. Never
    under-reports."""
    src = read_export_script(read)
    if src is None:
        tops = sorted(e for e in os.listdir(".") if not e.startswith(".") and e not in SKIP_DIRS)
        return tops, f"the tree itself ({EXPORT_SCRIPT} is absent, as it is in a public export)"
    m = re.search(r"^TOP_ALLOW=\(\n(.*?)^\)", src, re.S | re.M)
    if not m:
        unverified(f"UNVERIFIED check-release-version: TOP_ALLOW not found in {EXPORT_SCRIPT} "
                 "(it changed shape); arm B cannot know what ships.")
    tops = []
    for ln in m.group(1).splitlines():
        ln = ln.split("#", 1)[0].strip()
        tops += ln.split()
    if not tops:
        unverified("UNVERIFIED check-release-version: TOP_ALLOW parsed empty; refusing to certify.")
    return tops, f"{EXPORT_SCRIPT} TOP_ALLOW"

def curated_out(read=None):
    """-> the docs/ paths export-public.sh removes (DOCS_BLOCK), as a set.

    Same discipline as published_tops: READ from the export, never written here. Absent script
    (a public clone) -> empty set, which means NO internal-context prune: in a tree produced by
    the export the pruned directories cannot exist, and if one does it is judged. Present but of
    an unknown shape -> empty set too: a curation that cannot be parsed grants nothing. Present
    but UNREADABLE refuses (read_export_script): the same answer the census gives."""
    src = read_export_script(read)
    if src is None:
        return set()
    m = re.search(r"^DOCS_BLOCK=\(\n(.*?)^\)", src, re.S | re.M)
    if not m:
        return set()
    out = set()
    for ln in m.group(1).splitlines():
        ln = ln.split("#", 1)[0].strip()
        out.update(ln.split())
    return out


def curation_kept(read=None):
    """-> the docs/ paths export-public.sh re-publishes OUT of a blocked directory (DOCS_KEEP).

    ⛔ DOCS_BLOCK ALONE IS NOT THE CURATION. The export blocks a directory and then names files
    inside it that ship anyway — measured: four under docs/contracts/. A subtree allowance that
    reads DOCS_BLOCK and stops covers those four, which is the hole an independent review found
    on 2026-09-15. The answer is not to throw the whole directory away either (that discards a
    legitimate tie and forces the members to be spelled file by file, which put an internal
    session identifier into a script that SHIPS and turned lint:export red). It is subtree MINUS
    kept: the directory stays curated, and a kept file inside it earns nothing."""
    src = read_export_script(read)
    if src is None:
        return set()
    m = re.search(r"^DOCS_KEEP=\(\n(.*?)^\)", src, re.S | re.M)
    if not m:
        return set()
    out = set()
    for ln in m.group(1).splitlines():
        ln = ln.split("#", 1)[0].strip()
        out.update(ln.split())
    return out


# Which tree this is, decided by the classifier the Taskfile already trusts for hub-only legs:
# scripts/hub-leg.sh --classify answers hub | public | unknown from the sentence the export stamps
# into its marker AND the absence of every hub-only path. Not an environment variable, and not the
# absence of the export script alone — a hub that lost that one file classifies as hub and its
# battery stays red, instead of quietly passing the other profile's assertions.
HUB_LEG = "scripts/hub-leg.sh"


def tree_profile():
    """-> (profile, why): ("hub" | "public" | "unknown", the classifier's own reason).

    Anything that is not a clean `hub` or `public` from the classifier — the script missing, a
    non-zero exit, an unexpected word — is `unknown`, and the battery refuses on it."""
    try:
        run = subprocess.run(["bash", HUB_LEG, "--classify"], capture_output=True, text=True,
                             check=False)
    except OSError as exc:
        return "unknown", f"{HUB_LEG} could not be run ({exc})"
    verdict = run.stdout.strip()
    why = run.stderr.strip()
    if why.startswith("hub-leg: "):
        why = why[len("hub-leg: "):]
    if run.returncode != 0 or verdict not in ("hub", "public"):
        return "unknown", why or f"{HUB_LEG} --classify exited {run.returncode} saying {verdict!r}"
    return verdict, why


def require_known_profile(profile, why):
    """The run certifies a hub or a stamped public export and NOTHING ELSE.

    Measured 2026-09-06 by independent review: the battery refused `unknown`, but the plain run —
    classifier missing, answering `unknown`, or answering `public` with a non-zero exit — printed
    OK over a hub copy of 367 documents, because all `unknown` changed was which floor applied and
    the population cleared it. A population above a floor is not a profile."""
    if profile not in ("hub", "public"):
        unverified(f"UNVERIFIED check-release-version: this tree classifies as {profile} ({why}); "
                   "neither the full source tree's census nor the export's can be certified for a tree that is "
                   "neither, whatever its volume.")


def require_census_matches(profile, census_src):
    """A hub reads its census from the export script; a stamped export has no export script and
    enumerates itself. The other two pairings describe a tree this file does not: a hub that lost
    its export script, or an export that grew one. The battery already refuses both (the full source tree
    assertion on TOP_ALLOW, the public one on its absence); the run must not certify what the
    battery refuses."""
    from_script = "TOP_ALLOW" in census_src
    if (profile == "hub") != from_script:
        unverified(f"UNVERIFIED check-release-version: this tree classifies as {profile} but its "
                   f"published census came from {census_src}; the classification and the census "
                   "disagree, so neither is trusted.")


# Internal development context: exact directory, named kind, written reason — and honoured
# ONLY when the export curation confirms the path never ships (curated_out).
INTERNAL_CONTEXT = [
    ("docs/ai-context", "AI-agent operating context: the mandate names the NEXT target; curated out of every export"),
]


def internal_context(path, curated):
    """-> True iff `path` lies under an INTERNAL_CONTEXT directory that the export curation
    (`curated`, from curated_out) actually removes. Exact directory, never a substring:
    docs/ai-context-notes/ is not docs/ai-context/."""
    for d, _ in INTERNAL_CONTEXT:
        if d in curated and (path == d or path.startswith(d + "/")):
            return True
    return False


def go_test_fixture(path):
    """-> True iff Go's own build rule keeps this file out of every non-test binary."""
    return path.endswith("_test.go")


# ── THE DATED RECORD OF A RELEASE THAT ALREADY HAPPENED (added 2026-09-15) ────────────────
# Measured on the v26.9.0 cut, with RELEASE-VERSION re-derived and nothing else changed: 777
# shaped tokens turned red, and 383 of them were in documents whose entire subject is a release
# that SHIPPED. The `[26.8.0]` changelog section. The dated install-surface witness the sibling
# gate reads. The launch pack that was posted to five platforms on 2026-09-01. The runbooks that
# cut the tag and the audits that measured the result.
#
# Sweeping those to the new canon does not correct a claim — it FALSIFIES A RECORD, which is the
# one thing this file has refused since the dated-ADR prune: "sweeping a record to the canon
# falsifies it". So they are admitted, and the admission is written the way this file writes
# every other one: EXACT path or EXACT directory, a named kind, a written reason, and an
# invariant narrow enough that the allowance cannot be reached by anything else.
#
# THE INVARIANT IS "STRICTLY BELOW THE CANON", and it is the whole safety of the rule. A record
# is of the PAST. The canon itself needs no allowance (every rule above already admits it), and a
# token ABOVE the canon inside a record names a release nobody cut — the same falsehood the
# min-version rule refuses when a floor exceeds what ships. So a record may state 26.8.0 under a
# 26.9.0 canon and may not state 26.10.0, and no live surface gains anything at all: a stray
# 26.8.0 in README.md, INSTALL.md or a docs-site install page is red exactly as before.
#
# WHY NOT A PRUNE. DATED_PRUNE removes a directory from the census, so nothing inside is judged
# ever again. That is right for adr/ and archive/, which are closed by construction. It is wrong
# here: docs/launch/ and the release runbooks are WRITTEN IN, and a future launch pack promising
# a version that does not exist must still be caught. An allowance keeps them in the census and
# judges every token that is not a past release.
#
# THE CHANGELOG IS NARROWER THAN THE REST, and that is not a detail — it is this gate's founding
# defect. README x7 promised v26.7.0 while the CHANGELOG said v26.6.0, and a whole-file allowance
# would hand that exact bug an amnesty. A changelog has two halves: a MASTHEAD (title, format
# note, status block) that claims what ships TODAY, and the SECTIONS — `## [Unreleased]` and
# every dated version heading under it — that are the record. Only the second half is admitted;
# `dated_from()` draws the line AT the first section heading, so `## [Unreleased]` is inside it.
# That is deliberate: an Unreleased entry describing what a PAST release shipped ("v26.8.0 .apk
# still carried the systemd unit") is a statement about the record, and the entry keeps it when
# the section is dated at the next cut. The battery proves both sides of the line.
#
# TWO MEMBERS ARE DIRECTORIES, AND EACH IS HELD BY A DIFFERENT LEASH, because a subtree
# allowance covers documents nobody has written yet and that is the only dangerous shape here:
#   docs/launch     — TIED TO THE EXPORT'S OWN CURATION, as INTERNAL_CONTEXT is and for the same
#                     reason. It holds only while the export script says the directory never
#                     ships — DOCS_BLOCK minus DOCS_KEEP, because a directory that re-publishes
#                     even one file is not a directory that is removed.
#   docs/releases   — NOT tied, because it SHIPS. It is safe for a different reason: the kind is
#                     SELF-BOUND. A witness may name the version in its OWN FILENAME and nothing
#                     else, so a new file there earns nothing unless it is labelled, and a
#                     labelled one cannot drift even inside the ledger.
# The battery asserts that pairing by READING THE TREE (os.path.isdir), not by restating the
# constant: a third directory member that is neither tied nor self-bound fails there.
#
# EVERY OTHER MEMBER IS AN EXACT FILE, which is the shape DERIVED_ALLOW has always used: a named
# path, a named kind, a written reason, reviewable on sight. Two of them ship (the VPAT and
# docs/RELEASE-INSTALLER.md), and that is exactly why they are files and not the directories
# they sit in — a published subtree may not carry an allowance for prose that does not exist yet.
HISTORICAL_ALLOW = [
    # (exact path or exact directory, kind, reason)
    ("CHANGELOG.md", "changelog-history",
     "the dated sections below `## [Unreleased]` ARE the record of every release; the masthead "
     "above them is not, and is judged"),
    # export-closure: absent-by-design docs/RELEASE-EXECUTION-HANDOFF.md — DATA, not a caller.
    # The export removes it (the export curation script) and nothing here executes it; the path is
    # matched, never run. Same for the two records below it.
    # export-closure: absent-by-design docs/RELEASE-REHEARSAL-RUNBOOK.md — same class, same reason.
    # export-closure: absent-by-design docs/RELEASE-CHANNEL-POLICY.md — same class, same reason.
    ("docs/releases", "release-witness",
     "a dated install-surface witness may name the version in its OWN FILENAME and nothing else; "
     "scripts/check-install-docs.sh binds the current one separately"),
    ("docs/launch", "launch-record",
     "the launch pack of a release that was posted on the day it shipped; rewriting a posted "
     "text to a later version states that something else was posted"),
    ("docs/contracts", "contract-record",
     "internal contract notes quoting a past measurement together with the canon it was taken "
     "under; the quote is evidence, not an instruction. Curated out, minus the files DOCS_KEEP "
     "re-publishes, which are judged"),
    ("docs/claims/measurements/2026-09-12/AUDIT.md", "dated-measurement",
     "a measurement run filed under the date it was taken, naming the release it deliberately "
     "did NOT measure"),
    ("docs/accessibility/VPAT-olivares-admin.md", "conformance-record",
     "the conformance statement names the release run that produced its evidence; a run that "
     "happened cannot be renamed"),
    # export-closure: absent-by-design docs/RELEASE-GO-LIVE-RUNBOOK.md — a SUBJECT of this
    # rule, not a caller. The export removes it (the export curation script), and this record is
    # DATA: in the published tree the path is simply never matched. Nothing executes it, so there
    # is no call to guard — hub-only would be the wrong class.
    # export-closure: absent-by-design docs/RELEASE-NEXT-ACTIONS.md — same class, same reason.
    ("docs/RELEASE-GO-LIVE-RUNBOOK.md", "release-act-record",
     "the runbook of the go-live act that was executed, with the tag it cut"),
    ("docs/RELEASE-NEXT-ACTIONS.md", "release-act-record",
     "the ordered record of the first cut, kept as executed"),
    ("docs/RELEASE-EXECUTION-HANDOFF.md", "release-act-record",
     "the handoff that published the first release, with the tag it named"),
    ("docs/RELEASE-REHEARSAL-RUNBOOK.md", "release-act-record",
     "the one dress rehearsal, run once against the tag it names"),
    ("docs/RELEASE-CHANNEL-POLICY.md", "publication-state-record",
     "the MEASURED publication state of each distribution surface; a state may not be swept to "
     "a version that is not published yet, which is the rule check-install-docs.sh enforces"),
    ("docs/RELEASE-INSTALLER.md", "release-act-record",
     "the qualification names the published tag that predates the service adapter, which is why "
     "each leg tests two subjects"),
    # export-closure: absent-by-design docs/emitted-urls-hub-record.txt — DATA, not a caller: the
    # emitted-URL gate's excluded record, removed by one exact DOCS_BLOCK entry. Named here so the
    # development tree judges it; nothing runs it. (added 2026-09-23)
    ("docs/emitted-urls-hub-record.txt", "excluded-url-record",
     "the emitted-URL declarations of files the export drops, among them a posted launch record's "
     "link to a past release page; tied to the one exact entry that curates the record out"),
]

# Directory members whose allowance is granted only while the export curation removes them.
CURATION_TIED = {"docs/launch", "docs/contracts", "docs/emitted-urls-hub-record.txt"}

# The one other way a DIRECTORY member can be safe without the tie: a kind whose allowance is
# bound by the FILE ITSELF, so a document nobody has written yet earns nothing. `release-witness`
# is that kind — the version comes from the filename, an unlabelled file in docs/releases/ is
# judged normally, and a labelled one may name only its own release. Any other directory member
# hands an allowance to future prose and must be tied; the battery asserts exactly that.
SELF_BOUND_KINDS = {"release-witness"}


def historical_kind(path, curated, kept=frozenset()):
    """-> the kind that admits a PAST release at `path`, or None.

    EXACT, never a substring: a member is the whole path or a whole run of leading path
    COMPONENTS, so docs/launch-notes/ is not docs/launch/, docs/releases-archive/ is not
    docs/releases/, and docs/RELEASE-A-NEW-GUIDE.md is not a member at all. That is the sibling
    prune's own warning (2026-08-14) applied to an allowance instead of a prune.

    A TIED directory grants nothing to a file the export re-publishes out of it (`kept`,
    DOCS_KEEP): the tie's whole claim is "this never ships", and for those files it is false."""
    parts = path.split("/")
    for member, kind, _ in HISTORICAL_ALLOW:
        if member in CURATION_TIED:
            if member not in curated or path in kept:
                continue
        mp = member.split("/")
        if parts == mp or (len(parts) > len(mp) and parts[:len(mp)] == mp):
            return kind
    return None


def witness_version(path):
    """-> the version a dated witness records, read from its OWN FILENAME, or None.

    docs/releases/ is a ledger with one file per release, and the file says which one it is. The
    allowance is therefore not "any past version here" but "the version on the label": a v26.8.0
    witness stating 26.5.0 is a witness about the wrong release, and no other rule would see it."""
    base = os.path.basename(path)
    m = re.match(r"^(v(?:2[6-9]|[3-9][0-9])\.(?:[1-9]|1[0-2])\.[0-9]+)-", base)
    return m.group(1) if m else None


def historical_allowed(canon, path, tok, dated, curated, kept=frozenset()):
    """-> True iff `tok` is a past release named in the record of that release.

    Two conditions always, and neither is a line waiver: the PATH must be an exact member of the
    class above, and the TOKEN must be strictly below the canon — a record is of the past, and a
    token at or above the canon is the live claim every other rule already judges.

    Two kinds are narrower than that. `changelog-history` reaches only the record half of the
    file (see dated_from). `release-witness` admits only the version the filename names, so the
    ledger cannot drift inside itself."""
    kind = historical_kind(path, curated, kept)
    if kind is None:
        return False
    if kind == "changelog-history" and not dated:
        return False
    if _tuple(tok) >= _tuple(canon):
        return False
    if kind == "release-witness":
        named = witness_version(path)
        return named is not None and _tuple(tok) == _tuple(named)
    return True


# ── A CITATION OF A DATED CHANGELOG SECTION (added 2026-09-18) ────────────────────────
# The member above says the dated sections of CHANGELOG.md ARE the record of every release.
# Documentation OUTSIDE that file cites those sections by their own heading syntax, and a
# citation promises nothing about what ships: it names WHERE a fact is written.
#
# THE MEASURE THAT PUT IT HERE, on the v26.9.1 cut: with the canon re-derived and nothing
# else changed, 146 of the 589 divergent occurrences were `[26.9.0]` section references in
# 64 files — ten how-to and reference pages in seven locales, all written AFTER the v26.9.0
# tag. Swept to the canon they would send the reader to a `[26.9.1]` section that does not
# carry the entry. That is not a stale claim corrected; it is a working reference broken.
#
# IT IS BOUND BY FORM, NOT BY PATH, and the form is the changelog's own section syntax
# (Keep a Changelog): the token inside `[...]`, on a line that names the changelog file.
# A path list was rejected for the reason this file already gives for the docs walk — the
# eleventh page to cite a section would be born invisible, and nobody would be told.
#
# EVERY occurrence on the line, never any: the `bounded()` precedent, for its reason. A line
# that cites a section AND states the version bare is ambiguous, and an ambiguous claim is
# judged as the stale claim it might be. Seven real lines in seven locales read exactly like
# that, and they were rewritten rather than exempted.
#
# It cannot rescue anything `changelog-history` refuses: a heading does not name the file it
# sits in, so the masthead stays judged and the sections stay the record.
CITED_SECTION = re.compile(r"\[v?(?:2[6-9]|[3-9][0-9])[.](?:[1-9]|1[0-2])[.][0-9]+\]")

# The changelog is named by its filename, which is the same string every one of those pages
# uses to point at it. Bare "changelog" is deliberately not enough: the word is prose.
CHANGELOG_FILE = "CHANGELOG.md"


def wrapped_citation(line, tok):
    """-> True iff `tok` sits in a section reference on a line that names no changelog.

    The one shape a reader cannot diagnose from a divergence line: the citation is right and
    the LINE BREAK is what refuses it, because the file name ended the line above. Measured on
    the v26.9.1 cut: eight lines in eight files, every one of them wrapped in that same place.
    They were rewrapped, not exempted, and this note exists so the ninth is not a puzzle."""
    if CHANGELOG_FILE in line:
        return False
    spots = [m.start() for m in re.finditer(r"(?<![\w.-])" + re.escape(tok) + r"(?![\w.-])", line)]
    brackets = [m.span() for m in CITED_SECTION.finditer(line)]
    return bool(spots) and all(any(b < s and s + len(tok) < e for b, e in brackets) for s in spots)


def cited_section(line, tok):
    """-> True iff EVERY occurrence of `tok` in `line` sits inside a changelog section
    reference, on a line that names the changelog file.

    A section reference is the heading syntax the changelog itself uses, so `[26.9.0-fips]`
    is not one: a hardened image tag has no section and earns nothing here."""
    if CHANGELOG_FILE not in line:
        return False
    spots = [m.start() for m in re.finditer(r"(?<![\w.-])" + re.escape(tok) + r"(?![\w.-])", line)]
    if not spots:
        return False
    brackets = [m.span() for m in CITED_SECTION.finditer(line)]
    return all(any(b < s and s + len(tok) < e for b, e in brackets) for s in spots)


def scan_pins(tops):
    """-> [(path, line_no, token, line, dated)] for every artefact pin under a published tree."""
    hits = []
    for top in tops:
        kind = root_entry(top)   # refuses a present entry that cannot be examined (R1-TOP)
        if kind == "file":
            entries = [(".", [], [top])]
        elif kind == "dir":
            entries = walk(top)
        else:
            continue  # absent — go.work.sum etc. may legitimately not exist in a partial tree
        for root, dirs, files in entries:
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for f in sorted(files):
                if not f.endswith(TEXTY):
                    continue
                path = os.path.normpath(os.path.join(root, f))
                # This gate's own selftest fixtures are literal stale pins on purpose — they are
                # what proves arm B discriminates. Scanning them makes the gate fail on itself
                # forever. The fixtures could be assembled from fragments to dodge the scan, but
                # a fixture written to be invisible to the thing it tests is the weaker test.
                if path == os.path.join("scripts", "check-release-version.sh"):
                    continue
                text = read_surface(path)
                start = dated_from(text)
                for i, line in enumerate(text.splitlines(), 1):
                    for m in PIN.finditer(line):
                        hits.append((path, i, m.group(0), line.strip(),
                                     start is not None and i >= start))
    return hits

def judge_pins(canon, hits, curated=None, kept=None):
    """A pin must name the canon (or, where DERIVED_ALLOW says so, a derived form)."""
    require_decided(canon)
    if curated is None:
        curated = curated_out()
    if kept is None:
        kept = curation_kept()
    bad = []
    for path, i, tok, _line, dated in hits:
        # A `_test.go` pin is fixture data (see header): the multi-version regression keys an
        # object at the version AFTER the canon precisely to prove a stale token cannot fetch
        # it. Judging it would force the fixture to lie about the case it tests.
        if go_test_fixture(path):
            continue
        ver = re.search(r"v?(\d+\.\d+\.\d+(?:-fips|-stig)?)$", tok).group(1)
        if ver.lstrip("v") in {f.lstrip("v") for f in allowed_at(canon, path)}:
            continue
        if historical_allowed(canon, path, tok, dated, curated, kept):
            continue
        bad.append((path, i, tok))
    return bad

CANON_SHAPE = re.compile(r"v(2[6-9]|[3-9][0-9])\.([1-9]|1[0-2])\.([0-9]+)")

# Where a changelog stops stating the CURRENT release and starts being the RECORD of past
# ones: the first section heading, `## [Unreleased]` or the first dated version heading.
SECTION_START = re.compile(
    r"^## \[(?:Unreleased|v?(?:2[6-9]|[3-9][0-9])\.(?:[1-9]|1[0-2])\.[0-9]+)\]")


def dated_from(text):
    """-> the line number where a changelog's SECTIONS begin, or None when it has none.

    ABOVE that line a changelog is a masthead: the title, the format note and the status
    block, every one of them a claim about what ships TODAY. That is exactly where this
    gate's founding defect lived — README x7 promised v26.7.0 while the CHANGELOG masthead
    said v26.6.0 — so the historical-record allowance below must never reach it.

    BELOW it the file is the record: `## [Unreleased]` and every dated version section. A
    file with no section heading has no record part at all, and None admits nothing, which
    is the fail-closed direction: a changelog whose shape changed is judged whole."""
    for i, line in enumerate(text.splitlines(), 1):
        if SECTION_START.match(line):
            return i
    return None

# ── Artifact surfaces: shipped deployment coordinates, not prose ──────────────────────
# Walked whole (see header). The engine BINARY is deliberately absent: its version is
# injected at build time (.goreleaser.yaml:102,151 `-X main.version={{ .Version }}`), so
# it is correct by construction and the 26.7.0 tokens under cmd/ and core/ are help-text
# examples and doc comments, not declarations.
ARTIFACT_ROOTS = ("deploy", "packaging", "operator")

# Enumeration floor — the check-migrations.sh contract. If a walk of the three roots ever
# yields fewer files or zero version tokens, the enumeration broke (a moved directory, a
# rename, a widened prune); it does not mean the declarations went away.
LAST_GOOD = {"date": "2026-08-14", "files": 6, "tokens": 17,
             "note": "deploy/helm/olivares/Chart.yaml + deploy/manifests/install.yaml + "
                     "packaging/docker/dockerhub-overview.md + 3 under operator/"}

# The CRD API type declarations carry the third floor, and their path is DISCOVERED here
# instead of written down. kubebuilder names that file after the CRD Kind, this project's
# Kind is the PRE-RENAME product word, and THIS SCRIPT SHIPS in the public export: spelling
# the path planted that word in scripts/, where the export's leak gate refuses it (leg 2 —
# "stale identity outside operator/"). Measured 2026-08-14: the literal turned
# `task lint:export` red and would have blocked every contributor's push. The gate is right —
# in scripts/ that word is indistinguishable from a binary/command/image still carrying the
# old name — so the answer is not an allow-strings entry (which publishes the word AND keeps
# a second copy of a name the tree already owns, the same double cost measured on the egress
# guard the day before) but taking the scope from the operator's own API package, which is
# where a kubebuilder precondition doc comment lives. `*_types_test.go` is not that shape.
# Empty discovery is UNVERIFIED + exit 2 below, never a silently dropped exemption.
CRD_TYPES_GLOB = "operator/api/*/*_types.go"


def crd_types_files():
    """-> the CRD API type declarations, sorted. The min-version scope, derived not spelled."""
    return sorted(glob.glob(CRD_TYPES_GLOB))


# (exact path, kind, reason) — see header for the two kinds. The CRD API records come from
# the discovery above; everything else is exact, and no entry widens beyond its named file.
ARTIFACT_ALLOW = [
    ("packaging/docker/dockerhub-overview.md", "variants",
     "the -fips/-stig tag rows the Docker Hub landing page documents"),
    ("operator/README.md", "min-version",
     "the /pod-readyz precondition is a compatibility FLOOR, not a shipped coordinate"),
] + [(p, "min-version",
      "the role-label precondition is a compatibility FLOOR, not a shipped coordinate")
     for p in crd_types_files()]

# LOWER-BOUND VOCABULARY — how a floor is written in this tree. `>=`/`≥` is the prose
# form; `--min-version` is the CLI flag that IS the bound; `introduced` is the OSV/GHSA
# range key naming the first affected version. All three state "from here upwards", which
# is what the min-version allowance is scoped to. It is deliberately a vocabulary of BOUND
# EXPRESSIONS and not a line waiver: the token must ALSO be <= canon, and the path must
# already carry a min-version record. A line saying none of these gets no allowance.
#
# It binds the TOKEN, not the line. Measured while building this: `"ranges": [ { "introduced":
# "26.5.0", "fixed": "26.7.1" } ]` has a lower-bound word on it, and a line-scoped test handed
# the amnesty to `fixed` as well — a stale shipped version passing as a floor because a bound
# happened to share its line. So the expression must sit IMMEDIATELY before the token.
BOUND = re.compile(r"""(?:>=|≥|--min-version|introduced["']?\s*:)\s*["']?v?$""")


def bounded(line, tok):
    """-> True iff every occurrence of `tok` in `line` is governed by a lower bound.

    Every, not any: a line carrying the same token twice with only one of them bounded is
    ambiguous, and an ambiguous floor is judged as what it might be — a stale claim.
    """
    spots = [m.start() for m in re.finditer(r"(?<![\w.-])" + re.escape(tok) + r"(?![\w.-])", line)]
    return bool(spots) and all(BOUND.search(line[:s]) for s in spots)


def _tuple(tok):
    """'26.7.0' / 'v26.7.0-fips' -> (26, 7, 0). Comparable, so a floor can be ordered."""
    m = VERSION.search(tok)
    return (int(m.group(1)), int(m.group(2)), int(m.group(3)))


def artifact_allowed(canon, path, tok, line):
    """-> (ok, why). The canon exactly, plus the two context-bound artifact kinds."""
    base = canon.lstrip("v")
    if tok.lstrip("v") == base:
        return True, "canon"
    kinds = {k for p, k, _ in ARTIFACT_ALLOW if path == p or path.endswith("/" + p)}
    if "variants" in kinds and tok.lstrip("v") in {f"{base}-fips", f"{base}-stig"}:
        return True, "variants"
    # A floor is granted a NARROWER allowance (<= canon), and only on the line that
    # states the bound. Off that line the canon is required exactly.
    if "min-version" in kinds and bounded(line, tok) and _tuple(tok) <= _tuple(canon):
        return True, "min-version"
    return False, "divergent"


def judge_artifacts(canon, hits):
    """-> failures. Empty only when every artifact coordinate states the canon."""
    require_decided(canon)
    return [(p, i, tok) for p, i, tok, line, _dated in hits
            if not artifact_allowed(canon, p, tok, line)[0]]


def artifact_files():
    out = []
    for root in ARTIFACT_ROOTS:
        for r, dirs, files in walk(root):
            dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "vendor")]
            out += [os.path.join(r, f) for f in files]
    return sorted(f for f in out if regular_file(f))

def read_canon(text):
    """Schema, not scrape: exactly ONE non-comment record, and it must be a v-prefixed CalVer
    with a real month. Anything else refuses before any surface scan — a malformed canon
    certified OK was the measured failure mode.

    UNDECIDED is refused too, with UNVERIFIED and exit 2: it names no canon to hold the surfaces
    to, and the report mode that answered it with exit 0 was a success escape (removed
    2026-09-23)."""
    records = [ln.strip() for ln in text.splitlines() if ln.strip() and not ln.strip().startswith("#")]
    if len(records) != 1:
        sys.exit(f"FAIL check-release-version: RELEASE-VERSION must contain exactly one record, found {len(records)}")
    canon = records[0]
    if canon == "UNDECIDED":
        unverified("UNVERIFIED check-release-version: RELEASE-VERSION says UNDECIDED; there is no canon "
                   "to hold the release-bearing surfaces to, and nothing is certified without one.")
    if not CANON_SHAPE.fullmatch(canon):
        sys.exit(f"FAIL check-release-version: RELEASE-VERSION record {canon!r} is not vYY.M.PATCH (month 1-12)")
    return canon


def require_decided(canon):
    """Every judge's first line: a canon that is not vYY.M.PATCH is refused (exit 2), never
    judged as 'no failures'. read_canon() already refuses it; this keeps a caller that skips the
    schema from turning UNDECIDED back into a pass."""
    if not CANON_SHAPE.fullmatch(str(canon)):
        unverified(f"UNVERIFIED check-release-version: {canon!r} is not a decided canon (vYY.M.PATCH); "
                   "no surface is judged against it.")

def allowed_at(canon, path):
    m = CANON_SHAPE.fullmatch(canon)
    yy, mm, pp = m.groups()
    base = f"{yy}.{mm}.{pp}"
    allowed = {base, f"v{base}"}
    for suffix, kind, _ in DERIVED_ALLOW:
        if not path.endswith(suffix):
            continue
        if kind == "variants":
            allowed |= {f"{base}-fips", f"{base}-stig"}
    return allowed

def scan(files, rd):
    """-> [(path, line_no, token, line, dated)] for every product-version-shaped token.

    `dated` says the hit sits at or below the file's first changelog section heading. It is read
    HERE because it is a property of the whole file, and a judge that only ever sees one line
    cannot recover it. It is computed for every file and consumed by one kind
    (`changelog-history`), so a stray `## [...]` heading elsewhere changes no verdict."""
    hits = []
    for path in files:
        text = rd(path)
        start = dated_from(text)
        for i, line in enumerate(text.splitlines(), 1):
            for m in VERSION.finditer(line):
                hits.append((path, i, m.group(0), line.strip(),
                             start is not None and i >= start))
    return hits

def doc_allowed(canon, path, tok, line, dated, curated, kept=frozenset()):
    """-> bool. The canon (or a value-based derived form) plus the context-bound floor.

    Value-based kinds (variants, next-patch) are decided by allowed_at; min-version is
    the one kind that needs the LINE, because a floor is only a floor where the text
    states the bound. Off such a line the canon is required exactly, so this is a
    narrower allowance granted by context — not the general line-skip the header refuses.
    """
    if tok.lstrip("v") in {f.lstrip("v") for f in allowed_at(canon, path)}:
        return True
    kinds = {k for p, k, _ in DERIVED_ALLOW if path == p or path.endswith("/" + p)}
    if "min-version" in kinds and bounded(line, tok) and _tuple(tok) <= _tuple(canon):
        return True
    if historical_allowed(canon, path, tok, dated, curated, kept):
        return True
    # A citation of a section that already exists. Strictly below the canon for the same
    # reason every record is: the canon's own section is the live claim, and a section above
    # it is one nobody wrote.
    if cited_section(line, tok) and _tuple(tok) < _tuple(canon):
        return True
    return False


def judge(canon, hits, curated=None, kept=None):
    """-> (failures, census). The canon must be decided (require_decided)."""
    require_decided(canon)
    census = {}
    for path, i, tok, line, dated in hits:
        census.setdefault(tok.lstrip("v").replace("-fips", "").replace("-stig", ""), []).append((path, i, tok))
    if curated is None:
        curated = curated_out()
    if kept is None:
        kept = curation_kept()
    failures = [(path, i, tok) for path, i, tok, line, dated in hits
                if not doc_allowed(canon, path, tok, line, dated, curated, kept)]
    return failures, census

# Historical records, out of scope BY DIRECTORY — the rule the docs-site walk already
# applied, now stated once and used by both walks. A dated ADR and a frozen snapshot say
# what was true when they were written; sweeping them to the canon falsifies the record
# instead of correcting a claim. This is a prune of the CENSUS, not an exemption: nothing
# inside is judged, and nothing inside is a live release-bearing surface.
DATED_PRUNE = ("2026-06", "adr", "archive")


def pruned_dir(name):
    """-> True if this directory is out of scope. EXACT name, never a substring.

    A substring test reads the same and silently swallows a directory merely NAMED like a
    pruned one (`adrian`, `archived-decisions`) — live documentation, unscanned, with
    nothing to show for it.
    Named so the battery can assert it without needing those directories to exist.
    """
    return name in DATED_PRUNE + (".git", "node_modules", "vendor")


def docs_files(curated=None):
    """-> every file under docs/, walked whole. The declarants are DISCOVERED.

    Not a list of extensions either: a runbook that lands as .txt or .yaml still tells an
    operator which version to deploy. Binaries are skipped by the reader, not by name.

    `curated` is the export's DOCS_BLOCK (curated_out()); it decides the internal-context
    prune and is a parameter so the battery can prove, on the SAME tree, that the prune is
    what keeps docs/ai-context out — and that with the curation gone it comes straight back.
    """
    if curated is None:
        curated = curated_out()
    out = []
    for root, dirs, files in walk("docs"):
        dirs[:] = [d for d in dirs
                   if not pruned_dir(d) and not internal_context(os.path.join(root, d), curated)]
        out += [os.path.join(root, f) for f in sorted(files)]
    return sorted(f for f in out if regular_file(f))


# Enumeration floor for the documentary walk — the same contract as LAST_GOOD below and as
# check-migrations.sh: zero units is COULD NOT LOOK, never "nothing to declare". Measured
# 2026-08-14 on this branch: 283 files (489 under docs/, less the 206 in adr/ + archive/).
#
# The floor is set BELOW the measured population on purpose, and the margin is reasoned
# rather than picked: docs/ churns, so a tripwire at the exact count would turn every
# ordinary doc deletion into UNVERIFIED — a gate that cries wolf gets bypassed, which is a
# worse outcome than the one it guards. The failures this exists to catch are structural
# (a moved root, a rename, a prune that grew) and they remove a whole SUBTREE: the two
# pruned directories alone are 206 files. A floor of 200 cannot be reached by churn and
# cannot be missed by a subtree disappearing.
#
# `top` is the invariant with no magic number in it, and it is the one that fails if this
# section is ever undone: at least one file must be found DIRECTLY under docs/, because
# top-level docs/*.md being scanned by nobody is the exact regression closed here.
DOCS_LAST_GOOD = {"date": "2026-08-14", "measured": 283, "floor": 200, "top": 1,
                  "note": "docs/ walked whole minus the dated/archived directories"}

# The curated PUBLIC export is a smaller population BY DESIGN, not a broken walk: the export's
# DOCS_BLOCK removes whole subtrees and some thirty top-level files before this script ever runs
# there. Measured 2026-09-06 on a real export of this tree, from the export: 84 files, 32 of them
# directly under docs/, against the full source tree's floor of 200 — so the shipped gate answered UNVERIFIED
# (exit 2) on every public read of this tree, and the battery went red on the same line. This is
# that population's own record, with the source-tree record's reasoning and its margin (29 %
# below the measurement): the failures the floor exists to catch remove a SUBTREE, and the largest
# one the public walk carries (28 files) dropping out crosses 60; ordinary churn does not. The
# source-tree record is untouched and still holds it — and any tree that classifies as neither
# (docs_floor): an unknown tree earns the strictest floor, never the smaller one.
DOCS_LAST_GOOD_PUBLIC = {"date": "2026-09-06", "measured": 84, "floor": 60, "top": 1,
                         "note": "the curated public export: docs/ walked whole minus the "
                                 "dated/archived directories, after the export's own curation"}


def docs_floor(profile):
    """-> the enumeration record this tree is held to: only a stamped public export earns the
    public record; hub and unknown are held to the full source tree's, which is the stricter one."""
    return DOCS_LAST_GOOD_PUBLIC if profile == "public" else DOCS_LAST_GOOD


def surface_files():
    # root_entry(), not os.path.isfile(): absent is skipped, present-but-unexaminable refuses.
    out = [f for f in ("CHANGELOG.md", "README.md") if root_entry(f) == "file"] + sorted(glob.glob("README.*.md"))
    for base in ("docs/trust", "docs/launch"):
        out += sorted(glob.glob(f"{base}/*.md"))
    # docs/ whole — this is what closes the blind spot; the two globs above are kept
    # because they name surfaces that must be scanned even if the walk is ever narrowed.
    # (glob is silent about a directory it cannot open; the refusing walk right here covers
    # the same two directories, so an unlistable docs/trust refuses before glob's silence matters.)
    out += docs_files()
    for root, dirs, files in walk("docs-site/src/content/docs"):
        if "/2026-06" in root or "/adr" in root:
            dirs[:] = []
            continue
        dirs[:] = [d for d in dirs if d not in ("2026-06", "adr")]
        out += [os.path.join(root, f) for f in sorted(files) if f.endswith((".md", ".mdx"))]
    if root_entry("INSTALL.md") == "file":
        out.append("INSTALL.md")
    # ⛔ LAS SUPERFICIES DE DESPLIEGUE TAMBIÉN SE PUBLICAN, Y EL CENSO NO LAS MIRABA.
    #
    # `surface_files()` enumeraba a mano: CHANGELOG, los siete README, docs/trust, docs/launch,
    # docs-site e INSTALL.md. Pero the export curation script (línea 80) publica también `deploy/`,
    # `packaging/`, `examples/` y `oscap/` — y ahí viven versiones que un usuario COPIA Y PEGA.
    #
    # Medido 2026-08-13, con el canon ya en v26.8.0 y este gate en VERDE:
    #   deploy/manifests/install.yaml            6x "26.7.0", incluida la etiqueta de imagen
    #   deploy/helm/olivares/Chart.yaml          appVersion "26.7.0"
    #   packaging/docker/dockerhub-overview.md   5x, tags 26.7.0 / -fips / -stig
    #
    # Es decir: el manifiesto de Kubernetes que publicamos instalaba la versión ANTERIOR y el
    # gate no tenía nada que decir. Un gate que enumera a mano sus miembros caduca en silencio —
    # es la forma de gate que este repositorio ha encontrado rota más veces (el censo de rutas, el
    # mapa canon<->paquete, el allowlist por ruta, la clase de aislamiento de git-env).
    #
    # Se DESCUBRE en vez de enumerarse: cualquier fichero de una superficie publicada que
    # mencione una versión con forma de producto entra en el censo. Añadir un manifiesto nuevo no
    # exige acordarse de esta lista.
    for base in ("deploy", "packaging", "examples", "oscap"):
        for root, dirs, files in walk(base):
            dirs[:] = [d for d in dirs if d not in ("node_modules", ".git", "vendor")]
            for f in sorted(files):
                if f.endswith((".md", ".yaml", ".yml", ".json", ".txt", ".sh")):
                    out.append(os.path.join(root, f))
    # Everything listed above exists by construction (a fixed name was tested, a glob or a walk
    # found it); an entry that now cannot be examined is a refusal, never a drop.
    return [f for f in out if regular_file(f)]
    # Deduplicate: docs/trust and docs/launch are reached twice on purpose (see above).
    return sorted({f for f in out if os.path.isfile(f)})

def selftest():
    rd = lambda t: (lambda path: t[path])
    ok = True
    def expect(name, cond):
        nonlocal ok
        print(("selftest ok: " if cond else "selftest FAIL: ") + name)
        ok = ok and cond
    def canon_refuses(name, text):
        try:
            read_canon(text)
        except SystemExit:
            expect(name, True)
            return
        expect(name, False)
    # ── canon schema: every malformed state refuses BEFORE any surface scan ──
    canon_refuses("malformed canon (BROKEN-CANON) -> refuse", "# c\nBROKEN-CANON\n")
    canon_refuses("blank canon -> refuse", "# only comments\n")
    canon_refuses("two canon records -> refuse", "v26.7.0\nv29.1.0\n")
    canon_refuses("month-zero canon -> refuse", "v26.0.1\n")
    canon_refuses("prefixless canon -> refuse", "26.7.0\n")
    expect("well-formed canon -> accepted", read_canon("v26.7.0\n") == "v26.7.0")
    # ── decided canon: divergence is red; derived forms only in their contexts ──
    tree = {"README.md": "ships with `v26.7.0` today", "CHANGELOG.md": "the first release is `v26.6.0`"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("divergent CHANGELOG under decided canon -> red", fails == [("CHANGELOG.md", 1, "v26.6.0")])
    tree = {"docs-site/src/content/docs/how-to/docker-deployment.md":
            "pull olivares:26.7.0-fips then upgrade to 26.7.1"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("fips INSIDE the upgrade doc -> green; the canon's next patch there -> red (no invented release)",
           fails == [("docs-site/src/content/docs/how-to/docker-deployment.md", 1, "26.7.1")])
    tree = {"README.md": "the first release will be v26.7.1"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("next-patch OUTSIDE its documented example -> red", fails == [("README.md", 1, "v26.7.1")])
    tree = {"README.md": "grab olivares:26.7.0-fips"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("fips variant outside its image-tag docs -> red", len(fails) == 1)
    marker = "<<" + "FRAN"
    tree = {"b.md": f"v29.1.0 hiding here {marker}: confirm>>"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("foreign CalVer on a readiness-marker line -> red (no line waiver)",
           fails == [("b.md", 1, "v29.1.0")])
    tree = {"c.md": "release v0.1.0 pending"}
    expect("SemVer placeholder -> not product-shaped", scan(tree, rd(tree)) == [])
    tree = {"d.md": "requires Go 1.26.5 and chi v5.3.1"}
    expect("dependency versions -> not product-shaped", scan(tree, rd(tree)) == [])
    tree = {"e.md": "since 26.0.9 things"}
    expect("month-zero token -> not product-shaped", scan(tree, rd(tree)) == [])
    _, census = judge("v26.7.0", scan({"f.md": "v26.6.0 and v26.7.0"}, rd({"f.md": "v26.6.0 and v26.7.0"})))
    expect("the divergence census groups every shaped token by value", set(census) == {"26.6.0", "26.7.0"})
    # ── arm B: the pin/floor distinction, and the coverage that made arm A caducate ──
    pins = lambda text, path="operator/config/samples/x.yaml": judge_pins(
        "v26.7.0", [(path, i, m.group(0), ln, False)
                    for i, ln in enumerate(text.splitlines(), 1) for m in PIN.finditer(ln)],
        curated=set())
    expect("stale image PIN in a tree arm A never scans -> red",
           pins("  image: docker.io/olivaresai/olivares:26.6.0\n") ==
           [("operator/config/samples/x.yaml", 1, "olivaresai/olivares:26.6.0")])
    expect("current image pin -> green", pins("  image: olivaresai/olivares:26.7.0\n") == [])
    expect("a compatibility FLOOR is not a pin -> green (rewriting it would falsify history)",
           pins("// role label (olivares >= 26.6.0). With an older image every pod fails\n") == [])
    expect("air-gap bundle FILENAME is a pin -> red when stale",
           len(pins("curl -O https://dl/olivares-26.6.0-linux-amd64.tar.gz\n")) == 1)
    expect("hardened pin follows the SAME DERIVED_ALLOW as arm A",
           pins("olivaresai/olivares:26.7.0-fips", "packaging/docker/dockerhub-overview.md") == []
           and len(pins("olivaresai/olivares:26.7.0-fips", "operator/x.yaml")) == 1)
    # THE STRUCTURAL ONE. Arm A enumerated its surfaces by hand and went green for weeks over a
    # Kubernetes manifest pinning the PREVIOUS release. Arm B's census is read from the export,
    # so a newly published directory is scanned without anyone remembering this file. If that
    # tie breaks, the gate is blind again — and it must say so, not pass.
    # WHICH TREE. Decided by scripts/hub-leg.sh --classify — the sentence the export stamps into
    # its marker plus the absence of every hub-only path — so a copied marker, or a hub that lost
    # one file, cannot buy the other profile's assertions. `unknown` is a red, not a third branch.
    profile, why = tree_profile()
    expect(f"the tree classifies as the full source tree or a stamped public export, never guessed ({profile}: {why})",
           profile in ("hub", "public"))
    hub = profile == "hub"
    # PARSER CONTROLS, BOTH PROFILES, on an in-memory export script: the census and the curation
    # are parsed out of shell text, and the public tree — which has no export script to parse —
    # must keep proving the parsers. The second DOCS_BLOCK entry is a synthetic directory name.
    syn_export = ("TOP_ALLOW=(\n  core deploy  # a trailing comment must not become an entry\n"
                  "  packaging operator docs-site\n)\n"
                  "DOCS_BLOCK=(\n  " + INTERNAL_CONTEXT[0][0] + "  # the internal context\n"
                  "  docs/fixture-curated-out\n)\n")
    syn_tops, syn_src = published_tops(read=lambda _: syn_export)
    expect("TOP_ALLOW parser: entries per line and per word, comments stripped, order kept",
           syn_tops == ["core", "deploy", "packaging", "operator", "docs-site"] and "TOP_ALLOW" in syn_src)
    def refusal(fn):
        """-> (exit code, message) `fn` refused with, or (None, "") when it returned instead.

        The refusal's own message is CAPTURED, not printed: a battery that prints UNVERIFIED
        on its way to OK reads like a failure to anyone grepping the log."""
        held, sys.stderr = sys.stderr, io.StringIO()
        try:
            fn()
        except SystemExit as exc:
            return exc.code, sys.stderr.getvalue()
        finally:
            sys.stderr = held
        return None, ""
    code, msg = refusal(lambda: published_tops(read=lambda _: "TOP_ALLOW=(\n  # nothing\n)\n"))
    expect("TOP_ALLOW parsed EMPTY -> UNVERIFIED (exit 2), not an empty census",
           code == 2 and "parsed empty" in msg)
    def missing(_):
        raise FileNotFoundError(2, "No such file or directory", EXPORT_SCRIPT)
    def unreadable(_):
        raise PermissionError(13, "Permission denied", EXPORT_SCRIPT)
    def wrong_shape(_):
        return "TOP_ALLOW_RENAMED=(\n  core\n)\n"
    fb_tops, fb_src = published_tops(read=missing)
    expect("a tree WITHOUT the export script (i.e. a public clone) still gets a census",
           len(fb_tops) > 5 and "absent" in fb_src)
    code, msg = refusal(lambda: published_tops(read=wrong_shape))
    expect("an export script of UNKNOWN shape -> UNVERIFIED, not a guessed census",
           code == 2 and "TOP_ALLOW not found" in msg)
    # Measured 2026-09-06 in a hub copy with the export script at mode 000: the old `except
    # OSError` took the absent branch, enumerated the private directories and reported their
    # documents as divergent. Unreadable is not absent; it is COULD NOT LOOK.
    code, msg = refusal(lambda: published_tops(read=unreadable))
    expect("a PRESENT but unreadable export script -> UNVERIFIED (exit 2), never the public fallback",
           code == 2 and "unreadable" in msg)
    real_tops, src_name = published_tops()
    if hub:
        expect("the published census is read from export-public.sh, not written here",
               {"deploy", "packaging", "operator", "docs-site"} <= set(real_tops) and len(real_tops) > 15
               and "TOP_ALLOW" in src_name)
    else:
        # The export script is absent BY DESIGN here, so the fallback is not a stub: it is the
        # real census of the real tree, the verdict must name it, and it must reach what ships.
        expect("public export: the export script is absent, so the census is the tree itself and the verdict says so",
               not os.path.exists(EXPORT_SCRIPT) and "absent" in src_name and "TOP_ALLOW" not in src_name)
        expect("public export: the fallback census reaches every artefact root and both docs trees",
               {"deploy", "packaging", "operator", "docs-site", "docs"} <= set(real_tops) and len(real_tops) > 15)
        # scan_pins yields (path, line no, token, line, dated): FIVE fields. This branch runs only in
        # the public export, where the first hosted rehearsal of the exported v26.9.0 tree found it
        # unpacking four (ValueError, 2026-09-16); the full source tree never executes it. Bind the path only.
        walked = {hit[0].split("/", 1)[0] for hit in scan_pins([t for t in real_tops if t in ARTIFACT_ROOTS])}
        expect("public export: the fallback census walks to the shipped pins on disk (deploy/ and packaging/)",
               {"deploy", "packaging"} <= walked)

    # ── documentation scope: docs/ is WALKED, and floors survive the widening ──
    # Every case below is the witness for one rule of the docs/ widening. They are written
    # against judge() — the DOCUMENTARY arm — because that is what the widening changed;
    # a case that passes by reaching the artifact arm would prove nothing here.
    tree = {"docs/UPGRADE-AND-ROLLBACK.md": "container tags drop the leading v (`:26.7.0`)"}
    fails, _ = judge("v26.8.0", scan(tree, rd(tree)))
    expect("stale image tag in a top-level runbook -> red (the walk reaches docs/*.md)",
           fails == [("docs/UPGRADE-AND-ROLLBACK.md", 1, "26.7.0")])
    tree = {"docs/UPGRADE-AND-ROLLBACK.md": "(`:26.8.0`, `:latest`, `:26.8.0-fips`, `:26.8.0-stig`)"}
    expect("fips/stig variants at canon in the upgrade guide -> green",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [])
    # The floor rule, carried across from the artifact arm. Without it the widening would
    # have SWEPT this bound to the canon and narrowed the supported range to the newest
    # release — the defect this gate was preferred for not having.
    tree = {"docs/HA-LEADER-ROUTING.md": "Roll `spec.image` to ≥ 26.7.0 *while still on"}
    expect("prose compatibility floor below canon -> green (a floor is not a claim)",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [])
    tree = {"docs/HA-LEADER-ROUTING.md": "Roll `spec.image` to ≥ 26.9.0 *while still on"}
    expect("prose floor ABOVE canon -> red (requires an engine we do not ship)",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [("docs/HA-LEADER-ROUTING.md", 1, "26.9.0")])
    tree = {"docs/HA-LEADER-ROUTING.md": "image: docker.io/olivaresai/olivares:26.7.0"}
    expect("floor path, token OFF the bound -> red (narrower allowance, not a waiver)",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [("docs/HA-LEADER-ROUTING.md", 1, "26.7.0")])
    # The measured hole: one advisory line carries a lower bound AND a shipped version.
    tree = {"docs/PSIRT-RUNBOOK.md":
            '"ranges": [ { "introduced": "26.5.0", "fixed": "26.7.1" } ] } ],'}
    fails, _ = judge("v26.8.0", scan(tree, rd(tree)))
    expect("advisory range: `introduced` stays a floor, `fixed` does NOT inherit its amnesty",
           fails == [("docs/PSIRT-RUNBOOK.md", 1, "26.7.1")])
    tree = {"docs/PSIRT-RUNBOOK.md": "--advisory GHSA-xxxx-yyyy-zzzz --min-version 26.5.0 \\"}
    expect("--min-version is lower-bound vocabulary -> green",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [])
    # The release-key ledger: the bound row is a floor, every other token in the SAME file is not.
    tree = {"docs/RELEASE-VERIFICATION.md": "| ≥ v26.8.0 | license | `AAAA` | `beef` | `beef` |"}
    expect("key-ledger coverage floor below canon -> green (a floor is not a shipped coordinate)",
           judge("v26.9.0", scan(tree, rd(tree)))[0] == [])
    tree = {"docs/RELEASE-VERIFICATION.md": "  --expect-channel stable --expect-version 26.8.0"}
    expect("key-ledger path, token OFF the bound -> red (the live command must state the canon)",
           judge("v26.9.0", scan(tree, rd(tree)))[0]
           == [("docs/RELEASE-VERIFICATION.md", 1, "26.8.0")])
    tree = {"docs/RELEASE-VERIFICATION.md": "| ≥ v26.10.0 | license | `AAAA` | `beef` | `beef` |"}
    expect("key-ledger floor ABOVE canon -> red (no pair covers a release we do not ship)",
           judge("v26.9.0", scan(tree, rd(tree)))[0]
           == [("docs/RELEASE-VERIFICATION.md", 1, "v26.10.0")])
    # An unexempted doc is required to state the canon exactly — the safe default that
    # makes a NEW declarant caught without touching this file.
    tree = {"docs/A-BRAND-NEW-RUNBOOK.md": "deploy olivares:26.7.0 to the fleet"}
    expect("a doc no record exempts -> red (the eighth declarant is born visible)",
           judge("v26.8.0", scan(tree, rd(tree)))[0]
           == [("docs/A-BRAND-NEW-RUNBOOK.md", 1, "26.7.0")])
    # Population: the walk must actually reach top-level docs/*.md — the blind spot — and
    # must keep the dated/archived records out. Measured against the real tree, so a prune
    # that grows or a walk that stops rooting at docs/ fails here first.
    dfs = docs_files()
    expect("docs walk reaches the top level (the blind spot this closes)",
           any(os.path.dirname(f) == "docs" for f in dfs))
    # Derived from DATED_PRUNE rather than spelling the directories: this script SHIPS, and
    # the export's leak gate refuses those paths written out (measured — it turned
    # lint:export red). Deriving is also the stronger assertion: it covers every pruned
    # name, including ones added after this line was written.
    expect("docs walk keeps dated ADRs and archived records out of scope",
           not any(f.split("/")[1] in DATED_PRUNE for f in dfs if "/" in f))
    # The prune is by EXACT name. Asserted on synthetic names so it holds whether or not
    # such directories exist today — a substring prune passes every test the real tree can
    # offer right now and starts swallowing live docs the day one is created.
    expect("prune is exact: adr/ and archive/ are out",
           pruned_dir("adr") and pruned_dir("archive") and pruned_dir("2026-06"))
    expect("prune is exact: adrian/ and archived-decisions/ stay IN scope",
           not pruned_dir("adrian") and not pruned_dir("archived-decisions")
           and not pruned_dir("adr-rationale"))
    floor = docs_floor(profile)
    expect(f"docs walk is above its enumeration floor ({profile} record: floor {floor['floor']}, "
           f"measured {floor['measured']} on {floor['date']})",
           len(dfs) >= floor["floor"])
    expect("the public record is earned only by a stamped export: hub and unknown are held to the full source tree's floor",
           docs_floor("public") is DOCS_LAST_GOOD_PUBLIC and docs_floor("hub") is DOCS_LAST_GOOD
           and docs_floor("unknown") is DOCS_LAST_GOOD)
    # WIRING, not just the function. Every case above feeds judge() a synthetic tree, so
    # all of them would still pass if docs_files() were computed and then never handed to
    # the scan — the walk would be dead code and the blind spot would be back, silently.
    # This is the witness that kills that mutant, and the only one that can.
    expect("surface_files() actually WIRES the docs walk into the census",
           any(os.path.dirname(f) == "docs" for f in surface_files()))
    # The min-version allowance is granted BY PATH. A bound written in a doc that no record
    # exempts earns nothing — otherwise `>=` anywhere would be a universal escape hatch.
    tree = {"docs/SOME-OTHER-GUIDE.md": "requires olivares >= 26.7.0 at minimum"}
    expect("bound in a doc with NO min-version record -> red (the allowance is by path)",
           judge("v26.8.0", scan(tree, rd(tree)))[0]
           == [("docs/SOME-OTHER-GUIDE.md", 1, "26.7.0")])

    # ── internal development context and Go test fixtures (added 2026-09-05) ──
    # The measured pair: the mandate's NEXT target and a multi-version upgrade fixture. Each
    # exemption has its positive control beside it — the same token one directory over, or in
    # production Go, or in a manifest, stays red — and the docs prune is proven ON THE REAL TREE
    # both ways: with the export's curation it is out, with the curation gone it is back.
    # The parser and the prune, BOTH profiles, on the in-memory export script above: `syn_cur`
    # names the internal context and ONE synthetic directory this file does not name. The second
    # name is synthetic on purpose — a real curated directory spelled here is a STRING VALUE in a
    # script that SHIPS, and on 2026-09-06 `lint:export` reported exactly that as a leak. The full source tree
    # branch below reads the REAL curation on top, so every curated directory is still covered
    # there, including ones added after this line. The fixture path is synthetic too; rd(tree)
    # reads the in-memory dictionary further down.
    syn_cur = curated_out(read=lambda _: syn_export)
    expect("DOCS_BLOCK parser: the curation is READ out of the export script text, comments stripped",
           syn_cur == {INTERNAL_CONTEXT[0][0], "docs/fixture-curated-out"})
    cur = syn_cur
    expect("internal context: the mandate under a curated-out directory is pruned",
           internal_context("docs/ai-context/fixture-next-release.md", cur))
    expect("internal context: WITHOUT the curation the same path is NOT pruned (judged)",
           not internal_context("docs/ai-context/fixture-next-release.md", set()))
    expect("internal context: exact directory, never a substring (docs/ai-context-notes stays in)",
           not internal_context("docs/ai-context-notes/x.md", cur)
           and not internal_context("docs/OTHER-RUNBOOK.md", cur))
    # The exemption is granted by the INTERSECTION of INTERNAL_CONTEXT and the curation, never
    # by the curation alone. `outside` is every curated entry this file does not name; it is
    # asserted NON-EMPTY first, because `any()` over an empty set is a vacuous green and would
    # turn this control into decoration the day INTERNAL_CONTEXT grew to cover the whole block.
    outside = {d for d in cur if not internal_context(d, cur)}
    expect("internal context: a directory the export curates but this file does not name earns nothing",
           len(outside) > 0
           and not any(internal_context(d + "/x.md", cur) for d in outside))
    expect("an export script of UNKNOWN shape grants no curation (empty set, so nothing is pruned)",
           curated_out(read=wrong_shape) == set())
    expect("a tree WITHOUT the export script grants no curation either",
           curated_out(read=missing) == set())
    code, msg = refusal(lambda: curated_out(read=unreadable))
    expect("a PRESENT but unreadable export script grants no curation and refuses (exit 2), as the census does",
           code == 2 and "unreadable" in msg)
    internal = tuple(d + "/" for d, _ in INTERNAL_CONTEXT)
    with_cur = docs_files()
    without_cur = docs_files(curated=set())
    if hub:
        # The REAL curation, on the REAL tree: read from the export script, wider than this file,
        # and the prune is proven both ways — with it the internal context is out, without it
        # the same walk brings it straight back. Only the full source tree has both halves on disk.
        real_cur = curated_out()
        expect("the docs curation is read from export-public.sh, not written here",
               "docs/ai-context" in real_cur and len(real_cur) > 5)
        real_outside = {d for d in real_cur if not internal_context(d, real_cur)}
        expect("the real curation removes directories this file does not name, and they earn nothing",
               len(real_outside) > 0
               and not any(internal_context(d + "/x.md", real_cur) for d in real_outside))
        expect("docs walk WITH the curation keeps docs/ai-context out",
               not any(f.startswith(internal) for f in with_cur))
        expect("docs walk WITHOUT the curation brings docs/ai-context straight back (the prune is the curation, not this file)",
               any(f.startswith(internal) for f in without_cur) and len(without_cur) > len(with_cur))
        expect("the prune removes only that directory: every other docs/ file is walked either way",
               set(with_cur) == {f for f in without_cur if not f.startswith(internal)})
    else:
        # The exported profile, validated on the exported tree: the curation already happened
        # upstream, so nothing here may be pruned on this file's word, the internal context must
        # be gone, and the walk must not change whether or not a curation is handed to it.
        expect("public export: no export script, so the real curation grants nothing (nothing is pruned on this file's word)",
               curated_out() == set())
        expect("public export: no internal-context directory exists in the exported tree (the curation removed it)",
               not any(os.path.exists(d) for d, _ in INTERNAL_CONTEXT))
        expect("public export: the docs walk is identical with and without a curation, and carries no internal context",
               with_cur == without_cur and not any(f.startswith(internal) for f in with_cur))
    tree = {"docs/ai-context/fixture-next-release.md": "the owner fixed **v26.9.0** as the delivery objective"}
    expect("judged by itself the mandate token IS divergent: the exemption lives in the census, not in the judge",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [("docs/ai-context/fixture-next-release.md", 1, "v26.9.0")])
    tree = {"docs/RELEASE-CANDIDATE-NOTES.md": "the next release will be v26.9.0"}
    expect("a NEW target in a published doc stays red (no blanket skip of docs/)",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [("docs/RELEASE-CANDIDATE-NOTES.md", 1, "v26.9.0")])
    tree = {"docs-site/src/content/docs/how-to/install-from-packages.md": "The v26.7.0 GitHub release publishes `.deb`, `.rpm` and `.apk` assets"}
    expect("a STALE version in a published guide stays red",
           judge("v26.8.0", scan(tree, rd(tree)))[0] == [("docs-site/src/content/docs/how-to/install-from-packages.md", 1, "v26.7.0")])
    expect("a STALE bundle FILENAME in a published guide is a pin arm B judges red",
           judge_pins("v26.8.0", [("docs-site/src/content/docs/how-to/air-gap-install.md", 1, m.group(0), "x", False)
                                  for m in PIN.finditer("curl -O https://dl/olivares-26.7.0-linux-amd64.tar.gz")],
                      curated=set())
           == [("docs-site/src/content/docs/how-to/air-gap-install.md", 1, "olivares-26.7.0")])
    pins8 = lambda text, path: judge_pins(
        "v26.8.0", [(path, i, m.group(0), ln, False)
                    for i, ln in enumerate(text.splitlines(), 1) for m in PIN.finditer(ln)],
        curated=set())
    expect("Go test fixture: the multi-version regression's next-version key is test data -> green",
           pins8('b.mint("link9", "26.9.0"); readsContain(b.reads(), "olivares_26.9.0")\n',
                 "cmd/olivares/cmd_upgrade_releasev1_bridge_test.go") == [])
    expect("Go test fixture: a STALE key in a _test.go is test data too (a multi-version case needs both sides)",
           pins8('readsContain(b.reads(), "olivares_26.7.0")\n', "cmd/olivares/x_test.go") == [])
    expect("production Go: the same NEW pin in a non-test .go file stays red",
           pins8('const key = "olivares_26.9.0"\n', "cmd/olivares/cmd_upgrade.go")
           == [("cmd/olivares/cmd_upgrade.go", 1, "olivares_26.9.0")])
    expect("production Go: a STALE pin in a non-test .go file stays red",
           pins8('const key = "olivares_26.7.0"\n', "core/release/x.go")
           == [("core/release/x.go", 1, "olivares_26.7.0")])
    expect("manifest: a NEW image pin in deploy/ stays red (a target is not what ships)",
           pins8("  image: docker.io/olivaresai/olivares:26.9.0\n", "deploy/manifests/install.yaml")
           == [("deploy/manifests/install.yaml", 1, "olivaresai/olivares:26.9.0")])
    expect("the fixture class is Go's rule, not a directory: testdata-looking .go that is not _test.go is judged",
           len(pins8('"olivares_26.7.0"\n', "cmd/olivares/testdata_helpers.go")) == 1
           and go_test_fixture("a/b_test.go") and not go_test_fixture("a/b_test.go.txt"))

    # ── the dated record of a release that already happened (added 2026-09-15) ──
    # THE CASE THIS EXISTS FOR, measured on the v26.9.0 cut: with the canon re-derived, 383 of
    # the 777 shaped tokens sat in documents whose whole subject is a release that SHIPPED — the
    # `[26.8.0]` changelog section, the dated install-surface witness, the launch pack that was
    # posted, the runbooks that cut the tag. Sweeping those to the new canon does not correct a
    # claim; it falsifies a record, which is the one thing this file has refused since the
    # dated-ADR prune. Every case below is the witness for one rule of the allowance, and each
    # green one is paired with the red that keeps it narrow.
    hist = {"docs/launch"}                        # one curated-out DIRECTORY member, injected
    cur2 = {"docs/launch", "docs/contracts"}      # both of them, for the contract cases
    # 1 · a record may name a release that HAPPENED …
    tree = {"docs/launch/fixture-release-post.md": "the first tag `v26.8.0` is published"}
    expect("historical: a launch record names the release it records -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/releases/v26.8.0-install-surfaces.json": '  "version": "v26.8.0",'}
    expect("historical: the dated install witness of a past release -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/RELEASE-GO-LIVE-RUNBOOK.md": "git tag -s v26.8.0 -m 'Olivares AI v26.8.0'"}
    expect("historical: the runbook that cut the past tag -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    # 2 · … and NEVER one that did not. A record is of the past, so the allowance is strictly
    #     below the canon: the canon itself is the live claim every other rule already judges,
    #     and anything above it is a release nobody cut.
    tree = {"docs/launch/fixture-release-post.md": "next up is v26.10.0"}
    expect("historical: a token ABOVE the canon inside a record -> red (no release was cut)",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0]
           == [("docs/launch/fixture-release-post.md", 1, "v26.10.0")])
    # 3 · the class is EXACT — the sibling gate's own warning, restated for every member here.
    #     A substring test reads identically and swallows live documentation merely NAMED like
    #     a record, so each of these is a file that LOOKS historical and is not.
    for path in ("docs/launch-notes/old.md", "docs/releases-archive/x.md",
                 "docs/claims/measurements-old/x.md", "docs/RELEASE-A-NEW-GUIDE.md",
                 "docs/contracts-draft/x.md", "docs/accessibility/VPAT-olivares-admin.md.bak"):
        tree = {path: "deploy olivares:26.8.0 to the fleet"}
        expect(f"historical: {path} is NOT a member -> red (exact path, never a substring)",
               judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [(path, 1, "26.8.0")])
    # 4 · a live surface is untouched by any of this: the stray past version stays red exactly
    #     where a reader would act on it. These three are the surfaces the brief names.
    for path in ("README.md", "INSTALL.md",
                 "docs-site/src/content/docs/how-to/install-from-packages.md"):
        tree = {path: "install the published `v26.8.0` release"}
        expect(f"historical: a stray past version on the live surface {path} -> red",
               judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [(path, 1, "v26.8.0")])
    # 5 · the two DIRECTORY members are tied to the export's own curation, exactly as the
    #     internal-context prune is: the allowance holds only while the curation says the
    #     directory never ships, and it stops with it. Fail-closed toward judging.
    tree = {"docs/launch/fixture-release-post.md": "the first tag `v26.8.0` is published"}
    expect("historical: WITHOUT the curation the launch record is judged again",
           judge("v26.9.0", scan(tree, rd(tree)), curated=set())[0]
           == [("docs/launch/fixture-release-post.md", 1, "v26.8.0")])
    # The second tied directory, and the case its tie exists for: a file the export RE-PUBLISHES
    # out of a blocked directory (DOCS_KEEP) is not covered by that directory's allowance, because
    # the tie's whole claim — "this never ships" — is false for it.
    tree = {"docs/contracts/fixture-contract-note.md": "el canon de este arbol era v26.8.0"}
    expect("historical: a curated-out contract record -> green with the curation, red without",
           judge("v26.9.0", scan(tree, rd(tree)), curated=cur2)[0] == []
           and judge("v26.9.0", scan(tree, rd(tree)), curated=set())[0]
           == [("docs/contracts/fixture-contract-note.md", 1, "v26.8.0")])
    tree = {"docs/contracts/kept-and-published.md": "el canon de este arbol era v26.8.0"}
    expect("historical: a file DOCS_KEEP re-publishes out of a tied directory earns nothing -> red",
           judge("v26.9.0", scan(tree, rd(tree)), curated=cur2,
                 kept={"docs/contracts/kept-and-published.md"})[0]
           == [("docs/contracts/kept-and-published.md", 1, "v26.8.0")]
           and judge("v26.9.0", scan(tree, rd(tree)), curated=cur2, kept=set())[0] == [])
    # The three kinds whose only member is a SHIPPED file get their green witness here, so the
    # table is not exercised by the real tree alone.
    tree = {"docs/claims/measurements/2026-09-12/AUDIT.md": "the public release of `v26.8.0` is a separate dimension"}
    expect("historical: a dated measurement names the release it measured -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/accessibility/VPAT-olivares-admin.md": "the v26.8.0 release run executed the gate over 59 routes"}
    expect("historical: a conformance record names the run that produced its evidence -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/RELEASE-CHANNEL-POLICY.md": "| GitHub release archives | live for v26.8.0 |"}
    expect("historical: a measured publication state is not swept to an unpublished version -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    # The witness ledger is narrower than its directory: only the version ON THE LABEL.
    tree = {"docs/releases/v26.8.0-install-surfaces.json": '  "version": "v26.8.0",'}
    expect("historical: a witness may name the version in its own filename -> green",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/releases/v26.8.0-install-surfaces.json": '  "evidence": "measured against 26.5.0",'}
    expect("historical: a witness naming ANOTHER past release -> red (the label is the allowance)",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0]
           == [("docs/releases/v26.8.0-install-surfaces.json", 1, "26.5.0")])
    tree = {"docs/releases/NOTES.md": "see v26.8.0"}
    expect("historical: a docs/releases file with NO version label earns nothing -> red",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0]
           == [("docs/releases/NOTES.md", 1, "v26.8.0")])
    # 6 · THE CHANGELOG IS NARROWER THAN THE REST, and this is the case that keeps the gate's
    #     founding defect caught. Its masthead — title, format note, status block — states what
    #     ships TODAY; only the sections below `## [Unreleased]` are the record.
    masthead = ("# Changelog\n\nCalVer `vYY.M.PATCH`; the current release is `v26.8.0`.\n\n"
                "## [Unreleased]\n\n## [26.8.0] - 2026-09-01\n\nTag `v26.8.0` points at a commit\n")
    tree = {"CHANGELOG.md": masthead}
    fails, _ = judge("v26.9.0", scan(tree, rd(tree)), curated=hist)
    expect("historical: the changelog MASTHEAD states the canon, the dated section keeps its own",
           fails == [("CHANGELOG.md", 3, "v26.8.0")])
    tree = {"CHANGELOG.md": "# Changelog\n\nthe release is `v26.8.0`\n"}
    expect("historical: a changelog with NO section heading is judged whole (fail-closed)",
           judge("v26.9.0", scan(tree, rd(tree)), curated=hist)[0]
           == [("CHANGELOG.md", 3, "v26.8.0")])
    expect("historical: dated_from finds the first section heading and only a heading",
           dated_from(masthead) == 5 and dated_from("# Changelog\n\n## [26.8.0] - 2026-09-01\n") == 3
           and dated_from("## [Not a version]\n") is None
           and dated_from("text `## [Unreleased]` quoted inline\n") is None)
    # 7 · ARM B follows the same class, because a record quotes image pins too (the `[26.8.0]`
    #     changelog section names `docker.io/olivaresai/olivares:26.8.0` on two lines).
    histpin = lambda text, path, dated: judge_pins(
        "v26.9.0", [(path, i, m.group(0), ln, dated)
                    for i, ln in enumerate(text.splitlines(), 1) for m in PIN.finditer(ln)],
        curated=hist)
    expect("historical, arm B: a past image pin inside a launch record -> green",
           histpin("  image: docker.io/olivaresai/olivares:26.8.0\n",
                   "docs/launch/fixture-index.md", False) == [])
    expect("historical, arm B: the same pin in a dated changelog section -> green",
           histpin("images `docker.io/olivaresai/olivares:26.8.0` mirrored\n",
                   "CHANGELOG.md", True) == [])
    expect("historical, arm B: the same pin in the changelog MASTHEAD -> red",
           histpin("images `docker.io/olivaresai/olivares:26.8.0` today\n",
                   "CHANGELOG.md", False)
           == [("CHANGELOG.md", 1, "olivaresai/olivares:26.8.0")])
    expect("historical, arm B: the same pin in a shipped manifest -> red (a record is not a coordinate)",
           histpin("  image: docker.io/olivaresai/olivares:26.8.0\n",
                   "deploy/manifests/install.yaml", False)
           == [("deploy/manifests/install.yaml", 1, "olivaresai/olivares:26.8.0")])
    # 8b · TWO PAST RELEASES, NOT ONE (added 2026-09-18). The v26.9.1 cut is the first with
    #      more than one shipped release behind it, and every case above happens to use a single
    #      past version. "Strictly below the canon" is what the rule says and "the previous
    #      release" is what a reader may assume it says; these cases hold it to the first.
    ledger = {"docs/launch/fixture-two-releases.md":
              "the first tag `v26.8.0` shipped, and `v26.9.0` followed it"}
    expect("historical: a record naming TWO past releases -> both green",
           judge("v26.9.1", scan(ledger, rd(ledger)), curated=hist)[0] == [])
    # The ledger holds one file per release, and each admits ONLY its own label. Asserted in
    # both directions, because "any past version under docs/releases" would pass one direction
    # and is exactly what the self-bound kind refuses.
    older = {"docs/releases/v26.8.0-install-surfaces.json": '  "version": "v26.8.0",'}
    newer = {"docs/releases/v26.9.0-install-surfaces.json": '  "version": "v26.9.0",'}
    crossed = {"docs/releases/v26.9.0-install-surfaces.json": '  "evidence": "measured against v26.8.0",'}
    expect("historical: each witness admits its own label and refuses its neighbour's",
           judge("v26.9.1", scan(older, rd(older)), curated=hist)[0] == []
           and judge("v26.9.1", scan(newer, rd(newer)), curated=hist)[0] == []
           and judge("v26.9.1", scan(crossed, rd(crossed)), curated=hist)[0]
           == [("docs/releases/v26.9.0-install-surfaces.json", 1, "v26.8.0")])
    # THE ONE THAT FIRED FOR REAL while this cut was written: the CURRENT release's witness
    # named the previous tag in an evidence sentence. The filename is the allowance, so the
    # canon's own witness may not name a past release either — measured, and the sentence was
    # rewritten rather than exempted.
    current = {"docs/releases/v26.9.1-install-surfaces.json":
               '  "evidence": "the v26.9.0 tag is published and keeps its own witness",'}
    expect("historical: the CURRENT witness naming a past release -> red (the label is the allowance)",
           judge("v26.9.1", scan(current, rd(current)), curated=hist)[0]
           == [("docs/releases/v26.9.1-install-surfaces.json", 1, "v26.9.0")])
    # A changelog with TWO dated sections: both are the record, and the masthead above them is
    # still the live claim. The founding defect of this gate lives in that masthead.
    two = ("# Changelog\n\nthe current release is `v26.9.0`.\n\n## [Unreleased]\n\n"
           "## [26.9.0] - 2026-09-16\n\nTag `v26.9.0` points at a commit\n\n"
           "## [26.8.0] - 2026-09-01\n\nTag `v26.8.0` points at another\n")
    tree = {"CHANGELOG.md": two}
    expect("historical: two dated sections are both the record; the masthead is not",
           judge("v26.9.1", scan(tree, rd(tree)), curated=hist)[0] == [("CHANGELOG.md", 3, "v26.9.0")])
    # And the generation that is NOT past: a record may not name the canon's successor, however
    # many releases sit below it.
    ahead = {"docs/launch/fixture-two-releases.md": "next up is v26.9.2"}
    expect("historical: with two releases behind it, a record still may not name one ahead",
           judge("v26.9.1", scan(ahead, rd(ahead)), curated=hist)[0]
           == [("docs/launch/fixture-two-releases.md", 1, "v26.9.2")])
    # 9 · A CITATION OF A DATED SECTION IS NOT A CLAIM ABOUT WHAT SHIPS (added 2026-09-18).
    #     Measured on the v26.9.1 cut: 146 of the 589 divergences were pages naming the
    #     `[26.9.0]` changelog section as the SOURCE of a behaviour they document — ten how-to
    #     and reference pages in seven locales, all written after the v26.9.0 tag. Sweeping a
    #     citation to the new canon does not correct a claim; it points the reader at a section
    #     that does not carry the entry. The class is bound by FORM, so every case below either
    #     proves the form or proves what the form refuses.
    cite = lambda text, path="docs-site/src/content/docs/how-to/x.md": judge(
        "v26.9.1", scan({path: text}, rd({path: text})), curated=hist)[0]
    expect("citation: a page naming a past changelog section -> green",
           cite("the turn ends and the process stays usable (`CHANGELOG.md` `[26.9.0]`).") == [])
    expect("citation: the same past version stated BARE on the same page -> red",
           cite("v26.9.0 keys every live session row by its own identity.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "v26.9.0")])
    expect("citation: a bracketed version on a line that does NOT name the changelog -> red",
           cite("pin the `[26.9.0]` image before you roll the fleet.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.9.0")])
    # EVERY occurrence, not any — the `bounded()` precedent, for the same reason: a line that
    # cites a section AND states the version bare is ambiguous, and an ambiguous claim is judged
    # as the stale claim it might be. Measured: seven real lines in seven locales read exactly
    # like this, and they are rewritten rather than exempted.
    expect("citation: a citation and a bare token on ONE line -> the bare one stays red",
           cite("Source for the v26.9.0 behavior: `CHANGELOG.md` section `[26.9.0]`.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "v26.9.0")])
    # THE SAME TOKEN TWICE, which is what "every" and "any" actually disagree about. The case
    # above has one bare occurrence and one bracketed one of DIFFERENT tokens (`v26.9.0` and
    # `26.9.0`), so either quantifier answers it. A mutation control found that gap: with `any`
    # the battery stayed green. This line is the one that fails under it.
    expect("citation: the same token cited AND stated bare -> red (every, not any)",
           cite("see `CHANGELOG.md` `[26.9.0]`, then install 26.9.0 on every node.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.9.0")] * 2)
    expect("citation: a section ABOVE the canon -> red (nobody wrote that section)",
           cite("see `CHANGELOG.md` `[26.9.2]` for the fix.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.9.2")])
    expect("citation: a hardened variant in brackets is not a section reference -> red",
           cite("see `CHANGELOG.md` `[26.9.0-fips]` for the hardened build.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.9.0-fips")])
    expect("citation: a wrapped citation is named as wrapped, not left as a puzzle",
           wrapped_citation("`[26.9.0]` Added; `INSTALL.md`).", "26.9.0")
           and not wrapped_citation("see `CHANGELOG.md` `[26.9.0]`", "26.9.0")
           and not wrapped_citation("pin `[26.9.0]` and 26.9.0 too", "26.9.0")
           and not wrapped_citation("deploy 26.9.0 today", "26.9.0"))
    # The changelog's OWN headings are reached by `changelog-history`, not by this rule: a
    # heading does not name the file it sits in. The separation is asserted on the SAME BYTES
    # in two places, so neither rule can be mistaken for the other.
    heading = "## [26.9.0] - 2026-09-16\n"
    expect("citation: the changelog's own heading is the record; the same bytes on a page are not",
           judge("v26.9.1", scan({"CHANGELOG.md": heading}, rd({"CHANGELOG.md": heading})),
                 curated=hist)[0] == []
           and cite(heading) == [("docs-site/src/content/docs/how-to/x.md", 1, "26.9.0")])
    # A record that cites a section keeps its own allowance too, so the two never compete.
    expect("citation: a launch record citing a section is still a record -> green",
           judge("v26.9.1", scan({"docs/launch/fixture-post.md": "see `CHANGELOG.md` `[26.8.0]`"},
                                 rd({"docs/launch/fixture-post.md": "see `CHANGELOG.md` `[26.8.0]`"})),
                 curated=hist)[0] == [])
    # 9b · THE EXCLUDED EMITTED-URL RECORD (added 2026-09-23). docs/emitted-urls-hub-record.txt
    #      declares URLs that only curated-out files emit, among them the v26.8.0 release page a
    #      posted launch record links. A past release there is a record, and only while the export
    #      really drops that exact file: without the curation, from any other path, above the canon,
    #      or re-published by DOCS_KEEP, the same bytes are red.
    rec_path = "docs/emitted-urls-hub-record.txt"
    rec_line = "https://github.com/olivaresai/olivares/releases/tag/v26.8.0 200 2026-09-23"
    rec = lambda path, text=rec_line, cur=frozenset({rec_path}), kept=frozenset(): judge(
        "v26.9.0", scan({path: text}, rd({path: text})), curated=set(cur), kept=set(kept))[0]
    expect("excluded record: a past release page in the exact record the export drops -> green",
           rec(rec_path) == [])
    expect("excluded record: WITHOUT its export exclusion the same line is red",
           rec(rec_path, cur=frozenset()) == [(rec_path, 1, "v26.8.0")])
    expect("excluded record: re-published by DOCS_KEEP it earns nothing -> red",
           rec(rec_path, kept=frozenset({rec_path})) == [(rec_path, 1, "v26.8.0")])
    expect("excluded record: a sibling path is not the record -> red (exact file)",
           rec("docs/emitted-urls-hub-record-old.txt", cur=frozenset({rec_path, "docs/emitted-urls-hub-record-old"}))
           == [("docs/emitted-urls-hub-record-old.txt", 1, "v26.8.0")])
    expect("excluded record: a release ABOVE the canon there -> red (nobody published it)",
           rec(rec_path, text="https://github.com/olivaresai/olivares/releases/tag/v26.10.0 404 2026-09-23 x")
           == [(rec_path, 1, "v26.10.0")])
    # 9c · THE PUBLISHED BASELINE AND THE PENDING RELEASE (added 2026-09-23). The canon is the
    #      published install baseline and the next release is pending. The canon's same-month patch
    #      is not an example anyone may write: it names a release nobody cut. The pending release is
    #      written in its two-part form, which is no version token; its tag form is judged like any
    #      other version (there is no pending-target allowance); and past releases keep their records.
    live = lambda path, text: judge("v26.9.0", scan({path: text}, rd({path: text})), curated={"docs/launch"})[0]
    for path in ("INSTALL.md", "docs-site/src/content/docs/how-to/docker-deployment.md",
                 "docs/UPGRADE-AND-ROLLBACK.md", "docs/PSIRT-RUNBOOK.md"):
        expect(f"stale: the canon's next patch in {path} -> red (it names a release nobody cut)",
               live(path, "a same-month fix is v26.9.1") == [(path, 1, "v26.9.1")])
    expect("stale: a current-install pin one patch above the canon -> red",
           live("INSTALL.md", "docker pull docker.io/olivaresai/olivares:26.9.1")
           == [("INSTALL.md", 1, "26.9.1")])
    expect("pending: the next release in its two-part form (v26.10) is not a version token",
           scan({"INSTALL.md": "the next release is v26.10, pending"},
                rd({"INSTALL.md": "the next release is v26.10, pending"})) == [])
    expect("pending: its tag form on a live surface is judged like any version -> red (no pending-target allowance)",
           live("INSTALL.md", "the next tag is v26.10.0") == [("INSTALL.md", 1, "v26.10.0")])
    base2 = ("# Changelog\n\nthe latest published release is `v26.9.0`.\n\n## [Unreleased]\n\n"
             "pending for v26.10\n\n## [26.9.0] - 2026-09-16\n\nTag `v26.9.0`\n\n"
             "## [26.8.0] - 2026-09-01\n\nTag `v26.8.0`\n")
    expect("historical: the dated sections survive under the published baseline, and the masthead states it",
           live("CHANGELOG.md", base2) == [])
    expect("historical: a masthead naming a past release as the current one -> red",
           live("CHANGELOG.md", base2.replace("`v26.9.0`.", "`v26.8.0`.", 1)) == [("CHANGELOG.md", 3, "v26.8.0")])
    expect("historical: the dated witness of the previous release survives",
           live("docs/releases/v26.8.0-install-surfaces.json", '  "version": "v26.8.0",') == [])
    expect("historical: a witness labelled with a release above the canon -> red",
           live("docs/releases/v26.9.1-install-surfaces.json", '  "version": "v26.9.1",')
           == [("docs/releases/v26.9.1-install-surfaces.json", 1, "v26.9.1")])
    # 8 · every member carries a written reason, and the two tied ones are the two the export
    #     curates. Asserted so a member added without a reason, or a tie invented for a
    #     directory the export publishes, fails here rather than in review.
    expect("historical: every member names a kind and a written reason",
           len(HISTORICAL_ALLOW) > 0
           and all(isinstance(k, str) and k and len(r) > 30 for _, k, r in HISTORICAL_ALLOW))
    # ⛔ THE CONTROL THAT KEEPS THE TABLE HONEST, and it is derived rather than restated: a member
    # that is a DIRECTORY ON DISK grants an allowance to files nobody has written, so it must be
    # tied to the curation. A literal `CURATION_TIED == {...}` asserted nothing — it was the
    # constant read back to itself, and it sat there while three untied directory members were
    # added, two of which the export publishes. This reads the tree.
    dir_members = {m for m, k, _ in HISTORICAL_ALLOW
                   if os.path.isdir(m) and k not in SELF_BOUND_KINDS}
    expect(f"historical: every DIRECTORY member is tied to the curation or bound by the file itself "
           f"(untied and unbound: {sorted(dir_members - CURATION_TIED)})",
           dir_members and dir_members <= CURATION_TIED
           and CURATION_TIED <= {m for m, _, _ in HISTORICAL_ALLOW})
    expect("historical: a self-bound kind really is bound to the file (an unlabelled member earns nothing)",
           SELF_BOUND_KINDS == {"release-witness"}
           and witness_version("docs/releases/v26.8.0-install-surfaces.json") == "v26.8.0"
           and witness_version("docs/releases/NOTES.md") is None
           and witness_version("docs/releases/v26.8.0.md") is None)
    if hub:
        # … and the tie is only worth having if the export really removes them, DOCS_KEEP
        # included: a directory that re-publishes one file is not a directory that never ships.
        real_cur = curated_out()
        expect("historical: every tied member is one the export actually removes (DOCS_BLOCK minus DOCS_KEEP)",
               CURATION_TIED <= real_cur)
        # DOCS_KEEP is READ, like the block list, and it is the thing that keeps a tied
        # directory honest. Parsed from the export text so the public profile proves it too.
        syn_keep = curation_kept(read=lambda _: "DOCS_KEEP=(\n  docs/x/a.md  # comment\n  docs/y/b.md\n)\n")
        expect("historical: the DOCS_KEEP parser reads the re-published files, comments stripped",
               syn_keep == {"docs/x/a.md", "docs/y/b.md"})
        expect("historical: the real curation names files it re-publishes out of a tied directory",
               any(k.startswith("docs/contracts/") for k in curation_kept()))

    # ── enumeration and read failures REFUSE, and every enumerator is wired to it (2026-09-06) ──
    # Staged on disk under TMPDIR: os.walk's onerror and a reader's open() cannot be exercised by
    # an in-memory tree. Permission bits are the causal staging (the reviewed case was a directory
    # at mode 000); a process with uid 0 bypasses them, so the SAME failure is also staged in two
    # ways no uid bypasses — a path component that is not a directory, and a directory the walk is
    # told about but cannot enter. Nothing here is skipped: every staging that applies is asserted.
    def restore_modes(top):
        for r, ds, fs in os.walk(top):
            for d in ds:
                try:
                    os.chmod(os.path.join(r, d), 0o755)
                except OSError:
                    pass
            for f in fs:
                q = os.path.join(r, f)
                if not os.path.islink(q):
                    try:
                        os.chmod(q, 0o644)
                    except OSError:
                        pass
    stage = tempfile.mkdtemp(prefix="crv-selftest-", dir=os.environ.get("TMPDIR") or None)
    try:
        good = os.path.join(stage, "docs")
        os.makedirs(os.path.join(good, "open"))
        with open(os.path.join(good, "open", "a.md"), "w") as fh:
            fh.write("v26.8.0\n")
        with open(os.path.join(good, "top.md"), "w") as fh:
            fh.write("v26.8.0\n")
        rel = lambda paths: sorted(os.path.relpath(q, stage) for q in paths)
        expect("walk: a readable staged tree is walked whole (control)",
               rel(os.path.join(r, f) for r, _, fs in walk(good) for f in fs) == ["docs/open/a.md", "docs/top.md"])
        expect("walk: a root that does not exist yields nothing (a partial tree, not a failure)",
               list(walk(os.path.join(stage, "absent"))) == [])
        code, msg = refusal(lambda: list(walk(os.path.join(good, "top.md", "below"))))
        expect("walk: a root that exists but cannot be examined refuses (ENOTDIR), naming it",
               code == 2 and "enumeration failed" in msg and "top.md" in msg)
        def enter_vanished():
            for _, ds, _ in walk(good):
                ds.append("vanished")   # the walk is told to enter a directory it cannot
        code, msg = refusal(enter_vanished)
        expect("walk: a directory the walk cannot enter refuses through onerror, naming it — never a smaller census",
               code == 2 and "enumeration failed" in msg and "vanished" in msg)
        shut = os.path.join(good, "shut")
        os.makedirs(shut)
        with open(os.path.join(shut, "hidden.md"), "w") as fh:
            fh.write("v26.7.0\n")
        sealed = os.path.join(good, "sealed.md")
        with open(sealed, "w") as fh:
            fh.write("v26.7.0\n")
        perms = os.geteuid() != 0
        if perms:
            # The reviewed case, staged as reviewed: a stale token behind a directory at mode 000.
            os.chmod(shut, 0)
            code, msg = refusal(lambda: list(walk(good)))
            expect("walk: a subdirectory at mode 000 refuses, naming it (the reviewed case), never a smaller census",
                   code == 2 and "enumeration failed" in msg and "shut" in msg)
            os.chmod(shut, 0o755)
            os.chmod(sealed, 0)
            code, msg = refusal(lambda: read_surface(sealed))
            expect("read_surface: a listed file at mode 000 refuses, naming it — never '' and never a traceback",
                   code == 2 and "cannot be read" in msg and "sealed.md" in msg)
            os.chmod(sealed, 0o644)
            os.chmod(shut, 0o444)
            code, msg = refusal(lambda: [regular_file(os.path.join(shut, "hidden.md"))])
            expect("regular_file: an entry under a listable-but-unsearchable directory (mode 444) refuses, where os.path.isfile says False",
                   code == 2 and "cannot be examined" in msg and "hidden.md" in msg)
            os.chmod(shut, 0o755)
        else:
            expect("walk/read_surface/regular_file: permission stagings not applicable at uid 0; the ENOTDIR, onerror and dangling stagings above and below carry the same code paths",
                   True)
        dangling = os.path.join(good, "dangling.md")
        os.symlink(os.path.join(stage, "nowhere"), dangling)
        code, msg = refusal(lambda: read_surface(dangling))
        expect("read_surface: a listed entry that cannot be opened (dangling) refuses, naming it",
               code == 2 and "cannot be read" in msg and "dangling.md" in msg)
        code, msg = refusal(lambda: [regular_file(dangling)])
        expect("regular_file: a listed entry that cannot be examined (dangling) refuses, naming it",
               code == 2 and "cannot be examined" in msg and "dangling.md" in msg)
        with open(os.path.join(good, "blob.md"), "wb") as fh:
            fh.write(b"v26.7.0\0binary")
        expect("read_surface: a binary is '' by content and a text is its text (control)",
               read_surface(os.path.join(good, "blob.md")) == "" and read_surface(os.path.join(good, "top.md")) == "v26.8.0\n"
               and regular_file(os.path.join(good, "top.md")) and not regular_file(good))
    finally:
        restore_modes(stage)
        shutil.rmtree(stage, ignore_errors=True)
    # WIRING. The helpers refusing proves nothing if an enumerator still calls os.walk or
    # os.path.isfile directly, so each one is caught in the act of walking through walk().
    seen = []
    real_walk = walk
    def recording_walk(root):
        seen.append(root)
        return real_walk(root)
    globals()["walk"] = recording_walk
    try:
        docs_files(curated=set())
        surface_files()
        artifact_files()
        scan_pins(list(ARTIFACT_ROOTS))
    finally:
        globals()["walk"] = real_walk
    expect("every enumerator goes through the refusing walk: docs/, docs-site, the four extra bases, the artifact roots and the pin census",
           {"docs", "docs-site/src/content/docs", "deploy", "packaging", "examples", "oscap", "operator"} <= set(seen)
           and seen.count("deploy") >= 3)
    opened = []
    real_read = read_surface
    def recording_read(path):
        opened.append(path)
        return real_read(path)
    globals()["read_surface"] = recording_read
    try:
        scan_pins(["packaging"])
    finally:
        globals()["read_surface"] = real_read
    expect("the pin census reads through the refusing reader (no private open() that could skip a file)",
           any(q.startswith("packaging/") for q in opened))
    # ── root entries: PRESENT-but-unexaminable refuses, genuinely ABSENT keeps its semantics ──
    # R1-TOP (independent review, 2026-09-06): the reviewed staging is README.md as a link whose
    # target sits behind a directory at mode 000, or is gone. These cases run the REAL surfaces —
    # surface_files() and scan_pins() — from a staged working directory, so a private isfile()
    # creeping back into either admission fails here, not only in a helper test.
    stage = tempfile.mkdtemp(prefix="crv-root-", dir=os.environ.get("TMPDIR") or None)
    here = os.getcwd()
    try:
        troot = os.path.join(stage, "tree")
        vault = os.path.join(stage, "vault")
        os.makedirs(os.path.join(troot, "deploy"))
        os.makedirs(vault)
        with open(os.path.join(vault, "README.md"), "w") as fh:
            fh.write("ships v26.7.0\n")
        with open(os.path.join(troot, "CHANGELOG.md"), "w") as fh:
            fh.write("v26.8.0\n")
        with open(os.path.join(troot, "deploy", "x.yaml"), "w") as fh:
            fh.write("image: olivaresai/olivares:26.8.0\n")
        os.chdir(troot)
        expect("root_entry: absent, file and dir are told apart (control)",
               root_entry("INSTALL.md") == "absent" and root_entry("CHANGELOG.md") == "file"
               and root_entry("deploy") == "dir")
        expect("surface_files: a genuinely ABSENT optional root entry is simply not listed (README.md, INSTALL.md here)",
               surface_files() == ["CHANGELOG.md", "deploy/x.yaml"])
        expect("scan_pins: a genuinely ABSENT census entry is skipped and a present one is scanned (control)",
               [h[0] for h in scan_pins(["go.work.sum", "deploy"])] == ["deploy/x.yaml"])
        os.symlink(os.path.join(vault, "README.md"), "README.md")
        expect("root_entry: a present link with a reachable target is a file (control)",
               root_entry("README.md") == "file")
        expect("surface_files: the reachable link is admitted and scanned (control)",
               surface_files() == ["CHANGELOG.md", "README.md", "deploy/x.yaml"])
        os.rename(os.path.join(vault, "README.md"), os.path.join(vault, "README.off"))
        code, msg = refusal(lambda: root_entry("README.md"))
        expect("root_entry: a present but DANGLING root entry refuses, naming it",
               code == 2 and "'README.md'" in msg and "cannot be examined" in msg)
        code, msg = refusal(surface_files)
        expect("surface_files: the dangling README.md refuses through the REAL surface, before any isfile could drop it",
               code == 2 and "'README.md'" in msg)
        code, msg = refusal(lambda: scan_pins(["README.md"]))
        expect("scan_pins: the same dangling census entry refuses through the REAL census walk",
               code == 2 and "'README.md'" in msg)
        os.rename(os.path.join(vault, "README.off"), os.path.join(vault, "README.md"))
        # The entry itself cannot be looked up (lstat fails for a reason other than absence):
        # a path through a file (ENOTDIR, any uid), and a working directory that lost its
        # search bit (uid != 0). Neither is "absent", and neither may be read as it.
        code, msg = refusal(lambda: root_entry(os.path.join("CHANGELOG.md", "below")))
        expect("root_entry: an entry that cannot be looked up (ENOTDIR) refuses, naming it — not 'absent'",
               code == 2 and "below" in msg and "cannot be examined" in msg)
        if os.geteuid() != 0:
            os.chmod(troot, 0o444)
            code, msg = refusal(lambda: root_entry("CHANGELOG.md"))
            os.chmod(troot, 0o755)
            expect("root_entry: an entry under a working directory without its search bit refuses — not 'absent'",
                   code == 2 and "'CHANGELOG.md'" in msg and "cannot be examined" in msg)
            os.chmod(vault, 0)
            code, msg = refusal(surface_files)
            expect("surface_files: a present README.md whose target sits behind a directory at mode 000 refuses, naming it (the reviewed case)",
                   code == 2 and "'README.md'" in msg and "cannot be examined" in msg)
            code, msg = refusal(lambda: scan_pins(["README.md"]))
            expect("scan_pins: the same entry refuses in the pin census (the reviewed case)",
                   code == 2 and "'README.md'" in msg)
            os.chmod(vault, 0o755)
        else:
            expect("surface_files/scan_pins: the mode-000 target staging is not applicable at uid 0; the dangling staging above carries the same admission path",
                   True)
    finally:
        os.chdir(here)
        for d in (troot, vault):
            try:
                os.chmod(d, 0o755)
            except OSError:
                pass
        shutil.rmtree(stage, ignore_errors=True)
    # WIRING, on the real tree: each root admission is caught going through root_entry().
    admitted = []
    real_root_entry = root_entry
    def recording_root_entry(name):
        admitted.append(name)
        return real_root_entry(name)
    globals()["root_entry"] = recording_root_entry
    try:
        surface_files()
        scan_pins(["README.md", "deploy"])
    finally:
        globals()["root_entry"] = real_root_entry
    expect("every root admission goes through root_entry: CHANGELOG.md, README.md and INSTALL.md in surface_files, each census entry in scan_pins",
           {"CHANGELOG.md", "README.md", "INSTALL.md", "deploy"} <= set(admitted) and admitted.count("README.md") >= 2)
    code, msg = refusal(lambda: read_export_script(read=lambda _: b"\xff\xfe".decode("utf-8")))
    expect("an export script that is present but not UTF-8 -> UNVERIFIED (exit 2), never a traceback",
           code == 2 and "not decodable" in msg)
    # ── the RUN refuses what the battery refuses: unknown profile, mismatched census ──
    code, msg = refusal(lambda: require_known_profile("unknown", "the classifier is missing"))
    expect("run: an unknown profile is refused before anything is certified (exit 2), whatever the volume",
           code == 2 and "classifies as unknown" in msg)
    expect("run: hub and public are the only profiles the run certifies",
           require_known_profile("hub", "x") is None and require_known_profile("public", "x") is None)
    code, msg = refusal(lambda: require_census_matches("hub", "the tree itself (the script is absent)"))
    expect("run: a hub whose census fell back to the tree itself is refused (classification and census disagree)",
           code == 2 and "disagree" in msg)
    code, msg = refusal(lambda: require_census_matches("public", f"{EXPORT_SCRIPT} TOP_ALLOW"))
    expect("run: a public tree whose census came from an export script is refused too",
           code == 2 and "disagree" in msg)
    expect("run: hub+TOP_ALLOW and public+fallback are the two coherent pairs",
           require_census_matches("hub", f"{EXPORT_SCRIPT} TOP_ALLOW") is None
           and require_census_matches("public", "the tree itself (absent)") is None)

    # ── artifact surfaces: the shipped coordinates (added 2026-08-14) ──
    # The gate scanned documentation only and printed OK while deploy/, packaging/ and
    # operator/ shipped 26.7.0 under a v26.8.0 canon. Each case below is the witness for
    # one rule; none of them can pass by scanning prose.
    tree = {"deploy/helm/olivares/Chart.yaml": 'appVersion: "26.7.0"'}
    expect("stale chart appVersion -> red (the default helm-install image tag)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [("deploy/helm/olivares/Chart.yaml", 1, "26.7.0")])
    tree = {"deploy/helm/olivares/Chart.yaml": 'appVersion: "26.8.0"'}
    expect("chart appVersion at canon -> green", judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [])
    # A floor is not a claim about what ships: it must NOT be swept to the canon, but it
    # may never EXCEED it — you cannot require an engine newer than the one you ship.
    tree = {"operator/README.md": "must serve /pod-readyz (olivares >= 26.7.0) and clients"}
    expect("compatibility floor below canon -> green (a floor is not a shipped coordinate)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [])
    tree = {"operator/README.md": "must serve /pod-readyz (olivares >= 26.9.0) and clients"}
    expect("compatibility floor ABOVE canon -> red (requires an engine we do not ship)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [("operator/README.md", 1, "26.9.0")])
    tree = {"operator/README.md": "the image we publish is olivares:26.7.0"}
    expect("min-version path, token OFF the bound line -> red (narrower allowance, not a waiver)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [("operator/README.md", 1, "26.7.0")])
    tree = {"packaging/docker/dockerhub-overview.md": "| `26.8.0-fips` | FIPS build |"}
    expect("fips variant at canon on the Docker Hub page -> green",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [])
    tree = {"packaging/docker/dockerhub-overview.md": "| `26.7.0-fips` | FIPS build |"}
    expect("STALE fips variant on the Docker Hub page -> red (a tag the release never pushes)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [("packaging/docker/dockerhub-overview.md", 1, "26.7.0-fips")])
    tree = {"deploy/manifests/install.yaml": "image: docker.io/olivaresai/olivares:26.8.0-fips"}
    expect("fips variant OUTSIDE its documented page -> red",
           len(judge_artifacts("v26.8.0", scan(tree, rd(tree)))) == 1)
    # ── the floor whose path is DISCOVERED, not spelled (CRD_TYPES_GLOB) ──
    # These witnesses are derived for the same reason the record is: a literal here would
    # put the pre-rename product word back into a script that ships. They fail loudly if the
    # discovery stops resolving — the first on the count, the second because a path no
    # allowance covers turns a legitimate floor red.
    crds = crd_types_files()
    expect("CRD API type declaration discovered (the floor scope is derived, not spelled)",
           len(crds) >= 1)
    crd = crds[0] if crds else os.path.join("operator", "api", "v1alpha1", "undiscovered_types.go")
    tree = {crd: "//     role label (olivares >= 26.7.0). With an older image every pod fails"}
    expect("CRD-types floor below canon -> green (discovery preserves the exemption)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [])
    tree = {crd: "//     role label (olivares >= 26.9.0). With an older image every pod fails"}
    expect("CRD-types floor ABOVE canon -> red (requires an engine we do not ship)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [(crd, 1, "26.9.0")])
    tree = {crd: "//     the image we publish is olivares:26.7.0"}
    expect("CRD-types, token OFF the bound line -> red (narrower allowance, not a waiver)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree))) == [(crd, 1, "26.7.0")])
    tree = {"operator/internal/controller/reconcile.go": "// needs olivares >= 26.7.0 to start"}
    expect("floor in an operator file OUTSIDE the discovered scope -> red (scope did not widen)",
           judge_artifacts("v26.8.0", scan(tree, rd(tree)))
           == [("operator/internal/controller/reconcile.go", 1, "26.7.0")])
    # ── NO SUCCESS ESCAPE (2026-09-23) ──
    # RELEASE-VERSION is the published install baseline, so a tree always has one. UNDECIDED used
    # to put the run into a report mode that printed the census and exited 0: a green with no canon
    # behind it. It is refused now, by the schema and by every judge, and never with exit 0.
    code, msg = refusal(lambda: read_canon("# c\nUNDECIDED\n"))
    expect("UNDECIDED canon -> refused (exit 2), never a success", code == 2 and "UNDECIDED" in msg)
    und = scan({"deploy/x.yaml": "26.7.0"}, rd({"deploy/x.yaml": "26.7.0"}))
    expect("no judge treats UNDECIDED as a pass: documents, pins and artefacts all refuse (exit 2)",
           refusal(lambda: judge("UNDECIDED", und))[0] == 2
           and refusal(lambda: judge_pins("UNDECIDED", und, curated=set(), kept=set()))[0] == 2
           and refusal(lambda: judge_artifacts("UNDECIDED", und))[0] == 2)
    print("selftest " + ("OK — every red case is red, every green case is green" if ok else "FAILED"))
    sys.exit(0 if ok else 1)

if SELFTEST:
    selftest()

# ⛔ SIN ESTA GUARDA el gate moría con un FileNotFoundError de Python: un traceback y rc=1, que
# se lee como «he mirado y la versión diverge» cuando en realidad no había nada que leer.
try:
    _release_version_raw = open("RELEASE-VERSION", encoding="utf-8").read()
except OSError as exc:
    unverified(f"UNVERIFIED check-release-version: NO HE PODIDO MIRAR — RELEASE-VERSION is "
               f"unreadable ({exc}); there is no canon to compare against, which is not the "
               f"same as a canon that disagrees.")
canon = read_canon(_release_version_raw)

# ── Which tree, BEFORE anything is walked: a tree of unknown profile is refused here, not held
# to a floor it may happen to clear (see require_known_profile). ─────────────────────────────
profile, profile_why = tree_profile()
require_known_profile(profile, profile_why)

files = surface_files()

# ── Documentary walk, enumerated fail-closed ──────────────────────────────────────────
# Same contract as the artifact walk: a shrunken enumeration means the walk stopped
# working (a moved directory, a rename, a prune that grew), not that the prose stopped
# declaring. Left silent it would print OK over an unscanned docs/ — which is exactly the
# state this section was added to end.
dfiles = docs_files()
dtop = [f for f in dfiles if os.path.dirname(f) == "docs"]
floor = docs_floor(profile)
if len(dfiles) < floor["floor"] or len(dtop) < floor["top"]:
    print(f"check-release-version: {len(dfiles)} file(s) under docs/ ({len(dtop)} of them at the "
          "top level); the documentary release surfaces are UNVERIFIED.", file=sys.stderr)
    print(f"  This tree classifies as {profile} ({profile_why}); the record applied is the "
          f"{'public export' if floor is DOCS_LAST_GOOD_PUBLIC else 'hub'} one.", file=sys.stderr)
    print(f"  On {floor['date']} this same walk found {floor['measured']} "
          f"file(s): {floor['note']}.", file=sys.stderr)
    print(f"  Floor is {floor['floor']} file(s) and at least {floor['top']} "
          "directly under docs/ — a result below either means the walk broke (a moved root, a",
          file=sys.stderr)
    print("  rename, a prune that grew), not that the documentation stopped declaring versions.",
          file=sys.stderr)
    sys.exit(2)

hits = scan(files, read_surface)
failures, census = judge(canon, hits)

tops, census_src = published_tops()
require_census_matches(profile, census_src)
pin_hits = scan_pins(tops)
pin_failures = judge_pins(canon, pin_hits)
failures = failures + [f for f in pin_failures if f not in failures]
# ── Artifact surfaces, enumerated fail-closed ─────────────────────────────────────────
afiles = artifact_files()
ahits = scan(afiles, read_surface)
if len(afiles) < LAST_GOOD["files"] or not ahits:
    print(f"check-release-version: {len(afiles)} file(s) and {len(ahits)} version token(s) found "
          f"under {'/, '.join(ARTIFACT_ROOTS)}/; the shipped release coordinates are UNVERIFIED.",
          file=sys.stderr)
    print(f"  On {LAST_GOOD['date']} this same walk found {LAST_GOOD['tokens']} token(s) in "
          f"{LAST_GOOD['files']} file(s): {LAST_GOOD['note']}.", file=sys.stderr)
    print("  An empty or shrunken result means the walk stopped working — a moved directory, a", file=sys.stderr)
    print("  rename, a prune that grew — not that the declarations went away. Check ARTIFACT_ROOTS.", file=sys.stderr)
    sys.exit(2)
if not crd_types_files():
    # Same contract one level down: the min-version scope of the CRD API types is derived,
    # so a discovery that resolves to nothing is COULD NOT LOOK, not "no exemption needed".
    # Left silent it would judge a legitimate compatibility floor as a stale coordinate.
    print(f"check-release-version: no CRD API type declaration matched {CRD_TYPES_GLOB}; the "
          "compatibility-floor scope is UNVERIFIED.", file=sys.stderr)
    print("  The operator's API package moved or was renamed — the floors it declares did not", file=sys.stderr)
    print("  go away. Fix the discovery; do not write the path back (it carries the pre-rename", file=sys.stderr)
    print("  product word, and the public-export leak gate refuses that word in scripts/).", file=sys.stderr)
    sys.exit(2)
afailures = judge_artifacts(canon, ahits)

if failures or afailures:
    total = len(failures) + len(afailures)
    print(f"FAIL check-release-version: canon is {canon}; {total} divergent occurrence(s):")
    base = canon.lstrip("v")
    text_at = {(p, i): ln for p, i, _t, ln, _d in hits + pin_hits}
    for p, i, tok in failures[:40]:
        note = ""
        if tok.lstrip("v").replace("-fips", "").replace("-stig", "") == base:
            note = ("  <- the canon's HARDENED tag, not another version: if this file documents "
                    "the artefact matrix, give it a 'variants' record in DERIVED_ALLOW")
        elif wrapped_citation(text_at.get((p, i), ""), tok):
            note = ("  <- a changelog SECTION reference whose line names no changelog: if the "
                    "citation wrapped, put `CHANGELOG.md` and the section on ONE line")
        print(f"  {p}:{i}: {tok}{note}")
    for p, i, tok in afailures[:40]:
        print(f"  {p}:{i}: {tok}   [shipped release coordinate]")
    sys.exit(1)
print(f"OK check-release-version: every release-bearing surface states {canon} (or a derived form) "
      f"\u2014 {len(files)} documentation surface(s) scanned whole, plus {len(pin_hits)} artefact pin(s) "
      f"across the {len(tops)} published top-level entries (census: {census_src}; tree: {profile})")
print(f"OK check-release-version: {len(ahits)} shipped release coordinate(s) in {len(afiles)} walked "
      f"file(s) under {'/, '.join(ARTIFACT_ROOTS)}/ agree with the canon")
PY
