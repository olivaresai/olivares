#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Verify edition routing through the shipped Taskfile and dispatcher.
# Preserve all applicable public checks and the complete private sequence.
# Exercise classifier failures and a real public grants-binding defect.
# Exit0: all cases pass. Exit1: regression. Exit2: fixture cannot be verified.
set -u -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || { echo "test-addon-sets-scope: COULD NOT LOOK: no cd a ${ROOT}" >&2; exit 2; }

PASS=0
FAIL=0
say_ok()   { printf 'ok   %-60s %s\n' "$1" "${2:-}"; PASS=$((PASS + 1)); }
say_fail() { printf 'FAIL %-60s expected %s, got %s\n' "$1" "$2" "$3" >&2; FAIL=$((FAIL + 1)); }
cannot()   { echo "test-addon-sets-scope: COULD NOT LOOK — $*" >&2; exit 2; }

# ── layer 1: the shipping wiring, read and pinned ────────────────────────────────────────────
# -x, not a substring match: 'lint:addon-sets' is a PREFIX of 'lint:addon-sets-gate', and the
# sibling wrapper's line would count as a second copy of this one.
DISPATCH_LINE1='        bash scripts/edition-split-gate.sh lint:addon-sets'
DISPATCH_LAST='        lint:addon-sets:public lint:addon-sets:legs'
for needle in "${DISPATCH_LINE1}" "${DISPATCH_LAST}"; do
	n="$(command grep -cxF -- "${needle}" Taskfile.yml)" || n=0
	[ "${n}" -eq 1 ] || cannot "Taskfile.yml carries ${n} copies of '${needle}' (expected 1); the dispatcher moved"
done

# The two task bodies, by entry, straight out of the file that ships.
entries_of() {
	python3 - "$1" <<'PY'
import re, sys
name = sys.argv[1]
src = open('Taskfile.yml', encoding='utf-8').read().split('\n')
try:
    s = src.index('  %s:' % name)
except ValueError:
    sys.exit(3)
e = s + 1
while e < len(src) and not re.match(r'^  [a-z][a-zA-Z0-9_:.-]*:\s*$', src[e]):
    e += 1
for line in src[s:e]:
    m = re.match(r'^      - (.*)$', line)
    if m:
        print(m.group(1))
PY
}

PUBLIC_ENTRIES="$(entries_of lint:addon-sets:public)" || cannot "lint:addon-sets:public is not in Taskfile.yml"
PRIVATE_COUNT="$(entries_of lint:addon-sets:legs | wc -l)" || cannot "lint:addon-sets:legs is not in Taskfile.yml"
PRIVATE_COUNT="$(printf '%s' "${PRIVATE_COUNT}" | tr -d ' ')"

# ⛔ THE EIGHT ARE NAMED HERE, and that is deliberate. Root separated them from the 15 entries
# that already answer for themselves; if a future edit drops one, the leg would go green in the
# export having checked less, and nothing else in the tree would notice. This list is the
# expectation, so dropping one is a red and adding one is a red that says "update the battery".
EXPECTED_PUBLIC="$(cat <<'EOF'
bash scripts/check-c03-grants-seam.sh
bash scripts/check-json-decoders.sh
OLIVARES_CONFIG_DOC_FLOOR=${OLIVARES_CONFIG_DOC_FLOOR:-53} bash scripts/check-config-coverage.sh
OLIVARES_CLI_DOC_FLOOR=${OLIVARES_CLI_DOC_FLOOR:-321} bash scripts/check-cli-coverage.sh
bash scripts/check-caep-inbound.sh
bash scripts/check-aws-estate.sh
bash scripts/check-c03-02-sign-features.sh
bash scripts/check-aws-estate.sh
EOF
)"
if [ "${PUBLIC_ENTRIES}" = "${EXPECTED_PUBLIC}" ]; then
	say_ok "CASE 0: the eight applicable entries are intact, in order" "8 entries"
else
	say_fail "CASE 0: the eight applicable entries are intact, in order" "the 8 named here" "a different list"
	diff <(printf '%s\n' "${EXPECTED_PUBLIC}") <(printf '%s\n' "${PUBLIC_ENTRIES}") >&2 || true
