#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería del trinquete de formato. Corre contra un SEÑUELO —listador inyectado y líneas base
# fabricadas— y nunca contra `web/`: una batería que dependa del árbol vivo mide el árbol, no la
# regla, y se pone roja el día que alguien formatea un fichero.
#
# Cada casilla dice qué defecto la mataría, porque una casilla que no muere con el defecto puesto
# no fija nada.
set -uo pipefail

# Testigo de «NO HE PODIDO MIRAR»: se declara ARRIBA porque el primer señuelo que lo escribe
# está en la mitad del fichero, y una variable declarada más abajo no existe todavía ahí.
RATCHET_SELFTEST_NOMIRAR="${TMPDIR:-/tmp}/.ratchet-selftest-nomirar.$$"
rm -f "$RATCHET_SELFTEST_NOMIRAR"

# ⛔ AISLAMIENTO DE ENTORNO GIT, y es OBLIGATORIO para cualquier batería que empareje `mktemp -d`
# con git: `lint:git-env` lo comprueba y me lo puso rojo al añadir el señuelo de la línea base.
# Un `GIT_DIR` heredado hace que `git -C "$tmp"` opere sobre OTRO repositorio — el mismo mecanismo
# que hoy fabricó un directorio basura dentro del árbol real desde otra batería.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

AQUI=$(cd "$(dirname "$0")" && pwd)

# ⛔ LA BASE TEMPORAL TIENE QUE PODER EJECUTAR, y en este contenedor /tmp NO PUEDE.
#
# Toda esta batería se apoya en señuelos EJECUTABLES: `_npx_falso` escribe un `npx` y le da
# `chmod +x` para interponerse entre el SUT y el formateador real. Los doce `mktemp -d` de abajo
# los crea bajo `${TMPDIR:-/tmp}`, y **/tmp está montado `noexec`**: el señuelo devuelve **126** al
# ejecutarse y NADIE lo mira, porque quien lo invoca es el SUT por PATH.
#
# La consecuencia NO es una casilla roja honesta, que es lo que querríamos: el SUT se cae al
# siguiente resolutor —`npx --no-install prettier`— y **mide con el formateador REAL**. Medido el
# 2026-08-20 con el señuelo instrumentado: `PRETTIER=[npx --no-install prettier]` mientras la
# salida era del prettier de verdad («Code style issues found in 2 files. Run Prettier **with
# --write to fix.**», y los ficheros en otro orden). Es decir: la casilla que dice medir «un
# fichero borrado durante la medida» **no borraba nada** y juzgaba al guion por una situación que
# nunca ocurrió.
#
# ⇒ Y explica la divergencia que abrió la fila: **si /tmp puede ejecutar, el señuelo entra y la
#   casilla pasa; si no, no entra y falla**. Eso no es una propiedad del SUT: es del montaje de la
#   máquina, y por eso el mismo commit daba distinto en el runner y en local.
#
# Se elige la primera base que DE VERDAD ejecuta —probándolo, no suponiéndolo— y si ninguna puede,
# se muere con 2: una batería que no puede interponer sus señuelos no ha medido nada.
_base_ejecuta() { # <directorio> -> 0 si un script creado ahí se puede ejecutar
	[ -d "$1" ] && [ -w "$1" ] || return 1
	local _p="$1/.ratchet-selftest-exec.$$"
	printf '#!/usr/bin/env bash\nexit 7\n' > "$_p" 2>/dev/null || return 1
	chmod +x "$_p" 2>/dev/null || { rm -f "$_p"; return 1; }
	"$_p" >/dev/null 2>&1
	local _rc=$?
	rm -f "$_p"
	[ "$_rc" -eq 7 ]
}
_elige_base_temporal() {
	local _c
	for _c in "${TMPDIR:-}" "${HOME:-}" "$(cd "$(dirname "$0")/.." && pwd)/.selftest-tmp"; do
		[ -n "$_c" ] || continue
		mkdir -p "$_c" 2>/dev/null || continue
		if _base_ejecuta "$_c"; then printf '%s\n' "$_c"; return 0; fi
	done
	return 1
}
_TMP_BASE=$(_elige_base_temporal) || {
	echo "format-ratchet self-test: ⛔ NO HE PODIDO MIRAR: ninguna base temporal ejecuta." >&2
	echo "   probadas: TMPDIR='${TMPDIR:-}' HOME='${HOME:-}' y <repo>/.selftest-tmp." >&2
	echo "   Sin poder ejecutar un señuelo, el SUT mide con el formateador REAL y esta" >&2
	echo "   batería estaría juzgando otra cosa. Monta una de ellas sin 'noexec'." >&2
	exit 2
}
[ "$_TMP_BASE" = "${TMPDIR:-}" ] || echo "format-ratchet self-test: ⓘ TMPDIR no ejecuta; señuelos en ${_TMP_BASE}" >&2
export TMPDIR="$_TMP_BASE"

SUT="${AQUI}/format-ratchet.sh"
ok=0; fallos=0

# ⛔ CONTENIDO DE LOS SEÑUELOS DE ÁRBOL REAL. `MAL` no pasa prettier; `BIEN` sí — comprobado
#    contra prettier 3.9.6 antes de escribir la primera casilla que los usa. Si un día `BIEN`
#    dejara de ser conforme, las casillas que lo usan como control NEGATIVO fallarían, que es
#    exactamente lo que debe pasar: un control negativo que no puede fallar no controla.
MAL='const    x   =  {a:1,b:2}
export default x'
BIEN='const x = { a: 1, b: 2 };
export default x;'

