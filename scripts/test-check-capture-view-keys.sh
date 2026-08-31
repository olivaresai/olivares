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
	printf 'test-check-capture-view-keys: NO HE PODIDO MIRAR: no hay python3\n' >&2
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
SALIDA="$T/salida.txt"
rc_de() { python3 "$GATE" "$@" >"$SALIDA" 2>&1; printf '%s' "$?"; }
rc_de_con() { local g="$1"; shift; python3 "$g" "$@" >"$SALIDA" 2>&1; printf '%s' "$?"; }
casa() { command grep -qE "$1" "$SALIDA"; }

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
	malo "hay mensajes con backticks SIN escapar dentro de comillas dobles: la shell los ejecuta"
else
	paso "ningun mensaje del banco lleva un backtick sin escapar dentro de comillas dobles"
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
	if [ ! -s "$SALIDA" ]; then
		return 1
	fi
	if command grep -qE 'Traceback \(most recent call last\)' "$SALIDA"; then
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
		malo "NO HE PODIDO MIRAR: $2 no se construyo (fichero ausente o vacio): sin artefacto no hay juicio"
		return 1
	fi
	if cmp -s "$GATE" "$1"; then
		malo "NO HE PODIDO MIRAR: $2 es IDENTICO al sujeto: no muta nada y su verde no acredita nada"
		return 1
	fi
	return 0
}

# ── 1 · el fichero REAL del repositorio esta al dia ───────────────────────────────────────────
r="$(rc_de "$RAIZ/web/e2e/docs-captures.spec.ts")"
if [ "$r" = "0" ] && casa 'sin hallazgos: tipo, entradas e invocaciones concuerdan'; then
	paso "la spec real del repositorio no usa ninguna clave sin declarar, y lo DICE"
elif [ "$r" = "0" ]; then
	malo "salio 0 sin la linea de limpio: un cero mudo no distingue «mire y esta bien» de «no mire»"
else
	malo "la spec real sale $r: hay una clave de entrada fuera del tipo (corre el gate y lee cual)"
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
if [ "$r" = "1" ] && casa 'despuess. en typo\.ts' && casa 'una ENTRADA la usa y el tipo no la declara'; then
	paso "un typo en el nombre del gancho (despuess) sale 1, con su clave, su fichero y su razon"
elif [ "$r" = "1" ]; then
	malo "salio 1 sin nombrar clave+fichero+razon: la direccion entrada->tipo no esta acreditada"
else
	malo "el typo deberia salir 1 y salio $r: el gate no protege de lo unico que existe para cazar"
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
if [ "$r" = "0" ] && casa 'sin hallazgos'; then
	paso "claves anidadas en un closure NO se señalan (sin allowlist, por profundidad)"
elif [ "$r" = "0" ]; then
	malo "salio 0 sin la linea de limpio: no se distingue de un gate que no midio"
else
	malo "el gate señalo claves anidadas (rc $r): vuelve a dar falsos positivos y se ignorara"
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
if [ "$r" = "0" ] && casa 'sin hallazgos'; then
	paso "las claves citadas en COMENTARIOS no se cuentan como uso"
elif [ "$r" = "0" ]; then
	malo "salio 0 sin la linea de limpio: no se distingue de un gate que no midio"
else
	malo "el gate conto la prosa (rc $r)"
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
if [ "$r" = "1" ] && casa 'despuess.*typo-invocacion\.ts' && casa 'typo de invocacion'; then
	paso "un typo en la INVOCACION sale 1, nombrando la clave Y el fichero"
elif [ "$r" = "1" ]; then
	malo "salio 1 pero sin nombrar clave+fichero: el rojo no dice donde mirar"
