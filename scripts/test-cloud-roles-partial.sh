#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# T3-A / pg-roles-cloud — testigo de la degradacion honesta (NOT APPLICABLE) de #2155.
#
# QUE MIDE, y por que existe. La mainline VIAJA en el export del espejo preprod, y alli el paso
# «provision cloud control-plane roles» moria con `psql: error: cloud/control-plane/deploy/
# cloud-control-roles.sql: No such file or directory`. La curacion del export retira el MODULO
# entero, y no puede no hacerlo: el fichero lleva cabecera `LicenseRef-Olivares-Commercial` y la
# frontera de licencia es regla dura. Un arbol publico sin roles de control-plane no es un fallo.
#
# LA DISTINCION QUE ESTA BATERIA PROTEGE, y es la razon de que existan tres casos y no dos: una
# guarda `[ ! -f el.sql ]` daria PARTIAL tambien cuando el modulo SI esta y alguien borro o
# renombro el guion — taparia un defecto real con la excusa del export. Por eso la guarda mira el
# DIRECTORIO, y por eso el caso C exige que con el modulo presente y el fichero ausente el paso
# SIGA MURIENDO. Un mutante que cambia la guarda a `-f` pasa los casos A y B y muere en el C: ese
# mutante es el punto entero del fichero.
#
# El bloque `run:` no se transcribe: se EXTRAE del workflow y se ejecuta. Una copia a mano mide la
# copia.
#
# CCI1 (2026-09-10) EXTENDS THIS BENCH TO THE DATABASE BOUNDARY, and the reason is that the
# previous stub made that extension impossible to test. It answered "1" to every `-tAc` and 0 to
# everything else, so any check the step might add -- CREATE target, catalog identity, ACL
# equality, OID stability, DSN path -- would have passed in case B whatever the step actually
# did. A mock that always agrees proves the mock. The stub now classifies the REAL invocation
# (create, maintenance-acl, target-identity, bootstrap-file, cluster-role, target-objects,
# maintenance-objects, database-settings), refuses what it cannot classify, keeps an operation
# log of kind/database-path/order/status, and models the one piece of state that decides the
# postconditions: WHICH database received the bootstrap. That is what lets a misrouted `-f` be
# rejected by the step's own target validation instead of by a stub that declined to answer.
#
# CORRECTION 1 (2026-09-10) ADDS THE DECODER SEAM. Root's owned probe showed that the inline
# validator read observations with errors="replace", so two ACL observations differing in one
# byte -- 0xFF against 0xFE -- decoded to the same U+FFFD string and the maintenance comparison
# passed on inputs whose bytes differ. The reader now decodes strict UTF-8. The controls below
# feed root's retained probe bytes to the REAL extracted step, check what the step actually
# read against root's recorded digests, and keep a mutant that puts replacement decoding back.
#
# WHAT THIS BENCH DOES NOT PROVE: PostgreSQL semantics. No ACL is evaluated, no database is
# created and no privilege is revoked here. It proves caller wiring, ordering and failure
# routing. Real catalogs belong to a separate runtime qualification.
#
# Contrato de salida: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)

# export-closure: hub-only cloud/control-plane/deploy/cloud-control-roles.sql — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
# ⛔ Y ANTES DEL «no he podido mirar», LA TERCERA RESPUESTA QUE FALTABA: en el arbol
# PUBLICADO ese fichero no esta AUSENTE POR ERROR — esta CURADO FUERA, a proposito. Un rc=2
# alli es correcto como «no he mirado» y es ruido como veredicto: la pata no tiene sujeto y
# nunca lo tendra. Se distingue con el clasificador de hub-leg.sh —firma del generador MAS
# ausencia de todo camino hub-only—, no con un fichero-marcador suelto, porque un marcador a
# pelo es una contraseña que cualquier copia teclea. Misma plantilla que
# check-int-12-no-land.sh:37 y check-gate-parity.sh:346.
if [ ! -f "$ROOT"/cloud/control-plane/deploy/cloud-control-roles.sql ] \
   && [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
	printf '%s\n' "test-cloud-roles-partial: SCOPED — public export; cloud/control-plane is curated out."
	printf '%s\n' "  El modulo no viaja, asi que aqui no hay degradacion PARTIAL que medir. En el hub SI se mide."
	exit 0
fi
if [ ! -f "$ROOT"/cloud/control-plane/deploy/cloud-control-roles.sql ]; then
	printf '%s\n' "test-cloud-roles-partial: COULD NOT LOOK — cloud/control-plane/deploy/cloud-control-roles.sql is not in this tree" >&2
	exit 2
fi
WF="$ROOT/.github/workflows/mainline-ci.yml"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/cloud-roles.XXXXXX") || { echo "no pude crear el area"; exit 2; }
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok(){ printf 'ok    %s\n' "$1"; pass=$((pass+1)); }
no(){ printf 'FAIL  %s\n' "$1"; fail=$((fail+1)); }
cannot(){ printf 'NO HE PODIDO MIRAR: %s\n' "$1"; exit 2; }

command -v python3 >/dev/null 2>&1 || cannot "sin python3 no leo el YAML"
PEEK="$ROOT/scripts/lib/ci-yaml-peek.py"
[ -r "$PEEK" ] || cannot "falta scripts/lib/ci-yaml-peek.py, que es como leo estos YAML"
# Sin PyYAML a proposito: ver la cabecera de ci-yaml-peek.py — el runner de este job es
# autoalojado y nada en el arbol demuestra que la biblioteca se alcance alli.

step_field() { # <fichero> <id> <campo>  ; rc 3 = el paso no esta
  python3 "$PEEK" step-field "$1" control-plane "$2" "$3"
}
notice_if() { # el `if:` del paso de aviso, buscado por nombre (no tiene id)
  python3 "$PEEK" step-if-byname "$1" control-plane 'NOT APPLICABLE notice'
}

# ---------------------------------------------------------------- banco de pruebas

mk_stubs() {
  local b="$1/stubs"; mkdir -p "$b" || return 1
  cat > "$b/psql" <<'STUB'
#!/bin/sh
# Stub psql. It classifies the ACTUAL invocation and refuses what it cannot classify: an
# unknown invocation is a failure, never a silent success. It never records an argument of
# `-v` (those carry the generated passwords) and never records a whole URI.
set -u
LOG="${CP_STUB_LOG:?stub psql needs CP_STUB_LOG}"
MODE="${CP_STUB_MODE:-normal}"
[ -f "$LOG" ] || : > "$LOG"

SEQ=$(( $(wc -l < "$LOG") + 1 ))
record() { # <kind> <database path> <rc>
  printf '%s %s %s rc=%s\n' "$SEQ" "$1" "$2" "$3" >> "$LOG"
}
# A byte-exact copy of what this invocation answered, so the bench can check WHAT THE STEP
# READ and not merely what the bench meant to send. It carries catalog projections only --
# never a -v argument, never a URI. Buffered and replayed at exit because dash has no
# process substitution to tee through.
if [ -n "${CP_STUB_EMIT:-}" ]; then
  mkdir -p "$CP_STUB_EMIT" 2>/dev/null || :
  EMIT="$CP_STUB_EMIT/$SEQ.out"
  exec 9>&1
  exec > "$EMIT"
  trap 'cat "$EMIT" >&9' EXIT
fi
# The single piece of state that decides the postconditions: which database, if any, has
# already received the ACL file. Read back from the log, so it survives across invocations.
booted_on() { awk '$2 == "bootstrap-file" && $4 == "rc=0" { print $3; exit }' "$LOG"; }

uri=""; sql=""; sqlfile=""; flag=""; prev=""
for a in "$@"; do
  case "$prev" in
    -c) sql="$a"; flag=c ;;
    -tAc) sql="$a"; flag=tAc ;;
    -f) sqlfile="$a"; flag=f ;;
  esac
  case "$a" in
    postgres://*) if [ -z "$uri" ]; then uri="$a"; fi ;;
  esac
  prev="$a"
