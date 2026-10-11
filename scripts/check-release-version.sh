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
# source of truth, and every product release coordinate
# on a release-bearing surface must equal it — or its one derived form: the -fips / -stig
# image variants, where image tags are documented. Since 2026-10-09 a coordinate is MARKED in
# the source, and --stamp rewrites the marks only (see MARKED RELEASE POSITIONS below). A mark
# is no waiver: everything inside one is judged against the canon. The next-patch upgrade example is gone
# (2026-09-23): the canon is the published baseline, and its same-month patch names a release
# nobody cut, so no document may state it as an example.
#
# RELEASE-VERSION always holds the current release build identity, including before publication. The report
# mode this gate once had for UNDECIDED (print the census, exit 0 with a PENDING banner) was
# removed on 2026-09-23, because it was a green with no canon behind it: UNDECIDED, or any
# record that is not bare MAJOR.MINOR, is refused before any surface is read, and every judge
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
# And the ROOT ENTRIES (finding 1-TOP, same review, one round later): the three helpers refuse what
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
# Usage: check-release-version.sh [--selftest|--stamp|--stamp-compose]
set -eu
CDPATH='' cd -- "$(dirname -- "$0")/.."

CRV_SELFTEST=0
[ "${1:-}" = "--selftest" ] && CRV_SELFTEST=1
export CRV_SELFTEST
CRV_STAMP_COMPOSE=0
[ "${1:-}" = "--stamp-compose" ] && CRV_STAMP_COMPOSE=1
export CRV_STAMP_COMPOSE
CRV_STAMP=0
[ "${1:-}" = "--stamp" ] && CRV_STAMP=1
export CRV_STAMP

python3 - <<'PY'
import bisect, collections, fnmatch, glob, io, os, re, shutil, stat, subprocess, sys, tempfile, time, warnings

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


# ── ONE MATCHER, ONE TABLE (2026-10-08) ───────────────────────────────────────────────────────
# Every scanner, pin check and judge in this file reads versions through tokens() and nothing
# else, and every judge decides what a token is through stated() and the retired list. A token
# is COMPLETE: all of its numeric components and every suffix, whether it follows `-`, `+`, `~`,
# `_` or nothing at all (`1.0-rc.1`, `1.0+meta`, `1.0~rc1`, `1.0_rc1`, `1.0rc1`). The partial
# matchers this replaces stopped at the valid core, one pattern per judge: measured 2026-10-08,
# `appVersion: "1.0-rc.1"`, a `1.0+meta` release and the image `olivaresai/olivares:1.0-rc.1`
# all read as the canon 1.0, and `1.0.1.2` was not seen at all.
# Archive and package names separate their FIELDS with `_` (olivares_<version>_<os>_<arch>,
# olivares_<version>_fips_<os>_<arch>, olivares_<version>_amd64.deb): before one of those field
# words `_` ends the token; anywhere else it is a suffix, as in Arch's `pkgver = 1.0_rc1`. In the
# same way a `.word` is a suffix (`1.0.rc1`, `1.0.beta`) unless the word is a file extension or a
# package architecture (`1.0.tar.gz`, `1.0-1.x86_64.rpm`) or the `.x` of a release series.
# A version right after `..` (the right-hand side of a range) is a token too. A percent-escape
# (`%2B`, `%2D`) separates a suffix as `-`, `+` and `~` do, and ANY non-ASCII, non-space
# character after the numbers belongs to the token, prose punctuation aside: a lookalike dash, a
# zero-width or format character or a non-ASCII letter never ends a version early.
# Each character class has one role, so the grammar is unambiguous and runs in linear time:
# separators are ASCII and come only before an identifier; identifiers are ASCII alphanumerics
# and non-ASCII; a glued identifier (`1.0rc1`) may only open the suffix.
# Groups: 1 MAJOR, 2 MINOR, 3 further numbers (".1", ".1.2" or ""), 4 suffix ("-rc.1" or "").
# Any non-ASCII, non-space character except punctuation that closes or frames a word in prose:
# quotes, guillemets, ellipsis, bullets, arrows, box drawing, CJK and fullwidth punctuation.
_WIDE = (r"[^\x00-\x7f\s\u00ab\u00bb\u2039\u203a\u201c\u201d\u2018\u2019\u201e\u201a\u2026\u00b7"
         r"\u2022\u2190-\u21ff\u2500-\u257f\u3000-\u303f\uff01\uff08\uff09\uff0c\uff0e\uff1a\uff1b"
         r"\uff1f\uff3b\uff3d\uff5b\uff5d]")
_ID = r"(?:[0-9A-Za-z]|" + _WIDE + r")"          # an identifier character
_LEAD = r"(?:[A-Za-z]|" + _WIDE + r")"           # one that cannot continue the numbers
_END = r"(?!" + _ID + r")"                        # a word ends here
FIELD_WORDS = r"(?:linux|darwin|windows|fips|amd64|arm64|x86_64|aarch64|all)" + _END
FILE_WORDS = (r"(?:tar|gz|tgz|xz|zst|zip|deb|rpm|apk|pkg|sh|md|json|ya?ml|txt|sig|asc|pem|pub|"
              r"bundle|x86_64|aarch64|noarch|x)" + _END)
# One separator character: `-`, `+`, `~`, a percent-escape, `_` unless an archive field follows,
# `.` unless a digit (a number segment), a range (`..1.1`) or a file extension follows. Separators only ever open a
# segment, so every string has one parse and the grammar stays linear even where it fails.
_SEP = (r"(?:[-+~]|%[0-9A-Fa-f]{2}|_(?!" + FIELD_WORDS + r")|\.(?![0-9]|\.[0-9]|" + FILE_WORDS + r"))")
_NUMBER = r"\.[0-9]+(?:" + _LEAD + _ID + r"*)?"   # `.2`, `.2rc`
_SEGMENT = r"(?:" + _SEP + r"+(?:" + _ID + r"+|" + _NUMBER + r")|" + _NUMBER + r")"
# A dangling run of separators at the end of a token is still the token's when it holds a `-` or
# a percent-escape (`1.0-`, `1.0-.`, `1.0%2B+`); a lone `+` (`1.0+ and later`), `~` (a sort
# marker), `_` (markdown) or `.` (a full stop) is not. The run's first `-` or escape fixes its parse.
_DANGLING = (r"[+~_.]*(?:-|%[0-9A-Fa-f]{2})(?:[-+~_.]|%[0-9A-Fa-f]{2})*" + _END)
TOKEN = re.compile(r"(?:(?<![a-zA-Z0-9.])|(?<=\.\.))[vV]?([0-9]+)\.([0-9]+)((?:\.[0-9]+)*)"
                   r"((?:(?:" + _LEAD + _ID + r"*|" + _SEP + r"+(?:" + _ID + r"+|" + _NUMBER + r"))"
                   r"(?:" + _SEGMENT + r")*)?(?:" + _DANGLING + r")?)" + _END)
# A shields.io static badge is `label-message-colour`, a literal dash written `--`, so a version
# in the message field ends at the next SINGLE dash: the colour after it is not the version's,
# and an escaped `--` keeps the rest of the message in the token.
BADGE_FIELD = re.compile(r"img\.shields\.io/badge/[^\s/()]*-$")


def tokens(line):
    """-> every complete version token in `line`, as TOKEN matches. THE matcher of this file."""
    for m in TOKEN.finditer(line):
        if (m.group(4)[:1] == "-" and m.group(4)[:2] != "--"
                and BADGE_FIELD.search(line, max(0, m.start() - 512), m.start())):
            m = TOKEN.match(line, m.start(), m.end(3))
        yield m


# The ONLY suffixes a product coordinate carries after its bare MAJOR.MINOR, each named once
# with the context that carries it: the text right before the token must match the first
# pattern, the text right after it the second (None: any). Anything else (a prerelease, build
# metadata, a third or fourth number, a v prefix, a known suffix out of its context) is no
# product form and is refused wherever a judge meets it. Development stamps (`1.0-dev`,
# `1.0-SNAPSHOT-<sha>`) are build identities, never published, so they are not here either.
IMAGE_REPOSITORY = r"olivares(?:ai)?/olivares:$"
WITNESS_DIR = r"docs/releases/$"
FORMS = {
    "": (None, None, "the release"),
    "-fips": (None, None, "hardened image tag; only `variants` records admit it (allowed_at)"),
    "-stig": (None, None, "hardened image tag; only `variants` records admit it (allowed_at)"),
    "-amd64": (IMAGE_REPOSITORY, None, "per-architecture tag of the multi-arch image (.goreleaser.yaml)"),
    "-arm64": (IMAGE_REPOSITORY, None, "per-architecture tag of the multi-arch image (.goreleaser.yaml)"),
    "-fips-amd64": (IMAGE_REPOSITORY, None, "per-architecture tag of the hardened image (.goreleaser.yaml)"),
    "-stig-amd64": (IMAGE_REPOSITORY, None, "per-architecture tag of the hardened image (.goreleaser.yaml)"),
    "-1": (r"olivares-$", r"\.(?:x86_64|aarch64|noarch)(?:\.rpm)?(?![0-9A-Za-z])",
           "RPM release 1 of an nFPM package: name-version-release.arch[.rpm]"),
    "-install-surfaces": (WITNESS_DIR, r"\.json(?![0-9A-Za-z])",
                          "the release's dated install-surface witness, docs/releases/<release>-install-surfaces.json"),
}
HARDENED = ("-fips", "-stig")


def in_form(m):
    """-> True iff a token match carries a FORMS suffix in the context its row names."""
    row = FORMS.get(m.group(4))
    if row is None:
        return False
    before, after, _ = row
    return ((before is None or re.search(before, m.string[:m.start()]) is not None)
            and (after is None or re.match(after, m.string[m.end():]) is not None))


def in_context(tok, line):
    """-> True iff every occurrence of `tok` in `line` carries its suffix in its FORMS context
    (always True without a line: the caller judges the token alone)."""
    return line is None or all(in_form(o) for o in tokens(line) if o.group() == tok)


def stated(tok, line=None):
    """-> the release a complete token states, its hardened variant kept and any other FORMS
    suffix dropped ('1.0-fips-amd64' -> '1.0-fips', '1.0-1' -> '1.0'), or None when the token is
    no product form. Given its `line`, every occurrence there must sit in its suffix's context.
    Judges compare this against the canon's forms, never a token's core."""
    m = TOKEN.fullmatch(tok)
    if not m or tok[:1] in "vV" or m.group(3) or m.group(4) not in FORMS:
        return None
    if not in_context(tok, line):
        return None
    suffix = m.group(4)
    return tok[:m.start(4)] + next((h for h in HARDENED if suffix.startswith(h)), "")


def version_key(tok):
    """-> the numeric components of a complete token, for order and equality ('v26.8.0' and
    '26.8.0' are equal). Its v and suffix are not part of the number."""
    m = TOKEN.fullmatch(tok)
    numbers = (m.group(1), m.group(2)) + tuple(m.group(3).split(".")[1:])
    # (length, digits) orders decimals of any size without int(), which refuses 4300+ digits.
    return tuple((len(n.lstrip("0") or "0"), n.lstrip("0") or "0") for n in numbers)


# The published tags before MAJOR.MINOR: a LIST, read from the public tag list and shared with
# scripts/release-finalize-stable.sh, never a rule such as "any token that is not MAJOR.MINOR".
RETIRED_TAGS_FILE = "scripts/lib/retired-release-tags.txt"


# The list's own format, shared with release-finalize-stable.sh: printable ASCII lines, and a
# tag line is a published three-number tag with at most nine digits per number.
RETIRED_TAG_LINE = re.compile(r"v?[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}")
LIST_BYTES = frozenset(b"\n" + bytes(range(0x20, 0x7f)))


def read_retired_tags(read=None, path=RETIRED_TAGS_FILE):
    """-> the retired tags exactly as tagged. Read on first use (the Compose stamp needs none);
    an unreadable, empty or malformed list is COULD NOT LOOK, never an empty history. Any byte but
    printable ASCII and a newline (a CR, a tab, a NUL, a non-ASCII byte) refuses the whole list,
    as the finalizer's reader does, so the two readers give one verdict on the same bytes."""
    def read_file(path):
        with open(path, "rb") as source:
            return source.read()
    try:
        data = (read or read_file)(path)
    except (OSError, UnicodeDecodeError) as exc:
        unverified(f"UNVERIFIED check-release-version: {path} cannot be read ({exc}); "
                   "retired history cannot be told from a stale coordinate without it.")
    if isinstance(data, str):
        data = data.encode("utf-8", "surrogateescape")
    if not set(data) <= LIST_BYTES:
        unverified(f"UNVERIFIED check-release-version: {path} holds bytes other than printable ASCII "
                   "and newlines; the list is refused whole.")
    tags = [ln for ln in data.decode("ascii").split("\n") if ln and not ln.startswith("#")]
    bad = [t for t in tags if not RETIRED_TAG_LINE.fullmatch(t)]
    if not tags or bad:
        unverified(f"UNVERIFIED check-release-version: {path} must list published "
                   f"MAJOR.MINOR.PATCH tags, one per line (found {len(tags)}, malformed {bad!r}).")
    return frozenset(tags)


_retired = []


def retired_tags():
    """-> the retired tags exactly as tagged, read once."""
    if not _retired:
        _retired.append(read_retired_tags())
    return _retired[0]


def retired_spellings():
    """-> the spellings that name a retired release: each tag as tagged, and a v tag without its
    v, because the changelog's own sections spell v26.9.0 as [26.9.0]. Never a v that was never
    tagged: 26.10.0 was tagged bare, so v26.10.0 names nothing."""
    return retired_tags() | frozenset(t[1:] for t in retired_tags() if t.startswith("v"))


CHART_LABEL = re.compile(r"\s*helm\.sh/chart:\s*[\"']?olivares-")


def chart_metadata(match: re.Match[str]) -> bool:
    """Helm's chart-version label is independent of the engine's appVersion."""
    label = CHART_LABEL.match(match.string)
    return label is not None and label.end() == match.start()



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


# ── MARKED RELEASE POSITIONS (2026-10-08) ────────────────────────────────────────────────────────────
# A release position is MARKED in the source; nothing guesses it from the words around it. The
# context table this replaces judged a 1.x token only after one of 21 context rows, so a release
# in any other wording ("Olivares version 1.0.1 is the current release", `export
# OLIVARES_VERSION=1.0.1`, the same sentence in German or Japanese) was neither refused nor
# restamped: measured 2026-10-09, 9 of 14 planted patch releases passed. Guessing positions from
# prose cannot converge, so the data says where a release is:
#   1. Every place that names the current or next release sits inside a mark, the same in every
#      locale: <!-- release -->…<!-- /release --> in Markdown; {/* release */}…{/* /release */} in
#      MDX, which refuses HTML comments; and in every other file a comment line `# release` or
#      `// release` before the lines and `# /release` or `// /release` after them. A Markdown mark
#      may wrap a word, a line or a fenced code block from outside it, so the reader sees none of it.
#   2. --stamp rewrites the versions inside marks from RELEASE-VERSION, and nothing else.
#   3. The census needs no context words. Every version literal of the product's version lines
#      (product_line) outside a mark is refused unless it is a record the rules below admit (an
#      annotated fixed version, a recorded floor or contract, a dated record of a past release, a
#      changelog citation), a NOT_RELEASES row names what it versions, or it numbers a section
#      under its numbered parent (section_number).
# A mark that does not close, a close with no open, a mark inside a mark and a mark holding no
# version are malformed: the run stops with rc 1 and names the line.
MARK_FORMS = (
    ((".md",), r"<!--[ \t]*(/?)release[ \t]*-->"),
    ((".mdx",), r"\{/\*[ \t]*(/?)release[ \t]*\*/\}"),
)
LINE_MARK = r"(?m)^[ \t]*(?:#|//)[ \t]*(/?)release[ \t\r]*$"
# Where a Markdown block starts: after indentation, blockquote markers and a list marker. A mark
# there with text after it opens an HTML block, and the rest of the line is shown raw.
BLOCK_START = re.compile(r"[ \t]*(?:>[ \t]*)*(?:(?:[-*+]|[0-9]{1,9}[.)])[ \t]+)?")
# Files whose format has no comment a mark could use, each marked whole: exact path, written reason.
WHOLE_FILE_MARKS = {
    "packaging/aur/olivares-bin/.SRCINFO":
        "makepkg --printsrcinfo output, held byte for byte to the marked PKGBUILD by "
        "scripts/check-aur-olivares-bin.sh",
}


def malformed(path, number, why):
    sys.exit(f"FAIL check-release-version: {path}:{number}: {why}; a release mark is "
             "<!-- release -->…<!-- /release --> (Markdown), {/* release */}…{/* /release */} (MDX) "
             "or a `# release` … `# /release` comment line pair")


def lines_at(text):
    """-> [(offset, line)]: each line of `text` without its terminator (any str.splitlines()
    knows), with the offset where it starts."""
    out, offset = [], 0
    for raw in text.splitlines(keepends=True):
        out.append((offset, (raw.splitlines() or [""])[0]))
        offset += len(raw)
    return out


def line_number(text, offset):
    return sum(1 for start, _ in lines_at(text) if start <= offset) or 1


def marks(path, text):
    """-> the [start, end) spans of the release marks in `text`, refusing a malformed one."""
    if path in WHOLE_FILE_MARKS:
        return [(0, len(text))]
    pattern = next((p for suffixes, p in MARK_FORMS if path.endswith(suffixes)), LINE_MARK)
    spans, start = [], None
    for m in re.finditer(pattern, text):
        if path.endswith(".md"):
            begin = text.rfind("\n", 0, m.start()) + 1
            end = text.find("\n", m.end())
            if BLOCK_START.fullmatch(text, begin, m.start()) and text[m.end():len(text) if end < 0 else end].strip():
                malformed(path, line_number(text, m.start()), "a Markdown mark that starts a line turns the rest "
                          "of the line into raw HTML; end the line before with it, or give it a line of its own")
        if not m.group(1):
            if start is not None:
                malformed(path, line_number(text, m.start()), "a release mark opens inside another")
            start = m.end()
        elif start is None:
            malformed(path, line_number(text, m.start()), "a release mark closes without opening")
        else:
            spans.append((start, m.start()))
            start = None
    if start is not None:
        malformed(path, line_number(text, start), "a release mark never closes")
    return spans


# THE PRODUCT'S VERSION LINES, not every decimal. Measured 2026-10-09 on the 1,493 census files:
# 7,377 version-shaped literals, 6,411 of them outside a release context, mostly percentages,
# latencies, IPv4 addresses, OIDs, WCAG criteria and section numbers. Thirteen named rows still
# left 1,956 in 410 files, and the dependency-name row that got there also admitted "version
# 1.0.1": a wide exclusion vocabulary reopens the hole the census closes. Main judged every
# two-digit CalVer MAJOR (26 to 99) whatever the wording, so a later release in prose (`Olivares
# 27.1 adds clustering`, `>= 99.1.0`) was refused before any release above the canon existed. The
# product's lines keep that reach: MAJOR 1 to 99, above the canon too (`Olivares 2.0 adds
# clustering` is a claim nobody cut), and the retired 26.x line as main judged it (any patch, or
# 26.11 and later). A MAJOR of three or more digits was no release on main and is none here.
def product_line(tok):
    """-> True iff an unmarked literal could state a release of ours (see above)."""
    m = TOKEN.fullmatch(tok)
    if m is None:
        return True   # an image of ours states a release, whatever its tag
    major, minor = version_key(tok)[:2]
    if major == version_key("26.0")[0]:
        return not m.group(2).startswith("0") and (
            bool(m.group(3) or re.search(r"[0-9]", m.group(4))) or minor >= version_key("0.11")[1])
    return version_key("1.0")[0] <= major <= version_key("99.0")[0]


# The words localized documents write around a version literal are data, read by name, so this
# source stays in US English: a row inserts its vocabulary with word(). The file is read once and
# refused whole (COULD NOT LOOK, exit 2) when it cannot be read, a line is not name<TAB>value, a name
# repeats, or a value has an edge blank, does not compile on its own (`a)|(b` would escape its row's
# group) or names no word: it matches nothing at some position of NEUTRAL_TEXT, or one of its
# characters alone (`\b`, `.`, `\w+`, an empty alternative), so its row would admit a literal by its
# shape. A name this script reads and the file lacks is refused too, and so is a row the words make
# match a neutral window (NEUTRAL_WINDOWS).
WORDS_FILE = "scripts/lib/release-version-words.tsv"
NEUTRAL_TEXT = "x 1.0\t"
_WORDS_READ = set()


def read_words(read=None):
    """-> {name: value} of WORDS_FILE."""
    def read_file(path):
        with open(path, "rb") as source:
            return source.read()
    try:
        data = (read or read_file)(WORDS_FILE)
        text = data.decode("utf-8") if isinstance(data, bytes) else data
    except (OSError, UnicodeDecodeError) as exc:
        unverified(f"UNVERIFIED check-release-version: {WORDS_FILE} cannot be read ({exc}).")
    words = {}
    for number, line in enumerate(text.split("\n"), 1):
        if not line.strip() or line.startswith("#"):
            continue
        name, _, value = line.partition("\t")
        try:
            re.compile(value)
            probe = re.compile("(?:" + value + ")")
            names_no_word = (any(m.start() == m.end() for m in probe.finditer(NEUTRAL_TEXT))
                             or any(probe.fullmatch(c) for c in NEUTRAL_TEXT))
        except (re.error, OverflowError, RecursionError):
            names_no_word = True
        if (not re.fullmatch(r"[a-z0-9][a-z0-9.-]*", name) or name in words or "\t" in value
                or value != value.strip() or names_no_word):
            unverified(f"UNVERIFIED check-release-version: {WORDS_FILE}:{number} is not a new name<TAB>value whose "
                       "value compiles on its own, has no edge blank and names words; the words are refused whole.")
        words[name] = value
    return words


WORDS = read_words()


def word(name):
    """-> the WORDS_FILE value named `name`; a name the file lacks is COULD NOT LOOK."""
    if name not in WORDS:
        unverified(f"UNVERIFIED check-release-version: {WORDS_FILE} names no {name!r}.")
    _WORDS_READ.add(name)
    return WORDS[name]


# What a version literal of the product's lines versions when it is not a release of ours: each
# row names it once, in one place. A row is (name, the files it applies to as fnmatch patterns or
# None for every file, a pattern searched in the literal's window): up to 160 characters of its line
# before it, NUL, the literal, SOH, up to 80 characters after it. A cut window starts with STX, so
# `^` is the start of the line or nothing. A row applies inside a mark too: a marked line may name
# an address or a dependency beside the release, and that literal is no release.
# Rows name a dependency, a standard, an address, a quantity or a structure, never a word a release
# claim of ours would use ("version", "release", the product name).
# The names a dependency, protocol, standard or model goes by where its version follows. Each must
# be the only word that names some version a published document writes where no other row does
# (test-release-version-generation.py): a word nothing needs only opens a hole (`Olivares Compose
# 1.0.1 bundle.` and `Olivares will go 2.0` passed, 2026-10-09). Compose names only its floor
# (`Compose >= 2.24.4`), the one way a published document writes it.
DEPENDENCY_WORDS = (
    "TLS", "OAuth", "OCSF", "CycloneDX", "SLSA", "AuthZEN", "OpenGitOps", "jq", "Graph", "SCIM",
    "SAML", "OpenAPI", "AsyncAPI", "WCAG", "SPDX", "SARIF", r"Compose(?=[ \t]*(?:>=|≥))", "Python", "PostgreSQL",
    "SQLite", "modernc", "Ubuntu", "Alpine", "NIST", "CNSA", "AICM", "AI Controls Matrix", "AI-CAIQ", "RMF",
    "CVSS", "VCDM", "LEEF", "ECS", "CFTC", "grpc", "axe-core", "Grok Build", "OpenCode", "Google ADK",
    "pgx", "cosign", "nFPM", "cloudflare/circl", "PCI DSS", "Google-ADK", "Keep a Changelog",
    "DoD Zero Trust Strategy",
)
DEPENDENCY = "(?:" + "|".join(DEPENDENCY_WORDS) + ")"


