#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-cli-ref-wrapper.sh — the battery for the STAGES of scripts/check-cli-ref-docs.sh.
#
# WHY IT EXISTS, measured 2026-08-16 by mutation. scripts/cli-ref-docs carries a
# 28-case fixture battery, and it is thorough: seven mutants of the generator
# (fixed-list roster, disarmed population floor, self-comparison, neutered prose
# sweep, CANNOT LOOK downgraded to 0, unchecked schema, one-directional exit-code
# cross-check) were each killed by a NAMED case. But that battery links the
# generator directly and never runs the wrapper, and the wrapper is where the
# decision "could I look at all?" is actually taken. Two mutants of it survived the
# whole 28 cases untouched:
#
#   M8  `go test … || true` + `test_rc=0`     — 28/28 green, real tree green
#   M9  the `[ ! -s "$DUMP" ]` guard removed  — 28/28 green, real tree green
#
# M8 is not academic and it is not an equivalent mutant. TestCLIRefDump reports its
# environment-independence and universal-`--help` assertions with t.Errorf, which
# does NOT stop the test, so the dump is written anyway. Measured with that failure
# planted: the real wrapper answers 2 (CANNOT LOOK) and the M8 wrapper answers 0
# and prints "OK — the page matches the binary", while the walk's own assertions
# had failed. A gate that certifies is worse than no gate, and `… || true` on a
# sentinel is a defect this repository has already paid for once (CLAUDE.md,
# "un `fetch … || true` se traga el fallo").
#
# HOW IT TESTS THE STAGES WITHOUT A HOOK IN THEM. There is no environment variable
# that makes the wrapper substitute its walk — an override like that would be an
# escape hatch in the production path, which is the opposite of what this is for.
# Instead the battery runs the wrapper UNMODIFIED, from the real repository root,
# with a throwaway `go` shim FIRST ON PATH. The shim forwards everything except
# `go test` to the real toolchain; for `go test` it plays the failure the case is
# about. The wrapper cannot tell, and nothing in it changed.
#
# THE GREEN CONTROL IS NOT FILLER. Case 1 lets the shim forward the walk to the
# real toolchain and keeps the dump it produced as the golden one for the rest, so
# every red case below runs against an enumeration that is genuinely valid. Without
# that control a wrapper that failed unconditionally would satisfy the whole red
# column.
#
# Exit 0 all cases behaved / 1 a case did not / 2 the battery could not run.
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WRAPPER="$ROOT/scripts/check-cli-ref-docs.sh"

die_cannot() {
	echo "cli-ref wrapper battery: CANNOT LOOK — $1" >&2
	echo "  A battery that could not run is not a battery that passed." >&2
	exit 2
}

[ -f "$WRAPPER" ] || die_cannot "the wrapper under test is missing at $WRAPPER."
REAL_GO="$(command -v go 2>/dev/null)" || true
[ -n "$REAL_GO" ] || die_cannot "no Go toolchain on PATH, so the wrapper's own stages were never exercised."

[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
SCRATCH="$(mktemp -d 2>/dev/null)" || die_cannot "could not create a scratch dir (TMPDIR=${TMPDIR:-unset})."
trap 'rm -rf "$SCRATCH"' EXIT

GOLDEN="$SCRATCH/golden.json"
SHIMDIR="$SCRATCH/shim"
mkdir -p "$SHIMDIR" || die_cannot "could not create the shim directory under $SCRATCH."

# The shim. Everything that is not `go test` is the real toolchain, so the
# wrapper's own `go build` of the generator is untouched and real.
cat >"$SHIMDIR/go" <<'SHIM'
#!/bin/sh
if [ "${1:-}" != "test" ]; then
	exec "$CLIREF_REAL_GO" "$@"
fi
case "${CLIREF_SHIM_MODE:-}" in
passthrough)
	"$CLIREF_REAL_GO" "$@"
	rc=$?
	if [ -s "${OLIVARES_CLIREF_DUMP_OUT:-}" ]; then
		cp "$OLIVARES_CLIREF_DUMP_OUT" "$CLIREF_GOLDEN" || exit 3
	fi
	exit $rc
	;;
