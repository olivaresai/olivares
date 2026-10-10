#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-push-preflight.sh — un push a esta caja cuesta HORAS; esto cuesta un segundo.
#
# ⛔ POR QUÉ EXISTE, medido el 2026-08-26. Un push murió a **1h48** y el hook nombró la causa:
# «UN GATE MODIFICÓ EL ÁRBOL DE TRABAJO mientras lo comprobaba» — 69 borrados y 69 sin trackear
# bajo `core/internal/webui/dist/`, el bundle de consola reconstruido con hashes nuevos. El hook
# tiene razón en rechazarlo: en un árbol compartido eso lo recoge el `git add` del siguiente
# carril. El problema es CUÁNDO se entera: el veredicto llega cuando ya has pagado el gate entero.
#
# Y no se va solo. Censadas las 196 copias de trabajo de la caja ese día, el residuo seguía en su
# worktree HORAS después del fallo, así que un re-push desde ahí habría vuelto a morir igual.
#
# ⇒ La comprobación es trivial y el ahorro no: se mira el árbol ANTES de arrancar, se nombra el
#   desglose y, si el residuo es del bundle, se da el comando exacto que lo retira.
#
# Códigos: 0 limpio · 1 sucio (no arranques) · 2 no he podido mirar (que NO es «limpio»).

set -u

# ⛔ AISLAMIENTO DE ENTORNO GIT, y me lo exigió `lint:git-env` con razón: este guion empareja
# `mktemp -d` con git (el self-test fabrica repos desechables y los maneja con `git -C`), y
# **`GIT_DIR` GANA A `-C`**. git exporta `GIT_DIR` a sus hooks desde un worktree ENLAZADO —que es
# donde corre todo carril paralelo de este repositorio—, así que un self-test lanzado desde el
# hook `pre-push` operaría sobre el repositorio REAL que se está empujando en vez de sobre su
# señuelo: le commitea fixtures, le mueve la rama y le reescribe la identidad.
#
# Y hay una segunda razón, propia de este guion: su trabajo es SONDEAR el árbol de otro. Con un
# `GIT_DIR` heredado, `git -C "$wt" status` no miraría `$wt` sino lo que dijera el entorno, así
# que el veredicto «limpio»/«sucio» sería sobre un árbol que no es el que te preguntan.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

known_fixture_tmp_name() {
	local name="$1"
	[[ "$name" =~ ^tmp\.[[:alnum:]]{10}$ ]] ||
		[[ "$name" =~ ^engine-citations-[[:alnum:]]{6}$ ]] ||
		[[ "$name" =~ ^d1-migrations\.[[:alnum:]]{6}$ ]] ||
		[[ "$name" =~ ^program-anchors\.[[:alnum:]]{6}$ ]] ||
		[[ "$name" =~ ^unreachable-build-guards\.[[:alnum:]]{6}$ ]] ||
		[[ "$name" =~ ^olivares-fixture-run\.[[:alnum:]]{6}$ ]] ||
		[[ "$name" =~ ^with-clean-tmp-test\.[[:alnum:]]{6}$ ]] ||
		[[ "$name" =~ ^push-preflight\.[[:alnum:]]{6}$ ]]
}