OCTET = r"(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])"


# A path-less row never names a literal or a dependency word that follows the product's name, in any
# case, after one or two spaces, as the brand or in bold ("OLIVARES TLS 1.1", "Olivares  2.0x",
# "Olivares AI 2.0 GB", "**Olivares** 1.0 以降").
NOT_AFTER_PRODUCT = (r"(?<!(?i:olivares)[ \t\u00a0])(?<!(?i:olivares)[ \t\u00a0]{2})(?<!(?i:olivares ai)[ \t\u00a0])"
                     r"(?<!(?i:olivares)\*\*[ \t\u00a0])")
# The words the OpenTelemetry GenAI pages write within 24 characters of a convention version, in every
# locale, and the phrases that follow one: its row names a v-prefixed or three-number literal only there
# (measured 2026-10-10).
OTEL_SEMCONV = r"(?:v1\.(?:28|36|37|39|41|42)(?:\.[0-9]+)?|1\.(?:36|41)\.[0-9]+)(?:-[^\s`*|)]*)?"
OTEL_WORDS = word("otel-convention-before")
OTEL_AFTER = word("otel-convention-after")
DOD_CAPABILITY = r"(?:1\.[1-9]|2\.[1-7]|3\.[1-5]|4\.[1-7]|5\.[1-4]|6\.[1-7]|7\.[1-6])"
PG_MINOR = r"(?:1[5-8])\.[0-9]{1,2}"


def localized(page):
    """-> the fnmatch patterns of a docs-site page in English and in every locale."""
    return ("docs-site/src/content/docs/" + page, "docs-site/src/content/docs/??/" + page)


NOT_RELEASES = (
    ("a license identifier (SPDX)", None,
     r"\b(?:AGPL|LGPL|GPL|Apache|MPL|EPL|CDDL|EUPL|CC-BY(?:-SA|-NC|-ND)*|OFL|Artistic|BSL|Covenant)(?:-{1,2}|[ \t])\x00"),
    ("a version of a named dependency, protocol, standard or model", None,
     r"(?<![\w-])" + NOT_AFTER_PRODUCT + DEPENDENCY + r"\)?`?(?:\*\*)?[ /:@=-]?(?:\*\*)?[ \t]?\(?(?:≥[ \t]?|>=[ \t]?|≤[ \t]?)?`?\x00"),
    ("a standard's row in the SIEM formats table", localized("reference/siem-telemetry-egress.md"),
     r"^\|[ \t]*" + DEPENDENCY + r"[ \t]*\|(?:(?![^|\n]*(?i:olivares|release|version|community|business|enterprise|edition))[^|\n]*\|)?[ \t]*"
     r"\x00(?:2\.0|9\.4\.0|2\.1\.0|1\.8\.0)\x01"),
    ("a Linux kernel release", None, r"\bLinux[ \t]+\x00[3-9]\.[0-9]+\.[0-9]+-[a-z][0-9a-z.-]*\x01"),
    ("a document's own revision", None,
     r"\b(?:Rev(?:ision)?\.?|" + word("revision") + r")(?:\*\*)?[ \t]*(?:[0-9]+\.[0-9]+/)?\x00"),
    # Any dotted quad with its port or prefix length, and without one only the reserved ranges a
    # document names (loopback, private, link-local, shared, documentation, multicast, broadcast):
    # `1.0.1.2` alone is a malformed release, not an address.
    ("an IPv4 address", None,
     r"\x00" + OCTET + r"(?:\." + OCTET + r"){3}\x01(?::[0-9]|/[0-9])"
     r"|\x00(?:0|10|127|22[4-9]|23[0-9]|255)(?:\." + OCTET + r"){3}\x01"
     r"|\x00(?:169\.254|192\.168|172\.(?:1[6-9]|2[0-9]|3[01])|100\.(?:6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7]))"
     r"(?:\." + OCTET + r"){2}\x01"
     r"|\x00(?:192\.0\.2|198\.51\.100|203\.0\.113)\." + OCTET + r"\x01"),
    ("a quantity with its unit", None,
     NOT_AFTER_PRODUCT + r"\x00[0-9]+\.[0-9]+\x01[ \t]?(?:%|‰|ms\b|µs\b|ns\b|s\b|sec\b|seconds?\b|min\b|minutes?\b|h\b|hours?\b|"
     r"d\b|days?\b|weeks?\b|months?\b|years?\b|x\b|×|[KMGTP]i?B\b|vCPU|CPUs?\b|cores?\b|M\b|k\b|USD\b|EUR\b|€|pt\b|px\b|rem\b|em\b|" + word("time-unit")
     + r")(?![\w’'])"),
    ("a quantity glued to its unit", None, NOT_AFTER_PRODUCT + r"\x00[0-9]+\.[0-9]+(?:x|×|k|M|ms|s|GB|GiB|MB|MiB|KiB|KB|%)★?\x01"),
    ("a ratio", None, r"\x00[0-9]+\.[0-9]+(?:–[0-9]+\.[0-9]+)?\x01:1(?![0-9])"),
    ("a CPU count", None, r"\bcpus[\"']?:[ \t]*[\"']?\x00|range of CPUs is from [0-9.]+ to \x00"),
    ("a fraction in a list of thresholds", None, r"thresholds[\"']?[ \t]*[:=][ \t]*\[[^\]]*\x00"),
    ("a tool's own version setting", None,
     r"\b(?:kyverno_version|kubeVersion|required_version)[\"']?[ \t]*[:=][ \t]*[\"']?(?:>=[ \t]*)?\x00"),
    ("a section or clause reference", None,
     r"(?:§|\bORD-|\b[Ss]ections?|" + word("section") + r"|\b[Cc]lause|\bArt(?:icle|\.)|"
     r"\b(?:GOVERN|MAP|MEASURE|MANAGE|MS)-|\bB_)[ \t]*[-–]?[ \t]*(?:\*\*)?\x00"),
    ("a WCAG success criterion in an accessibility record", ("docs/accessibility/*",),
     r"\x00[1-4]\.[1-9][0-9]?\.[1-9][0-9]?(?:–[1-4]\.[1-9][0-9]?\.[1-9][0-9]?)?\x01"),
    ("a URL of another project", None,
     r"https?://(?![^\s`\"'<>()\]]*(?i:olivares))[^\s`\"'<>()\]]*\x00"),
    ("a Go module requirement", None,
     r"(?:^|[\s`(])(?![^\s@`]*(?i:olivares))(?:[a-z0-9-]+\.)+[a-z]{2,}/[^\s@`]*[ \t]+\x00"
     r"|(?:^|[\s`(])(?![^\s@`]*(?i:olivares))[^\s@`]+@\x00"),
    ("a Go toolchain version", None,
     NOT_AFTER_PRODUCT + r"(?:\bGo|(?:^|[`,·])[ \t]*go)(?:[ \t]*\|)?(?:` directives at)?[ \t]+\x00v?1\.2[0-9](?:\.[0-9]+)?\+?\x01"),
    ("another image's tag", None,
     r"(?<![A-Za-z0-9_./:$-])(?![A-Za-z0-9_./-]*(?i:olivares))(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+:\x00"
     r"|\bimage\"?[ \t]*[:=][ \t]*\"?(?![A-Za-z0-9_./-]*(?i:olivares))[A-Za-z0-9_.-]+:\x00"
     r"|\bFROM[ \t]+(?![A-Za-z0-9_./-]*(?i:olivares))[A-Za-z0-9_.-]+:\x00"),
    ("the semantic-version example of a field description", None, r"semantic version, e\.g\.[ \t]+\x00"),
    # 1.0 is where the release line starts over (2026-10-07): "pre-1.0", "before 1.0" and
    # "from 1.0" name that fixed boundary in every locale, and never the release being prepared.
    # After it only a compound a published page writes may follow (`pre-1.0-Projekt`,
    # `pre-1.0／設計段階にある`), each named here: any other suffix, in any dash, is refused
    # (`from 1.0-rc.1`, `Ab 1.0—beta`, `from 1.0-lts`). A page with a new compound adds it here.
    ("the 1.0 boundary of the release line", None,
     r"(?i:" + word("boundary-before") + r")`?\x001\.0(?:" + word("boundary-compound") + r")?\x01"
     r"|" + NOT_AFTER_PRODUCT + r"\x001\.0\x01`?[ \t]*(?:" + word("boundary-after") + r")"),
    ("the 1.0/GA milestone of the API stability page", localized("reference/api-stability.md"),
     r"(?:" + word("ga-milestone") + r") \x001\.0\x01/GA"),
    ("a time of day", None, r"(?<![0-9])[0-9]{1,2}:[0-9]{2}:\x00"),
    ("a percentile", None, r"quantile\(\x00"),
    ("a CVSS score band of a severity", None,
     r"^\|[ \t]*(?:Critical|High|Medium|Low)[ \t]*\((?:<[ \t]*)?\x00[0-9]{1,2}\.[0-9](?:–[0-9]{1,2}\.[0-9])?\x01\)[ \t]*\|"),
    ("a dashboard's Grafana requirement", None, r"\"name\":[ \t]*\"Grafana\",[ \t]*\"version\":[ \t]*\"\x00"),
    ("a seccomp rule's minimum kernel", None, r"\"minKernel\":[ \t]*\"\x00"),
    ("the Helm release that made OCI the default", None, r"\bOCI-default since \x00v3\.8\.0\x01"),
    ("the Docker AppArmor policy's provenance tag", None,
     r"(?:\bmoby/moby[ \t]+|https://github\.com/moby/moby/blob/)\x00"),
    ("the FedRAMP module-selection policy's version", None, r"Module[ _]Selection[ _]\x00"),
    ("an API document's own version", None, r"info\.version`?[ \t]*[(（]?`?\x00"),
    ("the version an XML or HTML document declares", None,
     r"<\?xml version=\"\x00|<plist version=\"\x00|DTD PLIST \x00|initial-scale=\x00"),
    ("a recall score", None, r"\brecall(?:@k)?[ \t]*=?[ \t]*\x00"),
    ("a number with a thousands separator", localized("reference/modules/i-inventory.md"),
     r"\x00[0-9]{1,3}\.[0-9]{3}\x01[ \t]+(?:" + word("thousands-word") + r")"),
    # Documents about one dependency, each holding only the versions they name. A row for documents
    # the export removes lives in PRIVATE_ROWS, so this script names none of them.
    ("the Go Cryptographic Module's versions (CMVP #5247 is v1.0.0)",
     ("docs/SCP-09-FIPS-STIG.md", "docs/SEC-G3-CRYPTO-AGILITY-PQC.md", "docs/trust/impact-levels.md",
      "docs/trust/evaluation-guide.md"),
     r"\x00v1\.(?:0\.0|26\.0)\x01"),
    ("OpenTelemetry GenAI semantic-convention versions",
     (*localized("how-to/connectors/otel-genai.md"), *localized("reference/connectors.md"),
      "examples/otel-genai-ingest/README.md"),
     r"(?:" + OTEL_WORDS + r")[^\n]{0,24}\x00" + OTEL_SEMCONV + r"\x01|\x00" + OTEL_SEMCONV + r"\x01[^\n]{0,24}(?:" + OTEL_WORDS
     + r")|\x00" + OTEL_SEMCONV + r"\x01[^\n]{0,4}(?:" + OTEL_AFTER + r")|^\|[ \t]*(?:\*\*)?\x00" + OTEL_SEMCONV
     + r"\x01|^[ \t]*\(\x00" + OTEL_SEMCONV + r"\x01\)\*\*|\x00v1\.41-Client\x01/Internal"),
    ("OCSF schema versions", localized("reference/siem-telemetry-egress.md"),
     r"(?:" + word("ocsf-1.8-before") + r")[ \t]*\x001\.8\.0"
     r"(?:-[^\s`*|)]*)?\x01|\bso \x001\.8\.0\x01$|^[ \t]*\x001\.8\.0\x01、SARIF"
     r"|(?:" + word("ocsf-1.3-before") + r")[ \t]*\x001\.3\x01|\x001\.3(?:" + word("ocsf-1.3-compound") + r")\x01"
     r"|\x001\.3\x01[ \t]*(?:" + word("ocsf-1.3-after") + r")|^[ \t]*\x001\.3-shaped\x01"),
    ("Agent2Agent (A2A) protocol versions", localized("reference/connectors.md"),
     r"Agent2Agent[ -]?[(（]A2A-?[)）][ \t]?\x00v1\.0(?:-Peers)?\x01"),
    ("Tetragon's minimum version", localized("reference/connectors.md"),
     r"Tetragon[^\n|:：]{0,12}[:：][ \t]*\x00v1\.0\x01"),
    ("CSA AI Controls Matrix and AI-CAIQ versions", ("docs/trust/csa-star-ai-readiness.md",),
     r"domain\)\. \x00v1\.0\x01 released 2025-07-09|the \*\*\x00v1\.1\x01 bundle \(2026-06\)|the AICM, shipped with the \x00v1\.1\x01"
     r"|bundle \(supersedes \x00v1\.0\.2\x01 of 2025-10-16|workbook \(\x00v1\.1\x01 with the AICM"),
    ("the DoD Zero Trust Strategy's version and capability numbers", ("docs/trust/dod-zero-trust-mapping.md",),
     r"^\|[ \t]*\x00" + DOD_CAPABILITY + r"\x01[ \t]*\||\x001\.6\x01/1\.9|1\.6/\x001\.9\x01|\(\x00v1\.0\x01[ \t]+roadmap"
     r"|\(\x007\.[24]\x01 row\)|listed in \x003\.4\x01|doing \x006\.4\x01|\(see \x006\.3\x01\)|;[ \t]*\x005\.2-5\.3\x01;"),
    ("an evaluation step number", ("docs/trust/evaluation-guide.md", "docs/trust/evaluation-report-template.md"),
     r"^\|[ \t]*\x00(?:1\.[1-6]|1\.2b|2\.(?:[1-9]|10)|3\.[1-9])\x01[ \t]*\||\bIf[ \t]+\x001\.3\x01"),
    ("a budget fraction, where 1.0 is the limit itself", ("docs/finops-alert-evidence.md",),
     r"\(threshold \x001\.0\x01\)|authoritative \x001\.0\x01 decision"),
    ("the CRA FAQ document's version", ("docs/CRA-READINESS.md",), r"non-binding; \x00v1\.3\x01 of"),
    ("a measured slowdown factor or query rate", ("docs/SIZING-AND-CAPACITY.md",), r"\*\*\x00(?:1\.06|10\.7)\x01\*\*"),
    ("the Kyverno CLI default of the policy-gate template",
     ("deploy/ci/gitlab/templates/olivares-policy-gate/template.yml",), r"default:[ \t]*\"\x00"),
    ("the versioning section's examples of the grammar", ("INSTALL.md",),
     r"MAJOR\.MINOR\*\* only: (?:`[0-9.]+`, )*`\x00|increments MAJOR \(for example, `\x00"),
    ("TLS and Go versions and the CNSA 2.0 FAQ's revision in the crypto-agility note", ("docs/SEC-G3-CRYPTO-AGILITY-PQC.md",),
     r"\bon \x001\.3\x01 \*\*hybrid|codepoint[^\n|]*\|[ \t]*\x001\.3\x01[ \t]*\||FAQ[ \t(]*\x00v2\.1\x01"
     r"|FIPS-mode since \x001\.25\x01|accepted for \x001\.27\x01|Go stance since \x001\.25\x01|milestone \x001\.27\x01"
     r"|default since \x001\.24\x01|MLKEM in \x001\.26\x01"),
    ("the SLO alert's fast-burn rate", ("deploy/monitoring/olivares-slo.rules.yaml", "docs/17-PRODUCTION-READINESS-SLO.md"),
     r"\(\x0014\.4\x01 \* 0\.001\)|\|[ \t]*\*\*\x0014\.4\x01\*\*[ \t]*\|"),
    ("npm dependency versions in a changelog entry", ("CHANGELOG.md",),
     r"DOMPurify is updated to \x00|`colord` \x00|undici \(\x00|miniflare, \x00"),
    ("the pinned gitleaks release", ("docs/SECURITY-HARDENING.md",), r"gitleaks \(pinned \x00"),
    ("the PostgreSQL minor releases a measurement names",
     (*localized("how-to/backup-and-restore.md"), "docs/DR-RUNBOOK.md", "docs/DIRECTORY-USER-AUTHORITY.md",
      "docs/DEPLOYMENT-CONTRACT-RESERVED-NAMES.md"),
     r"\x00" + PG_MINOR + r"\x01(?:,|、)[ \t]*(?:\*\*)?" + PG_MINOR + "|" + PG_MINOR + r"(?:,|、)[ \t]*(?:\*\*)?\x00" + PG_MINOR
     + r"\x01|" + PG_MINOR + r"[ \t]*(?:,|、)?[ \t]*(?:" + word("conjunction") + r")[ \t]*\x00" + PG_MINOR
     + r"\x01|[Mm]easured on \x0016\.15\x01"),
    ("Claude Code client versions",
     (*localized("how-to/claude-code-enterprise-otel.md"),
      *localized("explanation/positioning/claude-apps-gateway-co-deployment.md"), *localized("how-to/first-hour.md")),
     r"(?:" + word("claude-client-before") + r")[ \t]*\x00v?2\.1\.[0-9]{2,3}x?\x01"
     r"|^[ \t]*\x00v2\.1\.195\x01(?:;| " + word("claude-client-after") + r")"),
    ("the TAK Server guide and CoT event schema versions", localized("how-to/connectors/tak.md"),
     r"Guide(?:\*|[ \t])*\x00v5\.2\x01|Event-PUBLIC\.xsd[^\n]*[(（](?i:" + word("tak-version")
     + r")[ \t]*\x002\.0\x01[)）]"),
    ("the rollout an approval example names", localized("how-to/cookbook/hitl-approvals.md"),
     r"\"reason\":[ \t]*\"rollout \x00"),
    ("the self-hosted Langfuse release with OTLP", localized("reference/configuration.md"),
     r"Langfuse[^\n]*self-hosted[ \t]+\x00"),
    ("the VPAT edition, EN 301 549 and WCAG versions of the accessibility statements",
     ("docs/accessibility/*", "docs/trust/README.md", "docs/trust/hecvat-readiness.md",
      *localized("explanation/security/trust-and-procurement.md")),
     r"^(?=[^\n]*(?i:wcag|vpat|EN 301 549|\bSC\b|success criteri|harmoni[sz]ed))[^\n]*?(?:"
     r"VPAT(?:®)?[ \t]*(?:\*\*)?\x002\.5(?:Rev)?\x01|(?:EN 301 549[ \t]*(?:\*\*)?|\*\*)\x00V[34]\.[12]\.1\x01"
     r"|(?i:wcag)[ \t]*\x002\.[0-2]\x01|\x002\.[0-2]\x01/|/\x002\.[0-2]\x01|(?i:new-in-|new-|\(\+|removed in )\x002\.2\x01"
     r"|\x002\.[12]\x01[ \t]*AA|\x002\.2\x01[ \t]*tested|the \x002\.1\x01 baseline|fold the \x002\.2\x01 section|draft \x004\.1\.0\x01)"
     r"|^[ \t]*(?:\*\*)?\x002\.5Rev\x01 INT|^[ \t]*\x00V3\.2\.1\x01[)）]"),
    ("a WCAG guideline series or an EN 301 549 clause in an accessibility record", ("docs/accessibility/*",),
     r"see \x00[1-4]\.[1-9]\x01\.x|^\|[ \t]*\x0012\.[12](?:\.[12])?\x01|/ \x0012\.1\.2\x01|EN \x0012\.1\.1\x01|meet \x0012\.2\x01\."),
)


_NOT_RELEASES = [(name, paths, re.compile(pattern, re.M)) for name, paths, pattern in NOT_RELEASES]
_SCOPED = {name for name, paths, _ in NOT_RELEASES if paths is not None}


# A numbered heading's number is a section, not a release, where the document numbers the parent
# section too (`### 1.1` under `## 1.`): `## 1.0.1 Release notes` has no section 1.0 above it.
# Sections count from 1, so a number with a 0 after its first dot (`### 1.0`) is no section.
NUMBERED_HEADING = re.compile(r"^#{1,6}[ \t]+(?:\*\*)?(([0-9]+(?:\.[1-9][0-9]*)*)(?:bis|ter|[a-z])?)(?=[ .)]|\*\*|$)",
                              re.M)


def numbered_sections(path, text):
    """-> the numbers of a Markdown document's numbered headings (`## 1.` -> "1")."""
    if not path.endswith((".md", ".mdx")):
        return frozenset()
    return frozenset(m.group(2) for m in NUMBERED_HEADING.finditer(text))


def section_number(match, sections):
    """-> True iff a literal is a numbered heading's whole number and `sections` holds its parent."""
    head = NUMBERED_HEADING.match(match.string)
    return (head is not None and head.span(1) == match.span()
            and head.group(2).rpartition(".")[0] in sections)


# Inside a mark the author has said where the release is, so only rows naming a structure no
# release could share apply there: an address in a marked command or a license in the file marked
# whole. Chart labels use chart_metadata(), shared with the pin classifier. A dependency inside a mark is moved out
# of it; the shields.io badge of our release is judged and stamped, not taken for another project.
INSIDE_MARKS = {"an IPv4 address", "a license identifier (SPDX)"}


# ── THE ROWS OF THE DOCUMENTS THE EXPORT REMOVES ──
# A row that applies only to documents the public export removes (research notes, contract notes,
# internal plans) lives in a file the export removes with them, so this script, which ships, names
# none of them; a public tree has neither the file nor the documents. Where the file exists it is read
# whole and refused whole (COULD NOT LOOK, exit 2) when it cannot be read, a line is not
# `name<TAB>paths<TAB>pattern`, a pattern does not compile, or a row names a document the export
# publishes: that row belongs in NOT_RELEASES, where a reader of the public tree sees it.
# export-closure: absent-by-design scripts/lib/not-releases-private.tsv — DATA, not a caller: read when
# present, and removed by the export with the documents its rows name.
PRIVATE_ROWS = "scripts/lib/not-releases-private.tsv"
# Windows that name nothing: a row that matches one admits a literal by its shape alone (`\x00`, `.*\x00`,
# `x|\x00` all do). No row, public or private, may match them.
NEUTRAL_WINDOWS = ("\x02note \x001.1\x01 lands", "\x002.0.1\x01", "It lands in \x001.1\x01.", "x \x002.0\x01 y")
_private = {}
_open_rows = [name for name, _, found in _NOT_RELEASES
              if found.search("") is not None or any(found.search(window) for window in NEUTRAL_WINDOWS)]