# ⛔ TRES RESPUESTAS AL INFORMAR, NO DOS — y esto sale de la casilla NOMBRE AUTOEXCLUYENTE, que
#    tradujo un rc=2 («no alcancé el formateador») a la acusación sustantiva «se colaron»: en CI,
#    El carril de integración estuvo a punto de perseguir un agujero que no existía. La lección estaba en UNA
#    casilla; aquí queda en un sitio del que la heredan TODAS las de árbol real.
juzgar() { # <rc esperado> <rc obtenido> <descripción> <salida> [fragmento exigido]
  local esp="$1" got="$2" desc="$3" out="$4" frag="${5:-}" frag_ok=0
  if [ -n "$frag" ]; then
    case "$out" in *"$frag"*) frag_ok=0 ;; *) frag_ok=1 ;; esac
  fi
  if [ "$got" = "$esp" ] && [ "$frag_ok" -eq 0 ]; then
    ok=$((ok+1)); printf '  ok    %-58s rc=%s\n' "$desc" "$got"
  elif [ "$esp" != "2" ] && [ "$got" = "2" ]; then
    fallos=$((fallos+1))
    printf '  FALLO %-58s rc=2 · NO PUDE MIRAR: %s\n' "$desc" \
      "$(printf '%s' "$out" | grep -iE 'NO HE PODIDO MIRAR|no encuentro el formateador' | head -1 | cut -c1-58)"
  else
    fallos=$((fallos+1))
    if [ "$frag_ok" -ne 0 ]; then
      printf '  FALLO %-58s esperaba=%s obtuvo=%s · falta «%s»\n' "$desc" "$esp" "$got" "$frag"
      # ⛔ Y LO QUE EL SUT DIJO DE VERDAD. Sin esto, un fallo que sólo ocurre en el runner deja
      #    «esperaba=1 obtuvo=0» y nada más, y el diagnóstico exige acceso a la máquina. Medido el
      #    2026-08-20: dos casillas llevaban horas rojas en CI y verdes aquí, y el log no daba UN
      #    dato para distinguir «el SUT no vio la deuda» de «el señuelo no se construyó».
      printf '%s\n' "$out" | sed -n '1,6p' | sed 's/^/          SUT| /'
    else
      printf '  FALLO %-58s esperaba=%s obtuvo=%s\n' "$desc" "$esp" "$got"
    fi
    printf '%s\n' "$out" | tail -3 | sed 's/^/        /'
  fi
}

ULTIMA_SALIDA=""
probar() { # <rc esperado> <descripción> <línea base | __SIN_FICHERO__> <salida del listador> [rc]
  local esperado="$1" desc="$2" base_txt="$3" salida="$4" rc_list="${5:-1}"
  local t basef got_out got
  t=$(mktemp -d)
  if [ "$base_txt" = "__SIN_FICHERO__" ]; then
    basef="$t/no-existe"                       # se pasa una ruta que NO existe a propósito
  else
    basef="$t/base"; printf '%s\n' "$base_txt" > "$basef"
  fi
  printf '%s\n' "$salida" > "$t/salida"
  # ⛔ EL SEÑUELO CONTESTA A DOS PREGUNTAS, y esto salió de romper una casilla propia: desde que
  #    el trinquete VERIFICA cada supuesto arreglo (preguntando por el fichero como $1), un
  #    listador que ignore el argumento y salga 1 declara «escondido» todo lo que se arregló —
  #    y la casilla «y lo DICE, en vez de callarse la mejora» se puso roja por eso, no por lo que
  #    mide. Con argumento, el señuelo dice que SÍ está formateado; sin él, entrega el censo.
  {
    printf '#!/usr/bin/env bash\n'
    printf '[ $# -gt 0 ] && exit 0\n'
    printf "cat '%s'\n" "$t/salida"
    printf 'exit %s\n' "$rc_list"
  } > "$t/l.sh"
  got_out=$(FORMAT_RATCHET_BASELINE="$basef" \
            FORMAT_RATCHET_CMD="bash '$t/l.sh'" \
            bash "$SUT" 2>&1)
  got=$?
  ULTIMA_SALIDA="$got_out"
  if [ "$got" = "$esperado" ]; then
    ok=$((ok+1)); printf '  ok    %-58s rc=%s\n' "$desc" "$got"
  else
    fallos=$((fallos+1)); printf '  FALLO %-58s esperaba=%s obtuvo=%s\n' "$desc" "$esperado" "$got"
    printf '%s\n' "$got_out" | tail -4 | sed 's/^/        /'
  fi
  rm -rf "$t"
}

SALIDA_181='[warn] src/features/a.tsx
[warn] src/features/b.tsx
[warn] Code style issues found in 2 files. Run Prettier with --write to fix.'

echo "VERDES — la deuda no sube"
probar 0 'los incumplidores son exactamente los de la línea base' \
  'src/features/a.tsx
src/features/b.tsx' "$SALIDA_181"
probar 0 'uno ARREGLADO: pasa, y la línea base puede bajar' \
  'src/features/a.tsx
src/features/b.tsx
src/features/c.tsx' "$SALIDA_181"
case "$ULTIMA_SALIDA" in
  *'puede BAJAR 1'*) ok=$((ok+1)); printf '  ok    %-58s\n' 'y lo DICE, en vez de callarse la mejora' ;;
  *) fallos=$((fallos+1)); printf '  FALLO %-58s\n' 'no dijo que la línea base puede bajar' ;;
esac
probar 0 'árbol totalmente limpio con línea base vacía' '' '' 0

echo "ROJOS — la deuda sube, y el culpable se NOMBRA"
probar 1 'un incumplidor NUEVO tumba el trinquete' \
  'src/features/a.tsx' "$SALIDA_181"
case "$ULTIMA_SALIDA" in
  *'src/features/b.tsx'*) ok=$((ok+1)); printf '  ok    %-58s\n' 'y nombra al fichero nuevo, no sólo el total' ;;
  *) fallos=$((fallos+1)); printf '  FALLO %-58s\n' 'no nombró al incumplidor nuevo' ;;
esac
# ⛔ LA CASILLA QUE JUSTIFICA QUE LA LÍNEA BASE SEA UNA LISTA: total idéntico, culpable distinto.
#    Un trinquete por NÚMERO daría verde aquí, y es el defecto que de verdad tengo que impedir.
probar 1 'SUSTITUCIÓN: mismo total, culpable distinto' \
  'src/features/a.tsx
src/features/z.tsx' "$SALIDA_181"

echo "NO HE PODIDO MIRAR — nunca un verde"
probar 2 'sin línea base no es «cero deuda»' '__SIN_FICHERO__' "$SALIDA_181"
probar 2 'el formateador que no arranca (rc=127)' \
  'src/features/a.tsx' 'npx: command not found' 127