done

# THE DATABASE IS THE URI PATH, not a substring of the URI. The connecting user is also
# `postgres`, so `case $uri in *postgres*` cannot tell the user from the database.
dbpath=""
if [ -n "$uri" ]; then
  rest=${uri#postgres://}
  case "$rest" in */*) dbpath="/${rest#*/}" ;; esac
  dbpath=${dbpath%%\?*}
fi

kind=unknown
if [ "$flag" = f ]; then
  kind=bootstrap-file
elif [ "$flag" = c ]; then
  # The CREATE is pinned to its exact statement: no IF NOT EXISTS, no stdin, no heredoc.
  if [ "$sql" = "CREATE DATABASE cloudcp TEMPLATE template0 OWNER postgres" ]; then kind=create; fi
elif [ "$flag" = tAc ]; then
  # Classified by query SHAPE, never by a comment: a comment is not an identity.
  case "$sql" in
    *aclexplode*)
      case "$sql" in
        *"d.datname = 'postgres'"*) kind=maintenance-acl ;;
        *"d.datname = 'cloudcp'"*) kind=target-identity ;;
      esac ;;
    *migrator_role_setting_ok*) kind=database-settings ;;
    *cloud_role_settings_present*) kind=maintenance-objects ;;
    *cloud_control_owned_by_owner*) kind=target-objects ;;
    *rolcanlogin*)
      case "$sql" in *"rolname='cloud_cp_owner'"*) kind=cluster-role ;; esac ;;
  esac
fi

# Cluster catalogs (pg_database, pg_roles, pg_db_role_setting) are read on the maintenance
# database; database-scoped catalogs (pg_namespace, pg_class) only mean something on the
# database being checked. The bootstrap is the ONE invocation whose target is not pinned
# here: a misrouted `-f` must be caught by the step's own postconditions, not by a stub that
# refused to answer -- otherwise that mutant would die for the wrong reason.
expected=""
case "$kind" in
  create|maintenance-acl|target-identity|cluster-role|maintenance-objects|database-settings)
    expected="/postgres" ;;
  target-objects) expected="/cloudcp" ;;
  bootstrap-file) case "$dbpath" in /postgres|/cloudcp) expected="$dbpath" ;; esac ;;
esac
if [ "$kind" = unknown ] || [ -z "$dbpath" ] || [ "$dbpath" != "$expected" ]; then
  printf 'psql-stub: unclassifiable invocation (flag=%s path=%s)\n' "${flag:-none}" "${dbpath:-none}" >&2
  record unknown "${dbpath:-none}" 1
  exit 1
fi

db_json() { # <oid> <name> <owner> <datacl_is_null> <datacl> <connect> <create> <temporary>
  printf '{"oid": %s, "database": "%s", "owner": "%s", "datacl_is_null": %s, "datacl": %s, "public_connect": %s, "public_create": %s, "public_temporary": %s}\n' \
    "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$8"
}
# A database with a NULL datacl is not a database without privileges: the built-in default
# gives PUBLIC CONNECT and TEMPORARY, which is exactly what this step has to preserve.
# Root's retained malformed probe, byte for byte. %b turns the octal escape into the raw
# byte, so the step reads the sequence root recorded rather than a lookalike.
probe_obs() { # <octal escape of the invalid byte>
  printf '{"oid":5,"database":"postgres","owner":"postgres","datacl_is_null":false,"datacl":"{%b}","public_connect":true,"public_create":false,"public_temporary":true}\n' "$1"
}
# A LEGITIMATE non-ASCII ACL, in both normalisation forms of the same text. They render
# identically and are different values; a reader that normalised silently would call them
# equal. Written as escapes so the two forms cannot be flattened by an editor.
UTF8_NFC='{=Tc/postgres,postgres=CTc/postgres,olivares_a\0303\0261o=c/postgres}'
UTF8_NFD='{=Tc/postgres,postgres=CTc/postgres,olivares_an\0314\0203o=c/postgres}'
utf8_obs() { # <ACL body, with escapes>
  printf '{"oid": 5, "database": "postgres", "owner": "postgres", "datacl_is_null": false, "datacl": "%b", "public_connect": true, "public_create": false, "public_temporary": true}\n' "$1"
}
MAINT_DEFAULT='5 postgres postgres true null true false true'
TARGET_FRESH='16400 cloudcp postgres true null true false true'
TARGET_BOOTED='16400 cloudcp postgres false "{postgres=CTc/postgres,cloud_cp_owner=CTc/postgres,cloud_cp_migrator=CTc/postgres,cloud_cp_tenant=c/postgres}" false false false'

