#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Fixture battery for scripts/check-docs-parity.mjs.
#
# WHY FIXTURES AND NOT "run it on the repo". The repo tree is (by the end of) in
# parity, so running the checker against it proves only that a green tree stays green.
# Every property that matters is about the RED cases — a page that is missing, a waiver
# that has stopped meaning anything, a locale that was added to the manifest and never
# given a directory, a subtree the process cannot read. Each case below builds a
# miniature docs-site under a temp root and asserts BOTH the finding and the exit code.
#
# The locale and archive tokens are RANDOM per run. An earlier version always used the
# literal `it` and `2026-06`, so an implementation that hardcoded either string would
# have passed the battery while checking nothing.
#
# Properties under test:
#   1  a missing page is a finding, and --report still exits 0 (how it is wired today)
#   2  the same missing page fails --strict with exit 1
#   3  a valid waiver suppresses it, per locale, and is COUNTED not hidden
#   4  an archived version snapshot is IGNORED — under the root AND under each locale,
#      because starlight-versions copies every locale when it cuts one
#   5  a NEW locale added to the manifest is picked up with no code change
#   6  a locale with no directory reports locale-missing, not N page findings
#   7  an orphan (translated page whose English source vanished) is a finding, and it
#      is NOT waivable
#   8  a stale waiver (the page IS translated) is a finding
#   9  an expired waiver stops suppressing AND reports
#   10 malformed waivers are rejected field by field, including `"*"` and duplicates
#   11 a tree the checker cannot understand — or cannot READ, at any depth — is a HARD
#      ERROR (exit 2), never a silent green
#   12 route identity: .md vs .mdx is ONE route (format-drift, not missing+orphan),
#      `_`-prefixed files are ignored, and every extension Starlight loads is a page
#
# NO `set -e`: assertions REPORT through check(); `set -e` would turn the first failed
# assertion into a truncated run that looks like a clean tail (the failure mode
# scripts/test-pg-test-env.sh documents).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECKER="$ROOT/scripts/check-docs-parity.mjs"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-docsparity.XXXXXX")" || exit 1
cleanup() {
	chmod -R u+rwX "$WORK" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT HUP INT TERM

# Random tokens, so hardcoding any literal fails the battery.
NEWLOC="x$(od -An -N2 -tu2 </dev/urandom | tr -d ' ')"
ARCHIVE="19$(od -An -N1 -tu1 </dev/urandom | tr -d ' ')-snap"
TODAY="$(date -u +%F)"
YESTERDAY="$(date -u -d '1 day ago' +%F 2>/dev/null || date -u -v-1d +%F)"
TOMORROW="$(date -u -d '1 day' +%F 2>/dev/null || date -u -v+1d +%F)"
NEXTYEAR="$(date -u -d '365 days' +%F 2>/dev/null || date -u -v+365d +%F)"

pass=0
fail=0
# A failing row prints WHAT IT GOT, not only what it wanted. Without this the battery
# says `FAIL 10 rejected: no path   missing "path"` and stops there, so an intermittent
# red — measured 2026-08-01 at roughly 1 run in 12-20, in a DIFFERENT case each time
# (4, 8, 10, 12, 13) — cannot be told apart from a real regression, and re-running the
# captured fixture afterwards reproduces nothing because the tree was never at fault.
# The exit status is the discriminator: these rows assert `rc -eq 1`, and node exiting
# 2 (a hard error) or being unable to start at all fails them for a reason that has
# nothing to do with the property under test. That distinction is now on screen.
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-52s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-52s %s\n' "$1" "$2"
		printf '        assertion status=%s (this is what failed); checker rc=%s\n' \
			"$3" "${rc:-<unset>}"
		printf '        checker output:\n'
		printf '%s\n' "${out:-<no output captured>}" | sed 's/^/          /' | head -12
	fi
}