cleanup_stale_tmp() { # poda sólo raíces conocidas, viejas, propias y sin referencia de proceso
	local root age_minutes uid now candidate meta owner mtime removed=0 live_count=0 errors=0
	local proc_links=0 tool name
	local ref restore_nullglob=0
	local -a candidates=()
	local -A live_roots=()
	root="${OLIVARES_PREFLIGHT_TMP_ROOT:-/tmp}"
	age_minutes="${OLIVARES_PREFLIGHT_TMP_MAX_AGE_MINUTES:-180}"
	case "$age_minutes" in ''|*[!0-9]*)
		echo "check-push-preflight: COULD NOT CHECK: invalid temporary-directory age ('$age_minutes')" >&2
		return 2 ;;
	esac
	[ "${#age_minutes}" -le 7 ] || {
		echo "check-push-preflight: COULD NOT CHECK: temporary-directory age is out of range" >&2
		return 2
	}
	age_minutes=$((10#$age_minutes))
	[ "$age_minutes" -gt 0 ] || {
		echo "check-push-preflight: COULD NOT CHECK: temporary-directory age must be greater than zero" >&2
		return 2
	}
	[ -d "$root" ] || {
		echo "check-push-preflight: COULD NOT CHECK: temporary root is missing ('$root')" >&2
		return 2
	}
	root="$(cd -- "$root" 2>/dev/null && pwd -P)" || return 2
	case "$root" in /|'')
		echo "check-push-preflight: COULD NOT CHECK: refusing a broadly scoped temporary root ('$root')" >&2
		return 2 ;;
	esac
	uid="$(id -u)" || return 2
	now="$(date +%s)" || return 2
	for tool in find grep readlink rm stat; do
		command -v "$tool" >/dev/null 2>&1 || {
			echo "check-push-preflight: COULD NOT CHECK: '$tool' is required for safe cleanup" >&2
			return 2
		}
	done

	# Sólo nombres emitidos por nuestras baterías. `tmp.*` cubre los mktemp históricos que
	# explican la mayor parte del residuo medido; directorios jóvenes nunca entran en la lista.
	shopt -q nullglob || { restore_nullglob=1; shopt -s nullglob; }
	for candidate in \
		"$root"/tmp.* \
		"$root"/engine-citations-* \
		"$root"/d1-migrations.* \
		"$root"/program-anchors.* \
		"$root"/unreachable-build-guards.* \
		"$root"/olivares-fixture-run.* \
		"$root"/with-clean-tmp-test.* \
		"$root"/push-preflight.*; do
		[ -d "$candidate" ] && [ ! -L "$candidate" ] || continue
		name="${candidate##*/}"
		known_fixture_tmp_name "$name" || continue
		meta="$(stat -c '%u %Y' -- "$candidate" 2>/dev/null)" || continue
		owner="${meta%% *}"; mtime="${meta#* }"
		[ "$owner" = "$uid" ] || continue
		[ $((now - mtime)) -gt $((age_minutes * 60)) ] || continue
		candidates+=("$candidate")
	done
	if [ "${#candidates[@]}" -eq 0 ]; then
		[ "$restore_nullglob" -eq 0 ] || shopt -u nullglob
		echo "check-push-preflight: old temporary directories: 0 candidate roots."
		return 0
	fi

	readlink "/proc/$$/cwd" >/dev/null 2>&1 || {
		[ "$restore_nullglob" -eq 0 ] || shopt -u nullglob
		echo "check-push-preflight: COULD NOT CHECK: /proc cannot be used to check live processes" >&2
		return 2
	}

	# Una instantánea única evita el coste candidatos×procesos. No imprime argv ni entorno: sólo
	# reduce cualquier ruta bajo la raíz a su hijo de primer nivel y marca ese hijo como vivo.
	record_live_tmp_path() {
		local value="$1" relative first
		value="${value% (deleted)}"
		case "$value" in
		"$root"/*)
			relative="${value#"$root"/}"
			first="${relative%%/*}"
			[ -n "$first" ] && live_roots["$root/$first"]=1
			;;
		esac
	}
	# Un único `find` obtiene cwd, root y descriptores: lanzar un `readlink` por descriptor hacía
	# que la propia prevención costara ~15 s en esta caja cargada.
	while IFS= read -r ref; do
		if [ -n "$ref" ]; then
			proc_links=$((proc_links + 1))
			record_live_tmp_path "$ref"
		fi
	done < <(find /proc/[0-9]*/cwd /proc/[0-9]*/root /proc/[0-9]*/fd \
		-maxdepth 1 -type l -printf '%l\n' 2>/dev/null)
	[ "$proc_links" -gt 0 ] || {
		unset -f record_live_tmp_path
		[ "$restore_nullglob" -eq 0 ] || shopt -u nullglob
		echo "check-push-preflight: COULD NOT CHECK: /proc snapshot was empty" >&2
		return 2
	}
	# argv y entorno pueden ser grandes y sensibles. `grep -F` los lee en C, no los imprime:
	# devuelve únicamente el nombre aleatorio candidato que ya conocemos.
	while IFS= read -r -d '' ref; do
		[ -n "$ref" ] && live_roots["$ref"]=1
	done < <(grep -a -z -h -o -F -f <(printf '%s\n' "${candidates[@]}") \
		/proc/[0-9]*/environ /proc/[0-9]*/cmdline 2>/dev/null)
	unset -f record_live_tmp_path

	for candidate in "${candidates[@]}"; do
		if [ "${live_roots[$candidate]:-}" = 1 ]; then
			live_count=$((live_count + 1))
			continue
		fi
		# Revalida tipo, dueño y edad justo antes de borrar; jamás sigue un enlace simbólico.
		[ -d "$candidate" ] && [ ! -L "$candidate" ] || continue
		meta="$(stat -c '%u %Y' -- "$candidate" 2>/dev/null)" || { errors=$((errors + 1)); continue; }
		owner="${meta%% *}"; mtime="${meta#* }"
		[ "$owner" = "$uid" ] || continue
		[ $((now - mtime)) -gt $((age_minutes * 60)) ] || continue
		name="${candidate##*/}"
		known_fixture_tmp_name "$name" || { errors=$((errors + 1)); continue; }
		if rm -rf -- "$candidate"; then removed=$((removed + 1)); else errors=$((errors + 1)); fi
	done
	[ "$restore_nullglob" -eq 0 ] || shopt -u nullglob
	echo "check-push-preflight: old temporary directories: $removed root(s) removed; $live_count live root(s) preserved."
	[ "$errors" -eq 0 ] || {
		echo "check-push-preflight: COULD NOT CLEAN $errors temporary root(s)" >&2
		return 2
	}
	return 0
}