case "$kind" in
  create)
    if [ "$MODE" = create-refused ]; then
      printf 'ERROR:  database "cloudcp" already exists\n' >&2
      record create "$dbpath" 1; exit 1
    fi
    record create "$dbpath" 0; exit 0 ;;
  bootstrap-file)
    if [ ! -f "$sqlfile" ]; then
      printf 'psql: error: %s: No such file or directory\n' "$sqlfile" >&2
      record bootstrap-file "$dbpath" 1; exit 1
    fi
    record bootstrap-file "$dbpath" 0; exit 0 ;;
  cluster-role)
    record cluster-role "$dbpath" 0; echo 1; exit 0 ;;
  maintenance-acl)
    booted=$(booted_on)
    if [ -z "$booted" ]; then
      # The producer/output faults are injected on the FIRST observation of the step, so a
      # step that honours them cannot have created or bootstrapped anything yet.
      case "$MODE" in
        obs-empty)      record maintenance-acl "$dbpath" 0; exit 0 ;;
        obs-malformed)  record maintenance-acl "$dbpath" 0; printf 'ERROR:  column does not exist\n'; exit 0 ;;
        obs-extra-row)  record maintenance-acl "$dbpath" 0; db_json $MAINT_DEFAULT; db_json $MAINT_DEFAULT; exit 0 ;;
        obs-extra-field)
          record maintenance-acl "$dbpath" 0
          printf '{"oid": 5, "database": "postgres", "owner": "postgres", "datacl_is_null": true, "datacl": null, "public_connect": true, "public_create": false, "public_temporary": true, "datlastsysoid": 1}\n'
          exit 0 ;;
        obs-oversized)
          record maintenance-acl "$dbpath" 0
          pad=$(printf '%70000s' '' | tr ' ' 'x')
          db_json 5 postgres postgres false "\"$pad\"" true false true
          exit 0 ;;
        acl-bytes-ff|acl-bytes-probe)
          record maintenance-acl "$dbpath" 0; probe_obs '\0377'; exit 0 ;;
        acl-bytes-fe)
          record maintenance-acl "$dbpath" 0; probe_obs '\0376'; exit 0 ;;
        acl-utf8-equal|acl-utf8-nfd)
          record maintenance-acl "$dbpath" 0; utf8_obs "$UTF8_NFC"; exit 0 ;;
        obs-nonzero)
          # Valid-looking output AND a nonzero exit: a reader must never erase the producer.
          db_json $MAINT_DEFAULT
          printf 'psql: error: connection to server was lost\n' >&2
          record maintenance-acl "$dbpath" 1; exit 1 ;;
      esac
      record maintenance-acl "$dbpath" 0; db_json $MAINT_DEFAULT; exit 0
    fi
    record maintenance-acl "$dbpath" 0
    case "$MODE" in
      # A real ACL change: PUBLIC lost CONNECT and TEMPORARY on the maintenance database,
      # which is the exact regression this whole contract exists to catch.
      acl-mutated) db_json 5 postgres postgres false '"{postgres=CTc/postgres}"' false false false ;;
      # And the quiet one: a NULL datacl replaced by an EXPLICIT ACL granting the very same
      # effective privileges. Every boolean is identical; the state is not.
      acl-null-to-explicit) db_json 5 postgres postgres false '"{=Tc/postgres,postgres=CTc/postgres}"' true false true ;;
      # The other half of root's pair. A strict reader never gets here, because the FIRST
      # observation already failed; a replacement reader does, and calls the two equal.
      acl-bytes-probe) probe_obs '\0376' ;;
      acl-utf8-equal) utf8_obs "$UTF8_NFC" ;;
      acl-utf8-nfd) utf8_obs "$UTF8_NFD" ;;
      *) db_json $MAINT_DEFAULT ;;
    esac
    exit 0 ;;
  target-identity)
    booted=$(booted_on)
    if [ -z "$booted" ]; then
      case "$MODE" in
        identity-empty)     record target-identity "$dbpath" 0; exit 0 ;;
        identity-duplicate) record target-identity "$dbpath" 0; db_json $TARGET_FRESH; db_json $TARGET_FRESH; exit 0 ;;
        identity-null-oid)  record target-identity "$dbpath" 0; db_json null cloudcp postgres true null true false true; exit 0 ;;
        identity-wrong-owner) record target-identity "$dbpath" 0; db_json 16400 cloudcp cloud_cp_owner true null true false true; exit 0 ;;
      esac
      record target-identity "$dbpath" 0; db_json $TARGET_FRESH; exit 0
    fi
    record target-identity "$dbpath" 0
    if [ "$booted" != "/cloudcp" ]; then
      # The ACL file went somewhere else, so this database still carries the fresh default:
      # PUBLIC keeps CONNECT and TEMPORARY, and the step must refuse to publish.
      db_json $TARGET_FRESH; exit 0
    fi
    case "$MODE" in
      oid-replaced)      db_json 16999 cloudcp postgres false '"{postgres=CTc/postgres}"' false false false ;;
      public-connect-remains)   db_json 16400 cloudcp postgres false '"{=c/postgres,postgres=CTc/postgres}"' true false false ;;
      public-create-remains)    db_json 16400 cloudcp postgres false '"{=C/postgres,postgres=CTc/postgres}"' false true false ;;
      public-temporary-remains) db_json 16400 cloudcp postgres false '"{=T/postgres,postgres=CTc/postgres}"' false false true ;;
      *) db_json $TARGET_BOOTED ;;
    esac
    exit 0 ;;
  target-objects)
    record target-objects "$dbpath" 0
    v=false
    if [ "$(booted_on)" = "/cloudcp" ]; then v=true; fi
    printf '{"cloud_control_present": %s, "cloud_control_owned_by_owner": %s, "schema_migrations_present": %s, "schema_migrations_owned_by_migrator": %s}\n' "$v" "$v" "$v" "$v"
    exit 0 ;;
  database-settings)
    record database-settings "$dbpath" 0
    v=false
    if [ "$(booted_on)" = "/cloudcp" ]; then v=true; fi
    printf '{"migrator_role_setting_ok": %s, "migrator_search_path_ok": %s}\n' "$v" "$v"
    exit 0 ;;
  maintenance-objects)
    record maintenance-objects "$dbpath" 0
    booted=$(booted_on)
    schema=false; table=false; settings=false
    # If the ACL file landed on the maintenance database, its objects are THERE. This is the
    # causal seam that kills a misrouted bootstrap without any text comparison.
    if [ "$booted" = "/postgres" ]; then schema=true; table=true; settings=true; fi
    case "$MODE" in
      maint-schema-pre)    if [ -z "$booted" ]; then schema=true; fi ;;
      maint-table-pre)     if [ -z "$booted" ]; then table=true; fi ;;
      maint-settings-pre)  if [ -z "$booted" ]; then settings=true; fi ;;
      maint-schema-post)   if [ -n "$booted" ]; then schema=true; fi ;;
      maint-table-post)    if [ -n "$booted" ]; then table=true; fi ;;
      maint-settings-post) if [ -n "$booted" ]; then settings=true; fi ;;
    esac
    printf '{"cloud_control_present": %s, "cloud_schema_migrations_present": %s, "cloud_role_settings_present": %s}\n' "$schema" "$table" "$settings"
    exit 0 ;;