# --- fixture builders ----------------------------------------------------------------
# make_tree <dir> <locale...> — a site-locales manifest publishing the given locales
# plus one archived snapshot, and an English page set of three pages.
make_tree() {
	d="$1"
	shift
	mkdir -p "$d/docs-site/src/content/docs/explanation/adr" "$d/docs-site/src/content/docs/start"
	{
		printf 'export const LOCALES = {\n'
		printf "  root: { label: 'English', lang: 'en' },\n"
		for l in "$@"; do printf "  %s: { label: '%s', lang: '%s' },\n" "$l" "$l" "$l"; done
		printf '}\n'
		printf "export const PUBLISHED_LOCALES = Object.keys(LOCALES).filter((l) => l !== 'root')\n"
		printf "export const VERSIONS = [{ slug: '%s', label: 'snap' }]\n" "$ARCHIVE"
		printf 'export const ARCHIVED_SLUGS = VERSIONS.map((v) => v.slug)\n'
	} >"$d/docs-site/src/site-locales.mjs"
	printf -- '---\ntitle: home\n---\n' >"$d/docs-site/src/content/docs/index.mdx"
	printf -- '---\ntitle: adr25\n---\n' >"$d/docs-site/src/content/docs/explanation/adr/0025-x.md"
	printf -- '---\ntitle: install\n---\n' >"$d/docs-site/src/content/docs/start/install.md"
}

# fill_locale <dir> <locale> [page...] — default: every English page.
fill_locale() {
	d="$1"
	l="$2"
	shift 2
	src="$d/docs-site/src/content/docs"
	set -- "${@:-index.mdx explanation/adr/0025-x.md start/install.md}"
	for p in $*; do
		mkdir -p "$src/$l/$(dirname "$p")"
		printf -- '---\ntitle: x\n---\n' >"$src/$l/$p"
	done
}

waivers() { cat >"$1/docs-site/i18n-parity-waivers.json"; }

run() { # run <root> [flags...] -> stdout+stderr in $out, status in $rc
	r="$1"
	shift
	out="$(node "$CHECKER" --root "$r" "$@" 2>&1)"
	rc=$?
}

echo "check-docs-parity — fixture battery  (locale=$NEWLOC archive=$ARCHIVE)"

# --- 1/2 a missing page: reported in --report (exit 0), fatal in --strict ---------------
T="$WORK/t1"
make_tree "$T" es fr
fill_locale "$T" es
fill_locale "$T" fr index.mdx start/install.md # 0025 missing in fr only

run "$T"
[ "$rc" -eq 0 ]
check "1 report mode does not fail the gate" "exit 0" $?
grep -q 'missing (1)' <<<"$out"
check "1 the missing page is reported" "missing (1)" $?
grep -q 'fr/explanation/adr/0025-x.md' <<<"$out"
check "1 the finding names the exact path" "fr/.../0025-x.md" $?
grep -q 'not failing the gate' <<<"$out"
check "1 report mode says so out loud" "banner present" $?

run "$T" --strict
[ "$rc" -eq 1 ]
check "2 strict mode fails on the same tree" "exit 1" $?
grep -q 'FAILING' <<<"$out"
check "2 strict mode says it is failing" "FAILING" $?