fi

# The private sequence must not shrink: routing must never become a filter on the full source tree.
if [ "${PRIVATE_COUNT}" = "221" ]; then
	say_ok "CASE 0b: the private sequence is still 221 entries" "221"
else
	say_fail "CASE 0b: the private sequence is still 221 entries" "221" "${PRIVATE_COUNT}"
fi

# Every applicable entry must ALSO be in the private sequence: the public task is a
# SUBSEQUENCE, not a second list that can drift away from what the full source tree runs.
PRIVATE_ENTRIES="$(entries_of lint:addon-sets:legs)"
# ⛔ NO `printf … | grep -q`. Under `pipefail` grep -q exits on its first match, printf dies of
# SIGPIPE and the PIPELINE returns 141 ON SUCCESS — the exact shape lint:sigpipe-booleans
# refuses, and it caught this file. `case` over the newline-delimited string asks the same
# question (is this line present, whole?) with no pipeline and no early reader.
_haystack="
${PRIVATE_ENTRIES}
"
_missing=""
while IFS= read -r _e; do
	[ -n "${_e}" ] || continue
	case "${_haystack}" in
		*"
${_e}
"*) ;;
		*) _missing="${_missing}${_e} " ;;
	esac
done <<EOF
${PUBLIC_ENTRIES}
EOF
if [ -z "${_missing}" ]; then
	say_ok "CASE 0c: every applicable entry is also in the private sequence" "subsequence"
else
	say_fail "CASE 0c: every applicable entry is also in the private sequence" "all 8 present" "missing: ${_missing}"
fi

# ── layer 2 and 3: real trees, the real dispatcher, real source ──────────────────────────────
# An ARRAY, not a string the caller has to word-split: this repository runs zsh interactively
# and an unquoted expansion there iterates ONCE over the whole string while looking like a loop.
GRANTS_SOURCES=(
	cmd/olivares/license_holder.go
	cmd/olivares/boot.go
	cmd/olivares/wire_noenterprise.go
	cmd/olivares/seatcapwire.go
)
FIXTURE_SCRIPTS=(
	scripts/hub-leg.sh
	scripts/edition-split-gate.sh
	scripts/check-c03-grants-seam.sh
)
for f in "${FIXTURE_SCRIPTS[@]}" "${GRANTS_SOURCES[@]}"; do
	[ -r "${f}" ] || cannot "no ${f}; the fixture cannot carry the real gate and its subject"
done
command -v task >/dev/null 2>&1 || cannot "go-task is not on PATH; the dispatcher execs it"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/addon-sets-scope.XXXXXX")" || cannot "no scratch directory"
trap 'rm -rf "${WORK}"' EXIT

MARKER_SENTENCE="$(bash scripts/hub-leg.sh --marker-signature)" || cannot "hub-leg.sh --marker-signature failed"

# A fixture is a REAL tree with the real dispatcher, the real classifier, the real grants gate
# and the real four Go files it greps. The private branch writes a witness instead of running
# 221 scripts: what this layer measures is WHICH branch ran, and a witness says that exactly.
build_fixture() {
	local dir="$1" marker_mode="$2" sentinel="$3"
	rm -rf "${dir}"
	mkdir -p "${dir}/scripts" "${dir}/cmd/olivares" || return 1
	cp "${FIXTURE_SCRIPTS[@]}" "${dir}/scripts/" || return 1
	cp "${GRANTS_SOURCES[@]}" "${dir}/cmd/olivares/" || return 1

	case "${marker_mode}" in
		signed)   printf '%s\n' "${MARKER_SENTENCE}" > "${dir}/PUBLIC-EXPORT.md" ;;
		unsigned) printf '%s\n' "this is the public export, honest" > "${dir}/PUBLIC-EXPORT.md" ;;
		none)     : ;;
	esac
	if [ -n "${sentinel}" ]; then
		mkdir -p "${dir}/$(dirname "${sentinel}")" || return 1
		: > "${dir}/${sentinel}" || return 1
	fi

	{
		printf 'version: "3"\n\ntasks:\n'
		printf '  lint:addon-sets:\n    cmds:\n      - >-\n'
		printf '%s\n' "${DISPATCH_LINE1}"
		printf '        "the pricing canon and the commercial programme derived from it\n'
		printf '        (design/PRICING-CANON.md, commercial/, cloud/control-plane)"\n'
		printf '%s\n' "${DISPATCH_LAST}"
		printf '  lint:addon-sets:public:\n    cmds:\n'
		# ⛔ THE WITNESS GOES FIRST, and this cost a run to learn. With it last, CASE 7 — the
		# case this battery exists for — recorded branch=none, because the real gate failed
		# before the witness was written. A branch marker that only appears on success cannot
		# tell "the public branch ran and caught a defect" from "no branch ran at all". The
		# witness records WHICH branch; the gate's own line in the output records WHAT it found.
		printf '      - touch ran-public\n'
		# The FIRST applicable entry, taken from the shipping file rather than retyped.
		printf '      - %s\n' "${PUBLIC_ENTRIES%%$'\n'*}"
		printf '  lint:addon-sets:legs:\n    cmds:\n      - touch ran-private\n'
	} > "${dir}/Taskfile.yml" || return 1
	return 0
}