good)
	cp "$CLIREF_GOLDEN" "$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.02s"
	exit 0
	;;
fail-but-write)
	# The shape that matters: t.Errorf does not stop the test, so a walk whose
	# assertions FAILED still writes a complete, valid dump. Only the exit code
	# testifies, which is exactly what mutant M8 threw away.
	cp "$CLIREF_GOLDEN" "$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	echo "--- FAIL: TestCLIRefDump (simulated: a flag takes its default from the environment)"
	echo "FAIL	github.com/olivaresai/olivares/cmd/olivares	0.02s"
	exit 1
	;;
pass-write-nothing)
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.01s"
	exit 0
	;;
pass-write-empty)
	: >"$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.01s"
	exit 0
	;;
drift)
	# A valid enumeration that disagrees with the published page in ONE cell. The
	# verdict must be 1 (drift), never 2 and never 0: the wrapper has to pass the
	# generator's three answers through distinctly.
	sed 's/"default": ""/"default": "zz-wrapper-battery-drift"/' "$CLIREF_GOLDEN" \
		>"$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	if cmp -s "$CLIREF_GOLDEN" "$OLIVARES_CLIREF_DUMP_OUT"; then
		# The substitution found nothing to change, so this case would be testing
		# nothing. Fail loudly rather than hand back an accidental green.
		echo "drift shim: no empty-string default left in the dump to perturb" >&2
		exit 3
	fi
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.02s"
	exit 0
	;;
*)
	echo "shim: unknown CLIREF_SHIM_MODE '${CLIREF_SHIM_MODE:-}'" >&2
	exit 3
	;;
esac
SHIM
chmod +x "$SHIMDIR/go" || die_cannot "could not make the shim executable under $SCRATCH (a noexec TMPDIR?)."

fails=0
cases=0

# report <name> <want-rc> <got-rc> <needle> <output>
report() {
	cases=$((cases + 1))
	local name="$1" want="$2" got="$3" needle="$4" out="$5" bad=0
	[ "$got" = "$want" ] || bad=1
	case "$out" in *"$needle"*) ;; *) bad=1 ;; esac
	if [ "$bad" -eq 0 ]; then
		printf '  ok   %-38s want rc=%s got rc=%s\n' "$name" "$want" "$got"
		return
	fi
	fails=$((fails + 1))
	printf '  FAIL %-38s want rc=%s got rc=%s\n' "$name" "$want" "$got"
	case "$out" in *"$needle"*) ;; *) printf '       expected the output to name "%s"\n' "$needle" ;; esac
	printf '%s\n' "$out" | sed 's/^/       | /'
}

# run_shim <name> <mode> <want-rc> <needle>
run_shim() {
	local name="$1" mode="$2" want="$3" needle="$4" out rc
	out="$(cd "$ROOT" && PATH="$SHIMDIR:$PATH" CLIREF_REAL_GO="$REAL_GO" \
		CLIREF_GOLDEN="$GOLDEN" CLIREF_SHIM_MODE="$mode" \
		bash "$WRAPPER" 2>&1)"
	rc=$?
	report "$name" "$want" "$rc" "$needle" "$out"
}

# ── 1. the green control, which also mints the golden dump ────────────────────────────
run_shim "walk-succeeds-page-in-sync" passthrough 0 "matches the binary"
[ -s "$GOLDEN" ] || die_cannot "the forwarded walk produced no golden dump, so every case below would have been testing the shim rather than the wrapper."

# ── 2. the two mutants the generator's own battery could not kill ─────────────────────
run_shim "walk-fails-but-wrote-a-valid-dump" fail-but-write 2 "the command-tree walk failed"
run_shim "walk-passes-and-writes-nothing" pass-write-nothing 2 "wrote no command tree"
run_shim "walk-passes-and-writes-empty" pass-write-empty 2 "wrote no command tree"

