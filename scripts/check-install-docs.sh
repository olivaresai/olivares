#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bind current install prose to both a measured publication state and a real
# producer. This is deliberately network-free: changing a live state requires
# updating the dated witness first, then making every affected command agree.
#
# THREE PUBLICATION RESULTS, NOT TWO (2026-09-24). A surface is `published`, `not-published` or
# `publication-unverified`. The third is an INABILITY: every read that could settle the state was
# refused or unreadable, and a refusal is not an absence. It carries its `inabilities` (when, what
# was asked, what answered). The Helm OCI chart, the one surface published outside the engine
# release, states its result causally: `published` only with a digest-bound `publication`
# (identity, digest, the chart version of deploy/helm/olivares/Chart.yaml); `not-published` only
# with an authoritative `absence` whose scope is stated; otherwise it is publication-unverified.
#
# Two answers from one witness. The default run is the docs gate: it binds the prose to whatever
# the witness can say (an unverified surface keeps its source install documented and refuses every
# remote-install or absence claim) and names the inability. `--qualify` is the publication
# qualification: 0 only when every surface's result was observed; any publication-unverified
# surface is INABILITY (exit 2), never a qualification.
#
# Usage: check-install-docs.sh [--qualify]
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="${OLIVARES_ROOT:-$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)}"

fail() { printf 'check-install-docs: HALLAZGO — %s\n' "$*" >&2; exit 1; }
blind() { printf 'check-install-docs: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

QUALIFY=0
case "${1:-}" in
--qualify) QUALIFY=1 ;;
"") ;;
*) blind "unknown argument: $1 (usage: check-install-docs.sh [--qualify])" ;;
esac

for tool in jq grep find sort awk mktemp sed wc tr readlink; do
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
CHART="$ROOT/deploy/helm/olivares/Chart.yaml"
[ -r "$CHART" ] || blind "cannot read $CHART (the chart version a Helm publication must be bound to)"
chart_version="$(awk '/^version:[[:space:]]/ { gsub(/"/, "", $2); print $2; exit }' "$CHART")"
[ -n "$chart_version" ] || blind "no version: line in $CHART"

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
    ((keys | sort) as $k | ["evidence","id","producer","prose","status"] as $base |
      $k == ($base | sort) or
      (.status == "publication-unverified" and $k == ($base + ["inabilities"] | sort)) or
      (.status == "not-published" and $k == ($base + ["absence"] | sort)) or
      (.status == "published" and $k == ($base + ["publication"] | sort))) and
    (.status == "published" or .status == "not-published" or .status == "publication-unverified") and
    (if .status == "publication-unverified" then
      (.inabilities | type == "array" and length >= 1 and all(.[];
        type == "object" and ((keys | sort) == ["answer","observed_at","request"]) and
        (.observed_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
        (.request | type == "string" and length > 10) and (.answer | type == "string" and length > 5)))
    else true end) and
    (.evidence | type == "string" and length > 20) and
    (.producer | type == "object" and (keys | sort) == ["matches","path"] and
      (.path | test("^[A-Za-z0-9._/-]+$")) and (.matches | type == "string" and length > 0)) and
    (.prose | type == "object" and (keys | sort) == ["contains","path"] and
      (.path | test("^[A-Za-z0-9._/-]+$")) and (.contains | type == "string" and length > 0))))
' "$STATE" >/dev/null 2>&1; then
	fail "install-surface witness is malformed, incomplete, duplicated, or not bound to $version: $STATE"
fi