esac
exit 1
STUB
  printf '#!/bin/sh\necho deadbeefdeadbeefdeadbeefdeadbeef\n' > "$b/openssl"
  chmod +x "$b/psql" "$b/openssl" || return 1
  printf '%s' "$b"
}

run_case() { # <etiqueta-dir> <crear-modulo 0|1> <crear-sql 0|1> <bloque>
  local d; d=$(mktemp -d "$WORK/case.XXXXXX") || return 9
  if [ "$2" = 1 ]; then
    mkdir -p "$d/cloud/control-plane/deploy"
    [ "$3" = 1 ] && printf -- '-- roles\n' > "$d/cloud/control-plane/deploy/cloud-control-roles.sql"
  fi
  local stubs; stubs=$(mk_stubs "$d") || return 9
  mkdir -p "$d/rt"
  : > "$d/env"; : > "$d/out"; : > "$d/sum"; : > "$d/oplog"
  ( cd "$d" \
    && PATH="$stubs:$PATH" PGHOSTPORT=127.0.0.1:5432 RUNNER_TEMP="$d/rt" \
       GITHUB_ENV="$d/env" GITHUB_OUTPUT="$d/out" GITHUB_STEP_SUMMARY="$d/sum" \
       CP_STUB_LOG="$d/oplog" CP_STUB_MODE="${CP_STUB_MODE:-normal}" \
       CP_STUB_EMIT="$d/emitted" \
       bash "$4" ) > "$d/stdout" 2>&1
  CASE_RC=$?; CASE_DIR=$d
  return 0
}

# The eleven capability DSNs, by name. The old gate grepped for two of them, which cannot
# see a partial retarget: `OpenPools` refuses a MISSING DSN but never checks that the eleven
# agree on one database.
CP_DSNS="DATABASE_MIGRATOR_URL DATABASE_TENANT_URL DATABASE_TENANT_POOLED_URL \
DATABASE_SWEEPER_URL DATABASE_ADMIN_URL DATABASE_POLLER_URL DATABASE_NOTIFIER_URL \
DATABASE_BILLING_URL DATABASE_RESOLVER_URL DATABASE_EXPORTER_URL DATABASE_IDEMPOTENCY_URL"
# The names this step must NEVER write: they belong to the port resolver step and they name
# the core databases, which is exactly what this isolation has to leave alone.
CP_FOREIGN="OLIVARES_TEST_POSTGRES_DSN OLIVARES_TEST_POSTGRES_ADMIN_DSN \
OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_TEST_VECTOR_DSN PGHOSTPORT"

oplog_pairs() { awk '{ print $2, $3 }' "$1"; }
oplog_has() { awk -v k="$2" '$2 == k { found = 1 } END { exit found ? 0 : 1 }' "$1"; }
oplog_failed() { awk '$4 != "rc=0" { bad = 1 } END { exit bad ? 0 : 1 }' "$1"; }

# The eleven names, each exactly once, each with /cloudcp as the URI PATH. The path is
# parsed, never grepped: the login is `cloud_cp_*` and the host carries a port, so a
# substring test could not tell the database from the rest of the URI.
exports_ok() { # <fichero GITHUB_ENV>
  local f="$1" n
  for n in $CP_DSNS; do
    [ "$(command grep -c "^${n}=" "$f")" = "1" ] || return 1
  done
  [ "$(command grep -c '^DATABASE_' "$f")" = "11" ] || return 1
  [ "$(command grep '^DATABASE_' "$f" | sed -e 's/^[^=]*=//' -e 's/?.*$//' \
      -e 's#^postgres://[^/]*##' | command grep -c '^/cloudcp$')" = "11" ] || return 1
  return 0
}

B="$WORK/block.sh"
if ! step_field "$WF" pg-roles-cloud run > "$B" 2>"$WORK/e"; then
  cannot "no encuentro el paso pg-roles-cloud ($(head -1 "$WORK/e"))"
