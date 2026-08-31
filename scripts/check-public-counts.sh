#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-public-counts.sh — the numeric-truth gate for every public surface.
#
# The counts the product states about itself (modules, integrations, framework catalogs,
# deny-closed enforcement points) are DERIVED HERE from the same sources as
# the hub's state script, and every public surface that states one is checked against the
# derived value — as a digit with its localized noun (emphasis markers and CJK counters
# normalized first), as a spelled-out English/Spanish number word, or as a hedge
# ("more than / about / über / 以上 / 超过 / около…", all forbidden: the counts are exact).
#
# The checks are POSITIVE CONTRACTS, not just forbidden-pattern sweeps: required files
# must exist and be non-empty, required claims must be present, the sidebar translation
# set must equal the locale roster, and the reel's rendered masters must match a
# content-hash manifest written by the render pipeline itself
# (an internal design note (not shipped)) — git timestamps are not trusted
# (shallow clones and dirty trees defeat them).
#
# The derivations, in the script, so a number's provenance is code:
#   modules       = unique modules/<name> imports in cmd/olivares/wire.go
#   integrations  = connector dirs − connector dirs with no Go code
#   catalogs      = top-level entries in modules/compliance/frameworks.go
#   enforcement   = deny-closed PEP seams whose PROOF is intact, per the (seam, proof)
#                   census in scripts/enforcement-seams.tsv — not seam files present,
#                   which is what this counted until 2026-08-05 and which a fail-OPEN
#                   seam satisfied just as well
#
# Exemptions are explicit, never silent: the frozen docs-site 2026-06 snapshot, dated
# ADRs, and a VERSIONED allowlist of historical-quote lines (path + literal); the
# `counts-gate: historical-quote` token outside that allowlist FAILS the gate.
#
# The curated public export drops design/, docs/launch/ and sessions/ on purpose
# (the export curation script): those sections print SKIP when their root is absent. In
# the hub tree a missing file inside a present root is a FAILURE, never a skip.
#
# Version strings (v26.6.0 vs v26.7.0) are deliberately NOT checked yet: that
# contradiction is an explicit pending owner decision; the single-source version check
# lands with the export-integrity workstream once decided.
#
# Usage: check-public-counts.sh [--selftest]
#   --selftest runs the red-fixture battery (every trap this gate exists to catch must
#   FAIL on a fixture) and exits without scanning the tree.
set -eu
cd "$(dirname "$0")/.."

CPC_SELFTEST=0
[ "${1:-}" = "--selftest" ] && CPC_SELFTEST=1
export CPC_SELFTEST

# ── the OLIVARES_* configuration reference, delegated (C09-02) ──────────────────
#
# WHY IT HANGS HERE. This script calls itself "the numeric-truth gate for every public
# surface", and the configuration reference is one: measured 2026-08-16, it stated 7 of
# the 311 OLIVARES_* names the non-test Go sources declare. That count is not derived
# here because it is not a count — it is a ROSTER, enumerated from the code by
# scripts/config-env-docs and rendered into the page, so the gate is a regeneration
# check rather than a digit comparison. It is invoked from HERE, and not hung off the
# `lint` aggregate, for exactly the reason lint:docs-honesty records at Taskfile.yml:
# `task lint:public-counts` is what .githooks/pre-push:619 and mainline-ci.yml:517
# actually call, one by one; a check on the aggregate would never run.
#
# It costs 0.95s (check) / 0.28s (self-test), measured 2026-08-16 in the dev container.
#
# TMPDIR IS SET HERE AND NOT LEFT TO THE CALLER, same as lint:error-mappers does for
# its sibling: the gate builds a small Go helper and then EXECUTES it, and /tmp is
# tmpfs+noexec in the dev container — a build under the default TMPDIR dies at exit 126
# with "permission denied" on a file whose exec bit is set, and the wrapper reports it
# as CANNOT LOOK. .config-env-docs-tmp/ is gitignored beside its siblings.
#
# NOTE FOR THE INTEGRATOR: this belongs in a `lint:config-env-docs` task of its
# own; it lives here because was told not to touch Taskfile.yml. Promoting it is a
# six-line Taskfile block plus deleting this delegation — the gate itself does not
# change.
if [ -x scripts/check-config-env-docs.sh ] || [ -f scripts/check-config-env-docs.sh ]; then
	cpc_env_arg=""
	[ "$CPC_SELFTEST" = "1" ] && cpc_env_arg="--self-test"
	TMPDIR="$PWD/.config-env-docs-tmp" bash scripts/check-config-env-docs.sh $cpc_env_arg || {
		cpc_env_rc=$?
		echo "FAIL check-public-counts: the generated OLIVARES_* configuration reference is out of date (exit $cpc_env_rc)." >&2
		echo "  Regenerate with: bash scripts/check-config-env-docs.sh --write" >&2
		exit "$cpc_env_rc"
	}
else
	# Fail closed, in the shape check-migrations.sh uses: the delegation's own absence
	# is "I could not look", never "nothing to report".
	echo "FAIL check-public-counts: CANNOT LOOK — scripts/check-config-env-docs.sh is missing, so the" >&2
	echo "  OLIVARES_* configuration reference was not checked against the code at all." >&2
	exit 2
fi

# ── the generated CLI reference, delegated (C09-03) ────────────────────────────
#
# WHY IT HANGS HERE, for the same reason as its sibling above: `task lint:public-counts`
# is what .githooks/pre-push:619 and mainline-ci.yml:517 actually call, one by one, and a
# check hung off the `lint` aggregate would never run at all.
#
# WHAT IT COSTS, and why that is affordable in the fast lane. The command tree only
# exists at RUNTIME, so the gate walks it with `go test -run TestCLIRefDump
# ./cmd/olivares`. That looks expensive and is not: `task vet` runs at pre-push:397 and
# COMPILES THE TEST FILES of every workspace module, so by the time this runs the test
# binary is a cache hit. Measured 2026-08-16 in the dev container: 2.8s warm, of which
# ~2.6s is the walk and the rest the gate's own build.
#
# TMPDIR IS SET HERE AND NOT LEFT TO THE CALLER, same as the delegation above: the gate
# builds a Go helper and then EXECUTES it, and /tmp is tmpfs+noexec in the dev container
# — a build under the default TMPDIR dies at exit 126 with "permission denied" on a file
# whose exec bit is set. .cli-ref-docs-tmp/ is gitignored beside its siblings.
#
# NOTE FOR THE INTEGRATOR: this belongs in a `lint:cli-ref-docs` task of its own; it
# lives here because was told not to touch Taskfile.yml. Promoting it is a six-line
# Taskfile block plus deleting this delegation — the gate itself does not change:
#
#   lint:cli-ref-docs:
#     desc: Fail when the generated CLI reference and the olivares command tree disagree.
#     cmds:
#       - bash scripts/check-cli-ref-docs.sh --self-test
#       - bash scripts/check-cli-ref-docs.sh
#
# ...called one by one from .githooks/pre-push and .github/workflows/mainline-ci.yml,
# never from an aggregate.
if [ -x scripts/check-cli-ref-docs.sh ] || [ -f scripts/check-cli-ref-docs.sh ]; then
	cpc_cli_arg=""
	[ "$CPC_SELFTEST" = "1" ] && cpc_cli_arg="--self-test"
	TMPDIR="$PWD/.cli-ref-docs-tmp" bash scripts/check-cli-ref-docs.sh $cpc_cli_arg || {
		cpc_cli_rc=$?
		# The delegate keeps drift (1) and CANNOT LOOK (2) apart on purpose, so this
		# must not flatten them back. Measured 2026-08-16 with the walk forced to bail:
		# the delegate said CANNOT LOOK and this line still reported "out of date" and
		# prescribed --write — a false diagnosis with a harmful remedy, because
		# regenerating would write the page from an enumeration that was never made.
		if [ "$cpc_cli_rc" = "2" ]; then
			echo "FAIL check-public-counts: CANNOT LOOK — the CLI reference was not checked against the" >&2
			echo "  command tree at all (exit 2; the cause is named above). Fix what stopped the walk." >&2
			echo "  Do NOT regenerate: --write would publish a page built from an enumeration this gate" >&2
			echo "  could not make." >&2
		else
			echo "FAIL check-public-counts: the generated CLI reference is out of date with the binary (exit $cpc_cli_rc)." >&2
			echo "  Regenerate with: bash scripts/check-cli-ref-docs.sh --write" >&2
		fi
		exit "$cpc_cli_rc"
	}
else
	echo "FAIL check-public-counts: CANNOT LOOK — scripts/check-cli-ref-docs.sh is missing, so the" >&2
	echo "  CLI reference was not checked against the command tree at all." >&2
	exit 2
fi

# ── the published OpenAPI operation descriptions, delegated (C09-04) ───────────
#
# WHY IT HANGS HERE, the same reason as its two siblings above: `task lint:public-counts`
# is what .githooks/pre-push:619 and mainline-ci.yml:517 actually call, one by one, and a
# check hung off the `lint` aggregate would never run at all.
#
# WHAT IT CHECKS. web/openapi/openapi.json publishes 71 operations and
# web/openapi/openapi.beta.json publishes 686; on 2026-08-16 not one of the 757 carried a
# description. This is a ROSTER, not a count — one description per published operation,
# read from the doc comment of the handler the module registered (go/parser) or, where
# the code cannot describe it, from scripts/openapi-op-catalog.tsv — so the gate is a
# regeneration check rather than a digit comparison, exactly like the delegation above.
#
# WHAT IT COSTS, with the conditions it was taken under, because a duration without them
# ages in silence: 0.43s / 0.43s / 0.44s for the check and 0.24s for the self-test, three
# runs each on 2026-08-16 at loadavg 2.2 over 16 cpu in the dev container, with a warm Go
# build cache. It parses the module packages and reads both committed documents; it
# neither builds the binary nor starts a server. Re-measure before citing it.
#
# TMPDIR IS SET HERE AND NOT LEFT TO THE CALLER, same as its siblings: the gate builds a
# Go helper and then EXECUTES it, and /tmp is tmpfs+noexec in the dev container — a build
# under the default TMPDIR dies at exit 126 with "permission denied" on a file whose exec
# bit is set. .openapi-op-descriptions-tmp/ is gitignored beside its siblings.
#
# NOTE FOR THE INTEGRATOR: this belongs in a `lint:openapi-op-descriptions` task of
# its own; it lives here because was told not to touch Taskfile.yml. Promoting it is
# a six-line Taskfile block plus deleting this delegation — the gate does not change:
#
#   lint:openapi-op-descriptions:
#     desc: Fail when a published OpenAPI operation has no description, or one the code does not compose.
#     cmds:
#       - bash scripts/check-openapi-op-descriptions.sh --self-test
#       - bash scripts/check-openapi-op-descriptions.sh
#
# ...called one by one from .githooks/pre-push and .github/workflows/mainline-ci.yml,
# never from an aggregate.
if [ -x scripts/check-openapi-op-descriptions.sh ] || [ -f scripts/check-openapi-op-descriptions.sh ]; then
	cpc_oad_arg=""
	[ "$CPC_SELFTEST" = "1" ] && cpc_oad_arg="--self-test"
	TMPDIR="$PWD/.openapi-op-descriptions-tmp" bash scripts/check-openapi-op-descriptions.sh $cpc_oad_arg || {
		cpc_oad_rc=$?
		echo "FAIL check-public-counts: a published OpenAPI operation description is out of date with the code (exit $cpc_oad_rc)." >&2
		echo "  Regenerate with: bash scripts/check-openapi-op-descriptions.sh --write && task openapi:dump" >&2
		exit "$cpc_oad_rc"
	}