# The Helm OCI chart is published outside the engine release, so its result is causal: each arm
# names the evidence it rests on, and a refused read supports neither arm.
helm_cause="$(jq -r --arg name "ghcr.io/olivaresai/charts/olivares" --arg chart "$chart_version" '
  .surfaces[] | select(.id == "helm-oci") |
  if .status == "published" then
    if (.publication | type == "object" and ((keys | sort) == ["digest","identity","version"]) and
        .identity == $name and .version == $chart and
        (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$"))) then empty
    else "helm-oci is published only on a digest-bound record: publication {identity " + $name +
      ", digest sha256:<64 hex>, version " + $chart + " (deploy/helm/olivares/Chart.yaml)}" end
  elif .status == "not-published" then
    if (.absence | type == "object" and ((keys | sort) == ["observed_at","scope","source"]) and
        (.observed_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
        (.source | type == "string" and length > 10) and (.scope | type == "string" and length > 10)) then empty
    else "helm-oci is not-published only on an authoritative absence {source, scope, observed_at}; " +
      "a refused read is an inability, not absence: record it as publication-unverified" end
  else empty end' "$STATE")" || blind "jq failed while reading the Helm result in $STATE"
[ -z "$helm_cause" ] || fail "$helm_cause"

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
# are distinct facts (producer present, publication unverified).
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
# Outside the published arm no live Helm surface may offer the remote chart or call it
# published. The census is the current docs-site pages (the remote coordinate) and the two
# shipped instructions that would pull a chart: the upgrade guide and the chart's own NOTES,
# matched on the chart name with or without oci:// (2026-09-24).
helm_extra=("$ROOT/docs/UPGRADE-AND-ROLLBACK.md" "$ROOT/deploy/helm/olivares/templates/NOTES.txt")
remote_name='ghcr.io/olivaresai/charts/olivares'
overclaim='chart( itself)? (is|has been)( still)? published|is published to the public OCI registry|is published as an \*\*OCI'
# A remote chart COMMAND is the same claim wherever a reader copies it from (added 2026-09-24):
# the root READMEs, INSTALL.md, the chart README, the gitops README and every doc under docs/ that
# the export publishes are read for its shape, `helm install|upgrade|pull|show|template …
# oci://ghcr.io/olivaresai/charts`, with a command wrapped over `\` continuations joined first.
# The shape, not the coordinate: INSTALL.md names the coordinate to say its publication is
# unverified, and that sentence is true. Limits, stated: a CRLF continuation is not joined; a `#`
# or `|` between the verb and the coordinate ends the match (a comment, another table cell); a
# command assembled from variables is not seen; under docs/ only md, mdx and txt are read.
# A symlinked entry is a document too: the curation decides on its own path first, and it is read
# only when it resolves to a regular file inside the tree that the export also publishes.
#
# export-closure: absent-by-design scripts/export-public.sh — DATA, read and never run: its
# *_BLOCK and DOCS_KEEP lists say which docs the export drops, by the rule the export applies
# (a DOCS_KEEP path ships; a path matching a block entry, or below one, does not). In a public
# export it is absent and every doc present counts as published, which can over-report and never
# under-report. A curation this reader cannot follow is COULD NOT LOOK, never "all published".
remote_cmd_rx='helm[ 	]+(install|upgrade|pull|show|template)[^|#]*oci://ghcr[.]io/olivaresai/charts'
curation="$tmp/curation"
curation_lists='TOP_BLOCK DOCS_BLOCK SCRIPTS_BLOCK GITHUB_BLOCK COMMERCIAL_BLOCK MISC_BLOCK DOCS_KEEP'
read_curation() {
	local exporter="$ROOT/scripts/export-public.sh" rc
	: >"$curation"
	[ -e "$exporter" ] || [ -L "$exporter" ] || return 0
	[ -r "$exporter" ] || blind "cannot read $exporter, so which docs the export publishes is unknown"
	rc=0
	grep -Eq '^[[:space:]]*[A-Z_]+(_BLOCK|_KEEP)\+=' "$exporter" || rc=$?
	case "$rc" in
	0) blind "$exporter: a curation list is appended to; the curation changed shape" ;;
	1) ;;
	*) blind "grep failed with rc=$rc while reading $exporter" ;;
	esac
	# The grammar is check-emitted-urls.sh's publication_rule(), no wider: each list is defined
	# once, at the start of a line, as `NAME=()` or as `NAME=(` whose body closes on a line that
	# starts with `)`. A list written inline, indented, defined twice, appended to or left open is
	# valid shell this reader does not follow: COULD NOT LOOK, never a smaller curation.
	awk -v names="$curation_lists" '
		BEGIN { n = split(names, l, " "); for (i = 1; i <= n; i++) want[l[i]] = 1 }
		{
			line = $0; sub(/^[ \t]+/, "", line)
			if (match(line, /^[A-Z_]+[+]?=/)) {
				name = substr(line, 1, RLENGTH); plus = (name ~ /[+]=$/); sub(/[+]?=$/, "", name)
				if (name in want) {
					if (plus || k != "") exit 3
					seen[name]++
					if ($0 == name "=(") { k = (name == "DOCS_KEEP") ? "K" : "B"; next }
					if (index($0, name "=()") == 1) next
					exit 3
				}
			}
		}
		k != "" && /^\)/ { k = ""; next }
		k != "" { sub(/#.*/, ""); for (i = 1; i <= NF; i++) print k, $i }
		END { if (k != "") exit 3; for (x in want) if (seen[x] != 1) exit 3 }
	' "$exporter" >"$curation" \
		|| blind "$exporter: a curation list is written in a form this reader does not follow (inline, indented, defined twice, appended to or left open); the curation changed shape"
	rc=0
	grep -Ev '^[BK] [A-Za-z0-9._/@+*-]+$' "$curation" >"$tmp/curation-odd" || rc=$?
	case "$rc" in
	0) blind "$exporter: a curation entry this reader cannot match as the shell does: $(head -1 "$tmp/curation-odd")" ;;
	1) ;;
	*) blind "grep failed with rc=$rc while checking the curation entries" ;;
	esac
	grep -q '^B docs' "$curation" || blind "$exporter: no curation entry reaches docs/; refusing to say every doc publishes"
}
published_doc() { # published_doc <path from ROOT>: 0 when the export publishes it
	local kind entry
	while read -r kind entry; do
		[ "$kind" = K ] && [ "$entry" = "$1" ] && return 0
	done <"$curation"
	while read -r kind entry; do
		[ "$kind" = B ] || continue
		# The entry IS a glob, matched as the export matches it.
		case "$1" in $entry | $entry/*) return 1 ;; esac
	done <"$curation"
	return 0
}
remote_commands() { # remote_commands <hits file>: every published doc in scope, read for the shape
	local out="$1" file rel root_real target
	: >"$out"
	read_curation
	root_real="$(readlink -f -- "$ROOT")" || blind "cannot resolve $ROOT"
	{
		for file in "$ROOT/README.md" "$ROOT"/README.??.md "$ROOT/INSTALL.md" \
			"$ROOT/deploy/helm/README.md" "$ROOT/deploy/gitops/README.md"; do
			if [ -e "$file" ] || [ -L "$file" ]; then printf '%s\n' "$file"; fi
		done
		find "$ROOT/docs" \( -type f \( -name '*.md' -o -name '*.mdx' -o -name '*.txt' \) -o -type l \) -print
	} >"$tmp/command-docs" || blind "cannot enumerate the READMEs, INSTALL.md, the chart and gitops READMEs and docs/ for remote chart commands"
	while IFS= read -r file; do
		rel="${file#"$ROOT"/}"
		# The curation decides first: a curated-out path is never read, whatever it points to.
		published_doc "$rel" || continue
		if [ -L "$file" ]; then
			# A linked directory is not walked (no loop, no escape), so what it holds is unread.
			[ ! -d "$file" ] || blind "$rel is a linked directory; the census does not follow it, so what it holds is unread"
			case "$file" in *.md | *.mdx | *.txt) ;; *) continue ;; esac
			[ -e "$file" ] || blind "$rel is a dangling link; a document the census cannot read is not a clean one"
			target="$(readlink -f -- "$file")" || blind "cannot resolve the link $rel"
			case "$target" in "$root_real"/*) ;; *) blind "$rel resolves outside the tree; $target is not read" ;; esac
			[ -f "$target" ] || blind "$rel does not resolve to a regular document"
			published_doc "${target#"$root_real"/}" \
				|| blind "$rel resolves to curated-out content; it is not read, and in the export the link dangles"
		fi
		awk -v rel="$rel" -v rx="$remote_cmd_rx" '
			{ if (buf == "") start = FNR }
			/\\$/ { buf = buf substr($0, 1, length($0) - 1) " "; next }
			{ buf = buf $0; if (buf ~ rx) print rel ":" start ": " buf; buf = "" }
			END { if (buf != "" && buf ~ rx) print rel ":" start ": " buf }
		' "$file" >>"$out" || blind "cannot read $file for remote chart commands"
	done <"$tmp/command-docs"
}
helm_not_offered() { # helm_not_offered <how the state reads in a finding>
	local state="$1" hits="$tmp/remote-chart-hits" over="$tmp/overclaim-hits" file rc
	: >"$hits"
	: >"$over"
	while IFS= read -r file; do
		rc=0
		grep -HFn -- "$remote_chart" "$file" >>"$hits" || rc=$?
		case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
	done <"$current"
	for file in "${helm_extra[@]}"; do
		# Absent offers nothing; present but unreadable (a dangling link included) is COULD NOT LOOK.
		[ -e "$file" ] || [ -L "$file" ] || continue
		[ -r "$file" ] || blind "cannot read $file (a shipped Helm instruction)"
		rc=0
		grep -HFn -- "$remote_name" "$file" >>"$hits" || rc=$?
		case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
	done
	[ ! -s "$hits" ] || fail "the Helm OCI $state, but current docs offer its remote coordinate:\n$(sed "s#^$ROOT/##" "$hits")"
	while IFS= read -r file; do
		rc=0
		grep -HEin -- "$overclaim" "$file" >>"$over" || rc=$?
		case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $file" ;; esac
	done < <(printf '%s\n' "$ROOT/deploy/helm/README.md"; for file in "${helm_extra[@]}"; do
		[ ! -e "$file" ] || printf '%s\n' "$file"; done; cat "$current")
	# Chart.yaml's full-line comments are evidence about the chart and are read for the same claim;
	# its YAML values (coordinates, annotations) are product data and are not. Trailing comments on
	# a value line are not read (added 2026-09-24).
	file="$ROOT/deploy/helm/olivares/Chart.yaml"
	if [ -e "$file" ] || [ -L "$file" ]; then
		awk '/^[ \t]*#/ { print "deploy/helm/olivares/Chart.yaml:" FNR ":" $0 }' "$file" >"$tmp/chart-comments" \
			|| blind "cannot read $file"
		rc=0
		grep -Ei -- "$overclaim" "$tmp/chart-comments" >>"$over" || rc=$?
		case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $tmp/chart-comments" ;; esac
	fi
	[ ! -s "$over" ] || fail "a live Helm surface claims the chart is published, but the Helm OCI $state:\n$(sed "s#^$ROOT/##" "$over")"
	remote_commands "$tmp/remote-commands"
	[ ! -s "$tmp/remote-commands" ] \
		|| fail "the Helm OCI $state, but a published doc offers a remote chart command:\n$(cat "$tmp/remote-commands")"
}
case "$helm_status" in
not-published)
	helm_not_offered "surface is not published"
	contains "$ROOT/deploy/helm/README.md" 'not published to the public OCI registry' "Helm status"
	contains "$ROOT/deploy/helm/README.md" 'helm install olivares deploy/helm/olivares' "local Helm command"
	;;
publication-unverified)
	# INABILITY. Neither arm may be told: no current doc offers the remote chart (an unverified
	# remote installation claim), the README states the unverified status and no absence, and the
	# source install stays documented and usable.
	helm_not_offered "publication is unverified"
	# An absence may be stated only within its demonstrated scope: this repository published
	# nothing (no chart-v* tag, no publisher run). A registry-wide absence is exactly what a
	# refused read cannot establish, so an unscoped one is a finding.
	rc=0
	grep -F -- 'not published to the public OCI registry' "$ROOT/deploy/helm/README.md" >"$tmp/helm-absence" || rc=$?
	case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while reading $ROOT/deploy/helm/README.md" ;; esac
	rc=0
	grep -Fv -- 'from this repository' "$tmp/helm-absence" >"$tmp/helm-absence-unscoped" || rc=$?
	case "$rc" in 0|1) ;; *) blind "grep failed with rc=$rc while filtering $ROOT/deploy/helm/README.md" ;; esac
	[ ! -s "$tmp/helm-absence-unscoped" ] || fail "deploy/helm/README.md states an absence the witness cannot verify (helm-oci is publication-unverified); scope it to this repository or remove it"
	contains "$ROOT/deploy/helm/README.md" 'its publication to the public OCI registry is **unverified**' "Helm status"
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

# `helm verify` checks a Helm-native GPG .prov (added 2026-09-24). The chart publisher signs with
# cosign and writes none, and a source install has no .tgz, so in every publication state the
# NOTES may offer it only for the chart .tgz of an air-gap bundle packaged with --gpg-key, named
# on the line above it. Offered without that condition, next to "no chart signature to check",
# it reads as a signature the reader can verify, and there is none.
notes="$ROOT/deploy/helm/olivares/templates/NOTES.txt"
if [ -e "$notes" ] || [ -L "$notes" ]; then
	awk -v rel="deploy/helm/olivares/templates/NOTES.txt" '
		/helm verify/ && prev !~ /--gpg-key/ { print rel ":" FNR ": " $0 }
		$0 !~ /^[ \t]*$/ { prev = $0 }
	' "$notes" >"$tmp/notes-verify" || blind "cannot read $notes"
	[ ! -s "$tmp/notes-verify" ] \
		|| fail "the chart NOTES offer helm verify without its air-gap condition (a bundle packaged with --gpg-key, named on the line above); the chart publisher writes no .prov and a source install has no .tgz:\n$(cat "$tmp/notes-verify")"
fi

# ── THE PACKAGE FILENAME IS CHECKED IN EVERY LOCALE, NOT ONLY IN ENGLISH ────
# The three literals above are the English guide's, and they were the ONLY three of
# twenty-one that anything checked. Measured on the v26.9.1 cut: after the version sweep,
# `olivares_<previous>_linux_amd64.{deb,rpm,apk}` survived in all seven locales, because
# NEITHER arm of check-release-version.sh can see that shape — both token regexes end on
# `\b` and the next character is `_`, which is a word character, so there is no boundary.
# The HYPHEN form of the same name matches and has a case of its own; the UNDERSCORE form
# is the one GoReleaser actually produces.
#
# No release number is written in this comment, and that is not shyness: this script is
# scanned by the pin census of check-release-version.sh, which exempts only its OWN
# fixtures. The first draft spelled the hyphen form with a real past version and the gate
# refused the push, naming this line. A gate that catches its sibling's prose is the gate
# working.
#
# Same discipline as the three sibling loops below: enumerate the locales from the tree and
# check the LITERAL a reader copies, so an omitted translation is independently red rather
# than covered by its English original.
list="$tmp/install-from-packages.md"
find "$ROOT/docs-site/src/content/docs" -type f -name 'install-from-packages.md' ! -path '*/2026-06/*' -print | sort >"$list" \
	|| blind "cannot enumerate install-from-packages.md"
[ "$(wc -l <"$list" | tr -d ' ')" = 7 ] || fail "expected seven current package guides"
while IFS= read -r file; do
	for ext in deb rpm apk; do
		contains "$file" "olivares_${plain_version}_linux_amd64.$ext" "localized package command"
	done
	contains "$file" 'arm64' "localized package architecture"
	contains "$file" 'draft: false' "published localized package guide"
done <"$list"

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
publication-unverified)
	# Whether the tag is cut decides which phrasing is barred, and nothing observed says so.
	blind "github-release publication is unverified, so whether the pre-release phrasing is barred cannot be decided (INABILITY)"
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

unverified="$(jq -r '[.surfaces[] | select(.status == "publication-unverified") | .id] | join(", ")' "$STATE")" \
	|| blind "jq failed while listing unverified surfaces in $STATE"
n_unverified="$(jq '[.surfaces[] | select(.status == "publication-unverified")] | length' "$STATE")" \
	|| blind "jq failed while counting unverified surfaces in $STATE"
if [ "$QUALIFY" = 1 ]; then
	# The qualification answers with its verdict only: a docs "OK" line printed inside a refused
	# qualification would read as a success.
	[ -z "$unverified" ] || blind "publication NOT QUALIFIED — publication-unverified: $unverified; an inability is never a qualification"
	printf 'check-install-docs: publication qualified — %s published, %s not-published, every one observed\n' \
		"$(jq '[.surfaces[] | select(.status == "published")] | length' "$STATE")" \
		"$(jq '[.surfaces[] | select(.status == "not-published")] | length' "$STATE")"
	exit 0
fi
printf 'check-install-docs: OK — 8 producer/state/prose bindings (%s observed, %s publication-unverified), exact package names, 28 current localized guides\n' \
	"$((8 - n_unverified))" "$n_unverified"
[ -z "$unverified" ] || printf 'check-install-docs: INABILITY — publication-unverified: %s (the docs are bound to it; the publication qualification, --qualify, answers 2)\n' "$unverified"
