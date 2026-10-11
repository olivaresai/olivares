#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-r8-curar-superficies.sh — la batería del curador.
#
# ⛔ LA ASERCIÓN QUE JUSTIFICA ESTA BATERÍA ES QUE `filas: 0` NO ES UN VEREDICTO. El curador existe
#    porque cero filas significa DOS cosas —«la tabla está vacía» y «no hay tabla»— y confundirlas
#    convertiría formularios legítimos en huecos de sembrado. Los dos casos van con fixture propio.
set -u

AQUI="$(cd "$(dirname "$0")" && pwd)"
GUION="$AQUI/r8-curar-superficies.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass_count=0
fail_count=0
ok() { pass_count=$((pass_count + 1)); printf 'ok   %-52s %s\n' "$1" "${2:-}"; }
malo() {
	fail_count=$((fail_count + 1))
	printf 'FAIL %-51s %s\n' "$1" "${2:-}"
}

man() { # <fichero> <json de takes>
	printf '{"take": %s}\n' "$2" >"$WORK/$1"
}

echo "== r8-curar-superficies =="

# Cuatro superficies, una por veredicto, y las cifras salen del censo real del arnés.
#
# ⛔ EL FIXTURE DEL FORMULARIO ES `inference-proxy` Y NO `killswitch`, Y LA RAZÓN IMPORTA. La spec
#    del arnés cita LAS DOS como «pantallas de formulario llenas de conmutadores y sin una sola
#    tabla». Contra el manifiesto publicado (124 tomas, commit 2f60830ed) eso es cierto de
#    `inference-proxy` —`tablas_vacias: 0`, 2274 caracteres— y **FALSO de `killswitch`**, que da
#    `tablas_vacias: 3`. O sea que de los dos ejemplos que la spec usa para sostener su tesis, uno
#    la refuta. Este fixture llevaba el nombre del refutado, copiado de la prosa sin comprobarlo:
#    un testigo que hereda la afirmación que debía verificar da verde con la afirmación mal.
man nuevo.json '[
 {"id":"members","theme":"light","filas":7,"tablas_vacias":0,"texto_main":900,"sha256":"aaa"},
 {"id":"agents","theme":"light","filas":0,"tablas_vacias":2,"texto_main":600,"sha256":"bbb"},
 {"id":"inference-proxy","theme":"light","filas":0,"tablas_vacias":0,"texto_main":2274,"sha256":"ccc"},
 {"id":"rota","theme":"light","filas":0,"tablas_vacias":0,"texto_main":11,"sha256":"ddd"},
 {"id":"members","theme":"dark","filas":7,"tablas_vacias":0,"texto_main":900,"sha256":"zzz"}
]'

# ⛔ CONTRATO DE SALIDA, en sus tres estados. Este guion salia 0 SIEMPRE, incluso listando nueve
#    huecos: un hallazgo que sale 0 no es un hallazgo, y quien lo llame desde otro guion no puede
#    distinguir «no hay nada» de «hay nueve». Lo midio the reviewer.
bash "$GUION" "$WORK/nuevo.json" >"$WORK/o1.txt" 2>&1
rc=$?
[ "$rc" -eq 1 ] && ok "findings => rc 1" "one gap and one empty surface" || malo "findings returned rc=$rc"
grep -q 'require assessment' "$WORK/o1.txt" && ok "and reports how many" || malo "does not summarize findings"

# rc 0: un manifiesto SIN nada que adjudicar
man limpio.json '[
 {"id":"members","theme":"light","filas":7,"tablas_vacias":0,"texto_main":900,"sha256":"aaa"},
 {"id":"form","theme":"light","filas":0,"tablas_vacias":0,"texto_main":1672,"sha256":"bbb"}
]'
bash "$GUION" "$WORK/limpio.json" >"$WORK/o0.txt" 2>&1
rc0=$?
[ "$rc0" -eq 0 ] && ok "no findings => rc 0" "rc=0" || malo "no findings returned rc=$rc0"
grep -q 'no surface requires assessment' "$WORK/o0.txt" && ok "and reports it" || malo "does not report it"