# ── 3. the three verdicts stay distinct ───────────────────────────────────────────────
run_shim "valid-walk-that-disagrees-is-drift" drift 1 "out of date with the command tree"

# ── 4. the wrapper's other refusals ───────────────────────────────────────────────────
out="$(cd "$ROOT" && bash "$WRAPPER" --not-a-mode 2>&1)"
report "unknown-argument" 2 "$?" "unknown argument" "$out"

# ── 5. locale copies ride the same wrapper as English, on a disposable tree ───────────
#
# The generator's fixture battery already plants these shapes. The wrapper is
# what public-counts actually runs, so one of each defect is replayed here
# against the real dump (the `good` shim) and COPIES of every page the imported
# helper JSON names, plus site-locales.mjs, exitcode.go and the generator
# source. The tracked working tree is not moved or edited. The golden dump from
# case 1 remains the one authoritative command roster; this does not rebuild
# cmd/olivares.
#
# The copy/hash roster is the helper's JSON, not a second list of languages.
# Named es/ja/fr/zh cases stay because they exercise existing editorial content.
# The Go generator's miniature fixture list is independent and is not used here.

FIX="$SCRATCH/pagefix"
HELPER="$ROOT/scripts/cli-ref-docs/published-locales.mjs"
ROSTER_JSON="$SCRATCH/source-roster.json"
SYNTH_LOCALE="zz-cli-ref-x"

fix_page() { printf '%s\n' "$FIX/docs-site/src/content/docs/$1/reference/cli.md"; }

import_roster() {
	local tree="$1" dest="$2" err rc
	command -v node >/dev/null 2>&1 ||
		die_cannot "no node on PATH, so the published locale roster was never imported."
	[ -f "$HELPER" ] ||
		die_cannot "published-locales.mjs is missing, so the fixture roster was never imported."
	err="$(node "$HELPER" "$tree" 2>&1 1>"$dest")"
	rc=$?
	if [ "$rc" -ne 0 ]; then
		die_cannot "could not import the published locale roster from $tree (exit $rc): $err"
	fi
	[ -s "$dest" ] || die_cannot "published-locales.mjs wrote no roster for $tree."
}

roster_field() {
	node -e '
var fs = require("fs");
var r = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
var k = process.argv[2];
if (!r || !Array.isArray(r[k])) process.exit(2);
r[k].forEach(function (x) {
	if (typeof x !== "string" || !x) process.exit(2);
	process.stdout.write(x + "\n");
});
' "$1" "$2" || die_cannot "locale roster JSON at $1 has no usable $2 array."
}

copy_cli_page() {
	local src="$1" dest="$2"
	mkdir -p "$(dirname -- "$dest")" || die_cannot "could not create $(dirname -- "$dest")."
	[ -f "$src" ] || die_cannot "declared CLI page missing at $src."
	cp "$src" "$dest" || die_cannot "could not copy $src."
}

published_cli_files() {
	local tree="$1"
	printf '%s\n' "$tree/docs-site/src/content/docs/reference/cli.md"
	while IFS= read -r lang; do
		[ -n "$lang" ] || continue
		printf '%s\n' "$tree/docs-site/src/content/docs/$lang/reference/cli.md"
	done < <(roster_field "$ROSTER_JSON" published)
}

hash_cli_files() {
	local f
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		[ -f "$f" ] || die_cannot "cannot hash missing $f."
		sha256sum "$f"
	done
}

