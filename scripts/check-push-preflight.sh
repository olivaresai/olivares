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
		echo "check-push-preflight: NO HE PODIDO MIRAR: edad TMP inválida ('$age_minutes')" >&2
		return 2 ;;
	esac
	[ "${#age_minutes}" -le 7 ] || {
		echo "check-push-preflight: NO HE PODIDO MIRAR: edad TMP fuera de rango" >&2
		return 2
	}
	age_minutes=$((10#$age_minutes))
	[ "$age_minutes" -gt 0 ] || {
		echo "check-push-preflight: NO HE PODIDO MIRAR: la edad TMP debe ser mayor que cero" >&2
		return 2
	}
	[ -d "$root" ] || {
		echo "check-push-preflight: NO HE PODIDO MIRAR: raíz TMP inexistente ('$root')" >&2
		return 2
	}
	root="$(cd -- "$root" 2>/dev/null && pwd -P)" || return 2
	case "$root" in /|'')
		echo "check-push-preflight: NO HE PODIDO MIRAR: rehúso una raíz TMP amplia ('$root')" >&2
		return 2 ;;
	esac
	uid="$(id -u)" || return 2
	now="$(date +%s)" || return 2
	for tool in find grep readlink rm stat; do
		command -v "$tool" >/dev/null 2>&1 || {
			echo "check-push-preflight: NO HE PODIDO MIRAR: falta '$tool' para una poda segura" >&2
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
		echo "check-push-preflight: TMP viejo: 0 raíces candidatas."
		return 0
	fi

	readlink "/proc/$$/cwd" >/dev/null 2>&1 || {
		[ "$restore_nullglob" -eq 0 ] || shopt -u nullglob
		echo "check-push-preflight: NO HE PODIDO MIRAR: /proc no permite comprobar procesos vivos" >&2
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
		echo "check-push-preflight: NO HE PODIDO MIRAR: la instantánea /proc quedó vacía" >&2
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
	echo "check-push-preflight: TMP viejo: $removed raíz(es) retirada(s); $live_count viva(s) preservada(s)."
	[ "$errors" -eq 0 ] || {
		echo "check-push-preflight: NO HE PODIDO LIMPIAR $errors raíz(es) TMP" >&2
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
		echo "check-push-preflight: ⛔ no puedo ni ESCRIBIR en TMPDIR ('$d')."; return 1; }
	chmod +x "$probe" 2>/dev/null
	if "$probe" >/dev/null 2>&1; then
		rm -f "$probe"
		echo "check-push-preflight: TMPDIR ('$d') EJECUTA — go test podrá correr sus binarios."
		return 0
	fi
	rm -f "$probe"
	echo "check-push-preflight: ⛔ NO ARRANQUES: TMPDIR ('$d') NO ejecuta binarios."
	echo "check-push-preflight:    go test compila ahí y luego EJECUTA: el gate moriría en"
	echo "check-push-preflight:    lint:cli-registries con ~40 min ya pagados. Arranca así:"
	echo "       T=/workspace/.olv-push-tmp-\$\$; mkdir -p \"\$T\""
	echo "       TMPDIR=\"\$T\" GOTMPDIR=\"\$T\" git push origin <sha>:refs/heads/<rama>"
	return 1
}

bundle_al_dia() { # <worktree> -> 0 al día | 1 obsoleto | (dice y deja pasar si no puede mirar)
	# ⛔ LA TERCERA FORMA DE MORIR CON EL GATE CASI PAGADO, y la que más cara sale porque el
	#    mensaje final culpa a «un gate» sin decir cuál. Cazada en vivo el 2026-08-26:
	#
	#      `lint:git-env` corre en el hook y NO llama a los guiones de su clase: los EJECUTA para
	#      ver si se aíslan. Uno de ellos reconstruye la consola en su propio worktree
	#      (`vite` con `emptyOutDir: true`), así que VACÍA `core/internal/webui/dist` y lo
	#      reescribe. Si el bundle commiteado estaba AL DÍA el resultado es byte-idéntico y no
	#      pasa nada; si estaba OBSOLETO el árbol se queda sucio y `check-tree-untouched` mata el
	#      push — a 1 h 48 la vez que lo medimos.
	#
	#    ⇒ Un bundle obsoleto es lo único que convierte esa sonda en un push muerto, y se ve en
	#      UN SEGUNDO (medido) con el lint que ya existe. Aquí, no a las dos horas.
	local wt="$1" cmd rc
	# Inyectable para poder probar las DOS ramas sin un shim en PATH: en estos contenedores
	# TMPDIR está montado noexec, PATH se lo saltaría en silencio y el test mediría el `task` real.
	cmd="${OLIVARES_PREFLIGHT_BUNDLE_CMD:-task lint:web-bundle-freshness}"
	if [ "${OLIVARES_PREFLIGHT_BUNDLE_CMD:-}" = "" ] && ! command -v task >/dev/null 2>&1; then
		echo "check-push-preflight: no encuentro 'task' — NO he comprobado si el bundle está al día." >&2
		return 0
	fi
	( cd "$wt" 2>/dev/null && eval "$cmd" ) >/dev/null 2>&1; rc=$?
	if [ "$rc" -eq 0 ]; then
		echo "check-push-preflight: bundle empotrado AL DÍA — la sonda de lint:git-env lo reconstruirá idéntico."
		return 0
	fi
	echo "check-push-preflight: ⛔ NO ARRANQUES: el bundle empotrado está OBSOLETO (rc=$rc)."
	echo "check-push-preflight:    Un gate del hook lo reconstruirá durante la corrida y el árbol"
	echo "check-push-preflight:    quedará sucio: el push muere al FINAL, con ~2 h ya pagadas."
	echo "check-push-preflight:    Arréglalo aquí, en la rama, y NO en main:"
	echo "       task build:web && git add core/internal/webui/dist core/internal/webui/bundle-source.stamp"
	return 1
}

preflight() { # preflight <worktree> -> 0 limpio | 1 sucio o TMPDIR inservible | 2 no he podido mirar
	local wt="$1" estado sucio dist
	cleanup_stale_tmp || return $?
	[ -n "$wt" ] || { echo "check-push-preflight: NO HE PODIDO MIRAR: sin worktree" >&2; return 2; }
	[ -d "$wt" ] || { echo "check-push-preflight: NO HE PODIDO MIRAR: '$wt' no es un directorio" >&2; return 2; }
	# --no-optional-locks: sondear el árbol de otro carril NO debe plantarle un index.lock.
	estado=$(git --no-optional-locks -C "$wt" status --porcelain 2>/dev/null) || {
		echo "check-push-preflight: NO HE PODIDO MIRAR: '$wt' no responde como repositorio git" >&2; return 2; }
	if ! git --no-optional-locks -C "$wt" rev-parse --git-dir >/dev/null 2>&1; then
		echo "check-push-preflight: NO HE PODIDO MIRAR: '$wt' no es un repositorio git" >&2; return 2
	fi
	sucio=$(printf '%s' "$estado" | grep -c . || true)
	if [ "${sucio:-0}" -eq 0 ]; then
		echo "check-push-preflight: árbol LIMPIO — el gate no lo rechazará por residuo."
		tmpdir_ejecuta || return 1
		bundle_al_dia "$wt" || return 1
		return 0
	fi
	echo "check-push-preflight: ⛔ NO ARRANQUES: $sucio entrada(s) sin commitear en '$wt'."
	echo "check-push-preflight:    El gate lo descubriría al final del push, no ahora. Desglose:"
	printf '%s\n' "$estado" | awk '{print substr($0,1,2)}' | sort | uniq -c | sed 's/^/       /'
	dist=$(printf '%s\n' "$estado" | grep -c 'core/internal/webui/dist' || true)
	if [ "${dist:-0}" -gt 0 ]; then
		echo "check-push-preflight:    $dist son del bundle de consola (un gate lo reconstruyó). Se retira con:"
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
	base=$(mktemp -d "${TMPDIR:-/tmp}/push-preflight.XXXXXX") || { echo "selftest: NO HE PODIDO MIRAR: mktemp"; return 2; }
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
		echo "selftest: NO HE PODIDO MIRAR: no encuentro un TMPDIR que ejecute; no puedo separar los dos ejes"
		return 2
	fi
	rm -f "$tmpok/.p"

	# CASO 1 — limpio es 0. Control positivo: sin él, un guion que siempre diga «sucio» pasaría.
	mk limpio || { echo "selftest: NO HE PODIDO MIRAR: no puedo crear el repo"; return 2; }
	out=$(TMPDIR="$tmpok" OLIVARES_PREFLIGHT_BUNDLE_CMD=true preflight "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASO 1 (limpio) esperaba 0, dio $rc: $out"; fails=$((fails+1)); }

	# CASO 2 — sucio FUERA del bundle es 1, y NO menciona el remedio del bundle.
	mk otro && printf 'x\n' > "$base/otro/nuevo.md"
	out=$(preflight "$base/otro" 2>&1); rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASO 2 (sucio) esperaba 1, dio $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"bundle de consola"*) echo "selftest CASO 2: ofrece el remedio del bundle sin haber bundle"; fails=$((fails+1)) ;; esac

	# CASO 3 — el residuo del bundle sale nombrado CON su remedio.
	mk bundle && printf 'var b=2\n' > "$base/bundle/core/internal/webui/dist/assets/y-BBBB.js"
	out=$(preflight "$base/bundle" 2>&1); rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASO 3 (bundle) esperaba 1, dio $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"clean -fdq -- core/internal/webui/dist"*) ;; *) echo "selftest CASO 3: no da el comando que retira el residuo"; fails=$((fails+1)) ;; esac

	# CASO 4 — SIN SUJETO. Un directorio que no es repo es 2, nunca 0.
	mkdir -p "$base/norepo"
	out=$(preflight "$base/norepo" 2>&1); rc=$?
	[ "$rc" = 2 ] || { echo "selftest CASO 4 (no es repo) esperaba 2, dio $rc: $out"; fails=$((fails+1)); }

	# CASO 5 — una ruta inexistente también es 2.
	out=$(preflight "$base/no-existe" 2>&1); rc=$?
	[ "$rc" = 2 ] || { echo "selftest CASO 5 (ruta inexistente) esperaba 2, dio $rc: $out"; fails=$((fails+1)); }

	# CASO 6 — TMPDIR que EJECUTA es 0, y lo dice.
	out=$(TMPDIR="$tmpok" tmpdir_ejecuta 2>&1); rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASO 6 (TMPDIR ejecuta) esperaba 0, dio $rc: $out"; fails=$((fails+1)); }

	# CASO 7 — un TMPDIR en el que no se puede ni ESCRIBIR es 1, nunca 0.
	mkdir -p "$base/sinpermiso" && chmod 000 "$base/sinpermiso" 2>/dev/null
	out=$(TMPDIR="$base/sinpermiso" tmpdir_ejecuta 2>&1); rc=$?
	chmod 755 "$base/sinpermiso" 2>/dev/null
	[ "$rc" = 1 ] || { echo "selftest CASO 7 (TMPDIR sin escritura) esperaba 1, dio $rc: $out"; fails=$((fails+1)); }

	# CASO 8 — un TMPDIR montado `noexec` es 1 y NOMBRA el remedio. En estos contenedores /tmp lo
	#          está; si en otro sí ejecutara, se DICE en vez de saltárselo en silencio.
	if printf '#!/bin/sh\nexit 0\n' > /tmp/.olv-sp.$$ 2>/dev/null && chmod +x /tmp/.olv-sp.$$ 2>/dev/null && ! /tmp/.olv-sp.$$ >/dev/null 2>&1; then
		out=$(TMPDIR=/tmp tmpdir_ejecuta 2>&1); rc=$?
		[ "$rc" = 1 ] || { echo "selftest CASO 8 (/tmp noexec) esperaba 1, dio $rc"; fails=$((fails+1)); }
		case "$out" in *GOTMPDIR*) ;; *) echo "selftest CASO 8: no nombra el remedio"; fails=$((fails+1)) ;; esac
	else
		echo "selftest CASO 8: /tmp SÍ ejecuta en esta máquina — caso no ejercitado (no es un pase)"
	fi
	rm -f /tmp/.olv-sp.$$
	rm -rf "$tmpok"

	# CASO 9 — bundle AL DIA: deja pasar y lo dice.
	out=$(OLIVARES_PREFLIGHT_BUNDLE_CMD=true bundle_al_dia "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASO 9 (bundle al dia) esperaba 0, dio $rc: $out"; fails=$((fails+1)); }

	# CASO 10 — bundle OBSOLETO: rehusa Y da el comando exacto que lo arregla. Sin la segunda
	#           mitad, un rehuse mudo obliga a adivinar justo cuando ya vas con prisa.
	out=$(OLIVARES_PREFLIGHT_BUNDLE_CMD=false bundle_al_dia "$base/limpio" 2>&1); rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASO 10 (bundle obsoleto) esperaba 1, dio $rc: $out"; fails=$((fails+1)); }
	case "$out" in *"task build:web"*) ;; *) echo "selftest CASO 10: no da el comando que lo arregla"; fails=$((fails+1)) ;; esac

	# CASO 11 — una raíz conocida, propia, vieja y sin proceso vivo sí se retira.
	mkdir -p "$OLIVARES_PREFLIGHT_TMP_ROOT/engine-citations-OLD001"
	touch -d '4 hours ago' "$OLIVARES_PREFLIGHT_TMP_ROOT/engine-citations-OLD001"
	out=$(OLIVARES_PREFLIGHT_TMP_MAX_AGE_MINUTES=60 cleanup_stale_tmp 2>&1); rc=$?
	[ "$rc" = 0 ] && [ ! -e "$OLIVARES_PREFLIGHT_TMP_ROOT/engine-citations-OLD001" ] || {
		echo "selftest CASO 11 (TMP viejo muerto) no fue retirado: rc=$rc: $out"; fails=$((fails+1)); }

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
		echo "selftest CASO 12 (TMP viejo vivo) no fue preservado: rc=$rc: $out"; fails=$((fails+1)); }
	kill "$live_pid" 2>/dev/null; wait "$live_pid" 2>/dev/null; live_pid=""

	# CASO 13 — el nombre coincide, pero una raíz joven queda intacta.
	mkdir -p "$OLIVARES_PREFLIGHT_TMP_ROOT/tmp.YOUNG00001"
	out=$(OLIVARES_PREFLIGHT_TMP_MAX_AGE_MINUTES=60 cleanup_stale_tmp 2>&1); rc=$?
	[ "$rc" = 0 ] && [ -d "$OLIVARES_PREFLIGHT_TMP_ROOT/tmp.YOUNG00001" ] || {
		echo "selftest CASO 13 (TMP joven) no fue preservado: rc=$rc: $out"; fails=$((fails+1)); }

	if [ "$fails" -eq 0 ]; then
		echo "check-push-preflight --selftest: 13/13 (limpio · sucio-sin-bundle · bundle-con-remedio · no-es-repo · ruta-inexistente · TMPDIR-ejecuta · TMPDIR-sin-escritura · TMPDIR-noexec · bundle-al-dia · bundle-obsoleto-con-remedio · TMP-viejo-muerto · TMP-viejo-vivo · TMP-joven)"
		return 0
	fi
	echo "check-push-preflight --selftest: $fails caso(s) en rojo"
	return 1
}

case "${1:-}" in
--selftest) selftest; exit $? ;;
"") preflight "$(pwd)"; exit $? ;;
*) preflight "$1"; exit $? ;;
esac