if _open_rows:
    unverified(f"UNVERIFIED check-release-version: with the words of {WORDS_FILE}, the rows {_open_rows} match an "
               "empty or a neutral window, so they would admit a literal by its shape alone.")


def removed_by_export(path, curated, kept):
    """-> True iff the export removes `path`: a DOCS_BLOCK entry names it, a directory above it or a
    glob over it, and DOCS_KEEP does not publish it again."""
    return path not in kept and any(path == c or path.startswith(c.rstrip("/") + "/")
                                    or fnmatch.fnmatchcase(path, c) for c in curated)


def private_rows(read=None):
    """-> the PRIVATE_ROWS rows as (name, paths, compiled pattern), read once per tree; [] where the
    file does not exist."""
    key = os.getcwd()
    if read is None and key in _private:
        return _private[key]

    def read_file(path):
        with open(path, "rb") as source:
            return source.read()
    rows = []
    try:
        data = (read or read_file)(PRIVATE_ROWS)
    except FileNotFoundError:
        data = None
    except OSError as exc:
        unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS} cannot be read ({exc}).")
    if data is not None:
        try:
            text = data.decode("utf-8") if isinstance(data, bytes) else data
        except UnicodeDecodeError as exc:
            unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS} is not UTF-8 ({exc}).")
        curated, kept = curated_out(), curation_kept()
        for number, line in enumerate(text.splitlines(), 1):
            if not line.strip() or line.startswith("#"):
                continue
            fields = line.split("\t")
            if len(fields) != 3 or not all(field.strip() for field in fields):
                unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS}:{number} is not "
                           "name<TAB>paths<TAB>pattern; the rows are refused whole.")
            name, paths, pattern = fields
            paths = tuple(part.strip() for part in paths.split(","))
            published = [part for part in paths if not removed_by_export(part, curated, kept)
                         or any(fnmatch.fnmatchcase(doc, part) for doc in kept)]
            if published:
                unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS}:{number} names {published}, "
                           "which the export publishes; that row belongs in NOT_RELEASES.")
            if "\\x00" not in pattern and "\\x01" not in pattern:
                unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS}:{number}: the pattern names no position "
                           "of the literal (\\x00 or \\x01), so it would admit every literal of its documents.")
            try:
                found = re.compile(pattern, re.M)
            except (re.error, OverflowError, RecursionError) as exc:
                unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS}:{number}: the pattern does not compile: {exc}.")
            if found.search("") is not None or any(found.search(window) for window in NEUTRAL_WINDOWS):
                unverified(f"UNVERIFIED check-release-version: {PRIVATE_ROWS}:{number}: the pattern matches an empty or "
                           "a neutral window, so it would admit every literal of its documents.")
            rows.append((name, paths, found))
    if read is None:
        _private[key] = rows
    return rows


# A row scoped to documents names what a literal versions there, never a literal written right after
# the product's name: "Olivares 1.4.3 ships." in the accessibility record is a claim of ours whatever
# the row says the record numbers (measured 2026-10-09: 192 planted claims that reused a row's own
# literal passed in 12 published documents).
PRODUCT_BEFORE = re.compile(r"(?i:olivares)[^\x00\n]{0,16}\x00")


def not_release(match, path, marked=False):
    """-> the name of the NOT_RELEASES (or PRIVATE_ROWS) row that says what this literal versions, or
    None. Inside a mark only the INSIDE_MARKS rows apply; after the product's name no scoped row does."""
    window = row_window(match)
    scoped = (_SCOPED | {name for name, _, _ in private_rows()}) if PRODUCT_BEFORE.search(window) else ()
    for name, found in rows_at(path, marked):
        if found.search(window) and name not in scoped:
            return name
    return None


def row_window(match):
    """-> what a row searches for a literal: up to 160 characters of its line before it (STX first
    when cut), NUL, the literal, SOH, up to 80 characters after it."""
    s, e = match.start(), match.end()
    return (("\x02" if s > 160 else "") + match.string[max(0, s - 160):s] + "\0" + match.group()
            + "\x01" + match.string[e:e + 80])


_ROWS_AT = {}


def rows_at(path, marked):
    """-> the (name, pattern) rows that apply at `path`, inside a mark or outside one, chosen once
    per file rather than once per literal."""
    key = (os.getcwd(), path, marked)
    if key not in _ROWS_AT:
        _ROWS_AT[key] = [(name, found) for name, paths, found in _NOT_RELEASES + ([] if marked else private_rows())
                         if (not marked or name in INSIDE_MARKS)
                         and (paths is None or any(fnmatch.fnmatchcase(path, p) for p in paths))]
    return _ROWS_AT[key]


Hit = collections.namedtuple("Hit", "path number token line dated marked column")


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
    ("packaging/nfpm/postinstall.sh", "contract-floor",
     "first_contract_version names the first release whose prerm honors the upgrade contract: "
     "the cut being prepared, which the canon cannot name ahead of its tag"),
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
# A pin is the complete token right after an image repository or an `olivares-`/`olivares_` file
# stem; a Helm chart package (`olivares-0.2.4.tgz`) and the chart label are versioned on their own.
# Arm B's scope is what it always was: a token continued by `_` (the archive names
# `olivares_<version>_<os>_<arch>`) is no pin here, as the word boundary of the former pattern
# decided. Arm A judges those names on every documentation and artifact surface; elsewhere in
# the published tree they are test fixtures, a synthetic build and a comment (measured
# 2026-10-08), none a shipped coordinate.
PIN_CONTEXT = re.compile(r"(?:olivares(?:ai)?/olivares:|olivares[-_])$")


def is_pin(m):
    """-> True iff a TOKEN match from tokens() is an artefact pin of its line."""
    line = m.string
    return (PIN_CONTEXT.search(line, 0, m.start()) is not None
            and not line.startswith((".tgz", "_"), m.end()) and not chart_metadata(m))


def pin_tokens(line):
    """-> the artefact pins in `line`, as TOKEN matches from tokens()."""
    return [m for m in tokens(line) if is_pin(m)]

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
    def read_file(path):
        with open(path, encoding="utf-8") as source:
            return source.read()
    read = read or read_file
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


WITNESS_STEM = "-install-surfaces.json"


def witness_version(path):
    """-> the version a dated witness records, read from its OWN FILENAME, or None.

    docs/releases/ is a ledger with one file per release, and the file says which one it is. The
    allowance is therefore not "any past version here" but "the version on the label": a v26.8.0
    witness stating 26.5.0 is a witness about the wrong release, and no other rule would see it.

    The label is the release exactly as it was tagged: a retired tag from the retired list
    (v26.8.0 and v26.9.0 carry the v, 26.10.0 and 26.10.1 do not) or a bare MAJOR.MINOR release.
    A label in any other spelling, or any name but `<label>-install-surfaces.json`, names no
    release that was ever tagged, so it earns nothing: `1.0-rc.1-install-surfaces.json` is not
    the 1.0 witness."""
    base = os.path.basename(path)
    if not base.endswith(WITNESS_STEM):
        return None
    label = base[:-len(WITNESS_STEM)]
    return label if label in retired_tags() or release(label) else None


def historical_allowed(canon, path, tok, dated, curated, kept=frozenset(), line=None):
    """-> True iff `tok` is a past release named in the record of that release.

    Two conditions always, and neither is a line waiver: the PATH must be an exact member of the
    class above, and the TOKEN must name an explicit pre-transition record or a current-grammar
    version strictly below the canon. Unknown retired versions earn no historical allowance.

    Two kinds are narrower than that, and each may name the canon itself, because the record of
    the release being prepared is written before its tag and the stamp never moves a record.
    `changelog-history` reaches only the record half of the file (see dated_from), where the
    canon's dated section is the one scripts/check-install-docs.sh requires. `release-witness`
    admits only the version the filename names, so the ledger cannot drift inside itself."""
    kind = historical_kind(path, curated, kept)
    if kind is None:
        return False
    if kind == "changelog-history":
        return dated and reference_before(canon, path, tok, line=line)
    if kind == "release-witness":
        named = witness_version(path)
        return (named is not None and version_key(tok) == version_key(named)
                and reference_before(canon, path, tok, line=line))
    return reference_before(canon, path, tok, inclusive=False, line=line)


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
def section_reference(m):
    """-> True iff the token match IS a section reference: the whole of `[<version>]`, no suffix.
    A hardened image tag such as `[26.9.0-fips]` has no section and is not one."""
    return (not m.group(4) and m.string[m.start() - 1:m.start()] == "["
            and m.string[m.end():m.end() + 1] == "]")

# The changelog is named by its filename, which is the same string every one of those pages
# uses to point at it. Bare "changelog" is deliberately not enough: the word is prose.
CHANGELOG_FILE = "CHANGELOG.md"


def wrapped_citation(line, tok):
    """-> True iff `tok` sits in a section reference on a line that names no changelog.

    The one shape a reader cannot diagnose from a divergence line: the citation is right and
    the LINE BREAK is what refuses it, because the file name ended the line above. Measured on
    the v26.9.1 cut: eight lines in eight files, every one of them wrapped in that same place.
    They were rewrapped, not exempted, and this note exists so the ninth is not a puzzle."""
    if CHANGELOG_FILE in line or SECTION_HEADING.match(line):
        return False  # a heading IS the section, not a citation of it
    spots = [m for m in tokens(line) if m.group() == tok]
    return bool(spots) and all(section_reference(m) for m in spots)


def cited_section(line, tok):
    """-> True iff EVERY occurrence of `tok` in `line` sits inside a changelog section
    reference, on a line that names the changelog file."""
    if CHANGELOG_FILE not in line:
        return False
    spots = [m for m in tokens(line) if m.group() == tok]
    return bool(spots) and all(section_reference(m) for m in spots)


def scan_pins(tops):
    """-> [Hit] for every artefact pin under a published tree, and every literal inside a release
    mark there (file_hits), so a mark means the same in a Go or shell file as in the docs."""
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
                # Frozen documentation and Helm's independently versioned chart
                # packages are records, not current engine release coordinates.
                if path.startswith(("docs/", "docs-site/")) and any(
                    part in DATED_PRUNE for part in path.split("/")[:-1]
                ):
                    continue
                # This gate's own selftest fixtures are literal stale pins on purpose — they are
                # what proves arm B discriminates. Scanning them makes the gate fail on itself
                # forever. The fixtures could be assembled from fragments to dodge the scan, but
                # a fixture written to be invisible to the thing it tests is the weaker test.
                if path == os.path.join("scripts", "check-release-version.sh"):
                    continue
                text = read_surface(path)
                hits += file_hits(path, text, dated_from(text), is_pin)
    return hits

def judge_pins(canon, hits, curated=None, kept=None):
    """A pin sits in a release mark and names the canon (or, where DERIVED_ALLOW says so, a
    derived form), or it is the record of a past release (historical_allowed). An unmarked pin of
    the canon is refused: the stamp would leave it behind at the next cut."""
    require_decided(canon)
    if curated is None:
        curated = curated_out()
    if kept is None:
        kept = curation_kept()
    bad = []
    for hit in hits:
        # A `_test.go` pin is fixture data (see header): the multi-version regression keys an
        # object at the version AFTER the canon precisely to prove a stale token cannot fetch
        # it. Judging it would force the fixture to lie about the case it tests.
        if go_test_fixture(hit.path):
            continue
        if hit.marked:
            if marked_allowed(canon, hit.path, hit.token, hit.line):
                continue
        elif historical_allowed(canon, hit.path, hit.token, hit.dated, curated, kept, hit.line):
            continue
        bad.append((hit.path, hit.number, hit.token))
    return bad


def release(tok):
    """-> True iff `tok` is exactly a bare MAJOR.MINOR release: the only shape of a canon."""
    return stated(str(tok)) == str(tok) and not str(tok).endswith(HARDENED)

# Where a changelog stops stating the CURRENT release and starts being the RECORD of past
# ones: the first section heading, `## [Unreleased]` or the first dated version heading.
SECTION_HEADING = re.compile(r"^## \[([^\]]*)\]")


def section_start(line):
    """-> True iff `line` is `## [Unreleased]` or a heading naming one version, no suffix."""
    heading = SECTION_HEADING.match(line)
    version = heading and TOKEN.fullmatch(heading.group(1))
    return bool(heading) and (heading.group(1) == "Unreleased" or bool(version and not version.group(4)))


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
        if section_start(line):
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
    ("packaging/nfpm/postinstall.sh", "contract-floor",
     "first_contract_version: the first release whose prerm honors the upgrade contract"),
] + [(p, "min-version",
      "the role-label precondition is a compatibility FLOOR, not a shipped coordinate")
     for p in crd_types_files()]


# The package upgrade-contract coordinate: the ONE version a live script may name above the
# canon, because it names the cut being prepared, not a claim about what ships today. The canon
# cannot name it by its own rule (RELEASE-VERSION never names a cut ahead of its tag), and at
# the cut it ships with the value IS the canon. The allowance binds the token to the exact
# assignment the package logic reads — `first_contract_version='<v>~'`, the tilde sorting the
# value before that release's own pre-releases — on the exact path the records name: any other
# version in that file, or that assignment on any other file, is judged exactly as before.
CONTRACT = re.compile(r"""^first_contract_version=(['"])(.*)~\1$""")


def contract_assignment(line, tok):
    """-> True iff the line IS the upgrade-contract assignment and the token, its value, is a
    bare MAJOR.MINOR release: the cut being prepared, never a prerelease or a patch."""
    found = CONTRACT.match(line.strip())
    return bool(found) and found.group(2) == tok and release(tok)


def bounded(line, tok):
    """-> True iff every occurrence of `tok` in `line` is governed by a lower bound.

    Every, not any: a line carrying the same token twice with only one of them bounded is
    ambiguous, and an ambiguous floor is judged as what it might be — a stale claim.
    """
    spots = [m.start() for m in tokens(line) if m.group() == tok]
    return bool(spots) and all(BOUND.search(line[:s]) for s in spots)


# Retired history is the published tag LIST (retired_spellings above), never a range. Beside it,
# unpublished candidates, early preview drafts and the words a record wrote next to a version
# occur in pre-transition records: each complete token earns an allowance only in its file.
PRE_TRANSITION_RECORDS = {
    "docs/RELEASE-GO-LIVE-RUNBOOK.md": {"26.8.1", "26.8.1-SNAPSHOT-3df6798c1"},
    "docs/RELEASE-NEXT-ACTIONS.md": {"26.8.1"},
    "docs/integrations/paperclip-guide.md": {"26.10.2"},
    # export-closure: absent-by-design docs/launch/README.md — a row of the historical-version table, matched by name and never read or run
    # export-closure: absent-by-design docs/launch/REVIEW-NOTES.md — a row of the historical-version table, matched by name and never read or run
    "docs/launch/README.md": {"26.8.0-published"},
    "docs/launch/REVIEW-NOTES.md": {"26.8.0-published"},
    "docs/releases/v26.9.0-install-surfaces.json": {"26.9.0-amd64"},
}


def reference_before(canon, path, tok, inclusive=True, line=None):
    """-> True iff `tok` may name an EARLIER release here: a record this path names exactly, a
    published retired release (a retired spelling with a FORMS suffix), or a current MAJOR.MINOR
    form ordered against the canon. Any other three-number version is no history."""
    m = TOKEN.fullmatch(tok)
    if m is None:
        return False
    if tok.lstrip("v") in PRE_TRANSITION_RECORDS.get(path, ()):
        return True
    if m.group(3):
        return (m.group(4) in FORMS and in_context(tok, line)
                and tok[:m.start(4)] in retired_spellings())
    current = stated(tok, line)
    if current is None:
        return False
    return version_key(current) <= version_key(canon) if inclusive else version_key(current) < version_key(canon)


def judge_artifacts(canon, hits):
    """-> failures among the shipped coordinates: judge()'s verdict, with no curation (no
    artifact root is a curated record). A floor is a floor only on the line that states the
    bound, and the variants only where the records say so (record_allowed, allowed_at)."""
    require_decided(canon)
    return [(hit.path, hit.number, hit.token) for hit in hits
            if not hit_allowed(canon, hit, frozenset(), frozenset())]


def artifact_files():
    out = []
    for root in ARTIFACT_ROOTS:
        for r, dirs, files in walk(root):
            dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "vendor")]
            out += [os.path.join(r, f) for f in files]
    return sorted(f for f in out if regular_file(f) and not go_test_fixture(f))