fi
[ -s "$B" ] || cannot "el bloque run: de pg-roles-cloud salio vacio"

if bash -n "$B" 2>"$WORK/n"; then ok "el bloque run: extraido es shell valido (bash -n)"
else no "el bloque no pasa bash -n: $(head -1 "$WORK/n")"; fi

# --- caso A: el modulo NO esta (arbol publico) -> PARTIAL y rc 0
run_case A 0 0 "$B" || cannot "no pude montar el caso A"
if [ "$CASE_RC" = 0 ]; then ok "A: sin cloud/control-plane el paso NO rompe (rc 0)"
else no "A: sin el modulo el paso sale rc $CASE_RC: $(head -2 "$CASE_DIR/stdout")"; fi
if command grep -qF "cloud-control-roles: NOT APPLICABLE" "$CASE_DIR/stdout" && command grep -qF "cloud/control-plane" "$CASE_DIR/stdout"; then
  ok "A: imprime el NOT APPLICABLE de #2155, con su sujeto nombrado"
else no "A: no imprime el mensaje literal: $(head -2 "$CASE_DIR/stdout")"; fi
if command grep -q 'provisioned=false' "$CASE_DIR/out"; then ok "A: deja el marcador provisioned=false para los consumidores"
else no "A: no deja marcador: los pasos siguientes no pueden saber que fue PARTIAL"; fi
if command grep -q 'NOT APPLICABLE' "$CASE_DIR/sum"; then ok "A: y lo repite en el resumen del job (se dice tambien al final)"
else no "A: el resumen del job no repite el NOT APPLICABLE"; fi
if ! command grep -q 'DATABASE_TENANT_URL' "$CASE_DIR/env"; then ok "A: no finge DSNs que no existen"
else no "A: escribio DSNs de capacidad sin haber creado ningun rol"; fi
# O1: and it touched no database at all. "Exported nothing" is not the same claim as "ran no
# statement": without the operation log a step could CREATE, bootstrap and then keep quiet.
if [ ! -s "$CASE_DIR/oplog" ]; then ok "A: and it runs no database operation whatsoever"
else no "A: it reached the database without a module: $(oplog_pairs "$CASE_DIR/oplog" | tr '\n' ';')"; fi

# --- caso B: modulo y fichero presentes -> camino normal
run_case B 1 1 "$B" || cannot "no pude montar el caso B"
if [ "$CASE_RC" = 0 ]; then ok "B: con el modulo presente sigue el camino normal (rc 0)"
else no "B: el camino normal se rompio (rc $CASE_RC): $(tail -2 "$CASE_DIR/stdout")"; fi
if command grep -q 'provisioned=true' "$CASE_DIR/out"; then ok "B: marca provisioned=true"
else no "B: no marca provisioned=true"; fi
if command grep -q 'DATABASE_TENANT_URL' "$CASE_DIR/env" && command grep -q 'DATABASE_IDEMPOTENCY_URL' "$CASE_DIR/env"; then
  ok "B: escribe las DSN de capacidad (el trabajo real sigue haciendose)"
else no "B: el camino normal ya no escribe las DSN"; fi
if ! command grep -q 'NOT APPLICABLE' "$CASE_DIR/stdout"; then ok "B: y NO dice NOT APPLICABLE cuando no lo es"
else no "B: dice NOT APPLICABLE con el modulo presente"; fi
# O4/O5/O6: WHAT the step did, in WHICH order and against WHICH database. The sequence is
# pinned on purpose: reordering it is a topology change and has to be re-reviewed, not
# absorbed. Each pair is <kind> <URI path>, both read from the actual invocation.
CP_EXPECTED_OPS='maintenance-acl /postgres
maintenance-objects /postgres
create /postgres
target-identity /postgres
bootstrap-file /cloudcp
cluster-role /postgres
target-identity /postgres
target-objects /cloudcp
database-settings /postgres
maintenance-acl /postgres
maintenance-objects /postgres'
if [ "$(oplog_pairs "$CASE_DIR/oplog")" = "$CP_EXPECTED_OPS" ]; then
  ok "B: the database operations are the contracted sequence, each against its own database"
else no "B: the operation sequence is not the contracted one: $(oplog_pairs "$CASE_DIR/oplog" | tr '\n' ';')"; fi
if ! oplog_failed "$CASE_DIR/oplog"; then ok "B: and every producer completed (none was read without checking it)"
else no "B: an operation exited nonzero and the step carried on"; fi
if [ "$(awk '$2 == "bootstrap-file" { print $3 }' "$CASE_DIR/oplog")" = "/cloudcp" ]; then
  ok "B: the ACL file is applied to /cloudcp, taken from the -f invocation URI path"
else no "B: the ACL file is not applied to /cloudcp"; fi
if exports_ok "$CASE_DIR/env"; then ok "B: the eleven capability DSNs are written once each and all name /cloudcp"
else no "B: the eleven capability DSNs are not each present once naming /cloudcp"; fi
CP_FOREIGN_HIT=0
for n in $CP_FOREIGN; do
  if command grep -q "^${n}=" "$CASE_DIR/env"; then CP_FOREIGN_HIT=1; fi
done
if [ "$CP_FOREIGN_HIT" = 0 ]; then ok "B: and it never rewrites a core or vector DSN (those belong to the resolver step)"
else no "B: this step wrote a core/vector DSN that is not its own"; fi
# The stub openssl emits one constant, so a generated password appearing in the step log or
# in the operation log is visible here. `$GITHUB_ENV` is where they legitimately go.
if ! command grep -q 'deadbeef' "$CASE_DIR/stdout" && ! command grep -q 'deadbeef' "$CASE_DIR/oplog"; then
  ok "B: and no generated password reaches the step log or the operation log"
else no "B: a generated password was printed outside GITHUB_ENV"; fi
# Cleanup is restricted to the directory this invocation created: the file the role control
# writes straight into RUNNER_TEMP has to survive it.
if [ -z "$(command ls -d "$CASE_DIR"/rt/cloud-cp-obs.* 2>/dev/null)" ]; then
  ok "B: the private observation directory is removed at the end of the step"