probar 2 'rc=1 pero sin nombrar ningún fichero: ilegible' \
  'src/features/a.tsx' 'Something went wrong' 1
# ⛔ CONTROL DEL CONTADOR: la línea de resumen empieza por `[warn]` y contarla infla el censo.
#    Medido el 2026-08-15: `grep -c` dijo 182 donde prettier decía 181.
probar 0 'la línea de RESUMEN no se cuenta como incumplidor' \
  'src/features/a.tsx
src/features/b.tsx' "$SALIDA_181"
case "$ULTIMA_SALIDA" in
  *'2 incumplidor(es) hoy'*) ok=$((ok+1)); printf '  ok    %-58s\n' 'el censo dice 2, no 3' ;;
  *) fallos=$((fallos+1)); printf '  FALLO %-58s\n' "el censo contó el resumen: $ULTIMA_SALIDA" ;;
esac

echo "DEUDA OCULTA — salir del censo NO es estar arreglado"
# ⛔ REPRODUCIDO CONTRA EL ÁRBOL REAL ANTES DE ESCRIBIR ESTO: metí un fichero de la línea base en
#    `web/.prettierignore` y el trinquete contestó «✔ la línea base puede BAJAR 1» con rc=0. No se
#    formateó: se escondió, y el gate FELICITÓ por ello.
#
#    El listador del señuelo contesta a DOS preguntas según reciba argumento: sin él, el censo;
#    con él, si ESE fichero está formateado. Sin esa segunda pregunta ninguna casilla podría
#    alcanzar la verificación — y una guarda que las pruebas no tocan no está probada.
senuelo_oculto() { # <rc para b.tsx> -> imprime el directorio
  local rc_b="$1" t; t=$(mktemp -d)
  {
    printf '#!/usr/bin/env bash\n'
    printf 'if [ $# -eq 0 ]; then\n'
    printf "  printf '[warn] a.tsx\\n[warn] Code style issues found in 1 files.\\n'\n"
    printf '  exit 1\n'
    printf 'fi\n'
    printf 'case "$1" in b.tsx) exit %s ;; *) exit 0 ;; esac\n' "$rc_b"
  } > "$t/l.sh"
  printf 'a.tsx\nb.tsx\n' > "$t/base"
  printf '%s' "$t"
}
t1=$(senuelo_oculto 1)
out=$(FORMAT_RATCHET_BASELINE="$t1/base" FORMAT_RATCHET_CMD="bash $t1/l.sh" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *"SIN estar formateados"*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, y lo nombra\n' 'esconder deuda es ROJO, no una mejora'
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s\n' 'esconder deuda debia ser ROJO' "$rc"
fi
rm -rf "$t1"
# ⛔ CONTROL NEGATIVO: sin él, la casilla de arriba se cumpliría prohibiendo TODA mejora.
t2=$(senuelo_oculto 0)
out=$(FORMAT_RATCHET_BASELINE="$t2/base" FORMAT_RATCHET_CMD="bash $t2/l.sh" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && case "$out" in *"puede BAJAR 1"*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=0\n' 'un arreglo DE VERDAD si baja la linea base'
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s\n' 'un arreglo de verdad debia pasar' "$rc"
fi
rm -rf "$t2"

echo "NOMBRE AUTOEXCLUYENTE — un fichero llamado como el resumen no se saca del censo"
# ⛔ Lo encontró el contraste the model con positivos FABRICADOS, y lo reproduje: un árbol con
#    `ordinary.ts`, `Code style issues hidden.ts`, `Forgot to run hidden.ts` y `[diagnostic]
#    hidden.ts` —los cuatro mal formateados— publicaba «1 incumplidor(es) · nuevos 0» y rc=0.
#    Bastaba LLAMARSE como el resumen para desaparecer con la deuda dentro.
#
#    Esta casilla necesita un árbol REAL porque el filtro por existencia sólo corre sin señuelo.
#    Si el formateador no está, lo DICE y cuenta como fallo — un «no he podido mirar» silencioso
#    aquí dejaría la guarda sin medir justo donde importa.
tr=$(mktemp -d)
for f in 'ordinary.ts' 'Code style issues hidden.ts' 'Forgot to run hidden.ts' '[diagnostic] hidden.ts'; do
  printf 'const    x   =  {a:1,b:2}\nexport default x\n' > "$tr/$f"
done
# ⛔ SIN ENLAZAR `node_modules` NI PREGUNTARLE A `npx` DESDE EL SEÑUELO. Así estaba, y el 2026-08-15
# esta casilla —fail-closed, como debe— puso ROJO el check `web` para TODA la cola: en local la
# resolución funcionaba y en CI no. El formateador es la HERRAMIENTA, no parte del árbol medido, y
# ahora el guion lo resuelve por ruta absoluta desde su propio repositorio; el señuelo sólo aporta
# el SUJETO. Si no hubiera formateador, el guion sale 2 y esta casilla lo verá como tal.
printf 'ordinary.ts\n' > "$tr/base"
out=$(FORMAT_RATCHET_ROOT="$tr" FORMAT_RATCHET_BASELINE="$tr/base" FORMAT_RATCHET_GLOB='*.ts' \
      bash "$SUT" 2>&1); rc=$?
# ⛔ TRES RESPUESTAS AL INFORMAR, NO DOS — y este fichero las distinguía en el SUJETO mientras las
#    fundía al CONTARLO. Antes, cualquier `rc` distinto de 1 imprimía «los nombres-resumen se
#    colaron»: una acusación sustantiva y CONCRETA sobre el filtro de nombres. El 2026-08-15, en
#    CI, el guion salió **2** —«no he podido mirar», porque no alcanzaba el formateador— y esta
#    casilla lo tradujo a «se colaron». El carril de integración estuvo a punto de perseguir un agujero que
#    no existe, y lo dijo: **una batería que distingue tres respuestas debe distinguirlas también
#    al informar.**
if [ "$rc" -eq 1 ] && case "$out" in *'nuevos 3'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, nuevos 3\n' 'tres nombres-resumen NO se autoexcluyen'
elif [ "$rc" -eq 2 ]; then
  fallos=$((fallos+1))
  printf '  FALLO %-58s rc=2 · %s\n' 'NO PUDE MIRAR: el sujeto no llegó a censar' \
    "$(printf '%s' "$out" | grep -iE 'NO HE PODIDO MIRAR|formateador' | head -1 | cut -c1-70)"
  # ⛔ Y SE IMPRIME LA SALIDA ENTERA DEL SUJETO, no una línea recortada a 70 caracteres.
  #
  # Medido el 2026-08-16: esta casilla puso ROJO el check `web` —contexto REQUERIDO, o sea toda la
  # cola— y el mensaje que llegó al log de CI fue «el formateador falló pero no ...», cortado justo
  # antes del motivo. El sujeto SÍ escribe su diagnóstico (`format-ratchet.sh:157` vuelca las cinco
  # últimas líneas de la salida del formateador), y esta batería se lo tragaba entero dentro de
  # `$out`. Resultado: un rojo INATRIBUIBLE, que obliga al siguiente a adivinar — y adivinar sobre
  # un gate ajeno es cómo se rompen los gates ajenos.
  #
  # Localmente esta casilla pasa (19/19, prettier 3.9.6 emitiendo `[warn]` con normalidad), así que
  # la causa vive en el entorno de CI y NO se ve desde el hub. Esto no la arregla: hace que la
  # PRÓXIMA corrida la diga en vez de esconderla.
  printf '        ── salida completa del sujeto (para que el rojo sea atribuible) ──\n'
  printf '%s\n' "$out" | sed 's/^/        /'
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s · %s\n' 'los nombres-resumen se colaron' "$rc" \
    "$(printf '%s' "$out" | grep -o '[0-9]* incumplidor[^·]*' | head -1)"
fi
rm -rf "$tr"

echo "SALIDA DECORADA — un formateador que colorea no puede vaciar el censo"
# ⛔ ESTA CASILLA EXISTE PORQUE SU AUSENCIA TUVO ROJO EL CHECK `web` —contexto REQUERIDO, o sea
#    TODA la cola— del 2026-08-15 al 2026-08-16, y ninguna de las 19 casillas anteriores lo veía.
#
#    El censo pide el prefijo LITERAL `[warn] `. En local prettier lo escribe así y las 19 pasaban.
#    En CI escribe `[<ESC>[33mwarn<ESC>[39m] fichero` —chalk detecta GitHub Actions y colorea sin
#    TTY—, el `sed` no casaba ninguna línea y el censo salía VACÍO. Con rc=1 del formateador eso
#    aterrizaba en «falló pero no nombró ningún fichero»: un `NO HE PODIDO MIRAR` cuya causa no
#    estaba en el log. Medido con el binario real: **0 ficheros censados con color, 3 sin él.**
#
#    ⚠ POR QUÉ POR EL CAMINO DEL LISTADOR Y NO CON `FORCE_COLOR` SOBRE PRETTIER. Lo escribí primero
#    así y **el mutante SOBREVIVIÓ: 20/20 con el filtro ANSI retirado**. La casilla pasaba sin
#    ejercitarlo. Causa medida: el sujeto invoca prettier con `NO_COLOR=1` y **`NO_COLOR` gana a
#    `FORCE_COLOR`**, luego no había color que filtrar — verde por vacuidad, y de los que no se ven
#    porque el número sale bien. (La medición que me hizo creer lo contrario usaba `env $var` en
#    zsh, que NO hace word-splitting: pasaba los dos ajustes como UN argumento.)
#
#    El listador entrega la salida decorada **por construcción**, sin depender de que ninguna
#    herramienta respete ninguna variable. Y cubre la rama que `NO_COLOR` no toca: un
#    `FORMAT_RATCHET_CMD` que coloree rompe el censo igual, y ahí no hay variable que valga.
#
#    MUTANTE: quita el `sed` del ESC en `format-ratchet.sh` → esta casilla da rc=2. VERIFICADO.
#    NO DISPARA EN LA OTRA DIRECCIÓN: la casilla de arriba corre sin decorar y exige `nuevos 3`,
#    así que un filtro que se comiera las líneas buenas rompería aquélla.
d=$(mktemp -d)
printf 'a.ts\n' > "$d/base"
_E=$(printf '\033')
_cmd="printf '[${_E}[33mwarn${_E}[39m] a.ts\n[${_E}[33mwarn${_E}[39m] b.ts\n[${_E}[33mwarn${_E}[39m] c.ts\n[${_E}[33mwarn${_E}[39m] Code style issues found in 3 files.\n'; exit 1"
out=$(FORMAT_RATCHET_CMD="$_cmd" FORMAT_RATCHET_BASELINE="$d/base" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *'nuevos 2'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, nuevos 2\n' 'el censo sobrevive a una salida decorada'
elif [ "$rc" -eq 2 ]; then
  fallos=$((fallos+1))
  printf '  FALLO %-58s rc=2 · el filtro ANSI no está haciendo su trabajo\n' \
    'una salida decorada VACIA el censo'
  printf '%s\n' "$out" | sed 's/^/        /' >&2
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s · %s\n' 'salida decorada: veredicto inesperado' "$rc" \
    "$(printf '%s' "$out" | grep -o '[0-9]* incumplidor[^·]*' | head -1)"
fi
rm -rf "$d"

echo "AUTORIDAD — ni el entorno ni la propia línea base pueden comprar un verde"
# ⛔ HALLAZGO HIGH DEL CONTRASTE: un FORMAT_RATCHET_CMD heredado hacía pasar el Task COMPLETO —16/16
#    casillas incluidas— sin ejecutar prettier ni una vez. El aviso ruidoso lo lee un humano; el
#    verde le llega igual a la máquina. El punto de entrada de producción pasa `--gate` y rechaza
#    cualquier anulación.
out=$(FORMAT_RATCHET_CMD="printf '[warn] x\n'; exit 1" bash "$SUT" --gate 2>&1); rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *'no se admite ninguna anulación'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=2\n' '--gate RECHAZA un entorno inyectado'
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s\n' '--gate debia rechazar la inyeccion' "$rc"
fi

# ⛔ SEGUNDO HALLAZGO HIGH: la línea base es la autoridad, y el MISMO cambio podía ampliarla —
#    añadir un fichero malo y su ruta a la base devolvía rc=0. Sólo puede encoger.
#    El señuelo es un repositorio git de verdad: se commitea una base y se amplía en el árbol.
d=$(mktemp -d)
(
  # HERMÉTICO y COMPROBADO, por la misma razón que el señuelo de más abajo: los runners corren
  # como root y esta caja no, así que un safe.directory o una plantilla heredada tumban el commit.
  cd "$d" && \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TEMPLATE_DIR= \
    git init -q -b main . && printf 'a.ts\n' > base.txt
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    git -C "$d" add -A >/dev/null 2>&1
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
  GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
    git -C "$d" commit -qm base >/dev/null 2>&1
) >/dev/null 2>&1
# ⛔ SIN ESTA COMPROBACIÓN, TRES CASILLAS MIENTEN A LA VEZ, y la peor es la que PASA.
#    Las que dependen de un HEAD commiteado son :305 (espera 1), :314 (espera 0) y la del otro
#    señuelo (espera 1). Sin HEAD el SUT no puede comparar y devuelve 0 ⇒ las dos que esperan 1
#    fallan y **:314, que es el CONTROL NEGATIVO, pasa por la razón equivocada**: pasa porque el
#    señuelo no se construyó, no porque el SUT haya juzgado nada. Un control negativo vacuo es
#    peor que uno ausente: certifica lo que ya no mira. Hallazgo de otro carril.
if ! git -C "$d" rev-parse --verify -q HEAD >/dev/null 2>&1; then
  printf 'test-format-ratchet: ⛔ NO HE PODIDO MIRAR: el señuelo de línea base no creó HEAD en %s.\n' "$d" >&2
  : > "${RATCHET_SELFTEST_NOMIRAR:?}"
fi
printf 'a.ts\nb.ts\n' > "$d/base.txt"          # la base CRECE en el árbol
out=$(cd "$d" && FORMAT_RATCHET_BASELINE=base.txt FORMAT_RATCHET_BASE_REF=HEAD \
      FORMAT_RATCHET_CMD="printf '[warn] a.ts\n'; exit 1" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *'ha CRECIDO'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, nombra la ruta\n' 'una linea base que CRECE es ROJO'
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s\n' 'una linea base que crece debia ser ROJO' "$rc"
fi
# ⛔ CONTROL NEGATIVO: sin él, la casilla se cumpliría prohibiendo TODO cambio de la base.
printf 'a.ts\n' > "$d/base.txt"                 # sin cambios respecto al commit
out=$(cd "$d" && FORMAT_RATCHET_BASELINE=base.txt FORMAT_RATCHET_BASE_REF=HEAD \
      FORMAT_RATCHET_CMD="printf '[warn] a.ts\n'; exit 1" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  ok=$((ok+1)); printf '  ok    %-58s rc=0\n' 'una linea base IGUAL pasa'
else
  fallos=$((fallos+1)); printf '  FALLO %-58s rc=%s\n' 'una base sin cambios debia pasar' "$rc"
fi
rm -rf "$d"

echo "ALCANCE — el trinquete vigila lo MISMO que format:check, ni un fichero menos"
# ⛔ ESTA CASILLA EXISTE PORQUE EL DEFECTO YA ESTUVO PUESTO. El alcance por defecto era
#    `src/**/*.{ts,tsx}` mientras `web/package.json` corre `prettier --check .`: medido el
#    2026-08-15, **191 incumplidores reales contra los 181 que veía el glob** — diez ficheros en
#    `e2e/`, `e2e-visual/`, un `.md` y un `.html` podrían haber empeorado con el trinquete
#    publicando «la deuda no sube». Se comparan DOS ficheros independientes (el script y el
#    package.json), no una fuente consigo misma, así que la casilla puede fallar de verdad.
# ⛔ RESUELTO DESDE EL GUION, NO DESDE EL CWD DEL LLAMANTE — causa raíz de la discrepancia
#    «18/19 en el runner, 19/19 en local» (C15-P7). `web/package.json` era una ruta RELATIVA,
#    así que esta casilla no medía el alcance: medía desde dónde la habían invocado. Desde la
#    raíz pasa; desde cualquier otro cwd falla con «no leo web/package.json» — reproducido el
#    2026-08-16 corriendo esta misma batería desde otro directorio: 18 pasan, 1 falla, el mismo
#    par de cifras que enrojecía un check OBLIGATORIO.
#
#    Y el fallo era HONESTO en su forma —dice que no pudo mirar— pero se leía como un defecto
#    del trinquete, y eso es lo que costó el tiempo: nadie buscaba un cwd, todos buscaban una
#    regresión de formato.
_TFR_ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
PKG=${FORMAT_RATCHET_PKG:-$_TFR_ROOT/web/package.json}
if [ -r "$PKG" ]; then
  declarado=$(sed -n 's/.*"format:check"[[:space:]]*:[[:space:]]*"prettier --check \(.*\)".*/\1/p' "$PKG" | head -1)
  delscript=$(sed -n 's/^PATRON=\${FORMAT_RATCHET_GLOB:-\(.*\)}$/\1/p' "$SUT" | head -1 | tr -d "'")
  if [ -z "$declarado" ]; then
    fallos=$((fallos+1)); printf '  FALLO %-58s\n' 'no supe leer format:check de package.json'
  elif [ "$declarado" = "$delscript" ]; then
    ok=$((ok+1)); printf '  ok    %-58s %s\n' 'el alcance NO se estrecha respecto a format:check' "$delscript"
  else
    fallos=$((fallos+1))
    printf '  FALLO %-58s package.json=%s script=%s\n' \
      'el trinquete vigila MENOS que format:check' "$declarado" "$delscript"
  fi
else
  fallos=$((fallos+1)); printf '  FALLO %-58s\n' "no leo ${PKG}: no puedo afirmar el alcance"
fi

echo "ALCANCE, PERO EJECUTADO — la invocación REAL por defecto abarca el árbol, no sólo src/"
# ⛔ ESTA CASILLA EXISTE PORQUE LA DE ARRIBA SOBREVIVIÓ A SU MUTANTE. Hallazgo MEDIUM 5 del
#    contraste the model (`an internal design note (not shipped):171-176`):
#    estrechar el alcance EFECTIVO a `src` dejando intacta la línea declarativa `PATRON=…:-.}`
#    dejaba la batería en 16/16. Re-medido el 2026-08-16 contra la batería de HOY, ya con 19
#    casillas: **19/19, el mutante sigue vivo**. La casilla declarativa parsea una asignación; no
#    prueba que la orden que se ejecuta consuma ese valor.
#
#    Así que aquí NO se inyecta listador ni se pasa `FORMAT_RATCHET_GLOB`: se deja el defecto real
#    y se pone el incumplidor FUERA de `src/`, con un `src/` limpio y presente para que un alcance
#    estrechado salga verde por su cuenta en vez de morir por un error de patrón — un mutante que
#    muere porque el patrón no existe no testifica sobre el ALCANCE.
tA=$(mktemp -d); mkdir -p "$tA/root/src"
printf '%s\n' "$MAL"  > "$tA/root/top.ts"
printf '%s\n' "$BIEN" > "$tA/root/src/clean.ts"
: > "$tA/base"                                   # la base va FUERA del árbol medido
out=$(FORMAT_RATCHET_ROOT="$tA/root" FORMAT_RATCHET_BASELINE="$tA/base" bash "$SUT" 2>&1); rc=$?
juzgar 1 "$rc" 'un incumplidor FUERA de src/ entra al censo' "$out" 'top.ts'
rm -rf "$tA"

echo "DEUDA OCULTA EN PRODUCCIÓN — la verificación corre con prettier de verdad"
# ⛔ Y ESTA EXISTE POR LO MISMO: quitar SÓLO la rama de producción de `--ignore-path /dev/null`
#    dejaba la batería en 16/16 (`…-format-ratchet-contrast.md:171,174`), y re-medido el 2026-08-16
#    sigue en **19/19**. Las casillas de DEUDA OCULTA de arriba prueban la rama del señuelo, no la
#    llamada real a prettier: son dos ramas distintas del mismo `if`, y sólo una está en el gate.
#
#    Aquí el fichero está de verdad en `.prettierignore` y de verdad mal formateado, así que sale
#    del censo sin estar arreglado y la verificación de producción es el ÚNICO camino que lo ve.
tB=$(mktemp -d); mkdir -p "$tB/root"
printf '%s\n' "$MAL" > "$tB/root/keep.ts"        # se queda en el censo: sin él, «rc=1 sin nombres»
printf '%s\n' "$MAL" > "$tB/root/hidden.ts"      # mal formateado y ESCONDIDO tras la exclusión
printf 'hidden.ts\n' > "$tB/root/.prettierignore"
printf 'hidden.ts\nkeep.ts\n' > "$tB/base"
out=$(FORMAT_RATCHET_ROOT="$tB/root" FORMAT_RATCHET_BASELINE="$tB/base" bash "$SUT" 2>&1); rc=$?
juzgar 1 "$rc" 'un excluido REAL sin formatear es ROJO' "$out" 'SIN estar formateados'
rm -rf "$tB"

# ⛔ CONTROL NEGATIVO, Y EN LA DIRECCIÓN QUE CASI NUNCA SE ESCRIBE: sin él, la casilla anterior se
#    cumpliría prohibiendo TODA salida del censo, es decir, castigando a quien paga deuda. Mismo
#    montaje, misma exclusión real, pero el fichero SÍ está formateado: tiene que pasar y decir que
#    la línea base puede bajar.
tC=$(mktemp -d); mkdir -p "$tC/root"
printf '%s\n' "$MAL"  > "$tC/root/keep.ts"
printf '%s\n' "$BIEN" > "$tC/root/ok.ts"         # excluido igual, pero conforme de verdad
printf 'ok.ts\n' > "$tC/root/.prettierignore"
printf 'keep.ts\nok.ts\n' > "$tC/base"
out=$(FORMAT_RATCHET_ROOT="$tC/root" FORMAT_RATCHET_BASELINE="$tC/base" bash "$SUT" 2>&1); rc=$?
juzgar 0 "$rc" 'un excluido que SÍ está formateado baja la base' "$out" 'puede BAJAR 1'
rm -rf "$tC"

echo "REGISTRO SIN CLASIFICAR — descartarlo en silencio es el bypass, no la higiene"
# ⛔ a repository gate, el MEDIUM del contraste (`…-format-ratchet-contrast.md:215-217`). Reproducido con
#    prettier 3.9.6 el 2026-08-16 ANTES de tocar el guion: con `old.ts` en la línea base y un
#    `new⏎line.ts` mal formateado, prettier saca `[warn] new` y `[warn] line.ts`, ninguna mitad
#    existe, las dos se descartaban y el trinquete decía «1 incumplidor(es) · nuevos 0 · ✔ la deuda
#    no sube» con **rc=0** — con el fichero sin formatear dentro del árbol ya certificado.
#
#    Casilla de árbol REAL a propósito: el filtro de existencia sólo corre sin señuelo, así que una
#    versión con listador inyectado no podría alcanzar esta guarda.
tD=$(mktemp -d); mkdir -p "$tD/root"
printf '%s\n' "$MAL" > "$tD/root/old.ts"
printf 'old.ts\n' > "$tD/base"
# ⛔ CONTROL NEGATIVO, Y VA PRIMERO: el mismo montaje sin el nombre partido tiene que PASAR. Sin
#    él, la casilla de abajo se cumpliría con un guion que contestara 2 a todo.
out=$(FORMAT_RATCHET_ROOT="$tD/root" FORMAT_RATCHET_BASELINE="$tD/base" bash "$SUT" 2>&1); rc=$?
juzgar 0 "$rc" 'el mismo árbol SIN el nombre partido pasa' "$out" 'la deuda no sube'
printf '%s\n' "$MAL" > "$tD/root/new"$'\n'"line.ts"
out=$(FORMAT_RATCHET_ROOT="$tD/root" FORMAT_RATCHET_BASELINE="$tD/base" bash "$SUT" 2>&1); rc=$?
juzgar 2 "$rc" 'un nombre con SALTO DE LÍNEA no se descarta' "$out" 'NO HE PODIDO MIRAR'
rm -rf "$tD"

echo "EL ÁRBOL SE MUEVE BAJO LA MEDIDA — tampoco eso es «no hay deuda»"
# ⛔ a repository gate, el LOW del contraste (`…-format-ratchet-contrast.md:222-225`): un formateador que borra
#    un fichero ENTRE su salida y el filtrado deja el mismo hueco que el nombre partido, porque es
#    el mismo descarte silencioso. Reproducido el 2026-08-16: rc=0 con «1/1», y restaurar `new.ts`
#    después del veredicto dejaba la deuda dentro del árbol ya certificado.
#
# ⛔ POR QUÉ ESTA CASILLA COPIA EL SUT: el guion resuelve el formateador por ruta ABSOLUTA desde el
#    repositorio de su propio fichero (`format-ratchet.sh:77-84`), así que no hay forma de
#    interponerse sin moverlo. La copia se hace EN TIEMPO DE EJECUCIÓN desde "$SUT", que es lo que
#    mantiene la casilla sensible a un mutante: lo que se muta es lo que se copia. Y el directorio
#    de la copia NO tiene `web/node_modules`, que es lo que hace caer la resolución al `npx` falso.
_npx_falso() { # <ruta del ejecutable> <borra new.ts entre salida y filtrado: si|no>
  cat > "$1" <<'FALSO'
#!/usr/bin/env bash
for a in "$@"; do [ "$a" = "--version" ] && { echo 3.9.6; exit 0; }; done
printf 'Checking formatting...\n[warn] old.ts\n[warn] new.ts\n[warn] Code style issues found in 2 files. Run Prettier with --write to fix.\n'
FALSO
  [ "$2" = "si" ] && printf 'rm -f new.ts\n' >> "$1"
  printf 'exit 1\n' >> "$1"
  chmod +x "$1"
  # ⛔ CONTROL POSITIVO DEL PROPIO SEÑUELO. Escribirlo y darle +x no prueba que se ejecute: bajo un
  #    montaje `noexec` devuelve 126, el SUT se cae al formateador REAL y esta casilla mide otra
  #    cosa CREYENDO que mide la suya. Se comprueba aquí, donde se puede acusar al fichero, y no
  #    en el veredicto, donde ya sería indistinguible de un fallo del sujeto.
  local _v
  _v=$("$1" --version 2>/dev/null)
  if [ "$_v" != "3.9.6" ]; then
    echo "format-ratchet self-test: ⛔ NO HE PODIDO MIRAR: el señuelo $1 no se ejecuta" >&2
    echo "   (--version devolvio '${_v}', rc=$?); sin el, el SUT mide con el prettier real." >&2
    exit 2
  fi
}
tE=$(mktemp -d); mkdir -p "$tE/scripts" "$tE/bin" "$tE/root"
cp "$SUT" "$tE/scripts/format-ratchet.sh"
printf '%s\n' "$MAL" > "$tE/root/old.ts"
printf '%s\n' "$MAL" > "$tE/root/new.ts"
# ⛔ CONTROL NEGATIVO OTRA VEZ PRIMERO, y aquí es imprescindible: sin él, la casilla de abajo la
#    cumpliría un `npx` falso que fallara por CUALQUIER razón —incluida no arrancar—, y yo estaría
#    midiendo mi propio arnés en vez de la guarda.
_npx_falso "$tE/bin/npx" no
printf 'new.ts\nold.ts\n' > "$tE/base"
out=$(PATH="$tE/bin:$PATH" FORMAT_RATCHET_ROOT="$tE/root" FORMAT_RATCHET_BASELINE="$tE/base" \
      bash "$tE/scripts/format-ratchet.sh" 2>&1); rc=$?
juzgar 0 "$rc" 'el mismo arnés sin el borrado pasa' "$out" 'la deuda no sube'
_npx_falso "$tE/bin/npx" si
printf 'old.ts\n' > "$tE/base"
out=$(PATH="$tE/bin:$PATH" FORMAT_RATCHET_ROOT="$tE/root" FORMAT_RATCHET_BASELINE="$tE/base" \
      bash "$tE/scripts/format-ratchet.sh" 2>&1); rc=$?
juzgar 2 "$rc" 'un fichero borrado durante la medida no se descarta' "$out" 'new.ts'
rm -rf "$tE"

echo "LA EXCLUSIÓN QUE CRECE — y el MISMO descarte silencioso, escondido en la re-medida"
# ⛔ TRES CASILLAS QUE FALTABAN, Y LAS ENCONTRÉ BORRANDO CÓDIGO, no leyéndolo. Verificando este
#    fichero el 2026-08-17: quitar el bloque ENTERO de la re-medida contra el `.prettierignore` del
#    base de confianza —1793 bytes, el TERCER hallazgo HIGH del contraste— dejaba la batería en
#    **26/26**. Desactivar sólo su veredicto (`_tapados -gt 0` → `-gt 999`), **26/26** también. Y
#    quitar sólo la negativa por registros sin clasificar que a repository gate/a repository gate añadió DENTRO de ese
#    bloque, **26/26**. Es decir: la guarda que destapa la deuda que una exclusión nueva esconde no
#    tenía ni un testigo, y la mitad de un arreglo declarado cerrado vivía en el mismo hueco.
#
#    Las tres necesitan un repositorio git de verdad (la guarda compara el fichero de exclusiones
#    con el del `BASE_REF`) Y un árbol REAL (el bloque se salta cuando hay listador inyectado), así
#    que el señuelo es lo uno y lo otro: commitea un `.prettierignore` vacío y lo amplía a `hid/`
#    sólo en el árbol. Lo que cambia entre las tres es únicamente QUÉ tapa esa ampliación.
senuelo_ignore() { # <limpio|tapa|partido> -> imprime el repositorio desechable
  local caso="$1" t; t=$(mktemp -d); mkdir -p "$t/root/hid"
  printf '%s\n' "$MAL" > "$t/root/keep.ts"        # se queda en el censo Y en la línea base
  printf 'keep.ts\n' > "$t/base.txt"
  : > "$t/root/.prettierignore"
  case "$caso" in
    limpio)  printf '%s\n' "$BIEN" > "$t/root/hid/x.ts" ;;
    tapa)    printf '%s\n' "$MAL"  > "$t/root/hid/x.ts" ;;
    partido) printf '%s\n' "$MAL"  > "$t/root/hid/new"$'\n'"line.ts" ;;
  esac
  (
    # HERMÉTICO A PROPÓSITO: un señuelo desechable no puede depender de la configuración git del
    # anfitrión. En los runners —que corren como root, y esta caja no— un `safe.directory`, un
    # hook global o una plantilla heredada bastan para que este `commit` no ocurra, y entonces el
    # SUT no tiene HEAD contra el que comparar y devuelve 0 donde el caso espera 1.
    cd "$t" && \
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    GIT_TEMPLATE_DIR= \
      git init -q -b main .
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
      git add -A >/dev/null 2>&1
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
      git commit -qm base >/dev/null 2>&1
  ) >/dev/null 2>&1
  # ⛔ EL MONTAJE SE COMPRUEBA, NO SE SUPONE. Si ese commit no crea HEAD, el SUT no tiene contra
  #    qué comparar y devuelve 0 — es decir, el señuelo «pasa» por no haberse construido, y el
  #    caso que espera 1 falla sin decir por qué. Medido el 2026-08-20: con HEAD el SUT da rc=1;
  #    sin HEAD da rc=0, que es exactamente lo que mainline-ci reportaba mientras aquí salía
  #    30/30. Un auto-test que silencia su propio montaje no mide el SUT, mide la suerte.
  # ⛔ Y NO BASTA CON QUE HAYA HEAD: el caso mide que la EXCLUSIÓN CRECIÓ, así que compara el
  #    `.prettierignore` del árbol contra el de HEAD. Si el fichero no llegó al commit —un
  #    `core.excludesFile` del anfitrión lo salta, y el `git add` no era hermético hasta hoy—,
  #    no hay crecimiento que ver: el SUT censa 1 incumplidor, dice «la deuda no sube» y
  #    devuelve 0. Reproducido el 2026-08-20 con la salida IDÉNTICA a la del runner.
  # Sin tubería a propósito: `… | grep -q` bajo `pipefail` devuelve 141 EN ÉXITO, y esta guarda
  # la escribí yo con esa forma hace media hora — la cazó `check-sigpipe-booleans` en el barrido.
  _pi_en_head="$(git -C "$t" ls-tree HEAD -- root/.prettierignore 2>/dev/null)"
  if git -C "$t" rev-parse --verify -q HEAD >/dev/null 2>&1 && [ -z "$_pi_en_head" ]; then
    printf 'test-format-ratchet: ⛔ NO HE PODIDO MIRAR: root/.prettierignore no llegó a HEAD en %s.\n' "$t" >&2
    printf '                     Sin él no hay crecimiento de exclusión que medir; el caso no juzga nada.\n' >&2
    : > "${RATCHET_SELFTEST_NOMIRAR:?}"
  fi
  if ! git -C "$t" rev-parse --verify -q HEAD >/dev/null 2>&1; then
    printf 'test-format-ratchet: ⛔ NO HE PODIDO MIRAR: el señuelo no creó HEAD en %s.\n' "$t" >&2
    printf '                     git init/commit falló en este entorno; el caso no es concluyente.\n' >&2
    rm -rf "$t"
    # `exit` aquí sólo saldría de la sustitución de órdenes, así que el veredicto se deja en un
    # testigo en disco y lo recoge el final del fichero: la diferencia entre «no pude mirar» (2)
    # y «he mirado y está mal» (1) es justo la que este arreglo existe para no perder.
    : > "${RATCHET_SELFTEST_NOMIRAR:?}"
    printf ''
    return 2
  fi
  printf 'hid/\n' > "$t/root/.prettierignore"     # la exclusión CRECE, y sólo en el árbol
  printf '%s' "$t"
}
correr_ignore() { # <dir del señuelo> -> imprime la salida; el rc es el del SUT
  ( cd "$1" && FORMAT_RATCHET_ROOT=root FORMAT_RATCHET_BASELINE=base.txt \
      FORMAT_RATCHET_BASE_REF=HEAD bash "$SUT" 2>&1 )
}
# ⛔ CONTROL NEGATIVO PRIMERO: la exclusión crece IGUAL, pero lo que tapa está formateado de verdad.
#    Sin él, las dos casillas de abajo se cumplirían prohibiendo TODO cambio del fichero de
#    exclusiones — es decir, castigando a quien recorta la superficie excluida.
tF=$(senuelo_ignore limpio); out=$(correr_ignore "$tF"); rc=$?
juzgar 0 "$rc" 'la exclusión crece sobre algo YA formateado: pasa' "$out" 'la deuda no sube'
rm -rf "$tF"
tG=$(senuelo_ignore tapa); out=$(correr_ignore "$tG"); rc=$?
juzgar 1 "$rc" 'una exclusión nueva que TAPA deuda es ROJO' "$out" 'hid/x.ts'
rm -rf "$tG"
# ⛔ Y ÉSTA ES LA MITAD DE a repository gate/a repository gate QUE NADIE MIRABA: el nombre partido va DENTRO de lo que la
#    ampliación excluye, así que el censo del árbol no lo ve nunca y sólo la re-medida lo nombra.
#    El fragmento exigido es «con el .prettierignore de HEAD» a propósito: es lo único que distingue
#    esta negativa de la del censo —que dice «midiendo el árbol»—, y sin exigirlo la casilla la
#    cumpliría la guarda de la otra medida, que ya tiene su propio testigo.
tH=$(senuelo_ignore partido); out=$(correr_ignore "$tH"); rc=$?
juzgar 2 "$rc" 'un registro sin clasificar en la RE-MEDIDA' "$out" 'con el .prettierignore de HEAD'
rm -rf "$tH"

printf '\nformat-ratchet self-test: %d pasan, %d fallan\n' "$ok" "$fallos"
if [ -e "$RATCHET_SELFTEST_NOMIRAR" ]; then
  rm -f "$RATCHET_SELFTEST_NOMIRAR"
  printf 'test-format-ratchet: ⛔ veredicto = NO HE PODIDO MIRAR (2): el montaje de al menos un\n' >&2
  printf '                     señuelo no se construyó, así que sus casos no juzgan al SUT.\n' >&2
  exit 2
fi
[ "$fallos" -eq 0 ]
