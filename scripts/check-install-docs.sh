#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bind current install prose to both a measured publication state and a real
# producer. This is deliberately network-free: changing a live state requires
# updating the dated witness first, then making every affected command agree.
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="${OLIVARES_ROOT:-$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)}"

fail() { printf 'check-install-docs: HALLAZGO — %s\n' "$*" >&2; exit 1; }
blind() { printf 'check-install-docs: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

for tool in jq grep find sort awk mktemp sed wc tr; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
[ -r "$ROOT/RELEASE-VERSION" ] || blind "cannot read $ROOT/RELEASE-VERSION"

version="$(awk '!/^[[:space:]]*(#|$)/ { print; exit }' "$ROOT/RELEASE-VERSION")"
case "$version" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) blind "RELEASE-VERSION has no readable vYY.M.PATCH value" ;;
esac
plain_version="${version#v}"

# ⛔ THE WITNESS PATH IS DERIVED FROM THE CANON, NOT WRITTEN DOWN. It used to name
# v26.8.0 as a literal default, so re-deriving RELEASE-VERSION for the next cut left this
# gate binding the PREVIOUS release's witness to the NEW version and failing with "not bound
# to <canon>" — a true complaint about the wrong file. One release, one dated witness, and
# the name of the current one is a function of the canon. An earlier release keeps its own
# file beside it: docs/releases/ is the ledger, and nothing here rewrites a past entry.
STATE="${OLIVARES_INSTALL_SURFACES:-$ROOT/docs/releases/$version-install-surfaces.json}"
[ -r "$STATE" ] || blind "cannot read $STATE (the witness for $version; one release, one dated witness)"

if ! jq -e --arg version "$version" '
  type == "object" and
  ((keys | sort) == (["$comment","measured_at","schema","surfaces","version"] | sort)) and
  .schema == "olivares.ai/install-surfaces/v1" and .version == $version and
  (.measured_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
  (.surfaces | type == "array" and length == 8) and
  ([.surfaces[].id] | sort == [
    "archives","docker-hub","ghcr","github-release","helm-oci","homebrew","installer","native-packages"
  ]) and
  ([.surfaces[].id] | length == (unique | length)) and
  (all(.surfaces[];
    ((keys | sort) == (["evidence","id","producer","prose","status"] | sort)) and
    (.status == "published" or .status == "not-published") and
    (.evidence | type == "string" and length > 20) and
    (.producer | type == "object" and (keys | sort) == ["matches","path"] and
      (.path | test("^[A-Za-z0-9._/-]+$")) and (.matches | type == "string" and length > 0)) and
    (.prose | type == "object" and (keys | sort) == ["contains","path"] and
      (.path | test("^[A-Za-z0-9._/-]+$")) and (.contains | type == "string" and length > 0))))
' "$STATE" >/dev/null 2>&1; then
	fail "install-surface witness is malformed, incomplete, duplicated, or not bound to $version: $STATE"
fi

contains() { # contains <file> <literal> <role>
	local file="$1" needle="$2" role="$3" rc=0
	[ -r "$file" ] || blind "$role file is unreadable: $file"
	grep -Fq -- "$needle" "$file" || rc=$?
	case "$rc" in
	0) ;;
	1) fail "$role anchor is absent from ${file#"$ROOT/"}: $needle" ;;
	*) blind "grep failed with rc=$rc while reading $file" ;;
	esac
}

matches() { # matches <file> <extended-regexp> <role>
	local file="$1" pattern="$2" role="$3" rc=0
	[ -r "$file" ] || blind "$role file is unreadable: $file"
	grep -Eq -- "$pattern" "$file" || rc=$?
	case "$rc" in
	0) ;;
	1) fail "$role active anchor is absent from ${file#"$ROOT/"}: $pattern" ;;
	*) blind "grep failed with rc=$rc while reading $file" ;;
	esac
}

# Every public claim has a source producer and an explicit prose anchor. The
# status is not inferred from the existence of a workflow: Helm proves why those
# are distinct facts (producer present, publication absent).
while IFS=$'\t' read -r producer_path producer_pattern prose_path prose_anchor; do
	matches "$ROOT/$producer_path" "$producer_pattern" "producer"
	contains "$ROOT/$prose_path" "$prose_anchor" "prose"
done < <(jq -r '.surfaces[] | [.producer.path,.producer.matches,.prose.path,.prose.contains] | @tsv' "$STATE")

