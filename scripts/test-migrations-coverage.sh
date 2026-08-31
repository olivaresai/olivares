#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-migrations-coverage.sh — battery for scripts/check-migrations.sh.
#
# WHY THIS FILE EXISTS AT ALL, and why it is written against the census rather than against the SQL
# rules. This gate has been wrong twice, in two different repositories of the same tree, and neither
# time was the SQL logic at fault:
#
#   ·  a four-digit-only glob left 30 real Postgres migrations unread while the gate printed
#      "✓ 55 migration(s)" and exited 0 — a confident number about the wrong set;
#   ·  a raw `find .` census then graded whatever happened to be lying in the working tree: measured
#      2026-08-14 the same script graded 55 files in a clean worktree and 163 in the shared clone,
#      96 of them from another lane's `.claude/worktrees/wf_*` and 12 from a directory a different
#      gate CREATES earlier in the same hook run.
#
# Both were fixed. Neither had a test, so both fixes were themselves unverified, and a third
# implementation was in flight when this was written. A gate whose subject can silently change size
# needs its SUBJECT pinned, not just its rules.
#
# Every fixture here is a real git repository, because since 2026-08-14 the census IS the index and
# a fixture that is a plain directory would exercise a code path production never takes.
set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT. Git EXPORTA `GIT_DIR` a los hooks desde todo worktree ENLAZADO
# —o sea, desde cualquier sesion en paralelo— y `GIT_DIR` MANDA SOBRE `-C`: sin sanear, los
# repositorios desechables que construye este banco son el repositorio VIVO de quien lo invoque.
# MEDIDO el 2026-08-30 contra un repositorio de destino desechable, con este mismo fichero y sin
# esta linea: el destino recibio COMMITS. Falla cerrado: no poder aislar es «no he podido».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate
GATE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-migrations.sh"
cases=0; fails=0
BASE="$(mktemp -d)"; trap 'chmod -R u+rwX "$BASE" 2>/dev/null; rm -rf "$BASE"' EXIT

# new_repo builds a minimal tree the gate must grade GREEN: one additive migration in a graded
# directory, the control plane's up/down pair so the rollback rules have a subject, and the D1
# worker's tree populated so its exclusion is exercised rather than assumed.
new_repo() {
	local sb="${BASE}/$1"
	mkdir -p "${sb}/scripts" "${sb}/modules/eventing/migrations/postgres" \
		"${sb}/cloud/control-plane/migrations" "${sb}/commercial/license-worker/migrations" || return 1
	cp "${GATE}" "${sb}/scripts/check-migrations.sh" || return 1
	printf 'ALTER TABLE t ADD COLUMN c TEXT;\n' > "${sb}/modules/eventing/migrations/postgres/0001_seed.sql"
	printf 'CREATE TABLE tenants (id TEXT PRIMARY KEY);\n' > "${sb}/cloud/control-plane/migrations/001_base.up.sql"
	printf 'DROP TABLE tenants;\n' > "${sb}/cloud/control-plane/migrations/001_base.down.sql"
	printf 'ALTER TABLE d ADD COLUMN e TEXT;\n' > "${sb}/commercial/license-worker/migrations/0001_seed.sql"
	git -C "${sb}" init -q -b main || return 1
	git -C "${sb}" -c user.email=t@t -c user.name=t add -A || return 1
	git -C "${sb}" -c user.email=t@t -c user.name=t commit -qm fixture || return 1
	printf '%s' "${sb}"
}

# expect drives one case. The substring matters as much as the status: a red for the wrong reason is
# not the control firing, and this whole file exists because a green meant "I did not look".
expect() {
	local what="$1" sb="$2" want_rc="$3" want_txt="$4" out rc
	cases=$((cases + 1))
	out="$(cd "${sb}" && bash scripts/check-migrations.sh 2>&1)"; rc=$?
	if [ "${rc}" -ne "${want_rc}" ]; then
		printf '✗ %s\n    expected exit %s, got %s\n' "${what}" "${want_rc}" "${rc}"
		printf '%s\n' "${out}" | sed 's/^/      /' | head -4
		fails=$((fails + 1)); return
	fi
	case "${out}" in
		*"${want_txt}"*) printf '✓ %s\n' "${what}" ;;
		*)
			printf '✗ %s\n    exit %s was right, the verdict never said: %s\n' "${what}" "${rc}" "${want_txt}"
			printf '%s\n' "${out}" | sed 's/^/      /' | head -4
			fails=$((fails + 1)) ;;
	esac
}

# --- CASE 0. CALIBRATION. Without a green baseline every red below could be red for free.
sb="$(new_repo clean)" || exit 2
expect "CASE 0: a clean declared tree is GREEN" "${sb}" 0 "expand-contract online-safe"

# --- CASES 1-2. THE COUNTERFACTUAL PAIR ON DIGIT WIDTH. Same statement, same directory shape, only
# the prefix width differs. The four-digit case was always caught; the three-digit one was the
# defect, and a battery that tested only one of them would have passed throughout.
sb="$(new_repo destructive_three)" || exit 2
printf 'ALTER TABLE tenants DROP COLUMN name;\n' > "${sb}/cloud/control-plane/migrations/002_drop.up.sql"
git -C "${sb}" -c user.email=t@t -c user.name=t add -A >/dev/null
expect "CASE 1: a destructive THREE-digit expand is caught (the original defect)" \
	"${sb}" 1 "002_drop.up.sql"