else no "B: the private observation directory survived the step"; fi
if [ -f "$CASE_DIR/rt/cloud_cp_owner.txt" ]; then ok "B: and cleanup does not reach the rest of RUNNER_TEMP"
else no "B: cleanup removed a RUNNER_TEMP file this step does not own"; fi

# --- caso C: modulo presente y fichero AUSENTE -> el defecto real se sigue viendo
run_case C 1 0 "$B" || cannot "no pude montar el caso C"
if [ "$CASE_RC" != 0 ]; then ok "C: con el modulo presente y el guion ausente el paso SIGUE muriendo"
else no "C: un guion borrado dentro del hub se tapa como si fuera el export (rc 0)"; fi
if ! command grep -q 'NOT APPLICABLE' "$CASE_DIR/stdout"; then ok "C: y no lo llama NOT APPLICABLE (no es un arbol publico)"
else no "C: llama NOT APPLICABLE a un defecto real"; fi
if ! command grep -q '^DATABASE_' "$CASE_DIR/env"; then ok "C: and a missing script publishes no capability DSN"
else no "C: it published capability DSNs with the ACL file missing"; fi

# ---------------------------------------------------------------- causal controls
# Each variant breaks ONE named seam and the assertion is not merely "the step exited
# nonzero": it names the actions that must NOT have happened afterwards. A step can exit
# nonzero for the wrong reason and still be broken, and a subprocess exit alone cannot tell
# the two apart. These prove routing and control behaviour, never PostgreSQL semantics.
fault_case() { # <modo> <etiqueta> <accion prohibida: none|create|bootstrap-file> <diagnostico esperado>
  CP_STUB_MODE="$1"
  run_case F 1 1 "$B" || cannot "no pude montar el caso de fallo $1"
  CP_STUB_MODE=normal
  if [ "$CASE_RC" != 0 ]; then ok "$2: the step fails"
  else no "$2: the step survived with rc 0"; fi
  # The seam, by name. A step can exit nonzero for the wrong reason and still be broken, and
  # a bench that only reads the exit status cannot tell those two apart.
  if command grep -qF "$4" "$CASE_DIR/stdout"; then ok "$2: and it fails on its own seam ($4)"
  else no "$2: it failed for another reason: $(tail -1 "$CASE_DIR/stdout")"; fi
  if [ "$3" != none ]; then
    if ! oplog_has "$CASE_DIR/oplog" "$3"; then ok "$2: and it stops before $3"
    else no "$2: it reached $3 anyway"; fi
  fi
  if ! command grep -q '^DATABASE_' "$CASE_DIR/env"; then ok "$2: and it exports no capability DSN"
  else no "$2: it exported capability DSNs after the failure"; fi
}

fault_case create-refused           "O2 refused CREATE"                           bootstrap-file 'database "cloudcp" already exists'
fault_case identity-empty           "O3 identity: no row"                         bootstrap-file 'the catalog returned no row'
fault_case identity-duplicate       "O3 identity: two rows"                       bootstrap-file 'carries 2 lines; exactly one row is required'
fault_case identity-null-oid        "O3 identity: null OID"                       bootstrap-file 'oid is not a JSON integer'
fault_case identity-wrong-owner     "O3 identity: the owner is not the administrator" bootstrap-file "is owned by 'cloud_cp_owner', not 'postgres'"
fault_case obs-nonzero              "O9 nonzero producer with valid-looking output" create 'catalog observation did not complete'
fault_case obs-empty                "O9 empty observation"                        create 'the catalog returned no row'
fault_case obs-malformed            "O9 observation that is not JSON"             create 'is not one valid JSON object'
fault_case obs-extra-row            "O9 observation with a second row"            create 'carries 2 lines; exactly one row is required'
fault_case obs-extra-field          "O9 observation with an unexpected field"     create 'unexpected: datlastsysoid'
fault_case obs-oversized            "O9 observation above the size ceiling"       create 'above the 65536 byte ceiling'
fault_case maint-schema-pre         "O8 cloud_control already on postgres"        create 'before CREATE: cloud_control_present is not false'
fault_case maint-table-pre          "O8 cloud schema_migrations already on postgres" create 'before CREATE: cloud_schema_migrations_present is not false'
fault_case maint-settings-pre       "O8 cloud role settings already on postgres"  create 'before CREATE: cloud_role_settings_present is not false'
fault_case maint-schema-post        "O8 cloud_control left on postgres"           none 'after bootstrap: cloud_control_present is not false'
fault_case maint-table-post         "O8 cloud schema_migrations left on postgres" none 'after bootstrap: cloud_schema_migrations_present is not false'
fault_case maint-settings-post      "O8 cloud role settings left on postgres"     none 'after bootstrap: cloud_role_settings_present is not false'
fault_case acl-mutated              "O7 the maintenance ACL changed"              none 'datacl_is_null changed while the cloud plane was bootstrapped'
fault_case acl-null-to-explicit     "O7 a null ACL replaced by an equivalent explicit one" none 'datacl_is_null changed while the cloud plane was bootstrapped'
fault_case oid-replaced             "O10 the bound target OID moved after bootstrap" none 'oid moved from 16400 to 16999'
fault_case public-connect-remains   "O11 PUBLIC keeps CONNECT on cloudcp"         none 'PUBLIC still holds CONNECT on cloudcp'
fault_case public-create-remains    "O11 PUBLIC keeps CREATE on cloudcp"          none 'PUBLIC still holds CREATE on cloudcp'
fault_case public-temporary-remains "O11 PUBLIC keeps TEMPORARY on cloudcp"       none 'PUBLIC still holds TEMPORARY on cloudcp'