else
	malo "el typo de INVOCACION deberia salir 1 y salio $r: es el caso que el gate existe para cazar"
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
assert mut != src, "el mutante de invocaciones NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
if exige_construido "$mI" "el mutante sin invocaciones"; then
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
		malo "NO HE PODIDO MIRAR: el mutante sin invocaciones REVENTO (Traceback) en vez de morir: un mutante que revienta no acredita nada"
	elif ! casa 'typo de invocacion'; then
		paso "el mutante que deja de leer las INVOCACIONES ya no reporta el typo: el caso 4-bis lo cubre"
	else
		malo "el mutante sin invocaciones sigue reportando el typo (rc $r): imposible, revisa el mutante"
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
viejo = 'print(f"  ⛔ `{k}` en {nombre_fichero}: {porque}")'
nuevo = 'print(f"  ⛔ {porque}")'
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante del diagnostico NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
r="$(rc_de_con "$mF" "$T/typo-invocacion.ts")"
if [ "$r" = "1" ] && ! casa 'despuess.*typo-invocacion\.ts'; then
	paso "el mutante que quita el FICHERO del diagnostico es DETECTABLE: el rojo deja de decir donde"
else
	malo "el mutante del diagnostico no se distingue: el mensaje sigue sin estar protegido"
fi

# ── 5 · fichero ausente => 2, no 0 ────────────────────────────────────────────────────────────
r="$(rc_de "$T/no-existe.ts")"
if [ "$r" = "2" ] && casa 'NO HE PODIDO MIRAR' && casa 'No such file'; then
	paso "fichero ausente => rc 2 (no he podido mirar), no 0, y el rojo dice por que"
elif [ "$r" = "2" ]; then
	malo "salio 2 sin decir que no pudo mirar ni por que: el 2 mudo se lee como un fallo cualquiera"
else
	malo "fichero ausente deberia salir 2 y salio $r"
fi

# ── 6 · una spec que el gate NO entiende => 2, no 0 ───────────────────────────────────────────
# ⛔ ES LA REGLA 5 DEL CANON. Si mañana la spec declara `VIEWS` de otra forma, el gate NO puede
#    decir «limpia»: no ha podido mirar. Sin este caso, un refactor apagaria el gate en verde.
printf 'const OTRA_COSA = []\n' >"$T/rara.ts"
r="$(rc_de "$T/rara.ts")"
if [ "$r" = "2" ] && casa 'NO HE PODIDO MIRAR' && casa 'no encuentro la declaracion'; then
	paso "una spec con otra forma => rc 2 nombrando la declaracion que no encuentra"
elif [ "$r" = "2" ]; then
	malo "salio 2 sin nombrar lo que no encuentra: quien lo lea no sabe que refactor lo rompio"
else
	malo "una spec irreconocible deberia salir 2 y salio $r"
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
assert mut != src, "el mutante 1 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
r="$(rc_de_con "$m1" "$T/prosa.ts")"
if [ "$r" = "1" ] && casa 'fantasma'; then
	paso "el mutante que deja de borrar comentarios de BLOQUE MUERE en el caso 4, y por la clave que se inventa"
elif [ "$r" = "1" ]; then
	malo "el mutante murio con rc 1 pero por OTRA cosa: no acredita el borrado de bloque (mira la salida)"
else
	malo "el mutante de los comentarios de bloque SOBREVIVIO (rc $r): el caso 4 no cubre nada"
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
if [ "$r" = "1" ] && casa 'idd. en primera-clave\.ts' && casa 'el tipo no la declara'; then
	paso "un typo en la PRIMERA clave de una entrada escrita en linea sale 1 y la nombra"
elif [ "$r" = "1" ]; then
	malo "salio 1 sin nombrar la clave: el rojo no dice cual"
else
	malo "un typo en la primera clave de una entrada en linea salio $r: el escaner no la ve"
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
assert mut != src, "el mutante de la mirada atras NO se aplico"
compile(mut, "mP", "exec")
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mP" "$T/primera-clave.ts")"
# ⛔ Se exige el DEFECTO, no un rc cualquiera: el mutante tiene que salir 0 diciendo «sin
#    hallazgos» —que es la ceguera— y NO morir con una excepcion. Un mutante que revienta acredita
#    que el guion se rompe, no que la guarda funcione.
if [ "$r" = "0" ] && casa 'sin hallazgos'; then
	paso "el mutante que vuelve a consumir la llave DEJA PASAR el typo y dice «sin hallazgos»: el caso 2-bis lo caza"
