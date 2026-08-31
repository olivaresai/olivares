#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Batería de check-holds.sh (INT-23). Fixtures reales; el guardián de sandbox se invoca desde el
# shell principal y exige que la ruta cuelgue de $TMP — una batería mía ya escribió dentro del repo.
set -euo pipefail

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
RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUION="$RAIZ/scripts/check-holds.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/holds-bat.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pasa=0; falla=0

sandbox() {
	case "$1" in
		"") echo "⛔ ruta VACÍA: 'cd \"\"' sale 0 y trabajaría en el repo." >&2; exit 2;;
		"$TMP"/*) : ;;
		*) echo "⛔ fixture fuera del sandbox: $1" >&2; exit 2;;
	esac
	[ -d "$1" ] || { echo "⛔ fixture inexistente: $1" >&2; exit 2; }
}

registro() { # $1 dir
	mkdir -p "$1/design"
	cat > "$1/design/HOLDS-Y-VETOS-VIGENTES.md" <<'EOF'
| PR | Sujeto | Veredicto | Por qué |
|---|---|---|---|
| **#709** | coraza | ⛔ **NO SE MERGEA** | sus activos ya están dentro |
| — | rama NCM | ⏸ HOLD | de su propio autor |
EOF
}

repo() { # $1 nombre -> imprime dir
	local d="$TMP/$1"; mkdir -p "$d"
	git init -q -b main "$d"; git -C "$d" config user.name t; git -C "$d" config user.email t@t
	git -C "$d" config commit.gpgsign false
	echo base > "$d/f"; git -C "$d" add f; git -C "$d" commit -qm base
	registro "$d"; git -C "$d" add -A; git -C "$d" commit -qm "registro"
	# ⛔ EL FIXTURE MONTA UN REMOTO DE VERDAD, y no es adorno: la fase 2 resuelve la punta vetada
	# por `ls-remote origin refs/pull/<N>/head`. Un contraste `sol max` midio que con fixtures SIN
	# remoto —los de antes— un doble de git que hacia fallar TODOS los `ls-remote` dejaba la
	# bateria en 13/0: **el camino que produccion usa era el unico sin testar**, y el worktree real
	# tenia cero refs locales, o sea que ese era el camino de verdad para los cuatro vetos.
	local r="$TMP/$1-remoto.git"
	git init -q --bare "$r"
	git -C "$d" remote add origin "$r"
	git -C "$d" push -q origin main 2>/dev/null || true
	# La punta de #709: un commit que NO esta en HEAD, para que el caso por defecto sea
	# «el veto existe y no se pisa».
	local lado
	lado="$(git -C "$d" commit-tree "$(git -C "$d" rev-parse HEAD^{tree})" -p "$(git -C "$d" rev-parse HEAD)" -m "punta de #709, fuera de HEAD")"
	git -C "$d" update-ref refs/pull/709/head "$lado"
	git -C "$d" push -q origin "refs/pull/709/head:refs/pull/709/head" 2>/dev/null || true
	git -C "$d" update-ref -d refs/pull/709/head
	git -C "$d" update-ref refs/remotes/origin/main "$(git -C "$d" rev-parse HEAD)"
	printf '%s' "$d"
}

# La punta que el fixture le dio a #709 — se pregunta AL REMOTO, como hace el guion.
punta709() { git -C "$1" ls-remote origin refs/pull/709/head | cut -f1; }

comprueba() { # nombre · rc esperado · patrón · dir · [env...]
	local n="$1" e="$2" pat="$3" d="$4"; shift 4
	sandbox "$d"
	local out rc
	# ⛔ `-u OLIVARES_PUSH_REFS_FILE` NO ES HIGIENE: sin el, esta bateria MIENTE DENTRO DE UN PUSH.
	# El hook deja ahi los OID que se van a publicar y `check-holds.sh` los toma como SUJETO. Los
	# casos que NO pasan esa variable esperan modo manual sobre su repo de fixture; corriendo bajo
	# el hook heredaban los OID REALES, que no existen en el fixture, y `merge-base` fallaba: el
	# gate respondia PARCIAL (correctamente — un error no es «no es ancestro») y el caso leia eso
	# como rojo. Medido el 2026-08-26: suelta 16/0, dentro del push 11/5, MISMO arbol.
	# Va ANTES de "$@" a proposito: los dos casos que SI quieren modo push la pasan ellos y ganan.
	out="$(cd "$d" && env -u OLIVARES_PUSH_REFS_FILE OLIVARES_ROOT="$d" OLIVARES_HOLDS_RANGE="HEAD" "$@" bash "$GUION" 2>&1)" && rc=0 || rc=$?
	local ok=1; [ "$rc" = "$e" ] || ok=0
	# ⛔ HERE-STRING, NO TUBERÍA. `printf … | grep -q` devuelve **141 EN ÉXITO** bajo `pipefail`:
	# grep encuentra, cierra su entrada y printf muere de SIGPIPE. Ya me costó dos horas de
	# carril rojo el 2026-08-17 y lo repetí HOY en las dos baterías que escribí, bloqueando el
	# push de los cinco carriles hasta que `lint:sigpipe-booleans` lo nombró.
	if [ -n "$pat" ] && ! grep -q -- "$pat" <<<"$out"; then ok=0; fi
	if [ "$ok" = 1 ]; then printf '  ok    %-56s rc=%s\n' "$n" "$rc"; pasa=$((pasa+1))
	else printf '  FALLA %-55s rc=%s (esperaba %s)\n' "$n" "$rc" "$e"; printf '%s\n' "$out" | sed 's/^/        /' | head -5; falla=$((falla+1)); fi
}

d="$(repo limpio)"
comprueba "un árbol sin merges está limpio" 0 "ninguno los pisa" "$d"

# El control del falso positivo: citar #709 en un commit NORMAL no puede bloquear.
d="$(repo mencion)"; echo x >> "$d/f"; git -C "$d" add f
git -C "$d" commit -qm "docs: explica por qué #709 NO se mergea"
comprueba "citar el PR vetado en un commit normal NO bloquea" 0 "ninguno los pisa" "$d"

# Y el positivo: un MERGE de verdad que lo nombra.
d="$(repo merge)"
git -C "$d" checkout -q -b rama; echo y >> "$d/f"; git -C "$d" add f; git -C "$d" commit -qm "trabajo"
git -C "$d" checkout -q main
git -C "$d" merge -q --no-ff -m "chore(integration): merge #709 -- la coraza" rama
comprueba "un MERGE que nombra el PR vetado se rechaza" 1 "está vetado" "$d"

d="$(repo sinregistro)"; rm -f "$d/design/HOLDS-Y-VETOS-VIGENTES.md"
comprueba "sin registro es NO HE PODIDO MIRAR, no limpio" 2 "NO HE PODIDO MIRAR" "$d"

d="$(repo formato)"; printf '| nada que parsear |\n' > "$d/design/HOLDS-Y-VETOS-VIGENTES.md"
comprueba "un registro que no rinde vetos es NO HE PODIDO MIRAR" 2 "NO HE PODIDO MIRAR" "$d"

# ⛔ UN VETO NO PUEDE DEJAR DE APLICARSE POR HABERSE PUBLICADO. El rango era
# `HEAD --not --remotes`, asi que el mismo merge daba rc 1 en local y rc 0 en cuanto una ref
# remota lo alcanzaba — y publicar la rama antes de promocionarla es el camino normal.
d="$(repo vetopublicado)"
git -C "$d" checkout -q -b lateral
echo x > "$d/g"; git -C "$d" add g; git -C "$d" commit -qm trabajo
git -C "$d" checkout -q main
git -C "$d" merge -q --no-ff lateral -m "Merge pull request #709 from fran/coraza"
git -C "$d" update-ref refs/remotes/origin/main "$(git -C "$d" rev-parse HEAD~1)"
comprueba "un merge vetado LOCAL se caza" 1 "pisan un veto" "$d" \
	OLIVARES_HOLDS_RANGE="HEAD --not origin/main"
# CONTROL: y sigue cazandose cuando OTRA ref remota ya lo alcanza, que es el caso que fallaba.
git -C "$d" update-ref refs/remotes/origin/staging "$(git -C "$d" rev-parse HEAD)"
# ⚠ ESTA VA CON EL RANGO **VACIO** A PROPOSITO: asi ejercita la ELECCION POR DEFECTO del guion,
# no un valor que le pasa la bateria. Con el rango forzado, la casilla pasaria igual con el guion
# viejo — seria un testigo que no testifica. Control de mutacion hecho: con el default anterior
# (`HEAD --not --remotes`) esta casilla da rc 0 y FALLA.
comprueba "y sigue cazandose ya publicado en otra rama" 1 "pisan un veto" "$d" \
	OLIVARES_HOLDS_RANGE=

d="$(repo rangomalo)"
comprueba "un rango ilegible es NO HE PODIDO MIRAR, no limpio" 2 "NO HE PODIDO MIRAR" "$d" \
	OLIVARES_HOLDS_RANGE=rama-que-no-existe-xyz

if ( sandbox "/etc" ) 2>/dev/null; then echo "  FALLA el guardián aceptó /etc"; falla=$((falla+1))
else echo "  ok    el guardián rechaza una ruta fuera del sandbox      rc=2"; pasa=$((pasa+1)); fi

# ── FASE 2 · CONTENCION ─────────────────────────────────────────────────────────────────────
# ⛔ TODOS ESTOS CASOS SALEN DE UN CONTRASTE QUE DECLARO **NO APTA** LA PRIMERA VERSION DE LA
# FASE 2, con cuatro caminos de `rc 0` reproducidos. Mi bateria de entonces tenia 13 casos y
# pasaba entera: un 13/0 propio no prueba robustez, prueba que los casos que se me ocurrieron
# pasan. Estos son los que NO se me ocurrieron.

# EL DEFECTO QUE YA OCURRIO: un lote montado con `git merge <ref>` — sin numero de PR en el
# asunto — incorporo #675, en HOLD, y el gate salio 0.
d="$(repo contenido)"
git -C "$d" merge --no-edit -q "$(punta709 "$d")"
git -C "$d" merge-base --is-ancestor "$(punta709 "$d")" HEAD ||
	{ echo "  ⛔ PRECONDICION MUERTA: el merge del fixture no metio la punta"; falla=$((falla+1)); }
comprueba "un merge SIN el numero, con la punta vetada dentro, se caza" 1 "CONTENIDA en" "$d"
comprueba "y su mensaje NO nombra el PR (premisa del caso)" 1 "Ningun mensaje la nombra\|ningún mensaje\|Ningún mensaje" "$d"

# ⛔ H-01 · EL SUJETO ES LO QUE SE PUBLICA, NO EL CHECKOUT. La v1 miraba HEAD y daba VERDE con
# el veto en la ref empujada. `git push origin lote:main` desde un checkout limpio la esquivaba.
d="$(repo sujeto_push)"
lote="$(git -C "$d" commit-tree "$(git -C "$d" rev-parse HEAD^{tree})" -p "$(git -C "$d" rev-parse HEAD)" -p "$(punta709 "$d")" -m "lote que mete la punta vetada")"
git -C "$d" merge-base --is-ancestor "$(punta709 "$d")" HEAD 2>/dev/null &&
	{ echo "  ⛔ PRECONDICION MUERTA: HEAD deberia estar LIMPIO"; falla=$((falla+1)); }
git -C "$d" merge-base --is-ancestor "$(punta709 "$d")" "$lote" ||
	{ echo "  ⛔ PRECONDICION MUERTA: el OID a publicar deberia llevarla"; falla=$((falla+1)); }
printf 'refs/heads/lote %s refs/heads/main %s\n' "$lote" "0000000000000000000000000000000000000000" > "$TMP/pushrefs"
comprueba "HEAD limpio pero el OID EMPUJADO lleva el veto: se caza" 1 "CONTENIDA en" "$d" OLIVARES_PUSH_REFS_FILE="$TMP/pushrefs"

# Un push de solo BORRADOS no publica nada: no hay sujeto que mirar.
d="$(repo solo_borrado)"
printf '(delete) %s refs/heads/vieja %s\n' "0000000000000000000000000000000000000000" "$(git -C "$d" rev-parse HEAD)" > "$TMP/pushdel"
comprueba "un push de solo borrados no tiene sujeto" 0 "no publica" "$d" OLIVARES_PUSH_REFS_FILE="$TMP/pushdel"

# La punta vetada YA en el tronco no se imputa a este push.
d="$(repo ya_en_main)"
git -C "$d" merge --no-edit -q "$(punta709 "$d")"
git -C "$d" update-ref refs/remotes/origin/main HEAD
git -C "$d" merge-base --is-ancestor "$(punta709 "$d")" HEAD ||
	{ echo "  ⛔ PRECONDICION MUERTA: la punta deberia estar en HEAD"; falla=$((falla+1)); }
git -C "$d" merge-base --is-ancestor "$(punta709 "$d")" origin/main ||
	{ echo "  ⛔ PRECONDICION MUERTA: la punta deberia estar en origin/main"; falla=$((falla+1)); }
comprueba "la punta vetada ya en origin/main NO se imputa al lote" 0 "ninguno los pisa" "$d" \
	OLIVARES_HOLDS_RANGE="HEAD --not origin/main"

# ⛔ H-03 · UNA HISTORIA SHALLOW NO DA ERROR: **MIENTE**. Medido con git real: en un clon
# `--depth 1`, preguntar si un commit anterior al corte es ancestro de HEAD devuelve **1**
# («no es ancestro»), NO 128 — y en la historia completa SI lo es. Ninguna clasificacion de
# codigos de salida distingue eso, asi que en shallow el NEGATIVO es PARCIAL.
d="$(repo shallow_miente)"
git -C "$d" push -q origin main 2>/dev/null || true
sh_d="$TMP/clon_shallow"
git clone -q --depth 1 "file://$TMP/shallow_miente-remoto.git" "$sh_d" 2>/dev/null || true
if [ -d "$sh_d/.git" ]; then
	cp -r "$d/design" "$sh_d/design" 2>/dev/null || true
	git -C "$sh_d" remote set-url origin "$TMP/shallow_miente-remoto.git" 2>/dev/null || true
	# PRECONDICION del caso: el clon tiene que ser shallow de verdad, o no testifica.
	if [ "$(git -C "$sh_d" rev-parse --is-shallow-repository 2>/dev/null)" != "true" ]; then
		echo "  ⛔ PRECONDICION MUERTA: el clon no salio shallow"; falla=$((falla+1))
	fi
	comprueba "en un repo SHALLOW un negativo es PARCIAL (2), no limpio" 2 "SHALLOW" "$sh_d"
else
	echo "  ⛔ PRECONDICION MUERTA: no pude crear el clon shallow"; falla=$((falla+1))
fi

# ⛔ EL CAMINO REMOTO, que es el que produccion usa y la bateria vieja NO tocaba. Se rompe de
# verdad: `origin` apuntando a una ruta inexistente hace fallar `ls-remote` y `fetch` sin dobles.
d="$(repo remoto_mudo)"
git -C "$d" update-ref -d refs/pull/709/head 2>/dev/null || true
git -C "$d" remote set-url origin "$TMP/no-existe-este-remoto.git"
git -C "$d" ls-remote origin refs/pull/709/head >/dev/null 2>&1 &&
	{ echo "  ⛔ PRECONDICION MUERTA: ls-remote deberia FALLAR"; falla=$((falla+1)); }
git -C "$d" rev-parse -q --verify refs/pull/709/head >/dev/null 2>&1 &&
	{ echo "  ⛔ PRECONDICION MUERTA: no deberia quedar ref local"; falla=$((falla+1)); }
comprueba "sin remoto NI ref local, la contencion es PARCIAL (2)" 2 "PARCIAL" "$d"

echo "check-holds: $pasa passed, $falla failed"
[ "$falla" -eq 0 ]
