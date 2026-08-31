#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-release-version.sh — one first-release version, stated everywhere or nowhere.
#
# The public surfaces disagreed for weeks (README ×7 promised v26.7.0, CHANGELOG said
# v26.6.0, a launch draft carried v0.1.0) and no job noticed. The version CHOICE is the
# owner's; this gate owns the MECHANICS: RELEASE-VERSION at the repo root is the single
# source of truth, and every product-version-shaped token (CalVer vYY.M.PATCH, YY >= 26)
# on a release-bearing surface must equal it — or one of its derived forms: the -fips /
# -stig image variants and the next-patch upgrade example the docs legitimately use.
#
# While RELEASE-VERSION says UNDECIDED, the gate prints the full divergence census
# (grouped by value, so the owner can decide from evidence) and exits 0 with a loud
# PENDING banner — the lint:commerce report-mode precedent: land the gate before the
# decision, so the decision lands enforced. The moment a real version is written, any
# divergent surface turns the gate red.
#
# Exemptions, always explicit and context-bound: the -fips/-stig variants and the
# next-patch upgrade example are allowed ONLY at the named doc paths (DERIVED_ALLOW);
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
# Usage: check-release-version.sh [--selftest]
set -eu
cd "$(dirname "$0")/.."

CRV_SELFTEST=0
[ "${1:-}" = "--selftest" ] && CRV_SELFTEST=1
export CRV_SELFTEST

python3 - <<'PY'
import glob, os, re, sys

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

# CalVer product versions: vYY.M.PATCH with YY>=26 — dependency versions (go1.26.5,
# chi v5.3.1, node 24) do not match this shape.
VERSION = re.compile(r"\bv?(2[6-9]|[3-9][0-9])\.([1-9]|1[0-2])\.([0-9]+)(-fips|-stig)?\b")

