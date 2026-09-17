#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Banco de `cloud/control-plane/deploy/roles-oneshot.sh`, el entrypoint de la imagen
# `cloud-roles`.
#
# ⛔ CORRE SIN DOCKER Y SIN POSTGRES, y ésa es la razón de que exista aquí y no como una prueba
# de integración: lo que este entrypoint tiene que garantizar es que **NO invoca psql cuando
# falta una credencial** y que **comprueba el resultado cuando sí lo invoca**. Las dos son
# propiedades del control de flujo, no de la base de datos, así que se prueban con un `psql`
# simulado que registra sus argumentos — y probarlas contra un Postgres real las haría más
# lentas y no más ciertas.
#
# ⛔⛔ CORREGIDO EL 2026-09-02: aquí decía que el SQL «AVISA Y SALE CON CÓDIGO 0», y eso es
# FALSO en este árbol. Se curó el 2026-08-17 (`280326b45`) y hoy sus diez guardas ejecutan
# `SELECT 1/0` bajo `ON_ERROR_STOP`, así que falla con código distinto de cero. Lo levantó el
# contraste `sol max` (F-10); yo había leído el COMENTARIO del SQL —que explica por qué existe
# la cura— como si describiera el comportamiento actual.
#
# **Lo que este banco prueba sigue siendo lo mismo, y por eso los casos no cambian:** que el
# lanzador para ANTES de conectar y nombrando lo que falta, en vez de conectarse como
# superusuario para recibir un error de división por cero; que casa cada rol con su URL; y que
# comprueba el resultado después. Ninguna de las tres las puede hacer el SQL.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)"
SUT="$ROOT/cloud/control-plane/deploy/roles-oneshot.sh"
SQL="$ROOT/cloud/control-plane/deploy/cloud-control-roles.sql"
[ -r "$SUT" ] || { echo "roles-oneshot selftest: COULD NOT LOOK — no está $SUT" >&2; exit 2; }
[ -r "$SQL" ] || { echo "roles-oneshot selftest: COULD NOT LOOK — no está $SQL" >&2; exit 2; }

