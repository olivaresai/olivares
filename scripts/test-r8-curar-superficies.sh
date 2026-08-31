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

pasan=0
fallan=0
ok() { pasan=$((pasan + 1)); printf 'ok   %-52s %s\n' "$1" "${2:-}"; }
malo() {
	fallan=$((fallan + 1))
	printf 'FALLO %-51s %s\n' "$1" "${2:-}"
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
[ "$rc" -eq 1 ] && ok "con hallazgos => rc 1" "hay un hueco y una vacia" || malo "con hallazgos salió rc=$rc"
grep -q 'piden adjudicacion' "$WORK/o1.txt" && ok "y dice cuántas" || malo "no resume los hallazgos"

# rc 0: un manifiesto SIN nada que adjudicar
man limpio.json '[
 {"id":"members","theme":"light","filas":7,"tablas_vacias":0,"texto_main":900,"sha256":"aaa"},
 {"id":"form","theme":"light","filas":0,"tablas_vacias":0,"texto_main":1672,"sha256":"bbb"}
]'
bash "$GUION" "$WORK/limpio.json" >"$WORK/o0.txt" 2>&1
rc0=$?
[ "$rc0" -eq 0 ] && ok "sin hallazgos => rc 0" "rc=0" || malo "sin hallazgos salió rc=$rc0"
grep -q 'ninguna superficie pide adjudicacion' "$WORK/o0.txt" && ok "y lo dice" || malo "no lo dice"

grep -qE '^  members +CON DATOS' "$WORK/o1.txt" && ok "con filas => CON DATOS" || malo "members mal clasificada"
grep -qE '^  agents +HUECO DE SEMBRADO' "$WORK/o1.txt" && ok "tabla con cabeceras y 0 filas => HUECO" || malo "agents mal clasificada"

# ⛔ EL CASO QUE JUSTIFICA EL CURADOR: un formulario con 1672 caracteres y CERO tablas no es un
#    hueco de sembrado. Si esto se rompe, el guion ha vuelto al error del booleano.
grep -qE '^  inference-proxy +SIN TABLA' "$WORK/o1.txt" && ok "formulario sin tablas => SIN TABLA" || malo "⛔ inference-proxy leída como hueco"
grep -qE '^  rota +SIN TABLA NI TEXTO' "$WORK/o1.txt" && ok "sin tablas y sin texto => SIN TABLA NI TEXTO" || malo "rota mal clasificada"
grep -q 'backend (sembrado por ruta)' "$WORK/o1.txt" && ok "el hueco lleva dueño" || malo "el hueco sale sin dueño"

# ⛔⛔ EL CUARTO VEREDICTO NO PUEDE AFIRMAR QUE LA PANTALLA ESTA VACIA. Se llamaba «PANTALLA
#     VACIA» y era una afirmacion que la medida no sostiene: aqui solo se cuentan FILAS DE TABLA,
#     asi que una lista de TARJETAS cae en el mismo cubo. Medido abriendo las seis imagenes que
#     marco: CINCO estaban bien (`tenants`, `settings`, `status-page`, `login`, `setup`). Si
#     alguien lo renombra de vuelta, esto tiene que ponerse rojo.
grep -q 'SIN TABLA NI TEXTO' "$WORK/o1.txt" && ok "el 4.º veredicto describe lo MEDIDO" || malo "⛔ vuelve a afirmar «vacía»"
grep -qE '^  rota .*MIRA EL PNG' "$WORK/o1.txt" && ok "y manda mirar la imagen" "no concluye por su cuenta" || malo "no pide la comprobación visual"
# ⛔ INSENSIBLE A CAJA: la asercion anterior solo cazaba MAYUSCULAS y el resumen operativo
#    conservaba la inferencia en minuscula («pantallas vacias») — paso 30/30 con la frase viva.
#    Una sonda que solo mira una caja deja media superficie sin vigilar.
grep -qi 'pantallas *vacias' "$WORK/o1.txt" && malo "⛔ la inferencia «pantallas vacías» sigue en la salida" || ok "ni en minúscula queda la inferencia" "resumen incluido"
grep -q 'PANTALLA VACIA' "$WORK/o1.txt" && malo "⛔ el veredicto que afirma vacío sigue vivo" || ok "y la afirmación vieja no aparece"

# ⛔ SOLO EL TEMA CLARO: `members` aparece en claro y oscuro y debe contarse UNA vez, o "23
#    superficies" y "46 tomas" acabarian usandose como sinonimos.
grep -qE '^superficies \(tema claro\): 4$' "$WORK/o1.txt" && ok "cuenta superficies, no tomas" "4 de 5 tomas" || malo "contó las dos temáticas"

# Sin manifiesto anterior NO dice "0 cambiadas": dice que no lo miró. "No pude mirar" no es "limpio".
grep -q 'no se mide rancidez' "$WORK/o1.txt" && ok "sin anterior => lo DICE, no dictamina" || malo "se calló la falta de comparación"

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
grep -qE '^  al-filo .*AL FILO' "$WORK/o3.txt" && ok "395 con umbral 400 => marcado al filo" || malo "el caso al filo no se marcó"
grep -qE '^  lejos .*AL FILO' "$WORK/o3.txt" && malo "marcó como al filo uno que está lejos" || ok "11 caracteres => NO marcado"
grep -qE '^  con-tabla .*AL FILO' "$WORK/o3.txt" && malo "marcó al filo uno que TIENE tablas" || ok "con tablas el umbral no decide => no marcado"
grep -q '1 veredicto(s) AL FILO' "$WORK/o3.txt" && ok "y lo resume al final" || malo "no resume los del filo"

