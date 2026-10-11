#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-tenant-bound-reads.sh — el trinquete de LECTURA del binding de inquilino.
#
# ⛔ QUÉ DEFECTO CIERRA, y no es hipotético: está medido y escrito en el árbol. Una clave de
#    react-query SIN el inquilino hace que dos inquilinos COMPARTAN entrada de caché. La víctima
#    no ve un error: ve DATOS DE OTRO. Palabras de `web/src/lib/api/search.ts`, sobre este mismo
#    defecto ya corregido:
#
#      «Con `['console-search', q]` las dos compartían entrada: buscar «acme» en A, cambiar a B y
#       repetir el término dentro de los 30 s de `staleTime` servía de caché los nombres de entidad
#       de A bajo la cabecera de B, SIN EMITIR UNA SOLA PETICIÓN. Nadie limpia la caché al cambiar
#       de inquilino — el único `queryClient.clear()` es el del logout —, así que la clave es la
#       ÚNICA segregación que hay.»
#
#    ⇒ La clave NO es contabilidad: es la frontera. Por eso esto es un gate y no una convención.
#
# ⛔ Y ES UN TRINQUETE, NO UNA CAMPAÑA: medido el 2026-08-24, el árbol está LIMPIO — 406 entradas
#    de clave, 377 llevan inquilino y las 29 que no lo llevan es porque NO DEBEN. Este guion existe
#    para que la 407 no entre mal, no para arreglar nada hoy.
#
# ⛔ LA EXENCIÓN SE DERIVA DEL MOTOR, no de una lista de perdones. Una clave puede ser global sólo
#    si su handler lo es, y hay tres formas medidas de serlo:
#      · `authzSystem(...)`          — superficie de superadmin (`/v1/console/*`, backups, registry)
#      · el handler IGNORA el contexto (`_ api.ModuleContext`) — catálogo estático igual para todos
#      · el recurso ES el inquilino  — listar inquilinos es cross-tenant por definición
#
# TRES RESPUESTAS: 0 limpio · 1 hay claves sin atar · 2 NO HE PODIDO MIRAR.
set -uo pipefail
export LC_ALL=C

cannot_check() { printf 'check-tenant-bound-reads: 2 COULD NOT CHECK — %s\n' "$*" >&2; exit 2; }
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)" || cannot_check "cannot resolve the root"

# ── el censo de claves, sobre un árbol cualquiera ─────────────────────────────────────────────
# Imprime `fichero<TAB>nombre` por cada entrada de clave SIN inquilino.
#
# ⛔ LAS FIRMAS OCUPAN VARIAS LÍNEAS y mi primera versión no lo veía: `holdCheck: (\n  tenant: ...`
#    salía como SIN inquilino llevándolo. Un patrón de una línea inventa hallazgos, y el falso
#    positivo es peor aquí que el negativo: manda a "arreglar" una clave correcta añadiéndole un
#    parámetro que sus llamadores no pasan.
censar_lecturas() {
	local raiz="$1"
	[ -d "$raiz" ] || { printf 'missing %s\n' "$raiz" >&2; return 2; }
	local source_files
	source_files="$(grep -rlE '^export const [A-Za-z]*[Kk]eys' "$raiz" --include='*.ts' --include='*.tsx' 2>/dev/null | grep -v '\.test\.' || true)"
	[ -n "$source_files" ] || { printf 'no query-key objects in %s\n' "$raiz" >&2; return 2; }
	printf '%s\n' "$source_files" | while IFS= read -r f; do
		[ -n "$f" ] || continue
		OLIVARES_KEYS_FILE="$f" python3 - <<'PY'
import os, re
p = os.environ["OLIVARES_KEYS_FILE"]
try:
    s = open(p, encoding="utf8").read()
except OSError:
    raise SystemExit(0)
for m in re.finditer(r'^export const ([A-Za-z]*[Kk]eys)\s*=\s*\{(.*?)^\}', s, re.S | re.M):
    cuerpo = m.group(2)
    for e in re.finditer(r'^  ([A-Za-z_][A-Za-z0-9_]*):\s*\((.*?)\)\s*=>', cuerpo, re.S | re.M):
        nombre, args = e.group(1), e.group(2)
        if not re.search(r'\btenant\b|\bt\s*:', args):
            print(f"{p}\t{nombre}")
PY
	done
}

