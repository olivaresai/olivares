#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-npm-vuln-gate.sh — the battery for scripts/npm-vuln-gate.sh.
#
# WHY IT DOES NOT CALL THE REGISTRY. A gate battery that needed the network would be a battery
# that reports "could not look" on a bad day and gets read as flaky, and a flaky battery is one
# nobody trusts enough to block on. Every row here drives the gate with an INJECTED audit
# binary emitting a fixture, so each row asserts one decision of the gate and nothing else.
#
# WHAT EACH ROW IS FOR. The gate has exactly three answers and the rows are grouped by them:
# refusals (exit 2 — "the gate certifies nothing"), blocks (exit 1 — it looked and found), and
# clean (exit 0). The refusal rows are the load-bearing ones: they are the difference between
# this gate and the failure mode the Go gate shipped with for months, where a tool that could
# not run produced a green.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="${ROOT}/scripts/npm-vuln-gate.sh"
[ -x "${GATE}" ] || [ -f "${GATE}" ] || { echo "test-npm-vuln-gate: ${GATE} not found" >&2; exit 2; }

pass=0; fail=0
row() { # row <name> <want-exit> <must-contain> -- <cmd...>
	local name="$1"
	local want="$2"
	local needle="$3"
	shift 4
	local out rc
	out="$("$@" 2>&1)"; rc=$?
	if [ "${rc}" -ne "${want}" ]; then
		printf '  FAIL  %-58s exit=%s want=%s\n' "${name}" "${rc}" "${want}"
		printf '%s\n' "${out}" | sed 's/^/          /' | head -6
		fail=$((fail + 1)); return
	fi
	if [ -n "${needle}" ] && ! printf '%s' "${out}" | grep -qF "${needle}"; then
		printf '  FAIL  %-58s exit ok but never said %q\n' "${name}" "${needle}"
		printf '%s\n' "${out}" | sed 's/^/          /' | head -6
		fail=$((fail + 1)); return
	fi
	printf '  ok    %-58s %s\n' "${name}" "${needle:-exit ${want}}"
	pass=$((pass + 1))
}

# A throwaway repository per scenario. `git init` in a temp dir, NEVER `git -C` against a path
# inside this checkout: GIT_DIR is exported to hooks from a linked worktree and OUTRANKS -C,
# which is how a self-test in this repo once wrote to the live repository (see lint:git-env).
# Sourcing the shared sanitiser keeps that impossible here too.
# shellcheck source=lib/git-env.sh
[ -f "${ROOT}/scripts/lib/git-env.sh" ] && . "${ROOT}/scripts/lib/git-env.sh"

# The scratch directory must have a REAL execute bit, because every scenario below injects a
# fake audit binary and RUNS it. A bare `mktemp -d` honours TMPDIR and falls back to /tmp, and
# /tmp is mounted `noexec` in this project's container — measured 2026-08-07 by reproducing it:
# with TMPDIR=/tmp this battery reports 2 passed, 9 FAILED, every one of them
# `timeout: failed to run command '…/bin/thresh': Permission denied`.
#
# THAT IS NOT A COSMETIC FLAKE. This battery runs in the FAST lane of every push in every lane,
# so a lane whose TMPDIR is not exec-capable gets its push refused by the VULNERABILITY gate,
# with a message that reads like a finding. It was measured exactly that way: a push from the
# hub failed here while the same task run by hand, with an exec-capable TMPDIR exported, passed
# 11/11 seconds later.
#
# lib/exec-workdir.sh picks a directory and PROVES it can run a file before handing it back.
# This is its third consumer, after test-check-secrets.sh and test-check-hooks-path.sh — one
# hard-won environment fact, one file.
# shellcheck source=lib/exec-workdir.sh
. "${ROOT}/scripts/lib/exec-workdir.sh" || {
	echo "test-npm-vuln-gate: cannot source lib/exec-workdir.sh — could not run. NOT a pass." >&2
	exit 2
}
WORK="$(olivares_pick_exec_workdir npm-vuln-gate-tests)" || {
	echo "test-npm-vuln-gate: no scratch directory has a REAL execute bit" >&2
	echo "test-npm-vuln-gate: (tried RUNNER_TEMP, TMPDIR, /tmp, HOME, /workspace/.olivares-tmptest)." >&2
	echo "test-npm-vuln-gate: could not run the battery. This is NOT a pass." >&2
	exit 2
}
trap 'rm -rf "${WORK}"' EXIT

# fixture <name> <json>  ->  prints the path of a fake audit binary emitting that json
fixture() {
	local name="$1"
	local json="$2"
	local bin
	mkdir -p "${WORK}/bin"
	bin="${WORK}/bin/${name}"
	cat >"${bin}" <<EOF
#!/usr/bin/env bash
cat <<'JSON'
${json}
JSON
exit 1
EOF
	chmod +x "${bin}"
	printf '%s' "${bin}"
}