LAST_DIR=""
LAST_RC=0
# ⛔ COUNTS IN THIS SHELL. An earlier version returned the fixture's output on stdout, so every
# caller wrapped it in `$(...)` — a subshell, where PASS and FAIL were incremented on copies
# that died with it. A failing case printed FAIL and left the totals at zero: the silent green
# this battery exists to deny, reproduced inside it.
run_case() {
	local name="$1" expect_branch="$2" expect_verdict="$3" marker_mode="$4" sentinel="$5" mutate="${6:-}"
	local dir="${WORK}/$(printf '%s' "${name}" | tr -c 'a-zA-Z0-9' '_')"
	build_fixture "${dir}" "${marker_mode}" "${sentinel}" || cannot "fixture '${name}' could not be built"
	# A failed producer's partial stdout is never a valid classification.
	case "${7:-}" in
		public-error) printf 'printf "public\\n"\nexit 2\n' > "${dir}/scripts/hub-leg.sh" ;;
		empty-error) printf 'exit 2\n' > "${dir}/scripts/hub-leg.sh" ;;
		missing) rm "${dir}/scripts/hub-leg.sh" ;;
	esac
	if [ -n "${mutate}" ]; then
		# A REAL defect in REAL public source: the binding the grants seam exists to require.
		command sed -i 's/bindEnterpriseEntitlement(licHolder\.grants)/bindEnterpriseEntitlement(nil)/' \
			"${dir}/cmd/olivares/boot.go" || cannot "could not mutate boot.go in '${name}'"
		command grep -q 'bindEnterpriseEntitlement(nil)' "${dir}/cmd/olivares/boot.go" \
			|| cannot "the mutation did not apply in '${name}'; the seam's source moved"
	fi
	LAST_DIR="${dir}"; LAST_RC=0
	( cd "${dir}" && task lint:addon-sets ) > "${dir}/.output" 2>&1 || LAST_RC=$?

	local ran_pub=0 ran_priv=0 branch
	[ -f "${dir}/ran-public" ] && ran_pub=1
	[ -f "${dir}/ran-private" ] && ran_priv=1
	if [ "${ran_pub}" -eq 1 ] && [ "${ran_priv}" -eq 1 ]; then branch="BOTH"
	elif [ "${ran_pub}" -eq 1 ]; then branch="public"
	elif [ "${ran_priv}" -eq 1 ]; then branch="private"
	else branch="none"; fi

	local verdict="ok"
	[ "${LAST_RC}" -eq 0 ] || verdict="red"

	if [ "${branch}" = "${expect_branch}" ] && [ "${verdict}" = "${expect_verdict}" ]; then
		say_ok "${name}" "branch=${branch} rc=${LAST_RC}"
	else
		say_fail "${name}" "branch=${expect_branch} ${expect_verdict}" "branch=${branch} rc=${LAST_RC}"
		sed 's/^/       /' "${dir}/.output" >&2
	fi
}

