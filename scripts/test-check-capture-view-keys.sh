#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de `scripts/check-capture-view-keys.py`.
#
# ⛔ LO QUE DE VERDAD HAY QUE PROBAR AQUI NO ES QUE ENCUENTRE: es que NO SEÑALE DE MAS. La primera
#    version de ese gate llevaba un allowlist de nombres anidados y daba SIETE falsos positivos
#    (`body`, `status`, `json`… de un `fulfill` dentro de un closure). Un gate ruidoso se ignora, y
#    un gate ignorado es peor que no tenerlo. Por eso la mitad de los casos de abajo son de
#    NO-DISPARO, y el caso 3 es literalmente el que la version del allowlist suspendia.
set -u -o pipefail

if ! command -v python3 >/dev/null 2>&1; then
	printf 'test-check-capture-view-keys: COULD NOT CHECK: python3 is not installed\n' >&2
	exit 2
fi

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
GATE="$RAIZ/scripts/check-capture-view-keys.py"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

ok=0
fail=0
paso() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
malo() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }
OUTPUT="$T/output.txt"
rc_de() { python3 "$GATE" "$@" >"$OUTPUT" 2>&1; printf '%s' "$?"; }
rc_de_con() { local g="$1"; shift; python3 "$g" "$@" >"$OUTPUT" 2>&1; printf '%s' "$?"; }
casa() { command grep -qE "$1" "$OUTPUT"; }

# La cabecera de los fixtures. ⛔ Los cierres de abajo añaden SIEMPRE un arnes minimo que ligue una
# variable a una entrada: sin `for (const view of VIEWS)` el gate no puede leer las invocaciones y
# contesta 2 —correctamente—, asi que un fixture sin arnes no probaria lo que dice probar.
cabecera() {
	cat <<'TS'
const VIEWS: {
  id: string
  path: string
  settle?: number
  despues?: (page: import('@playwright/test').Page) => Promise<void>
}[] = [
TS
}
arnes() {
	cat <<'TS'
for (const view of VIEWS) {
  void view.id
  void view.path
  if (view.despues) void view.despues
}
TS
}

# ── 0-bis · EL BANCO SE AUDITA A SI MISMO: NINGUN MENSAJE EJECUTA NADA ─────────────────────────
# ⛔ ESTE TESTIGO EXISTIA EN `test-seed-adoption-otlp.sh` Y NO AQUI, y la asimetria no era teorica:
#    un mensaje mio de esta misma bateria llevaba `` `alfa.despuess` `` sin escapar dentro de
#    comillas dobles, o sea una SUSTITUCION DE COMANDO. La shell lo ejecutaba, salia vacio, y el
#    fallo se leia «el diagnostico nombra : manda al lector al bucle limpio» — un rojo mutilado
#    justo en el sitio donde hay que entender que paso.
#
#    Lo destapo una mutacion, no una lectura: el mutante murio y su mensaje vino sin el dato.
#    Una convencion que tiene guarda en un banco y no en su hermano es una costumbre, no una regla.
if command grep -q '[^\\]`' <(command grep -nE "^[[:space:]]*(paso|malo) \"" "$0"); then
	command grep -nE "^[[:space:]]*(paso|malo) \"" "$0" | command grep '[^\\]`' >&2
	malo "messages contain UNESCAPED backticks inside double quotes: the shell executes them"
else
	paso "no test message contains an unescaped backtick inside double quotes"
fi

# muerte_valida — rc 0 si la ULTIMA salida capturada es una muerte LEGIBLE del sujeto y no un
# reventon del mutante.
# ⛔ MEDIDO EN ESTE MISMO BANCO, no supuesto: sustituyendo el mutante de la plantilla por uno que
#    revienta con `NameError`, el caso 4-sexies decia «MUERE en el caso 4-quinquies, Y POR SU
#    NOMBRE» y la bateria quedaba en 25/0. El mutante no llego a correr y el arnes se lo apunto.
#    Los dos casos que se acreditan por AUSENCIA de mensaje (`if ! casa …`) tienen esa forma: lo
#    que buscan es que el mutante DEJE de decir algo, y un mutante muerto antes de nacer tampoco
#    lo dice. Es la clase que un lector encontro en el banco hermano hace una hora.
muerte_valida() {
	if [ ! -s "$OUTPUT" ]; then
		return 1
	fi
	if command grep -qE 'Traceback \(most recent call last\)' "$OUTPUT"; then
		return 1
	fi
	return 0
}

# exige_construido <fichero del mutante> <etiqueta> — rc 0 si el mutante EXISTE y DIFIERE del
# sujeto. Se llama ANTES de correrlo.
# ⛔ `muerte_valida` cubre el REVENTON EN EJECUCION y no la CONSTRUCCION: los constructores de este
#    banco llevan `assert mut != src`, asi que un ancla movida deja el fichero SIN ESCRIBIR y lo que
#    se ejecuta despues es un fichero que no existe. Lo que pase entonces depende de accidentes
#    —que `$SALIDA` conserve la corrida anterior, que python imprima «can't open file»— y ninguno
#    de esos es un veredicto. Se comprueba antes y se dice, en vez de deducirlo de la salida.
#
#    Y ademas se exige DIFERIR: un mutante identico al sujeto se construye sin error y no muta nada.
exige_construido() {
	if [ ! -s "$1" ]; then
		malo "COULD NOT CHECK: $2 was not built (missing or empty file): there is no artifact to judge"
		return 1
	fi
	if cmp -s "$GATE" "$1"; then
		malo "COULD NOT CHECK: $2 is IDENTICAL to the subject: no mutation occurred and a pass proves nothing"
		return 1
	fi
	return 0
}

# ── 1 · el fichero REAL del repositorio esta al dia ───────────────────────────────────────────
r="$(rc_de "$RAIZ/web/e2e/docs-captures.spec.ts")"
if [ "$r" = "0" ] && casa 'no findings: type, entries, and property accesses agree'; then
	paso "the repository spec uses no undeclared keys and reports that result"
elif [ "$r" = "0" ]; then
	malo "returned 0 without the clean-result line: silence does not prove the subject was checked"
else
	malo "repository spec returned $r: an entry key is missing from the type (run the check for details)"
fi

# ── 2 · un TYPO en la clave del gancho tiene que salir 1 ──────────────────────────────────────
# Es el caso que da sentido al gate: `despuess` no falla en ninguna parte — el arnes evalua
# `if (view.despues)`, sale falsa, no pincha nada, y la captura se guarda con la pestaña por
# defecto mientras su `id` promete el estado interno.
{
	cabecera
	cat <<'TS'
  { id: 'work-decisions', path: '/work', despuess: async (page) => { await page.click('x') } },
]
TS
	arnes
} >"$T/typo.ts"
r="$(rc_de "$T/typo.ts")"
if [ "$r" = "1" ] && casa 'despuess. in typo\.ts' && casa 'an entry uses it, but the type does not declare it'; then
	paso "a hook-name typo (despuess) returns 1 with the key, filename and reason"