tmpdir_ejecuta() { # 0 si TMPDIR puede EJECUTAR un binario; 1 si no, con el remedio
	# ⛔ La segunda forma de morir con el gate casi pagado, y la que más veces mordió: `go test`
	#    compila su binario en TMPDIR y luego lo EJECUTA. En estos contenedores /tmp está montado
	#    `noexec`, así que sin TMPDIR fijado el gate llega hasta `lint:cli-registries` —ya con
	#    ~40 min gastados— y muere con `fork/exec .../x.test: permission denied`. Medido tres
	#    veces el 2026-08-26 (37, 39 y 40 min). El repo ya usa la convención por tarea
	#    (Taskfile: TMPDIR="{{.ROOT_DIR}}/.export-tmp"); esto la comprueba para el push entero.
	local d probe
	d="${TMPDIR:-/tmp}"
	probe="$d/.olv-preflight-probe.$$"
	printf '#!/bin/sh\nexit 0\n' > "$probe" 2>/dev/null || {
		echo "check-push-preflight: ⛔ cannot write to TMPDIR ('$d')."; return 1; }
	chmod +x "$probe" 2>/dev/null
	if "$probe" >/dev/null 2>&1; then
		rm -f "$probe"
		echo "check-push-preflight: TMPDIR ('$d') supports execution; go test can run its binaries."
		return 0
	fi
	rm -f "$probe"
	echo "check-push-preflight: ⛔ DO NOT START: TMPDIR ('$d') cannot execute binaries."
	echo "check-push-preflight:    go test compiles there, then executes its binaries. The check would fail at"
	echo "check-push-preflight:    lint:cli-registries after about 40 minutes of work. Start with:"
	echo "       T=/workspace/.olv-push-tmp-\$\$; mkdir -p \"\$T\""
	echo "       TMPDIR=\"\$T\" GOTMPDIR=\"\$T\" git push origin <sha>:refs/heads/<rama>"
	return 1
}

bundle_al_dia() { # <worktree> -> 0 al día | 1 obsoleto | 2 no puede mirar
	# Source pushes carry no generated output; CI owns the fresh build.
	local wt="$1" cmd rc
	# Inyectable para poder probar las DOS ramas sin un shim en PATH: en estos contenedores
	# TMPDIR está montado noexec, PATH se lo saltaría en silencio y el test mediría el `task` real.
	cmd="${OLIVARES_PREFLIGHT_BUNDLE_CMD:-task lint:web-bundle-freshness}"
	if [ "${OLIVARES_PREFLIGHT_BUNDLE_CMD:-}" = "" ] && ! command -v task >/dev/null 2>&1; then
		echo "check-push-preflight: COULD NOT CHECK: 'task' is not installed; the bundle was not checked." >&2
		return 2
	fi
	( cd "$wt" 2>/dev/null && eval "$cmd" ) >/dev/null 2>&1; rc=$?
	if [ "$rc" -eq 0 ]; then
		echo "check-push-preflight: source push OK — CI builds and verifies the console."
		return 0
	fi
    echo "check-push-preflight: generated console output check failed (rc=$rc)."
    echo "Run task lint:web-bundle-freshness for details; commit sources only."
	return 1
}