install_page_fixture() {
	rm -rf "$FIX"
	mkdir -p "$FIX/scripts" \
		"$FIX/docs-site/src/content/docs/reference" \
		"$FIX/cmd/olivares/exitcode" ||
		die_cannot "could not create the disposable page fixture."
	cp "$WRAPPER" "$FIX/scripts/" || die_cannot "could not copy the wrapper into the fixture."
	cp -a "$ROOT/scripts/cli-ref-docs" "$FIX/scripts/" ||
		die_cannot "could not copy the generator source into the fixture."
	mkdir -p "$FIX/docs-site/src" || die_cannot "could not create docs-site/src in the fixture."
	cp "$ROOT/docs-site/src/site-locales.mjs" "$FIX/docs-site/src/site-locales.mjs" ||
		die_cannot "could not copy site-locales.mjs into the fixture."
	cp "$ROOT/docs-site/src/site-locales.mjs" "$FIX/docs-site/src/site-locales.canonical.mjs" ||
		die_cannot "could not copy the canonical site-locales snapshot into the fixture."
	cp "$ROOT/cmd/olivares/exitcode/exitcode.go" "$FIX/cmd/olivares/exitcode/" ||
		die_cannot "could not copy exitcode.go into the fixture."
	import_roster "$ROOT" "$ROSTER_JSON"
	copy_cli_page \
		"$ROOT/docs-site/src/content/docs/reference/cli.md" \
		"$FIX/docs-site/src/content/docs/reference/cli.md"
	while IFS= read -r lang; do
		[ -n "$lang" ] || continue
		copy_cli_page \
			"$ROOT/docs-site/src/content/docs/$lang/reference/cli.md" \
			"$FIX/docs-site/src/content/docs/$lang/reference/cli.md"
	done < <(roster_field "$ROSTER_JSON" published)
	while IFS= read -r slug; do
		[ -n "$slug" ] || continue
		src="$ROOT/docs-site/src/content/docs/$slug/reference/cli.md"
		[ -f "$src" ] || continue
		copy_cli_page "$src" "$FIX/docs-site/src/content/docs/$slug/reference/cli.md"
	done < <(roster_field "$ROSTER_JSON" archived)
}

run_fix() {
	local name="$1" mode="$2" want="$3" needle="$4" extra="${5:-}" out rc
	out="$(cd "$FIX" && PATH="$SHIMDIR:$PATH" CLIREF_REAL_GO="$REAL_GO" \
		CLIREF_GOLDEN="$GOLDEN" CLIREF_SHIM_MODE="$mode" \
		bash "$FIX/scripts/check-cli-ref-docs.sh" $extra 2>&1)"
	rc=$?
	report "$name" "$want" "$rc" "$needle" "$out"
}

install_page_fixture

# 5a. one locale region disagrees; English still matches the dump.
es_page="$(fix_page es)"
grep -q 'Nothing here is a stability promise' "$es_page" ||
	die_cannot "Spanish generated region missing the stability sentence to perturb."
sed -i 's/Nothing here is a stability promise/NOTHING here is a stability promise/' "$es_page"
run_fix "stale-locale-page-is-drift" good 1 "es/reference/cli.md"
install_page_fixture

# 5b. an expected locale page is gone: CANNOT LOOK, not in-sync.
rm -f "$(fix_page ja)"
run_fix "missing-locale-page-is-cannot-look" good 2 "ja/reference/cli.md"
install_page_fixture

# 5c. a second begin marker makes the region ambiguous.
printf '%s\n' '<!-- BEGIN GENERATED olivares-cli-reference -->' >>"$(fix_page fr)"
run_fix "duplicate-locale-marker-is-cannot-look" good 2 "ambiguous generated markers"
install_page_fixture

# 5d. --write on a malformed last locale must not rewrite the pages that loaded.
en_sum="$(sha256sum "$FIX/docs-site/src/content/docs/reference/cli.md")"
es_sum="$(sha256sum "$(fix_page es)")"
printf '%s\n' '<!-- END GENERATED olivares-cli-reference -->' >>"$(fix_page zh)"
run_fix "write-refuses-malformed-locale" good 2 "ambiguous generated markers" --write
en_sum2="$(sha256sum "$FIX/docs-site/src/content/docs/reference/cli.md")"
es_sum2="$(sha256sum "$(fix_page es)")"
if [ "$en_sum" != "$en_sum2" ] || [ "$es_sum" != "$es_sum2" ]; then
	report "write-refuses-malformed-locale-left-others" 0 1 "unchanged siblings" \
		"English or Spanish was rewritten despite a malformed zh page"