elif [ "$r" = "1" ]; then
	malo "returned 1 without key+filename+reason: entry-to-type validation is unverified"
else
	malo "the typo should return 1 but returned $r: the check missed its intended defect"
fi

# ── 3 · NO-DISPARO · claves ANIDADAS dentro de un closure ─────────────────────────────────────
# ⛔ ESTE ES EL CASO QUE SUSPENDIA LA VERSION CON ALLOWLIST. `status`, `body` y `contentType` son
#    de un `fulfill` de Playwright, no de la entrada. Si el gate los señala, alguien lo apaga.
{
	cabecera
	cat <<'TS'
  {
    id: 'x',
    path: '/x',
    despues: async (page) => {
      await page.route('**/v1/x', (route) =>
        route.fulfill({ status: 200, contentType: 'application/json', body: '{}' }),
      )
      await page.getByRole('tab', { name: /^y$/i }).click({ timeout: 8_000 })
    },
  },
]
TS
	arnes
} >"$T/anidadas.ts"
r="$(rc_de "$T/anidadas.ts")"
if [ "$r" = "0" ] && casa 'no findings'; then
	paso "nested closure keys are NOT reported (depth-based, without an allowlist)"
elif [ "$r" = "0" ]; then
	malo "returned 0 without a clean-result line: the check may not have measured anything"
else
	malo "the check reported nested keys (rc $r): false positives have returned"
fi

# ── 4 · NO-DISPARO · la prosa de los comentarios no es codigo ─────────────────────────────────
# La spec real tiene comentarios enormes que citan `id:`, `path:` y nombres de campo. Un regex de
# tokens contaria la prosa del fichero que mide — defecto ya fichado en esta casa.
{
	cabecera
	cat <<'TS'
  {
    id: 'x',
    // ⛔ La prosa va DENTRO de la entrada a proposito: fuera, la profundidad ya la descarta y el
    //    caso no probaria el borrado de comentarios. La primera version la puso fuera y su
    //    mutante sobrevivio — el fixture no ejercitaba lo que decia ejercitar.
    // Aqui se explica que antes hubo un campo inventado: y otro fantasma: que se retiraron.
    /* Un bloque de varias lineas, y la siguiente EMPIEZA como si fuera una clave:
    fantasma: 'esto es prosa, no un campo'
    */
    path: '/x',
  },
]
TS
	arnes
} >"$T/prosa.ts"
r="$(rc_de "$T/prosa.ts")"
if [ "$r" = "0" ] && casa 'no findings'; then
	paso "keys mentioned in COMMENTS do not count as usage"
elif [ "$r" = "0" ]; then
	malo "returned 0 without a clean-result line: the check may not have measured anything"
else
	malo "the check counted prose (rc $r)"
fi