# ── selftest ──────────────────────────────────────────────────────────────────────────────────
if [ "${1:-}" = "--selftest" ]; then
	fail=0; oks=0; fails=0
	T="$(mktemp -d "${TMPDIR:-/tmp}/tbr.XXXXXX")" || cannot_check "mktemp failed"
	trap 'rm -rf "$T"' EXIT
	ok()  { oks=$((oks + 1));  printf '  ok    %s\n' "$*"; }
	bad() { fails=$((fails + 1)); fail=1; printf '  FAIL  %s\n' "$*"; }

	mkdir -p "$T/src"
	printf 'export const xKeys = {\n  malo: (params?: unknown) => ["x", params] as const,\n}\n' > "$T/src/a.ts"
	output="$(censar_lecturas "$T/src" || true)"
	case "$output" in *malo*) ok "a key without tenant binding is detected" ;; *) bad "misses a key without tenant binding" ;; esac

	printf 'export const xKeys = {\n  bueno: (tenant: string | null, params?: unknown) => ["x", tenant, params] as const,\n}\n' > "$T/src/a.ts"
	output="$(censar_lecturas "$T/src" || true)"
	case "$output" in *bueno*) bad "reports a key that binds the tenant; does not distinguish the cases" ;; *) ok "a tenant-bound key is not a finding" ;; esac

	# ⛔ LA CELDA QUE ME FALTABA EN LA PRIMERA VERSIÓN: la firma multilínea.
	printf 'export const xKeys = {\n  multi: (\n    tenant: string | null,\n    q: { a?: string },\n  ) => ["x", tenant, q] as const,\n}\n' > "$T/src/a.ts"
	output="$(censar_lecturas "$T/src" || true)"
	case "$output" in *multi*) bad "reports a multiline signature with tenant binding: false positive" ;; *) ok "a multiline signature with tenant binding is read correctly" ;; esac

	# La forma corta `t:` también cuenta como inquilino (la usa medio árbol).
	printf 'export const xKeys = {\n  corto: (t: string | null) => ["x", t] as const,\n}\n' > "$T/src/a.ts"
	output="$(censar_lecturas "$T/src" || true)"
	case "$output" in *corto*) bad 'short form t: is not recognized as the tenant' ;; *) ok 'short form t: counts as the tenant' ;; esac

	# Un directorio sin claves es 2, no 0: «no he podido mirar» no es «limpio».
	mkdir -p "$T/vacio"; printf 'export const nada = 1\n' > "$T/vacio/b.ts"
	censar_lecturas "$T/vacio" >/dev/null 2>&1
	case $? in 2) ok "a tree without query-key objects exits 2, not 0" ;; *) bad "a tree without keys does not exit 2" ;; esac

	# Árbol REAL: la exención tiene que cubrir exactamente lo que sale.
	if [ -d "$ROOT/web/src" ]; then
		reales="$(censar_lecturas "$ROOT/web/src" 2>/dev/null | wc -l | tr -d ' ')"
		[ "${reales:-0}" -gt 0 ] && ok "actual tree: scan finds ${reales} key(s) without tenant binding (all exempted below)" \
			|| bad "actual tree: scan finds no keys; expected global keys"
	fi

	# ⛔ UNA LINEA BASE ABSOLUTA TIENE QUE FUNCIONAR, y esta celda existe por un fallo propio: el
	#    guion hacia siempre "$ROOT/$BASE", asi que medir un arbol FUSIONADO —lo unico que dice si
	#    el gate estara verde AL ATERRIZAR— daba 2 con el fichero delante.
	mkdir -p "$T/abs"
	printf 'export const xKeys = {\n  glob: () => ["x"] as const,\n}\n' > "$T/abs/a.ts"
	printf 'a.ts\tglob\n' > "$T/base-abs.txt"
	OLIVARES_WEB_SRC="$T/abs" OLIVARES_TBR_BASELINE="$T/base-abs.txt" bash "$0" >/dev/null 2>&1
	case $? in
	0) ok "an absolute baseline is honored (merged tree can be measured)" ;;
	*) bad "an absolute baseline is not honored; cannot measure the merged tree" ;;
	esac

	[ "$fail" = "0" ] && { echo "check-tenant-bound-reads selftest: ${oks} passed, ${fails} failed"; exit 0; }
	echo "check-tenant-bound-reads selftest: ${oks} passed, ${fails} failed"; exit 1
fi