else
	report "write-refuses-malformed-locale-left-others" 0 0 "unchanged siblings" "unchanged siblings"
fi
install_page_fixture

# 5e. successful --write is idempotent on an in-sync tree (manual prose included).
before_tree="$(published_cli_files "$FIX" | hash_cli_files)"
arch_before=""
while IFS= read -r slug; do
	[ -n "$slug" ] || continue
	archf="$FIX/docs-site/src/content/docs/$slug/reference/cli.md"
	[ -f "$archf" ] || continue
	arch_before="${arch_before}$(sha256sum "$archf")
"
done < <(roster_field "$ROSTER_JSON" archived)
run_fix "write-all-published-pages" good 0 "wrote" --write
after_tree="$(published_cli_files "$FIX" | hash_cli_files)"
if [ "$before_tree" = "$after_tree" ]; then
	report "second-write-is-idempotent" 0 0 "identical bytes" "identical bytes"
else
	report "second-write-is-idempotent" 0 1 "identical bytes" "$before_tree"$'\n'"$after_tree"
fi
es_prose="$(fix_page es)"
if grep -q 'Olivares AI se distribuye como un único binario' "$es_prose" &&
	grep -q 'Referencia de CLI: olivares' "$es_prose"; then
	report "write-leaves-locale-manual-prose" 0 0 "preserved" "preserved"
else
	report "write-leaves-locale-manual-prose" 0 1 "preserved" \
		"Spanish editorial prose was missing after --write"
fi

# 5f. archived VERSIONS snapshots named by the same roster are not written.
if [ -n "$arch_before" ]; then
	arch_after=""
	while IFS= read -r slug; do
		[ -n "$slug" ] || continue
		archf="$FIX/docs-site/src/content/docs/$slug/reference/cli.md"
		[ -f "$archf" ] || continue
		arch_after="${arch_after}$(sha256sum "$archf")
"
	done < <(roster_field "$ROSTER_JSON" archived)
	if [ "$arch_before" = "$arch_after" ]; then
		report "write-leaves-archived-snapshot" 0 0 "unchanged archive" "unchanged archive"
	else
		report "write-leaves-archived-snapshot" 0 1 "unchanged archive" "archived CLI page changed"
	fi
fi

# 5g. a newly declared published locale is demanded automatically (actual import).
# The extra name is a synthetic locale that is not in the imported roster — never
# a reserved real language. It is added by a fixture module that re-exports the
# canonical LOCALES/VERSIONS objects, not by editing a presumed source line.
install_page_fixture
while IFS= read -r lang; do
	[ "$lang" = "$SYNTH_LOCALE" ] &&
		die_cannot "synthetic locale $SYNTH_LOCALE is already published; the extra-locale case would test nothing."
done < <(roster_field "$ROSTER_JSON" published)
while IFS= read -r slug; do
	[ "$slug" = "$SYNTH_LOCALE" ] &&
		die_cannot "synthetic locale $SYNTH_LOCALE is an archived slug; the extra-locale case would test nothing."
done < <(roster_field "$ROSTER_JSON" archived)
cat >"$FIX/docs-site/src/site-locales.mjs" <<EOF
import {
	LOCALES as CANONICAL_LOCALES,
	VERSIONS,
	OG_CLASS_CARDS,
} from './site-locales.canonical.mjs'

export const LOCALES = {
	...CANONICAL_LOCALES,
	'$SYNTH_LOCALE': { label: 'Synthetic extra locale', lang: '$SYNTH_LOCALE' },
}

export { VERSIONS, OG_CLASS_CARDS }