_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/roles-oneshot.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()  { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# ── El `psql` simulado ───────────────────────────────────────────────────────
# Registra CADA invocación con sus argumentos, y contesta al recuento de `pg_roles` con lo que
# le diga `FAKE_ROLE_COUNT`. Así el caso del recuento se puede forzar sin una base de datos.
# ⛔ DEVUELVE NOMBRES, NO UNA CUENTA. El sujeto comparaba cardinalidades y ahora compara el
# CONJUNTO de nombres —un rol esperado que falte lo compensaba cualquier otro con el mismo
# prefijo, por ejemplo uno rancio (contraste `sol max`, F-03)—, asi que el simulado tiene que
# hablar el mismo idioma. `FAKE_ROLE_NAMES` permite a un caso quitar uno o meter uno de mas.
#
# ⚠ Y REGISTRA `"$@"` CON SEPARADOR, no `"$*"`: con `$*` los argumentos se pegan con espacios y
# una contrasena que CONTENGA un espacio se lee igual que dos argumentos — que es justo el
# defecto F-02 que este banco tiene que poder ver. Un registro que borra la frontera entre
# argumentos no puede testificar sobre ella.
# ⛔ Y DENTRO USA ${VAR-def}, NO ${VAR:-def}: con los dos puntos una variable VACIA tambien cae
# al valor por defecto, asi que el caso «psql devuelve cero roles» recibia los once y no
# disparaba. Sin ellos, una cadena vacia definida es una cadena vacia.
#
# The fixture body is a quoted heredoc: comments and shell expressions remain data when
# it is generated. Only its two per-case inputs are emitted as Bash-quoted assignments.
mkpsql() { # mkpsql <nombres-de-rol-separados-por-espacio>
  mkdir -p "$TMP/bin"
  {
    printf '#!/usr/bin/env bash\nfixture_calls=%q\nfixture_roles=%q\n' "$TMP/psql-calls" "$1"
    cat <<'PSQL'
{ printf 'CALL'; for a in "$@"; do printf ' <%s>' "$a"; done; printf '\n'; } >> "$fixture_calls"
# La sonda de LOGIN se distingue de la del censo por su consulta, y el rol se saca de la URL
# que la propia sonda pasa: desde que el sujeto se conecta con la URL ORIGINAL —para no
# reconstruir la credencial con la misma funcion que la instalo— ya no hay `PGUSER` que mirar.
#
# ⛔ Y ESE DETALLE COSTO SIETE CASOS: la version anterior hacia
# `case " ${FAKE_LOGIN_FAIL-} " in *" ${PGUSER-} "*)`, y con las DOS variables vacias el patron
# `*"  "*` casa con la cadena `"  "` — asi que rechazaba TODOS los logins y el camino feliz
# salia en rojo. Un comodin sobre un valor ausente casa con todo.
for a in "$@"; do
  if [ "$a" = "SELECT 1" ]; then
    _who=""
    for b in "$@"; do
      case "$b" in postgres://*) _who="${b#postgres://}"; _who="${_who%%:*}" ;; esac
    done
    # `FAKE_ALL_DOWN` tira TODA conexion, incluida la del master: es como se prueba que una
    # indisponibilidad se distingue de una credencial mala.
    [ -n "${FAKE_ALL_DOWN-}" ] && exit 2
    if [ -n "${FAKE_LOGIN_FAIL-}" ] && [ -n "$_who" ]; then
      case " ${FAKE_LOGIN_FAIL} " in *" ${_who} "*) exit 2 ;; esac
    fi
    printf '1\n'; exit 0
  fi
done
for a in "$@"; do
  if [ "$a" = "-tAq" ]; then
    for r in ${FAKE_ROLE_NAMES-$fixture_roles}; do printf '%s\n' "$r"; done
    exit 0
  fi
done
exit 0
PSQL
  } > "$TMP/bin/psql"
  chmod +x "$TMP/bin/psql"
}

# Los nombres que el SQL crea, derivados de el y no tecleados.
created_roles() {
  sed -n 's/^[[:space:]]*CREATE ROLE \([a-z_][a-z0-9_]*\).*/\1/p' "$SQL" | sort -u | tr '\n' ' '
}

# Las diez URLs que un `cloud-cp-databases` bien puesto produce, derivadas del PROPIO SQL para
# que este banco no lleve una lista que caduque — el mismo principio que el sujeto.
roles_from_sql() {
  # Misma normalización que el sujeto —`\n` y `\t` a espacio, y luego squeeze— a propósito: si
  # este helper derivara nueve donde el sujeto deriva diez, C-00 lo diría en voz alta, pero
  # divergir aquí sería un acertijo para quien lea el banco dentro de tres meses.
  tr '\n\t' '  ' < "$SQL" | tr -s ' ' | sed -n "s/ALTER ROLE /\n&/gp" \
    | sed -n "s/^ALTER ROLE \([a-z_][a-z0-9_]*\) WITH LOGIN PASSWORD :'\([a-z_][a-z0-9_]*\)'.*/\1 \2/p"
}

# Nombre de variable de entorno para un rol: DATABASE_<ALGO>_URL. El nombre concreto da igual —
# el sujeto casa por USUARIO de la URL, no por el nombre de la variable, y este banco lo
# aprovecha para probar justamente eso.
env_for() { printf 'DATABASE_%s_URL' "$(printf '%s' "$1" | tr 'a-z' 'A-Z')"; }

# Prepara el entorno completo y correcto en un fichero que cada caso puede modificar.
base_env() {
  : > "$TMP/env.sh"
  {
    echo "PGMASTER_USER=olivares_admin"
    echo "PGMASTER_PASSWORD=masterpw"
  } >> "$TMP/env.sh"
  while IFS=' ' read -r role _var; do
    [ -n "$role" ] || continue
    printf '%s=postgres://%s:pw-%s@db.internal:5432/cloudcp\n' "$(env_for "$role")" "$role" "$role" >> "$TMP/env.sh"
  done < <(roles_from_sql)
}

