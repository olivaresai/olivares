#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-screenshot-coverage.sh. Each case builds a small tree and runs the real check
# against it through OLIVARES_CLONE, the anchor the check already documents. Rules under test:
#   - a route is covered by a take in the capture manifest, never by a file under public/;
#   - a published capture must be named by a docs page, a README or launch material, or the check fails.
set -uo pipefail
LC_ALL=C
export LC_ALL

AQUI="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd -P)"
CHECK="$AQUI/check-screenshot-coverage.sh"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
CASOS=0
FALLOS=0

# fixture <name> [take ids...]: ten routes /r0../r9 in the registry, one take per id given.
fixture() {
	local d="$TMP_ROOT/$1" i takes=""
	shift
	mkdir -p "$d/web/src/features/f" "$d/docs-site/public/console" "$d/docs-site/src/content/docs"
	for i in 0 1 2 3 4 5 6 7 8 9; do
		printf "  {\n    id: 'r%s',\n    path: '/r%s',\n  },\n" "$i" "$i"
	done >"$d/web/src/features/f/views.tsx"
	for i in "$@"; do
		takes="$takes{\"id\":\"$i\",\"theme\":\"dark\",\"route\":\"/$i\",\"sha256\":\"00\"},"
	done
	printf '{"take":[%s]}\n' "${takes%,}" >"$d/docs-site/public/console/manifest.json"
	printf '%s' "$d"
}

# run_check <dir> [max]: prints "<rc>" then the output.
run_check() {
	local out rc
	out="$(OLIVARES_CLONE="$1" OLIVARES_SCREENSHOT_UNCOVERED_MAX="${2:-0}" bash "$CHECK" 2>&1)"
	rc=$?
	printf '%s\n%s\n' "$rc" "$out"
}

expect() { # <case> <want rc> <dir> [max] [text the output must contain]
	local name="$1" want="$2" dir="$3" max="${4:-0}" text="${5:-}" res rc
	CASOS=$((CASOS + 1))
	res="$(run_check "$dir" "$max")"
	rc="$(head -1 <<<"$res")"
	if [ "$rc" != "$want" ] || { [ -n "$text" ] && ! grep -qF -- "$text" <<<"$res"; }; then
		echo "  FAIL $name: rc=$rc, wanted $want${text:+ with \"$text\"}" >&2
		sed 's/^/        /' <<<"$res" >&2
		FALLOS=$((FALLOS + 1))
		return
	fi
	echo "  ok   $name"
}

ALL="r0 r1 r2 r3 r4 r5 r6 r7 r8 r9"

# 1) every route has a take and no PNG is published: green. The old check read the files.
d="$(fixture takes_without_files $ALL)"
expect "a take covers its route without a published file" 0 "$d"

# 2) a PNG exists for r9 but the manifest has no take for it: r9 is uncovered.
d="$(fixture file_without_take r0 r1 r2 r3 r4 r5 r6 r7 r8)"
printf 'png' >"$d/docs-site/public/console/r9-dark.png"
printf '![r9](/console/r9-dark.png)\n' >"$d/docs-site/src/content/docs/p.md"
expect "a published file does not cover a route" 1 "$d" 0 "1 of 10"

# 3) a published capture no page names is an orphan.
d="$(fixture orphan $ALL)"
printf 'png' >"$d/docs-site/public/console/r3-dark.png"
expect "an unreferenced published capture fails" 1 "$d" 0 "r3-dark.png"

# 4) a docs page names it: not an orphan.
d="$(fixture page_reference $ALL)"
printf 'png' >"$d/docs-site/public/console/r3-dark.png"
printf '![r3](/console/r3-dark.png)\n' >"$d/docs-site/src/content/docs/p.md"
expect "a docs page reference keeps a capture" 0 "$d"

# 5) a README names it: not an orphan.
d="$(fixture readme_reference $ALL)"
printf 'png' >"$d/docs-site/public/console/r3-dark.png"
printf '![r3](docs-site/public/console/r3-dark.png)\n' >"$d/README.md"
expect "a README reference keeps a capture" 0 "$d"