export const PUBLISHED_LOCALES = Object.keys(LOCALES).filter((l) => l !== 'root')
export const ARCHIVED_SLUGS = VERSIONS.map((v) => v.slug)
EOF
[ -s "$FIX/docs-site/src/site-locales.mjs" ] ||
	die_cannot "could not write the extra-locale fixture module."
run_fix "declared-eighth-locale-missing-page" good 2 "$SYNTH_LOCALE/reference/cli.md"

# 5h. the helper is not optional: without it the wrapper cannot import the roster.
install_page_fixture
rm -f "$FIX/scripts/cli-ref-docs/published-locales.mjs"
run_fix "published-locales-helper-missing" good 2 "published-locales.mjs is missing"

# 5i. an unreadable declaration is CANNOT LOOK, not in-sync.
install_page_fixture
printf '%s\n' 'this is not a module { LOCALES' >"$FIX/docs-site/src/site-locales.mjs"
run_fix "site-locales-unreadable-is-cannot-look" good 2 "could not import the published locale roster"

# A PATH without `go` rather than a hardcoded /usr/bin:/bin: the wrapper still needs
# dirname, mktemp and sed to reach its toolchain check at all, and where Go is installed
# is a property of the machine.
# ⛔ SE QUITA `go`, NO SU DIRECTORIO. La primera version descartaba del PATH cada directorio que
# tuviera un `go` ejecutable; en la imagen hospedada de GitHub (ubuntu-24.04, 2026-09-16, control-plane
# de olivares.preprod 35147069626) ese directorio es /usr/bin, donde tambien viven `bash` y `dirname`,
# y el caso murio con «bash: command not found» (rc 127) en vez de medir la respuesta del wrapper.
# Un caso que posee su entorno conserva el interprete y las utilidades que el sujeto necesita
# ANTES de comprobar Go (misma clase que el caso XDG de test-service-install.sh). Se construye un
# directorio sombra con un enlace a cada ejecutable del PATH menos `go` (la primera aparicion gana,
# como en el PATH), y ese directorio es TODO el PATH del hijo: el resultado no depende de en que
# directorio instale Go cada maquina.
NOGO_BIN="$SCRATCH/nogo-bin"
rm -rf "$NOGO_BIN" && mkdir -p "$NOGO_BIN"
IFS=':' read -r -a path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
	[ -n "$d" ] && [ -d "$d" ] || continue
	for f in "$d"/*; do
		[ -x "$f" ] && [ ! -d "$f" ] || continue
		b="$(basename -- "$f")"
		[ "$b" = go ] && continue
		[ -e "$NOGO_BIN/$b" ] || ln -s -- "$f" "$NOGO_BIN/$b"
	done
done
if [ -e "$NOGO_BIN/go" ] || ! [ -x "$NOGO_BIN/bash" ]; then
	echo "cli-ref wrapper battery: CANNOT LOOK — the no-go shadow PATH is wrong (go present: $([ -e "$NOGO_BIN/go" ] && echo yes || echo no); bash present: $([ -x "$NOGO_BIN/bash" ] && echo yes || echo no))" >&2
	exit 2
fi
out="$(cd "$ROOT" && PATH="$NOGO_BIN" bash "$WRAPPER" 2>&1)"
report "no-go-toolchain" 2 "$?" "no Go toolchain on PATH" "$out"

out="$(cd "$ROOT" && TMPDIR=/proc/zz-cli-ref-battery-nowhere bash "$WRAPPER" 2>&1)"
report "scratch-dir-uncreatable" 2 "$?" "could not create a scratch dir" "$out"

mkdir -p "$SCRATCH/orphan/scripts" && cp "$WRAPPER" "$SCRATCH/orphan/scripts/"
out="$(bash "$SCRATCH/orphan/scripts/check-cli-ref-docs.sh" 2>&1)"
report "generator-source-missing" 2 "$?" "the gate's own source is missing" "$out"

echo "cli-ref wrapper battery: $cases cases, $fails failed"
[ "$fails" -eq 0 ] || exit 1
exit 0