preflight() { # preflight <worktree> -> 0 limpio | 1 sucio o TMPDIR inservible | 2 no he podido mirar
	local wt="$1" estado sucio dist bundle_rc
	cleanup_stale_tmp || return $?
	[ -n "$wt" ] || { echo "check-push-preflight: COULD NOT CHECK: no worktree" >&2; return 2; }
	[ -d "$wt" ] || { echo "check-push-preflight: COULD NOT CHECK: '$wt' is not a directory" >&2; return 2; }
	# --no-optional-locks: sondear el árbol de otro carril NO debe plantarle un index.lock.
	estado=$(git --no-optional-locks -C "$wt" status --porcelain 2>/dev/null) || {
		echo "check-push-preflight: COULD NOT CHECK: '$wt' cannot be queried as a Git repository" >&2; return 2; }
	if ! git --no-optional-locks -C "$wt" rev-parse --git-dir >/dev/null 2>&1; then
		echo "check-push-preflight: COULD NOT CHECK: '$wt' is not a Git repository" >&2; return 2
	fi
	sucio=$(printf '%s' "$estado" | grep -c . || true)
	if [ "${sucio:-0}" -eq 0 ]; then
		echo "check-push-preflight: CLEAN tree — the check will not reject it for residue."
		tmpdir_ejecuta || return 1
		bundle_al_dia "$wt"
		bundle_rc=$?
		[ "$bundle_rc" -eq 0 ] || return "$bundle_rc"
		return 0
	fi
	echo "check-push-preflight: ⛔ DO NOT START: $sucio uncommitted entry/entries in '$wt'."
	echo "check-push-preflight:    The check would detect them at the end of the push. Breakdown:"
	printf '%s\n' "$estado" | awk '{print substr($0,1,2)}' | sort | uniq -c | sed 's/^/       /'
	dist=$(printf '%s\n' "$estado" | grep -c 'core/internal/webui/dist' || true)
	if [ "${dist:-0}" -gt 0 ]; then
		echo "check-push-preflight:    $dist belong to the console bundle (rebuilt by a check). Remove them with:"
		echo "       git -C '$wt' restore --source=HEAD --worktree -- core/internal/webui/dist"
		echo "       git -C '$wt' clean -fdq -- core/internal/webui/dist"
	fi
	return 1
}