# The package command is derived from the producer's literal GoReleaser naming
# template, then checked against the filenames that the dated release witness
# says were measured. A generic wildcard cannot hide a wrong public filename.
contains "$ROOT/.goreleaser.yaml" '{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}' "archive name producer"
contains "$ROOT/.goreleaser.yaml" 'formats: [deb, rpm, apk]' "native-package format producer"
package_doc="$ROOT/docs-site/src/content/docs/how-to/install-from-packages.md"
contains "$package_doc" "olivares_${plain_version}_linux_amd64.deb" "package command"
contains "$package_doc" "olivares_${plain_version}_linux_amd64.rpm" "package command"
contains "$package_doc" "olivares_${plain_version}_linux_amd64.apk" "package command"
contains "$package_doc" 'arm64' "package architecture"
contains "$package_doc" 'draft: false' "published package guide"
matches "$ROOT/docs-site/astro.config.mjs" "^[[:space:]]*\\{ label: 'Install from a package', slug: 'how-to/install-from-packages' \\},$" "package guide route"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/check-install-docs.XXXXXX")" || blind "cannot create scratch directory"
trap 'rm -rf "$tmp"' EXIT
current="$tmp/current-docs"
if ! find "$ROOT/docs-site/src/content/docs" -type f \
	! -path '*/2026-06/*' \( -name '*.md' -o -name '*.mdx' \) -print | sort >"$current"; then
	blind "cannot enumerate current docs-site prose"
fi
[ -s "$current" ] || blind "current docs-site census is empty"

helm_status="$(jq -r '.surfaces[] | select(.id == "helm-oci") | .status' "$STATE")"
remote_chart='oci://ghcr.io/olivaresai/charts/olivares'
case "$helm_status" in
not-published)
	hits="$tmp/remote-chart-hits"
	: >"$hits"
	while IFS= read -r file; do
		rc=0
		grep -Fn -- "$remote_chart" "$file" >>"$hits" || rc=$?
		case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
	done <"$current"
	[ ! -s "$hits" ] || fail "the Helm OCI surface is not published, but current docs offer its remote coordinate:\n$(sed "s#^$ROOT/##" "$hits")"
	contains "$ROOT/deploy/helm/README.md" 'not published to the public OCI registry' "Helm status"
	contains "$ROOT/deploy/helm/README.md" 'helm install olivares deploy/helm/olivares' "local Helm command"
	;;
published)
	# Publishing Helm is a state transition, not a one-word edit: current guides
	# must switch to the remote coordinate before the witness may say published.
	while IFS= read -r file; do
		case "$file" in
		*/how-to/self-hosting.md|*/tutorials/getting-started/kubernetes.mdx)
			contains "$file" "$remote_chart" "published Helm command"
			;;
		esac
	done <"$current"
	;;
*) fail "unknown Helm publication status: $helm_status" ;;
esac

# All seven current locales must send users to the same executable source path.
# Checking the path rather than translated adjectives keeps this mechanical and
# makes an omitted translation independently red.
for name in self-hosting.md docker-deployment.md verify-a-release.md; do
	list="$tmp/$name"
	find "$ROOT/docs-site/src/content/docs" -type f -name "$name" ! -path '*/2026-06/*' -print | sort >"$list" \
		|| blind "cannot enumerate $name"
	[ "$(wc -l <"$list" | tr -d ' ')" = 7 ] || fail "expected seven current $name files"
	while IFS= read -r file; do contains "$file" 'deploy/helm/olivares' "current $name"; done <"$list"
done
list="$tmp/kubernetes.mdx"
find "$ROOT/docs-site/src/content/docs" -type f -path '*/tutorials/getting-started/kubernetes.mdx' \
	! -path '*/2026-06/*' -print | sort >"$list" || blind "cannot enumerate Kubernetes tutorials"
[ "$(wc -l <"$list" | tr -d ' ')" = 7 ] || fail "expected seven current Kubernetes tutorials"
while IFS= read -r file; do contains "$file" 'helm install olivares deploy/helm/olivares' "current Kubernetes tutorial"; done <"$list"