# ── 4-bis · EL TYPO DE INVOCACION, que es el unico que hace daño de verdad ────────────────────
# ⛔ ES EL CASO QUE EL GATE PROMETIA Y NO TENIA. La entrada esta BIEN —`despues` declarada y usada—
#    y el arnes lee `view.despuess`. No falla en ninguna parte: la condicion sale falsa, no se
#    pincha nada, y la captura se guarda con la pestaña por defecto mientras su `id` promete el
#    estado interno. La version anterior del gate salia **rc 0** aqui, diciendo «ninguna».
{
	cat <<'TS'
const VIEWS: {
  id: string
  path: string
  despues?: (page: import('@playwright/test').Page) => Promise<void>
}[] = [
  { id: 'x', path: '/x', despues: async (page) => { await page.click('t') } },
]
async function capturar(view: (typeof VIEWS)[number], page: any) {
  if (view.despuess) await view.despuess(page)
}
TS
} >"$T/typo-invocacion.ts"
r="$(rc_de "$T/typo-invocacion.ts")"
if [ "$r" = "1" ] && casa 'despuess.*typo-invocacion\.ts' && casa 'property-access typo'; then
	paso "a property-access typo returns 1 with the key AND filename"
elif [ "$r" = "1" ]; then
	malo "returned 1 without key+filename: the failure does not identify where to look"
else
	malo "the property-access typo should return 1 but returned $r: the intended defect was missed"
fi

# ── 4-ter · MUTANTE: se retira el cruce con las INVOCACIONES ──────────────────────────────────
# Es exactamente la version anterior del gate. Tiene que MORIR en el caso 4-bis, y por MENSAJE.
mI="$T/mI.py"
python3 - "$GATE" "$mI" <<'PY'
import sys
src = open(sys.argv[1]).read()
# ⛔ ESTE ANCLA LA MOVI YO, ESTA MISMA NOCHE, y el banco no se entero: la cura que hace que el
# diagnostico nombre al invocador REAL reescribio este bucle, el `assert` del constructor salto, el
# fichero no se escribio… y el caso 4-bis siguio en VERDE acreditando nada. Lo destapo
# `exige_construido`, no una lectura. Una cura desarma los mutantes que apuntan a lo que cura.
viejo = (
    "    for n in sorted(nombres):\n"
    "        for k in re.findall(rf\"\\b{re.escape(n)}\\.(\\w+)\", limpio_todo):\n"
    "            invocadas.add(k)\n"
    "            quien.setdefault(k, []).append(n)"
)
nuevo = "    pass  # MUTANTE: no se leen las invocaciones (la version que salia rc 0 ante el typo)"
mut = src.replace(viejo, nuevo)
assert mut != src, "the property-access mutant was not applied"
open(sys.argv[2], "w").write(mut)
PY
if exige_construido "$mI" "mutant without property accesses"; then
	r="$(rc_de_con "$mI" "$T/typo-invocacion.ts")"
	# ⛔ EL JUICIO VA DENTRO DEL EXITO, y esto lo caza un lector sobre la version que introdujo la
	#    guarda. Antes la rama de fallo hacia `r=99` y el bloque de juicio corria IGUAL — contra la
	#    `$SALIDA` de la corrida ANTERIOR, que sigue en disco. O sea: la guarda cortaba bien y
	#    ademas producia un SEGUNDO diagnostico falso, sobre la salida de otro caso.
	#
	#    Es literalmente el accidente que nombre al escribirla («que `$SALIDA` conserve la corrida
	#    anterior»), mordiendo por el camino que la propia guarda abre al fallar. Un control nuevo
	#    trae su propia rama de fallo, y esa rama tambien hay que escribirla.
	# ⛔ NO SE ACREDITA POR rc, Y ESTO ME MORDIO AL ESCRIBIRLO. Sin leer invocaciones el mutante SIGUE
	#    saliendo 1 — pero por la pata de «gancho muerto», que es OTRO hallazgo. Un mutante que muere en
	#    la pata anterior no acredita la que nombra: se le exige que NO diga lo del typo.
	if ! muerte_valida; then
		malo "COULD NOT CHECK: the mutant without property accesses CRASHED (Traceback): a crash proves nothing"
	elif ! casa 'property-access typo'; then
		paso "the mutant that ignores PROPERTY ACCESSES no longer reports the typo: case 4-bis covers it"
	else
		malo "the mutant without property accesses still reports the typo (rc $r): inspect the mutant"
	fi
fi