elif casa 'Traceback'; then
	malo "el mutante murio con una EXCEPCION, no con la ceguera: eso no acredita la mirada atras"
else
	malo "el mutante no reprodujo la ceguera (rc $r): el caso 2-bis no acredita nada"
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
if [ "$r" = "1" ] && casa 'settle. en gancho-muerto\.ts' && casa 'gancho muerto'; then
	paso "una clave que las ENTRADAS traen y el arnes no invoca nunca sale 1 como gancho muerto"
elif [ "$r" = "1" ]; then
	malo "salio 1 sin nombrar clave+fichero+'gancho muerto': la tercera direccion no esta acreditada"
else
	malo "el gancho muerto deberia salir 1 y salio $r: las entradas piden algo que nadie lee"
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
         '        anota(k, "las ENTRADAS la traen y el arnes NO la invoca nunca: gancho muerto")')
nuevo = "    pass  # MUTANTE: el cruce del gancho muerto ya no se hace"
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante del gancho muerto NO se aplico"
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mG" "$T/gancho-muerto.ts")"
if [ "$r" = "0" ] && ! casa 'gancho muerto'; then
	paso "el mutante que retira el cruce del gancho muerto MUERE en el caso 8, y por su nombre"
else
	malo "el mutante del gancho muerto sobrevivio (rc $r): el caso 8 no acredita esa direccion"
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
if [ "$r" = "1" ] && casa 'despues. en tipo-en-prosa\.ts'; then
	paso "una clave mencionada SOLO en un comentario del tipo no cuenta como declarada: sale 1 y la nombra"
elif [ "$r" = "1" ]; then
	malo "salio 1 sin nombrar la clave: el rojo no dice cual"
else
	malo "una clave solo mencionada en prosa del tipo salio $r: el tipo se sigue leyendo en crudo"
fi

# ── 2-quinquies · MUTANTE: el tipo vuelve a leerse del fuente en crudo ─────────────────────────
mQ="$T/mQ.py"
python3 - "$GATE" "$mQ" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = 'm = re.search(r"const VIEWS:\\s*\\{(.*?)\\}\\[\\]\\s*=", src_sin_prosa, re.S)'
nuevo = 'm = re.search(r"const VIEWS:\\s*\\{(.*?)\\}\\[\\]\\s*=", src, re.S)  # MUTANTE: tipo en crudo'
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "el mutante del tipo en crudo NO se aplico"
compile(mut, "mQ", "exec")  # un mutante que no compila no acredita nada
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mQ" "$T/tipo-en-prosa.ts")"
# Se exige el DEFECTO —rc 0 diciendo «sin hallazgos»— y NO una excepcion cualquiera.
if [ "$r" = "0" ] && casa 'sin hallazgos'; then
	paso "leyendo el tipo en crudo, la clave en prosa se cuenta como declarada y sale «sin hallazgos»: el caso 2-quater lo caza"
elif casa 'Traceback'; then
	malo "el mutante del tipo en crudo murio con una EXCEPCION, no con la ceguera"
else
	malo "el mutante del tipo en crudo no reprodujo la ceguera (rc $r): el caso 2-quater no acredita nada"
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
if ! command grep -q 'medido ahora' <<<"$r_sin"; then
	malo "con un tsconfig que NO nombra e2e, el gate no dice que lo midio: $r_sin"
elif ! command grep -q 'ALGUN tsconfig NOMBRA' <<<"$r_con"; then
	malo "con un tsconfig que SI nombra e2e, el gate sigue afirmando que ninguno lo hace: $r_con"
elif ! command grep -q 'no he podido comprobar' <<<"$r_vacio"; then
	malo "sin ningun tsconfig, el gate AFIRMA en vez de decir que no pudo mirar: $r_vacio"