# First-release stale claims are barred from the current public surfaces. This
# intentionally excludes the dated 2026-06 snapshot, which records what readers
# were told then rather than what they should do now.
#
# ── DOCUMENTATION SCOPE: docs/ IS WALKED, NOT LISTED (added 2026-09-03) ──────
# The census below enumerated its members by hand and `docs/` was in NONE of them,
# so a whole class of release-bearing prose was scanned by neither arm. Measured on
# the landed tree with the tag already cut: 29 stale lines in 11 files — the six
# launch texts posts from, the launch README, the video script, the release
# checklist, its review notes, and `docs/trust/one-pager.md`, which is the trust
# one-pager an evaluator reads.
#
# Listing the files that happened to be reported would have rebuilt the exact blind
# spot this closes; the twelfth declarant would be born invisible. The prunes carry
# over the sibling gate's rule AND its warning (check-release-version.sh, 2026-08-14):
# EXACT directory names, never substrings, so a live `archived-decisions/` is not
# swallowed by a prune meant for `archive/`. `find -name` is an exact match by
# construction, which is why the walk is written this way and not with `-path '*adr*'`.
stale="$tmp/stale-release-hits"
: >"$stale"
scan_files=(
	"$ROOT/README.md" "$ROOT/README.de.md" "$ROOT/README.es.md" "$ROOT/README.fr.md"
	"$ROOT/README.ja.md" "$ROOT/README.ru.md" "$ROOT/README.zh.md"
	"$ROOT/INSTALL.md" "$ROOT/SECURITY.md" "$ROOT/SUPPORT.md" "$ROOT/CHANGELOG.md"
	"$ROOT/RELEASE-VERSION" "$package_doc"
)
while IFS= read -r file; do scan_files+=("$file"); done <"$current"
walked="$tmp/walked-docs"
# ⛔ LOS NOMBRES DEL `find` VAN ENTRECOMILLADOS, Y NO ES ESTILO: el de arriba, sin comillas,
# metia este fichero en la clase de `check-git-env-isolation.sh` — «construye un repositorio
# desechable Y CORRE GIT»— sin que este guion invoque git ni una vez. Su predicado de pertenencia
# es `mktemp -d|git init` (lo cumple el scratch de :97, que viene de main) Y ademas
# `(^|[^-[:alnum:]_])git ` — y el punto del nombre satisface esa clase de caracter, con un espacio
# detras. Resultado medido el 2026-09-04: la clase pasaba de 116 miembros a 121 y la pata daba
# BROKEN acusando a este fichero de no aislar un entorno git que nunca toca.
# Entrecomillar no cambia nada para `find` (no hay metacaracteres) y hace que el texto diga lo que
# se quiere decir: un LITERAL exacto, que es justo lo que el bloque de arriba exige. La causa —un
# predicado que no distingue una invocacion de un argumento— esta reportada al dueno del gate.
if ! find "$ROOT/docs" \
	\( -type d \( -name '2026-06' -o -name 'adr' -o -name 'archive' \
		-o -name '.git' -o -name 'node_modules' -o -name 'vendor' \) -prune \) -o \
	-type f -name '*.md' -print >"$walked"; then
	blind "cannot walk docs/ for release-bearing prose"
fi
[ -s "$walked" ] || blind "docs/ walk is empty"
# El alcance del MARCADOR de plantilla y el del CALIFICATIVO pre-release NO son el
# mismo, y confundirlos sobre-bloquea. `[VERIFICAR]` es una marca de TRABAJO legitima
# en un documento de planificacion: medido hoy, seis viven en docs/launch/PLAN-* y
# BORRADORES-*, que nadie publica. Su sujeto siempre fue la prosa PUBLICADA, asi que
# conserva el censo de superficies; el calificativo, que si es una afirmacion sobre el
# producto la lea quien la lea, usa la caminata entera.
surface_files=("${scan_files[@]}")
while IFS= read -r file; do scan_files+=("$file"); done <"$walked"