# repo <name> <lockfile> -> prints the path of a throwaway repo carrying that lockfile
# NEVER let this return empty. An empty path makes `cd ""` a no-op, so the throwaway-repo
# commands would run against THIS checkout instead — the exact hazard lint:git-env exists for,
# and it bit while writing this file: a `local` with a self-reference failed under `set -u`,
# the path came back empty, and `git ls-files` listed the live repository.
repo() {
	local name="$1"
	local lock="$2"
	local dir="${WORK}/repo-${name}"
	[ -n "${WORK}" ] && [ -n "${name}" ] || { echo "repo: refusing to build a fixture with an empty path" >&2; exit 2; }
	mkdir -p "${dir}/scripts/lib"
	cp "${GATE}" "${dir}/scripts/npm-vuln-gate.sh"
	# The sanitiser travels WITH the gate. It sources lib/git-env.sh fail-closed — a missing
	# sanitiser is "I could not isolate", never "isolation was not needed" — so a fixture without
	# it makes every row exit 2 for a reason that has nothing to do with the row. Measured while
	# writing this: adding the source to the gate turned 11/11 into 0/11 until the fixture carried
	# it too, which is the fail-closed behaviour working, seen from the other side.
	cp "${ROOT}/scripts/lib/git-env.sh" "${dir}/scripts/lib/git-env.sh"
	: >"${dir}/${lock}"
	( cd "${dir}" && git init -q . && git add -A && \
	  git -c user.email=t@t -c user.name=t commit -qm f ) >/dev/null 2>&1
	printf '%s' "${dir}"
}

# La razon que el gate exige desde el 2026-09-03 (>= 20 caracteres). Va en UNA variable: si el
# minimo cambia, cambia aqui y no en seis printf que se olvidarian de uno.
RAZON='    reason: fixture entry, justified at length to clear the twenty-character floor\n'

HIGH='{"advisories":{"1":{"severity":"high","references":"https://github.com/advisories/GHSA-aaaa-bbbb-cccc"}}}'
MOD='{"advisories":{"1":{"severity":"moderate","references":"https://github.com/advisories/GHSA-dddd-eeee-ffff"}}}'
CLEAN='{"advisories":{},"metadata":{"vulnerabilities":{"total":0}}}'
NPMSHAPE='{"vulnerabilities":{"p":{"severity":"high","via":[{"url":"https://github.com/advisories/GHSA-9999-8888-7777","severity":"high"}]}},"metadata":{}}'
NOCONTAINER='{"hello":"world"}'

echo "test-npm-vuln-gate: the three answers"

# ---- REFUSALS (exit 2): the gate could not look, and says so ------------------------------
R="$(repo tool "pnpm-lock.yaml")"
row "an audit binary that cannot run REFUSES (never a green)" 2 "certifies nothing" -- \
	env NPM_VULN_PNPM=/bin/false bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo parse "pnpm-lock.yaml")"; B="$(fixture notjson 'this is not json')"
row "a stream that does not parse REFUSES" 2 "does not parse" -- \
	env NPM_VULN_PNPM="${B}" bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo container "pnpm-lock.yaml")"; B="$(fixture nocontainer "${NOCONTAINER}")"
row "valid JSON with NO advisory container REFUSES" 2 "did not run" -- \
	env NPM_VULN_PNPM="${B}" bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo empty "README.md")"
row "a repository with no tracked lockfile REFUSES" 2 "refusing to grade nothing" -- \
	env NPM_VULN_PNPM=/bin/true bash "${R}/scripts/npm-vuln-gate.sh"

# Empty stdout is the GitHub-hosted docs-site shape (run 33866495506). Bounded retry,
# then still refuse — never a silent green. SLEEP=0 so the battery stays hermetic and fast.
R="$(repo emptyout "package-lock.json")"
row "empty audit stdout still REFUSES after bounded retries" 2 "after 2 attempt(s)" -- \
	env NPM_VULN_NPM=/bin/true NPM_VULN_AUDIT_ATTEMPTS=2 NPM_VULN_AUDIT_RETRY_SLEEP_S=0 \
	bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo retryok "package-lock.json")"
mkdir -p "${WORK}/bin"
RETRY_BIN="${WORK}/bin/retry-then-json"
RETRY_STATE="${WORK}/retry-then-json.state"
rm -f "${RETRY_STATE}"
cat >"${RETRY_BIN}" <<EOF
#!/usr/bin/env bash
n=0
[ -f "${RETRY_STATE}" ] && n=\$(cat "${RETRY_STATE}")
n=\$((n + 1))
echo "\$n" >"${RETRY_STATE}"
if [ "\$n" -lt 2 ]; then
	exit 1