# ── 4-quater · MUTANTE: el diagnostico pierde el FICHERO ──────────────────────────────────────
# ⛔ El lector señalo que retirar el diagnostico clave+ruta dejaba el banco en 7/0: el mensaje que
#    hace util al gate no estaba protegido por nada. Este mutante se lo quita y el caso 4-bis, que
#    ahora exige el fichero en la linea, tiene que verlo.
mF="$T/mF.py"
python3 - "$GATE" "$mF" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = 'print(f"  ⛔ `{k}` in {file_name}: {porque}")'
nuevo = 'print(f"  ⛔ {porque}")'
mut = src.replace(viejo, nuevo)
assert mut != src, "the diagnostic mutant was not applied"
open(sys.argv[2], "w").write(mut)
PY
r="$(rc_de_con "$mF" "$T/typo-invocacion.ts")"
if [ "$r" = "1" ] && ! casa 'despuess.*typo-invocacion\.ts'; then
	paso "removing the FILENAME from the diagnostic is DETECTABLE: the failure no longer identifies the location"
else
	malo "the diagnostic mutant is indistinguishable: the message remains unprotected"
fi

# ── 5 · fichero ausente => 2, no 0 ────────────────────────────────────────────────────────────
r="$(rc_de "$T/no-existe.ts")"
if [ "$r" = "2" ] && casa 'COULD NOT CHECK' && casa 'No such file'; then
	paso "a missing file returns rc 2 (could not check), with the reason"
elif [ "$r" = "2" ]; then
	malo "returned 2 without explaining why the subject could not be checked"
else
	malo "a missing file should return 2 but returned $r"
fi

# ── 6 · una spec que el gate NO entiende => 2, no 0 ───────────────────────────────────────────
# ⛔ ES LA REGLA 5 DEL CANON. Si mañana la spec declara `VIEWS` de otra forma, el gate NO puede
#    decir «limpia»: no ha podido mirar. Sin este caso, un refactor apagaria el gate en verde.
printf 'const OTRA_COSA = []\n' >"$T/rara.ts"
r="$(rc_de "$T/rara.ts")"
if [ "$r" = "2" ] && casa 'COULD NOT CHECK' && casa 'cannot find the declaration'; then
	paso "an unsupported spec format returns rc 2 and names the missing declaration"
elif [ "$r" = "2" ]; then
	malo "returned 2 without naming the missing item: the failed refactor cannot be located"
else
	malo "an unrecognized spec should return 2 but returned $r"
fi

# ── 7 · MUTANTE: se quita el borrado de comentarios de BLOQUE ─────────────────────────────────
# ⛔ ATACA EL DE BLOQUE Y NO EL DE LINEA, Y LA RAZON ES UNA MEDIDA. El primer intento mutaba el
#    borrado de `//` y SOBREVIVIO — y al perseguirlo se ve por que: la clave se reconoce con el
#    ancla `^|{|,`, y en una linea de comentario `//` va delante, asi que un `//` NUNCA puede
#    producir una clave falsa. El borrado de linea es defensa redundante con ese ancla; el de
#    BLOQUE no, porque una linea DENTRO de `/* */` si puede empezar por `palabra:`. Un mutante que
#    sobrevive no siempre acusa al caso: a veces dice que la linea que ataca no sostiene nada.
m1="$T/m1.py"
python3 - "$GATE" "$m1" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '    limpio = re.sub(r"/\\*.*?\\*/", blanquea, cuerpo, flags=re.S)'
nuevo = '    limpio = cuerpo  # MUTANTE: los comentarios de BLOQUE ya no se borran'
mut = src.replace(viejo, nuevo)
assert mut != src, "mutant 1 was not applied"
open(sys.argv[2], "w").write(mut)
PY
r="$(rc_de_con "$m1" "$T/prosa.ts")"
if [ "$r" = "1" ] && casa 'fantasma'; then
	paso "the mutant that keeps BLOCK comments FAILS in case 4 for the invented key"
elif [ "$r" = "1" ]; then
	malo "the mutant returned 1 for ANOTHER reason: block-comment removal is unverified (inspect output)"
else
	malo "the block-comment mutant SURVIVED (rc $r): case 4 proves nothing"
fi

# ── 2-bis · LA PRIMERA CLAVE DE UNA ENTRADA ESCRITA EN LINEA ──────────────────────────────────
# ⛔ ESTE CASO FALTABA Y EL GATE PASABA UN TYPO REAL. El caso 2 dice ser «el que da sentido al
#    gate», pero pone su typo en TERCERA posicion, asi que medía otra cosa: la alternancia del
#    escaner consumia la `{` y la PRIMERA clave de una entrada en linea no entraba en `usadas`.
#    Medido sobre la spec real antes de curar: `{ idd: 'inventory', …}` daba «sin hallazgos» y
#    rc 0 — y `id` es la clave con la que el arnes NOMBRA el test y el fichero de la captura.
{
	cabecera
	cat <<'TS'
  { idd: 'x', path: '/x' },
]
TS
	arnes
} >"$T/primera-clave.ts"
r="$(rc_de "$T/primera-clave.ts")"
if [ "$r" = "1" ] && casa 'idd. in primera-clave\.ts' && casa 'the type does not declare it'; then
	paso "a typo in the FIRST key of an inline entry returns 1 and names it"
elif [ "$r" = "1" ]; then
	malo "returned 1 without naming the key: the failure cannot be located"