# ── THE PRE-RELEASE QUALIFIER IS BARRED ONLY ONCE THE TAG IS CUT ─────────
# RELEASE-VERSION cannot answer this: it carried v26.8.0 BEFORE the cut and after it,
# because CalVer derives the number from the intended date, not from publication. The
# witness can — `github-release`.status is the machine-readable fact, and it is the
# same file this gate already trusts for the Helm transition.
#
# WHY THE CONDITION IS THE WHOLE POINT. `docs/launch/RELEASE-CHECKLIST.md` carried:
# «If the tag isn't cut yet, every artifact must keep "build from source until
# v26.8.0" — confirm that wording is what's live.Âb That item was CORRECT. The tag
# landed on 2026-09-01, nothing re-evaluated the condition, and two days into the
# launch 29 lines still told readers to build from source. A checklist is read when
# you pass it, never when the world changes underneath; an unticked box cannot tell
# "not looked at" from "no longer applies". This gate is that missing twin: while the
# status is not-published the phrasings are TRUE and must NOT be barred (barring them
# would make a correct pre-release tree unpushable); the moment it flips, each one is
# red with its file and line.
release_status="$(jq -r '.surfaces[] | select(.id == "github-release") | .status' "$STATE")"
case "$release_status" in
published)
	# Two shapes, because the claim wraps across lines in Markdown and a same-line
	# pairing requirement measured 2 genuine misses (`launch/README.md:57` ends at
	# "until it lands, build", with "from source" on the next line).
	prerelease_pattern='no tagged release exists|no release tag exists|no public release cut yet|not yet released|pending the first public release|first release is still a draft|build from source until|until then,? build from source|public but still empty'
	# "until it lands" ALONE is not evidence: it measured 2 false positives on prose
	# about a FEATURE landing (`docs/RELEASE-EXECUTION-HANDOFF.md:50`, inside a bullet
	# deliberately kept as a retired record, and `docs/07-LICENSE-AND-OPEN-CORE.md:467`).
	# A claim about THE RELEASE landing names the release, so the version must be on the
	# same line — and taking it from RELEASE-VERSION keeps this true after the next cut.
	landing_pattern='until (it|that tag) lands'
	;;
not-published)
	prerelease_pattern=''
	landing_pattern=''
	;;
*) fail "unknown github-release publication status: $release_status" ;;
esac

# The placeholder marker is unconditional: folding it into the status-gated block would
# have silently disarmed it on every pre-release tree. The pattern accepts BOTH shapes.
# `[VERIFICAR:` was the only one checked, so a bare `[VERIFICAR]` on a published page was
# invisible — measured today: zero in scope, which is why widening it costs nothing now and
# closes the hole for the next page that lands with one. In an ERE bracket expression `]`
# must come FIRST to be a literal, hence `[]:]` and not `[:\]]`.
for file in "${surface_files[@]}"; do
	rc=0
	grep -HnEi -- '\[VERIFICAR[]:]' "$file" >>"$stale" || rc=$?
	case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
done

landing="$tmp/landing-hits"
for file in "${scan_files[@]}"; do
	[ -n "$prerelease_pattern" ] || continue
	rc=0
	grep -HnEi -- "$prerelease_pattern" "$file" >>"$stale" || rc=$?
	case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
	# NOT `grep ... | grep ...`: under `pipefail` the pipeline reports the RIGHTMOST
	# non-zero status, so a real rc=2 from the first grep is masked by the second one's
	# rc=1 — the gate would read "could not look" as "nothing found", which is the one
	# confusion this file exists to prevent.
	rc=0
	grep -HnEi -- "$landing_pattern" "$file" >"$landing" || rc=$?
	case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
	rc=0
	grep -F -- "$version" "$landing" >>"$stale" || rc=$?
	case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while filtering $file" ;; esac
done
[ ! -s "$stale" ] || fail "public prose still describes the pre-release state:\n$(sed "s#^$ROOT/##" "$stale")"

contains "$ROOT/CHANGELOG.md" '## [Unreleased]' "changelog"
# ⛔ THE DATE IS READ FROM THE SECTION, NOT TYPED HERE. This line carried the literal
# 2026-09-01, which is the date of the FIRST cut: re-deriving the canon made it demand that
# the new release section carry the old release's date, so the gate asked for a false record.
# What must be true is the binding, not a particular day — the canon has a dated section, and
# the date is whatever that section states. The shape is pinned (ISO 8601, anchored both ends)
# so an UNDATED heading and a non-date marker are still red.
#
# WHAT THIS CANNOT SEE, SAID PLAINLY, because the literal it replaced could: a WELL-FORMED date
# that is simply the wrong day. A version-cut series is prepared before the tag exists and
# renders a sample date, and this check cannot tell that sample from the real one. Nothing in
# the tree can: the cut date is not derivable from the tree, it is an act. The control is the
# release procedure — one sed over this one heading at tag time — not a gate, and pretending
# otherwise here would be the worse failure, because a false literal blocked correct trees
# without ever catching a plausible wrong date either.
version_re="$(printf '%s' "$plain_version" | sed 's/[.]/[.]/g')"
matches "$ROOT/CHANGELOG.md" "^## \\[${version_re}\\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$" "released changelog section"
contains "$ROOT/SECURITY.md" "v${plain_version}" "supported release"

printf 'check-install-docs: OK — 8 producer/state/prose bindings, exact package names, 28 current localized guides\n'