# 5b) launch material under docs/launch names it: not an orphan (the post would embed a missing image).
d="$(fixture launch_reference $ALL)"
printf 'png' >"$d/docs-site/public/console/r3-dark.png"
mkdir -p "$d/docs/launch"
printf '![r3](../../docs-site/public/console/r3-dark.png)\n' >"$d/docs/launch/post.md"
expect "a launch post reference keeps a capture" 0 "$d"

# 6) a longer name that ends the same way must not count as a reference to the short one.
d="$(fixture suffix_confusion $ALL)"
printf 'png' >"$d/docs-site/public/console/a-dark.png"
printf '![x](/console/xa-dark.png)\n' >"$d/docs-site/src/content/docs/p.md"
expect "a reference to xa-dark.png is not a reference to a-dark.png" 1 "$d" 0 "a-dark.png"

# 7) no take list: the check cannot look, so it is not a green.
d="$(fixture empty_takes)"
expect "an empty take list is not measurable" 2 "$d"

# 8) no manifest: same.
d="$(fixture no_manifest $ALL)"
rm "$d/docs-site/public/console/manifest.json"
expect "a missing manifest is not measurable" 2 "$d"

# 9) a parametric route and a state take: /session-viewer/$id is covered by the take `session-viewer`
#    (the parameter segment drops out of the slug), /access-map by the state take `access-map-drift`.
d="$(fixture parametric $ALL session-viewer access-map-drift)"
printf '"/session-viewer/$id"\n"/access-map"\n' >"$d/web/src/features/route-census.json"
expect "a parametric route and a state take cover their routes" 0 "$d" 0 "0 of 12"

# 10) a pinned snapshot in a subdirectory is not a published capture, and naming it keeps nothing alive.
d="$(fixture snapshot_dir $ALL)"
mkdir -p "$d/docs-site/public/console/2026-09"
printf 'png' >"$d/docs-site/public/console/2026-09/x.png"
expect "a snapshot subdirectory is not an orphan" 0 "$d"
printf 'png' >"$d/docs-site/public/console/r3-dark.png"
printf '![r3](../../assets/console/2026-09/r3-dark.png)\n' >"$d/docs-site/src/content/docs/p.md"
expect "a snapshot reference does not keep a root capture" 1 "$d" 0 "r3-dark.png"

# 11) a take without the harness sha256 does not cover its route.
d="$(fixture hand_added_take r0 r1 r2 r3 r4 r5 r6 r7 r8)"
jq '.take += [{"id":"r9","theme":"dark","route":"/r9"}]' "$d/docs-site/public/console/manifest.json" >"$d/m.json"
mv "$d/m.json" "$d/docs-site/public/console/manifest.json"
expect "a take without sha256 does not cover a route" 1 "$d" 0 "1 of 10"

# 12) faults are not answers: a malformed manifest, or no docs tree to scan, is rc 2.
d="$(fixture malformed_manifest $ALL)"
printf '{' >"$d/docs-site/public/console/manifest.json"
expect "a malformed manifest is not measurable" 2 "$d"
d="$(fixture no_docs_tree $ALL)"
rm -r "$d/docs-site/src"
expect "a missing docs tree is not measurable" 2 "$d"

# 13) a docs file grep cannot read is a fault, not an empty reference set (skipped when the runner
#     can read it anyway, as root can).
d="$(fixture unreadable_page $ALL)"
printf 'png' >"$d/docs-site/public/console/r3-dark.png"
printf '![r3](/console/r3-dark.png)\n' >"$d/docs-site/src/content/docs/p.md"
chmod 000 "$d/docs-site/src/content/docs/p.md"
if [ -r "$d/docs-site/src/content/docs/p.md" ]; then
	echo "  skip an unreadable docs file is not measurable (this runner reads mode 000 files)"
else
	expect "an unreadable docs file is not measurable" 2 "$d"
fi

echo "test-screenshot-coverage: $CASOS cases, $FALLOS failed"
[ "$FALLOS" -eq 0 ]