grep -qE '^  members +HAS DATA' "$WORK/o1.txt" && ok "with rows => HAS DATA" || malo "members misclassified"
grep -qE '^  agents +SEEDING GAP' "$WORK/o1.txt" && ok "table with headers and 0 rows => GAP" || malo "agents misclassified"

# ⛔ EL CASO QUE JUSTIFICA EL CURADOR: un formulario con 1672 caracteres y CERO tablas no es un
#    hueco de sembrado. Si esto se rompe, el guion ha vuelto al error del booleano.
grep -qE '^  inference-proxy +NO TABLE' "$WORK/o1.txt" && ok "form without tables => NO TABLE" || malo "⛔ inference-proxy interpreted as a gap"
grep -qE '^  rota +NO TABLE OR TEXT' "$WORK/o1.txt" && ok "no tables or text => NO TABLE OR TEXT" || malo "broken surface misclassified"
grep -q 'backend (seeded per route)' "$WORK/o1.txt" && ok "the gap has an owner" || malo "the gap has no owner"

# ⛔⛔ EL CUARTO VEREDICTO NO PUEDE AFIRMAR QUE LA PANTALLA ESTA VACIA. Se llamaba «PANTALLA
#     VACIA» y era una afirmacion que la medida no sostiene: aqui solo se cuentan FILAS DE TABLA,
#     asi que una lista de TARJETAS cae en el mismo cubo. Medido abriendo las seis imagenes que
#     marco: CINCO estaban bien (`tenants`, `settings`, `status-page`, `login`, `setup`). Si
#     alguien lo renombra de vuelta, esto tiene que ponerse rojo.
grep -q 'NO TABLE OR TEXT' "$WORK/o1.txt" && ok "the fourth verdict describes what was MEASURED" || malo "⛔ claims «empty» again"
grep -qE '^  rota .*CHECK THE PNG' "$WORK/o1.txt" && ok "and asks to inspect the image" "does not draw its own conclusion" || malo "does not request visual verification"
# ⛔ INSENSIBLE A CAJA: la asercion anterior solo cazaba MAYUSCULAS y el resumen operativo
#    conservaba la inferencia en minuscula («pantallas vacias») — paso 30/30 con la frase viva.
#    Una sonda que solo mira una caja deja media superficie sin vigilar.
grep -qi 'pantallas *vacias' "$WORK/o1.txt" && malo "⛔ the «empty screens» inference remains in the output" || ok "the lowercase inference is also absent" "including the summary"
grep -q 'PANTALLA VACIA' "$WORK/o1.txt" && malo "⛔ the verdict claiming emptiness remains" || ok "and the old claim is absent"

# ⛔ SOLO EL TEMA CLARO: `members` aparece en claro y oscuro y debe contarse UNA vez, o "23
#    superficies" y "46 tomas" acabarian usandose como sinonimos.
grep -qE '^surfaces \(light theme\): 4$' "$WORK/o1.txt" && ok "counts surfaces, not shots" "4 of 5 shots" || malo "counted both themes"

# Sin manifiesto anterior NO dice "0 cambiadas": dice que no lo miró. "No pude mirar" no es "limpio".
grep -q 'staleness is unmeasured' "$WORK/o1.txt" && ok "no previous snapshot => REPORTS it without a verdict" || malo "did not report the missing comparison"