else
	malo "a typo in the first inline-entry key returned $r: the scanner missed it"
fi

# ── 2-ter · MUTANTE: se vuelve a la alternancia que consumia la llave ──────────────────────────
mP="$T/mP.py"
python3 - "$GATE" "$mP" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = '(?<=[\\{,])\\s*(\\w+)\\s*:|^\\s*(\\w+)\\s*:'
# ⛔ El grupo 2 tiene que SEGUIR EXISTIENDO: mi primera version lo suprimia y el mutante moria
# con `IndexError: no such group` — por CRASH y no por el defecto, que es la clase de mutante que
# acredita que el guion se rompe y no que la guarda funcione. `(?!)` nunca casa y conserva el grupo.
nuevo = '(?:^|[\\{,])\\s*(\\w+)\\s*:|(?!)(\\w+)'
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "the lookbehind mutant was not applied"
compile(mut, "mP", "exec")
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mP" "$T/primera-clave.ts")"
# ⛔ Se exige el DEFECTO, no un rc cualquiera: el mutante tiene que salir 0 diciendo «sin
#    hallazgos» —que es la ceguera— y NO morir con una excepcion. Un mutante que revienta acredita
#    que el guion se rompe, no que la guarda funcione.
if [ "$r" = "0" ] && casa 'no findings'; then
	paso "the mutant consuming the brace ALLOWS the typo and reports no findings: case 2-bis catches it"
elif casa 'Traceback'; then
	malo "the mutant crashed with an EXCEPTION instead of reproducing the blind spot: lookbehind is unverified"
else
	malo "the mutant did not reproduce the blind spot (rc $r): case 2-bis proves nothing"
fi

# ── 8 · EL GANCHO MUERTO, tercera direccion del cruce ─────────────────────────────────────────
# ⛔ ESTA DIRECCION NO TENIA NI FIXTURE NI MUTANTE, y el banco salia 10/0 con el bucle RETIRADO —
#    lo comprobe quitandolo antes de escribir esto. Es el caso simetrico del 4-bis: alli el arnes
#    invoca algo que ninguna entrada trae; aqui las entradas traen algo que el arnes NO invoca
#    jamas. Duele igual y en silencio: el autor cree haber pedido una espera y la captura sale sin
#    ella. `settle` esta declarada en el tipo y puesta por la entrada, y el arnes solo lee
#    id/path/despues.
{
	cabecera
	cat <<'TS'
  { id: 'x', path: '/x', settle: 900 },
]
TS
	arnes
} >"$T/gancho-muerto.ts"
r="$(rc_de "$T/gancho-muerto.ts")"
if [ "$r" = "1" ] && casa 'settle. in gancho-muerto\.ts' && casa 'unused hook'; then
	paso "a key supplied by ENTRIES but never accessed by the harness returns 1 as an unused hook"
elif [ "$r" = "1" ]; then
	malo "returned 1 without key+filename+unused hook: the third direction is unverified"
else
	malo "the unused hook should return 1 but returned $r: entries request a key nobody reads"
fi

# ── 8-bis · MUTANTE: se retira el bucle del gancho muerto ─────────────────────────────────────
# ⛔ ES EL MUTANTE QUE EL BANCO NO TENIA. Sin el, retirar esas dos lineas deja el banco en verde y
#    la tercera direccion queda de adorno. Muere en el caso 8, y se le exige que MUERA POR SU
#    NOMBRE: no basta con que cambie el rc, tiene que dejar de decir 'gancho muerto'.
mG="$T/mG.py"
python3 - "$GATE" "$mG" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = ('    for k in sorted((usadas & declaradas) - invocadas):\n'
         '        anota(k, "entries provide it, but the harness never accesses it: unused hook")')
nuevo = "    pass  # MUTANTE: el cruce del unused hook ya no se hace"
mut = src.replace(viejo, nuevo)
assert mut != src, "the unused-hook mutant was not applied"
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mG" "$T/gancho-muerto.ts")"
if [ "$r" = "0" ] && ! casa 'unused hook'; then
	paso "the mutant removing unused-hook validation FAILS in case 8 for that hook"
else
	malo "the unused-hook mutant survived (rc $r): case 8 does not verify that direction"
fi