# Exact allowlist records: (path suffix, exemption kind, reason). Kinds:
#   variants   — the -fips/-stig image-tag forms of the exact canon
#   next-patch — the canon's next patch, ONLY as the documented upgrade example
# Nothing else is ever exempt; there is no line-level waiver (a readiness marker or an
# illustrative SemVer example never matches the CalVer shape, so neither needs one — and
# a general line-skip was measured hiding a foreign version on a marker line).
DERIVED_ALLOW = [
    # INSTALL.md documents the SAME two things its two allowlisted siblings do — the FIPS/STIG
    # image tags and a same-month fix as the upgrade example — and was simply never listed. It
    # only surfaced when a canon was finally set: while RELEASE-VERSION said UNDECIDED the gate
    # printed a census instead of judging, so the omission could not show.
    ("INSTALL.md", "variants", "the FIPS/STIG image tags the install guide documents"),
    ("INSTALL.md", "next-patch", "the same-month fix named as the CalVer example"),
    ("how-to/docker-deployment.md", "variants", "image tags for the FIPS/STIG builds"),
    ("how-to/docker-deployment.md", "next-patch", "the documented upgrade example"),
    ("how-to/air-gap-install.md", "variants", "air-gap bundle filenames for the hardened builds"),
    # The Docker Hub overview IS the artefact matrix — the same class as its two siblings above,
    # and it never surfaced because `packaging/` was outside the census entirely (see below).
    ("packaging/docker/dockerhub-overview.md", "variants", "the published FIPS/STIG image tags"),
    # Top-level docs/, reachable only since docs/ became a walk. Same discipline as every
    # record above: exact path, named kind, written reason — the CENSUS is derived, the
    # EXEMPTIONS are exact, so a doc nobody exempted is required to state the canon.
    ("docs/UPGRADE-AND-ROLLBACK.md", "variants",
     "the :TAG, :TAG-fips and :TAG-stig image coordinates the upgrade guide documents"),
    ("docs/UPGRADE-AND-ROLLBACK.md", "next-patch",
     "the LTS line's first backport, named as the patch-bumping example"),
    ("docs/HA-LEADER-ROUTING.md", "min-version",
     "the /pod-readyz precondition is a compatibility FLOOR, not a shipped coordinate"),
    ("docs/PSIRT-RUNBOOK.md", "next-patch",
     "the out-of-band security release the incident drill cuts"),
    ("docs/PSIRT-RUNBOOK.md", "min-version",
     "the advisory's affected-range START (--min-version / introduced), a bound not a claim"),
    # A consumed tag cannot be un-published, so both release runbooks prescribe the same
    # remedy: cut the NEXT PATCH, never re-point the tag. That is the canon's next patch
    # named as a remedy rather than as an upgrade example — same value, same kind.
    # export-closure: absent-by-design docs/RELEASE-GO-LIVE-RUNBOOK.md — a SUBJECT of this
    # rule, not a caller. The export removes it (the export curation script, line 175), and this record
    # is DATA: in the published tree the path is simply never matched. Nothing executes it, so
    # there is no call to guard — hub-only would be the wrong class.
    # export-closure: absent-by-design docs/RELEASE-NEXT-ACTIONS.md — same class, same reason
    # (removed at the export curation script, line 177): named here only so the HUB tree does not demand
    # the canon inside a runbook whose whole prescription is to cut the NEXT patch.
    ("docs/RELEASE-GO-LIVE-RUNBOOK.md", "next-patch",
     "the next patch named as the roll-forward remedy for a bad tag"),
    ("docs/RELEASE-NEXT-ACTIONS.md", "next-patch",
     "the next patch named as the roll-forward remedy for a bad tag"),
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
    read = read or (lambda p: open(p, encoding="utf-8").read())
    try:
        src = read(EXPORT_SCRIPT)
    except OSError:
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

def scan_pins(tops):
    """-> [(path, line_no, token, line)] for every artefact pin under a published tree."""
    hits = []
    for top in tops:
        if os.path.isfile(top):
            walk = [(".", [], [top])]
        elif os.path.isdir(top):
            walk = os.walk(top)
        else:
            continue  # go.work.sum etc. may legitimately not exist in a partial tree
        for root, dirs, files in walk:
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
                try:
                    text = open(path, encoding="utf-8", errors="replace").read()
                except OSError:
                    continue
                for i, line in enumerate(text.splitlines(), 1):
                    for m in PIN.finditer(line):
                        hits.append((path, i, m.group(0), line.strip()))
    return hits

def judge_pins(canon, hits):
    """A pin must name the canon (or, where DERIVED_ALLOW says so, a derived form)."""
    if canon == "UNDECIDED":
        return []
    bad = []
    for path, i, tok, _ in hits:
        ver = re.search(r"v?(\d+\.\d+\.\d+(?:-fips|-stig)?)$", tok).group(1)
        if ver.lstrip("v") not in {f.lstrip("v") for f in allowed_at(canon, path)}:
            bad.append((path, i, tok))
    return bad

CANON_SHAPE = re.compile(r"v(2[6-9]|[3-9][0-9])\.([1-9]|1[0-2])\.([0-9]+)")

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
# `task lint:export` red and would have blocked every lane's push. The gate is right —
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
    if canon == "UNDECIDED":
        return []
    return [(p, i, tok) for p, i, tok, line in hits
            if not artifact_allowed(canon, p, tok, line)[0]]


def artifact_files():
    out = []
    for root in ARTIFACT_ROOTS:
        for r, dirs, files in os.walk(root):
            dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "vendor")]
            out += [os.path.join(r, f) for f in files]
    return sorted(f for f in out if os.path.isfile(f))

def read_canon(text):
    """Schema, not scrape: exactly ONE non-comment record, and it must be UNDECIDED or a
    v-prefixed CalVer with a real month. Anything else refuses before any surface scan —
    a malformed canon certified OK was the measured failure mode."""
    records = [ln.strip() for ln in text.splitlines() if ln.strip() and not ln.strip().startswith("#")]
    if len(records) != 1:
        sys.exit(f"FAIL check-release-version: RELEASE-VERSION must contain exactly one record, found {len(records)}")
    canon = records[0]
    if canon != "UNDECIDED" and not CANON_SHAPE.fullmatch(canon):
        sys.exit(f"FAIL check-release-version: RELEASE-VERSION record {canon!r} is neither UNDECIDED nor vYY.M.PATCH (month 1-12)")
    return canon

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
        elif kind == "next-patch":
            nxt = f"{yy}.{mm}.{int(pp) + 1}"
            allowed |= {nxt, f"v{nxt}"}
    return allowed

def scan(files, rd):
    """-> [(path, line_no, token, line)] for every product-version-shaped token."""
    hits = []
    for path in files:
        text = rd(path)
        for i, line in enumerate(text.splitlines(), 1):
            for m in VERSION.finditer(line):
                hits.append((path, i, m.group(0), line.strip()))
    return hits

def doc_allowed(canon, path, tok, line):
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
    return False