run() { # run  → deja rc en $TMP/rc, stderr+stdout en $TMP/out
  : > "$TMP/psql-calls"
  local rc=0
  ( set -a; . "$TMP/env.sh"; set +a
    PATH="$TMP/bin:$PATH" OLIVARES_ROLES_SQL="$SQL" sh "$SUT" ) > "$TMP/out" 2>&1 || rc=$?
  printf '%s\n' "$rc" > "$TMP/rc"
}

expect() { # expect <rc> <trozo-de-frase|""> <rótulo>
  local want="$1" needle="$2" label="$3" got
  run
  got="$(cat "$TMP/rc")"
  if [ "$got" != "$want" ]; then
    bad "$label — rc=$got, want $want ($(head -c 300 "$TMP/out"))"
    return
  fi
  if [ -n "$needle" ] && ! command grep -qF -- "$needle" "$TMP/out"; then
    bad "$label — rc=$want pero el mensaje no nombra su guarda; salió: $(head -c 300 "$TMP/out")"
    return
  fi
  ok "$label"
}

N_ROLES="$(roles_from_sql | wc -l | tr -d ' ')"
ALL_ROLES="$(created_roles)"

# ── C-00 · El camino feliz, y lo que tiene que llevar la invocación ──────────
mkpsql "$ALL_ROLES"; base_env
expect 0 "OK" "el camino feliz provisiona y verifica"

# ⛔ Y NO BASTA CON EL rc: hay que ver que la invocación llevó UN `-v` por rol. Sin esto, un
# entrypoint que llamara a psql con NUEVE credenciales fallaría por la décima —el SQL falla— y
# el mensaje vendría del servidor, no de aquí: el rc sería el mismo y el diagnóstico, peor.
if [ "$(command grep -c -- '<-f>' "$TMP/psql-calls" || true)" -ge 1 ]; then
  _vs="$(command grep -- '<-f>' "$TMP/psql-calls" | command grep -o -- '<cloud_cp_[a-z_]*_password=' | sort -u | wc -l | tr -d ' ')"
  if [ "$_vs" = "$N_ROLES" ]; then
    ok "la invocación de psql lleva las $N_ROLES credenciales de rol"
  else
    bad "la invocación de psql lleva $_vs credenciales y el SQL exige $N_ROLES"
  fi
else
  bad "el camino feliz no llegó a invocar psql con -f"
fi

# ⛔ Y LA PAREJA IRREGULAR, que es la que un mapa escrito a mano se come: el rol
# `cloud_cp_sweeper_ro` recibe su contraseña en `cloud_cp_sweeper_password`, SIN el `_ro`,
# mientras `cloud_cp_admin_ro` la recibe en `cloud_cp_admin_ro_password`, CON él.
if command grep -q -- '<cloud_cp_sweeper_password=' "$TMP/psql-calls" \
   && command grep -q -- '<cloud_cp_admin_ro_password=' "$TMP/psql-calls"; then
  ok "la pareja IRREGULAR rol↔variable se respeta (sweeper_ro→sweeper, admin_ro→admin_ro)"
else
  bad "la pareja irregular no se respeta: $(command grep -o -- '<cloud_cp_[a-z_]*=' "$TMP/psql-calls" | tr '\n' ' ')"
fi

# ── C-01 · UNA credencial de menos. Es el defecto entero, en su forma mínima ─
mkpsql "$ALL_ROLES"; base_env
sed -i "/^$(env_for cloud_cp_billing)=/d" "$TMP/env.sh"
expect 1 "ninguna DATABASE_*_URL trae ese" "una credencial de menos para ANTES de invocar psql"
if [ -s "$TMP/psql-calls" ]; then
  bad "con una credencial de menos NO se debe invocar psql, y se invocó"
else
  ok "con una credencial de menos psql no llega a invocarse"