# ⛔⛔ Y SU rc, QUE ES LO QUE FALTABA: el contrato dice que las AL FILO no son hallazgos —si lo
#     fueran, mover el umbral cambiaria el rc sin que el arbol cambiase—, y el codigo las contaba
#     igual. La bateria daba 24/24 porque NO asertaba este rc: el mutante que yo mismo habia
#     declarado ERA el codigo. Un caso sin asercion de rc no cubre el contrato de rc.
cat >"$WORK/solo-filo.json" <<'FIN'
{"take":[{"id":"al-filo","theme":"light","filas":0,"tablas_vacias":0,"texto_main":395,"sha256":"f1"}]}
FIN
bash "$GUION" "$WORK/solo-filo.json" >"$WORK/of.txt" 2>&1
rc_filo="$?"
[ "$rc_filo" = "0" ] && ok "SOLO una AL FILO => rc 0" "no es un hallazgo" || malo "⛔ una AL FILO sola dio rc=$rc_filo"
grep -q 'AL FILO' "$WORK/of.txt" && ok "y aun asi sale listada" "se ve, no cuenta" || malo "la escondió"

# el mutante: si se contara, el rc seria 1 — y este caso lo mata.
grep -qE 'AL FILO" not in r\[5\]' "$GUION" && ok "el rc excluye AL FILO en el codigo" || malo "la exclusión no está en el código"

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
grep -qE 'paneles vacios: 3 superficie' "$WORK/o4.txt" && ok "cuenta las superficies con panel vacío" || malo "no contó los paneles"
grep -qE 'hueco de sembrado +1 +con-tabla' "$WORK/o4.txt" && ok "panel + tabla sin filas => hueco" || malo "clase 1 mal"
grep -qE 'panel sin tabla +1 +sin-tabla' "$WORK/o4.txt" && ok "panel sin tabla => clase propia" || malo "clase 2 mal"
grep -qE 'superficie CON datos +1 +con-datos' "$WORK/o4.txt" && ok "panel vacío CON filas => incompleta" || malo "⛔ clase 3 perdida: se leería como vacía"
grep -q 'NO son pantallas vacias' "$WORK/o4.txt" && ok "y lo dice explícitamente" || malo "no avisa de la clase 3"

# sin empty_panels no inventa la sección
grep -q 'paneles vacios' "$WORK/o1.txt" && malo "inventó la sección sin datos" || ok "sin empty_panels no imprime nada" "no rellena"

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
grep -qE '^  tenants +SIN TABLA NI TEXTO' "$WORK/ot.txt" && ok "una lista de TARJETAS => sin afirmar" "no dice «vacía»" || malo "⛔ concluye sobre una pantalla que no sabe leer"
grep -q 'MIRA EL PNG' "$WORK/ot.txt" && ok "y manda a la imagen" || malo "no manda mirar"
# ⛔⛔ ESTA ASERCION NO COMPROBABA NADA, y la escribi yo: `grep -q` NO EMITE SALIDA, asi que el
#     segundo `grep` de la tuberia recibia cero bytes, devolvia 1, y el `&&` no disparaba jamas.
#     Prometia un negativo —«la palabra vacía sólo aparece para negarla»— y lo concedia siempre.
#     Lo destapo el contraste inyectando la frase: seguia en 34/34. Un caso que no puede fallar
#     no es un caso.
if grep -i 'vacia' "$WORK/ot.txt" | grep -qiv 'NO significa vacia'; then
	malo "⛔ «vacía» aparece en una línea que NO es la que lo niega"
else
	ok "«vacía» sólo aparece para NEGARLA" "y la sonda lo comprueba de verdad"
fi

# ── rancidez: el sha manda, no la fecha ──────────────────────────────────────────────────────
man viejo.json '[
 {"id":"members","theme":"light","filas":7,"tablas_vacias":0,"texto_main":900,"sha256":"aaa"},
 {"id":"agents","theme":"light","filas":0,"tablas_vacias":2,"texto_main":600,"sha256":"OTRO"},
 {"id":"retirada","theme":"light","filas":1,"tablas_vacias":0,"texto_main":50,"sha256":"eee"}
]'
bash "$GUION" "$WORK/nuevo.json" "$WORK/viejo.json" >"$WORK/o2.txt" 2>&1
grep -qE 'cambiadas 1 · iguales 1 · nuevas 3 · desaparecidas 1' "$WORK/o2.txt" &&
	ok "rancidez por sha256" "1 cambiada, 1 igual, 3 nuevas, 1 ida" || malo "el recuento de rancidez no cuadra"
grep -q 'desaparecidas  retirada' "$WORK/o2.txt" && ok "nombra la desaparecida" || malo "no nombra la desaparecida"

# entrada inservible: 2, no un veredicto vacío
bash "$GUION" "$WORK/no-existe.json" >/dev/null 2>&1
[ "$?" -eq 2 ] && ok "manifiesto ausente => rc=2" || malo "no distingue 'no puedo mirar'"

echo
echo "test-r8-curar-superficies: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ]