def read_canon(text):
    """Schema, not scrape: exactly ONE non-comment MAJOR.MINOR record. Anything else refuses before any surface scan — a malformed canon
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
    if not release(canon):
        sys.exit(f"FAIL check-release-version: RELEASE-VERSION record {canon!r} is not MAJOR.MINOR")
    return canon


def require_decided(canon):
    """Every judge's first line: an invalid MAJOR.MINOR canon is refused (exit 2), never judged as 'no failures'.
    read_canon() already refuses it; this keeps a caller that skips the schema from turning
    UNDECIDED back into a pass."""
    if not release(canon):
        unverified(f"UNVERIFIED check-release-version: {canon!r} is not a decided canon "
                   "(bare MAJOR.MINOR); no surface is judged against it.")

def allowed_at(canon, path):
    base = canon
    allowed = {base}
    for suffix, kind, _ in DERIVED_ALLOW:
        if not (path == suffix or path.endswith("/" + suffix)):   # whole path components only
            continue
        if kind == "variants":
            allowed |= {base + h for h in HARDENED}
    return allowed


# Match image coordinates, not github.com repository links. README translations and
# new README languages use the same rule; tagless references must not disappear from the scan.
# The tag runs to a real delimiter (whitespace, quotes, brackets, markdown emphasis and other
# punctuation, or a sentence's final `.` or `:`), so a malformed tag is judged whole, never cut
# short and never padded with the text around it; the judge compares the WHOLE tag.
_TAG_STOP = (r"""[\s@`"'<>()\[\]{}|,;*!?\\$=&#\u00ab\u00bb\u2039\u203a\u201c\u201d\u2018\u2019"""
             r"\u201e\u201a\u2026\u3000-\u303f\uff01\uff08\uff09\uff0c\uff1a\uff1b\uff1f]")
README_IMAGE = re.compile(
    r"(?<![\w/.-])(?:(?:docker\.io|ghcr\.io)/)?olivaresai/olivares"
    r"(?::(?P<tag>(?:(?!" + _TAG_STOP + r").)+?(?=[.:]?(?:" + _TAG_STOP + r"|$))))?"
    r"(?:@sha256:[a-fA-F0-9]+)?(?![\w/+-]|\.\w)"
)


def scan(files, rd):
    """-> [Hit] for every version literal no NOT_RELEASES row names and no numbered heading numbers
    (section_number), and every README image of ours, each with whether it sits inside a release
    mark (marks()).

    `dated` says the hit sits at or below the file's first changelog section heading. It is read
    HERE because it is a property of the whole file, and a judge that only ever sees one line
    cannot recover it. It is computed for every file and consumed by one kind
    (`changelog-history`), so a stray `## [...]` heading elsewhere changes no verdict. `column`
    is the hit's offset in its stripped line, so the stamp rewrites that occurrence and no other."""
    hits = []
    for path in files:
        text = rd(path)
        sections = numbered_sections(path, text)
        hits += file_hits(path, text, dated_from(text), lambda m, path=path, sections=sections:
                          not (not_release(m, path) or section_number(m, sections)))
    return hits


def uncut(path, number, body, form):
    """Refuse a version an inline mark cuts in two (`<!-- release -->1.0<!-- /release -->.1`): the
    reader sees one version, and the census would judge only the part inside the mark."""
    kept, last = [], 0
    for m in re.finditer(form, body):
        kept += range(last, m.start())
        last = m.end()
    if not last:
        return
    kept += range(last, len(body))
    for t in tokens("".join(body[i] for i in kept)):
        if kept[t.end() - 1] - kept[t.start()] != t.end() - 1 - t.start():
            malformed(path, number, "a release mark cuts a version in two; mark the whole version")


def file_hits(path, text, start, wanted):
    """-> [Hit] in one file: every literal inside a mark that no INSIDE_MARKS row names (and, in a
    README, our image coordinates), plus every literal outside one that `wanted` keeps. A mark
    that holds no such literal is malformed."""
    spans = marks(path, text)
    starts = [a for a, _ in spans]
    inline = next((p for suffixes, p in MARK_FORMS if path.endswith(suffixes)), None)
    filled = set()
    readme = re.fullmatch(r"README(?:\.[^.]+)?\.md", path) is not None
    hits = []
    for number, (offset, body) in enumerate(lines_at(text), 1):
        lead = len(body) - len(body.lstrip())
        line = body.strip()
        dated = start is not None and number >= start
        if inline and "release" in body:
            uncut(path, number, body, inline)

        def inside(s, e):
            k = bisect.bisect_right(starts, offset + s) - 1
            return [k] if k >= 0 and offset + e <= spans[k][1] else []
        images = list(README_IMAGE.finditer(body)) if readme else []
        for image in images:
            held = inside(image.start(), image.end())
            filled.update(held)
            hits.append(Hit(path, number, image.group(0), line, False, bool(held), image.start() - lead))
        for m in tokens(body):
            if chart_metadata(m) or any(image.start() <= m.start() < image.end() for image in images):
                continue
            held = inside(m.start(), m.end())
            if held:
                if not not_release(m, path, marked=True):
                    filled.update(held)
                    hits.append(Hit(path, number, m.group(0), line, dated, True, m.start() - lead))
            elif wanted(m):
                hits.append(Hit(path, number, m.group(0), line, dated, False, m.start() - lead))
    for k, (a, _b) in enumerate(spans):
        if k not in filled:
            malformed(path, line_number(text, a), "a release mark holds no version")
    return hits

def fixed_version(line, token):
    """A historical version annotation binds only the immediately preceding token.

    Like a compatibility floor, a fixed version must name an explicit pre-transition
    record or a current-grammar version that does not exceed the release.
    Every occurrence must be annotated; a marker never exempts the rest of a line. The closing
    backtick of a code span may sit between the token and its annotation.
    """
    matches = [m for m in tokens(line) if m.group() == token]
    return bool(matches) and all(FIXED.match(line, m.end()) for m in matches)


FIXED = re.compile(r"`?<!-- release-fixed -->")


def marked_allowed(canon, path, tok, line):
    """-> True iff a marked position states the canon, or a derived form its path records
    (allowed_at). A README image states its tag, which must be the canon."""
    image = README_IMAGE.fullmatch(tok)
    if image:
        return image.group("tag") == canon
    return stated(tok, line) in allowed_at(canon, path)


def record_allowed(canon, path, tok, line, dated, curated, kept=frozenset(), column=None):
    """-> True iff an unmarked literal of the product's lines is a record, never a live claim: an
    annotated fixed version, a recorded floor or upgrade contract, a dated record of a past
    release, or a citation of a changelog section. A live claim of the canon earns nothing here: a live
    release is marked, so the stamp moves it at the next cut.

    min-version is the one kind that needs the LINE, because a floor is only a floor where the
    text states the bound; the records name the paths (DERIVED_ALLOW by path suffix,
    ARTIFACT_ALLOW exactly). Given the hit's `column`, a fixed-version annotation binds that
    occurrence alone, as the stamp binds each rewrite to its occurrence."""
    kinds = record_kinds(path)
    fixed = FIXED.match(line, column + len(tok)) if column is not None else fixed_version(line, tok)
    if fixed and reference_before(canon, path, tok, line=line):
        return True
    if "min-version" in kinds and bounded(line, tok) and reference_before(canon, path, tok, line=line):
        return True
    if "contract-floor" in kinds and contract_assignment(line, tok):
        return True
    if historical_allowed(canon, path, tok, dated, curated, kept, line):
        return True
    # A citation of a section that exists: the canon's own dated section is written before its tag
    # (scripts/check-install-docs.sh requires it), and a section above the canon is one nobody wrote.
    return cited_section(line, tok) and reference_before(canon, path, tok, line=line)


def record_kinds(path):
    """-> the exemption kinds the records grant at `path` (DERIVED_ALLOW by path suffix,
    ARTIFACT_ALLOW exactly)."""
    return ({k for p, k, _ in DERIVED_ALLOW if path == p or path.endswith("/" + p)}
            | {k for p, k, _ in ARTIFACT_ALLOW if path == p})


def in_record(hit):
    """-> True iff an unmarked literal sits in a record structure of ours, which names a release of
    ours whatever its value: a fixed-version annotation, a floor on a path that records one, the
    upgrade contract on its path, or a changelog section heading."""
    kinds = record_kinds(hit.path)
    return bool(FIXED.match(hit.line, hit.column + len(hit.token))
                or ("min-version" in kinds and BOUND.search(hit.line[:hit.column]))
                or ("contract-floor" in kinds and CONTRACT.match(hit.line))
                or (hit.path == CHANGELOG_FILE and SECTION_HEADING.match(hit.line)))


def hit_allowed(canon, hit, curated, kept):
    """-> True iff a hit is no divergence: a marked release that states the canon, a literal
    outside the product's lines and outside every record structure, or a record."""
    if hit.marked:
        return marked_allowed(canon, hit.path, hit.token, hit.line)
    if not (product_line(hit.token) or in_record(hit)):
        return True
    return record_allowed(canon, hit.path, hit.token, hit.line, hit.dated, curated, kept, hit.column)


def judge(canon, hits, curated=None, kept=None):
    """-> (failures, census). The canon must be decided (require_decided)."""
    require_decided(canon)
    census = {}
    for hit in hits:
        if hit.marked or product_line(hit.token) or in_record(hit):
            census.setdefault(hit.token.lstrip("v").replace("-fips", "").replace("-stig", ""),
                              []).append((hit.path, hit.number, hit.token))
    if curated is None:
        curated = curated_out()
    if kept is None:
        kept = curation_kept()
    failures = [(hit.path, hit.number, hit.token) for hit in hits
                if not hit_allowed(canon, hit, curated, kept)]
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
    out = [f for f in ("CHANGELOG.md", "README.md", "SECURITY.md") if root_entry(f) == "file"] + sorted(glob.glob("README.*.md"))
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

COMPOSE_FILES = ("deploy/compose/docker-compose.yml", "deploy/compose/docker-compose.backup.yml")


def stamp_compose(canon: str, text: str) -> str:
    """Keep the image override, replacing only its release-owned default tag."""
    image = "    image: ${OLIVARES_IMAGE:-docker.io/olivaresai/olivares:" + canon.lstrip("v") + "}"
    stamped, count = re.subn(
        r"(?m)^    image: \$\{OLIVARES_IMAGE:-docker\.io/olivaresai/olivares:[^}\s]+\}$",
        lambda _match: image, text)
    if count != 1:
        print("FAIL check-release-version: expected one OLIVARES_IMAGE default in Compose", file=sys.stderr)
        sys.exit(1)
    return stamped


def restamped(tok, canon):
    """-> `tok` naming the canon instead, its FORMS suffix and image repository kept, or None
    when it names no release of ours: a MAJOR.MINOR of the product's line at or below the canon,
    or a retired tag, with a FORMS suffix.
    Anything else (a patch, a v, a prerelease, build metadata, a version nobody tagged) is left
    for the checker to refuse, never repaired into a valid spelling: the export stamps before it
    checks, so a repair would launder it."""
    head, tag = "", tok
    image = README_IMAGE.fullmatch(tok)
    if image:
        if "@" in tok or not image.group("tag"):
            return None
        head, tag = tok[:image.start("tag")], image.group("tag")
    m = TOKEN.fullmatch(tag)
    if not m or m.group(4) not in FORMS:
        return None
    core = tag[:m.start(4)]
    # A marked 0.x is ours too: product_line() leaves MAJOR 0 to prose, where a bare 0.N is a
    # decimal, but inside a release mark it can only be a release of the 0.x line.
    ours = (release(core) and (product_line(core) or version_key(core)[0] == version_key("0.0")[0])
            and version_key(core) <= version_key(canon))
    if not (ours or core in retired_spellings()):
        return None  # another project's version in a mark (Go 1.26, postgres:16.4) stays for the checker
    return head + canon + m.group(4)


def stamp_surfaces(canon, hits):
    """Rewrite every marked release position from the canon; nothing outside a mark moves.

    A marked version restamped() cannot derive (a patch, a v, a suffix nobody tagged, another
    project's version) is left for the checker to refuse, never repaired into a valid spelling:
    the export stamps before it checks, so a repair would launder it. The same occurrence may
    reach here from more than one census; it is rewritten once, at its own column. Every swap is
    checked before any file is written, and each file is replaced whole."""
    swaps = {}
    for hit in hits:
        replacement = restamped(hit.token, canon) if hit.marked else None
        if replacement is not None and replacement != hit.token:
            swaps.setdefault(hit.path, {}).setdefault(hit.number, {})[hit.column] = (hit.token, replacement)
    stamped = {}
    for path, rows in swaps.items():
        with open(path, "rb") as source:  # every byte back as it was, a non-UTF-8 one included
            lines = source.read().decode("utf-8", "surrogateescape").splitlines(keepends=True)
        for number, row in rows.items():
            line = lines[number - 1]
            lead = len(line) - len(line.lstrip())
            out, last = [], 0
            for column, (token, replacement) in sorted(row.items()):
                at = lead + column
                if line[at:at + len(token)] != token:
                    sys.exit(f"FAIL check-release-version: {path}:{number} changed under the stamp; "
                             "nothing was written")
                out += [line[last:at], replacement]
                last = at + len(token)
            lines[number - 1] = "".join(out) + line[last:]
        stamped[path] = "".join(lines).encode("utf-8", "surrogateescape")
    for path, data in stamped.items():
        with open(path + ".stamp-tmp", "wb") as output:
            output.write(data)
        shutil.copymode(path, path + ".stamp-tmp")
        os.replace(path + ".stamp-tmp", path)
    return len(swaps)


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
    canon_refuses("two canon records -> refuse", "26.700\n29.100\n")
    canon_refuses("patch canon -> refuse", "1.0.1\n")
    canon_refuses("CalVer canon -> refuse", "26.10.2\n")
    expect("well-formed canon -> accepted", read_canon("26.700\n") == "26.700")
    # ── the tag-name correction: bare CalVer from 26.1000 on, v-shaped history before it ──
    expect("bare canon from 26.1000 -> accepted", read_canon("26.1000\n") == "26.1000")
    expect("monthly canon -> accepted", read_canon("26.1100\n") == "26.1100")
    expect("patch canon -> accepted", read_canon("26.1101\n") == "26.1101")
    canon_refuses("zero patch canon -> refused", "1.0.0\n")
    monthly_tree = {"README.md": "Olivares <!-- release -->26.1100<!-- /release --> today",
                    "deploy/fixture.yaml": "# release\nimage: olivaresai/olivares:26.1100\n# /release\n"}
    expect("monthly surface pins -> green", judge("26.1100", scan(monthly_tree, rd(monthly_tree)))[0] == [])
    stale_tree = {"README.md": "Olivares 26.1000 today"}
    expect("monthly canon sees historical stale surface", judge("26.1100", scan(stale_tree, rd(stale_tree)))[0] == [("README.md", 1, "26.1000")])
    canon_refuses("v-prefixed canon -> refuse", "v1.0\n")
    canon_refuses("suffix canon -> refuse", "1.0-rc.1\n")
    # README container references must name the release even without a CalVer token.
    for path in ("README.md", "README.es.md", "README.new.md"):
        for registry in ("docker.io/", "ghcr.io/", ""):
            image = registry + "olivaresai/olivares"
            for suffix in ("", ":latest", ":stable", ":26.1000", ":26.1001-preview",
                           ":26.1001.9", "@sha256:" + "a" * 64):
                ref = image + suffix
                tree = {path: "docker run " + ref + "\n"}
                failures, _ = judge("26.1001", scan(tree, rd(tree)))
                expect(f"{path} rejects image {ref}", failures == [(path, 1, ref)])
            for canon in ("26.1001", "26.1100", "26.900"):
                ref = image + ":" + canon.lstrip("v")
                tree = {path: "<!-- release -->\nUse `" + ref + "`\ndocker run " + ref + "\n<!-- /release -->\n"}
                expect(f"{path} accepts every marked {canon} image occurrence",
                       judge(canon, scan(tree, rd(tree)))[0] == [])
                tree = {path: "docker run " + ref + "\n"}
                expect(f"{path} refuses the {canon} image outside a release mark",
                       judge(canon, scan(tree, rd(tree)))[0] == [(path, 1, ref)])
    tree = {"README.md": "Run: <!-- release -->docker run docker.io/olivaresai/olivares:26.1001<!-- /release -->\n"
                          "docker run ghcr.io/olivaresai/olivares:latest\n"}
    expect("a pinned README image cannot hide a second floating image",
           judge("26.1001", scan(tree, rd(tree)))[0]
           == [("README.md", 2, "ghcr.io/olivaresai/olivares:latest")])
    tree = {"README.md": "https://github.com/olivaresai/olivares.git\n"
                          "https://github.com/olivaresai/olivares/releases\n"}
    expect("README repository links are not container images",
           judge("26.1001", scan(tree, rd(tree)))[0] == [])
    tree = {"README.md": "Run: <!-- release -->docker run docker.io/olivaresai/olivares:26.1001@sha256:" + "a" * 64
                          + "<!-- /release -->"}
    expect("a README release tag can also be pinned by digest",
           judge("26.1001", scan(tree, rd(tree)))[0] == [])
    tree = {"deploy/README.md": "Registry: `docker.io/olivaresai/olivares`"}
    expect("registry names in detailed deployment docs keep their existing scope",
           judge("26.1001", scan(tree, rd(tree)))[0] == [])
    # ── decided canon: divergence is red; derived forms only in their contexts ──
    tree = {"README.md": "ships with <!-- release -->`26.700`<!-- /release --> today",
            "CHANGELOG.md": "the first release is `26.600`"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("divergent CHANGELOG under decided canon -> red", fails == [("CHANGELOG.md", 1, "26.600")])
    tree = {"docs-site/src/content/docs/how-to/docker-deployment.md":
            "pull olivares:<!-- release -->26.700-fips<!-- /release --> then upgrade to 26.701"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("fips INSIDE the upgrade doc -> green; the canon's next patch there -> red (no invented release)",
           fails == [("docs-site/src/content/docs/how-to/docker-deployment.md", 1, "26.701")])
    # Under a BARE canon only the bare form states the release; the v-prefixed spelling of
    # the same version is a divergence the sweep reports (the correction's surface rule).
    tree = {"README.md": "run olivares <!-- release -->26.1000<!-- /release --> today"}
    fails, _ = judge("26.1000", scan(tree, rd(tree)))
    expect("bare canon in a release mark -> green", fails == [])
    tree = {"README.md": "run olivares 26.1000 today"}
    fails, _ = judge("26.1000", scan(tree, rd(tree)))
    expect("bare canon outside a release mark -> red (the stamp would leave it behind)",
           fails == [("README.md", 1, "26.1000")])
    tree = {"README.md": "run olivares v26.1000 today"}
    fails, _ = judge("26.1000", scan(tree, rd(tree)))
    expect("v-prefixed 26.1000 on a surface -> red under the bare canon",
           fails == [("README.md", 1, "v26.1000")])
    tree = {"CHANGELOG.md": "## [26.1000] - 2026-10-01\nships today\n\n## [26.900] - 2026-09-17\nthe record of 26.900"}
    fails, _ = judge("26.1000", scan(tree, rd(tree)))
    expect("26.900 history under the bare canon -> green (the record keeps its shape)",
           fails == [])
    tree = {"README.md": "the first release will be 26.701"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("next-patch OUTSIDE its documented example -> red", fails == [("README.md", 1, "26.701")])
    tree = {"README.md": "grab olivares:<!-- release -->26.700-fips<!-- /release -->"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("fips variant outside its image-tag docs -> red", fails == [("README.md", 1, "26.700-fips")])
    marker = "<<" + "FRAN"
    tree = {"b.md": f"Olivares 26.100 hiding here {marker}: confirm>>"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("foreign release on a readiness-marker line -> red (no line waiver)",
           fails == [("b.md", 1, "26.100")])
    tree = {"c.md": "release <!-- release -->v0.1.0<!-- /release --> pending"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("a prefixed patch in a release mark -> red, whatever its MAJOR", fails == [("c.md", 1, "v0.1.0")])
    # The upgrade-contract coordinate: green on its one assignment, red everywhere else.
    line = "first_contract_version='26.800~'"
    tree = {"packaging/nfpm/postinstall.sh": line}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("upgrade-contract coordinate above the canon, on its assignment -> green", fails == [])
    tree = {"packaging/nfpm/postinstall.sh": "echo see 26.800 today"}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("the same value outside the assignment on the same file -> red", fails == [("packaging/nfpm/postinstall.sh", 1, "26.800")])
    tree = {"packaging/nfpm/postinstall.sh": "echo see 26.800 today\n" + line}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("a stray value on another line does not reach the assignment's allowance -> red there, green on it",
           fails == [("packaging/nfpm/postinstall.sh", 1, "26.800")])
    tree = {"packaging/nfpm/prerm.sh": line}
    fails, _ = judge("26.700", scan(tree, rd(tree)))
    expect("the assignment on a file the records do not name -> red", fails == [("packaging/nfpm/prerm.sh", 1, "26.800")])
    tree = {"packaging/nfpm/postinstall.sh": line}
    expect("the same coordinate in the artifact census -> green for the same reason",
           judge_artifacts("26.700", scan(tree, rd(tree))) == [])
    tree = {"d.md": "requires Go 1.26.500 and chi v5.3.1"}
    expect("a dependency version is named (Go) or red: no row names chi",
           judge("1.0", scan(tree, rd(tree)))[0] == [("d.md", 1, "v5.3.1")]
           and [h.token for h in scan(tree, rd(tree))] == ["v5.3.1"])
    references = {"d.md": "target 99.900% uptime; see SPEC §47.100 and ORD-47.100"}
    expect("percentage and specification section are not monthly releases", scan(references, rd(references)) == [])
    tree = {"e.md": "since 26.9 things"}
    expect("month-zero token -> not product-shaped", judge("1.0", scan(tree, rd(tree)))[0] == [])
    _, census = judge("26.700", scan({"f.md": "26.600 and 26.700"}, rd({"f.md": "26.600 and 26.700"})))
    expect("the divergence census groups every shaped token by value", set(census) == {"26.600", "26.700"})
    # ── arm B: the pin/floor distinction, and the coverage that made arm A caducate ──
    pins = lambda text, path="operator/config/samples/x.yaml", marked=False: judge_pins(
        "26.700", [Hit(path, i, m.group(0), ln, False, marked, m.start())
                    for i, ln in enumerate(text.splitlines(), 1) for m in pin_tokens(ln)],
        curated=set())
    expect("stale image PIN in a tree arm A never scans -> red",
           pins("  image: docker.io/olivaresai/olivares:26.600\n") ==
           [("operator/config/samples/x.yaml", 1, "26.600")])
    expect("current image pin in a release mark -> green", pins("  image: olivaresai/olivares:26.700\n", marked=True) == [])
    expect("current image pin outside a release mark -> red (the stamp would leave it behind)",
           pins("  image: olivaresai/olivares:26.700\n") == [("operator/config/samples/x.yaml", 1, "26.700")])
    expect("a compatibility FLOOR is not a pin -> green (rewriting it would falsify history)",
           pins("// role label (olivares >= 26.600). With an older image every pod fails\n") == [])
    expect("a package FILENAME is a pin -> red when stale",
           pins("curl -O https://dl/olivares-26.600-1.x86_64.rpm\n")
           == [("operator/config/samples/x.yaml", 1, "26.600-1")])
    expect("the same package FILENAME at the canon -> green (its revision is a FORMS suffix)",
           pins("curl -O https://dl/olivares-26.700-1.x86_64.rpm\n", marked=True) == [])
    expect("hardened pin follows the SAME DERIVED_ALLOW as arm A",
           pins("olivaresai/olivares:26.700-fips", "packaging/docker/dockerhub-overview.md", True) == []
           and len(pins("olivaresai/olivares:26.700-fips", "operator/x.yaml", True)) == 1)
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
               {"deploy", "packaging", "docs-site"} <= set(real_tops) and len(real_tops) > 15
               and "TOP_ALLOW" in src_name)
    else:
        # The export script is absent BY DESIGN here, so the fallback is not a stub: it is the
        # real census of the real tree, the verdict must name it, and it must reach what ships.
        expect("public export: the export script is absent, so the census is the tree itself and the verdict says so",
               not os.path.exists(EXPORT_SCRIPT) and "absent" in src_name and "TOP_ALLOW" not in src_name)
        expect("public export: the fallback census reaches every artefact root and both docs trees",
               {"deploy", "packaging", "docs-site", "docs"} <= set(real_tops) and len(real_tops) > 15)
        # scan_pins yields (path, line no, token, line, dated): FIVE fields. This branch runs only in
        # the public export, where the first hosted rehearsal of the exported 26.900 tree found it
        # unpacking four (ValueError, 2026-09-16); the full source tree never executes it. Bind the path only.
        walked = {hit[0].split("/", 1)[0] for hit in scan_pins([t for t in real_tops if t in ARTIFACT_ROOTS])}
        expect("public export: the fallback census walks to the shipped pins on disk (deploy/ and packaging/)",
               {"deploy", "packaging"} <= walked)

    # ── documentation scope: docs/ is WALKED, and floors survive the widening ──
    # Every case below is the witness for one rule of the docs/ widening. They are written
    # against judge() — the DOCUMENTARY arm — because that is what the widening changed;
    # a case that passes by reaching the artifact arm would prove nothing here.
    tree = {"docs/UPGRADE-AND-ROLLBACK.md": "container tags drop the leading v (`:26.700`)"}
    fails, _ = judge("26.800", scan(tree, rd(tree)))
    expect("stale image tag in a top-level runbook -> red (the walk reaches docs/*.md)",
           fails == [("docs/UPGRADE-AND-ROLLBACK.md", 1, "26.700")])
    tree = {"docs/UPGRADE-AND-ROLLBACK.md": "(<!-- release -->`:26.800`, `:latest`, `:26.800-fips`, `:26.800-stig`<!-- /release -->)"}
    expect("fips/stig variants at canon in the upgrade guide -> green",
           judge("26.800", scan(tree, rd(tree)))[0] == [])
    # The floor rule, carried across from the artifact arm. Without it the widening would
    # have SWEPT this bound to the canon and narrowed the supported range to the newest
    # release — the defect this gate was preferred for not having.
    tree = {"docs/HA-LEADER-ROUTING.md": "Roll `spec.image` to ≥ 26.700 *while still on"}
    expect("prose compatibility floor below canon -> green (a floor is not a claim)",
           judge("26.800", scan(tree, rd(tree)))[0] == [])
    tree = {"docs/HA-LEADER-ROUTING.md": "Roll `spec.image` to ≥ 26.900 *while still on"}
    expect("prose floor ABOVE canon -> red (requires an engine we do not ship)",
           judge("26.800", scan(tree, rd(tree)))[0] == [("docs/HA-LEADER-ROUTING.md", 1, "26.900")])
    tree = {"docs/HA-LEADER-ROUTING.md": "image: docker.io/olivaresai/olivares:26.700"}
    expect("floor path, token OFF the bound -> red (narrower allowance, not a waiver)",
           judge("26.800", scan(tree, rd(tree)))[0] == [("docs/HA-LEADER-ROUTING.md", 1, "26.700")])
    # The measured hole: one advisory line carries a lower bound AND a shipped version.
    tree = {"docs/PSIRT-RUNBOOK.md":
            '"ranges": [ { "introduced": "26.500", "fixed": "26.701" } ] } ],'}
    fails, _ = judge("26.800", scan(tree, rd(tree)))
    expect("advisory range: `introduced` stays a floor, `fixed` does NOT inherit its amnesty",
           fails == [("docs/PSIRT-RUNBOOK.md", 1, "26.701")])
    tree = {"docs/PSIRT-RUNBOOK.md": "--advisory GHSA-xxxx-yyyy-zzzz --min-version 26.500 \\"}
    expect("--min-version is lower-bound vocabulary -> green",
           judge("26.800", scan(tree, rd(tree)))[0] == [])
    # The release-key ledger: the bound row is a floor, every other token in the SAME file is not.
    tree = {"docs/RELEASE-VERIFICATION.md": "| ≥ 26.800 | license | `AAAA` | `beef` | `beef` |"}
    expect("key-ledger coverage floor below canon -> green (a floor is not a shipped coordinate)",
           judge("26.900", scan(tree, rd(tree)))[0] == [])
    tree = {"docs/RELEASE-VERIFICATION.md": "  --expect-channel stable --expect-version 26.800"}
    expect("key-ledger path, token OFF the bound -> red (the live command must state the canon)",
           judge("26.900", scan(tree, rd(tree)))[0]
           == [("docs/RELEASE-VERIFICATION.md", 1, "26.800")])
    tree = {"docs/RELEASE-VERIFICATION.md": "| ≥ 26.1000 | license | `AAAA` | `beef` | `beef` |"}
    expect("key-ledger floor ABOVE canon -> red (no pair covers a release we do not ship)",
           judge("26.900", scan(tree, rd(tree)))[0]
           == [("docs/RELEASE-VERIFICATION.md", 1, "26.1000")])
    # An unexempted doc is required to state the canon exactly — the safe default that
    # makes a NEW declarant caught without touching this file.
    tree = {"docs/A-BRAND-NEW-RUNBOOK.md": "deploy olivares:26.700 to the fleet"}
    expect("a doc no record exempts -> red (the eighth declarant is born visible)",
           judge("26.800", scan(tree, rd(tree)))[0]
           == [("docs/A-BRAND-NEW-RUNBOOK.md", 1, "26.700")])
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
    tree = {"docs/SOME-OTHER-GUIDE.md": "requires olivares >= 26.700 at minimum"}
    expect("bound in a doc with NO min-version record -> red (the allowance is by path)",
           judge("26.800", scan(tree, rd(tree)))[0]
           == [("docs/SOME-OTHER-GUIDE.md", 1, "26.700")])

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
    tree = {"docs/ai-context/fixture-next-release.md": "the owner fixed **26.900** as the delivery objective"}
    expect("judged by itself the mandate token IS divergent: the exemption lives in the census, not in the judge",
           judge("26.800", scan(tree, rd(tree)))[0] == [("docs/ai-context/fixture-next-release.md", 1, "26.900")])
    tree = {"docs/RELEASE-CANDIDATE-NOTES.md": "the next release will be 26.900"}
    expect("a NEW target in a published doc stays red (no blanket skip of docs/)",
           judge("26.800", scan(tree, rd(tree)))[0] == [("docs/RELEASE-CANDIDATE-NOTES.md", 1, "26.900")])
    tree = {"docs-site/src/content/docs/how-to/install-from-packages.md": "The 26.700 GitHub release publishes `.deb`, `.rpm` and `.apk` assets"}
    expect("a STALE version in a published guide stays red",
           judge("26.800", scan(tree, rd(tree)))[0] == [("docs-site/src/content/docs/how-to/install-from-packages.md", 1, "26.700")])
    expect("a STALE package FILENAME in a published guide is a pin arm B judges red",
           judge_pins("26.800", [Hit("docs-site/src/content/docs/how-to/air-gap-install.md", 1, m.group(0), "x", False, False, m.start())
                                  for m in pin_tokens("curl -O https://dl/olivares-26.700-1.x86_64.rpm")],
                      curated=set())
           == [("docs-site/src/content/docs/how-to/air-gap-install.md", 1, "26.700-1")])
    pins8 = lambda text, path: judge_pins(
        "26.800", [Hit(path, i, m.group(0), ln, False, False, m.start())
                    for i, ln in enumerate(text.splitlines(), 1) for m in pin_tokens(ln)],
        curated=set())
    expect("Go test fixture: the multi-version regression's next-version key is test data -> green",
           pins8('b.mint("link9", "26.900"); readsContain(b.reads(), "olivares_26.900")\n',
                 "cmd/olivares/cmd_upgrade_releasev1_bridge_test.go") == [])
    expect("Go test fixture: a STALE key in a _test.go is test data too (a multi-version case needs both sides)",
           pins8('readsContain(b.reads(), "olivares_26.700")\n', "cmd/olivares/x_test.go") == [])
    expect("production Go: the same NEW pin in a non-test .go file stays red",
           pins8('const coordinate = "olivares_26.900"\n', "cmd/olivares/cmd_upgrade.go")
           == [("cmd/olivares/cmd_upgrade.go", 1, "26.900")])
    expect("production Go: a STALE pin in a non-test .go file stays red",
           pins8('const coordinate = "olivares_26.700"\n', "core/release/x.go")
           == [("core/release/x.go", 1, "26.700")])
    expect("manifest: a NEW image pin in deploy/ stays red (a target is not what ships)",
           pins8("  image: docker.io/olivaresai/olivares:26.900\n", "deploy/manifests/install.yaml")
           == [("deploy/manifests/install.yaml", 1, "26.900")])
    expect("the fixture class is Go's rule, not a directory: testdata-looking .go that is not _test.go is judged",
           len(pins8('"olivares_26.700"\n', "cmd/olivares/testdata_helpers.go")) == 1
           and go_test_fixture("a/b_test.go") and not go_test_fixture("a/b_test.go.txt"))

    # ── the dated record of a release that already happened (added 2026-09-15) ──
    # THE CASE THIS EXISTS FOR, measured on the 26.900 cut: with the canon re-derived, 383 of
    # the 777 shaped tokens sat in documents whose whole subject is a release that SHIPPED — the
    # `[26.800]` changelog section, the dated install-surface witness, the launch pack that was
    # posted, the runbooks that cut the tag. Sweeping those to the new canon does not correct a
    # claim; it falsifies a record, which is the one thing this file has refused since the
    # dated-ADR prune. Every case below is the witness for one rule of the allowance, and each
    # green one is paired with the red that keeps it narrow.
    hist = {"docs/launch"}                        # one curated-out DIRECTORY member, injected
    cur2 = {"docs/launch", "docs/contracts"}      # both of them, for the contract cases
    # 1 · a record may name a release that HAPPENED …
    tree = {"docs/launch/fixture-release-post.md": "the first tag `26.800` is published"}
    expect("historical: a launch record names the release it records -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/releases/26.800-install-surfaces.json": '  "version": "26.800",'}
    expect("historical: the dated install witness of a past release -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/RELEASE-GO-LIVE-RUNBOOK.md": "git tag -s 26.800 -m 'Olivares AI 26.800'"}
    expect("historical: the runbook that cut the past tag -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    # 2 · … and NEVER one that did not. A record is of the past, so the allowance is strictly
    #     below the canon: the canon itself is the live claim every other rule already judges,
    #     and anything above it is a release nobody cut.
    tree = {"docs/launch/fixture-release-post.md": "next up is 26.1000"}
    expect("historical: a token ABOVE the canon inside a record -> red (no release was cut)",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0]
           == [("docs/launch/fixture-release-post.md", 1, "26.1000")])
    # 3 · the class is EXACT — the sibling gate's own warning, restated for every member here.
    #     A substring test reads identically and swallows live documentation merely NAMED like
    #     a record, so each of these is a file that LOOKS historical and is not.
    for path in ("docs/launch-notes/old.md", "docs/releases-archive/x.md",
                 "docs/claims/measurements-old/x.md", "docs/RELEASE-A-NEW-GUIDE.md",
                 "docs/contracts-draft/x.md", "docs/accessibility/VPAT-olivares-admin.md.bak"):
        tree = {path: "deploy olivares:26.800 to the fleet"}
        expect(f"historical: {path} is NOT a member -> red (exact path, never a substring)",
               judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [(path, 1, "26.800")])
    # 4 · a live surface is untouched by any of this: the stray past version stays red exactly
    #     where a reader would act on it. These three are the surfaces the brief names.
    for path in ("README.md", "INSTALL.md",
                 "docs-site/src/content/docs/how-to/install-from-packages.md"):
        tree = {path: "install the published `26.800` release"}
        expect(f"historical: a stray past version on the live surface {path} -> red",
               judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [(path, 1, "26.800")])
    # 5 · the two DIRECTORY members are tied to the export's own curation, exactly as the
    #     internal-context prune is: the allowance holds only while the curation says the
    #     directory never ships, and it stops with it. Fail-closed toward judging.
    tree = {"docs/launch/fixture-release-post.md": "the first tag `26.800` is published"}
    expect("historical: WITHOUT the curation the launch record is judged again",
           judge("26.900", scan(tree, rd(tree)), curated=set())[0]
           == [("docs/launch/fixture-release-post.md", 1, "26.800")])
    # The second tied directory, and the case its tie exists for: a file the export RE-PUBLISHES
    # out of a blocked directory (DOCS_KEEP) is not covered by that directory's allowance, because
    # the tie's whole claim — "this never ships" — is false for it.
    tree = {"docs/contracts/fixture-contract-note.md": "this tree's canon was 26.800"}
    expect("historical: a curated-out contract record -> green with the curation, red without",
           judge("26.900", scan(tree, rd(tree)), curated=cur2)[0] == []
           and judge("26.900", scan(tree, rd(tree)), curated=set())[0]
           == [("docs/contracts/fixture-contract-note.md", 1, "26.800")])
    tree = {"docs/contracts/kept-and-published.md": "this tree's canon was 26.800"}
    expect("historical: a file DOCS_KEEP re-publishes out of a tied directory earns nothing -> red",
           judge("26.900", scan(tree, rd(tree)), curated=cur2,
                 kept={"docs/contracts/kept-and-published.md"})[0]
           == [("docs/contracts/kept-and-published.md", 1, "26.800")]
           and judge("26.900", scan(tree, rd(tree)), curated=cur2, kept=set())[0] == [])
    # The three kinds whose only member is a SHIPPED file get their green witness here, so the
    # table is not exercised by the real tree alone.
    tree = {"docs/claims/measurements/2026-09-12/AUDIT.md": "the public release of `26.800` is a separate dimension"}
    expect("historical: a dated measurement names the release it measured -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/accessibility/VPAT-olivares-admin.md": "the 26.800 release run executed the gate over 59 routes"}
    expect("historical: a conformance record names the run that produced its evidence -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/RELEASE-CHANNEL-POLICY.md": "| GitHub release archives | live for 26.800 |"}
    expect("historical: a measured publication state is not swept to an unpublished version -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    # The witness ledger is narrower than its directory: only the version ON THE LABEL.
    tree = {"docs/releases/26.800-install-surfaces.json": '  "version": "26.800",'}
    expect("historical: a witness may name the version in its own filename -> green",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0] == [])
    tree = {"docs/releases/26.800-install-surfaces.json": '  "evidence": "measured against 26.500",'}
    expect("historical: a witness naming ANOTHER past release -> red (the label is the allowance)",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0]
           == [("docs/releases/26.800-install-surfaces.json", 1, "26.500")])
    tree = {"docs/releases/NOTES.md": "see 26.800"}
    expect("historical: a docs/releases file with NO version label earns nothing -> red",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0]
           == [("docs/releases/NOTES.md", 1, "26.800")])
    # 6 · THE CHANGELOG IS NARROWER THAN THE REST, and this is the case that keeps the gate's
    #     founding defect caught. Its masthead — title, format note, status block — states what
    #     ships TODAY; only the sections below `## [Unreleased]` are the record.
    masthead = ("# Changelog\n\nCalVer `vYY.M.PATCH`; the current release is `26.800`.\n\n"
                "## [Unreleased]\n\n## [26.800] - 2026-09-01\n\nTag `26.800` points at a commit\n")
    tree = {"CHANGELOG.md": masthead}
    fails, _ = judge("26.900", scan(tree, rd(tree)), curated=hist)
    expect("historical: the changelog MASTHEAD states the canon, the dated section keeps its own",
           fails == [("CHANGELOG.md", 3, "26.800")])
    tree = {"CHANGELOG.md": "# Changelog\n\nthe release is `26.800`\n"}
    expect("historical: a changelog with NO section heading is judged whole (fail-closed)",
           judge("26.900", scan(tree, rd(tree)), curated=hist)[0]
           == [("CHANGELOG.md", 3, "26.800")])
    expect("historical: dated_from finds the first section heading and only a heading",
           dated_from(masthead) == 5 and dated_from("# Changelog\n\n## [26.800] - 2026-09-01\n") == 3
           and dated_from("## [Not a version]\n") is None
           and dated_from("text `## [Unreleased]` quoted inline\n") is None)
    # 7 · ARM B follows the same class, because a record quotes image pins too (the `[26.800]`
    #     changelog section names `docker.io/olivaresai/olivares:26.800` on two lines).
    histpin = lambda text, path, dated: judge_pins(
        "26.900", [Hit(path, i, m.group(0), ln, dated, False, m.start())
                    for i, ln in enumerate(text.splitlines(), 1) for m in pin_tokens(ln)],
        curated=hist)
    expect("historical, arm B: a past image pin inside a launch record -> green",
           histpin("  image: docker.io/olivaresai/olivares:26.800\n",
                   "docs/launch/fixture-index.md", False) == [])
    expect("historical, arm B: the same pin in a dated changelog section -> green",
           histpin("images `docker.io/olivaresai/olivares:26.800` mirrored\n",
                   "CHANGELOG.md", True) == [])
    expect("historical, arm B: the same pin in the changelog MASTHEAD -> red",
           histpin("images `docker.io/olivaresai/olivares:26.800` today\n",
                   "CHANGELOG.md", False)
           == [("CHANGELOG.md", 1, "26.800")])
    expect("historical, arm B: the same pin in a shipped manifest -> red (a record is not a coordinate)",
           histpin("  image: docker.io/olivaresai/olivares:26.800\n",
                   "deploy/manifests/install.yaml", False)
           == [("deploy/manifests/install.yaml", 1, "26.800")])
    # 8b · TWO PAST RELEASES, NOT ONE (added 2026-09-18). The 26.901 cut is the first with
    #      more than one shipped release behind it, and every case above happens to use a single
    #      past version. "Strictly below the canon" is what the rule says and "the previous
    #      release" is what a reader may assume it says; these cases hold it to the first.
    ledger = {"docs/launch/fixture-two-releases.md":
              "the first tag `26.800` shipped, and `26.900` followed it"}
    expect("historical: a record naming TWO past releases -> both green",
           judge("26.901", scan(ledger, rd(ledger)), curated=hist)[0] == [])
    # The ledger holds one file per release, and each admits ONLY its own label. Asserted in
    # both directions, because "any past version under docs/releases" would pass one direction
    # and is exactly what the self-bound kind refuses.
    older = {"docs/releases/26.800-install-surfaces.json": '  "version": "26.800",'}
    newer = {"docs/releases/26.900-install-surfaces.json": '  "version": "26.900",'}
    crossed = {"docs/releases/26.900-install-surfaces.json": '  "evidence": "measured against 26.800",'}
    expect("historical: each witness admits its own label and refuses its neighbour's",
           judge("26.901", scan(older, rd(older)), curated=hist)[0] == []
           and judge("26.901", scan(newer, rd(newer)), curated=hist)[0] == []
           and judge("26.901", scan(crossed, rd(crossed)), curated=hist)[0]
           == [("docs/releases/26.900-install-surfaces.json", 1, "26.800")])
    # THE ONE THAT FIRED FOR REAL while this cut was written: the CURRENT release's witness
    # named the previous tag in an evidence sentence. The filename is the allowance, so the
    # canon's own witness may not name a past release either — measured, and the sentence was
    # rewritten rather than exempted.
    current = {"docs/releases/26.901-install-surfaces.json":
               '  "evidence": "the 26.900 tag is published and keeps its own witness",'}
    expect("historical: the CURRENT witness naming a past release -> red (the label is the allowance)",
           judge("26.901", scan(current, rd(current)), curated=hist)[0]
           == [("docs/releases/26.901-install-surfaces.json", 1, "26.900")])
    # 8c · THE BARE ERA'S LEDGER (added 2026-10-02, on the 26.1001 cut). 26.1000 is the first
    #      release with a bare label, and its witness must keep its allowance once the canon moves
    #      past it. A label in the wrong shape for its era names no tagged release: it earns nothing.
    bare_prev = {"docs/releases/26.1000-install-surfaces.json": '  "version": "26.1000",'}
    v_in_bare_era = {"docs/releases/v26.1000-install-surfaces.json": '  "version": "26.1000",'}
    bare_before_era = {"docs/releases/26.9.0-install-surfaces.json": '  "version": "26.9.0",'}
    expect("historical: a bare-era witness admits its own label under a later canon",
           judge("26.1001", scan(bare_prev, rd(bare_prev)), curated=hist)[0] == [])
    expect("historical: a v label in the bare era, or a bare label before it, earns nothing",
           judge("26.1001", scan(v_in_bare_era, rd(v_in_bare_era)), curated=hist)[0]
           == [("docs/releases/v26.1000-install-surfaces.json", 1, "26.1000")]
           and judge("26.1001", scan(bare_before_era, rd(bare_before_era)), curated=hist)[0]
           == [("docs/releases/26.9.0-install-surfaces.json", 1, "26.9.0")])
    # A changelog with TWO dated sections: both are the record, and the masthead above them is
    # still the live claim. The founding defect of this gate lives in that masthead.
    two = ("# Changelog\n\nthe current release is `26.900`.\n\n## [Unreleased]\n\n"
           "## [26.900] - 2026-09-16\n\nTag `26.900` points at a commit\n\n"
           "## [26.800] - 2026-09-01\n\nTag `26.800` points at another\n")
    tree = {"CHANGELOG.md": two}
    expect("historical: two dated sections are both the record; the masthead is not",
           judge("26.901", scan(tree, rd(tree)), curated=hist)[0] == [("CHANGELOG.md", 3, "26.900")])
    # And the generation that is NOT past: a record may not name the canon's successor, however
    # many releases sit below it.
    ahead = {"docs/launch/fixture-two-releases.md": "next up is 26.902"}
    expect("historical: with two releases behind it, a record still may not name one ahead",
           judge("26.901", scan(ahead, rd(ahead)), curated=hist)[0]
           == [("docs/launch/fixture-two-releases.md", 1, "26.902")])
    # 9 · A CITATION OF A DATED SECTION IS NOT A CLAIM ABOUT WHAT SHIPS (added 2026-09-18).
    #     Measured on the 26.901 cut: 146 of the 589 divergences were pages naming the
    #     `[26.900]` changelog section as the SOURCE of a behaviour they document — ten how-to
    #     and reference pages in seven locales, all written after the 26.900 tag. Sweeping a
    #     citation to the new canon does not correct a claim; it points the reader at a section
    #     that does not carry the entry. The class is bound by FORM, so every case below either
    #     proves the form or proves what the form refuses.
    cite = lambda text, path="docs-site/src/content/docs/how-to/x.md": judge(
        "26.901", scan({path: text}, rd({path: text})), curated=hist)[0]
    expect("citation: a page naming a past changelog section -> green",
           cite("the turn ends and the process stays usable (`CHANGELOG.md` `[26.900]`).") == [])
    expect("citation: the same past version stated BARE on the same page -> red",
           cite("26.900 keys every live session row by its own identity.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.900")])
    expect("citation: a bracketed version on a line that does NOT name the changelog -> red",
           cite("pin the `[26.900]` image before you roll the fleet.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.900")])
    # EVERY occurrence, not any — the `bounded()` precedent, for the same reason: a line that
    # cites a section AND states the version bare is ambiguous, and an ambiguous claim is judged
    # as the stale claim it might be. Measured: seven real lines in seven locales read exactly
    # like this, and they are rewritten rather than exempted.
    expect("citation: a citation and a bare token on ONE line -> the bare one stays red",
           cite("Source for the 26.900 behavior: `CHANGELOG.md` section `[26.900]`.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.900")] * 2)
    # THE SAME TOKEN TWICE, which is what "every" and "any" actually disagree about. The case
    # above has one bare occurrence and one bracketed one of DIFFERENT tokens (`26.900` and
    # `26.900`), so either quantifier answers it. A mutation control found that gap: with `any`
    # the battery stayed green. This line is the one that fails under it.
    expect("citation: the same token cited AND stated bare -> red (every, not any)",
           cite("see `CHANGELOG.md` `[26.900]`, then install 26.900 on every node.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.900")] * 2)
    expect("citation: a section ABOVE the canon -> red (nobody wrote that section)",
           cite("see `CHANGELOG.md` `[26.902]` for the fix.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.902")])
    expect("citation: a hardened variant in brackets is not a section reference -> red",
           cite("see `CHANGELOG.md` `[26.900-fips]` for the hardened build.")
           == [("docs-site/src/content/docs/how-to/x.md", 1, "26.900-fips")])
    expect("citation: a wrapped citation is named as wrapped, not left as a puzzle",
           wrapped_citation("`[26.900]` Added; `INSTALL.md`).", "26.900")
           and not wrapped_citation("see `CHANGELOG.md` `[26.900]`", "26.900")
           and not wrapped_citation("pin `[26.900]` and 26.900 too", "26.900")
           and not wrapped_citation("deploy 26.900 today", "26.900"))
    # The changelog's OWN headings are reached by `changelog-history`, not by this rule: a
    # heading does not name the file it sits in. The separation is asserted on the SAME BYTES
    # in two places, so neither rule can be mistaken for the other.
    heading = "## [26.900] - 2026-09-16\n"
    expect("citation: the changelog's own heading is the record; the same bytes on a page are not",
           judge("26.901", scan({"CHANGELOG.md": heading}, rd({"CHANGELOG.md": heading})),
                 curated=hist)[0] == []
           and cite(heading) == [("docs-site/src/content/docs/how-to/x.md", 1, "26.900")])
    # A record that cites a section keeps its own allowance too, so the two never compete.
    expect("citation: a launch record citing a section is still a record -> green",
           judge("26.901", scan({"docs/launch/fixture-post.md": "see `CHANGELOG.md` `[26.800]`"},
                                 rd({"docs/launch/fixture-post.md": "see `CHANGELOG.md` `[26.800]`"})),
                 curated=hist)[0] == [])
    # 9b · THE EXCLUDED EMITTED-URL RECORD (added 2026-09-23). docs/emitted-urls-hub-record.txt
    #      declares URLs that only curated-out files emit, among them the 26.800 release page a
    #      posted launch record links. A past release there is a record, and only while the export
    #      really drops that exact file: without the curation, from any other path, above the canon,
    #      or re-published by DOCS_KEEP, the same bytes are red.
    rec_path = "docs/emitted-urls-hub-record.txt"
    rec_line = "https://github.com/olivaresai/olivares/releases/tag/26.800 200 2026-09-23"
    rec = lambda path, text=rec_line, cur=frozenset({rec_path}), kept=frozenset(): judge(
        "26.900", scan({path: text}, rd({path: text})), curated=set(cur), kept=set(kept))[0]
    expect("excluded record: a past release page in the exact record the export drops -> green",
           rec(rec_path) == [])
    expect("excluded record: WITHOUT its export exclusion the same line is red",
           rec(rec_path, cur=frozenset()) == [(rec_path, 1, "26.800")])
    expect("excluded record: re-published by DOCS_KEEP it earns nothing -> red",
           rec(rec_path, kept=frozenset({rec_path})) == [(rec_path, 1, "26.800")])
    expect("excluded record: a sibling path is not the record -> red (exact file)",
           rec("docs/emitted-urls-hub-record-old.txt", cur=frozenset({rec_path, "docs/emitted-urls-hub-record-old"}))
           == [("docs/emitted-urls-hub-record-old.txt", 1, "26.800")])
    expect("excluded record: a release ABOVE the canon there -> red (nobody published it)",
           rec(rec_path, text="https://github.com/olivaresai/olivares/releases/tag/26.1000 404 2026-09-23 x")
           == [(rec_path, 1, "26.1000")])
    # 9c · THE PUBLISHED BASELINE AND THE PENDING RELEASE (added 2026-09-23). The canon is the
    #      published install baseline and the next release is pending. The canon's same-month patch
    #      is not an example anyone may write: it names a release nobody cut. The pending release is
    #      written in its two-part form, which is no version token; its tag form is judged like any
    #      other version (there is no pending-target allowance); and past releases keep their records.
    live = lambda path, text: judge("26.900", scan({path: text}, rd({path: text})), curated={"docs/launch"})[0]
    for path in ("INSTALL.md", "docs-site/src/content/docs/how-to/docker-deployment.md",
                 "docs/UPGRADE-AND-ROLLBACK.md", "docs/PSIRT-RUNBOOK.md"):
        expect(f"stale: the canon's next patch in {path} -> red (it names a release nobody cut)",
               live(path, "a same-month fix is 26.901") == [(path, 1, "26.901")])
    expect("stale: a current-install pin one patch above the canon -> red",
           live("INSTALL.md", "docker pull docker.io/olivaresai/olivares:26.901")
           == [("INSTALL.md", 1, "26.901")])
    expect("a future monthly release is a version token and is refused on a live surface",
           judge("26.1000", scan({"INSTALL.md": "the next release is 26.1100, pending"},
                rd({"INSTALL.md": "the next release is 26.1100, pending"})))[0] == [("INSTALL.md", 1, "26.1100")])
    expect("pending: its tag form on a live surface is judged like any version -> red (no pending-target allowance)",
           live("INSTALL.md", "the next tag is 26.1000") == [("INSTALL.md", 1, "26.1000")])
    base2 = ("# Changelog\n\nthe latest published release is <!-- release -->`26.900`<!-- /release -->.\n\n## [Unreleased]\n\n"
             "pending for YY.M\n\n## [26.900] - 2026-09-16\n\nTag `26.900`\n\n"
             "## [26.800] - 2026-09-01\n\nTag `26.800`\n")
    expect("historical: the dated sections survive under the published baseline, and the masthead states it",
           live("CHANGELOG.md", base2) == [])
    expect("historical: a masthead naming a past release as the current one -> red",
           live("CHANGELOG.md", base2.replace("<!-- release -->`26.900`<!-- /release -->.", "`26.800`.", 1))
           == [("CHANGELOG.md", 3, "26.800")])
    expect("historical: the dated witness of the previous release survives",
           live("docs/releases/26.800-install-surfaces.json", '  "version": "26.800",') == [])
    expect("historical: a witness labelled with a release above the canon -> red",
           live("docs/releases/26.901-install-surfaces.json", '  "version": "26.901",')
           == [("docs/releases/26.901-install-surfaces.json", 1, "26.901")])
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
           and witness_version("docs/releases/26.800-install-surfaces.json") == "26.800"
           and witness_version("docs/releases/NOTES.md") is None
           and witness_version("docs/releases/26.800.md") is None)
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
            fh.write("26.800\n")
        with open(os.path.join(good, "top.md"), "w") as fh:
            fh.write("26.800\n")
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
            fh.write("26.700\n")
        sealed = os.path.join(good, "sealed.md")
        with open(sealed, "w") as fh:
            fh.write("26.700\n")
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
            fh.write(b"26.700\0binary")
        expect("read_surface: a binary is '' by content and a text is its text (control)",
               read_surface(os.path.join(good, "blob.md")) == "" and read_surface(os.path.join(good, "top.md")) == "26.800\n"
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
    # finding 1-TOP (independent review, 2026-09-06): the reviewed staging is README.md as a link whose
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
            fh.write("ships 26.700\n")
        with open(os.path.join(troot, "CHANGELOG.md"), "w") as fh:
            fh.write("26.800\n")
        with open(os.path.join(troot, "deploy", "x.yaml"), "w") as fh:
            fh.write("image: olivaresai/olivares:26.800\n")
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
    # operator/ shipped 26.700 under a 26.800 canon. Each case below is the witness for
    # one rule; none of them can pass by scanning prose.
    tree = {"deploy/helm/olivares/Chart.yaml": 'appVersion: "26.700"'}
    expect("stale chart appVersion -> red (the default helm-install image tag)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [("deploy/helm/olivares/Chart.yaml", 1, "26.700")])
    tree = {"deploy/helm/olivares/Chart.yaml": '# release\nappVersion: "26.800"\n# /release\n'}
    expect("chart appVersion at canon in a release mark -> green", judge_artifacts("26.800", scan(tree, rd(tree))) == [])
    # A floor is not a claim about what ships: it must NOT be swept to the canon, but it
    # may never EXCEED it — you cannot require an engine newer than the one you ship.
    tree = {"operator/README.md": "must serve /pod-readyz (olivares >= 26.700) and clients"}
    expect("compatibility floor below canon -> green (a floor is not a shipped coordinate)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [])
    tree = {"operator/README.md": "must serve /pod-readyz (olivares >= 26.900) and clients"}
    expect("compatibility floor ABOVE canon -> red (requires an engine we do not ship)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [("operator/README.md", 1, "26.900")])
    tree = {"operator/README.md": "the image we publish is olivares:26.700"}
    expect("min-version path, token OFF the bound line -> red (narrower allowance, not a waiver)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [("operator/README.md", 1, "26.700")])
    tree = {"packaging/docker/dockerhub-overview.md": "| <!-- release -->`26.800-fips`<!-- /release --> | FIPS build |"}
    expect("fips variant at canon on the Docker Hub page -> green",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [])
    tree = {"packaging/docker/dockerhub-overview.md": "| `26.700-fips` | FIPS build |"}
    expect("STALE fips variant on the Docker Hub page -> red (a tag the release never pushes)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [("packaging/docker/dockerhub-overview.md", 1, "26.700-fips")])
    tree = {"deploy/manifests/install.yaml": "image: docker.io/olivaresai/olivares:26.800-fips"}
    expect("fips variant OUTSIDE its documented page -> red",
           len(judge_artifacts("26.800", scan(tree, rd(tree)))) == 1)
    # ── the floor whose path is DISCOVERED, not spelled (CRD_TYPES_GLOB) ──
    # These witnesses are derived for the same reason the record is: a literal here would
    # put the pre-rename product word back into a script that ships. They fail loudly if the
    # discovery stops resolving — the first on the count, the second because a path no
    # allowance covers turns a legitimate floor red.
    # Exercise discovery against an actual CRD-shaped staged tree, including in
    # Community where the Business operator sources are intentionally absent.
    crd_stage = tempfile.mkdtemp(prefix="crv-crd-", dir=os.environ.get("TMPDIR") or None)
    crd_here = os.getcwd()
    crd_dir = os.path.join(crd_stage, "operator", "api", "v1alpha1")
    os.makedirs(crd_dir)
    with open(os.path.join(crd_dir, "fixture_types.go"), "w") as fh:
        fh.write("package v1alpha1\n")
    os.chdir(crd_stage)
    crds = crd_types_files()
    os.chdir(crd_here)
    crd_allow = list(ARTIFACT_ALLOW)
    ARTIFACT_ALLOW.extend((p, "min-version", "staged CRD floor") for p in crds)
    expect("CRD API type declaration discovered (the floor scope is derived, not spelled)",
           len(crds) >= 1)
    crd = crds[0] if crds else os.path.join("operator", "api", "v1alpha1", "undiscovered_types.go")
    tree = {crd: "//     role label (olivares >= 26.700). With an older image every pod fails"}
    expect("CRD-types floor below canon -> green (discovery preserves the exemption)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [])
    tree = {crd: "//     role label (olivares >= 26.900). With an older image every pod fails"}
    expect("CRD-types floor ABOVE canon -> red (requires an engine we do not ship)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [(crd, 1, "26.900")])
    tree = {crd: "//     the image we publish is olivares:26.700"}
    expect("CRD-types, token OFF the bound line -> red (narrower allowance, not a waiver)",
           judge_artifacts("26.800", scan(tree, rd(tree))) == [(crd, 1, "26.700")])
    tree = {"operator/internal/controller/reconcile.go": "// needs olivares >= 26.700 to start"}
    expect("floor in an operator file OUTSIDE the discovered scope -> red (scope did not widen)",
           judge_artifacts("26.800", scan(tree, rd(tree)))
           == [("operator/internal/controller/reconcile.go", 1, "26.700")])
    ARTIFACT_ALLOW[:] = crd_allow
    shutil.rmtree(crd_stage)
    # The default must select this release even when an older :latest is cached.
    compose_canon = read_canon(read_surface("RELEASE-VERSION"))
    expected_image = "    image: ${OLIVARES_IMAGE:-docker.io/olivaresai/olivares:" + compose_canon.lstrip("v") + "}"
    for path in COMPOSE_FILES:
        images = [line for line in read_surface(path).splitlines() if line.lstrip().startswith("image:")]
        expect(f"{path} defaults to RELEASE-VERSION", images == [expected_image])
    expect("the example environment inherits the release image default",
           "OLIVARES_IMAGE=" in read_surface("deploy/compose/.env.example").splitlines())
    for tag in ("latest", "26.1000", "26.1001"):
        text = "    image: ${OLIVARES_IMAGE:-docker.io/olivaresai/olivares:" + tag + "}\n"
        stamped = stamp_compose("26.1001", text)
        expect(f"Compose stamps {tag} from the canon and preserves the override",
               stamped == "    image: ${OLIVARES_IMAGE:-docker.io/olivaresai/olivares:26.1001}\n")
        expect(f"Compose detects {tag} drift", (text != stamped) == (tag != "26.1001"))
    expect("Compose refuses a missing image instead of silently stamping nothing",
           refusal(lambda: stamp_compose("26.1001", "services: {}\n"))[0] == 1)
    expect("Compose refuses duplicate image defaults",
           refusal(lambda: stamp_compose("26.1001", text + text))[0] == 1)
    # Exercise the exported script, which must work without the source tree's profile.
    with tempfile.TemporaryDirectory() as fixture:
        os.makedirs(os.path.join(fixture, "scripts/lib"))
        os.makedirs(os.path.join(fixture, "deploy/compose"))
        checker = os.path.join(fixture, "scripts/check-release-version.sh")
        shutil.copyfile("scripts/check-release-version.sh", checker)
        shutil.copyfile(WORDS_FILE, os.path.join(fixture, WORDS_FILE))
        with open(os.path.join(fixture, "RELEASE-VERSION"), "w") as output:
            output.write("26.1001\n")
        for path in COMPOSE_FILES:
            with open(os.path.join(fixture, path), "w") as output:
                output.write(text.replace("26.1001", "latest"))
        result = subprocess.run(["sh", checker], capture_output=True, text=True)
        expect("release check refuses a floating Compose default before scanning",
               result.returncode == 1 and "image differs from RELEASE-VERSION" in result.stderr)
        result = subprocess.run(["sh", checker, "--stamp-compose"], capture_output=True, text=True)
        expect("export stamping derives both image defaults from RELEASE-VERSION",
               result.returncode == 0 and all(
                   read_surface(os.path.join(fixture, path)) == text for path in COMPOSE_FILES))
        with open(os.path.join(fixture, WORDS_FILE), "w", encoding="utf-8") as output:
            output.write("".join(f"{name}\t{'in' if name == 'revision' else value}\n" for name, value in WORDS.items()))
        result = subprocess.run(["sh", checker], capture_output=True, text=True)
        expect("words that open a row to any literal refuse the run before any surface is read (exit 2)",
               result.returncode == 2 and "neutral window" in result.stderr and "a document's own revision" in result.stderr)
        os.remove(os.path.join(fixture, WORDS_FILE))
        result = subprocess.run(["sh", checker], capture_output=True, text=True)
        expect("no words file refuses the run (exit 2)", result.returncode == 2 and "cannot be read" in result.stderr)

    # ── ONE REFUSAL CORPUS, EVERY CALLER OF THE MATCHER (2026-10-08) ──
    # Measured 2026-10-08: the partial matchers read `1.0-rc.1` and `1.0+meta` as the canon and
    # never saw `1.0.1.2`. Each spelling below goes through a release mark, the artifact judge,
    # the pin check, the fixed-version and floor judges and the history judge, and each is refused
    # as its COMPLETE token. The stamp never repairs a non-release spelling into a valid one.
    corpus = ("1.0-rc.1", "1.0+meta", "1.0.1", "1.0.1.2", "v2.0", "26.11.0", "99.1.0",
              "1.0_rc1", "1.0~rc1", "1.0rc1", "1.0.rc1", "1.0.beta", "1.0.1rc1", "V2.0",
              "1.0%2Bmeta", "1.0‑rc.1", "1.0​-rc.1", "1.0--rc.1")
    for bad in corpus:
        tree = {"README.md": f"Olivares <!-- release -->{bad}<!-- /release --> ships today\n"}
        expect(f"corpus, release mark: {bad} -> red as the whole token",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("README.md", 1, bad)])
        tree = {"README.md": f"Olivares {bad} ships today\n"}
        expect(f"corpus, unmarked prose: {bad} -> red, above the canon too",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("README.md", 1, bad)])
        chart = {"deploy/helm/olivares/Chart.yaml": f'# release\nappVersion: "{bad}"\n# /release\n'}
        expect(f"corpus, artifact judge: appVersion {bad} -> red",
               judge_artifacts("1.0", scan(chart, rd(chart))) == [("deploy/helm/olivares/Chart.yaml", 2, bad)])
        for line in (f"  image: docker.io/olivaresai/olivares:{bad}", f"curl -O https://dl/olivares-{bad}.tar.gz"):
            expect(f"corpus, pin check: {line.split()[-1]} -> red",
                   judge_pins("1.0", [Hit("operator/config/samples/x.yaml", 1, m.group(), line, False, False, m.start())
                                      for m in pin_tokens(line)], curated=set(), kept=set())
                   == [("operator/config/samples/x.yaml", 1, bad)])
        tree = {"README.md": f"Olivares {bad}<!-- release-fixed --> was cut\n"}
        expect(f"corpus, fixed-version judge: {bad}<!-- release-fixed --> -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("README.md", 1, bad)])
        tree = {"docs/RELEASE-NOTES.md": f"It was cut as {bad}<!-- release-fixed -->.\n"}
        expect(f"corpus, an annotation alone puts {bad} in the census -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
               == [("docs/RELEASE-NOTES.md", 1, bad)])
        tree = {"README.md": f"docker run docker.io/olivaresai/olivares:{bad}\n"}
        expect(f"corpus, README image: olivaresai/olivares:{bad} -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
               == [("README.md", 1, f"docker.io/olivaresai/olivares:{bad}")])
        line = f"first_contract_version='{bad}~'"
        expect(f"corpus, contract floor: {line} -> red",
               not record_allowed("1.0", "packaging/nfpm/postinstall.sh", bad, line, False, set())
               and judge_artifacts("1.0", scan({"packaging/nfpm/postinstall.sh": line},
                                               rd({"packaging/nfpm/postinstall.sh": line})))
               == [("packaging/nfpm/postinstall.sh", 1, bad)])
        tree = {"operator/README.md": f"(olivares >= {bad})"}
        expect(f"corpus, floor judge: (olivares >= {bad}) on a min-version record -> red",
               judge_artifacts("1.0", scan(tree, rd(tree))) == [("operator/README.md", 1, bad)])
        tree = {"CHANGELOG.md": f"# Changelog\n\n## [26.10.1] - 2026-10-04\nthe record of olivares {bad}\n"}
        expect(f"corpus, history judge: {bad} in a dated changelog section -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("CHANGELOG.md", 4, bad)])
        tree = {"CHANGELOG.md": f"# Changelog\n\n## [{bad}] - 2026-10-04\n"}
        expect(f"corpus, a changelog section heading names a release whatever its value: [{bad}] -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("CHANGELOG.md", 3, bad)])
        expect(f"corpus, stamp: {bad} is left for the checker to refuse, never repaired",
               restamped(bad, "1.1") is None and restamped(f"olivaresai/olivares:{bad}", "1.1") is None)
    for value in ("26.12.0-rc.1", "26.12.0+meta", "26.12.0.1", "1.0.1", "v1.1"):
        line = f"first_contract_version='{value}~'"
        expect(f"the upgrade contract takes only a bare MAJOR.MINOR: {line} -> red",
               not record_allowed("1.0", "packaging/nfpm/postinstall.sh", value, line, False, set()))
    expect("the upgrade contract above the canon on its assignment -> green",
           record_allowed("1.0", "packaging/nfpm/postinstall.sh", "1.1", "first_contract_version='1.1~'", False, set()))
    expect("stamp: a release, its FORMS and a retired tag take the canon",
           [restamped(t, "1.1") for t in ("1.0", "1.0-amd64", "1.0-1", "26.10.1", "v26.9.0-fips")]
           == ["1.1", "1.1-amd64", "1.1-1", "1.1", "1.1-fips"])
    # ── EVERY WORDING IS JUDGED BY ITS MARK (2026-10-08) ──
    # Each row is a wording the former context table named. A live one is refused outside a mark
    # and green inside one; a record (a floor, the upgrade contract, a dated heading, a citation, a
    # witness) is green unmarked; a patch is refused in every one of them. `{o}` and `{c}` are where
    # a live row's mark opens and closes.
    md_mark, line_mark = ("<!-- release -->", "<!-- /release -->"), ("# release\n", "\n# /release")
    context_rows = (
        ("INSTALL.md", "Pin: {o}`curl -fsSL https://olivares.ai/olivares/install.sh | sh -s -- --version {v}`{c}.\n", 1, ""),
        ("docs-site/src/content/docs/how-to/air-gap-install.md",
         "scripts/airgap-bundle.sh \\\n  --version {o}{v}{c} \\\n  --chart deploy/helm/olivares\n", 2, ""),
        ("docs/UPGRADE-AND-ROLLBACK.md",
         "scripts/export-update-bundle.sh --dir <release-dir> --channel stable --version {o}{v}{c} \\\n", 1, ""),
        ("docs/RELEASE-VERIFICATION.md", "scripts/verify-release.sh --source-tag {o}{v}{c}\n", 1, ""),
        ("docs/RELEASE-VERIFICATION.md", "scripts/airgap-mirror.sh --bundle olivares-airgap-{o}{v}{c}.tar.gz\n", 1, ""),
        ("docs/RELEASE-VERIFICATION.md", "git checkout {o}{v}{c}\n", 1, ""),
        ("docs/RELEASE-VERIFICATION.md",
         "olivares release verify-manifest --manifest m.json \\\n  --checksums c.txt --expect-version {o}{v}{c}\n", 2, ""),
        ("docs/UPGRADE-AND-ROLLBACK.md", "olivares upgrade --target /opt/olivares/olivares --current-version {o}{v}{c}\n", 1, ""),
        ("docs/PSIRT-RUNBOOK.md", "olivares security check --feed advisories.json --product-version {o}{v}{c}\n", 1, ""),
        ("README.md", "Badge: {o}[![Next release: 1.0](https://img.shields.io/badge/release-1.0-28282B)]"
                      "(https://github.com/olivaresai/olivares/releases/tag/{v}){c}\n", 1, ""),
        ("INSTALL.md", "Tags: {o}`:{v}`{c} pins a release.\n", 1, ""),
        ("docs-site/src/content/docs/how-to/docker-deployment.md", 'DIGEST="$(crane digest "$IMAGE:{o}{v}{c}")"\n', 1, ""),
        ("docs-site/src/content/docs/how-to/air-gap-install.md",
         "({o}`docs/releases/{v}-install-surfaces.json`{c})\n", 1, "-install-surfaces"),
        ("deploy/helm/README.md", "as of the Olivares {o}{v}{c} engine release.\n", 1, ""),
        ("docs/PSIRT-RUNBOOK.md", "olivares release manifest --channel stable --version {o}{v}{c} --dir ./dist\n", 1, ""),
        ("docs/RELEASE-VERIFICATION.md", "#   scripts/airgap-bundle.sh \\\n#     --version {o}{v}{c} \\\n", 2, ""),
        ("docs/RELEASE-VERIFICATION.md",
         "scripts/install.sh --version {o}{v}{c} && helm upgrade x oci://ghcr.io/olivaresai/charts/olivares --version 0.2.4\n",
         1, ""),
        ("INSTALL.md", "scripts/install.sh --version={o}{v}{c}\n", 1, ""),
        ("docs/UPGRADE-AND-ROLLBACK.md", "olivares release manifest --channel security --min-version {o}{v}{c}\n", 1, ""),
        ("docs/UPGRADE-AND-ROLLBACK.md", "Requires Olivares >= {o}{v}{c} to upgrade.\n", 1, ""),
        ("INSTALL.md", "To install {o}{v}{c}, run it.\n", 1, ""),
        ("packaging/nfpm/postinstall.sh", "{o}ver={v}{c}\n", 1, ""),
        ("packaging/docker/dockerhub-overview.md", "| {o}`{v}`{c} | the release |\n", 1, ""),
        ("INSTALL.md", 'scripts/install.sh --version "{o}{v}{c}"\n', 1, ""),
        ("INSTALL.md", "Run {o}sh olivares-install-1.0.sh --version {v}{c}\n", 1, ""),
        ("docs/RELEASE-CUT.md", "git push origin {o}{v}{c}\n", 1, ""),
        ("docs/RELEASE-CUT.md", "gh release download {o}{v}{c} --dir ota\n", 1, ""),
        ("docs/RELEASE-CUT.md", "gh workflow run release.yml --ref main -f version={o}{v}{c} -f channel=stable\n", 1, ""),
        ("docs/RELEASE-CUT.md", "--certificate-identity "
                                "https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/{o}{v}{c}\n",
         1, ""),
        ("docs/RELEASE-CUT.md", "https://github.com/OlivaresAI/olivares/releases/download/{o}{v}{c}/checksums.txt\n", 1, ""),
        ("packaging/docker/dockerhub-overview.md", "| `latest`, {o}`{v}`{c} | the release |\n", 1, ""),
        # Wordings no context row named (planted 2026-10-09).
        ("INSTALL.md", "Olivares version {o}{v}{c} is the current release.\n", 1, ""),
        ("INSTALL.md", "Set {o}export OLIVARES_VERSION={v}{c}\n", 1, ""),
    ) + tuple((f"README.{lang}.md", word("next-release-" + lang) + " {o}`{v}`{c}.\n", 1, "")
              for lang in ("de", "es", "fr", "ja", "zh", "ru"))
    for path, text, number, suffix in context_rows:
        opening, closing = line_mark if path.endswith(".sh") else md_mark
        plain = lambda v: text.format(v=v, o="", c="")
        marked = lambda v: text.format(v=v, o=opening, c=closing)
        shown = plain("1.0.1").splitlines()[number - 1].strip()
        expect(f"any wording refuses a patch: {path}: {shown}",
               (path, number, "1.0.1" + suffix) in judge("1.0", scan({path: plain("1.0.1")}, rd({path: plain("1.0.1")})),
                                                       curated=set(), kept=set())[0])
        expect(f"any wording refuses the canon outside a mark: {path}:{number}",
               (path, number, "1.0" + suffix) in judge("1.0", scan({path: plain("1.0")}, rd({path: plain("1.0")})),
                                                     curated=set(), kept=set())[0])
        expect(f"any wording judges the marked canon green: {path}:{number}",
               judge("1.0", scan({path: marked("1.0")}, rd({path: marked("1.0")})), curated=set(), kept=set())[0] == [])
    record_rows = (
        ("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [{v}] - 2026-10-08\n", 5),
        ("docs/HA-LEADER-ROUTING.md", "Roll `spec.image` to ≥ {v} first.\n", 1),
        ("docs/PSIRT-RUNBOOK.md", '"ranges": [ {{ "introduced": "{v}", "fixed": "<fix-version>" }} ]\n', 1),
        ("packaging/nfpm/postinstall.sh", "first_contract_version='{v}~'\n", 1),
        ("docs/UPGRADE-AND-ROLLBACK.md", "See CHANGELOG.md [{v}] here.\n", 1),
        ("docs/releases/1.0-install-surfaces.json", '  "version": "{v}",\n', 1),
        ("docs/releases/1.0-install-surfaces.json",
         '  "request": "GET https://ghcr.io/v2/olivaresai/olivares/manifests/{v}",\n', 1),
        ("docs/UPGRADE-AND-ROLLBACK.md", "Upgrading from {v}<!-- release-fixed --> keeps its release.\n", 1),
    )
    for path, text, number in record_rows:
        good, bad = {path: text.format(v="1.0")}, {path: text.format(v="1.0.1")}
        expect(f"a record names the canon unmarked: {path}: {text.format(v='1.0').splitlines()[number - 1].strip()}",
               judge("1.0", scan(good, rd(good)), curated=set(), kept=set())[0] == [])
        expect(f"a record refuses a patch: {path}:{number}",
               judge("1.0", scan(bad, rd(bad)), curated=set(), kept=set())[0] == [(path, number, "1.0.1")])
    # What a literal versions when it is not a release of ours is named once (NOT_RELEASES).
    for path, text in (("docs-site/src/content/docs/how-to/self-hosting.md",
                        "helm upgrade --install olivares oci://ghcr.io/olivaresai/charts/olivares --version 0.2.4\n"),
                       ("INSTALL.md", "OAuth 1.0 and TLS 1.2 stay protocols; Go 1.26.8 builds it.\n"),
                       ("docs-site/src/content/docs/how-to/tools.md",
                        "olivares agent tool install --driver codex --version 0.153.4 --yes\n"),
                       ("docs-site/src/content/docs/how-to/connectors.md",
                        "https://github.com/example/dep/releases/tag/1.0.1\n"),
                       ("docs/x.md", "listen on 127.0.0.1:8443 or 10.0.0.0/8; 1.5 GB; 99.9%; §1.2; AGPL-3.0-only\n"),
                       ("docs/x.md", "Releases before 1.0 used CalVer; pre-1.0 builds, from `1.0` on.\n")):
        tree = {path: text}
        expect(f"named, not a release of ours: {path}: {text.strip()}",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [])
    for text in ("listen on 1.0.1.2", "Olivares version 1.0.1", "release 1.0.1.2 ships", "Version 1.0/GA is out.",
                 "olivares agent tool install --driver codex --version 2.0.14 --yes"):
        tree = {"docs/x.md": text + "\n"}
        expect(f"no row hides a release: {text}", judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] != [])
    # A section number is one where the document numbers the parent section too.
    for text, refused in (("## 1. Model\n\n### 1.1 Scope\n\n### 1.1bis More\n\n#### 1.1.2 Detail\n", []),
                          ("### 1.1 Scope\n", [("docs/x.md", 1, "1.1")]),
                          ("## 1. Intro\n\n## 1.0.1 Release notes\n", [("docs/x.md", 3, "1.0.1")]),
                          ("## 1. Intro\n\n### 1.0 Release notes\n", [("docs/x.md", 3, "1.0")]),
                          ("## 1. Intro\n\n### 1.2 Scope\n\n#### 1.2.0 Release\n", [("docs/x.md", 5, "1.2.0")]),
                          ("## 1. Intro\n\nsee 1.1 below\n", [("docs/x.md", 3, "1.1")])):
        tree = {"docs/x.md": text}
        expect(f"a numbered heading is a section only under its numbered parent: {text!r}",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == refused)
    tree = {"docs/x.yaml": "# 1.\n# 1.1 Scope\n"}
    expect("a numbered heading is Markdown only", judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
           == [("docs/x.yaml", 2, "1.1")])
    tree = {"INSTALL.md": "sh olivares-install-v1.0.1.sh --user; then install v2.1 of the plugin.\n"}
    expect("a malformed release in a file name is refused, and so is a v2.1 no row names",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
           == [("INSTALL.md", 1, "v1.0.1"), ("INSTALL.md", 1, "v2.1")])
    tree = {"README.md": "see docs/releases/1.0-install-surfaces.txt\n"}
    expect("the witness suffix states a release only on its .json file name -> red",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
           == [("README.md", 1, "1.0-install-surfaces")])
    # ── THE WORDS OF LOCALIZED DOCUMENTS (WORDS_FILE) ──
    for name, data, why in (("a missing file", FileNotFoundError(2, "absent"), "cannot be read"),
                            ("bytes that are not UTF-8", b"revision\tRevisi\xf3n\n", "cannot be read"),
                            ("a line without a tab", "revision Revision\n", "is not a new name<TAB>value"),
                            ("a repeated name", "revision\tRev\nrevision\tRevision\n", "is not a new name<TAB>value"),
                            ("a third field", "revision\tRev\tRevision\n", "is not a new name<TAB>value"),
                            ("a value that does not compile", "revision\t(Rev\n", "is not a new name<TAB>value"),
                            ("an empty alternative", "boundary-before\tbefore||since\n", "is not a new name<TAB>value"),
                            ("an empty value", "revision\t\n", "is not a new name<TAB>value"),
                            ("a value that escapes its group", "revision\ta)|(b\n", "is not a new name<TAB>value"),
                            ("a value of any character", "boundary-after\t.\n", "is not a new name<TAB>value"),
                            ("a value that matches nothing", "boundary-after\t\\b\n", "is not a new name<TAB>value"),
                            ("a value with an edge blank", "revision\tRevision \n", "is not a new name<TAB>value"),
                            ("a name after a byte-order mark", "\ufeffrevision\tRev\n", "is not a new name<TAB>value"),
                            ("an empty name", "\tRev\n", "is not a new name<TAB>value")):
        read = (lambda _, e=data: (_ for _ in ()).throw(e)) if isinstance(data, Exception) else (lambda _, d=data: d)
        code, msg = refusal(lambda: read_words(read=read))
        expect(f"words: {name} refuses the file whole (exit 2)", code == 2 and why in msg)
    code, msg = refusal(lambda: word("no-such-name"))
    expect("words: a name the file lacks refuses (exit 2)", code == 2 and "names no 'no-such-name'" in msg)
    expect("words: a line separator inside a comment makes no entry", read_words(read=lambda _: "# n\u2028fake\tx\n") == {})
    # ── THE ROWS OF THE DOCUMENTS THE EXPORT REMOVES (PRIVATE_ROWS) ──
    removed = {"docs/contracts"}
    good_row = "an A2A version\tdocs/contracts/S1.md\t\\x00v1\\.0\\.1\\x01\n"
    expect("private rows: no file is no rows (a public tree has neither the file nor its documents)",
           private_rows(read=lambda _: (_ for _ in ()).throw(FileNotFoundError(2, "absent"))) == [])
    for name, text, why in (("a line without three fields", "an A2A version\tdocs/contracts/S1.md\n", "is not"),
                            ("a pattern that does not compile", "x\tdocs/contracts/S1.md\t(\\x00\n", "missing"),
                            ("a row naming a published document", "x\tdocs/RELEASE-VERIFICATION.md\t\\x00\n",
                             "publishes"),
                            ("a glob over a document the export keeps", "x\tdocs/contracts/S*.md\t\\x00\n", "publishes"),
                            ("a pattern that names no position of the literal", "x\tdocs/contracts/S1.md\tspec\n",
                             "no position"),
                            ("a pattern that matches an empty window", "x\tdocs/contracts/S1.md\t(?:\\x00)?\n", "empty"),
                            ("a pattern that names the literal alone", "x\tdocs/contracts/S1.md\t\\x00\n", "neutral"),
                            ("a pattern that names any literal", "x\tdocs/contracts/S1.md\tx|\\x00\n", "neutral"),
                            ("a pattern too large to compile", "x\tdocs/contracts/S1.md\t\\x00a{4294967296}\n",
                             "does not compile")):
        held = curated_out, curation_kept
        try:
            globals()["curated_out"] = lambda read=None: removed
            globals()["curation_kept"] = lambda read=None: {"docs/contracts/S02-kept.md"}
            code, msg = refusal(lambda: private_rows(read=lambda _: text))
        finally:
            globals()["curated_out"], globals()["curation_kept"] = held
        expect(f"private rows: {name} refuses the file whole (exit 2)", code == 2 and why in msg)
    held = curated_out
    try:
        globals()["curated_out"] = lambda read=None: removed
        rows = private_rows(read=lambda _: "# comment\n" + good_row)
    finally:
        globals()["curated_out"] = held
    expect("no public row admits a literal by its shape alone (a neutral window)",
           [name for name, _, found in _NOT_RELEASES if any(found.search(w) for w in NEUTRAL_WINDOWS)] == [])
    expect("private rows: a valid row names its documents and compiles", [(n, p) for n, p, _ in rows]
           == [("an A2A version", ("docs/contracts/S1.md",))])
    key = os.getcwd()
    saved = _private.get(key)
    _private[key] = rows
    _ROWS_AT.clear()
    try:
        for path, text, refused in (("docs/contracts/S1.md", "pin v1.0.1 of the spec\n", []),
                                    ("docs/contracts/S2.md", "pin v1.0.1 of the spec\n", [("docs/contracts/S2.md", 1, "v1.0.1")]),
                                    ("docs/contracts/S1.md", "pin <!-- release -->v1.0.1<!-- /release -->\n",
                                     [("docs/contracts/S1.md", 1, "v1.0.1")])):
            expect(f"private rows apply to their own documents, never inside a mark: {path}: {text.strip()}",
                   judge("1.0", scan({path: text}, rd({path: text})), curated=set(), kept=set())[0] == refused)
    finally:
        if saved is None:
            _private.pop(key, None)
        else:
            _private[key] = saved
        _ROWS_AT.clear()
    # ── MARKS ARE DATA: malformed ones stop the run, and every form is one shape ──
    for path, text, why in (("docs/x.md", "x <!-- release -->1.0\n", "never closes"),
                            ("docs/x.md", "1.0<!-- /release -->\n", "closes without opening"),
                            ("docs/x.md", "x <!-- release --><!-- release -->1.0<!-- /release --><!-- /release -->\n",
                             "opens inside another"),
                            ("docs/x.md", "x <!-- release -->the next release<!-- /release -->\n", "holds no version"),
                            ("docs/x.md", "x <!-- release -->127.0.0.1<!-- /release -->\n", "holds no version"),
                            ("docs/x.mdx", "{/* release */}1.0\n", "never closes"),
                            ("docs/x.md", "x <!-- release -->1.0<!-- /release -->.1\n", "cuts a version"),
                            ("docs/x.md", "x v<!-- release -->1.0<!-- /release -->\n", "cuts a version"),
                            ("docs/x.md", "x <!-- release -->1.0<!-- /release -->-rc1\n", "cuts a version"),
                            ("docs/x.md", "<!-- release -->1.0<!-- /release --> ships\n", "starts a line"),
                            ("docs/x.md", "- <!-- release -->1.0<!-- /release --> ships\n", "starts a line"),
                            ("docs/x.md", "> x\n> <!-- /release -->1.0\n", "starts a line"),
                            ("deploy/x.yaml", "# release\nimage: x\n", "never closes")):
        code, _ = refusal(lambda: scan({path: text}, rd({path: text})))
        expect(f"a malformed release mark stops the run with a FAIL line (rc 1): {why}",
               isinstance(code, str) and code.startswith("FAIL") and why in code)
    for path, text in (("docs/x.md", "x <!--release-->1.0<!--/release-->\n"),
                       ("docs/x.mdx", "{/* release */}`1.0`{/* /release */}\n"),
                       ("deploy/x.yaml", "  # release\n  image: docker.io/olivaresai/olivares:1.0\n  # /release\n"),
                       ("deploy/x.yaml", "# release\r\nimage: docker.io/olivaresai/olivares:1.0\r\n# /release\r\n"),
                       ("docs/x.md", "<!-- release -->\n```sh\nolivares 1.0\n```\n<!-- /release -->\n"),
                       ("scripts/x.go", "\t// release\n\tconst v = \"olivares-1.0-1.x86_64.rpm\"\n\t// /release\n"),
                       ("packaging/aur/olivares-bin/.SRCINFO", "\tpkgver = 1.0\n\tlicense = AGPL-3.0-only\n")):
        hits = scan({path: text}, rd({path: text}))
        expect(f"a mark form of its file type marks the release: {path}",
               any(h.marked for h in hits) and judge("1.0", hits, curated=set(), kept=set())[0] == [])
    tree = {"docs/x.md": "# release\nOlivares 1.0\n# /release\n"}
    expect("a comment line is no mark in Markdown, where `# release` is a heading",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("docs/x.md", 2, "1.0")])
    tree = {"docs/x.md": "Olivares 1.0<!-- release-fixed -->, and Olivares 1.0 again\n"}
    expect("a fixed-version annotation binds its own occurrence and no other on the line",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("docs/x.md", 1, "1.0")])
    with warnings.catch_warnings(record=True) as seen:
        warnings.simplefilter("always")
        compile(read_surface("scripts/check-release-version.sh").split("python3 - <<'PY'\n", 1)[1],
                "check-release-version.sh", "exec")
    expect("the checker's own source compiles without a warning (Python 3.12 prints invalid escapes)",
           [str(w.message) for w in seen] == [])
    expect("the stamp moves only a release of ours at or below the canon, or a retired tag",
           [restamped(t, "1.1") for t in ("1.0", "26.10.1", "1.26", "16.4", "0.9", "2.0", "1.2")]
           == ["1.1", "1.1", None, None, "1.1", None, None])
    with tempfile.TemporaryDirectory() as stage:
        held = os.getcwd()
        os.chdir(stage)
        try:
            with open("x.md", "wb") as output:
                output.write(b"\xe9 Olivares <!-- release -->1.0<!-- /release -->\n")
            stamp_surfaces("1.1", scan(["x.md"], read_surface))
            with open("x.md", "rb") as source:
                expect("the stamp keeps a byte that is not UTF-8", source.read()
                       == b"\xe9 Olivares <!-- release -->1.1<!-- /release -->\n")
        finally:
            os.chdir(held)
    expect("a CHANGELOG heading is the section itself, never a wrapped citation",
           not wrapped_citation("## [1.0.1] - 2026-10-08", "1.0.1")
           and wrapped_citation("see [1.0.1] for the record", "1.0.1"))
    # One stamp at canon 1.1 over marked and unmarked positions, on real files: every marked
    # version names 1.1, and nothing outside a mark moves: a dated changelog section, a floor, an
    # annotated history, the contract, a citation and a witness keep the release they record.
    stamp_before = {
        "README.md": "Badge: <!-- release -->[![Next release: 1.0](https://img.shields.io/badge/release-1.0-28282B)]"
                     "(https://github.com/olivaresai/olivares/releases/tag/1.0)<!-- /release -->\n",
        "README.ja.md": word("next-release-ja") + " <!-- release -->`1.0`<!-- /release -->.\n",
        "INSTALL.md": "<!-- release -->\n```sh\ncurl -fsSL https://olivares.ai/olivares/install.sh | sh -s -- --version 1.0\n"
                      "```\n<!-- /release -->\nTags: <!-- release -->`:1.0` and `:1.0-fips`<!-- /release -->.\n"
                      "Olivares version <!-- release -->1.0<!-- /release --> is the current release.\n",
        "docs-site/src/content/docs/how-to/air-gap-install.md":
            "<!-- release -->\nscripts/airgap-bundle.sh \\\n  --version 1.0 \\\n"
            "  --image ghcr.io/olivaresai/olivares:1.0-amd64\n"
            "scripts/airgap-mirror.sh --bundle olivares-airgap-1.0.tar.gz\n<!-- /release -->\n"
            "(<!-- release -->`docs/releases/1.0-install-surfaces.json`<!-- /release -->)\n",
        "docs-site/src/content/docs/tutorials/getting-started/docker-compose.mdx":
            "{/* release */}\n<Aside type=\"caution\" title=\"Olivares 1.0\">\nThe next release is `1.0`.\n</Aside>\n"
            "{/* /release */}\n",
        "docs/RELEASE-VERIFICATION.md": "scripts/verify-release.sh --source-tag <!-- release -->1.0<!-- /release -->\n",
        "docs/UPGRADE-AND-ROLLBACK.md": "Upgrading from 1.0<!-- release-fixed --> keeps its release.\n",
        "docs/HA-LEADER-ROUTING.md": "Roll `spec.image` to ≥ 1.0 first.\n",
        "CHANGELOG.md": "# Changelog\n\nThe next release is <!-- release -->`1.0`<!-- /release -->.\n\n"
                        "## [Unreleased]\n\n## [1.0] - 2026-10-07\n",
        "packaging/nfpm/postinstall.sh": "first_contract_version='1.0~'\n# release\nver=1.0\n# /release\n",
        "docs/api-errors.md": "Recorded under CHANGELOG.md [1.0]; the release <!-- release -->`1.0`<!-- /release --> ships it.\n",
        "docs/releases/1.0-install-surfaces.json": '  "version": "1.0",\n',
        "docs/x.md": "Olivares 1.0 is the current version.\n",
    }
    # Kept: everything outside a mark.
    kept_lines = ("## [1.0] - 2026-10-07", "≥ 1.0 first", "1.0<!-- release-fixed -->",
                  "first_contract_version='1.0~'", '"version": "1.0"', "Olivares 1.0 is")
    stamp_after = {p: "".join(line if any(k in line for k in kept_lines) else line.replace("1.0", "1.1")
                              for line in t.splitlines(keepends=True))
                   for p, t in stamp_before.items()}
    stamp_after["docs/api-errors.md"] = ("Recorded under CHANGELOG.md [1.0]; the release "
                                         "<!-- release -->`1.1`<!-- /release --> ships it.\n")
    back = os.getcwd()
    with tempfile.TemporaryDirectory() as stage:
        os.chdir(stage)
        try:
            for path, text in stamp_before.items():
                os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
                with open(path, "w", encoding="utf-8") as output:
                    output.write(text)
            stamp_surfaces("1.1", scan(sorted(stamp_before), read_surface))
            stamped = {path: read_surface(path) for path in stamp_before}
            for path in stamp_before:
                expect(f"stamp at canon 1.1: {path} names 1.1 inside its marks and nothing else moves",
                       stamped[path] == stamp_after[path])
            expect("stamp at canon 1.1: the stamped surfaces recheck green, except the unmarked live line",
                   judge("1.1", scan(sorted(stamp_before), read_surface), curated=set(), kept=set())[0]
                   == [("docs/x.md", 1, "1.0")])
            stamp_surfaces("1.1", scan(sorted(stamp_before), read_surface))
            expect("stamp at canon 1.1: a second stamp changes nothing",
                   {path: read_surface(path) for path in stamp_before} == stamped)
        finally:
            os.chdir(back)
    for bad in ("26.10.1-rc.1", "26.10.2", "v26.10.0", "26.8.1"):
        tree = {"CHANGELOG.md": f"# Changelog\n\n## [26.10.1] - 2026-10-04\nthe record of olivares {bad}\n"}
        expect(f"retired history is the published tag list: {bad} -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("CHANGELOG.md", 4, bad)])
    # Green controls: the same callers still admit the release, its FORMS and the retired list.
    tree = {"CHANGELOG.md": "# Changelog\n\n## [26.10.1] - 2026-10-04\n"
                            "olivares 26.10.1, v26.9.0, docker.io/olivaresai/olivares:26.9.0-amd64 and v26.8.0\n"}
    expect("retired history: each published tag, with or without its v, in a FORMS spelling -> green",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [])
    for good in ("olivaresai/olivares:1.0", "olivaresai/olivares:1.0-amd64", "olivares-1.0-1.x86_64.rpm"):
        expect(f"FORMS: the pin {good} states the canon -> green",
               judge_pins("1.0", [Hit("operator/x.yaml", 1, m.group(), good, False, True, m.start()) for m in pin_tokens(good)],
                          curated=set(), kept=set()) == [] and len(pin_tokens(good)) == 1)
    for bad, line in (("1.0--rc.1", "Olivares 1.0--rc.1 ships today"),
                      ("1.0--rc.1", "x <!-- release -->[![r](https://img.shields.io/badge/release-1.0--rc.1-28282B)](x)"
                                    "<!-- /release -->")):
        tree = {"README.md": line + "\n"}
        fails = judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
        expect(f"a hyphenated suffix stays in the token: {line} -> red",
               len(fails) == 1 and fails[0][2].startswith(bad))
    # A FORMS suffix outside the context its row names states nothing: a Helm appVersion or an
    # image tag is never a package revision, and prose or a chart is never an image tag.
    for path, line, bad in (("deploy/helm/olivares/Chart.yaml", 'appVersion: "1.0-1"', "1.0-1"),
                            ("deploy/helm/olivares/Chart.yaml", 'appVersion: "1.0-arm64"', "1.0-arm64"),
                            ("deploy/manifests/install.yaml", "image: docker.io/olivaresai/olivares:1.0-1", "1.0-1"),
                            ("packaging/aur/olivares-bin/.SRCINFO", "pkgver = 1.0_rc1", "1.0_rc1"),
                            ("README.md", "the release 1.0-amd64 ships", "1.0-amd64")):
        tree = {path: line + "\n"}
        expect(f"a suffix out of its FORMS context -> red: {line}",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [(path, 1, bad)])
    for good in ("image: docker.io/olivaresai/olivares:1.0-arm64", "image: ghcr.io/olivaresai/olivares:1.0-amd64",
                 "curl -O https://dl/olivares-1.0-1.aarch64.rpm"):
        tree = {"deploy/manifests/install.yaml": "# release\n" + good + "\n# /release\n"}
        expect(f"a suffix in its FORMS context -> green: {good}",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [])
    expect("an archive field after `_` ends the token; any other `_` continues it",
           [m.group() for m in tokens("olivares_1.0_linux_amd64.tar.gz olivares_1.0_fips_linux_amd64 1.0_rc1")]
           == ["1.0", "1.0", "1.0_rc1"])
    expect("the right-hand side of a range is a token too",
           [m.group() for m in tokens("compare/26.10.1...1.0 and 1.0..1.1")] == ["26.10.1", "1.0", "1.0", "1.1"])
    expect("a witness is named only <label>-install-surfaces.json: a prerelease file is no 1.0 witness",
           witness_version("docs/releases/1.0-install-surfaces.json") == "1.0"
           and witness_version("docs/releases/v26.8.0-install-surfaces.json") == "v26.8.0"
           and witness_version("docs/releases/1.0-rc.1-install-surfaces.json") is None
           and witness_version("docs/releases/1.0-fips-install-surfaces.json") is None)
    expect("a retired list without a final newline keeps its last tag",
           read_retired_tags(read=lambda _: "v26.8.0\nv26.9.0\n26.10.0\n26.10.1") ==
           {"v26.8.0", "v26.9.0", "26.10.0", "26.10.1"})
    for corrupt in ("v26.8.0\r\n26.10.1\r\n", "v26.8.0\n 26.10.1\n", "V26.8.0\n"):
        code, msg = refusal(lambda: read_retired_tags(read=lambda _: corrupt))
        expect(f"a retired list with a CR, a space or a capital V is COULD NOT LOOK: {corrupt!r}", code == 2)
    expect("FORMS names exactly the published suffixes",
           set(FORMS) == {"", "-fips", "-stig", "-amd64", "-arm64", "-fips-amd64", "-stig-amd64", "-1",
                         "-install-surfaces"})
    for bad in ("1.0-dev", "1.0-2", "1.0-SNAPSHOT-2a86e10a", "1.0-fips-arm64"):
        line = f"image: docker.io/olivaresai/olivares:{bad}"
        expect(f"a development stamp or an unpublished suffix is no product form: {bad} -> red",
               judge_pins("1.0", [Hit("deploy/x.yaml", 1, m.group(), line, False, False, m.start()) for m in pin_tokens(line)],
                          curated=set(), kept=set()) == [("deploy/x.yaml", 1, bad)])
    expect("a dash-joined archive name is no published artifact (they use `_`) -> red even at the canon",
           judge_pins("1.0", [Hit("deploy/x.yaml", 1, m.group(), "x", False, False, m.start())
                              for m in pin_tokens("curl -O https://dl/olivares-1.0-linux-amd64.tar.gz")],
                      curated=set(), kept=set()) == [("deploy/x.yaml", 1, "1.0-linux-amd64")])
    expect("a changelog section starts only at a heading naming one version without a suffix",
           section_start("## [1.0] - 2026-10-07") and section_start("## [Unreleased]")
           and not section_start("## [1.0-rc.1] - 2026-10-07"))
    expect("the stamp leaves an image pinned by digest",
           restamped("docker.io/olivaresai/olivares:1.0@sha256:" + "a" * 64, "1.1") is None)
    tree = {"CHANGELOG.md": "# Changelog\n\n## [26.10.1] - 2026-10-04\n"
                            "olivares 26.10.0, olivares 26.8.0 and olivares v26.9.0\n"}
    expect("retired history: a bare tag and a v tag spelled without its v -> green",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [])
    for exc in (FileNotFoundError(2, "absent"), PermissionError(13, "denied"),
                UnicodeDecodeError("utf-8", b"\xff", 0, 1, "invalid")):
        def unreadable_list(_, exc=exc):
            raise exc
        code, msg = refusal(lambda: read_retired_tags(read=unreadable_list))
        expect(f"an unreadable retired list is COULD NOT LOOK (exit 2): {type(exc).__name__}",
               code == 2 and "cannot be read" in msg)
    with tempfile.TemporaryDirectory() as folder:
        for name, data in (("crlf", b"v26.8.0\r\nv26.9.0\r\n26.10.0\r\n26.10.1\r\n"),
                           ("nul", b"v26.8.0\nv26.9.0\n26.10.0\n26.10.1\x00\n")):
            listed = os.path.join(folder, name)
            with open(listed, "wb") as out:
                out.write(data)
            code, _ = refusal(lambda: read_retired_tags(path=listed))
            expect(f"the real list reader refuses a {name} file as the finalizer does (exit 2)", code == 2)
    for good in ("**docker.io/olivaresai/olivares:1.0**", "`olivaresai/olivares:1.0`.", "olivaresai/olivares:1.0:"):
        tree = {"README.md": f"Run <!-- release -->{good}<!-- /release -->\n"}
        expect(f"a README image tag ends at its delimiter: {good} -> green",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [])
    for bad in ("26.9.0:1.0", "rc:1.0", "1.0:1.0", "1.0.beta"):
        tree = {"README.md": f"docker pull olivaresai/olivares:{bad}\n"}
        expect(f"a README image is judged by its WHOLE tag: olivaresai/olivares:{bad} -> red",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]
               == [("README.md", 1, f"olivaresai/olivares:{bad}")])
    for spelled in ("1.0\u00adrc1", "1.0\uff0drc1", "1.0\ufe63rc1", "1.0\u200e-rc.1", "1.0%2Drc.1",
                    "1.0\u00e9", "1.0\u200b.1", "1.0-r\u200bc1", "1.0.b\u00e9ta"):
        tree = {"deploy/helm/olivares/Chart.yaml": f'appVersion: "{spelled}"\n'}
        expect(f"any non-ASCII character or percent-escape stays in the token: {spelled!r} -> red",
               judge_artifacts("1.0", scan(tree, rd(tree))) == [("deploy/helm/olivares/Chart.yaml", 1, spelled)])
    for prose in ("x <!-- release -->\u00abOlivares 1.0\u00bb ships<!-- /release -->",
                  "x <!-- release -->Olivares 1.0\u2026 ships<!-- /release -->",
                  "x <!-- release -->\u201cOlivares 1.0\u201d ships<!-- /release -->",
                  "x <!-- release -->Olivares 1.0\u3002<!-- /release -->", "the 26.10-Cask and v26.9-Bundle months"):
        tree = {"docs/x.md": prose + "\n"}
        expect(f"prose punctuation closes a token and a month stays prose: {prose!r} -> green",
               judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [])
    for spelled in ("1.0-amd64.2rc", "1.0-arm64.1a", "1.0-_a", "1.0-.0", "1.0__a", "1.0.-a", "1.0..a",
                    "1.0+.0", "1.0~.a", "1.0-%2B", "1.0%2B", "1.0-", "1.0-.", "1.0-+", "1.0%2B+", "1.0_-",
                    "1.0-amd64-.", "1.0_linux\u00e9", "1.0.tar\u200b",
                    "26.9.\u200b0", "26.9%2E0"):
        line = f"image: docker.io/olivaresai/olivares:{spelled}"
        expect(f"a separator run, a number tail or a word boundary keeps the whole token: {spelled!r} -> red",
               judge_pins("1.0", [Hit("deploy/x.yaml", 1, m.group(), line, False, False, m.start()) for m in pin_tokens(line)],
                          curated=set(), kept=set()) == [("deploy/x.yaml", 1, spelled)])
    expect("a range stays two tokens; a dangling + or a final full stop stays outside",
           [m.group() for m in tokens("1.0..1.1 and 1.0+ and 1.0.")] == ["1.0", "1.1", "1.0", "1.0"])
    expect("a variant record binds whole path components: NOT-INSTALL.md is not INSTALL.md",
           allowed_at("1.0", "deploy/NOT-INSTALL.md") == {"1.0"} and "1.0-fips" in allowed_at("1.0", "INSTALL.md"))
    expect("numbers of any length order without int()",
           version_key("1." + "9" * 5000) > version_key("1.0") and version_key("01.0") == version_key("1.0"))
    for comment in (b"# caf\xe9\n", b"# n\x00ul\n", b"# tab\there\n"):
        code, _ = refusal(lambda: read_retired_tags(read=lambda _: comment + b"v26.8.0\n26.10.1\n"))
        expect(f"a list with any byte but printable ASCII, even in a comment, is refused: {comment!r}", code == 2)
    tree = {"docs/x.md": "install olivares 26.9.\u200b0 today\n"}
    expect("a retired-month number with any digit in its suffix is a release, not month prose",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("docs/x.md", 1, "26.9.\u200b0")])
    tree = {"docs/x.md": "install olivares 26.9\u200b.0 today\n"}
    expect("a retired-month number with a suffix is judged, not read as month prose",
           judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0] == [("docs/x.md", 1, "26.9\u200b.0")])
    started = time.monotonic()
    section_start("## [1.0" + "a" * 26 + "!]")
    TOKEN.fullmatch("1.0" + "-a" * 4000 + "!")
    list(tokens("olivares 1.0" + "a" * 26 + "! 1.0" + "%2B" * 4000 + "!"))
    expect("the token grammar is linear: adversarial inputs take well under a second",
           time.monotonic() - started < 1.0)
    def scan_time(text):
        tree = {"docs/x.md": text}
        started = time.monotonic()
        scan(tree, rd(tree))
        return time.monotonic() - started
    for name, make in (("a long run of olivares-install- names", lambda n: "olivares-install-" * n + " --version 1.0\n"),
                       ("a long run of continued lines", lambda n: "1.0 \\\n" * n),
                       ("many tokens on one long line", lambda n: "olivares 1.0 " * n + "\n")):
        small, large = scan_time(make(2500)), scan_time(make(5000))
        expect(f"the scan is linear: {name}, twice the input in {large:.2f}s against {small:.2f}s",
               large < 3 * small + 0.25)
    expect("FORMS: a hardened architecture tag keeps its variant",
           stated("1.0-fips-amd64") == "1.0-fips" and stated("1.0-fips-arm64") is None)
    badge = {"README.md": "x <!-- release -->[![release](https://img.shields.io/badge/release-1.0-28282B)](x)"
                          "<!-- /release -->\n"}
    expect("a shields.io badge's version ends at its field dash -> the canon is green, a stale one red",
           judge("1.0", scan(badge, rd(badge)), curated=set(), kept=set())[0] == []
           and judge("1.1", scan(badge, rd(badge)), curated=set(), kept=set())[0] == [("README.md", 1, "1.0")])
    expect("the floor judge admits a retired tag as tagged -> green",
           record_allowed("1.0", "operator/README.md", "v26.8.0", "(olivares >= v26.8.0)", False, set()))
    code, msg = refusal(lambda: read_retired_tags(read=lambda _: "# none\n"))
    expect("an empty retired list is COULD NOT LOOK (exit 2), never an empty history",
           code == 2 and "retired-release-tags" in msg)
    code, msg = refusal(lambda: read_retired_tags(read=lambda _: "v26.8.0\n1.0\n26.10.1-rc.1\n"))
    expect("a retired list naming a non-tag spelling is COULD NOT LOOK (exit 2)",
           code == 2 and "'1.0'" in msg and "'26.10.1-rc.1'" in msg)

    # ── EVERY EDGE OF THE MARKS HAS A CASE (measured by mutation, 2026-10-09: each case below is red
    # under a weakening of its rule that every other case let pass) ──
    image = "docker.io/olivaresai/olivares"
    for tag in (":1.0.1", ":latest", "", ":1.1", ":1.0-rc.1", ":26.10.1"):
        tree = {"README.md": f"Run <!-- release -->docker run {image}{tag}<!-- /release -->\n"}
        expect(f"a marked README image names the canon, or it is red: {image}{tag}",
               len(judge("1.0", scan(tree, rd(tree)), curated=set(), kept=set())[0]) == 1)
    for path, text in (("deploy/helm/olivares/Chart.yaml", '# release\nappVersion: "1.0-1"\n# /release\n'),
                       ("README.md", "Run <!-- release -->the release 1.0-amd64 ships<!-- /release -->\n")):
        expect(f"a marked suffix out of its FORMS context -> red: {text.strip()!r}",
               len(judge("1.0", scan({path: text}, rd({path: text})), curated=set(), kept=set())[0]) == 1)
    expect("a marked pin is judged as a release, never as history: a retired tag in a mark is red",
           judge_pins("1.0", [Hit("CHANGELOG.md", 3, "26.10.1", "pull olivaresai/olivares:26.10.1", True, True, 24)],
                      curated=set(), kept=set()) != [])
    expect("the product's lines: MAJOR 1 to 99 whatever the canon, and 26.x as main judged it",
           all(product_line(t) for t in ("1.0.1", "1.9", "2.0", "v2.0", "3.0", "27.1", "99.1.0", "26.11"))
           and not any(product_line(t) for t in ("0.9", "100.0", "2025.10", "26.10", "26.05.1")))

    def marked_spans(path, text):
        spans = []
        code, _ = refusal(lambda: spans.extend(marks(path, text)))
        return "malformed" if code else spans
    for path, text in (("deploy/x.yaml", "# Release\nimage: x\n"), ("deploy/x.sh", "echo hi # release\n"),
                       ("docs/x.mdx", "x <!-- release -->1.0<!-- /release -->\n"),
                       ("docs/x.md", "x <!-- Release -->1.0<!-- /Release -->\n"), ("other/.SRCINFO", "pkgver = 1.0\n")):
        expect(f"a mark is exact: lowercase, a whole line or its file type's form, a whole-file path: {path}: {text!r}",
               marked_spans(path, text) == [])
    code, _ = refusal(lambda: scan(["docs/x.mdx"], lambda _: "x {/* release */}1.0{/* /release */}.1\n"))
    expect("an MDX mark cutting a version in two is malformed", isinstance(code, str) and "cuts a version" in code)
    for text in ("1. <!-- release -->1.0<!-- /release --> ships\n", "* <!-- release -->1.0<!-- /release --> ships\n",
                 "   <!-- release -->1.0<!-- /release --> ships\n"):
        expect(f"a Markdown mark that starts a block, after any list marker or indentation, is malformed: {text!r}",
               marked_spans("docs/x.md", text) == "malformed")
    code, _ = refusal(lambda: scan(["docs/x.md"], lambda _: "a\n\nx <!-- release -->1.0\n"))
    expect("a malformed mark names its path and line", isinstance(code, str) and "docs/x.md:3:" in code)
    for path, text, refused in (("docs/x.md", "## [2.0] - 2026-10-08\n", [("docs/x.md", 1, "2.0")]),
                                ("docs/x.md", "first_contract_version='2.0~'\n", [("docs/x.md", 1, "2.0")]),
                                ("docs/x.md", "It was cut as 1.0 <!-- release-fixed --> once\n", [("docs/x.md", 1, "1.0")]),
                                ("docs/x.md", "`1.0`<!-- release-fixed --> was a fresh start\n", []),
                                ("docs/x.md", "  - Olivares 1.0<!-- release-fixed --> was cut\n", []),
                                ("docs/x.md", "Olivares release go" + " " * 158 + "1.0.1\n", [("docs/x.md", 1, "1.0.1")]),
                                ("docs/x.md", "From 1.0<!-- release-fixed --> on; Kubernetes ships 2.0 later\n",
                                 [("docs/x.md", 1, "2.0")]),
                                ("docs/x.md", "Roll the chart to >= 2.0 first.\n", [("docs/x.md", 1, "2.0")])):
        expect(f"a record structure binds only on its path and at its occurrence: {text.strip()[:60]!r}",
               judge("1.0", scan({path: text}, rd({path: text})), curated=set(), kept=set())[0] == refused)
    with tempfile.TemporaryDirectory() as stage:
        held = os.getcwd()
        os.chdir(stage)
        try:
            files = {"README.md": f"Run <!-- release -->docker run {image}:1.0<!-- /release -->\n"
                                  f"Run <!-- release -->docker run {image}<!-- /release -->\n",
                     "p/a.sh": "#!/bin/sh\n# release\nver=1.0\n# /release\n",
                     "x.md": "intro\u2028more\n<!-- release -->\n1.0\n<!-- /release -->\n",
                     "p/b.go": "\t// release\n\tconst v = \"1.0\" // and `:1.0`\n\t// /release\n"}
            for path, text in files.items():
                os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
                with open(path, "w", encoding="utf-8", newline="") as output:
                    output.write(text)
            os.chmod("p/a.sh", 0o755)
            count = stamp_surfaces("1.1", scan(sorted(files), read_surface))
            expect("the stamp keeps a README image's repository, and leaves a tagless one",
                   read_surface("README.md") == files["README.md"].replace(":1.0<", ":1.1<"))
            expect("the stamp keeps a file's mode and leaves no temporary file",
                   stat.S_IMODE(os.stat("p/a.sh").st_mode) == 0o755 and sorted(os.listdir("p")) == ["a.sh", "b.go"])
            expect("the stamp splits lines as the scan does (a U+2028 line break included)",
                   read_surface("x.md") == files["x.md"].replace("\n1.0\n", "\n1.1\n"))
            expect("the stamp rewrites each occurrence at its column after a tab indentation",
                   read_surface("p/b.go") == files["p/b.go"].replace("1.0", "1.1"))
            expect("the stamp returns the number of files it rewrote", count == 4)
            hits = scan(sorted(files), read_surface)
            expect("a second stamp at the same canon rewrites no file", stamp_surfaces("1.1", hits) == 0)
            with open("p/a.sh", "w", encoding="utf-8") as output:
                output.write("#!/bin/sh\n# release\nver=9.9\n# /release\n")
            code, _ = refusal(lambda: stamp_surfaces("1.2", hits))
            expect("a file changed under the stamp stops it before any file is written",
                   isinstance(code, str) and "p/a.sh:3 changed under the stamp" in code
                   and ":1.1<" in read_surface("README.md"))
            stamp_surfaces("1.10", scan(["p/b.go"], read_surface))
            expect("a longer canon replaces each version whole and keeps what follows it",
                   read_surface("p/b.go") == files["p/b.go"].replace("1.0", "1.10"))
            os.makedirs("scripts")
            with open("scripts/t.sh", "w", encoding="utf-8") as output:
                output.write("FIXTURE_RPM=olivares-1.0-1.x86_64.rpm\n")
            pins = scan_pins(["scripts"])
            expect("the pin census reads shell scripts: an unmarked canon pin there is red",
                   len(pins) == 1 and judge_pins("1.0", pins, curated=set(), kept=set()) != [])
        finally:
            os.chdir(held)
    held_curated, held_kept = curated_out, curation_kept
    try:
        globals()["curated_out"] = lambda read=None: {"docs/contracts"}
        globals()["curation_kept"] = lambda read=None: {"docs/contracts/S9.md"}
        for name, read, why in (("a fourth field", lambda _: "x\tdocs/contracts/S1.md\t\\x00\tmore\n", "is not"),
                                ("an empty field", lambda _: "x\t \t\\x00\n", "is not"),
                                ("a document the export publishes again", lambda _: "x\tdocs/contracts/S9.md\t\\x00\n",
                                 "publishes"),
                                ("bytes that are not UTF-8", lambda _: b"x\tdocs/contracts/S1.md\t\\x00\xff\n", "not UTF-8"),
                                ("a file that cannot be read",
                                 lambda _: (_ for _ in ()).throw(PermissionError(13, "denied")), "cannot be read")):
            code, msg = refusal(lambda: private_rows(read=read))
            expect(f"private rows: {name} refuses the file whole (exit 2)", code == 2 and why in msg)
        expect("private rows: blank lines and comments are no rows",
               [n for n, _, _ in private_rows(read=lambda _: "\n# c\n\nx\tdocs/contracts/S1.md\t\\x00v1\\.0\\.1\\x01\n\n")] == ["x"])
    finally:
        globals()["curated_out"], globals()["curation_kept"] = held_curated, held_kept

    # ── NO SUCCESS ESCAPE (2026-09-23) ──
    # RELEASE-VERSION is the published install baseline, so a tree always has one. UNDECIDED used
    # to put the run into a report mode that printed the census and exited 0: a green with no canon
    # behind it. It is refused now, by the schema and by every judge, and never with exit 0.
    code, msg = refusal(lambda: read_canon("# c\nUNDECIDED\n"))
    expect("UNDECIDED canon -> refused (exit 2), never a success", code == 2 and "UNDECIDED" in msg)
    und = scan({"deploy/x.yaml": "26.700"}, rd({"deploy/x.yaml": "26.700"}))
    expect("no judge treats UNDECIDED as a pass: documents, pins and artefacts all refuse (exit 2)",
           refusal(lambda: judge("UNDECIDED", und))[0] == 2
           and refusal(lambda: judge_pins("UNDECIDED", und, curated=set(), kept=set()))[0] == 2
           and refusal(lambda: judge_artifacts("UNDECIDED", und))[0] == 2)
    expect("words: every entry of the file is read (an unread entry is dead data)", sorted(set(WORDS) - _WORDS_READ) == [])
    print("selftest " + ("OK — every red case is red, every green case is green" if ok else "FAILED"))
    sys.exit(0 if ok else 1)

if SELFTEST:
    selftest()

# ⛔ SIN ESTA GUARDA el gate moría con un FileNotFoundError de Python: un traceback y rc=1, que
# se lee como «he mirado y la versión diverge» cuando en realidad no había nada que leer.
try:
    _release_version_raw = open("RELEASE-VERSION", encoding="utf-8").read()
except OSError as exc:
    unverified(f"UNVERIFIED check-release-version: COULD NOT CHECK — RELEASE-VERSION is "
               f"unreadable ({exc}); there is no canon to compare against, which is not the "
               f"same as a canon that disagrees.")
canon = read_canon(_release_version_raw)

for path in COMPOSE_FILES:
    text = read_surface(path)
    stamped = stamp_compose(canon, text)
    if os.environ.get("CRV_STAMP_COMPOSE") == "1" or os.environ.get("CRV_STAMP") == "1":
        with open(path, "w", encoding="utf-8") as output:
            output.write(stamped)
    elif text != stamped:
        sys.exit(f"FAIL check-release-version: {path} image differs from RELEASE-VERSION {canon}; "
                 "run scripts/check-release-version.sh --stamp-compose")
if os.environ.get("CRV_STAMP_COMPOSE") == "1":
    print(f"OK check-release-version: Compose image defaults stamped from {canon}")
    sys.exit(0)

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
if os.path.isdir("operator") and not crd_types_files():
    # Same contract one level down: the min-version scope of the CRD API types is derived,
    # so a discovery that resolves to nothing is COULD NOT LOOK, not "no exemption needed".
    # Left silent it would judge a legitimate compatibility floor as a stale coordinate.
    print(f"check-release-version: no CRD API type declaration matched {CRD_TYPES_GLOB}; the "
          "compatibility-floor scope is UNVERIFIED.", file=sys.stderr)
    print("  The operator's API package moved or was renamed — the floors it declares did not", file=sys.stderr)
    print("  go away. Fix the discovery; do not write the path back (it carries the pre-rename", file=sys.stderr)
    print("  product word, and the public-export leak gate refuses that word in scripts/).", file=sys.stderr)
    sys.exit(2)
if os.environ.get("CRV_STAMP") == "1":
    count = stamp_surfaces(canon, hits + pin_hits + ahits)
    print(f"Stamped {count} release surfaces from {canon}; checking the result", flush=True)
    os.execv("/bin/sh", ["sh", "scripts/check-release-version.sh"])

afailures = judge_artifacts(canon, ahits)

if failures or afailures:
    total = len(failures) + len(afailures)
    print(f"FAIL check-release-version: canon is {canon}; {total} divergent occurrence(s):")
    base = canon.lstrip("v")
    unmarked = {(hit.path, hit.number) for hit in hits + pin_hits + ahits if not hit.marked}
    text_at = {(hit.path, hit.number): hit.line for hit in hits + pin_hits}
    for p, i, tok in failures[:40]:
        note = ""
        if any(tok.endswith(h) and tok.lstrip("v")[:-len(h)] == base for h in HARDENED):
            note = ("  <- the canon's HARDENED tag, not another version: if this file documents "
                    "the artefact matrix, give it a 'variants' record in DERIVED_ALLOW")
        elif wrapped_citation(text_at.get((p, i), ""), tok):
            note = ("  <- a changelog SECTION reference whose line names no changelog: if the "
                    "citation wrapped, put `CHANGELOG.md` and the section on ONE line")
        elif tok.lstrip("v") == base and (p, i) in unmarked:
            note = ("  <- the canon outside a release mark: wrap a live release in "
                    "<!-- release -->…<!-- /release --> (MDX {/* release */}, other files a "
                    "`# release` line pair); a fixed one takes <!-- release-fixed --> after it")
        print(f"  {p}:{i}: {tok}{note}")
    for p, i, tok in afailures[:40]:
        print(f"  {p}:{i}: {tok}   [shipped release coordinate]")
    sys.exit(1)
marked = {(hit.path, hit.number, hit.column) for hit in hits + pin_hits + ahits if hit.marked}
pins = sum(1 for hit in pin_hits if not hit.marked and not go_test_fixture(hit.path))
judged = sum(1 for hit in ahits if hit.marked or product_line(hit.token) or in_record(hit))
print(f"OK check-release-version: every release-bearing surface states {canon} (or a derived form) "
      f"\u2014 {len(files)} documentation surface(s) scanned whole, {len(marked)} marked release "
      f"position(s) in {len({m[0] for m in marked})} file(s), plus {pins} artefact pin(s) judged as records "
      f"across the {len(tops)} published top-level entries (census: {census_src}; tree: {profile})")
print(f"OK check-release-version: {judged} release coordinate(s) and record(s) in {len(afiles)} walked "
      f"file(s) under {'/, '.join(ARTIFACT_ROOTS)}/ agree with the canon")
PY