fi

# ── C-02 · Ninguna credencial: el caso con el que nació el defecto ──────────
mkpsql "$ALL_ROLES"; base_env
sed -i '/^DATABASE_/d' "$TMP/env.sh"
expect 1 "no llega ninguna DATABASE" "sin ninguna URL para, y lo dice"

# ── C-03 · Una URL que no casa con ningún rol del SQL ────────────────────────
mkpsql "$ALL_ROLES"; base_env
echo 'DATABASE_GHOST_URL=postgres://cloud_cp_ghost:pw@db.internal:5432/cloudcp' >> "$TMP/env.sh"
expect 1 "no declara ningun rol con ese" "una URL que ningún rol reclama es un hallazgo"

# ── C-04 · Las URLs describen DOS bases distintas ───────────────────────────
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=postgres://[^@]*@\)db.internal:5432/cloudcp|\1otra.internal:5432/cloudcp|" "$TMP/env.sh"
expect 1 "tienen que describir una sola base" "URLs que apuntan a dos bases es un hallazgo"

# ── C-05 · Sin la credencial del master ─────────────────────────────────────
mkpsql "$ALL_ROLES"; base_env
sed -i '/^PGMASTER_USER=/d' "$TMP/env.sh"
expect 1 "falta PGMASTER_USER" "sin la credencial del master para antes de conectarse"

# ── C-06 · Una URL sin contraseña ───────────────────────────────────────────
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_tenant)=postgres://cloud_cp_tenant\):[^@]*@|\1:@|" "$TMP/env.sh"
expect 1 "no trae contrasena" "una URL sin contraseña es un hallazgo"

# ── C-07 · ⛔ EL RECUENTO POSTERIOR, que es lo que separa esto de un psql a pelo.
#           psql sale 0 —el SQL siempre sale 0— y los roles NO están.
mkpsql "$ALL_ROLES"; base_env
echo "FAKE_ROLE_NAMES=" >> "$TMP/env.sh"
expect 1 "estos roles NO estan" "psql en verde con cero roles creados es un hallazgo"

# ── C-08 · Y su dirección de NO disparo: el recuento correcto no dispara ────
mkpsql "$ALL_ROLES"; base_env
echo "FAKE_ROLE_NAMES=\"$ALL_ROLES\"" >> "$TMP/env.sh"
expect 0 "OK" "el recuento correcto NO dispara (dirección de no disparo)"

# ── C-09 · Contraseña con `%XX` y con `@`, que es donde un parser ingenuo cae ─
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=postgres://cloud_cp_billing\):[^@]*@|\1:p%40ss%3Aword@|" "$TMP/env.sh"
run
if [ "$(cat "$TMP/rc")" = 0 ] && command grep -qF -- '<cloud_cp_billing_password=p@ss:word>' "$TMP/psql-calls"; then
  ok "una contraseña con %XX se decodifica antes de pasarla a psql"
else
  bad "la contraseña con %XX no se decodificó: $(command grep -o -- '<cloud_cp_billing_password=[^>]*>' "$TMP/psql-calls" || echo '(no aparece)')"
fi

# ── C-12 · ⛔ UNA CONTRASENA CON ESPACIO, que es el defecto F-02 del contraste y un falso verde
#           PRESENTE hasta hoy. `%20` es codificacion valida en una URI de PostgreSQL; con el
#           `psql … $ARGS` sin comillas de la primera version, `alpha%20cloudcp` llegaba como
#           `…=alpha` MAS un argumento suelto `cloudcp`, que psql toma como NOMBRE DE BASE. La
#           contrasena quedaba truncada y la conexion podia ir a otra base. Ninguna contrasena
#           de este banco llevaba espacio, y por eso no lo vio.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=postgres://cloud_cp_billing\):[^@]*@|\1:alpha%20cloudcp@|" "$TMP/env.sh"
run
if [ "$(cat "$TMP/rc")" = 0 ] \
   && command grep -qF -- '<cloud_cp_billing_password=alpha cloudcp>' "$TMP/psql-calls"; then
  ok "una contraseña con espacio viaja como UN argumento (F-02)"