else
	echo "FAIL check-public-counts: CANNOT LOOK — scripts/check-openapi-op-descriptions.sh is missing, so the" >&2
	echo "  published OpenAPI operation descriptions were not checked against the code at all." >&2
	exit 2
fi

# ── the console, gRPC and upgrade guides: PROMOTED OUT of this gate ─────────────────
#
# Esto delegaba en `scripts/check-guide-docs.sh`, y la nota que su autor dejo aqui mismo pedia
# promoverlo a tarea propia: *«this belongs in a `lint:guide-docs` task of its own … promoting
# it is a six-line Taskfile block plus deleting this delegation — the gate does not change»*.
# Hecho, y no por higiene: **este gate corre en el CARRIL RAPIDO y el delegado necesita el
# compilador de TypeScript**, que un worktree recien creado no tiene.
#
# Medido el 2026-08-20 en un worktree nuevo de `origin/main`:
#
#     console-dump: CANNOT LOOK — no TypeScript compiler under web/node_modules or node_modules
#     FAIL check-public-counts: CANNOT LOOK … (exit 2)
#
# ⇒ el gate NO estaba roto: era fail-closed y honesto, y por eso **tumbaba el push de
#   cualquiera** — corre sin `|| true` bajo `set -euo pipefail`. El integrador lo midio el mismo
#   dia dando TRES veredictos para un mismo SHA (worktree anclado PASA · worktree nuevo 5/5
#   FALLAN · worktree de lote 2/8), y la causa es exactamente esta: su veredicto era sobre el
#   ARBOL donde corre, no sobre el codigo.
#
# La regla que ya estaba escrita en este repositorio lo colocaba: **«the fast lane cannot run
# node»**, por eso `lint:format-ratchet` vive en el carril PESADO. `lint:guide-docs` va ahora a
# su lado, y `mainline-ci` lo corre con las deps que YA instala a proposito para este gate
# (`.github/workflows/mainline-ci.yml`, paso `install web deps … needed by lint:public-counts`).
#
# ⛔ NO se ha debilitado nada: el delegado conserva su `--self-test` (46 casos, 37 rojos y 9
#   verdes) y su distincion entre deriva (1) y NO PUDE MIRAR (2). Lo unico que cambia es DONDE
#   se le pregunta.

hub_only_missing() { # <path> <reason>
	if [ -f .olivares-public-export ]; then
		printf 'SKIP %s (hub-only: %s)\n' "$1" "$2"
		return 0
	fi
	printf 'FAIL check-public-counts: hub-only input %s is missing from an unstamped hub: %s\n' \
		"$1" "$2" >&2
	return 1
}

CPC_RELEASE_CHECKLIST=""
# export-closure: hub-only docs/launch/RELEASE-CHECKLIST.md — it contains unpublished maintainer approvals, not product documentation.
if [ -f "docs/launch/RELEASE-CHECKLIST.md" ]; then
	CPC_RELEASE_CHECKLIST="docs/launch/RELEASE-CHECKLIST.md"
else
	hub_only_missing "docs/launch/RELEASE-CHECKLIST.md" \
		"maintainer approval decisions are not published" || exit 1
fi

CPC_REVIEW_NOTES=""
# export-closure: hub-only docs/launch/REVIEW-NOTES.md — it contains internal pre-publication findings, not product documentation.
if [ -f "docs/launch/REVIEW-NOTES.md" ]; then
	CPC_REVIEW_NOTES="docs/launch/REVIEW-NOTES.md"
else
	hub_only_missing "docs/launch/REVIEW-NOTES.md" \
		"pre-publication review findings remain internal" || exit 1
fi

CPC_DAY_D_RUNBOOK=""
# export-closure: hub-only docs/launch/RUNBOOK-DAY-D.md — it contains private launch timing and escalation instructions.
if [ -f "docs/launch/RUNBOOK-DAY-D.md" ]; then
	CPC_DAY_D_RUNBOOK="docs/launch/RUNBOOK-DAY-D.md"
else
	hub_only_missing "docs/launch/RUNBOOK-DAY-D.md" \
		"launch timing and escalation steps are maintainer-only" || exit 1
fi

CPC_BLOG_DRAFT=""
# export-closure: hub-only docs/launch/blog-launch-post.md — it is an unpublished editorial draft with maintainer placeholders.
if [ -f "docs/launch/blog-launch-post.md" ]; then
	CPC_BLOG_DRAFT="docs/launch/blog-launch-post.md"
else
	hub_only_missing "docs/launch/blog-launch-post.md" \
		"the announcement is an unpublished editorial draft" || exit 1
fi

CPC_LAUNCH_INDEX=""
# export-closure: hub-only docs/launch/README.md — it indexes the private launch workspace and publication plan.
if [ -f "docs/launch/README.md" ]; then
	CPC_LAUNCH_INDEX="docs/launch/README.md"
else
	hub_only_missing "docs/launch/README.md" \
		"the launch workspace index is internal planning material" || exit 1
fi

CPC_AGENT_STATE=""
# export-closure: hub-only docs/ai-context/STATE.md — it is mutable operational context for agents, not a public product contract.
if [ -f "docs/ai-context/STATE.md" ]; then
	CPC_AGENT_STATE="docs/ai-context/STATE.md"
else
	hub_only_missing "docs/ai-context/STATE.md" \
		"the dated inventory is private agent-operational context" || exit 1
fi

export CPC_RELEASE_CHECKLIST CPC_REVIEW_NOTES CPC_DAY_D_RUNBOOK
export CPC_BLOG_DRAFT CPC_LAUNCH_INDEX CPC_AGENT_STATE

python3 - <<'PY'
import glob, hashlib, json, os, re, sys

SELFTEST = os.environ.get("CPC_SELFTEST") == "1"
failures = []
skips = []


def blind(msg):
    """NO SE HA PODIDO MIRAR (C15-P6): sale 2, no 1.

    ⛔ POR QUÉ SEPARARLO, cuando este fichero YA decía la verdad en prosa. Sus mensajes llevan
       escrito «an unmeasurable claim is not a passing one» y «a vanished measurement, not a
       zero» — el razonamiento estaba bien y la CODIFICACIÓN no: `sys.exit("FAIL …")` con una
       cadena sale con código 1, que en este repositorio significa «una afirmación pública es
       FALSA». Un censo que falta no hace falsa ninguna afirmación: impide comprobarlas.

       La diferencia importa donde se lee el código y no el texto — un job de CI, un `||` en un
       Taskfile, un carril que triaja diez gates rojos. «La cifra pública miente» manda a corregir
       copy; «no he podido mirar» manda a arreglar el checkout. Confundirlos cuesta la sesión de
       quien lo lea.

       2 sigue siendo NO CERO: nada se relaja. Lo único que cambia es que el código dice cuál de
       las dos cosas pasó.
    """
    print(msg, file=sys.stderr)
    sys.exit(2)

# The explicit public-export marker (written by the curation pipeline, never tracked in
# the hub): a curated-out root's absence is sanctioned ONLY when it is present. In the
# hub, a vanished design/ or docs/launch is a broken checkout, not an export.
PUBLIC_EXPORT = os.path.isfile(".olivares-public-export")

def curated_absence(surface, root, label):
    if PUBLIC_EXPORT:
        skips.append(label)
    else:
        fail(surface, f"{root} is MISSING and this tree carries no public-export marker — a hub input vanished")

def fail(surface, msg):
    failures.append(f"{surface}: {msg}")