# ---------------------------------------------------------------- the decoder seam
# The digests root recorded for the two halves of its probe. Checking what the step ACTUALLY
# read against them is the difference between a control that uses those bytes and a control
# that claims to: a typo in an octal escape would otherwise pass unnoticed.
CP_PROBE_FF=e15045b973d02db9743a94e5d329388fd33cadffc0b4eec72b58aa298e0903d6
CP_PROBE_FE=0d86f5d8c1cc77ead4fe443c31a638884ee52441595ee9cabdc39da41ddcac76
UTF8_SEAM='observation is not valid UTF-8'
emitted_sha() { # <directorio del caso> <numero de operacion>
  [ -f "$1/emitted/$2.out" ] || return 1
  sha256sum < "$1/emitted/$2.out" | cut -d' ' -f1
}

fault_case acl-bytes-ff    "O14 malformed UTF-8 in the ACL (0xFF)" create "$UTF8_SEAM"
if [ "$(emitted_sha "$CASE_DIR" 1)" = "$CP_PROBE_FF" ]; then
  ok "O14 (0xFF): and what the step read is root's retained probe, byte for byte"
else no "O14 (0xFF): the control did not feed root's exact probe bytes"; fi
fault_case acl-bytes-fe    "O14 malformed UTF-8 in the ACL (0xFE)" create "$UTF8_SEAM"
if [ "$(emitted_sha "$CASE_DIR" 1)" = "$CP_PROBE_FE" ]; then
  ok "O14 (0xFE): and what the step read is root's retained probe, byte for byte"
else no "O14 (0xFE): the control did not feed root's exact probe bytes"; fi
# The pair itself. A strict reader never reaches the second half, and that is the point: the
# step stops at the decoder, long before it could compare anything or publish anything.
fault_case acl-bytes-probe "O14 root's unequal malformed pair"      create "$UTF8_SEAM"

# O15 is the other direction, and it is the one a strict decoder could plausibly break: a
# LEGITIMATE non-ASCII ACL must still be read, accepted and published. The second half is
# sharper than it looks -- NFC and NFD carry the same text and different bytes, so a reader
# that normalised quietly would report them equal and lose the exact value it must preserve.
CP_STUB_MODE=acl-utf8-equal
run_case U 1 1 "$B" || cannot "no pude montar el caso de ACL no-ASCII valido"
CP_STUB_MODE=normal
if [ "$CASE_RC" = 0 ]; then ok "O15 a valid non-ASCII ACL is accepted and the step completes"
else no "O15 a valid non-ASCII ACL broke the step: $(tail -1 "$CASE_DIR/stdout")"; fi
if exports_ok "$CASE_DIR/env"; then ok "O15 and the eleven DSNs are still published"
else no "O15 the step published nothing with a valid non-ASCII ACL"; fi
fault_case acl-utf8-nfd    "O15 the same non-ASCII ACL in NFD instead of NFC" none 'datacl changed while the cloud plane was bootstrapped'
# ---------------------------------------------------------------- el cableado de los consumidores

IF_SUITE=$(step_field "$WF" test-cloud-norace if) || cannot "no encuentro el paso test-cloud-norace"
case "$IF_SUITE" in
  *"steps.pg-roles-cloud.outputs.provisioned == 'true'"*)
    ok "el consumidor (test:cloud:norace) solo corre si los roles se provisionaron" ;;
  *) no "el consumidor no mira el marcador: correria sin DSN ($IF_SUITE)" ;;
esac
IF_NOTICE=$(notice_if "$WF") || cannot "no encuentro el paso de aviso NOT APPLICABLE"
case "$IF_NOTICE" in
  *"provisioned == 'false'"*) ok "y existe un aviso que dice CON PALABRAS que se salto y por que" ;;
  *) no "el aviso no esta atado al marcador: el salto seria mudo ($IF_NOTICE)" ;;
esac

# ---------------------------------------------------------------- mutantes

mut_file(){ python3 - "$WF" "$WORK/mut.yml" "$1" <<'PY'
import sys
s=open(sys.argv[1],encoding='utf-8').read()
kind=sys.argv[3]
original=s
if kind=='sin-guarda':
    i=s.index('          if [ ! -d cloud/control-plane ]; then')
    j=s.index('          echo "provisioned=true" >> "$GITHUB_OUTPUT"')
    s=s[:i]+s[j:]
elif kind=='por-fichero':
    s=s.replace('if [ ! -d cloud/control-plane ]; then',
                'if [ ! -f cloud/control-plane/deploy/cloud-control-roles.sql ]; then',1)
elif kind=='sin-exit0':
    s=s.replace('            exit 0\n          fi\n          echo "provisioned=true"',
                '            exit 1\n          fi\n          echo "provisioned=true"',1)
elif kind=='consumidor-suelto':
    s=s.replace("        if: ${{ success() && steps.pg-roles-cloud.outputs.provisioned == 'true' }}\n",'',1)
elif kind=='dsn-mal-enrutada':
    s=s.replace('DATABASE_SWEEPER_URL=postgres://cloud_cp_sweeper_ro:${PW_sweeper}@${PGHOSTPORT}/${CP_DB}?sslmode=disable',
                'DATABASE_SWEEPER_URL=postgres://cloud_cp_sweeper_ro:${PW_sweeper}@${PGHOSTPORT}/postgres?sslmode=disable',1)
elif kind=='bootstrap-en-postgres':
    s=s.replace('psql "$CP_URI_TARGET" \\\n            -X -v ON_ERROR_STOP=1 "${args[@]}" -f ',
                'psql "$CP_URI_MAINT" \\\n            -X -v ON_ERROR_STOP=1 "${args[@]}" -f ',1)
elif kind=='decodificacion-por-reemplazo':
    s=s.replace('                  with open(path, encoding="utf-8") as handle:',
                '                  with open(path, encoding="utf-8", errors="replace") as handle:',1)
elif kind=='sin-comparacion-acl':
    s=s.replace('          python3 "$CP_OBS/validate.py" database --file "$CP_OBS/maint-post.json" \\\n'
                '            --kind "the maintenance database after bootstrap" --database "$CP_MAINT_DB"'
                ' --owner postgres --identical "$CP_OBS/maint-pre.json"\n','',1)
else:
    sys.stderr.write('mutacion desconocida: %s\n'%kind)
    raise SystemExit(5)
# Una mutacion que NO APLICA no es un negativo que pase: es un negativo que no se ha hecho. El
# `mutant_dies` de antes la contaba como mutante muerto, que es un falso positivo del banco.
if s==original:
    sys.stderr.write('la mutacion %s no cambia el workflow\n'%kind)
    raise SystemExit(4)
open(sys.argv[2],'w',encoding='utf-8').write(s)
PY
}