def judge(canon, hits):
    """-> (failures, census) — failures non-empty only when the canon is decided."""
    census = {}
    for path, i, tok, line in hits:
        census.setdefault(tok.lstrip("v").replace("-fips", "").replace("-stig", ""), []).append((path, i, tok))
    if canon == "UNDECIDED":
        return [], census
    failures = [(path, i, tok) for path, i, tok, line in hits
                if not doc_allowed(canon, path, tok, line)]
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


def docs_files():
    """-> every file under docs/, walked whole. The declarants are DISCOVERED.

    Not a list of extensions either: a runbook that lands as .txt or .yaml still tells an
    operator which version to deploy. Binaries are skipped by the reader, not by name.
    """
    out = []
    for root, dirs, files in os.walk("docs"):
        dirs[:] = [d for d in dirs if not pruned_dir(d)]
        out += [os.path.join(root, f) for f in sorted(files)]
    return sorted(f for f in out if os.path.isfile(f))


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


def surface_files():
    out = ["CHANGELOG.md", "README.md"] + sorted(glob.glob("README.*.md"))
    for base in ("docs/trust", "docs/launch"):
        out += sorted(glob.glob(f"{base}/*.md"))
    # docs/ whole — this is what closes the blind spot; the two globs above are kept
    # because they name surfaces that must be scanned even if the walk is ever narrowed.
    out += docs_files()
    for root, dirs, files in os.walk("docs-site/src/content/docs"):
        if "/2026-06" in root or "/adr" in root:
            dirs[:] = []
            continue
        dirs[:] = [d for d in dirs if d not in ("2026-06", "adr")]
        out += [os.path.join(root, f) for f in sorted(files) if f.endswith((".md", ".mdx"))]
    if os.path.isfile("INSTALL.md"):
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
        for root, dirs, files in os.walk(base):
            dirs[:] = [d for d in dirs if d not in ("node_modules", ".git", "vendor")]
            for f in sorted(files):
                if f.endswith((".md", ".yaml", ".yml", ".json", ".txt", ".sh")):
                    out.append(os.path.join(root, f))
    return [f for f in out if os.path.isfile(f)]
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
    expect("UNDECIDED canon -> accepted", read_canon("# c\nUNDECIDED\n") == "UNDECIDED")
    expect("well-formed canon -> accepted", read_canon("v26.7.0\n") == "v26.7.0")
    # ── decided canon: divergence is red; derived forms only in their contexts ──
    tree = {"README.md": "ships with `v26.7.0` today", "CHANGELOG.md": "the first release is `v26.6.0`"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("divergent CHANGELOG under decided canon -> red", fails == [("CHANGELOG.md", 1, "v26.6.0")])
    tree = {"docs-site/src/content/docs/how-to/docker-deployment.md":
            "pull olivares:26.7.0-fips then upgrade to 26.7.1"}
    fails, _ = judge("v26.7.0", scan(tree, rd(tree)))
    expect("fips + next-patch INSIDE the upgrade doc -> green", fails == [])
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
    fails, census = judge("UNDECIDED", scan({"f.md": "v26.6.0 and v26.7.0"}, rd({"f.md": "v26.6.0 and v26.7.0"})))
    expect("undecided canon -> census only", fails == [] and set(census) == {"26.6.0", "26.7.0"})
    # ── arm B: the pin/floor distinction, and the coverage that made arm A caducate ──
    pins = lambda text, path="operator/config/samples/x.yaml": judge_pins(
        "v26.7.0", [(path, i, m.group(0), ln)
                    for i, ln in enumerate(text.splitlines(), 1) for m in PIN.finditer(ln)])
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
    real_tops, src_name = published_tops()
    expect("the published census is read from export-public.sh, not written here",
           {"deploy", "packaging", "operator", "docs-site"} <= set(real_tops) and len(real_tops) > 15
           and "TOP_ALLOW" in src_name)
    def missing(_):
        raise OSError("no such file")
    fb_tops, fb_src = published_tops(read=missing)
    expect("a tree WITHOUT the export script (i.e. a public clone) still gets a census",
           len(fb_tops) > 5 and "absent" in fb_src)
    def wrong_shape(_):
        return "TOP_ALLOW_RENAMED=(\n  core\n)\n"
    try:
        published_tops(read=wrong_shape)
        expect("an export script of UNKNOWN shape -> UNVERIFIED, not a guessed census", False)
    except SystemExit:
        expect("an export script of UNKNOWN shape -> UNVERIFIED, not a guessed census", True)

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
    expect("docs walk is above its enumeration floor",
           len(dfs) >= DOCS_LAST_GOOD["floor"])
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
    expect("undecided canon -> artifacts judged as census, never red",
           judge_artifacts("UNDECIDED", scan({"deploy/x.yaml": "26.7.0"}, rd({"deploy/x.yaml": "26.7.0"}))) == [])
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


def rd_text(path):
    """Both walks enumerate whole trees, so binaries must be skipped, not decoded."""
    with open(path, "rb") as fh:
        raw = fh.read()
    if b"\0" in raw[:8192]:
        return ""
    return raw.decode("utf-8", errors="replace")


files = surface_files()

# ── Documentary walk, enumerated fail-closed ──────────────────────────────────────────
# Same contract as the artifact walk: a shrunken enumeration means the walk stopped
# working (a moved directory, a rename, a prune that grew), not that the prose stopped
# declaring. Left silent it would print OK over an unscanned docs/ — which is exactly the
# state this section was added to end.
dfiles = docs_files()
dtop = [f for f in dfiles if os.path.dirname(f) == "docs"]
if len(dfiles) < DOCS_LAST_GOOD["floor"] or len(dtop) < DOCS_LAST_GOOD["top"]:
    print(f"check-release-version: {len(dfiles)} file(s) under docs/ ({len(dtop)} of them at the "
          "top level); the documentary release surfaces are UNVERIFIED.", file=sys.stderr)
    print(f"  On {DOCS_LAST_GOOD['date']} this same walk found {DOCS_LAST_GOOD['measured']} "
          f"file(s): {DOCS_LAST_GOOD['note']}.", file=sys.stderr)
    print(f"  Floor is {DOCS_LAST_GOOD['floor']} file(s) and at least {DOCS_LAST_GOOD['top']} "
          "directly under docs/ — a result below either means the walk broke (a moved root, a",
          file=sys.stderr)
    print("  rename, a prune that grew), not that the documentation stopped declaring versions.",
          file=sys.stderr)
    sys.exit(2)

hits = scan(files, rd_text)
failures, census = judge(canon, hits)

tops, census_src = published_tops()
pin_hits = scan_pins(tops)
pin_failures = judge_pins(canon, pin_hits)
failures = failures + [f for f in pin_failures if f not in failures]
# ── Artifact surfaces, enumerated fail-closed ─────────────────────────────────────────
afiles = artifact_files()
ahits = scan(afiles, rd_text)
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

if canon == "UNDECIDED":
    for path, i, tok, _ in ahits:
        census.setdefault(tok.lstrip("v").replace("-fips", "").replace("-stig", ""), []).append((path, i, tok))
    print("check-release-version: PENDING — RELEASE-VERSION is UNDECIDED (the choice is the owner's).")
    print("Divergence census across release-bearing surfaces (this becomes RED the moment a version is set):")
    for value in sorted(census):
        locs = census[value]
        print(f"  {value}: {len(locs)} occurrence(s) in {len({p for p, _, _ in locs})} file(s)")
        for p, i, tok in locs[:3]:
            print(f"      e.g. {p}:{i} ({tok})")
    sys.exit(0)

if failures or afailures:
    total = len(failures) + len(afailures)
    print(f"FAIL check-release-version: canon is {canon}; {total} divergent occurrence(s):")
    base = canon.lstrip("v")
    for p, i, tok in failures[:40]:
        note = ""
        if tok.lstrip("v").replace("-fips", "").replace("-stig", "") == base:
            note = ("  <- the canon's HARDENED tag, not another version: if this file documents "
                    "the artefact matrix, give it a 'variants' record in DERIVED_ALLOW")
        print(f"  {p}:{i}: {tok}{note}")
    for p, i, tok in afailures[:40]:
        print(f"  {p}:{i}: {tok}   [shipped release coordinate]")
    sys.exit(1)
print(f"OK check-release-version: every release-bearing surface states {canon} (or a derived form) "
      f"\u2014 {len(files)} documentation surface(s) scanned whole, plus {len(pin_hits)} artefact pin(s) "
      f"across the {len(tops)} published top-level entries (census: {census_src})")
print(f"OK check-release-version: {len(ahits)} shipped release coordinate(s) in {len(afiles)} walked "
      f"file(s) under {'/, '.join(ARTIFACT_ROOTS)}/ agree with the canon")
PY
