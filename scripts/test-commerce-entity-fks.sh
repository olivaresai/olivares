#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería de scripts/check-commerce-entity-fks.sh. Construye árboles de mentira y comprueba
# los TRES veredictos, incluido un CONTROL NEGATIVO que debe salir 0: un gate que sólo se
# prueba con casos que fallan no distingue «encuentra el defecto» de «siempre grita».
#
# Y el rc se captura en la MISMA línea que ejecuta el guion. Escribirlo en un comando aparte
# —$? o ${PIPESTATUS[0]} después de un echo— mide el echo. Es la clase de defecto que este
# árbol ya tiene medida y no se repite en un fichero nuevo.
set -uo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUION="$RAIZ/scripts/check-commerce-entity-fks.sh"
pass_count=0; fail_count=0

check() { # <nombre> <esperado> <obtenido>
	if [ "$2" = "$3" ]; then printf '  ok    %-58s rc=%s\n' "$1" "$3"; pass_count=$((pass_count+1))
	else printf '  FAIL  %-58s expected=%s got=%s\n' "$1" "$2" "$3"; fail_count=$((fail_count+1)); fi
}

arbol() { # -> imprime la ruta de un árbol de mentira completo y sano
	local d; d="$(mktemp -d "${TMPDIR:-/tmp}/commfk-t.XXXXXX")"
	mkdir -p "$d/commercial/commerce/cmd/commerce" "$d/commercial/commerce/migrations"
	cp "$RAIZ/commercial/commerce/migrations/"*.up.sql "$d/commercial/commerce/migrations/"
	cp "$RAIZ/commercial/commerce/cmd/commerce/main.go" "$d/commercial/commerce/cmd/commerce/main.go"
	printf '%s' "$d"
}

# 1 · CONTROL NEGATIVO — el árbol real, sin mutar
rc=0; bash "$GUION" "$RAIZ" >/dev/null 2>&1 || rc=$?
check "the real tree is CLEAN (negative control)" 0 "$rc"

# 2 · una FK en las migraciones que la lista no declara
d="$(arbol)"
cat > "$d/commercial/commerce/migrations/900_mutante.up.sql" <<'SQL'
CREATE TABLE commerce.mutante (
    id        TEXT PRIMARY KEY,
    entity_id TEXT NOT NULL REFERENCES commerce.legal_entities(entity_id)
);
SQL
rc=0; output="$(bash "$GUION" "$d" 2>&1)" || rc=$?
check "an undeclared NEW FK -> finding" 1 "$rc"
case "$output" in *mutante_entity_id_fkey*) check "and NAMES the row to add" 0 0 ;;
                  *) check "and NAMES the row to add" 0 1 ;; esac
rm -rf "$d"

# 3 · una entrada declarada que ninguna migración crea
d="$(arbol)"
python3 - "$d/commercial/commerce/cmd/commerce/main.go" <<'PY'
import sys,re
p=sys.argv[1]; s=open(p).read()
m=re.search(r"([^\S\n]*)\('grant_commands_entity_id_fkey'[^\n]*\n",s)
s=s.replace(m.group(0), m.group(0).rstrip('\n').rstrip()+",\n"+m.group(1)+"('fantasma_entity_id_fkey', 'fantasma', 'entity_id')\n")
open(p,'w').write(s)
PY
rc=0; output="$(bash "$GUION" "$d" 2>&1)" || rc=$?
check "a declared PHANTOM entry -> finding" 1 "$rc"
case "$output" in *fantasma*) check "and NAMES the extra row" 0 0 ;;
                  *) check "and NAMES the extra row" 0 1 ;; esac
rm -rf "$d"

# 4 · NO PUDE MIRAR: sin main.go
d="$(arbol)"; rm -f "$d/commercial/commerce/cmd/commerce/main.go"
rc=0; bash "$GUION" "$d" >/dev/null 2>&1 || rc=$?
check "missing main.go -> COULD NOT LOOK" 2 "$rc"
rm -rf "$d"

# 5 · NO PUDE MIRAR: sin migraciones
d="$(arbol)"; rm -f "$d/commercial/commerce/migrations/"*.up.sql
rc=0; bash "$GUION" "$d" >/dev/null 2>&1 || rc=$?
check "missing migrations -> COULD NOT LOOK" 2 "$rc"
rm -rf "$d"

# 6 · NO PUDE MIRAR: una forma que el guion NO sabe leer
d="$(arbol)"
cat > "$d/commercial/commerce/migrations/901_alter.up.sql" <<'SQL'
ALTER TABLE commerce.otra
    ADD CONSTRAINT otra_entity_id_fkey FOREIGN KEY (entity_id)
    REFERENCES commerce.legal_entities(entity_id);
SQL
rc=0; bash "$GUION" "$d" >/dev/null 2>&1 || rc=$?
check "an ALTER ... ADD CONSTRAINT -> COULD NOT LOOK, does not guess" 2 "$rc"
rm -rf "$d"

# 7 · la lista ilegible tambien es 2, no 0
d="$(arbol)"
python3 - "$d/commercial/commerce/cmd/commerce/main.go" <<'PY'
import sys,re
p=sys.argv[1]; s=open(p).read()
s=s.replace('expected_entity_foreign_keys(','expected_entity_foreign_keys_RENOMBRADA(')
open(p,'w').write(s)
PY
rc=0; bash "$GUION" "$d" >/dev/null 2>&1 || rc=$?
check "unreadable list -> COULD NOT LOOK (not 'clean')" 2 "$rc"
rm -rf "$d"

echo
echo "commerce-entity-fks: $pass_count passed, $fail_count failed"
[ "$fail_count" = "0" ] || exit 1