echo
echo "== routing: five trees, one dispatcher, and only one answer takes the short list =="

# CASE 1 — the published tree: the applicable entries RUN and pass over clean public source.
run_case "CASE 1: public export RUNS the applicable entries" public ok signed ""
if command grep -q 'edition-split-gate: PARTIALLY APPLICABLE' "${LAST_DIR}/.output" \
   && command grep -q 'check-c03-grants-seam: CLEAN' "${LAST_DIR}/.output"; then
	say_ok "CASE 1b: it says what it did NOT check, and the real gate spoke" "both lines present"
else
	say_fail "CASE 1b: it says what it did NOT check, and the real gate spoke" "both lines" "see output"
	sed 's/^/       /' "${LAST_DIR}/.output" >&2
fi

# CASE 2 — a hub. One sentinel path is enough, and the private sequence must run.
#
# The sentinel is passed as DATA: build_fixture writes an EMPTY file of that name inside the
# scratch tree so the real classifier answers "hub". Nothing here reads, sources or runs the
# exporter, and it could not: the export removes scripts/export-public.sh because that script
# carries the private-token denylist verbatim and would trip the export's own leak gate. So a
# published caller can never project an export; it can only NAME the path the projection drops
# — which is exactly what hub-leg.sh does at scripts/hub-leg.sh:59, and this case is its test.
# Keeping the real sentinel string is the point of the case: a stand-in would stop proving that
# THIS path still classifies a tree as a hub. The case therefore runs unchanged in both trees.
#
# export-closure: absent-by-design scripts/export-public.sh — a SENTINEL passed as fixture
#   data, not a dependency: this battery writes an empty file of that name into a scratch
#   tree to make the real classifier answer "hub", and never hands it to an execution verb.
#   The full source tree has it and the export removes it on purpose; that is the fact under test.
run_case "CASE 2: a hub runs the private sequence" private ok signed scripts/export-public.sh

# CASE 3 — THE SOURCE-SENSITIVE MUTATION of the discriminator: right NAME, wrong CONTENT. A
# marker anybody can copy must not buy the short list (adversarial review X-07).
run_case "CASE 3: a marker without the generator's sentence is not public" private ok unsigned ""

# CASE 4 — neither stamped nor a hub: unknown, and unknown is not public.
run_case "CASE 4: an unclassifiable tree runs the private sequence" private ok none ""

# CASE 5 — MIXED: the stamped marker AND a hub-only path, the half-copied tree. Sentinel wins.
run_case "CASE 5: marker plus a hub-only path is a hub, not an export" private ok signed design/PRICING-CANON.md

run_case "CASE 5b: public output with a failed classifier runs private" private ok signed "" "" public-error
run_case "CASE 5c: a silent failed classifier runs private" private ok signed "" "" empty-error
run_case "CASE 5d: a missing classifier runs private" private ok signed "" "" missing

echo
echo "== the case that carries this battery: a real defect must fail the leg IN THE EXPORT =="

# CASE 6 — the control positive for CASE 7: same fixture, same classification, clean source.
run_case "CASE 6: control positive — clean public source is green" public ok signed ""

# CASE 7 — ONE LINE of real cmd/olivares source changed, in a tree that classifies as the
# public export. If the leg were merely skipped there, this would pass and nobody would know
# the published tree had stopped being checked. It is the whole reason the leg routes.
run_case "CASE 7: a real grants-seam defect FAILS the leg in the export" public red signed "" mutate
if command grep -q 'check-c03-grants-seam: FAIL' "${LAST_DIR}/.output"; then
	say_ok "CASE 7b: and the failure is the gate's own, named" "grants-seam FAIL"
else
	say_fail "CASE 7b: and the failure is the gate's own, named" "a grants-seam FAIL line" "see output"
	sed 's/^/       /' "${LAST_DIR}/.output" >&2
fi

echo
if [ "${FAIL}" -ne 0 ]; then
	echo "test-addon-sets-scope: ${PASS} passed, ${FAIL} FAILED" >&2
	exit 1
fi
echo "test-addon-sets-scope: ${PASS} passed, 0 failed — the export is routed, not skipped"
exit 0