# ── corrida normal ────────────────────────────────────────────────────────────────────────────
SRC="${OLIVARES_WEB_SRC:-$ROOT/web/src}"
BASE="${OLIVARES_TBR_BASELINE:-docs/tenant-bound-reads-baseline.txt}"
# ⛔ UNA RUTA ABSOLUTA NO SE PREFIJA. La primera version hacia siempre "$ROOT/$BASE", asi que un
#    override absoluto —el que hace falta para medir un arbol FUSIONADO, que es donde este gate
#    tiene que estar verde ANTES de aterrizar— quedaba como "$ROOT//tmp/...". El gate contestaba 2
#    («no he podido mirar»), que es honesto, pero por la razon equivocada: el fichero SI existia.
case "$BASE" in /*) BASE_ABS="$BASE" ;; *) BASE_ABS="$ROOT/$BASE" ;; esac
[ -d "$SRC" ] || cannot_check "missing $SRC"

output="$(censar_lecturas "$SRC")" ; rc=$?
[ "$rc" = "2" ] && cannot_check "the scan could not traverse $SRC"

if [ ! -f "$BASE_ABS" ]; then
	cannot_check "missing baseline $BASE_ABS; it is required to distinguish legitimate global keys from keys missing tenant binding"
fi

nuevas=""
# Listas capturadas UNA vez: se comparan con `case`, no con tuberias.
EXENTAS="$(grep -v '^#' "$BASE_ABS" 2>/dev/null || true)"
RELATIVE_OUTPUT="$(printf '%s\n' "$output" | sed "s|^${SRC}/||")"
while IFS= read -r linea; do
	[ -n "$linea" ] || continue
	clave="$(printf '%s' "$linea" | sed "s|^${SRC}/||")"
	# ⛔ SIN TUBERIA hacia `grep -q`: bajo `set -o pipefail` sale al primer casamiento, el
	#    productor recibe SIGPIPE y pipefail propaga 141 — la comprobacion FALLA JUSTO CUANDO
	#    ACIERTA, y de forma intermitente. Lo caza `lint:sigpipe-booleans`, y me lo cazo a MI
	#    escribiendo este mismo guion, tres parrafos debajo de mi propio aviso sobre la clase.
	case $'\n'"$EXENTAS"$'\n' in
	*$'\n'"$clave"$'\n'*) ;;
	*) nuevas="${nuevas}${clave}
" ;;
	esac
done < <(printf '%s\n' "$output" | grep . || true)

n="$(printf '%s' "$nuevas" | grep -c . || true)"
if [ "${n:-0}" -gt 0 ]; then
	echo "check-tenant-bound-reads: ⛔ ${n} query key(s) without tenant binding or an exemption:" >&2
	printf '%s' "$nuevas" | sed 's/^/    /' >&2
	cat >&2 <<'NOTA'

  A key without a tenant makes TWO TENANTS SHARE A CACHE ENTRY. The affected user sees
  another tenant's data rather than an error. The key provides the only isolation: the cache is
  not cleared when switching tenants.

  Two options, with the first preferred:
    1. ADD the tenant to the key -> `(tenant: string | null, ...)` and `[..., tenant, ...]`
    2. If it is truly GLOBAL, verify that in the ENGINE and add it to the baseline with its rationale:
         · its handler uses `authzSystem(...)`                    -> superadmin surface
         · its handler ignores context (`_ api.ModuleContext`)   -> static catalog
         · the resource is the tenant                            -> inherently cross-tenant
       Checking the handler is required: on 2026-08-24, the asymmetry of `reporting/reports`
       raised a concern, but the engine disproved it: `handleListReports` ignores the request and
       context and returns a catalog.
NOTA
	exit 1
fi

sobran=0
while IFS= read -r l; do
	# ⛔ Los comentarios de la linea base NO son entradas. Sin este filtro, las ocho lineas que
	#    explican POR QUE cada familia es global se contaban como «ya no salen» y el gate sugeria
	#    apretar una base que estaba exacta: un consejo falso que envejece hacia el lado peor.
	case "$l" in ''|'#'*) continue ;; esac
	case $'\n'"$RELATIVE_OUTPUT"$'\n' in
	*$'\n'"$l"$'\n'*) ;;
	*) sobran=$((sobran + 1)) ;;
	esac
done < "$BASE_ABS"

total="$(printf '%s\n' "$output" | grep -c . || true)"
echo "check-tenant-bound-reads: CLEAN — ${total} global key(s), all in the baseline, none missing tenant binding.$([ "$sobran" -gt 0 ] && printf ' (%s baseline entries no longer reported: the baseline can be tightened)' "$sobran")"
exit 0