# ── 2-quater · UNA CLAVE «DECLARADA» DENTRO DE UN COMENTARIO DEL TIPO ─────────────────────────
# ⛔ SEGUNDO FALSO NEGATIVO DEL MISMO GATE, y del mismo dia. El gate blanqueaba prosa para el
#    cuerpo del array y para el barrido de invocaciones, y el LADO DEL TIPO era el unico que corria
#    sobre el fuente en crudo — asi que una linea de comentario que empiece por `palabra:` entraba
#    en `declaradas` como si fuera una declaracion. Medido sobre la spec REAL antes de curar:
#    retirando `despues` del tipo y dejandola MENCIONADA en un `/* */`, el gate la listaba en
#    «declaradas en el tipo» y salia rc 0 «sin hallazgos». Y ese es, palabra por palabra, el estado
#    que la cabecera del gate declara como su motivo (`despues` invocada y ausente del tipo): un
#    comentario que nombra la clave que falta bastaba para apagar el hallazgo que lo justifica.
{
	cat <<'TS'
const VIEWS: {
  id: string
  path: string
  /* PENDIENTE de declararla de verdad (esto es PROSA, no una declaracion):
     despues: (page: Page) => Promise<void>
  */
}[] = [
  { id: 'x', path: '/x', despues: async () => {} },
]
TS
	arnes
	printf '  void view.despues\n}\n'
} >"$T/tipo-en-prosa.ts"
r="$(rc_de "$T/tipo-en-prosa.ts")"
if [ "$r" = "1" ] && casa 'despues. in tipo-en-prosa\.ts'; then
	paso "a key mentioned ONLY in a type comment is undeclared: returns 1 and names it"
elif [ "$r" = "1" ]; then
	malo "returned 1 without naming the key: the failure cannot be located"
else
	malo "a key mentioned only in type prose returned $r: the type is still read without stripping comments"
fi

# ── 2-quinquies · MUTANTE: el tipo vuelve a leerse del fuente en crudo ─────────────────────────
mQ="$T/mQ.py"
python3 - "$GATE" "$mQ" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = 'm = re.search(r"const VIEWS:\\s*\\{(.*?)\\}\\[\\]\\s*=", src_sin_prosa, re.S)'
nuevo = 'm = re.search(r"const VIEWS:\\s*\\{(.*?)\\}\\[\\]\\s*=", src, re.S)  # MUTANTE: tipo en crudo'
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "the raw-type mutant was not applied"
compile(mut, "mQ", "exec")  # un mutante que no compila no acredita nada
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mQ" "$T/tipo-en-prosa.ts")"
# Se exige el DEFECTO —rc 0 diciendo «sin hallazgos»— y NO una excepcion cualquiera.
if [ "$r" = "0" ] && casa 'no findings'; then
	paso "reading the raw type counts a prose key as declared and reports no findings: case 2-quater catches it"
elif casa 'Traceback'; then
	malo "the raw-type mutant crashed with an EXCEPTION instead of reproducing the blind spot"
else
	malo "the raw-type mutant did not reproduce the blind spot (rc $r): case 2-quater proves nothing"
fi