sb="$(new_repo destructive_four)" || exit 2
printf 'ALTER TABLE t DROP COLUMN c;\n' > "${sb}/modules/eventing/migrations/postgres/0002_drop.sql"
git -C "${sb}" -c user.email=t@t -c user.name=t add -A >/dev/null
expect "CASE 2: a destructive FOUR-digit expand is caught (control positive)" \
	"${sb}" 1 "0002_drop.sql"

# --- CASE 3. The D1 worker's tree is SQLite and has its own gate. A destructive statement there must
# not turn this one red — an exclusion that fires only when nothing is inside it proves nothing.
sb="$(new_repo excluded_d1)" || exit 2
printf 'ALTER TABLE d DROP COLUMN e;\n' > "${sb}/commercial/license-worker/migrations/0002_drop.sql"
git -C "${sb}" -c user.email=t@t -c user.name=t add -A >/dev/null
expect "CASE 3: the D1 tree is excluded, and a destructive statement there stays green" \
	"${sb}" 0 "expand-contract online-safe"

# --- CASE 4. testdata is fixtures, not schema.
sb="$(new_repo testdata_excluded)" || exit 2
mkdir -p "${sb}/modules/x/testdata/migrations"
printf 'ALTER TABLE t DROP COLUMN c;\n' > "${sb}/modules/x/testdata/migrations/0001_drop.sql"
git -C "${sb}" -c user.email=t@t -c user.name=t add -A >/dev/null
expect "CASE 4: a migration-shaped fixture under testdata is not schema" "${sb}" 0 "online-safe"

# --- CASE 5. NON-NEGOTIABLE: zero units is never green. This is the shape of BOTH historical
# defects — a subject that shrank to nothing while the verdict stayed confident.
sb="$(new_repo no_migrations)" || exit 2
git -C "${sb}" rm -q -r --cached modules cloud commercial >/dev/null
find "${sb}" -name '*.sql' -delete
expect "CASE 5: an empty enumeration is UNVERIFIED (exit 2), never a clean green" "${sb}" 2 "UNVERIFIED"

# --- CASE 6. THE INDEX CENSUS CONTROL. A tracked migration the pattern stops matching must turn the
# gate red rather than quietly shrink the graded set. Reached by numbering with TWO digits, which the
# `[0-9]{3,}` pattern does not match while `git ls-files` still lists it.
sb="$(new_repo census_control)" || exit 2
printf 'ALTER TABLE t ADD COLUMN z TEXT;\n' > "${sb}/modules/eventing/migrations/postgres/02_short.sql"
git -C "${sb}" -c user.email=t@t -c user.name=t add -A >/dev/null
expect "CASE 6: a tracked migration the pattern misses is a refusal, not a smaller set" \
	"${sb}" 2 "02_short.sql"

# --- CASE 7. THE REGRESSION THE INDEX CENSUS COULD HAVE CAUSED, and the reason the enumeration reads
# --others. An author's brand-new migration, written and not yet `git add`-ed, must still be graded:
# "green about a file I never opened" is this gate's founding defect, and reading only the index
# would have moved it one step earlier instead of closing it.
sb="$(new_repo untracked_new)" || exit 2
printf 'ALTER TABLE t DROP COLUMN c;\n' > "${sb}/modules/eventing/migrations/postgres/0002_brand_new.sql"
expect "CASE 7: a never-added migration is STILL graded" "${sb}" 1 "0002_brand_new.sql"

# --- CASE 8. And its counterweight: an IGNORED tree is somebody else's checkout. This is the shared
# hub clone exactly — `.claude/worktrees/**` is gitignored, and grading it made the gate red for
# sixteen directories that are not the project.
sb="$(new_repo ignored_scratch)" || exit 2
printf '.scratch/\n' > "${sb}/.gitignore"
mkdir -p "${sb}/.scratch/modules/eventing/migrations/postgres"
printf 'ALTER TABLE t DROP COLUMN c;\n' > "${sb}/.scratch/modules/eventing/migrations/postgres/0002_theirs.sql"
git -C "${sb}" -c user.email=t@t -c user.name=t add .gitignore >/dev/null
expect "CASE 8: a gitignored scratch tree is not enumerated at all" "${sb}" 0 "online-safe"

# --- CASE 9. A file that cannot be read is UNKNOWN, not clean. Guarded: as root, chmod 000 does not
# deny reads, so the case would pass for the wrong reason and is skipped with its reason said out
# loud rather than counted as a pass.
sb="$(new_repo unreadable)" || exit 2
chmod 000 "${sb}/modules/eventing/migrations/postgres/0001_seed.sql"
if [ -r "${sb}/modules/eventing/migrations/postgres/0001_seed.sql" ]; then
	printf '· CASE 9 SKIPPED: this user can read a 000 file (root?), so unreadability cannot be staged\n'
else
	expect "CASE 9: a migration that cannot be read is UNVERIFIED" "${sb}" 2 "UNVERIFIED"
fi
chmod 644 "${sb}/modules/eventing/migrations/postgres/0001_seed.sql"

# --- CASE 10. No repository at all: the census cannot be taken, and "no migrations found" would be
# the confident wrong answer.
sb="$(new_repo not_a_repo)" || exit 2
rm -rf "${sb}/.git"
expect "CASE 10: a tree that is not a repository is UNVERIFIED" "${sb}" 2 "UNVERIFIED"

echo
if [ "${cases}" -eq 0 ]; then
	echo "test-migrations-coverage: NO CASES RAN — that is not a pass." >&2
	exit 2
fi
if [ "${fails}" -ne 0 ]; then
	echo "✗ migration coverage battery FAILED (${fails} of ${cases} case(s))"
	exit 1
fi
echo "✓ migration coverage battery: ${cases} case(s), all behaved"