selftest() {
	# Se llama a la FUNCIÓN, no al guion. Re-invocarse por "$0" es lo que convierte un TMPDIR
	# montado noexec en «todos los casos en rojo» — un gate ciego que afirma sobre el árbol sin
	# haberlo mirado. Aquí ese modo de fallo no existe porque no hay re-invocación.
	local base rc fails=0 out live_pid=""
	base=$(mktemp -d "${TMPDIR:-/tmp}/push-preflight.XXXXXX") || { echo "selftest: COULD NOT CHECK: mktemp"; return 2; }
	trap '[ -n "$live_pid" ] && kill "$live_pid" 2>/dev/null; rm -rf "$base"' RETURN
	mkdir -p "$base/preflight-tmp"
	OLIVARES_PREFLIGHT_TMP_ROOT="$base/preflight-tmp"

	mk() { # mk <nombre> -> repo con un commit
		local d="$base/$1"
		git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
		git -C "$d" config user.email "t@example.invalid"; git -C "$d" config user.name "t"
		git -C "$d" config commit.gpgsign false
		mkdir -p "$d/core/internal/webui/dist/assets"
		printf 'seed\n' > "$d/README.md"
		printf 'var a=1\n' > "$d/core/internal/webui/dist/assets/x-AAAA.js"
		git -C "$d" add -A >/dev/null 2>&1
		git -C "$d" commit -q -m seed --no-verify >/dev/null 2>&1
	}

	# El árbol y el TMPDIR son dos ejes: cada caso fija el suyo, o un entorno roto tiñe de rojo
	# comprobaciones que no van de eso. `$base` está bajo TMPDIR, así que si TMPDIR no ejecuta
	# tampoco lo hace `$base`: para los casos de ÁRBOL se usa un TMPDIR que sí ejecute.
	local tmpok
	tmpok="/workspace/.olv-selftest-tmp.$$"
	mkdir -p "$tmpok" 2>/dev/null
	printf '#!/bin/sh\nexit 0\n' > "$tmpok/.p" 2>/dev/null; chmod +x "$tmpok/.p" 2>/dev/null
	if ! "$tmpok/.p" >/dev/null 2>&1; then
		rm -rf "$tmpok"
		echo "selftest: COULD NOT CHECK: no TMPDIR supports execution; cannot isolate the two checks"
		return 2
	fi
	rm -f "$tmpok/.p"

	# CASO 1 — limpio es 0. Control positivo: sin él, un guion que siempre diga «sucio» pasaría.
	mk limpio || { echo "selftest: COULD NOT CHECK: cannot create the repository"; return 2; }
	out=$(TMPDIR="$tmpok" OLIVARES_PREFLIGHT_BUNDLE_CMD=true preflight "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 1 (clean) expected 0, got $rc: $out"; fails=$((fails+1)); }

	# CASO 2 — sucio FUERA del bundle es 1, y NO menciona el remedio del bundle.
	mk otro && printf 'x\n' > "$base/otro/nuevo.md"
	out=$(preflight "$base/otro" 2>&1); rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 2 (dirty) expected 1, got $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"console bundle"*) echo "selftest CASE 2: offers the bundle fix when no bundle is present"; fails=$((fails+1)) ;; esac

	# CASO 3 — el residuo del bundle sale nombrado CON su remedio.
	mk bundle && printf 'var b=2\n' > "$base/bundle/core/internal/webui/dist/assets/y-BBBB.js"
	out=$(preflight "$base/bundle" 2>&1); rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 3 (bundle) expected 1, got $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"clean -fdq -- core/internal/webui/dist"*) ;; *) echo "selftest CASE 3: missing the command to remove residue"; fails=$((fails+1)) ;; esac

	# CASO 4 — SIN SUJETO. Un directorio que no es repo es 2, nunca 0.
	mkdir -p "$base/norepo"
	out=$(preflight "$base/norepo" 2>&1); rc=$?
	[ "$rc" = 2 ] || { echo "selftest CASE 4 (not a repository) expected 2, got $rc: $out"; fails=$((fails+1)); }

	# CASO 5 — una ruta inexistente también es 2.
	out=$(preflight "$base/no-existe" 2>&1); rc=$?
	[ "$rc" = 2 ] || { echo "selftest CASE 5 (missing path) expected 2, got $rc: $out"; fails=$((fails+1)); }

	# CASO 6 — TMPDIR que EJECUTA es 0, y lo dice.
	out=$(TMPDIR="$tmpok" tmpdir_ejecuta 2>&1); rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 6 (TMPDIR supports execution) expected 0, got $rc: $out"; fails=$((fails+1)); }

	# CASO 7 — un TMPDIR en el que no se puede ni ESCRIBIR es 1, nunca 0.
	mkdir -p "$base/sinpermiso" && chmod 000 "$base/sinpermiso" 2>/dev/null
	out=$(TMPDIR="$base/sinpermiso" tmpdir_ejecuta 2>&1); rc=$?
	chmod 755 "$base/sinpermiso" 2>/dev/null
	[ "$rc" = 1 ] || { echo "selftest CASE 7 (TMPDIR not writable) expected 1, got $rc: $out"; fails=$((fails+1)); }

	# CASO 8 — un TMPDIR montado `noexec` es 1 y NOMBRA el remedio. En estos contenedores /tmp lo
	#          está; si en otro sí ejecutara, se DICE en vez de saltárselo en silencio.
	if printf '#!/bin/sh\nexit 0\n' > /tmp/.olv-sp.$$ 2>/dev/null && chmod +x /tmp/.olv-sp.$$ 2>/dev/null && ! /tmp/.olv-sp.$$ >/dev/null 2>&1; then
		out=$(TMPDIR=/tmp tmpdir_ejecuta 2>&1); rc=$?
		[ "$rc" = 1 ] || { echo "selftest CASE 8 (/tmp noexec) expected 1, got $rc"; fails=$((fails+1)); }
		case "$out" in *GOTMPDIR*) ;; *) echo "selftest CASE 8: does not name the fix"; fails=$((fails+1)) ;; esac
	else
		echo "selftest CASE 8: /tmp supports execution on this host; case not exercised (not a pass)"
	fi
	rm -f /tmp/.olv-sp.$$
	rm -rf "$tmpok"

	# CASO 9 — bundle AL DIA: deja pasar y lo dice.
	out=$(OLIVARES_PREFLIGHT_BUNDLE_CMD=true bundle_al_dia "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 9 (current bundle) expected 0, got $rc: $out"; fails=$((fails+1)); }

	# CASO 10 — bundle OBSOLETO: rehusa Y da el comando exacto que lo arregla. Sin la segunda
	#           mitad, un rehuse mudo obliga a adivinar justo cuando ya vas con prisa.
	out=$(OLIVARES_PREFLIGHT_BUNDLE_CMD=false bundle_al_dia "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 10 (stale bundle) expected 1, got $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"task lint:web-bundle-freshness"*) ;; *) echo "selftest CASE 10: missing the command to fix it"; fails=$((fails+1)) ;; esac

	# CASO 11 — una raíz conocida, propia, vieja y sin proceso vivo sí se retira.
	mkdir -p "$OLIVARES_PREFLIGHT_TMP_ROOT/engine-citations-OLD001"
	touch -d '4 hours ago' "$OLIVARES_PREFLIGHT_TMP_ROOT/engine-citations-OLD001"
	out=$(OLIVARES_PREFLIGHT_TMP_MAX_AGE_MINUTES=60 cleanup_stale_tmp 2>&1); rc=$?
	[ "$rc" = 0 ] && [ ! -e "$OLIVARES_PREFLIGHT_TMP_ROOT/engine-citations-OLD001" ] || {
		echo "selftest CASE 11 (old unused temporary directory) was not removed: rc=$rc: $out"; fails=$((fails+1)); }

	# CASO 12 — la misma edad no autoriza borrar una raíz que sea cwd de un proceso exacto.
	mkdir -p "$OLIVARES_PREFLIGHT_TMP_ROOT/program-anchors.LIVE01"
	touch -d '4 hours ago' "$OLIVARES_PREFLIGHT_TMP_ROOT/program-anchors.LIVE01"
	sh -c 'cd "$1" || exit 1; exec sleep 30' _ "$OLIVARES_PREFLIGHT_TMP_ROOT/program-anchors.LIVE01" &
	live_pid=$!
	for _ in $(seq 1 50); do
		[ "$(readlink "/proc/$live_pid/cwd" 2>/dev/null)" = "$OLIVARES_PREFLIGHT_TMP_ROOT/program-anchors.LIVE01" ] && break
		sleep 0.1
	done
	out=$(OLIVARES_PREFLIGHT_TMP_MAX_AGE_MINUTES=60 cleanup_stale_tmp 2>&1); rc=$?
	[ "$rc" = 0 ] && [ -d "$OLIVARES_PREFLIGHT_TMP_ROOT/program-anchors.LIVE01" ] || {
		echo "selftest CASE 12 (old live temporary directory) was not preserved: rc=$rc: $out"; fails=$((fails+1)); }
	kill "$live_pid" 2>/dev/null; wait "$live_pid" 2>/dev/null; live_pid=""

	# CASO 13 — el nombre coincide, pero una raíz joven queda intacta.
	mkdir -p "$OLIVARES_PREFLIGHT_TMP_ROOT/tmp.YOUNG00001"
	out=$(OLIVARES_PREFLIGHT_TMP_MAX_AGE_MINUTES=60 cleanup_stale_tmp 2>&1); rc=$?
	[ "$rc" = 0 ] && [ -d "$OLIVARES_PREFLIGHT_TMP_ROOT/tmp.YOUNG00001" ] || {
		echo "selftest CASE 13 (young temporary directory) was not preserved: rc=$rc: $out"; fails=$((fails+1)); }

	# CASO 14 — sin `task` no hay veredicto sobre el bundle: es 2, nunca el 0 que autoriza el push.
	# PATH vacío es una sonda hermética: `command` y `echo` son builtins y esta rama no ejecuta nada.
	mkdir -p "$base/no-tools"
	out=$(PATH="$base/no-tools" OLIVARES_PREFLIGHT_BUNDLE_CMD= bundle_al_dia "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 2 ] || { echo "selftest CASE 14 (task missing) expected 2, got $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"COULD NOT CHECK"*) ;; *) echo "selftest CASE 14: does not distinguish a missing tool"; fails=$((fails+1)) ;; esac

	if [ "$fails" -eq 0 ]; then
		echo "check-push-preflight --selftest: 14/14 (includes TMPDIR noexec, cleanup with live-use guard, stale bundle, and missing task)"
		return 0
	fi
	echo "check-push-preflight --selftest: $fails failing case(s)"
	return 1
}

case "${1:-}" in
--selftest) selftest; exit $? ;;
"") preflight "$(pwd)"; exit $? ;;
*) preflight "$1"; exit $? ;;
esac