# --- 3 a valid waiver suppresses exactly that page -------------------------------------
T="$WORK/t3"
make_tree "$T" es fr
fill_locale "$T" es
fill_locale "$T" fr index.mdx start/install.md
waivers "$T" <<JSON
{ "waivers": [
  { "path": "explanation/adr/0025-x.md", "locales": ["fr"],
    "reason": "French legal review pending on the reserve-ledger wording",
    "date": "$YESTERDAY", "expires": "$NEXTYEAR" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 0 ]
check "3 a valid waiver clears the finding" "exit 0" $?
# `missing (N)` is the finding-section header; the clean summary line legitimately
# contains "missing 0", so match the header, not the bare word.
grep -q 'missing (' <<<"$out"
[ $? -ne 0 ]
check "3 nothing is reported as missing" "no missing section" $?
grep -q 'or covered by one of 1 active waiver' <<<"$out"
check "3 the verdict does NOT claim full translation" "honest verdict" $?
run "$T" --summary
grep -q 'fr: 2 pages, missing 0, orphan 0, format-drift 0, waived 1' <<<"$out"
check "3 the waiver is counted, not hidden" "waived 1" $?

# A waiver for one locale must NOT leak to another.
T="$WORK/t3b"
make_tree "$T" es fr
fill_locale "$T" es index.mdx start/install.md
fill_locale "$T" fr index.mdx start/install.md
waivers "$T" <<JSON
{ "waivers": [
  { "path": "explanation/adr/0025-x.md", "locales": ["fr"],
    "reason": "French legal review pending on the reserve-ledger wording", "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 1 ]
check "3 a per-locale waiver does not leak" "es still fails" $?
grep -q 'es/explanation/adr/0025-x.md' <<<"$out"
check "3 the un-waived locale is the one named" "es reported" $?

# The waiver matches by ROUTE, so it covers the page whatever extension it carries.
T="$WORK/t3c"
make_tree "$T" es
fill_locale "$T" es explanation/adr/0025-x.md start/install.md
waivers "$T" <<JSON
{ "waivers": [
  { "path": "index.md", "locales": ["es"],
    "reason": "landing page is rendered from a shared component, nothing to translate",
    "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 0 ]
check "3 a waiver matches by route, not extension" "index.md waives index.mdx" $?

# --- 4 the archived version snapshot is ignored, on BOTH sides ---------------------------
T="$WORK/t4"
make_tree "$T" es
fill_locale "$T" es
mkdir -p "$T/docs-site/src/content/docs/$ARCHIVE/explanation/adr"
printf -- '---\ntitle: snap\n---\n' >"$T/docs-site/src/content/docs/$ARCHIVE/index.mdx"
printf -- '---\ntitle: snap\n---\n' >"$T/docs-site/src/content/docs/$ARCHIVE/explanation/adr/0025-x.md"
run "$T" --strict
[ "$rc" -eq 0 ]
check "4 root snapshot pages are not demanded" "exit 0" $?
grep -q "$ARCHIVE" <<<"$out"
check "4 the exclusion is stated in the output" "snapshot named" $?
run "$T" --json
grep -q '"english": 3' <<<"$out"
check "4 snapshot pages are out of the English set" "english 3" $?
# starlight-versions copies each LOCALE when it cuts a snapshot. Excluding only the
# root copy turned the localized snapshot into a tree of phantom orphans.
mkdir -p "$T/docs-site/src/content/docs/es/$ARCHIVE/explanation/adr"
printf -- '---\ntitle: snap\n---\n' >"$T/docs-site/src/content/docs/es/$ARCHIVE/index.mdx"
printf -- '---\ntitle: snap\n---\n' >"$T/docs-site/src/content/docs/es/$ARCHIVE/explanation/adr/0025-x.md"
run "$T" --strict
[ "$rc" -eq 0 ]
check "4 the LOCALIZED snapshot is ignored too" "exit 0" $?
grep -q 'orphan (' <<<"$out"
[ $? -ne 0 ]
check "4 it is not a tree of phantom orphans" "no orphan section" $?

# --- 5/6 a new locale in the manifest is picked up with no code change -------------------
T="$WORK/t5"
make_tree "$T" es fr "$NEWLOC" # the new locale has no directory at all
fill_locale "$T" es
fill_locale "$T" fr
run "$T" --json
grep -q "\"$NEWLOC\"" <<<"$out"
check "5 a new manifest locale is discovered" "$NEWLOC present" $?
run "$T" --strict
[ "$rc" -eq 1 ]
check "5 the new locale fails until translated" "exit 1" $?
grep -q 'locale-missing (1)' <<<"$out"
check "6 an absent locale dir is ONE finding" "locale-missing (1)" $?
grep -q 'missing (3)' <<<"$out"
[ $? -ne 0 ]
check "6 not one finding per page" "no per-page spam" $?

fill_locale "$T" "$NEWLOC"
run "$T" --strict
[ "$rc" -eq 0 ]
check "5 translating the new locale clears it" "exit 0" $?

# --- 7 an orphan translation (English source gone), and it is NOT waivable ---------------
T="$WORK/t7"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: old\n---\n' >"$T/docs-site/src/content/docs/es/start/removed.md"
run "$T" --strict
[ "$rc" -eq 1 ]
check "7 an orphan translation fails strict" "exit 1" $?
grep -q 'orphan (1)' <<<"$out"
check "7 it is reported as an orphan" "orphan (1)" $?
grep -q 'es/start/removed.md' <<<"$out"
check "7 the orphan path is named" "removed.md" $?
# Documented as unwaivable — pin it, because it is a promise the file makes to readers.
waivers "$T" <<JSON
{ "waivers": [
  { "path": "start/removed.md", "locales": ["es"],
    "reason": "attempting to mute a broken tree instead of fixing it", "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 1 ]
check "7 a waiver CANNOT suppress an orphan" "still exit 1" $?
grep -q 'orphan (1)' <<<"$out"
check "7 the orphan is still reported" "orphan (1)" $?

# --- 8 a stale waiver (the page IS translated) ------------------------------------------
T="$WORK/t8"
make_tree "$T" es
fill_locale "$T" es
waivers "$T" <<JSON
{ "waivers": [
  { "path": "explanation/adr/0025-x.md", "locales": ["es"],
    "reason": "Spanish review pending, tracked in the session log", "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 1 ]
check "8 a waiver that mutes nothing is a finding" "exit 1" $?
grep -q 'waiver-stale (1)' <<<"$out"
check "8 reported as waiver-stale" "waiver-stale (1)" $?
grep -q 'remove this waiver' <<<"$out"
check "8 the fix is spelled out" "remove this waiver" $?

# A waiver naming a page that is not English at all is stale too (renamed source).
T="$WORK/t8b"
make_tree "$T" es
fill_locale "$T" es
waivers "$T" <<JSON
{ "waivers": [
  { "path": "explanation/adr/9999-nonexistent.md", "locales": ["es"],
    "reason": "kept from an earlier layout that no longer exists here", "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
grep -q 'does not exist' <<<"$out"
check "8 a waiver for a vanished page is stale" "does not exist" $?

# --- 9 an expired waiver stops suppressing ----------------------------------------------
T="$WORK/t9"
make_tree "$T" es
fill_locale "$T" es index.mdx start/install.md
waivers "$T" <<'JSON'
{ "waivers": [
  { "path": "explanation/adr/0025-x.md", "locales": ["es"],
    "reason": "temporary hold while the reserve-ledger wording settles", "date": "2026-01-01",
    "expires": "2026-02-01" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 1 ]
check "9 an expired waiver fails strict" "exit 1" $?
grep -q 'waiver-expired (1)' <<<"$out"
check "9 reported as waiver-expired" "waiver-expired (1)" $?
grep -q 'missing (1)' <<<"$out"
check "9 AND the page is missing again" "missing (1)" $?

waivers "$T" <<JSON
{ "waivers": [
  { "path": "explanation/adr/0025-x.md", "locales": ["es"],
    "reason": "temporary hold while the reserve-ledger wording settles",
    "date": "$YESTERDAY", "expires": "$TOMORROW" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 0 ]
check "9 a future expiry still suppresses" "exit 0" $?

# --- 10 malformed waivers, field by field -----------------------------------------------
malformed() { # malformed <label> <json> <expected substring>
	T="$WORK/t10-$(printf '%s' "$1" | tr -c 'a-z0-9' '-')"
	make_tree "$T" es
	fill_locale "$T" es
	printf '%s' "$2" >"$T/docs-site/i18n-parity-waivers.json"
	run "$T" --strict
	[ "$rc" -eq 1 ] && grep -q "$3" <<<"$out"
	check "10 rejected: $1" "$3" $?
}
malformed "no path" '{"waivers":[{"locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'missing "path"'
malformed "no reason" '{"waivers":[{"path":"index.mdx","locales":["es"],"date":"2026-01-01"}]}' 'at least 20 characters'
malformed "short reason" '{"waivers":[{"path":"index.mdx","locales":["es"],"reason":"todo","date":"2026-01-01"}]}' 'at least 20 characters'
malformed "no date" '{"waivers":[{"path":"index.mdx","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa"}]}' 'real ISO YYYY-MM-DD'
malformed "bad date" '{"waivers":[{"path":"index.mdx","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"29-07-2026"}]}' 'real ISO YYYY-MM-DD'
# The SHAPE of a date is not a date. `2026-13-45` matches ^\d{4}-\d{2}-\d{2}$, and as an
# `expires` it compares lexicographically — it would sit in the future forever and the
# waiver would never expire. Built by the adversarial round (Codex sol/max).
malformed "impossible date" '{"waivers":[{"path":"index.mdx","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-13-45"}]}' 'real ISO YYYY-MM-DD'
malformed "impossible expires" '{"waivers":[{"path":"index.mdx","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01","expires":"2026-02-30"}]}' 'real ISO YYYY-MM-DD'
malformed "future grant date" "{\"waivers\":[{\"path\":\"index.mdx\",\"locales\":[\"es\"],\"reason\":\"aaaaaaaaaaaaaaaaaaaaaaaa\",\"date\":\"$TOMORROW\"}]}" 'in the future'
malformed "expires before date" '{"waivers":[{"path":"index.mdx","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-05-01","expires":"2026-04-01"}]}' 'must be after'
malformed "unknown locale" '{"waivers":[{"path":"index.mdx","locales":["qq"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'unknown locale'
malformed "empty locales" '{"waivers":[{"path":"index.mdx","locales":[],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'non-empty array'
# `"*"` would expand from the run-time locale list, silently covering locales added
# after the waiver was granted — an exemption nobody reviewed.
malformed "wildcard locales" '{"waivers":[{"path":"index.mdx","locales":"*","reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'silently cover locales added later'
malformed "escaping path" '{"waivers":[{"path":"../../etc/passwd","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'canonical'
malformed "non-canonical path" '{"waivers":[{"path":"./start//install.md","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'canonical'
malformed "backslash path" '{"waivers":[{"path":"start\\install.md","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"}]}' 'canonical'
# Two entries over the same (locale, route): both would count as "used", so neither
# could ever be reported stale and the file would accumulate dead mutes invisibly.
malformed "duplicate coverage" '{"waivers":[{"path":"start/install.md","locales":["es"],"reason":"aaaaaaaaaaaaaaaaaaaaaaaa","date":"2026-01-01"},{"path":"start/install.md","locales":["es"],"reason":"bbbbbbbbbbbbbbbbbbbbbbbb","date":"2026-01-02"}]}' 'duplicate coverage'

# A legal filename containing `..` must NOT be rejected — the old substring test did.
T="$WORK/t10-dots"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: v\n---\n' >"$T/docs-site/src/content/docs/start/v1..2.md"
waivers "$T" <<JSON
{ "waivers": [
  { "path": "start/v1..2.md", "locales": ["es"],
    "reason": "version-numbered page carrying only a redirect stub", "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 0 ]
check "10 a legal '..' inside a filename is fine" "exit 0" $?

# --- 11 hard errors: a checker that cannot see must not report green --------------------
T="$WORK/t11a"
make_tree "$T" es
fill_locale "$T" es
printf 'export default {}\n' >"$T/docs-site/src/site-locales.mjs"
run "$T"
[ "$rc" -eq 2 ]
check "11 a manifest with no exports is FATAL" "exit 2" $?
grep -q 'FATAL' <<<"$out"
check "11 and says FATAL" "FATAL" $?

T="$WORK/t11b"
make_tree "$T" es
fill_locale "$T" es
printf "export const LOCALES = { root: { label: 'English', lang: 'en' } }\nexport const PUBLISHED_LOCALES = []\nexport const VERSIONS = []\nexport const ARCHIVED_SLUGS = []\n" \
	>"$T/docs-site/src/site-locales.mjs"
run "$T"
[ "$rc" -eq 2 ]
check "11 zero published locales is FATAL" "exit 2" $?
grep -q 'ZERO published locales' <<<"$out"
check "11 it names the reason" "ZERO published locales" $?

T="$WORK/t11c"
make_tree "$T" es
fill_locale "$T" es
rm -rf "$T/docs-site/src/content/docs/explanation" "$T/docs-site/src/content/docs/start" \
	"$T/docs-site/src/content/docs/index.mdx" "$T/docs-site/src/content/docs/es"
run "$T"
[ "$rc" -eq 2 ]
check "11 an empty English tree is FATAL" "exit 2" $?

T="$WORK/t11d"
make_tree "$T" es
fill_locale "$T" es
printf '{ not json\n' >"$T/docs-site/i18n-parity-waivers.json"
run "$T"
[ "$rc" -eq 2 ]
check "11 an unparseable waiver file is FATAL" "exit 2" $?

T="$WORK/t11e"
make_tree "$T" es
fill_locale "$T" es
run "$T"
[ "$rc" -eq 0 ]
check "11 baseline before the unreadable subtree" "exit 0" $?
mkdir -p "$T/docs-site/src/content/docs/private"
printf -- '---\ntitle: p\n---\n' >"$T/docs-site/src/content/docs/private/only.md"
chmod 000 "$T/docs-site/src/content/docs/private"
run "$T"
rc_unreadable=$rc
chmod 755 "$T/docs-site/src/content/docs/private"
# Running as root defeats chmod; skip rather than assert a false pass.
if [ "$(id -u)" -eq 0 ]; then
	printf '  skip  %-52s %s\n' "11 an unreadable SUBTREE is FATAL" "running as root"
else
	[ "$rc_unreadable" -eq 2 ]
	check "11 an unreadable SUBTREE is FATAL" "exit 2, not silent green" $?
fi

T="$WORK/t11f"
make_tree "$T" es
fill_locale "$T" es
rm "$T/docs-site/src/site-locales.mjs"
run "$T"
[ "$rc" -eq 2 ]
check "11 a missing manifest is FATAL" "exit 2" $?

# --- 12 route identity ------------------------------------------------------------------
# Starlight derives an EXTENSIONLESS content id, so English .mdx + locale .md is ONE
# route. Reporting it as missing+orphan was the checker's own false positive.
T="$WORK/t12a"
make_tree "$T" es
fill_locale "$T" es explanation/adr/0025-x.md start/install.md
printf -- '---\ntitle: x\n---\n' >"$T/docs-site/src/content/docs/es/index.md" # .md vs .mdx
run "$T" --strict
# Starlight routes .md and .mdx identically and a locale may legitimately need no MDX
# syntax, so this is a convention NOTE, not a parity gap: reported, never a failure.
[ "$rc" -eq 0 ]
check "12 .md vs .mdx does not fail the gate" "exit 0" $?
grep -q 'format-drift' <<<"$out"
check "12 but it IS reported, once" "format-drift note" $?
grep -q 'NOT counted as findings' <<<"$out"
check "12 and is labelled as not a finding" "notes banner" $?
grep -q 'missing (' <<<"$out"
[ $? -ne 0 ]
check "12 not a phantom missing page" "no missing section" $?
grep -q 'orphan (' <<<"$out"
[ $? -ne 0 ]
check "12 not a phantom orphan" "no orphan section" $?

# Starlight's loader ignores `_`-prefixed files; demanding a translation of one is noise.
T="$WORK/t12b"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: wip\n---\n' >"$T/docs-site/src/content/docs/start/_work-in-progress.md"
run "$T" --strict
[ "$rc" -eq 0 ]
check "12 an _-prefixed file is not a page" "exit 0" $?

# ...and every extension that loader accepts IS a page.
T="$WORK/t12c"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: legacy\n---\n' >"$T/docs-site/src/content/docs/start/legacy.markdown"
run "$T" --strict
[ "$rc" -eq 1 ]
check "12 a .markdown page is a page" "exit 1" $?
grep -q 'start/legacy.markdown' <<<"$out"
check "12 and it is the one reported" "legacy.markdown" $?

# --- 13 route identity as Astro actually computes it ------------------------------------
# Astro's generateIdDefault(): a frontmatter `slug` wins; otherwise every path segment
# is github-slugged, joined with `/`, and a terminal `/index` is dropped. Starlight
# then drops `draft: true` pages in production. Approximating that with "strip the
# extension" produced BOTH silent gaps and false positives; every case here is one of
# them, built by the adversarial round.
T="$WORK/t13a"
make_tree "$T" es
fill_locale "$T" es
mkdir -p "$T/docs-site/src/content/docs/topic"
printf -- '---\ntitle: t\n---\n' >"$T/docs-site/src/content/docs/topic/index.md"
printf -- '---\ntitle: t\n---\n' >"$T/docs-site/src/content/docs/es/topic.md" # same route
run "$T" --strict
[ "$rc" -eq 0 ]
check "13 topic/index.md == topic.md is ONE route" "exit 0" $?

T="$WORK/t13b"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: g\nslug: english-route\n---\n' >"$T/docs-site/src/content/docs/start/guide.md"
printf -- '---\ntitle: g\nslug: ruta-espanola\n---\n' >"$T/docs-site/src/content/docs/es/start/guide.md"
run "$T" --strict
[ "$rc" -eq 1 ]
check "13 a divergent frontmatter slug is a gap" "exit 1" $?
grep -q 'missing (1)' <<<"$out"
check "13 the English route has no translation" "missing (1)" $?

T="$WORK/t13c"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: d\ndraft: true\n---\n' >"$T/docs-site/src/content/docs/start/preview.md"
run "$T" --strict
[ "$rc" -eq 0 ]
check "13 an English draft is not demanded" "exit 0" $?
# ...but a DRAFT translation is not published, so the route really is untranslated.
printf -- '---\ntitle: d\n---\n' >"$T/docs-site/src/content/docs/start/preview.md"
printf -- '---\ntitle: d\ndraft: true\n---\n' >"$T/docs-site/src/content/docs/es/start/preview.md"
run "$T" --strict
[ "$rc" -eq 1 ]
check "13 a DRAFT translation does not count" "exit 1" $?
grep -q 'missing (1)' <<<"$out"
check "13 it is reported as missing" "missing (1)" $?

T="$WORK/t13d"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: c\n---\n' >"$T/docs-site/src/content/docs/start/Case Study.md"
printf -- '---\ntitle: c\n---\n' >"$T/docs-site/src/content/docs/es/start/case-study.md"
run "$T" --strict
[ "$rc" -eq 0 ]
check "13 segments are github-slugged like Astro" "exit 0" $?

T="$WORK/t13e"
make_tree "$T" es
fill_locale "$T" es
printf -- '---\ntitle: a\n---\n' >"$T/docs-site/src/content/docs/start/Install.md" # -> start/install
run "$T" --strict
[ "$rc" -eq 1 ]
check "13 two files on one route is a finding" "exit 1" $?
grep -q 'route-collision' <<<"$out"
check "13 reported as route-collision" "route-collision" $?

# --- 14 every Starlight extension, parameterized ----------------------------------------
# The battery used to name only `.markdown`, so an implementation recognising just
# md/mdx/markdown still passed the "every extension" claim.
for ext in md mdx markdown mdown mkdn mkd mdwn; do
	T="$WORK/t14-$ext"
	make_tree "$T" es
	fill_locale "$T" es
	printf -- '---\ntitle: e\n---\n' >"$T/docs-site/src/content/docs/start/extra.$ext"
	run "$T" --strict
	[ "$rc" -eq 1 ] && grep -q "start/extra.$ext" <<<"$out"
	check "14 .$ext is a page Starlight would load" "reported missing" $?
done

# --- 15 the manifest boundary really is fail-closed --------------------------------------
# `PUBLISHED_LOCALES = ['.']` made path.join(contentRoot, '.') the content root, so the
# "locale" walk and the English walk inspected the SAME tree — a clean, fully-translated
# verdict for a site with no translations at all.
unsafe_manifest() { # unsafe_manifest <label> <manifest body> <expected substring>
	T="$WORK/t15-$(printf '%s' "$1" | tr -c 'a-z0-9' '-')"
	make_tree "$T" es
	fill_locale "$T" es
	printf -- '---\ntitle: u\n---\n' >"$T/docs-site/src/content/docs/start/only.md"
	printf '%s\n' "$2" >"$T/docs-site/src/site-locales.mjs"
	run "$T"
	[ "$rc" -eq 2 ] && grep -q "$3" <<<"$out"
	check "15 rejected manifest: $1" "$3" $?
}
unsafe_manifest "dot locale" \
	"export const LOCALES = { root: {}, '.': {} }
export const PUBLISHED_LOCALES = ['.']
export const VERSIONS = []
export const ARCHIVED_SLUGS = []" 'not a safe directory name'
unsafe_manifest "escaping locale" \
	"export const LOCALES = { root: {}, 'es/..': {} }
export const PUBLISHED_LOCALES = ['es/..']
export const VERSIONS = []
export const ARCHIVED_SLUGS = []" 'not a safe directory name'
unsafe_manifest "unsafe archive slug" \
	"export const LOCALES = { root: {}, es: {} }
export const PUBLISHED_LOCALES = ['es']
export const VERSIONS = [{ slug: '../escape' }]
export const ARCHIVED_SLUGS = ['../escape']" 'not a safe directory name'
unsafe_manifest "locale is also an archive slug" \
	"export const LOCALES = { root: {}, es: {} }
export const PUBLISHED_LOCALES = ['es']
export const VERSIONS = [{ slug: 'es' }]
export const ARCHIVED_SLUGS = ['es']" 'both a locale and an archive slug'
unsafe_manifest "projection disagrees with LOCALES" \
	"export const LOCALES = { root: {}, es: {}, de: {} }
export const PUBLISHED_LOCALES = ['es']
export const VERSIONS = []
export const ARCHIVED_SLUGS = []" 'disagrees with LOCALES'
unsafe_manifest "projection disagrees with VERSIONS" \
	"export const LOCALES = { root: {}, es: {} }
export const PUBLISHED_LOCALES = ['es']
export const VERSIONS = [{ slug: 'v1' }]
export const ARCHIVED_SLUGS = []" 'disagrees with the VERSIONS slugs'

# --- 16 a waiver must name a real page file ---------------------------------------------
# Matching is by route, so `guide.xyz` resolved to the same route as `guide.md` and
# quietly suppressed it while appearing to refer to something else.
T="$WORK/t16"
make_tree "$T" es
fill_locale "$T" es index.mdx explanation/adr/0025-x.md
waivers "$T" <<JSON
{ "waivers": [
  { "path": "start/install.xyz", "locales": ["es"],
    "reason": "a plausible-looking path that is not a page file at all", "date": "$YESTERDAY" }
] }
JSON
run "$T" --strict
[ "$rc" -eq 1 ]
check "16 a non-page waiver path is rejected" "exit 1" $?
grep -q 'must name a page file' <<<"$out"
check "16 and says why" "must name a page file" $?

# --- 9 INFORMED PARITY (F-2026-08-01-D) ------------------------------------------
# US-English-only authoring, and the translations that already exist are NOT removed. So
# --informed must let the English-only backlog through and keep everything else blocking.
# The whole value of this mode is the SPLIT, so every row below is a red one: if the
# classifier collapsed either way, some row here goes the wrong colour.

# 9a a page missing in EVERY locale is the backlog: reported, not blocking.
T="$WORK/t9a"
make_tree "$T" es fr
fill_locale "$T" es index.mdx start/install.md
fill_locale "$T" fr index.mdx start/install.md # 0025 missing in BOTH locales
run "$T" --informed
[ "$rc" -eq 0 ]
check "9 english-only page does NOT block in --informed" "exit 0" $?
case "$out" in *"no translation in ANY locale"*) true ;; *) false ;; esac
check "9 the backlog is reported out loud" "backlog stated" $?
case "$out" in *"explanation/adr/0025-x"*) true ;; *) false ;; esac
check "9 the backlog names the page" "route named" $?
# ...and the SAME tree is still fatal under --strict: the mode changed, not the facts.
run "$T" --strict
[ "$rc" -eq 1 ]
check "9 the same tree still fails under --strict" "exit 1" $?

# 9b PARTIAL coverage is a regression, not backlog: it WAS being translated.
T="$WORK/t9b"
make_tree "$T" es fr
fill_locale "$T" es
fill_locale "$T" fr index.mdx start/install.md # 0025 missing in fr ONLY
run "$T" --informed
[ "$rc" -eq 1 ]
check "9 a page missing in SOME locales BLOCKS in --informed" "exit 1" $?
case "$out" in *"BLOCKING"*) true ;; *) false ;; esac
check "9 it is named a blocking finding" "blocking stated" $?
# Left as it is ON PURPOSE: `|| true` discards this line's status and the next line
# overwrites rc, so it asserts nothing and a 141 here cannot reach any check. The four
# pipes that DID feed a boolean are gone; this one is a marker, not a test.
printf '%s' "$out" | grep -qv "no translation in ANY locale" || true
case "$out" in *"0 English page(s) with no translation"*) rc=1 ;; *) rc=0 ;; esac
[ "$rc" -eq 0 ]
check "9 a partial page is NOT counted as backlog" "not backlog" $?

# 9c an ORPHAN stays blocking — a translation whose English source went away is the
# deletion direction, and nothing about English-only authoring excuses it.
T="$WORK/t9c"
make_tree "$T" es fr
fill_locale "$T" es
fill_locale "$T" fr
mkdir -p "$T/docs-site/src/content/docs/es/gone"
printf -- '---\ntitle: g\n---\n' >"$T/docs-site/src/content/docs/es/gone/page.md"
run "$T" --informed
[ "$rc" -eq 1 ]
check "9 an orphaned translation BLOCKS in --informed" "exit 1" $?

# 9d "could not look" is still exit 2 in --informed: a mode that reports less must not
# report a broken tree as a clean one.
T="$WORK/t9d"
make_tree "$T" es fr
fill_locale "$T" es
fill_locale "$T" fr
printf 'not json at all' >"$T/docs-site/i18n-parity-waivers.json"
run "$T" --informed
[ "$rc" -eq 2 ]
check "9 a broken waiver file is UNVERIFIED (exit 2), never a pass" "exit 2" $?

# 9e the vacuity guard survives the new mode: an empty English tree is exit 2, not a
# clean bill of health. A scan that saw nothing must never report nothing found.
T="$WORK/t9e"
make_tree "$T" es fr
rm -f "$T/docs-site/src/content/docs/index.mdx" \
	"$T/docs-site/src/content/docs/explanation/adr/0025-x.md" \
	"$T/docs-site/src/content/docs/start/install.md"
run "$T" --informed
[ "$rc" -eq 2 ]
check "9 an empty English tree is exit 2 in --informed too" "exit 2" $?

# --- the repository's own waiver file must be valid ------------------------------------
node -e '
const fs=require("fs");
const j=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));
if(!Array.isArray(j.waivers)) process.exit(1);
' "$ROOT/docs-site/i18n-parity-waivers.json"
check "repo waiver file parses with a waivers array" "docs-site/i18n-parity-waivers.json" $?

echo
echo "check-docs-parity fixtures: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