else
	paso "la premisa del gate se mide: la afirma, la retira si algun tsconfig nombra e2e, y calla si no puede mirar"
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
salida="$( cd "$T/uni" && python3 "$GATE" 2>&1 )"; rc=$?
if [ "$rc" != 1 ]; then
	malo "con DOS tablas VIEWS y la segunda sucia, el gate da rc=$rc en vez de 1 (universo de un solo fichero)"
elif ! command grep -q 'z-sucia' <<<"$salida"; then
	malo "el gate no nombra el fichero del hallazgo: $salida"
elif ! command grep -q 'a-limpia' <<<"$salida"; then
	malo "el gate no dice que reviso TAMBIEN la limpia — sin eso no se sabe que abarco"
else
	paso "el universo se deriva: dos tablas VIEWS, revisa las dos y caza la del segundo fichero"
fi

# Y la otra direccion: sin ninguna tabla NO dice «limpio», dice que no pudo mirar.
mkdir -p "$T/uni-vacio/web/e2e"
salida="$( cd "$T/uni-vacio" && python3 "$GATE" 2>&1 )"; rc=$?
if [ "$rc" != 2 ]; then
	malo "sin ninguna tabla VIEWS el gate da rc=$rc; un universo vacio no es un arbol limpio"
else
	paso "un universo vacio sale rc 2 (no pude mirar), no rc 0"
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
if [ "$r" = "1" ] && casa 'typo de invocacion' && casa 'despuess'; then
	paso "un typo de invocacion escrito DENTRO de una plantilla sale 1 y lo nombra"
elif [ "$r" = "0" ]; then
	malo "el typo dentro de la plantilla sale rc 0: la sonda vuelve a ser ciega al codigo de \${}"
else
	malo "el typo dentro de la plantilla da rc $r sin nombrarlo: $(cat "$T/salida" 2>/dev/null | head -3)"
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
assert mut != src, "el mutante de la plantilla NO se aplico"
open(sys.argv[2], "w").write(mut)
PY2
if exige_construido "$mPlantilla" "el mutante que ciega la plantilla"; then
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
		malo "NO HE PODIDO MIRAR: el mutante que ciega la plantilla REVENTO (Traceback) en vez de morir: un mutante que revienta no acredita nada"
	elif ! casa 'typo de invocacion'; then
		paso "el mutante que blanquea la plantilla entera MUERE en el caso 4-quinquies, y por su nombre"
	else
		malo "el mutante que ciega la plantilla sigue reportando el typo (rc $r): revisa el mutante"
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
if [ "$r" = "2" ] && casa 'NO HE PODIDO MIRAR' && casa 'forma que no se leer'; then
	paso "una forma de uso de VIEWS que el gate no modela sale rc 2 nombrando la linea, no rc 0"
elif [ "$r" = "0" ]; then
	malo "la forma no modelada sale rc 0: el gate llama limpio a lo que no ha podido leer"
else
	malo "la forma no modelada da rc $r sin decir que no pudo mirar"
fi

# ── 5-ter · MUTANTE: se retira la deteccion de usos no modelados ──────────────────────────────
mN="$T/mN.py"
python3 - "$GATE" "$mN" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = "    sueltos = usos_no_modelados(limpio_todo)"
nuevo = "    sueltos = []  # MUTANTE: la lista de formas vuelve a ser una promesa"
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "el mutante de usos no modelados NO se aplico"
open(sys.argv[2], "w").write(mut)
PY2
r="$(rc_de_con "$mN" "$T/forma-mixta.ts")"
if [ "$r" = "0" ]; then
	paso "el mutante que se fia de la lista de formas MUERE en el caso 5-bis: sale rc 0 con el typo delante"
else
	malo "el mutante sin deteccion de usos no modelados da rc $r; se esperaba el falso verde"
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
	paso "con dos ligaduras el diagnostico nombra la que de verdad invoca (zeta), no la primera del alfabeto"
elif casa 'alfa\.despuess'; then
	malo "el diagnostico nombra \`alfa.despuess\`: manda al lector al bucle limpio"
else
	malo "el caso de dos ligaduras da rc $r sin nombrar la invocacion"
fi

printf '\ntest-check-capture-view-keys: %d pasan, %d fallan\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