fi
cat <<'JSON'
{"advisories":{},"metadata":{"vulnerabilities":{"total":0}}}
JSON
exit 0
EOF
chmod +x "${RETRY_BIN}"
row "empty then JSON on retry is graded, not refused" 0 "census across" -- \
	env NPM_VULN_NPM="${RETRY_BIN}" NPM_VULN_AUDIT_ATTEMPTS=3 NPM_VULN_AUDIT_RETRY_SLEEP_S=0 \
	NPM_VULN_ALLOW=/nonexistent bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo badsleep "package-lock.json")"
row "a non-numeric retry sleep REFUSES (named, not sleep's rc)" 2 "RETRY_SLEEP_S is not a non-negative integer" -- \
	env NPM_VULN_NPM=/bin/true NPM_VULN_AUDIT_RETRY_SLEEP_S=nope \
	bash "${R}/scripts/npm-vuln-gate.sh"

# ---- BLOCKS (exit 1): it looked, and found ------------------------------------------------
R="$(repo high "pnpm-lock.yaml")"; B="$(fixture high "${HIGH}")"
row "a HIGH advisory that is not allowlisted BLOCKS" 1 "BLOCKING" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW=/nonexistent bash "${R}/scripts/npm-vuln-gate.sh"

printf 'allow:\n  - id: GHSA-aaaa-bbbb-cccc\n%b    expires: "2020-01-01"\n' \
	"${RAZON}" >"${WORK}/expired.yaml"
row "an EXPIRED allowlist entry BLOCKS (no silent forever-exception)" 1 "EXPIRED" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW="${WORK}/expired.yaml" bash "${R}/scripts/npm-vuln-gate.sh"

# ⛔ UNA EXCEPCION SIN RAZON NO EXIME. La razon era decorativa hasta el 2026-09-03: el lector
#    parseaba `id:` y `expires:` y nada mas, asi que la cabecera del fichero pedia un porque que
#    NADIE hacia cumplir. Estos dos casos son la guarda, y el segundo existe porque «tiene el
#    campo» no es «tiene una razon»: una de tres letras pasaria una comprobacion de presencia.
printf 'allow:\n  - id: GHSA-aaaa-bbbb-cccc\n    expires: "2099-01-01"\n' >"${WORK}/norazon.yaml"
row "an allowlist entry with NO reason does not exempt" 1 "carries no real reason" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW="${WORK}/norazon.yaml" bash "${R}/scripts/npm-vuln-gate.sh"

printf 'allow:\n  - id: GHSA-aaaa-bbbb-cccc\n    reason: porque si\n    expires: "2099-01-01"\n' >"${WORK}/razoncorta.yaml"
row "a reason too SHORT does not exempt either (presence is not justification)" 1 "the minimum is 20" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW="${WORK}/razoncorta.yaml" bash "${R}/scripts/npm-vuln-gate.sh"

# ⛔ Y EL ESCALAR PLEGADO SE LEE. El fichero real usa `reason: >-` con el texto en las lineas
#    SIGUIENTES, asi que un lector que solo mire la linea de la clave veria una razon VACIA y
#    bloquearia las veinte entradas de golpe. Direccion de no disparo: esta razon es valida.
printf 'allow:\n  - id: GHSA-aaaa-bbbb-cccc\n    reason: >-\n      folded scalar whose text lives on the following lines, as the real file writes it\n    expires: "2099-01-01"\n' >"${WORK}/plegada.yaml"
row "a FOLDED reason is read from its continuation lines, not called empty" 0 "temporarily accepted" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW="${WORK}/plegada.yaml" bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo npmshape "package-lock.json")"; B2="$(fixture npmshape "${NPMSHAPE}")"
row "the npm 7+ shape is READ too (not silently clean)" 1 "GHSA-9999-8888-7777" -- \
	env NPM_VULN_NPM="${B2}" NPM_VULN_ALLOW=/nonexistent bash "${R}/scripts/npm-vuln-gate.sh"

# ---- CLEAN (exit 0) -----------------------------------------------------------------------
printf 'allow:\n  - id: GHSA-aaaa-bbbb-cccc\n%b    expires: "2099-01-01"\n' \
	"${RAZON}" >"${WORK}/live.yaml"
R="$(repo highok "pnpm-lock.yaml")"; B="$(fixture high2 "${HIGH}")"
row "the same advisory, allowlisted and unexpired, passes" 0 "temporarily accepted" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW="${WORK}/live.yaml" bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo mod "pnpm-lock.yaml")"; B="$(fixture mod "${MOD}")"
row "a MODERATE advisory is reported, not blocked" 0 "moderate=1" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW=/nonexistent bash "${R}/scripts/npm-vuln-gate.sh"