# ── 2-sexies · LA PREMISA DEL GATE SE MIDE, NO SE RECITA ──────────────────────────────────────
# ⛔ El gate imprimia como parte de su veredicto «NINGUN tsconfig incluye `e2e/`» — un hecho sobre
#    el repositorio, verificado UNA vez a mano y afirmado desde entonces en cada rojo. El dia que
#    alguien meta `e2e/` en un tsconfig —que es lo DESEABLE, y la cabecera de este mismo fichero lo
#    dice— el gate seguiria diciendo que nadie lo hace: su razon de existir se volveria falsa
#    mientras la sigue imprimiendo. Una garantia escrita donde no hay control.
razon_con() { # $1 = dir con los tsconfig (cwd del gate)
	( cd "$1" && python3 -c '
import importlib.util, sys
spec = importlib.util.spec_from_file_location("g", sys.argv[1])
g = importlib.util.module_from_spec(spec); sys.modules["g"] = g; spec.loader.exec_module(g)
print(g.razon_de_existir())
' "$GATE" )
}
mkdir -p "$T/ts-sin/web" "$T/ts-con/web" "$T/ts-vacio"
printf '{"include":["src"]}\n' >"$T/ts-sin/web/tsconfig.app.json"
printf '{"include":["src","e2e"]}\n' >"$T/ts-con/web/tsconfig.app.json"
r_sin="$(razon_con "$T/ts-sin")"
r_con="$(razon_con "$T/ts-con")"
r_vacio="$(razon_con "$T/ts-vacio")"
if ! command grep -q 'checked now' <<<"$r_sin"; then
	malo "with a tsconfig excluding e2e, the check does not report measuring it: $r_sin"
elif ! command grep -q 'A tsconfig NAMES' <<<"$r_con"; then
	malo "with a tsconfig including e2e, the check still claims none includes it: $r_con"
elif ! command grep -q 'could not check' <<<"$r_vacio"; then
	malo "without any tsconfig, the check makes an unverified assertion: $r_vacio"
else
	paso "the check measures its premise: asserts it, retracts it if a tsconfig includes e2e, and makes no claim if unreadable"
fi

# ── 2-septies · EL UNIVERSO SE DERIVA, NO SE ESCRIBE A MANO ───────────────────────────────────
# ⛔ El gate fijaba su universo a UN fichero (`RUTA`) y salia verde. Medido en el arbol real:
#    `web/e2e/` tiene DOS tablas `VIEWS`, y la de `management-views.spec.ts` no la miraba NADIE —
#    ademas sin tipo declarado, o sea con la puerta abierta de par en par. Un universo escrito a
#    mano no da un falso verde ruidoso: da uno SILENCIOSO, porque lo que no enumera no existe.
mkdir -p "$T/uni/web/e2e"
cat >"$T/uni/web/e2e/a-limpia.spec.ts" <<'TS'
const VIEWS: {
  id: string
  path: string
}[] = [{ id: 'x', path: '/x' }]
for (const view of VIEWS) { await page.goto(view.path); use(view.id) }
TS
cat >"$T/uni/web/e2e/z-sucia.spec.ts" <<'TS'
const VIEWS: {
  id: string
  path: string
}[] = [{ id: 'y', path: '/y', despues: 1 }]
for (const view of VIEWS) { await page.goto(view.path); use(view.id) }
TS
output="$( cd "$T/uni" && python3 "$GATE" 2>&1 )"; rc=$?
if [ "$rc" != 1 ]; then
	malo "with TWO VIEWS tables and a defect in the second, the check returns rc=$rc instead of 1"
elif ! command grep -q 'z-sucia' <<<"$output"; then
	malo "the check does not name the finding’s filename: $output"
elif ! command grep -q 'a-limpia' <<<"$output"; then
	malo "the check does not report inspecting the clean file TOO: its coverage is unknown"
else
	paso "the scope is derived: both VIEWS tables are inspected and the second file’s defect is caught"
fi

# Y la otra direccion: sin ninguna tabla NO dice «limpio», dice que no pudo mirar.
mkdir -p "$T/uni-vacio/web/e2e"
output="$( cd "$T/uni-vacio" && python3 "$GATE" 2>&1 )"; rc=$?
if [ "$rc" != 2 ]; then
	malo "without any VIEWS tables the check returns rc=$rc: an empty scope is not a clean tree"
else
	paso "an empty scope returns rc 2 (could not check)"
fi

# ── 4-quinquies · UNA INVOCACION DENTRO DE UNA PLANTILLA SE VE ────────────────────────────────
# ⛔ `sin_prosa` blanqueaba la plantilla ENTERA, `${...}` incluido, que es CODIGO. El daño era un
#    falso negativo del caso INSIGNIA de este gate: un typo de invocacion escrito dentro de una
#    plantilla salia **rc 0 «sin hallazgos»**. Y dejaba huella: la exencion a mano `{"id","path"}`
#    existia para callar el falso positivo gemelo de dos claves invocadas asi.
{
	cat <<'TS'
const VIEWS: {
  id: string
  path: string
}[] = [
  { id: 'x', path: '/x' },
]
TS
	printf 'for (const view of VIEWS) {\n  await page.goto(view.path)\n'
	# La invocacion mala va DENTRO de una plantilla, que es justo donde el gate era ciego.
	printf '  await page.screenshot({ path: `informe/%s-${view.despuess}.png` })\n' 'x'
	printf '  use(view.id)\n}\n'
} >"$T/typo-en-plantilla.ts"
r="$(rc_de "$T/typo-en-plantilla.ts")"
if [ "$r" = "1" ] && casa 'property-access typo' && casa 'despuess'; then
	paso "a property-access typo INSIDE a template returns 1 and names it"
elif [ "$r" = "0" ]; then
	malo "the template typo returns rc 0: the probe again ignores code inside \${}"
else
	malo "the template typo returns rc $r without naming it: $(cat "$T/output" 2>/dev/null | head -3)"
fi

# ── 4-sexies · MUTANTE: la plantilla vuelve a blanquearse entera ──────────────────────────────
# Se le exige MORIR POR SU NOMBRE: no basta con que cambie el rc, tiene que dejar de decir el typo.
# ⛔ NOMBRE PROPIO, Y NO ES ESTILO. Este fichero se llamaba `$T/mP.py`, IGUAL que el mutante de
#    «la primera clave» de mas arriba. Consecuencia medida: si el constructor de ESTE falla, la
#    guarda `exige_construido` encuentra el artefacto que dejo el OTRO caso —existe y difiere del
#    sujeto—, la da por buena, y el caso juzga UN MUTANTE QUE NO ES EL SUYO. Es decir: la guarda de
#    construccion se satisface con un artefacto rancio de otra prueba, que es peor que no tenerla.
mPlantilla="$T/mPlantilla.py"
python3 - "$GATE" "$mPlantilla" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = "    return _sin_cadenas(src)"
# El mutante blanquea la plantilla ENTERA sobre el FUENTE y luego deja correr el escaner: eso es
# exactamente el punto ciego original. Hacerlo al reves no ciega nada — cuando el escaner termina ya
# no quedan backticks que casar, y el primer intento de este mutante sobrevivio por eso.
nuevo = ('    import re as _re\n'
         '    _bl = lambda m: "".join(c if c == "\\n" else " " for c in m.group(0))\n'
         '    return _sin_cadenas(_re.sub(r"`(?:\\\\.|[^`\\\\])*`", _bl, src, flags=_re.S))  # MUTANTE')
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "the template mutant was not applied"
open(sys.argv[2], "w").write(mut)
PY2
if exige_construido "$mPlantilla" "mutant ignoring template code"; then
	r="$(rc_de_con "$mPlantilla" "$T/typo-en-plantilla.ts")"
	# ⛔ EL JUICIO VA DENTRO DEL EXITO, y esto lo caza un lector sobre la version que introdujo la
	#    guarda. Antes la rama de fallo hacia `r=99` y el bloque de juicio corria IGUAL — contra la
	#    `$SALIDA` de la corrida ANTERIOR, que sigue en disco. O sea: la guarda cortaba bien y
	#    ademas producia un SEGUNDO diagnostico falso, sobre la salida de otro caso.
	#
	#    Es literalmente el accidente que nombre al escribirla («que `$SALIDA` conserve la corrida
	#    anterior»), mordiendo por el camino que la propia guarda abre al fallar. Un control nuevo
	#    trae su propia rama de fallo, y esa rama tambien hay que escribirla.
	if ! muerte_valida; then
		malo "COULD NOT CHECK: the mutant ignoring template code CRASHED (Traceback): a crash proves nothing"
	elif ! casa 'property-access typo'; then
		paso "the mutant blanking the entire template FAILS in case 4-quinquies for the intended defect"
	else
		malo "the mutant ignoring template code still reports the typo (rc $r): inspect the mutant"
	fi
fi

# ── 5-bis · UN USO DE VIEWS QUE EL GATE NO SABE LEER NO ES «LIMPIO» ───────────────────────────
# ⛔ Ya habia guarda para «ninguna ligadura», y cubria el caso facil: una spec entera escrita de
#    otra forma. NO cubria el MIXTO, que es el de la vida real: un `for … of VIEWS` modelado
#    conviviendo con un `VIEWS.reduce((acc, v) => …)` que no lo esta. La guarda pasaba —hay
#    ligaduras— y las invocaciones del segundo eran invisibles: `v.despuess` salia **rc 0**.
{
	cat <<'TS'
const VIEWS: {
  id: string
  path: string
}[] = [
  { id: 'x', path: '/x' },
]
for (const view of VIEWS) { use(view.id, view.path) }
VIEWS.reduce((acc, v) => { use(v.despuess); return acc }, 0)
TS
} >"$T/forma-mixta.ts"
r="$(rc_de "$T/forma-mixta.ts")"
if [ "$r" = "2" ] && casa 'COULD NOT CHECK' && casa 'unsupported form'; then
	paso "an unsupported VIEWS usage returns rc 2 and names its line"
elif [ "$r" = "0" ]; then
	malo "the unsupported form returns rc 0: the check reports unreadable input as clean"
else
	malo "the unsupported form returns rc $r without explaining that it could not be checked"
fi

# ── 5-ter · MUTANTE: se retira la deteccion de usos no modelados ──────────────────────────────
mN="$T/mN.py"
python3 - "$GATE" "$mN" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = "    sueltos = usos_no_modelados(limpio_todo)"
nuevo = "    sueltos = []  # MUTANTE: la lista de formas vuelve a ser una promesa"
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "the unsupported-usage mutant was not applied"
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mN" "$T/forma-mixta.ts")"
if [ "$r" = "0" ]; then
	paso "the mutant trusting the list of forms FAILS in case 5-bis: returns rc 0 with the typo present"
else
	malo "the mutant ignoring unsupported usage returns rc $r; a false pass was expected"
fi

# ── 6-bis · EL DIAGNOSTICO NOMBRA A QUIEN INVOCA, NO AL PRIMERO POR ORDEN ──────────────────────
# ⛔ Decia `{sorted(nombres)[0]}.{k}`. Con dos ligaduras, el typo de una se anunciaba con el nombre
#    de la otra — y el bucle al que mandaba estaba LIMPIO, asi que el rojo parecia falso positivo.
{
	cat <<'TS'
const VIEWS: {
  id: string
  path: string
}[] = [
  { id: 'x', path: '/x' },
]
for (const alfa of VIEWS) { use(alfa.id, alfa.path) }
VIEWS.map((zeta) => use(zeta.despuess))
TS
} >"$T/dos-ligaduras.ts"
r="$(rc_de "$T/dos-ligaduras.ts")"
if [ "$r" = "1" ] && casa 'zeta\.despuess' && ! casa 'alfa\.despuess'; then
	paso "with two bindings the diagnostic names the actual accessor (zeta) instead of the first alphabetic name"
elif casa 'alfa\.despuess'; then
	malo "the diagnostic names \`alfa.despuess\`: directs the reader to the clean loop"
else
	malo "the two-binding case returns rc $r without naming the property access"
fi

printf '\ntest-check-capture-view-keys: %d passed, %d failed\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