mutant_dies(){ # <etiqueta> <kind>  ; 0 muere · 1 SOBREVIVE · 2 la mutacion no aplica
  mut_file "$2" || return 2
  local MB="$WORK/mutblock.sh"
  if ! step_field "$WORK/mut.yml" pg-roles-cloud run > "$MB" 2>/dev/null; then
    printf 'MUERTO %s (el paso deja de ser legible)\n' "$1"; return 0; fi
  case "$2" in
    sin-guarda|sin-exit0)
      run_case M 0 0 "$MB"; [ "$CASE_RC" = 0 ] || { printf 'MUERTO %s (caso A deja de dar rc 0)\n' "$1"; return 0; } ;;
    por-fichero)
      run_case M 1 0 "$MB"; [ "$CASE_RC" != 0 ] || { printf 'MUERTO %s (caso C deja de morir)\n' "$1"; return 0; } ;;
    consumidor-suelto)
      local i; i=$(step_field "$WORK/mut.yml" test-cloud-norace if 2>/dev/null)
      case "$i" in *"provisioned == 'true'"*) ;; *) printf 'MUERTO %s (el consumidor pierde su guarda)\n' "$1"; return 0;; esac ;;
    dsn-mal-enrutada)
      # O12: killed by the export checker, not by the step -- the step composes the eleven from
      # one variable, so a hand-edited path is a SOURCE defect the bench has to see.
      run_case M 1 1 "$MB"
      exports_ok "$CASE_DIR/env" || { printf 'MUERTO %s (el checador de las once exportaciones lo rechaza)\n' "$1"; return 0; } ;;
    bootstrap-en-postgres)
      # O13a: killed by the step's OWN postconditions. The stub answers a bootstrap on
      # /postgres normally; what fails is that cloudcp then still carries the fresh defaults
      # and postgres carries the cloud objects. No text comparison is involved.
      run_case M 1 1 "$MB"; [ "$CASE_RC" = 0 ] || { printf 'MUERTO %s (las postcondiciones del paso lo rechazan)\n' "$1"; return 0; } ;;
    decodificacion-por-reemplazo)
      # Root's finding, reproduced end to end through the real step. With replacement
      # decoding the two halves of the probe pair collapse to one string, the maintenance
      # comparison passes and the step publishes. The kill is that the malformed control
      # stops failing; the emitted bytes are what make it a finding rather than a claim.
      CP_STUB_MODE=acl-bytes-probe; run_case M 1 1 "$MB"; CP_STUB_MODE=normal
      local first last; first=$(emitted_sha "$CASE_DIR" 1); last=$(emitted_sha "$CASE_DIR" 10)
      if [ "$first" = "$CP_PROBE_FF" ] && [ "$last" = "$CP_PROBE_FE" ]; then
        ok "$1: the mutated step read both halves of root's probe, and they are byte-unequal"
      else no "$1: the mutated step did not read root's two probe halves (got ${first:-none} / ${last:-none})"; fi
      [ "$CASE_RC" != 0 ] || { printf 'MUERTO %s (con decodificacion por reemplazo dos observaciones de bytes distintos vuelven a compararse iguales)\n' "$1"; return 0; } ;;
    sin-comparacion-acl)
      # O13b: a removed comparison can only be caught with UNEQUAL values on either side, so
      # this one is run with the maintenance ACL actually changed.
      CP_STUB_MODE=acl-mutated; run_case M 1 1 "$MB"; CP_STUB_MODE=normal
      [ "$CASE_RC" != 0 ] || { printf 'MUERTO %s (sin la comparacion un ACL alterado ya no mata el paso)\n' "$1"; return 0; } ;;
  esac
  printf 'SOBREVIVE %s\n' "$1"; return 1
}

for m in "M1 guarda borrada:sin-guarda" \
         "M2 guarda por FICHERO en vez de por DIRECTORIO:por-fichero" \
         "M3 NOT APPLICABLE que rompe igual:sin-exit0" \
         "M4 consumidor sin guarda:consumidor-suelto" \
         "M5 una de las once DSN apunta a /postgres:dsn-mal-enrutada" \
         "M6 el bootstrap se aplica a /postgres:bootstrap-en-postgres" \
         "M7 sin la comparacion del ACL de mantenimiento:sin-comparacion-acl" \
         "M8 el validador decodifica por reemplazo:decodificacion-por-reemplazo"; do
  lab=${m%%:*}; kind=${m##*:}
  mutant_dies "$lab" "$kind"; mrc=$?
  case "$mrc" in
    0) ok "$lab: el mutante muere" ;;
    2) no "$lab: la mutacion NO APLICA sobre el workflow — un negativo que no se ha hecho no pasa" ;;
    *) no "$lab SOBREVIVE" ;;
  esac
done

# CONTROL sobre el propio guion: si mira un workflow sin el paso, dice que no puede mirar.
printf 'jobs:\n  control-plane:\n    steps:\n      - name: nada\n' > "$WORK/vacio.yml"
step_field "$WORK/vacio.yml" pg-roles-cloud run >/dev/null 2>&1
if [ $? -eq 3 ]; then ok "CONTROL: sin el paso, el extractor dice que no puede mirar"
else no "CONTROL: la ausencia del paso no se distingue de un pase"; fi

printf '\ntest-cloud-roles-partial: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