R="$(repo clean "pnpm-lock.yaml")"; B="$(fixture clean "${CLEAN}")"
row "a genuinely clean workspace passes AND prints its census" 0 "census across" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW=/nonexistent bash "${R}/scripts/npm-vuln-gate.sh"

# ---- THE MUTATION THAT MATTERS -----------------------------------------------------------
# Lowering the blocking set must NOT be able to hide a high advisory silently: the gate prints
# the severities it is blocking on, so a narrowed threshold is visible in its own output.
R="$(repo thresh "pnpm-lock.yaml")"; B="$(fixture thresh "${HIGH}")"
row "a narrowed threshold still DECLARES itself in the output" 0 "blocking severities: critical" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW=/nonexistent NPM_VULN_BLOCK=critical \
	bash "${R}/scripts/npm-vuln-gate.sh"

# ⛔ AUSENTE DEL CENSO ⇒ SE BORRA, CADUCADA O NO. La nota exigia AMBAS cosas hasta el
#    2026-09-03, asi que una entrada CURADA se callaba hasta su fecha y en la practica para
#    siempre: nadie revisa una linea que no se queja, y la allowlist se llena de excepciones para
#    problemas que ya no existen. El workspace de este caso esta LIMPIO y la entrada NO ha
#    caducado: antes no decia nada, ahora manda borrarla.
R="$(repo huerfana "pnpm-lock.yaml")"; B="$(fixture huerfana "${CLEAN}")"
row "an allowlist entry CURED but not yet expired is called out for removal" 0 "no longer present" -- \
	env NPM_VULN_PNPM="${B}" NPM_VULN_ALLOW="${WORK}/live.yaml" bash "${R}/scripts/npm-vuln-gate.sh"

# ---- MUTANTES DE LAS DOS CURAS ------------------------------------------------------------
# Un caso verde no prueba que su guarda sirva: se rompe la guarda y se comprueba que el caso
# MUERE. Y si el mutante no llega a aplicarse —porque el ancla cambio—, eso se dice a gritos,
# porque un mutante sin aplicar se lee igual que un mutante que sobrevive.
mutante() { # mutante <rotulo> <sed> <allowlist> <fixture-json> <aguja-que-debe-DESAPARECER>
	local rotulo="$1" expr="$2" allow="$3" datos="$4" aguja="$5"
	local M="${WORK}/mut-gate.sh" clave
	clave="$(printf '%s' "${rotulo}" | tr -cd 'a-z')"
	sed "${expr}" "${GATE}" >"${M}"
	if cmp -s "${GATE}" "${M}"; then
		echo "FAIL  ${rotulo}: el mutante NO se aplico (el ancla cambio) — sin aplicar no prueba nada"
		fail=$((fail + 1)); return
	fi
	local RM BM salida
	RM="$(repo "mut${clave}" "pnpm-lock.yaml")"
	cp "${M}" "${RM}/scripts/npm-vuln-gate.sh"
	BM="$(fixture "mut${clave}" "${datos}")"
	salida="$(env NPM_VULN_PNPM="${BM}" NPM_VULN_ALLOW="${allow}" bash "${RM}/scripts/npm-vuln-gate.sh" 2>&1)"
	# ⛔ Se comprueba la AGUJA, no el codigo de salida: la segunda cura no cambia el rc —solo el
	#    mensaje—, asi que un arnes que compare rc la daria por probada sin haberla tocado.
	# ⛔ SIN TUBERIA: `printf … | grep -qF` bajo `pipefail` devuelve 141 CUANDO ACIERTA —grep
	#    cierra la tuberia al primer hallazgo y el productor muere de SIGPIPE—, asi que el
	#    fallo cae en el camino del EXITO. `salida` ya es una variable: `case` compara la
	#    subcadena sin lanzar un solo proceso. Lo caza `lint:sigpipe-booleans`, que mato este
	#    push en la pata 166 con «scripts/test-npm-vuln-gate.sh: 1 -> 2».
	case "${salida}" in
	*"${aguja}"*)
		echo "FAIL  ${rotulo}: el mutante SOBREVIVE (la aguja «${aguja}» sigue saliendo)"
		fail=$((fail + 1))
		;;
	*)
		echo "ok    mutante muerto — ${rotulo}"
		pass=$((pass + 1))
		;;
	esac
}

# Sin el umbral, ninguna entrada se marca y `norazon.yaml` vuelve a eximir en silencio.
mutante "sin el umbral de razon" 's/< MIN_REASON/< 0/' \
	"${WORK}/norazon.yaml" "${HIGH}" "carries no real reason"

# Con la condicion vieja, la entrada curada y NO caducada se vuelve a callar.
mutante "la nota exigiendo tambien caducidad" 's/^    if ghsa not in seen:$/    if ghsa not in seen and exp < today:/' \
	"${WORK}/live.yaml" "${CLEAN}" "no longer present"

echo
echo "test-npm-vuln-gate: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