else
  bad "la contraseña con espacio se partió: $(command grep -o -- '<cloud_cp_billing_password=[^>]*>' "$TMP/psql-calls" || echo '(no aparece)')"
fi

# ── C-13 · Y un glob, que se habría expandido contra el directorio por la misma vía.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_tenant)=postgres://cloud_cp_tenant\):[^@]*@|\1:a%2Ab@|" "$TMP/env.sh"
run
if command grep -qF -- '<cloud_cp_tenant_password=a*b>' "$TMP/psql-calls"; then
  ok "una contraseña con un glob viaja literal, sin expandirse"
else
  bad "el glob se expandió o se perdió: $(command grep -o -- '<cloud_cp_tenant_password=[^>]*>' "$TMP/psql-calls" || echo '(no aparece)')"
fi

# ── C-14 · ⛔ EL CONJUNTO, NO LA CUENTA (F-03). Un rol esperado que falta, COMPENSADO por otro
#           con el mismo prefijo: la cardinalidad cuadra y el rol no está. La version que
#           contaba salia en verde justo aqui.
mkpsql "$ALL_ROLES"; base_env
_swapped="$(printf '%s' "$ALL_ROLES" | sed 's/cloud_cp_billing /cloud_cp_ranciodeotrointento /')"
echo "FAKE_ROLE_NAMES=\"$_swapped\"" >> "$TMP/env.sh"
expect 1 "estos roles NO estan: cloud_cp_billing" \
  "un rol que falta compensado por otro del mismo prefijo es un hallazgo (no basta la cuenta)"

# ── C-15 · ⛔ UN PARAMETRO QUE CAMBIA EL DESTINO. `?host=` lo honra el cliente del runtime
#           (pgx), y la primera version de este guion tiraba la query entera en silencio: se
#           provisionaba un servidor y el runtime arrancaba contra otro, con este paso diciendo
#           OK. Es el peor sintoma posible de un falso verde (contraste `sol max`).
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=.*cloudcp\)$|\1?host=otro.internal|" "$TMP/env.sh"
expect 1 "puede cambiar el servidor o la base" \
  "un parametro de consulta que puede mover el destino es un hallazgo"

# ── C-16 · Y la direccion de NO disparo: `sslmode` no mueve el destino y no debe rechazarse.
#           Sin este caso, la lista blanca podria estrecharse hasta romper URLs legitimas.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=.*cloudcp\)$|\1?sslmode=require|" "$TMP/env.sh"
expect 0 "OK" "sslmode NO se rechaza (dirección de no disparo de la lista blanca)"

# ── C-17 · Multi-host: libpq elige uno y este paso no puede saber cual.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|@db.internal:5432/cloudcp|@db.internal,replica.internal:5432/cloudcp|" "$TMP/env.sh"
expect 1 "declara varios hosts" \
  "una URL con varios hosts es un hallazgo"

# ── C-18 · Un `%` que no abre un octeto valido: libpq lo rechaza y la version anterior lo
#           dejaba pasar tal cual, cambiando la contrasena en silencio.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_tenant)=postgres://cloud_cp_tenant\):[^@]*@|\1:100%pura@|" "$TMP/env.sh"
expect 1 "no abre un octeto valido" \
  "una codificacion porcentual mal formada es un hallazgo"

# ── C-19 · ⛔ «EXISTE» NO ES «SIRVE» (F-05, la mitad larga). Un rol creado, presente en el censo
#           y sin LOGIN —o con la contrasena equivocada, o sin CONNECT a esa base— deja el plano
#           de control sin arrancar EXACTAMENTE igual que si no existiera. Y la contrasena es
#           ademas la unica prueba real de que los `-v` llegaron intactos: el defecto F-03
#           instalaba una TRUNCADA y todo lo demas salia bien.
mkpsql "$ALL_ROLES"; base_env
echo 'FAKE_LOGIN_FAIL="cloud_cp_billing"' >> "$TMP/env.sh"
expect 1 "NO conecta" \
  "un rol que existe pero no conecta con su contraseña es un hallazgo"