def rd(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()

def norm(line):
    """Strip emphasis/code markers and normalize exotic spaces so `**156** integrations`
    and NBSP-joined CJK counters cannot hide a number from its noun."""
    return (line.replace("**", "").replace("__", "").replace("`", "")
                .replace(" ", " ").replace("　", " "))

# ── the enforcement census: POSTURE, not file existence ─────────────────────────────────
# Until 2026-08-05 this metric was `sum(1 for f in seams if os.path.isfile(f))` over four
# hardcoded paths. That counted THE EXISTENCE OF A FILE, so a seam present but fail-OPEN
# counted exactly like a deny-closed one and the published adjective "deny-closed" rested
# on nothing this gate could see. The audit that replaced it found two of those four paths
# were not the enforcement point at all: modules/inferenceproxy/api.go is the module's
# admin CRUD surface ("This module DECIDES NOTHING about a live request" — its own
# doc.go:13) and connectors/a2a/a2a.go is the read-only Agent-Card SourceConnector.
#
# A seam counts only when the proof named in the census exists AND its assertion is still
# INSIDE that test function's body: a function gutted to a no-op keeps its name and stops
# counting. the hub's state script reads the SAME census, so the two counters cannot drift —
# duplicating a weak count in two places is how this defect survived.
ENFORCEMENT_CENSUS = os.environ.get("CPC_ENFORCEMENT_CENSUS") or "scripts/enforcement-seams.tsv"

# The assertion must sit inside a FAILING call. Without this a `t.Log`, a comment or a
# string constant carrying the same words counts as a proof — the census would be back to
# matching text that merely looks like enforcement.
ASSERT_CALL = re.compile(r"\bt\.(?:Fatal|Error)f?\(")

def proven_seams(census=None, root=""):
    census = census or ENFORCEMENT_CENSUS
    def under(p):
        return os.path.join(root, p) if root else p
    if not os.path.isfile(census):
        blind(f"UNVERIFIED check-public-counts: {census} is missing — the public deny-closed "
                 "claim has no census to rest on, and an unmeasurable claim is not a "
                 "passing one")
    n = rows = 0
    seen = {"seam": set(), "proof": set(), "label": set()}
    for i, raw in enumerate(rd(census).splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        parts = raw.split("\t")
        if len(parts) != 5 or not all(p.strip() for p in parts):
            blind(f"UNVERIFIED check-public-counts: {census}:{i} is malformed (want 5 non-empty "
                     f"tab-separated fields, got {len(parts)}) — a census row nobody can read "
                     "must not quietly fail to count")
        rows += 1
        seam, test, func, assertion, label = parts
        # Duplicates would let one seam be counted twice: three real enforcement points
        # could publish "four" by listing one of them again.
        for key, val in (("seam", seam), ("proof", (test, func)), ("label", label)):
            if val in seen[key]:
                sys.exit(f"FAIL check-public-counts: {census}:{i} repeats a {key} already "
                         f"counted ({val!r}) — one enforcement point is one row, or the "
                         "count inflates itself")
            seen[key].add(val)
        if not (os.path.isfile(under(seam)) and os.path.isfile(under(test))):
            continue
        # `func ... ^}` bounds the search to that ONE function body: the file merely
        # mentioning the assertion elsewhere is not a proof that this test makes it. The
        # closing brace is REQUIRED — a truncated function has no body to trust.
        body = re.search(func + r".*?^\}", rd(under(test)), re.S | re.M)
        if not body:
            continue
        for line in body.group(0).splitlines():
            if re.search(assertion, line) and ASSERT_CALL.search(line):
                n += 1
                break
    if rows == 0:
        blind(f"UNVERIFIED check-public-counts: {census} has no rows — an empty census is a "
                 "vanished measurement, not a zero")
    return n

# ── the published API surface: read from the CONTRACT, never transcribed ────────────────
# The reference index states this count TWICE per language and it had drifted to THREE
# values at once (contract 53, EN/ES 24, and five locales 24) with nothing watching. The
# derivation is not new: the hub's state script already emits (line 241) `openapi_paths` from this
# exact file. It was derived in one place and gated in NEITHER — which is the whole
# mechanism, because a number nobody compares is a number that only ever drifts.
#
# `web/openapi/openapi.json` is the CORE document, not "the API": the module routes
# (/v1/m/…) live in web/openapi/openapi.beta.json and the pages say so explicitly. Both
# are generated artefacts — Taskfile.yml regenerates them under `git diff --exit-code`,
# and core/api/openapi_router_parity_test.go proves every published operation exists in
# the router — so counting keys here is counting the shipped surface, not a hand list.
OPENAPI_CONTRACT = os.environ.get("CPC_OPENAPI_CONTRACT") or "web/openapi/openapi.json"

def openapi_paths(path=None):
    p = path or OPENAPI_CONTRACT
    if not os.path.isfile(p):
        blind(f"UNVERIFIED check-public-counts: {p} is missing — the published path count has no "
                 "contract to rest on, and an unmeasurable claim is not a passing one")
    try:
        spec = json.loads(rd(p))
    except json.JSONDecodeError as exc:
        blind(f"UNVERIFIED check-public-counts: {p} is not valid JSON ({exc}) — a contract nobody "
                 "can parse must not quietly stop being counted")
    paths = spec.get("paths")
    # Anti-vacuity, the same rule the enforcement census carries: a contract whose paths
    # object vanished or emptied would derive 0, and 0 sits under every floor — the gate
    # would go green over a claim with nothing behind it. An empty measurement is not a zero.
    if not isinstance(paths, dict) or not paths:
        blind(f"UNVERIFIED check-public-counts: {p} declares no paths — an empty contract is a "
                 "vanished measurement, not a count of zero")
    return len(paths)

# ── derivations (mirror the hub's state script; keep the two in step) ───────────────────
def derive():
    wire = rd("cmd/olivares/wire.go")
    modules = len(set(re.findall(r'"github\.com/olivaresai/olivares/modules/[a-z-]+"', wire)))
    conn_dirs = sorted(d for d in glob.glob("connectors/*/") if os.path.isdir(d))
    def has_go(d):
        # A connector counts as a Go integration because it SHIPS Go, not because it is
        # TESTED in Go. Test files and fixture trees are evidence about the code, never
        # the code itself: a directory holding only `foo_test.go` implements nothing, and
        # a `testdata/` tree can contain arbitrary sources that are inputs to a test.
        # Measured on 2026-08-04: 0 of the connector directories depend on that evidence
        # today, so this is a latent class being closed before it can be activated in
        # silence by whoever adds the next connector.
        for root, dirs, files in os.walk(d):
            dirs[:] = [x for x in dirs if x not in ("node_modules", "testdata")]
            if any(f.endswith(".go") and not f.endswith("_test.go") for f in files):
                return True
        return False
    nongo = sum(1 for d in conn_dirs if not has_go(d))
    fw = rd("modules/compliance/frameworks.go")
    catalogs = sum(1 for line in fw.splitlines() if line.startswith("\t{"))
    enforcement = proven_seams()
    kinds = len(set(re.findall(r'case "[a-z0-9_-]+"', rd("cmd/olivares/sources.go"))))
    # connector taxonomy (mirrors the ai-state.sh compile-time-assertion greps): the
    # connectors/README.md breakdown states every one of these, so every one is derived.
    plugins = len(glob.glob("connectors/*/cmd/*/main.go"))
    asserts = {"output": re.compile(r"_\s+sdk\.OutputConnector\s*="),
               "roster": re.compile(r"_\s+identitysource\.GraphProvider\s*="),
               "content": re.compile(r"_\s+contentsource\.Source\s*="),
               "content_live": re.compile(r"_\s+contentsource\.LiveSource\s*=")}
    hits = {k: set() for k in asserts}
    for d in conn_dirs:
        top = d.rstrip("/").split("/")[1]
        for root, dirs, files in os.walk(d):
            dirs[:] = [x for x in dirs if x != "node_modules"]
            for f in files:
                if not f.endswith(".go") or f.endswith("_test.go"):
                    continue
                src = rd(os.path.join(root, f))
                for k, pat in asserts.items():
                    if pat.search(src):
                        hits[k].add(top)
    LIB_DIRS = ["contentsource", "datasourceacl", "identitysource", "internal", "modelprovider",
                "modelrouter", "redact", "secretref", "siemsink", "threatfeed", "vectorindex", "voice"]
    libs = sum(1 for d in LIB_DIRS if os.path.isdir(f"connectors/{d}"))
    # SOURCE-SCAFFOLD gate (ported from ai-state.sh, which nothing in CI runs): every
    # sdk.SourceConnector must have an activation path — a buildInProcSource case, a
    # plugin binary, or a roster/output/content class. The allowlist may only SHRINK.
    src_pat = re.compile(r"_\s+sdk\.SourceConnector\s*=")
    source_dirs = set()
    for d in conn_dirs:
        top = d.rstrip("/").split("/")[1]
        for root, dirs2, files2 in os.walk(d):
            dirs2[:] = [x for x in dirs2 if x != "node_modules"]
            if any(src_pat.search(rd(os.path.join(root, f))) for f in files2
                   if f.endswith(".go") and not f.endswith("_test.go")):
                source_dirs.add(top)
                break
    src = rd("cmd/olivares/sources.go")
    imports = dict(re.findall(r'(?:(\w+)\s+)?"github\.com/olivaresai/olivares/connectors/([^"]+)"', src))
    inproc_dirs = set()
    fn = re.search(r"func buildInProcSource\(.*?\n\}", src, re.S)
    if fn:
        for alias in re.findall(r"return\s+([A-Za-z_][A-Za-z0-9_]*)\.New(?:Audit)?\(", fn.group(0)):
            for a, path_ in re.findall(r'(?:(\w+)\s+)?"github\.com/olivaresai/olivares/connectors/([^"]+)"', src):
                name = path_.rsplit("/", 1)[-1]
                if (a or name.replace("-", "")) == alias:
                    inproc_dirs.add(name)
    plugin_dirs = {p.split("/")[1] for p in glob.glob("connectors/*/cmd/*/main.go")}
    SCAFFOLD_ALLOW = {"a2a"}  # pending its source-face wiring; this set may only shrink
    scaffolds = source_dirs - inproc_dirs - plugin_dirs - hits["roster"] - hits["output"] - hits["content"]
    rogue = scaffolds - SCAFFOLD_ALLOW
    if rogue and not SELFTEST:
        sys.exit("FAIL check-public-counts: source connector(s) with NO activation path "
                 f"(not in the shrink-only allowlist): {sorted(rogue)} — wire them or do not merge")
    return (modules, len(conn_dirs), nongo, catalogs, enforcement, kinds, plugins,
            len(hits["output"]), len(hits["roster"]), len(hits["content"]), len(hits["content_live"]), libs,
            openapi_paths())

if not SELFTEST:
    (MODULES, CONN_DIRS, NONGO, CATALOGS, ENFORCEMENT, KINDS,
     PLUGINS, OUTPUT, ROSTER, CONTENT, CONTENT_LIVE, LIBS, PATHS) = derive()
else:
    # fixtures assume today's canon; the real run re-derives every time
    (MODULES, CONN_DIRS, NONGO, CATALOGS, ENFORCEMENT, KINDS,
     PLUGINS, OUTPUT, ROSTER, CONTENT, CONTENT_LIVE, LIBS, PATHS) = (30, 158, 1, 26, 4, 110,
                                                                     67, 22, 22, 11, 10, 12, 53)
INTEGRATIONS = CONN_DIRS - NONGO

EXPECT = {"modules": MODULES, "integrations": INTEGRATIONS,
          "catalogs": CATALOGS, "enforcement": ENFORCEMENT, "paths": PATHS}

# ── digit-with-noun matching, all locales, CJK counters normalized ──────────────────────
JOIN = r"(?:[\s\-]|の|個の|个|件の|项|項|つの|间的)*"
NOUN = {
    # "module dirs" is a DIFFERENT metric (31 incl. modules/example): the lookahead keeps a
    # correct "31 module dirs" claim from tripping the wired-module check. `path` and `route`
    # joined it on 2026-08-15, found by a fixture for the new paths metric: "24 module paths"
    # is a claim about the BETA CONTRACT's surface, and this pattern read it as a claim that
    # the product ships 24 modules — a true sentence about one metric failing the gate of
    # another, which is the same class the "dir" lookahead was written for.
    "modules": r"(?:product\s+)?(?:modules?\b(?!\s+(?:dir|path|route))|módulos|Produktmodule\w*|Module\b(?!\s+(?:dir|path|route))|Modulen\b|modules?\s+produit|モジュール|модул\w*|模块)",
    "integrations": r"(?:integrations?\b|integraciones|Integrationen|intégrations|統合|интеграц\w*|集成)",
    "catalogs": r"(?:(?:compliance[- ])?framework\s+catalogs?|catálogos\s+de\s+marcos|frameworks?\b|Framework-Katalog\w*|catalogues\s+de\s+cadres|フレームワークカタログ|каталог\w*(?:\s+фреймворков)?|фреймворк\w*|框架目录)",
    "enforcement": r"(?:deny-closed\s+)?(?:enforcement\s+points?|puntos\s+de\s+aplicación|Enforcement\s+Points?|points\s+d'application|エンフォースメントポイント|точк\w*\s+принуждени\w*|执行点)",
    # "core" is load-bearing in every language: it is what separates this document from the
    # beta module contract, which the same pages name three lines below. A pattern matching
    # a bare "paths" would also match the beta count and the two would silently swap.
    # `path stable core` is the SECOND wording the same claim ships in — the open-core page and
    # the reference index both say "N-path stable core contract". It was measured stale in EN
    # itself on 2026-08-16 while the two "N core paths" sites of the same file were already
    # correct: one page, one number, two spellings, and only one of them was being watched.
    # The other two odd forms («à N chemins», «контракт из N путей») were normalised to the
    # canonical noun in the same commit rather than pattern-matched, because a bare "N chemins"
    # or "N путей" would match unrelated prose and this gate must not fire on a true sentence.
    "paths": r"(?:core\s+paths?|paths?\s+core|Core-Paths?|chemins?\s+de\s+cœur|コアパス|базов\w*\s+пут\w*|条?核心路径|-?\s*path\s+stable\s+core)",
}
# The digit must stand alone: "v1 module" and "v1.26.0 module" are version strings, not
# catalog counts — hence the lookbehind. ASCII-only on purpose: \w would match CJK text,
# where digits legitimately follow a character with no space ("全部で156件の統合"). And a
# LOW count with these nouns is legitimate subset prose ("three modules make up X",
# "11 content sources"); the wrong-count trap only exists near the canonical magnitude,
# so each metric carries a floor.
DIGIT_UNIT = {m: re.compile(r"(?<![A-Za-z0-9.])(\d+)" + JOIN + NOUN[m]) for m in NOUN}
FLOOR = {"modules": 15, "integrations": 100, "catalogs": 10, "enforcement": 0, "paths": 20}

# The per-line waiver token, valid ONLY on the versioned allowlist below (audit trails
# that QUOTE dead values as history). Anywhere else the token itself is a failure.
WAIVER = "counts-gate: historical-quote"
RELEASE_CHECKLIST = os.environ.get("CPC_RELEASE_CHECKLIST", "")
REVIEW_NOTES = os.environ.get("CPC_REVIEW_NOTES", "")
DAY_D_RUNBOOK = os.environ.get("CPC_DAY_D_RUNBOOK", "")
BLOG_DRAFT = os.environ.get("CPC_BLOG_DRAFT", "")
LAUNCH_INDEX = os.environ.get("CPC_LAUNCH_INDEX", "")
AGENT_STATE = os.environ.get("CPC_AGENT_STATE", "")
WAIVER_ALLOWLIST = [(path, literal) for path, literal in (
    (RELEASE_CHECKLIST, 'more than 125'),
    (REVIEW_NOTES, '23 modules'),
    (DAY_D_RUNBOOK, 'more than 125'),
    (DAY_D_RUNBOOK, '41 console views'),
    (DAY_D_RUNBOOK, '28 · ~109'),
    (DAY_D_RUNBOOK, '97 bootstrapped / ~18 remaining'),
) if path]

def waiver_ok(path, line):
    return any(path.endswith(p) and lit in line for p, lit in WAIVER_ALLOWLIST)

def check_units(surface, path, text, metrics=("modules", "integrations", "catalogs", "enforcement", "paths")):
    for i, raw in enumerate(text.splitlines(), 1):
        line = norm(raw)
        if WAIVER in raw:
            if not waiver_ok(path, raw):
                fail(surface, f"{path}:{i} waiver token OUTSIDE the versioned allowlist — remove it or extend WAIVER_ALLOWLIST with path+literal+reason")
            continue
        for metric in metrics:
            for m in DIGIT_UNIT[metric].finditer(line):
                n = int(m.group(1))
                if n != EXPECT[metric] and n >= FLOOR[metric]:
                    fail(surface, f"{path}:{i} states {n} {metric} (measured: {EXPECT[metric]}) — «{m.group(0)}»")

# hedges: the counts are exact by owner ruling — every approximation resurrects a lie
HEDGES = [
    r"\b(?:more than|over|about|around|approximately|nearly|some|roughly)\s+\d+\s*integrations?",
    r"(?:más de|aproximadamente|alrededor de|unas|cerca de)\s+\d+\s*integraciones",
    r"(?:mehr als|etwa|rund|ungefähr|circa)\s+\d+\s*Integrationen",
    r"[Üü]ber\s+\d+\s*Integrationen(?!\s+hinweg)",
    r"(?:plus de|environ|autour de|près de|quelque)\s+\d+\s*intégrations",
    r"\d+\s*(?:を超える|以上の?)\s*統合", r"約\s*\d+\s*(?:件の)?統合",
    r"(?:более(?:\s+чем)?|около|порядка|примерно|свыше)\s+\d+\s*интеграц",
    r"(?:超过|约|大约|近)\s*\d+\s*项?集成", r"\d+\s*项以上集成",
    r"~\s?\d+\s*(?:integrations?|integraciones|Integrationen|intégrations|統合|интеграц\w*|集成)",
]

def check_hedges(surface, path, text):
    for i, raw in enumerate(text.splitlines(), 1):
        if WAIVER in raw and waiver_ok(path, raw):
            continue
        line = norm(raw)
        for h in HEDGES:
            if re.search(h, line):
                fail(surface, f"{path}:{i} hedged/approximate integration count — «{line.strip()[:80]}»")

# ── spelled-out numbers (EN/ES) for ALL FOUR metrics ────────────────────────────────────
EN_ONES = ["zero","one","two","three","four","five","six","seven","eight","nine","ten",
           "eleven","twelve","thirteen","fourteen","fifteen","sixteen","seventeen",
           "eighteen","nineteen"]
EN_TENS = ["","","twenty","thirty","forty","fifty","sixty","seventy","eighty","ninety"]
def en_words(n):
    if n < 20: return EN_ONES[n]
    if n < 100:
        return EN_TENS[n // 10] + ("-" + EN_ONES[n % 10] if n % 10 else "")
    if n < 1000:
        return r"(?:a|one) hundred" + (" and " + en_words(n % 100) if n % 100 else "")
    sys.exit(f"check-public-counts: extend en_words for {n}")

ES_BASE = {0:"cero",1:"uno",2:"dos",3:"tres",4:"cuatro",5:"cinco",6:"seis",7:"siete",
           8:"ocho",9:"nueve",10:"diez",11:"once",12:"doce",13:"trece",14:"catorce",
           15:"quince",16:"dieciséis",17:"diecisiete",18:"dieciocho",19:"diecinueve",
           20:"veinte",21:"veintiuno",22:"veintidós",23:"veintitrés",24:"veinticuatro",
           25:"veinticinco",26:"veintiséis",27:"veintisiete",28:"veintiocho",29:"veintinueve"}
ES_TENS = {30:"treinta",40:"cuarenta",50:"cincuenta",60:"sesenta",70:"setenta",
           80:"ochenta",90:"noventa"}
def es_words(n):
    if n in ES_BASE: return ES_BASE[n]
    if n < 100:
        t = (n // 10) * 10
        return ES_TENS[t] + (" y " + ES_BASE[n % 10] if n % 10 else "")
    if n < 200:
        return "ciento" + (" " + es_words(n % 100) if n % 100 else "")
    sys.exit(f"check-public-counts: extend es_words for {n}")

SPELLED_NOUNS = {  # metric -> (EN noun, ES noun)
    "modules": (r"modules", r"módulos"),
    "integrations": (r"integrations", r"integraciones"),
    "catalogs": (r"framework catalogs", r"catálogos de marcos"),
    "enforcement": (r"enforcement points", r"puntos de aplicación"),
}

def check_spelled(surface, path, text, require=(), require_langs=("en", "es")):
    """Forbid every neighbouring word-form (±8) of each metric's canonical value; require
    the canonical spelled form for the metrics in `require` (the VO-bearing files), in the
    languages the file actually carries (an .en. subtitle has no Spanish to demand)."""
    flat = " ".join(norm(text).split()).lower()
    for metric, (noun_en, noun_es) in SPELLED_NOUNS.items():
        canon = EXPECT[metric]
        for n in range(max(0, canon - 8), canon + 9):
            if n == canon:
                continue
            if re.search(en_words(n) + r"\s+" + noun_en, flat):
                fail(surface, f"{path} spells out '{en_words(n)} {noun_en}' (measured: {canon})")
            if re.search(es_words(n) + r"\s+" + noun_es, flat):
                fail(surface, f"{path} spells out '{es_words(n)} {noun_es}' (measured: {canon})")
    for metric in require:
        noun_en, noun_es = SPELLED_NOUNS[metric]
        canon = EXPECT[metric]
        if "en" in require_langs and not re.search(en_words(canon) + r"\s+" + noun_en, flat):
            fail(surface, f"{path} lacks the required spelled EN claim '{en_words(canon)} {noun_en}'")
        if "es" in require_langs and not re.search(es_words(canon) + r"\s+" + noun_es, flat):
            fail(surface, f"{path} lacks the required spelled ES claim '{es_words(canon)} {noun_es}'")

def require_digit_claim(surface, path, text, metric, minimum=1):
    flat = norm(text)
    pat = re.compile(str(EXPECT[metric]) + JOIN + NOUN[metric])
    found = len(pat.findall(flat))
    if found < minimum:
        fail(surface, f"{path} declares the {metric} count ({EXPECT[metric]}) in {found} place(s), "
                      f"required {minimum} — a claim that vanished is as false as a wrong one, and "
                      "if the phrasing changed, update this gate's noun patterns")

def require_nonempty(surface, path, text, floor):
    if len(text.strip()) < floor:
        fail(surface, f"{path} is empty or gutted ({len(text.strip())} chars < {floor}) — a vanished claim is as false as a wrong one")

def check_connectors_readme(text, path="connectors/README.md"):
    """The connector taxonomy breakdown ships with the Apache tree and once drifted a
    full release cycle behind the code (150 dirs, 6 scaffolds, 84/79 aliases, 9 content)
    with nothing watching — every figure it states is derived above and required here."""
    require_nonempty("connectors-readme", path, text, 1000)
    for needle, what in ((f"**{CONN_DIRS} connector directories**", "connector dir count"),
                         (f"**{INTEGRATIONS} containing Go code**", "with-Go split"),
                         (f"**{KINDS} unique kind aliases**", "in-proc kind count"),
                         (f"**{PLUGINS} binaries**", "plugin binary count"),
                         (f"output connectors (**{OUTPUT}**)", "output connector count"),
                         (f"identity-roster providers (**{ROSTER}**)", "roster provider count"),
                         (f"content sources (**{CONTENT}**", "content source count"),
                         (f"**{CONTENT_LIVE} live**", "live content source count"),
                         (f"**{LIBS} are shared contract/library", "contract/library count")):
        if needle not in text:
            fail("connectors-readme", f"{path} lacks '{needle}' ({what}) — refresh the breakdown from the derivations in this gate")
    check_units("connectors-readme", path, text)
    check_hedges("connectors-readme", path, text)

# ── sidebar translations: the key set must EQUAL the locale roster ──────────────────────
SIDEBAR_LOCALES = {"es", "de", "fr", "ja", "ru", "zh-CN"}

def check_sidebar_block(surface, sidebar_text):
    m = re.search(r'"Overview — the (\d+) modules": \{(.*?)\n  \}', sidebar_text, re.S)
    if not m:
        fail(surface, "sidebar-i18n.mjs: overview label block not found (did its EN key change?)")
        return
    if int(m.group(1)) != MODULES:
        fail(surface, f"sidebar-i18n.mjs overview key says {m.group(1)} modules (measured: {MODULES})")
    entries = dict(re.findall(r'"([a-zA-Z-]+)": "([^"]+)"', m.group(2)))
    if set(entries) != SIDEBAR_LOCALES:
        fail(surface, f"sidebar-i18n.mjs overview translations cover {sorted(entries)} — required exactly {sorted(SIDEBAR_LOCALES)}")
    for loc, label in entries.items():
        if str(MODULES) not in label:
            fail(surface, f"sidebar-i18n.mjs overview label for '{loc}' lacks the count {MODULES}")
    if re.search(r'"zh": ', sidebar_text):
        fail(surface, "sidebar-i18n.mjs uses \"zh\" keys — Starlight matches by BCP-47 lang (zh-CN); with \"zh\" the Chinese sidebar silently falls back to English")

# ── render-manifest verification (content hashes, not git timestamps) ───────────────────
def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

MANIFEST_REQUIRED = {  # deleting a manifest entry must not hide an output
    "render": ["design/launch-video/out/olivares-launch-reel.webm",
               "design/launch-video/out/olivares-launch-reel-9x16.webm"],
    "derive": ["design/launch-video/out/olivares-launch-reel.mp4",
               "design/launch-video/out/olivares-launch-reel-9x16.mp4",
               ".github/assets/olivares-reel.gif",
               "design/launch-video/out/olivares-console-tour-short.gif",
               "design/launch-video/out/cuts/olivares-cut-access-deny-169.mp4",
               "design/launch-video/out/cuts/olivares-cut-access-deny-916.mp4"],
    "subtitles": ["design/launch-video/out/olivares-launch-reel.en.srt",
                  "design/launch-video/out/olivares-launch-reel.es.srt",
                  "design/launch-video/out/olivares-launch-reel.en.vtt",
                  "design/launch-video/out/olivares-launch-reel.es.vtt"],
}

def inputs_digest(inputs):
    pairs = sorted(inputs.items())
    blob = json.dumps([[k, v] for k, v in pairs], separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(blob.encode()).hexdigest()

def check_manifest(surface, man, hash_fn):
    sections = man.get("sections", {})
    for section, required in MANIFEST_REQUIRED.items():
        sec = sections.get(section)
        if not sec:
            fail(surface, f"render-manifest.json lacks the '{section}' section — run its pipeline tool")
            continue
        for p, recorded in sec.get("inputs", {}).items():
            actual = hash_fn(p)
            if actual is None:
                fail(surface, f"manifest[{section}] input {p} is missing from the tree")
            elif actual != recorded:
                fail(surface, f"manifest[{section}] input {p} changed since the last render — re-run the pipeline")
        digest = inputs_digest(sec.get("inputs", {}))
        if digest != sec.get("inputsDigest"):
            fail(surface, f"manifest[{section}] inputsDigest is stale — re-run the pipeline tool")
        outputs = sec.get("outputs", {})
        for p in required:
            if p not in outputs:
                fail(surface, f"manifest[{section}] does not record required output {p}")
        for p, rec in outputs.items():
            actual = hash_fn(p)
            if actual is None:
                fail(surface, f"manifest[{section}] output {p} is missing from the tree")
            elif actual != rec.get("sha256"):
                fail(surface, f"manifest[{section}] output {p} does not match its recorded hash — regenerate it from the pipeline, never by hand")
            if rec.get("from") != digest:
                fail(surface, f"manifest[{section}] output {p} was derived from OLDER inputs — re-run the full {section} step")

# ── selftest: every trap this gate exists for must fail on a fixture ────────────────────
def selftest():
    def expect_red(name, fn):
        del failures[:]
        fn()
        if not failures:
            print(f"selftest FAIL: red fixture '{name}' passed the gate")
            sys.exit(1)
        print(f"selftest ok: {name} -> red ({failures[0][:90]}…)")
    def expect_green(name, fn):
        del failures[:]
        fn()
        if failures:
            print(f"selftest FAIL: green fixture '{name}' turned red: {failures[0]}")
            sys.exit(1)
        print(f"selftest ok: {name} -> green")

    expect_red("bold-wrapped wrong digit", lambda: check_units("t", "f.md", "**156** integrations here"))
    expect_red("CJK counter wrong digit", lambda: check_units("t", "f.md", "全部で156件の統合です"))
    expect_red("wrong enforcement digit", lambda: check_units("t", "f.md", "3 enforcement points"))
    expect_red("wrong catalog digit de", lambda: check_units("t", "f.md", "25 Framework-Kataloge"))
    # The core-path count, in the five wordings that were actually stale. Each locale gets
    # its own fixture because each has its own noun: a single EN case would have passed
    # while «24 条核心路径» shipped, which is precisely how these five survived.
    expect_red("wrong paths digit en", lambda: check_units("t", "f.md", "The contract describes **24 core paths**."))
    expect_red("wrong paths digit es", lambda: check_units("t", "f.md", "El contrato describe **24 paths core**."))
    expect_red("wrong paths digit de", lambda: check_units("t", "f.md", "Contract des Produkts (24 Core-Paths)"))
    expect_red("wrong paths digit fr", lambda: check_units("t", "f.md", "Le contrat décrit **24 chemins de cœur**."))
    expect_red("wrong paths digit ja", lambda: check_units("t", "f.md", "契約は **24 のコアパス**を記述する。"))
    expect_red("wrong paths digit ru", lambda: check_units("t", "f.md", "Контракт описывает **24 базовых пути**."))
    expect_red("wrong paths digit zh", lambda: check_units("t", "f.md", "该契约描述 **24 条核心路径**。"))
    # One site fixed and the other left behind — the failure mode this metric exists for.
    expect_red("paths claimed in only ONE of the two sites",
               lambda: require_digit_claim("t", "reference/index.md",
                                           "row: (53 core paths)\nprose: the contract describes 24 core paths",
                                           "paths", minimum=2))
    expect_red("spelled wrong integrations", lambda: check_spelled("t", "f.srt", "A hundred and fifty-six\nintegrations."))
    expect_red("spelled wrong modules es", lambda: check_spelled("t", "f.srt", "Veintinueve módulos."))
    expect_red("hedge about", lambda: check_hedges("t", "f.md", "about 157 integrations"))
    expect_red("hedge es", lambda: check_hedges("t", "f.md", "más de **135** integraciones"))
    expect_red("hedge ja", lambda: check_hedges("t", "f.md", "135を超える統合"))
    outside_waiver = BLOG_DRAFT or "fixtures/unpublished-blog.md"
    allowlisted_waiver = DAY_D_RUNBOOK or "fixtures/internal-day-d-runbook.md"
    if not DAY_D_RUNBOOK:
        WAIVER_ALLOWLIST.append((allowlisted_waiver, '41 console views'))
    expect_red("waiver outside allowlist", lambda: check_units("t", outside_waiver, f"28 modules <!-- {WAIVER} -->"))
    expect_red("gutted required page", lambda: require_nonempty("t", "what-is.md", "  ", 1000))
    expect_red("missing required claim", lambda: require_digit_claim("t", "f.md", "no numbers here at all", "integrations"))
    expect_red("missing spelled requirement", lambda: check_spelled("t", "f.srt", "no numbers", require=("modules",)))
    expect_red("connectors breakdown stale", lambda: check_connectors_readme(
        "x" * 1200 + " There are currently **150 connector directories** in the tree."))
    expect_red("sidebar locale deleted", lambda: check_sidebar_block("t",
        '"Overview — the 30 modules": {\n    "es": "los 30", "de": "die 30", "ja": "30個", "ru": "30", "zh-CN": "30 个"\n  }'))
    expect_red("sidebar zh key", lambda: check_sidebar_block("t",
        '"Overview — the 30 modules": {\n    "es": "los 30", "de": "die 30", "fr": "les 30", "ja": "30個", "ru": "30", "zh-CN": "30 个"\n  }\n  "Other": { "zh": "x" }'))

    good_inputs = {"design/launch-video/reel.html": "aa"}
    digest = inputs_digest(good_inputs)
    def man(out_sha="bb", frm=None):
        outs = {p: {"sha256": out_sha, "from": frm or digest} for req in MANIFEST_REQUIRED.values() for p in req}
        return {"sections": {s: {"inputs": good_inputs, "inputsDigest": digest,
                                 "outputs": {p: outs[p] for p in MANIFEST_REQUIRED[s]}}
                             for s in MANIFEST_REQUIRED}}
    hashes = {"design/launch-video/reel.html": "aa"}
    hash_ok = lambda p: hashes.get(p, "bb")
    expect_green("manifest coherent", lambda: check_manifest("t", man(), hash_ok))
    expect_red("manifest tampered output", lambda: check_manifest("t", man(out_sha="TAMPERED"), hash_ok))
    expect_red("manifest stale derivation", lambda: check_manifest("t", man(frm="olddigest"), hash_ok))
    expect_red("manifest input drift", lambda: check_manifest("t", man(), lambda p: "CHANGED" if p.endswith("reel.html") else "bb"))

    expect_green("exact digits", lambda: check_units("t", "f.md", "**30 modules**, 157 integrations, 26 framework catalogs, 4 deny-closed enforcement points"))
    expect_green("ja counters", lambda: check_units("t", "f.md", "30個のモジュール、157件の統合、26 のフレームワークカタログ"))
    expect_green("spelled canon", lambda: check_spelled("t", "f.srt",
        "Thirty modules. A hundred and fifty-seven integrations. Twenty-six framework catalogs. Treinta módulos. Ciento cincuenta y siete integraciones. Veintiséis catálogos de marcos."))
    expect_green("allowlisted waiver", lambda: check_units("t", allowlisted_waiver, f"the old '41 console views' figure <!-- {WAIVER} -->"))
    expect_green("module dirs metric untouched", lambda: check_units("t", "f.md", "31 module dirs"))
    expect_green("paths canon, all seven nouns", lambda: check_units("t", "f.md",
        "(53 core paths) · **53 paths core** · (53 Core-Paths) · **53 chemins de cœur** · "
        "**53 のコアパス** · **53 базовых пути** · **53 条核心路径**"))
    expect_green("paths claimed at BOTH sites", lambda: require_digit_claim(
        "t", "reference/index.md", "row: (53 core paths)\nprose: describes **53 core paths**.",
        "paths", minimum=2))
    # Subset prose below the floor stays legal: "three core paths handle auth" is a true
    # sentence about a subset, and only the canonical magnitude carries the wrong-count trap.
    expect_green("low paths count is subset prose", lambda: check_units("t", "f.md", "three of them, 3 core paths, handle auth"))
    # The beta module contract is a DIFFERENT document with its own count: matching a bare
    # "paths" would have made these two collide and swap without a word.
    expect_green("beta module paths are not this metric", lambda: check_units("t", "f.md", "the beta document publishes 24 module paths"))

    # ── the enforcement census, hermetically ────────────────────────────────────────────
    # These fixtures exist because the counter that replaced `os.path.isfile` was itself
    # only ever seen returning 4. A gate whose new half nothing exercises is a gate on
    # trust. `proven_seams` REFUSES (sys.exit) rather than appending to `failures`, so it
    # needs its own two expectations.
    import shutil, tempfile
    def expect_count(name, census_body, test_body, want):
        d = tempfile.mkdtemp(prefix="cpc-census-")
        try:
            os.makedirs(os.path.join(d, "pkg"))
            for stem in ("seam.go", "seam_test.go"):
                with open(os.path.join(d, "pkg", stem), "w", encoding="utf-8") as fh:
                    fh.write(test_body if stem.endswith("_test.go") else "package pkg\n")
            cpath = os.path.join(d, "census.tsv")
            with open(cpath, "w", encoding="utf-8") as fh:
                fh.write(census_body)
            got = proven_seams(cpath, d)
            if got != want:
                print(f"selftest FAIL: census fixture '{name}' counted {got}, want {want}")
                sys.exit(1)
            print(f"selftest ok: census {name} -> {got}")
        finally:
            shutil.rmtree(d, ignore_errors=True)
    def expect_refusal(name, census_body):
        d = tempfile.mkdtemp(prefix="cpc-census-")
        try:
            cpath = os.path.join(d, "census.tsv")
            with open(cpath, "w", encoding="utf-8") as fh:
                fh.write(census_body)
            try:
                proven_seams(cpath, d)
            except SystemExit:
                print(f"selftest ok: census {name} -> refused")
                return
            print(f"selftest FAIL: census fixture '{name}' was accepted; it must refuse")
            sys.exit(1)
        finally:
            shutil.rmtree(d, ignore_errors=True)

    ROW = "pkg/seam.go\tpkg/seam_test.go\t^func TestX\\(\tmust deny\tX"
    PROOF = 'package pkg\n\nfunc TestX(t *testing.T) {\n\tif ok {\n\t\tt.Fatal("must deny")\n\t}\n}\n'
    expect_count("intact proof counts", ROW + "\n", PROOF, 1)
    expect_count("no trailing newline still counts", ROW, PROOF, 1)
    expect_count("name kept, body gutted", ROW + "\n",
                 'package pkg\n\nfunc TestX(t *testing.T) {\n\t_ = t\n}\n', 0)
    expect_count("assertion outside the named function", ROW + "\n",
                 'package pkg\n\nfunc TestX(t *testing.T) {\n}\n\nfunc TestY(t *testing.T) {\n\tt.Fatal("must deny")\n}\n', 0)
    expect_count("t.Log is not an assertion", ROW + "\n",
                 'package pkg\n\nfunc TestX(t *testing.T) {\n\tt.Log("must deny")\n}\n', 0)
    expect_count("truncated function has no body to trust", ROW + "\n",
                 'package pkg\n\nfunc TestX(t *testing.T) {\n\tt.Fatal("must deny")\n', 0)
    expect_count("longer name is not this proof", "pkg/seam.go\tpkg/seam_test.go\t^func TestX\\(\tmust deny\tX\n",
                 'package pkg\n\nfunc TestXExtra(t *testing.T) {\n\tt.Fatal("must deny")\n}\n', 0)
    expect_refusal("empty census", "# only a comment\n")
    expect_refusal("four fields", "a\tb\tc\td\n")
    expect_refusal("six fields (a stray tab)", "a\tb\tc\td\te\tf\n")
    expect_refusal("trailing tab", "a\tb\tc\td\te\t\n")
    expect_refusal("duplicate seam", ROW + "\n" + "pkg/seam.go\tother_test.go\t^func TestZ\\(\tz\tZ\n")
    expect_refusal("duplicate proof", ROW + "\n" + "pkg/other.go\tpkg/seam_test.go\t^func TestX\\(\tmust deny\tOther\n")
    # ── the contract derivation: it must REFUSE, never answer zero ──────────────────────
    # A vanished or emptied `paths` object derives 0, and 0 is under every floor — the gate
    # would print OK over a claim with nothing behind it. Same shape as the census refusals
    # above, and it needs its own fixtures for the same reason: `openapi_paths` exits rather
    # than appending to `failures`, so no red fixture elsewhere can reach it.
    def expect_paths(name, body, want):
        d = tempfile.mkdtemp(prefix="cpc-openapi-")
        try:
            p = os.path.join(d, "openapi.json")
            with open(p, "w", encoding="utf-8") as fh:
                fh.write(body)
            got = openapi_paths(p)
            if got != want:
                print(f"selftest FAIL: contract fixture '{name}' counted {got}, want {want}")
                sys.exit(1)
            print(f"selftest ok: contract {name} -> {got}")
        finally:
            shutil.rmtree(d, ignore_errors=True)
    def expect_paths_refusal(name, body):
        d = tempfile.mkdtemp(prefix="cpc-openapi-")
        try:
            p = os.path.join(d, "openapi.json")
            if body is not None:
                with open(p, "w", encoding="utf-8") as fh:
                    fh.write(body)
            try:
                openapi_paths(p)
            except SystemExit:
                print(f"selftest ok: contract {name} -> refused")
                return
            print(f"selftest FAIL: contract fixture '{name}' was accepted; it must refuse")
            sys.exit(1)
        finally:
            shutil.rmtree(d, ignore_errors=True)

    expect_paths("two paths count two", '{"paths": {"/a": {"get": {}}, "/b": {"post": {}}}}', 2)
    expect_paths_refusal("empty paths object", '{"paths": {}}')
    expect_paths_refusal("paths key absent", '{"info": {"title": "x"}}')
    expect_paths_refusal("paths is a list, not a map", '{"paths": ["/a", "/b"]}')
    expect_paths_refusal("unparseable contract", '{"paths": {')
    expect_paths_refusal("contract file missing", None)

    print("selftest OK — every red case is red, every green case is green")
    sys.exit(0)

if SELFTEST:
    selftest()

print(f"derived: modules={MODULES} integrations={INTEGRATIONS} ({CONN_DIRS} dirs − {NONGO} non-Go) "
      f"paths={PATHS} "
      f"catalogs={CATALOGS} enforcement={ENFORCEMENT} kinds={KINDS}")

# ═══ surface 1: README family (7 languages, hardcoded digits, identical structure) ══════
README_FAMILY = ["README.md", "README.es.md", "README.de.md", "README.fr.md",
                 "README.ja.md", "README.ru.md", "README.zh.md"]
# The spelled enforcement claim, per locale, REQUIRED — and keyed by the MEASURED count,
# not hardcoded to four. This table used to be seven literals saying "four", which meant
# the seven front pages spell the number as a WORD, check_units only matches (\d+), and so
# NOTHING here compared the published claim against the measurement: had the count fallen
# to three, this gate would have gone on demanding "four deny-closed enforcement points"
# and the READMEs would have shipped green stating a number the tree no longer supported.
# An honest counter behind a literal table is still a coincidence.
#
# Only counts with a vetted sentence in all seven languages are listed. 1 and 0 are absent
# ON PURPOSE and fail loudly below: they cross a grammatical-number boundary (EN point/
# points, ES un/dos, DE Point/Points, FR fermé/fermés, RU одна/две + точка/точки), and the
# copy needs a human, not a numeral substituted into a plural sentence. A count nobody has
# written a correct sentence for must not ship.
# Each locale lists EVERY wording the claim is made in, because each front page states it
# TWICE — once in the "Govern & enforce it" bullet and once in the capability table — and
# fr/ru word the two differently (measured: the other five reuse one wording). ALL forms
# for the measured count must be present, and NO form of any other count may appear. A
# single required phrase would have let a session update the bullet, leave a stale "quatre"
# in the table row, and still pass: the gate would have found what it was looking for and
# stopped looking. It is the same defect as counting a file and calling it a posture.
ENFORCEMENT_PHRASE = {
    "README.md": {
        4: [r"four deny-closed enforcement points"],
        3: [r"three deny-closed enforcement points"],
        2: [r"two deny-closed enforcement points"],
    },
    "README.es.md": {
        4: [r"cuatro puntos de aplicación deny-closed"],
        3: [r"tres puntos de aplicación deny-closed"],
        2: [r"dos puntos de aplicación deny-closed"],
    },
    "README.de.md": {
        4: [r"vier deny-closed Enforcement Points"],
        3: [r"drei deny-closed Enforcement Points"],
        2: [r"zwei deny-closed Enforcement Points"],
    },
    "README.fr.md": {
        4: [r"quatre points d'application fermés par défaut \(deny-closed\)",
            r"quatre points d'application deny-closed"],
        3: [r"trois points d'application fermés par défaut \(deny-closed\)",
            r"trois points d'application deny-closed"],
        2: [r"deux points d'application fermés par défaut \(deny-closed\)",
            r"deux points d'application deny-closed"],
    },
    "README.ja.md": {
        4: [r"4\s*つの\s*deny-closed\s*エンフォースメントポイント"],
        3: [r"3\s*つの\s*deny-closed\s*エンフォースメントポイント"],
        2: [r"2\s*つの\s*deny-closed\s*エンフォースメントポイント"],
    },
    "README.ru.md": {
        4: [r"четыре закрытые по умолчанию \(deny-closed\) точки принуждения",
            r"четыре deny-closed точки принуждения"],
        3: [r"три закрытые по умолчанию \(deny-closed\) точки принуждения",
            r"три deny-closed точки принуждения"],
        2: [r"две закрытые по умолчанию \(deny-closed\) точки принуждения",
            r"две deny-closed точки принуждения"],
    },
    "README.zh.md": {
        4: [r"四个\s*deny-closed\s*执行点"],
        3: [r"三个\s*deny-closed\s*执行点"],
        2: [r"两个\s*deny-closed\s*执行点"],
    },
}
# ── C10-06: la cifra que vive DENTRO de un SVG ya renderizado ────────────────────────
#
# ⛔ POR QUÉ ESTE BLOQUE EXISTE. «FRAMEWORKS · 8 MAPPED» va rotulado dentro de tres diagramas
#    que salen en el material de lanzamiento, y hasta hoy **ningún gate lo miraba**: este guion
#    escaneaba los siete README y nada más. Una cifra pública dentro de una imagen es la que más
#    envejece, porque no aparece en ningún `grep` de prosa y nadie la relee al cambiar el código.
#
# ⚠ Y ESTE GATE NO ADJUDICA: SE NIEGA. El catálogo del motor declara CATALOGS marcos
#    (`modules/compliance/frameworks.go`, entradas de primer nivel); el rótulo dice otra cosa.
#    **No sé cuál de las dos preguntas contesta el rótulo** — «marcos declarados» y «marcos con
#    mapeo de controles completo» son cifras distintas y las dos son legítimas. Elegir una aquí
#    sería fabricar un empate, que es el defecto que esta campaña ya pagó una vez. Así que el
#    gate enseña las dos y para; decide una persona, y lo que decida se escribe.
FRAMEWORK_DIAGRAMS = [
    "design/launch-video/assets/diagrams/04-activity-to-evidence.svg",
    "design/logo-olivaresai-01-resources/06-OUTPUT/P04-diagrams/04-activity-to-evidence-dark.svg",
    "design/logo-olivaresai-01-resources/06-OUTPUT/P04-diagrams/04-activity-to-evidence-light.svg",
]
FRAMEWORK_LABEL = re.compile(r"FRAMEWORKS\s*[·.\u00b7]\s*(\d+)\s*MAPPED", re.I)

def check_framework_diagrams(paths=None, waiver=None):
    """El rótulo de marcos de los diagramas, contrastado con el catálogo derivado.

    `waiver` permite declarar POR ESCRITO que el rótulo mide otra cosa; mientras no exista, una
    discrepancia es roja. Un waiver vacío no vale: tiene que decir QUÉ mide el rótulo."""
    paths = FRAMEWORK_DIAGRAMS if paths is None else paths
    presentes = [p for p in paths if os.path.isfile(p)]
    if not presentes:
        # Los diagramas son material de diseño y pueden no estar en un árbol exportado. Que no
        # estén NO es un verde: es que aquí no se puede mirar, y se dice.
        print("check-public-counts: (diagramas de marcos ausentes en este árbol — no medidos)")
        return
    for p in presentes:
        text = rd(p)
        m = FRAMEWORK_LABEL.search(text)
        if not m:
            fail("diagrams", f"{p} ya no rotula «FRAMEWORKS · N MAPPED» — una cifra pública que "
                             "desaparece es tan falsa como una equivocada; si el diagrama cambió, "
                             "actualiza FRAMEWORK_LABEL en este gate")
            continue
        rotulado = int(m.group(1))
        if waiver:
            # Un waiver NO silencia: se IMPRIME en cada corrida, con su motivo. Un permiso que
            # nadie vuelve a leer es como no tener gate, sólo que con la conciencia tranquila.
            print(f"check-public-counts: ⚠ diagrama {os.path.basename(p)} rotula {rotulado} "
                  f"marcos, con waiver declarado — {waiver}")
            continue
        if rotulado != CATALOGS:
            fail("diagrams", f"{p} rotula {rotulado} marcos y el catálogo del motor declara "
                             f"{CATALOGS} (modules/compliance/frameworks.go, entradas de primer "
                             f"nivel). NO decido cuál vale: «declarados» y «con mapeo completo» "
                             f"son preguntas distintas. Corrige el diagrama, o declara por escrito "
                             f"qué mide el rótulo y pásalo como waiver a check_framework_diagrams.")

# ⛔ WAIVER DECLARADO, con su medición, porque la cifra buena es una decisión de PRODUCTO.
#
# RE-MEDIDO el 2026-08-17 sobre `modules/compliance/frameworks.go`, y la medida ANTERIOR de este
# mismo comentario no se sostiene. Decía «23 marcos con controles mapeados; los otros 3 declarados y
# sin mapear», y **ninguna definición natural da 23**. Contando por bloques emparejados con llaves:
#
#     26  DECLARADOS  (entradas de primer nivel — es lo que deriva este gate como CATALOGS)
#     26  con AL MENOS un control mapeado  ⇒ NINGÚN marco está sin mapear
#     18  con TODOS sus controles mapeados
#      8  con algún control sin `Capabilities`, y son 17 controles en total:
#         nist_ai_rmf 1/14 · iso_42001 2/13 · nist_ai_600_1 3/12 · cisa_ai_data_security 1/10
#         csa_aicm 5/18 · llm_top10 1/10 · pci_dss_401_ai 3/11 · ferpa 1/7
#
# ⛔ Importa porque el trabajo que colgaba de aquel 23 —«completar los tres mapeos»— NO EXISTE: no
#    hay tres marcos sin mapear. Lo que hay son 17 CONTROLES sin capacidades repartidos en ocho
#    marcos, que es otra tarea y de otro tamaño.
#
# El rótulo pasa de 8 a 26, que es la cifra que TODAS las demás superficies públicas ya declaran y
# este gate verifica. Con eso el bloque pasa por sus propios méritos y el waiver sobra.
#
# ⚠ RESIDUO DECLARADO, y es de producto: la palabra del rótulo es MAPPED, y en su lectura estricta
#   —«marcos con TODO mapeado»— la cifra sería 18, no 26. 26 es «declarados/con mapeo». Las dos son
#   defendibles y la elección es una afirmación pública que no decide un gate ni esta sesión: queda
#   escalada con esta medida, que es mejor que la que la escaló la primera vez.
check_framework_diagrams()

for f in README_FAMILY:
    if not os.path.isfile(f):
        fail("readme", f"{f} missing — the 7-language README family is a public invariant")
        continue
    text = rd(f)
    require_nonempty("readme", f, text, 4000)
    check_units("readme", f, text)
    check_hedges("readme", f, text)
    for metric in ("modules", "integrations", "catalogs"):
        require_digit_claim("readme", f, text, metric)
    vetted = ENFORCEMENT_PHRASE[f].get(ENFORCEMENT)
    flat_readme = norm(text)
    if vetted is None:
        fail("readme", f"{f}: the census proves {ENFORCEMENT} enforcement seams and no vetted "
                       f"sentence exists for that count — write the copy in all seven languages "
                       f"and add it to ENFORCEMENT_PHRASE; do not substitute a numeral")
    else:
        for form in vetted:
            if not re.search(form, flat_readme):
                fail("readme", f"{f} does not state the measured enforcement count ({ENFORCEMENT}) "
                               f"in the wording «{form}» — the census proves {ENFORCEMENT} seams, "
                               f"so every place this page makes the claim must say {ENFORCEMENT}")
        # Every page makes the claim TWICE — the "Govern & enforce it" bullet and the
        # "Policy & enforcement" table row (measured across all seven). Requiring each
        # wording only ONCE would let a page keep a correct first mention and carry a
        # second one saying something else: the gate would find what it looked for and
        # stop looking, which is the same mistake as counting a file and calling it a
        # posture. Five locales reuse one wording for both sites, so count occurrences.
        sites = sum(len(re.findall(form, flat_readme)) for form in vetted)
        if sites < 2:
            fail("readme", f"{f} states the enforcement claim in {sites} place(s), expected the "
                           f"bullet AND the capability-table row — a mention that vanished is as "
                           f"wrong as one that lies, and a second mention nobody checks is worse")
    for other, forms in ENFORCEMENT_PHRASE[f].items():
        if other == ENFORCEMENT:
            continue
        for form in forms:
            if re.search(form, flat_readme):
                fail("readme", f"{f} still claims {other} enforcement points somewhere «{form}» "
                               f"while the census proves {ENFORCEMENT} — a stale second mention is "
                               f"as false as a stale first one")

# ═══ surface 2: org profile (ships with the public org) ═════════════════════════════════
ORG = ".github/org-profile/README.md"
if not os.path.isfile(ORG):
    fail("org-profile", f"{ORG} missing — the org profile is a public surface")
else:
    t = rd(ORG)
    require_nonempty("org-profile", ORG, t, 1000)
    check_units("org-profile", ORG, t)
    check_hedges("org-profile", ORG, t)
    for metric in ("modules", "integrations", "catalogs"):
        require_digit_claim("org-profile", ORG, t, metric)

# ═══ surface 3: docs-site — FULL live tree, dated roots excluded explicitly ═════════════
DOCS_ROOT = "docs-site/src/content/docs"
EXCLUDED_SEGMENTS = ("/2026-06/", "/adr/")
live_docs = []
for root, dirs, files in os.walk(DOCS_ROOT):
    for name in files:
        if not name.endswith((".md", ".mdx")):
            continue
        p = os.path.join(root, name).replace(os.sep, "/")
        if any(seg in p + "/" for seg in EXCLUDED_SEGMENTS):
            continue
        live_docs.append(p)
for p in sorted(live_docs):
    t = rd(p)
    check_units("docs-site", p, t)
    check_hedges("docs-site", p, t)

LOCALES = ["", "es/", "de/", "fr/", "ja/", "ru/", "zh/"]
for loc in LOCALES:
    ov = f"{DOCS_ROOT}/{loc}reference/modules/overview.md"
    if not os.path.isfile(ov):
        fail("docs-site", f"{ov} missing")
        continue
    rows = sum(1 for line in rd(ov).splitlines() if re.match(r"^\| \[", line))
    if rows != MODULES:
        fail("docs-site", f"{ov} lists {rows} module rows in the live catalog (wired: {MODULES})")
    wi = f"{DOCS_ROOT}/{loc}start/what-is-olivares-ai.md"
    if not os.path.isfile(wi):
        fail("docs-site", f"{wi} missing")
    else:
        require_nonempty("docs-site", wi, rd(wi), 2000)
        require_digit_claim("docs-site", wi, rd(wi), "integrations")
    # The API reference index states the core-path count TWICE — the capability-table row
    # and the prose sentence under it — in all seven languages. Requiring it ONCE would let
    # a session fix the sentence, leave a stale row above it, and pass: the gate would find
    # what it was looking for and stop looking. That is not hypothetical here — it is the
    # exact shape the drift had on 2026-08-15, when EN and ES were corrected at both sites
    # and de/fr/ja/ru/zh kept 24 at both, while the contract had been at 53 since July.
    ri = f"{DOCS_ROOT}/{loc}reference/index.md"
    if not os.path.isfile(ri):
        fail("docs-site", f"{ri} missing — the API reference index is a public surface in every locale")
    else:
        require_nonempty("docs-site", ri, rd(ri), 2000)
        require_digit_claim("docs-site", ri, rd(ri), "paths", minimum=2)
    eu = f"{DOCS_ROOT}/{loc}explanation/eu-ai-act-evidence.md"
    if not os.path.isfile(eu):
        fail("docs-site", f"{eu} missing")
    else:
        require_nonempty("docs-site", eu, rd(eu), 2000)
        require_digit_claim("docs-site", eu, rd(eu), "catalogs")

astro = rd("docs-site/astro.config.mjs")
if f"Overview — the {MODULES} modules" not in astro:
    fail("docs-site", f"astro.config.mjs sidebar label does not say 'the {MODULES} modules'")
check_sidebar_block("docs-site", rd("docs-site/src/sidebar-i18n.mjs"))

# ═══ surface 4: docs/trust + STATE.md + AGENTS.md ═══════════════════════════════════════
TRUST_REQUIRED = ["docs/trust/README.md", "docs/trust/questionnaire-answer-bank.md",
                  "docs/trust/i18n-roadmap.md", "docs/trust/reference-architecture.md",
                  "docs/trust/one-pager.md"]
for p in TRUST_REQUIRED:
    if not os.path.isfile(p):
        fail("trust", f"{p} missing — the trust package is a public evaluation surface")
trust_files = sorted(glob.glob("docs/trust/*.md"))
if not trust_files:
    fail("trust", "docs/trust/ has no markdown at all")
for p in trust_files:
    t = rd(p)
    check_units("trust", p, t)
    check_hedges("trust", p, t)
    for i, line in enumerate(t.splitlines(), 1):
        if re.search(r"\b1[47][ -]framework", line) or re.search(r"\b1[47] frameworks", line):
            fail("trust", f"{p}:{i} pre-26 framework-catalog count — «{line.strip()[:80]}»")
if os.path.isfile("docs/trust/reference-architecture.md"):
    if not re.search(rf"core \+ {MODULES} modules", rd("docs/trust/reference-architecture.md")):
        fail("trust", f"reference-architecture.md diagram no longer says 'core + {MODULES} modules'")

if AGENT_STATE:
    t = rd(AGENT_STATE)
    for needle, what in ((f"**{MODULES} wired**", "wired module count"),
                         (f"**{CONN_DIRS}** ({INTEGRATIONS} with Go code)", "connector dir split"),
                         (f"| {KINDS} |", "in-proc kind count"),
                         (f"**{CATALOGS}**", "compliance framework count"),
                         (f"| Deny-closed enforcement points | **{ENFORCEMENT}** |", "enforcement row")):
        if needle not in t:
            fail("state", f"{AGENT_STATE} lacks '{needle}' ({what}) — regenerate the table from the derivations in this gate")
elif os.path.isdir("docs/ai-context"):
    fail("state", "agent state snapshot missing")
else:
    # the curated public export drops docs/ai-context wholesale — absence of the DIR is
    # the curation working; absence of the FILE with the dir present is a real hole
    curated_absence("state", "docs/ai-context", "docs/ai-context (absent: curated export)")

if os.path.isfile("AGENTS.md"):
    check_units("agents-md", "AGENTS.md", rd("AGENTS.md"), ("modules",))

if not os.path.isfile("ARCHITECTURE.md"):
    fail("architecture", "ARCHITECTURE.md missing — the public architecture overview")
else:
    check_units("architecture", "ARCHITECTURE.md", rd("ARCHITECTURE.md"))
    check_hedges("architecture", "ARCHITECTURE.md", rd("ARCHITECTURE.md"))

if os.path.isfile("connectors/README.md"):
    check_connectors_readme(rd("connectors/README.md"))
else:
    fail("connectors-readme", "connectors/README.md missing — the Apache tree's front door")

# ═══ surface 5: docs/launch (blocked from the public export; hub-only) ══════════════════
if os.path.isdir("docs/launch"):
    LAUNCH_FORBIDDEN = [r"\b28 modules", r"28-module", r"\b29 modules", r"~\s?109", r"roughly 109",
                        r"\b97 (?:of|are|wired|bootstrapped|connectors)", r"~\s?18\b",
                        r"1[47]-framework", r"\b1[47] (?:framework|compliance)",
                        r"\b25 framework", r"~\s?140", r"more than 12[0-9]", r"more than 13[0-9]",
                        r"\d+ console views",
                        r"(?:each|every)\b[^.\n]{0,60}wired activation path"]
    for p in sorted(glob.glob("docs/launch/*.md")):
        t = rd(p)
        for i, raw in enumerate(t.splitlines(), 1):
            if WAIVER in raw:
                continue  # allowlist validity is enforced inside check_units
            line = norm(raw)
            for fb in LAUNCH_FORBIDDEN:
                if re.search(fb, line):
                    fail("launch", f"{p}:{i} stale marker /{fb}/ — «{line.strip()[:80]}»")
        check_units("launch", p, t)
        check_hedges("launch", p, t)
    lr = rd(LAUNCH_INDEX) if LAUNCH_INDEX else ""
    if not lr:
        fail("launch", "launch package index missing")
    else:
        for metric in ("modules", "integrations", "catalogs", "enforcement"):
            require_digit_claim("launch", LAUNCH_INDEX, lr, metric)
else:
    curated_absence("launch", "docs/launch", "docs/launch (absent: curated export)")

# ═══ surface 6: the launch video family — sources AND rendered masters ══════════════════
if os.path.isdir("design/launch-video"):
    reel = rd("design/launch-video/reel.html")
    m = re.search(r"const data = \[(.*?)\];", reel)
    if not m:
        fail("video", "reel.html: scene-13 data literal not found (did the scene change? update this gate)")
    else:
        pairs = re.findall(r'\["([^"]+)", "([^"]+)"\]', m.group(1))
        want = {"modules": str(MODULES), "integrations": str(INTEGRATIONS),
                "framework catalogs": str(CATALOGS), "enforcement points": str(ENFORCEMENT)}
        got = {label: value for value, label in pairs}
        for label, value in want.items():
            if got.get(label) != value:
                fail("video", f"reel.html scene 13 shows {got.get(label)!r} {label} (measured: {value})")
    tuple_re = rf"{MODULES} modules · {INTEGRATIONS} integrations · {CATALOGS} framework catalogs · {ENFORCEMENT} enforcement points"
    if not re.search(tuple_re, rd("design/launch-video/SCRIPT.md")):
        fail("video", f"SCRIPT.md lacks the canonical tuple «{tuple_re}»")
    if f"({MODULES} / {INTEGRATIONS} / {CATALOGS} / {ENFORCEMENT}" not in rd("design/launch-video/README.md"):
        fail("video", f"launch-video/README.md honesty rule is not ({MODULES} / {INTEGRATIONS} / {CATALOGS} / {ENFORCEMENT}, …)")
    VO_FILES = ["design/launch-video/PROMPTS.md", "design/launch-video/subtitles.cjs",
                *sorted(glob.glob("design/launch-video/out/*.srt")),
                *sorted(glob.glob("design/launch-video/out/*.vtt"))]
    if len(VO_FILES) < 6:
        fail("video", f"expected PROMPTS + subtitles.cjs + 4 srt/vtt, found {len(VO_FILES)} — a missing subtitle file hides a claim")
    for p in VO_FILES:
        subtitle = p.endswith((".srt", ".vtt"))
        langs = ("en",) if ".en." in p else ("es",) if ".es." in p else ("en", "es")
        check_spelled("video", p, rd(p),
                      require=("modules", "integrations", "catalogs") if subtitle else (),
                      require_langs=langs)
    check_spelled("video", "design/launch-video/SCRIPT.md", rd("design/launch-video/SCRIPT.md"))

    MF = "design/launch-video/out/render-manifest.json"
    if not os.path.isfile(MF):
        fail("video", f"{MF} missing — re-run render.cjs, derive.cjs and subtitles.cjs so provenance is recorded")
    else:
        check_manifest("video", json.loads(rd(MF)),
                       lambda p: sha256_file(p) if os.path.isfile(p) else None)
else:
    curated_absence("video", "design/launch-video", "design/launch-video (absent: curated export)")

# ═══ surface 7: the assets ledger — its LATEST counts declaration is live ═══════════════
LEDGER = "design/ASSETS-LEDGER.md"
if os.path.isfile(LEDGER):
    decls = re.findall(r"\*\*\d+ modules? · [^*]*\*\*", rd(LEDGER))
    if not decls:
        fail("ledger", f"{LEDGER}: no counts declaration found")
    else:
        last = re.sub(r"\s+", " ", decls[-1])
        for needle in (f"{MODULES} modules", f"{INTEGRATIONS} integrations",
                       f"{CATALOGS} framework", f"{ENFORCEMENT} enforcement points"):
            if needle not in last:
                fail("ledger", f"{LEDGER}: latest counts declaration lacks '{needle}' — «{last[:100]}»")
elif os.path.isdir("design"):
    fail("ledger", f"{LEDGER} missing")
else:
    curated_absence("ledger", "design/", "design/ (absent: curated export)")

for s in skips:
    print(f"SKIP {s}")
if failures:
    print(f"\nFAIL check-public-counts: {len(failures)} finding(s)\n")
    for f in failures:
        print(f"  {f}")
    sys.exit(1)
print(f"OK check-public-counts: every public surface states the measured counts "
      f"({MODULES} modules · {INTEGRATIONS} integrations · {CATALOGS} catalogs · {ENFORCEMENT} enforcement points · {PATHS} core paths)")
PY