# ⛔ EL FILO DEL UMBRAL, en las dos direcciones. `rota` (11 caracteres) esta lejos y NO debe
#    marcarse; `work-decisions` dio 395 con el umbral en 400 en la primera corrida real —cinco
#    caracteres entre «hueco» y «formulario»— y SI debe marcarse. Un veredicto que depende de cinco
#    caracteres no es falso, pero presentarlo como firme si lo es.
man filo.json '[
 {"id":"al-filo","theme":"light","filas":0,"tablas_vacias":0,"texto_main":395,"sha256":"f1"},
 {"id":"lejos","theme":"light","filas":0,"tablas_vacias":0,"texto_main":11,"sha256":"f2"},
 {"id":"con-tabla","theme":"light","filas":0,"tablas_vacias":3,"texto_main":405,"sha256":"f3"}
]'
bash "$GUION" "$WORK/filo.json" >"$WORK/o3.txt" 2>&1
grep -qE '^  al-filo .*BORDERLINE' "$WORK/o3.txt" && ok "395 with threshold 400 => marked near the threshold" || malo "the near-threshold case was not marked"
grep -qE '^  lejos .*BORDERLINE' "$WORK/o3.txt" && malo "marked a distant value as near the threshold" || ok "11 characters => NOT marked"
grep -qE '^  con-tabla .*BORDERLINE' "$WORK/o3.txt" && malo "marked a surface WITH tables as near the threshold" || ok "with tables, the threshold does not decide => not marked"
grep -q '1 verdict(s) BORDERLINE' "$WORK/o3.txt" && ok "and summarizes it at the end" || malo "does not summarize near-threshold cases"

# ⛔⛔ Y SU rc, QUE ES LO QUE FALTABA: el contrato dice que las AL FILO no son hallazgos —si lo
#     fueran, mover el umbral cambiaria el rc sin que el arbol cambiase—, y el codigo las contaba
#     igual. La bateria daba 24/24 porque NO asertaba este rc: el mutante que yo mismo habia
#     declarado ERA el codigo. Un caso sin asercion de rc no cubre el contrato de rc.
cat >"$WORK/solo-filo.json" <<'FIN'
{"take":[{"id":"al-filo","theme":"light","filas":0,"tablas_vacias":0,"texto_main":395,"sha256":"f1"}]}
FIN
bash "$GUION" "$WORK/solo-filo.json" >"$WORK/of.txt" 2>&1
rc_filo="$?"
[ "$rc_filo" = "0" ] && ok "ONLY one NEAR THRESHOLD => rc 0" "is not a finding" || malo "⛔ a single NEAR THRESHOLD case returned rc=$rc_filo"
grep -q 'BORDERLINE' "$WORK/of.txt" && ok "and is still listed" "visible, but not counted" || malo "hid it"

# el mutante: si se contara, el rc seria 1 — y este caso lo mata.
grep -qE 'AL FILO" not in r\[5\]' "$GUION" && ok "the code excludes NEAR THRESHOLD from the exit code" || malo "the exclusion is absent from the code"

# ⛔ `empty_panels` SON TRES CLASES, NO UNA LISTA DE HUECOS. La tercera —panel vacío en una
#    superficie que SÍ trae filas— es la que ninguna búsqueda de «pantallas vacías» encuentra,
#    porque esa superficie no está vacía: está INCOMPLETA. Si esta batería se rompe, hemos vuelto
#    a leer las 22 como si fueran lo mismo.
cat >"$WORK/paneles.json" <<'FIN'
{"empty_panels": [
  {"id":"con-tabla","paneles_vacios":1,"texto_main":598},
  {"id":"sin-tabla","paneles_vacios":1,"texto_main":444},
  {"id":"con-datos","paneles_vacios":2,"texto_main":3334}
], "take": [
  {"id":"con-tabla","theme":"light","filas":0,"tablas_vacias":1,"texto_main":598,"sha256":"p1"},
  {"id":"sin-tabla","theme":"light","filas":0,"tablas_vacias":0,"texto_main":444,"sha256":"p2"},
  {"id":"con-datos","theme":"light","filas":1,"tablas_vacias":0,"texto_main":3334,"sha256":"p3"}
]}
FIN
bash "$GUION" "$WORK/paneles.json" >"$WORK/o4.txt" 2>&1
grep -qE 'empty panels: 3 surface' "$WORK/o4.txt" && ok "counts surfaces with an empty panel" || malo "did not count panels"
grep -qE 'seeding gap +1 +con-tabla' "$WORK/o4.txt" && ok "panel + table without rows => gap" || malo "class 1 incorrect"
grep -qE 'panel without a table +1 +sin-tabla' "$WORK/o4.txt" && ok "panel without a table => separate class" || malo "class 2 incorrect"
grep -qE 'surface WITH data +1 +con-datos' "$WORK/o4.txt" && ok "empty panel WITH rows => incomplete" || malo "⛔ class 3 lost: would be interpreted as empty"
grep -q 'does NOT contain empty screens' "$WORK/o4.txt" && ok "and reports it explicitly" || malo "does not report class 3"