# ── C-20 · Y que la sonda se haga DE VERDAD, una por rol de login: si no se invocara, el caso
#           de arriba pasaria por no haber preguntado.
mkpsql "$ALL_ROLES"; base_env
run
_probes="$(command grep -c -- '<SELECT 1>' "$TMP/psql-calls" || true)"
if [ "$_probes" = "$N_ROLES" ]; then
  ok "se prueba el login de los $N_ROLES roles, uno por uno"
else
  bad "se hicieron $_probes sondas de login y hay $N_ROLES roles de login"
fi

# ── C-21 · ⛔⛔ EL FALSO VERDE QUE LA SONDA DEBIA EXCLUIR Y CONFIRMABA. Medido por el contraste
#           `sol max` sobre este mismo banco: una contrasena terminada en `%0A` se instalaba
#           TRUNCADA —la sustitucion de comandos POSIX se come los saltos FINALES— y la sonda,
#           que reconstruia la credencial con la MISMA funcion, mandaba el mismo valor truncado
#           y se daba la razon. El runtime parsea la URL entera y usa `alpha\n`. rc=0, «los 10
#           de login conectan», y el rol con otra contrasena.
#
#           Dos curas, y este caso las cubre las dos: se RECHAZA lo que el shell no puede
#           transportar, y la sonda se conecta con la URL ORIGINAL en vez de reconstruirla.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=postgres://cloud_cp_billing\):[^@]*@|\1:alpha%0A@|" "$TMP/env.sh"
expect 1 "lleva un salto de linea" \
  "una contraseña con salto de linea se rechaza en vez de instalarse truncada"

# ── C-22 · Y el porcentaje mal formado que la validacion no veia porque miraba el valor YA
#           DECODIFICADO: `good%25abc%zz` decodifica a `good%abc%zz`, cuyo `%ab` satisfacia la
#           comprobacion mientras el `%zz` de la URL original sigue roto — y es esa URL la que
#           parsea el runtime.
mkpsql "$ALL_ROLES"; base_env
sed -i "s|^\($(env_for cloud_cp_billing)=postgres://cloud_cp_billing\):[^@]*@|\1:good%25abc%zz@|" "$TMP/env.sh"
expect 1 "no abre un octeto valido" \
  "un % mal formado tras uno bien formado es un hallazgo (se valida la URL, no lo decodificado)"

# ── C-23 · ⛔ Y LA SONDA TIENE QUE USAR LA URL, no una reconstruccion: es lo unico que impide
#           que dos errores identicos coincidan. Se comprueba en la invocacion.
mkpsql "$ALL_ROLES"; base_env
run
if command grep -q -- '<postgres://cloud_cp_billing:' "$TMP/psql-calls" \
   && command grep -q -- '<SELECT 1>' "$TMP/psql-calls"; then
  ok "la sonda de login se conecta con la URL ORIGINAL, no con una reconstrucción"
else
  bad "la sonda no lleva la URL original: $(command grep -o -- '<postgres://[^>]*>' "$TMP/psql-calls" | head -1 || echo '(ninguna)')"
fi

# ── C-24 · ⛔ UNA CAIDA NO ES UN HALLAZGO. Un fallo de conexion puede ser la credencial —que es
#           lo que este paso responde— o la RED, el DNS, el TLS o un servidor que se fue.
#           Devolver 1 en los dos casos convierte una indisponibilidad en un defecto del
#           trabajo (contraste `sol max`, B-05). El discriminador: si el MASTER tampoco entra,
#           lo que cambio es el camino y no el rol.
mkpsql "$ALL_ROLES"; base_env
echo 'FAKE_ALL_DOWN=1' >> "$TMP/env.sh"
expect 2 "no he podido comprobar el login de" \
  "si nada conecta, la respuesta es 2 «no he podido mirar», no 1"