# sin empty_panels no inventa la sección
grep -q 'empty panels' "$WORK/o1.txt" && malo "invented the section without data" || ok "without empty_panels, prints nothing" "does not fill in data"

# 11 · ⛔ EL CASO QUE EL GUION NO SABE VER, y por eso su veredicto no puede afirmar. Una pantalla
#      de TARJETAS o de definiciones no tiene `tbody tr`, asi que da `filas: 0` aunque este llena.
#      Se modela con el dato REAL de `tenants` en la corrida del 2026-08-30 —filas 0,
#      tablas_vacias 0, texto_main 145— y la imagen mostraba su organizacion con sus acciones.
#      La bateria NO puede exigir que el guion acierte aqui (no tiene con que); lo que exige es
#      que NO afirme: que salga «SIN TABLA NI TEXTO» y mande mirar el PNG.
man tarjetas.json '[
 {"id":"tenants","theme":"light","filas":0,"tablas_vacias":0,"texto_main":145,"sha256":"t1"}
]'
bash "$GUION" "$WORK/tarjetas.json" >"$WORK/ot.txt" 2>&1
grep -qE '^  tenants +NO TABLE OR TEXT' "$WORK/ot.txt" && ok "a list of CARDS => no claim" "does not claim «empty»" || malo "⛔ draws a conclusion about a screen it cannot read"
grep -q 'Check the PNG' "$WORK/ot.txt" && ok "and refers to the image" || malo "does not ask to inspect it"
# ⛔⛔ ESTA ASERCION NO COMPROBABA NADA, y la escribi yo: `grep -q` NO EMITE SALIDA, asi que el
#     segundo `grep` de la tuberia recibia cero bytes, devolvia 1, y el `&&` no disparaba jamas.
#     Prometia un negativo —«la palabra vacía sólo aparece para negarla»— y lo concedia siempre.
#     Lo destapo el contraste inyectando la frase: seguia en 34/34. Un caso que no puede fallar
#     no es un caso.
if grep -i 'empty' "$WORK/ot.txt" | grep -vE '^  surface +verdict +rows +empty\.t +text +owner$' | grep -qiv 'does NOT mean empty'; then
	malo "⛔ «empty» appears in a line that does NOT deny it"
else
	ok "«empty» appears only to DENY it" "and the probe actually verifies it"
fi

# ── rancidez: el sha manda, no la fecha ──────────────────────────────────────────────────────
man viejo.json '[
 {"id":"members","theme":"light","filas":7,"tablas_vacias":0,"texto_main":900,"sha256":"aaa"},
 {"id":"agents","theme":"light","filas":0,"tablas_vacias":2,"texto_main":600,"sha256":"OTRO"},
 {"id":"retirada","theme":"light","filas":1,"tablas_vacias":0,"texto_main":50,"sha256":"eee"}
]'
bash "$GUION" "$WORK/nuevo.json" "$WORK/viejo.json" >"$WORK/o2.txt" 2>&1
grep -qE 'changed 1 · unchanged 1 · new 3 · removed 1' "$WORK/o2.txt" &&
	ok "staleness by sha256" "1 changed, 1 unchanged, 3 new, 1 removed" || malo "the staleness count does not match"
grep -qE 'removed +retirada' "$WORK/o2.txt" && ok "names the removed surface" || malo "does not name the removed surface"

# entrada inservible: 2, no un veredicto vacío
bash "$GUION" "$WORK/no-existe.json" >/dev/null 2>&1
[ "$?" -eq 2 ] && ok "missing manifest => rc=2" || malo "does not distinguish 'cannot inspect'"

echo
echo "test-r8-curar-superficies: $pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