# ── C-25 · Y la direccion contraria, que es la que da valor a la de arriba: si el master SI
#           entra y el rol no, el rol es el problema y eso sigue siendo 1.
mkpsql "$ALL_ROLES"; base_env
echo 'FAKE_LOGIN_FAIL="cloud_cp_tenant"' >> "$TMP/env.sh"
expect 1 "y el master SI" \
  "si el master entra y el rol no, sigue siendo un hallazgo (1, no 2)"

# ── C-10 · Sin psql: es «no he podido mirar», no un hallazgo ────────────────
#
# ⛔ NO BASTA CON BORRAR EL SIMULADO: esta caja trae un `psql` REAL (18.4 en /usr/bin), asi que
# el sujeto lo encontraba, intentaba conectar a `db.internal` y el caso salia 1 en vez de 2 —
# probando el fallo de red, no la ausencia de la herramienta. Se construye un PATH minimo con
# enlaces SOLO a lo que el sujeto necesita, y psql no esta entre ellos.
mkdir -p "$TMP/nopsql"
for _t in sh sed tr awk grep wc cut sort env printf cat; do
  _p="$(command -v "$_t" || true)"
  [ -n "$_p" ] && ln -sf "$_p" "$TMP/nopsql/$_t"
done
if [ -x "$TMP/nopsql/sed" ] && [ ! -e "$TMP/nopsql/psql" ]; then
  base_env
  _rc=0
  ( set -a; . "$TMP/env.sh"; set +a
    PATH="$TMP/nopsql" OLIVARES_ROLES_SQL="$SQL" sh "$SUT" ) > "$TMP/out" 2>&1 || _rc=$?
  if [ "$_rc" = 2 ] && command grep -qF "no hay psql" "$TMP/out"; then
    ok "sin psql la respuesta es 2, no 1 ni 0"
  else
    bad "sin psql la respuesta fue $_rc: $(head -c 300 "$TMP/out")"
  fi
else
  bad "no he podido construir el PATH sin psql"
fi

# ── C-11 · ⛔ LA GUARDA QUE ESCRIBIÓ MI PROPIO FALLO: si el patrón de ALTER ROLE
#           deja de casar y pierde una pareja, esto para. Sin ella, provisionar de
#           menos habría salido en verde — y perdí una de diez escribiendo esto.
mkpsql "$ALL_ROLES"; base_env
# ⛔ EL MUTANTE TUVO QUE CAMBIAR, Y ESO ES PARTE DEL CASO. Empezo siendo espaciado doble, y
# dejo de morder en cuanto la extraccion aprendio a normalizar espacios y tabuladores — un
# mutante que el sujeto ya resiste no prueba la guarda, la adorna. SQL es insensible a
# mayusculas, asi que un `alter role` en minusculas es una deriva REAL que el patron no casa,
# y es la que se usa ahora.
sed "s/^ALTER ROLE cloud_cp_billing WITH LOGIN/alter role cloud_cp_billing with login/" "$SQL" > "$TMP/mutant.sql"
if command grep -q '^alter role cloud_cp_billing' "$TMP/mutant.sql"; then
  rc=0
  ( set -a; . "$TMP/env.sh"; set +a
    PATH="$TMP/bin:$PATH" OLIVARES_ROLES_SQL="$TMP/mutant.sql" sh "$SUT" ) > "$TMP/out" 2>&1 || rc=$?
  # El SQL mutado sigue teniendo su guarda `\if`, así que la pareja perdida se detecta.
  if [ "$rc" = 2 ] && command grep -qF "no cubre la guarda" "$TMP/out"; then
    ok "una extracción que pierde una pareja para con «no he podido mirar»"
  else
    bad "la extracción incompleta no paró — rc=$rc: $(head -c 300 "$TMP/out")"
  fi
else
  bad "el mutante de espaciado no se aplicó al SQL"
fi

printf 'roles-oneshot selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
