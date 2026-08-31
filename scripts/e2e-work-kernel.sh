#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Verificacion e2e de la ORQUESTACION del kernel de trabajo sobre un motor REAL.
#
# No es un test unitario: arranca contra un `olivares serve` vivo y recorre el ciclo
# completo de un objetivo de trabajo -- crear, poner listo, tomar el lease, HANDOFF por
# takeover, entregar -- por el mismo plano de mando de tres fases (validate/plan/apply)
# que usa la consola y que van a usar los CLIs.
#
# Tres respuestas, nunca dos:  0 limpio · 1 hallazgo · 2 no he podido mirar.
set -u -o pipefail

# ⛔ NO CON `${VAR:?}`, Y NO ES ESTILO: `:?` SALE 1, QUE AQUI SIGNIFICA HALLAZGO.
# Un arnes al que no le han dado configuracion no ha mirado nada, asi que acusaba al
# producto de un defecto por la unica razon de que a el le faltaba una variable -- la
# confusion exacta que las tres respuestas existen para impedir, en la PRIMERA linea del
# fichero. Falta de configuracion es NO HE PODIDO MIRAR, y se dicen TODAS las que faltan
# de una vez en vez de una por corrida.
faltan=
for v in OLIVARES_E2E_BASE OLIVARES_E2E_TOKEN OLIVARES_E2E_TENANT OLIVARES_E2E_WORKSPACE OLIVARES_E2E_USER; do
  eval "val=\${$v-}"
  [ -z "$val" ] && faltan="$faltan $v"
done
if [ -n "$faltan" ]; then
  printf '⚠ NO HE PODIDO MIRAR: sin configuracion no se ha ejercitado nada.\n' >&2
  printf '  falta(n):%s\n' "$faltan" >&2
  printf '  este arnes habla con un motor VIVO; tambien `--selftest`, cuyos casos A y C\n' >&2
  printf '  mandan de verdad. Arranca uno y exporta las cinco.\n' >&2
  exit 2
fi
B=$OLIVARES_E2E_BASE
TOK=$OLIVARES_E2E_TOKEN
TEN=$OLIVARES_E2E_TENANT
WS=$OLIVARES_E2E_WORKSPACE
USERID=$OLIVARES_E2E_USER
# Un directorio NUEVO por corrida: la evidencia de una corrida no puede contaminar a la
# siguiente, y un fichero plantado por otro no puede hacerse pasar por resultado nuestro.
OUT=${OLIVARES_E2E_OUT:-}
if [ -z "$OUT" ]; then
  OUT=$(mktemp -d "${TMPDIR:-/tmp}/olivares-e2e-work-kernel.XXXXXX") || exit 2
else
  mkdir -p "$OUT" || exit 2
  rm -f "$OUT"/*.result.json "$OUT"/*.validate.json "$OUT"/*.plan.json 2>/dev/null || true
fi

fallos=0; ciegos=0
paso()  { printf '\n\033[1m### %s\033[0m\n' "$*"; }
ok()    { printf '  ok    %s\n' "$*"; }
mal()   { printf '  ⛔ FALLO %s\n' "$*"; fallos=$((fallos+1)); }
ciego() { printf '  ⚠ NO HE PODIDO MIRAR %s\n' "$*"; ciegos=$((ciegos+1)); }

api() { # api <metodo> <ruta> <fichero-cuerpo|-> [cabeceras extra...]
  api_as "$TOK" "$@"
}
# api_as habla con OTRA credencial. Existe porque el relevo REAL no se puede medir con una
# sola: cada sesion tiene que actuar con la suya, que es justo la garantia que se comprueba.
api_as() { # api_as <token> <metodo> <ruta> <fichero-cuerpo|-> [cabeceras...]
  local tok=$1 m=$2 p=$3 body=$4; shift 4
  local args=(-s -X "$m" "$B$p"
    -H "Authorization: Bearer $tok" -H "X-Olivares-Tenant: $TEN"
    -H 'Content-Type: application/json' "$@")
  [ "$body" != "-" ] && args+=(-d @"$body")
  curl "${args[@]}"
}
jq_() { python3 -c "import json,sys
try: d=json.load(sys.stdin)
except Exception as e: print('__PARSE_ERROR__'); sys.exit(0)
cur=d
for k in sys.argv[1].split('.'):
    if k=='': continue
    if isinstance(cur,list):
        try: cur=cur[int(k)]
        except Exception: print('__MISSING__'); sys.exit(0)
    elif isinstance(cur,dict) and k in cur: cur=cur[k]
    else: print('__MISSING__'); sys.exit(0)
print(cur if not isinstance(cur,(dict,list)) else json.dumps(cur))" "$1"; }

# es_ilegible distingue «el motor dijo algo que no entiendo» de «el motor dijo que no».
# Los dos centinelas de `jq_` significan lo mismo para un veredicto: no hay observacion.
es_ilegible() { case "$1" in __PARSE_ERROR__|__MISSING__) return 0 ;; *) return 1 ;; esac; }

# --- un mando de tres fases, con su hash de plan atado -----------------------------
# El valor de esta funcion es el ATADO: el hash que devuelve `plan` es el que viaja en
# `apply`. Si el plan cambia entre las dos fases, el motor rechaza -- y eso es lo que
# convierte "he mirado" en "he mirado ESTO".
mando() { # mando <ruta> <fichero> <etiqueta> [cabeceras...]   (MANDO_TOK: credencial)
  local ruta=$1 cuerpo=$2 etq=$3; shift 3
  local tok=${MANDO_TOK:-$TOK}
  local idem; idem=$(python3 -c 'import uuid;print(uuid.uuid4())')
  local v p a
  v=$(api_as "$tok" POST "$ruta?mode=validate" "$cuerpo" -H "Idempotency-Key: $idem" "$@")
  local vv; vv=$(printf '%s' "$v" | jq_ verdict)
  if [ "$vv" != "LIMPIO" ]; then
    printf '%s\n' "$v" > "$OUT/$etq.validate.json"
    local cc; cc=$(printf '%s' "$v" | jq_ code)
    # ⛔ UNA RESPUESTA ILEGIBLE ES «NO HE PODIDO MIRAR», Y ESTE GUION LA LLAMABA HALLAZGO.
    #
    # Un motor caido, un 502 en HTML de un proxy o una conexion cortada dejan a `curl` con
    # stdout VACIO, `jq_` devuelve `__PARSE_ERROR__` —que no es `NO_HE_PODIDO_MIRAR`— y el
    # flujo caia en `mal`: el arnes salia 1 diciendo que habia encontrado algo. **Es la
    # unica confusion que un gate de tres respuestas existe para impedir**, y la tenia el
    # guion que la predica en su cabecera. La cazo una revision de codigo sobre mi diff.
    if es_ilegible "$vv" || es_ilegible "$cc"; then
      ciego "$etq: respuesta ILEGIBLE del motor (vacia o no-JSON) — no es un hallazgo, es que no he podido mirar"
      return 2
    fi
    if [ "$vv" = "NO_HE_PODIDO_MIRAR" ]; then ciego "$etq validate: $cc"; return 2; fi
    # ⛔ Un codigo que el llamador declara ESPERADO no se rotula FALLO ni aqui ni en el
    # recuento. La primera version imprimia «⛔ FALLO» y despues restaba uno del contador:
    # el titular salia bien y el cuerpo del informe decia lo contrario, que es la forma de
    # error que mas caro sale cuando alguien lee por encima.
    case " ${MANDO_TOLERA:-} " in
      *" $cc "*) printf '  ·     %s: %s (esperado con esta credencial, no es hallazgo)\n' "$etq" "$cc"; return 3 ;;
    esac
    mal "$etq validate = $vv ($cc)"; return 1
  fi
  p=$(api_as "$tok" POST "$ruta?mode=plan" "$cuerpo" -H "Idempotency-Key: $idem" "$@")
  printf '%s\n' "$p" > "$OUT/$etq.plan.json"
  local ph; ph=$(printf '%s' "$p" | jq_ plan_hash)
  if [ -z "$ph" ] || es_ilegible "$ph"; then
    ciego "$etq plan: sin plan_hash legible"; return 2
  fi
  a=$(api_as "$tok" POST "$ruta?mode=apply" "$cuerpo" -H "Idempotency-Key: $idem" -H "If-Plan-Hash: $ph" "$@")
  printf '%s\n' "$a" > "$OUT/$etq.apply.json"
  local av; av=$(printf '%s' "$a" | jq_ verdict)
  if [ "$av" != "LIMPIO" ]; then
    if es_ilegible "$av"; then
      ciego "$etq apply: respuesta ILEGIBLE del motor — no he podido mirar"; return 2
    fi
    if [ "$av" = "NO_HE_PODIDO_MIRAR" ]; then ciego "$etq apply: $(printf '%s' "$a" | jq_ code)"; return 2; fi
    mal "$etq apply = $av ($(printf '%s' "$a" | jq_ code))"; return 1
  fi
  ok "$etq  plan_hash=${ph:0:12}…  $(printf '%s' "$a" | jq_ code)"
  printf '%s' "$a" > "$OUT/$etq.result.json"
  return 0
}

# ── --selftest ────────────────────────────────────────────────────────────────────────
# Un arnes sin control positivo no es un arnes: si no se puede poner ROJO, un verde suyo
# no dice nada. Esto comprueba que sabe dar SUS TRES respuestas, contra el mismo motor.
if [ "${1:-}" = "--selftest" ]; then
  st_fallos=0
  st() { # st <esperado> <etiqueta> <rc-observado>
    if [ "$1" = "$3" ]; then printf '  ok    %s -> rc=%s\n' "$2" "$3"
    else printf '  ⛔ %s -> rc=%s, esperaba %s\n' "$2" "$3" "$1"; st_fallos=$((st_fallos+1)); fi
  }
  OUT="$OUT/selftest"; mkdir -p "$OUT" || exit 2

  # A · HALLAZGO: un workspace que no existe. El motor lo rechaza y el arnes debe decir 1.
  printf '{"work_kind":"implementation","workspace_id":"00000000-0000-7000-8000-000000000000",\n "title":"selftest","brief_md":"selftest","context_refs":[],"priority":"p1",\n "owner_kind":"user","owner_ref":"%s","provenance_kind":"human","provenance_ref":"selftest"}\n' \
    "$USERID" > "$OUT/a.json"
  mando /v1/m/sessions/work-items "$OUT/a.json" st_a >/dev/null 2>&1
  st 1 "A · workspace inexistente = HALLAZGO" "$?"

  # B · NO HE PODIDO MIRAR: un puerto donde no escucha nadie.
  # ⛔ ESTE CASO ACEPTABA «1 o 2» Y POR ESO NO VALIA PARA NADA.
  # Con esa tolerancia pasaba sobre la propia confusion que el arnes existe para no
  # cometer —motor caido reportado como HALLAZGO— asi que el `--selftest` afirmaba
  # «se pueden dar las tres respuestas» sin que NINGUN caso probara la tercera. Lo
  # cazo una revision sobre mi diff, y tenia razon: A prueba el 1, C prueba la
  # tolerancia, y el 2 no lo probaba nadie. Aqui se exige 2 EXACTO.
  ( B="http://127.0.0.1:1"; mando /v1/m/sessions/work-items "$OUT/a.json" st_b >/dev/null 2>&1 )
  rc=$?
  if [ "$rc" = 2 ]; then printf '  ok    B · motor inalcanzable -> rc=2 (NO HE PODIDO MIRAR)\n'
  else printf '  ⛔ B · motor inalcanzable -> rc=%s, esperaba 2: un motor caido no es un hallazgo\n' "$rc"; st_fallos=$((st_fallos+1)); fi

  # C · TOLERANCIA: el MISMO mando que en A, pero declarando esperado el codigo que A
  # devolvio de verdad. Se lee del fichero, no se teclea: un codigo tecleado a mano es la
  # forma de que este control pase por casualidad -- y de hecho la primera version de C
  # tolero `owner_ineligible` sobre una ruta que devuelve OTRO codigo, asi que el control
  # fallaba por construccion. Lo cazo el propio selftest, que es para lo que existe.
  code_a=$(jq_ code < "$OUT/st_a.validate.json" 2>/dev/null)
  antes=$fallos
  MANDO_TOLERA="$code_a" mando /v1/m/sessions/work-items "$OUT/a.json" st_c >/dev/null 2>&1
  rc=$?
  if [ "$fallos" = "$antes" ] && [ "$rc" = 3 ]; then
    printf '  ok    C · el codigo tolerado «%s» sale rc=3 y no incrementa fallos\n' "$code_a"
  else
    printf '  ⛔ C · tolerando «%s»: rc=%s, fallos %s -> %s (esperaba rc=3 y sin incremento)\n' \
      "$code_a" "$rc" "$antes" "$fallos"; st_fallos=$((st_fallos+1))
  fi

  printf '\ne2e-work-kernel selftest: %s\n' "$([ "$st_fallos" = 0 ] && echo 'las tres respuestas se pueden dar' || echo "$st_fallos FALLO(S)")"
  exit $([ "$st_fallos" = 0 ] && echo 0 || echo 1)
fi

# ── MODO ────────────────────────────────────────────────────────────────────────────
# Con DOS credenciales de sesion gestionada se mide el relevo DE VERDAD. Sin ellas se mide
# todo lo demas y los pasos del relevo dicen NO HE PODIDO MIRAR — nunca un verde vacio.
#
# Como se obtienen: lanza dos runs (`POST /v1/m/sessions/runs`) y lee el triple que el motor
# pone en el entorno de cada hijo (`OLIVARES_WORK_SESSION_ID` y `OLIVARES_WORK_TOKEN`).
SID_A=${OLIVARES_E2E_SID_A:-}; TOKEN_A=${OLIVARES_E2E_TOKEN_A:-}
SID_B=${OLIVARES_E2E_SID_B:-}; TOKEN_B=${OLIVARES_E2E_TOKEN_B:-}
MODO_SESION=0
if [ -n "$SID_A" ] && [ -n "$TOKEN_A" ] && [ -n "$SID_B" ] && [ -n "$TOKEN_B" ]; then
  MODO_SESION=1
  printf '\033[1mmodo:\033[0m dos sesiones gestionadas — el relevo se MIDE\n'
else
  printf '\033[1mmodo:\033[0m sin credenciales de sesion — el relevo quedara SIN MEDIR\n'
  printf '        (define OLIVARES_E2E_SID_A/TOKEN_A y _B para medirlo)\n'
fi

paso "1 · CREAR el objetivo de trabajo"
# ⛔ EL DUENO DECIDE SI EL RELEVO SE PUEDE MEDIR, y no es un detalle de forma.
# Un lease lo sostiene LA SESION QUE POSEE EL ITEM: con `owner_kind:"user"` ninguna sesion
# puede tomarlo (`owner_ineligible`, y el motor tiene razon). Asi que cuando hay credenciales
# de sesion el item nace a nombre de A; sin ellas nace a nombre del operador y los pasos del
# relevo se declaran SIN MEDIR, que es lo honesto.
if [ "$MODO_SESION" = 1 ]; then
  OWNER_KIND=session; OWNER_REF=$SID_A
else
  OWNER_KIND=user;    OWNER_REF=$USERID
fi
cat > "$OUT/crear.json" <<EOF
{"work_kind":"implementation","workspace_id":"$WS",
 "title":"B1 e2e: orquestacion del kernel de trabajo","brief_md":"Recorrido punta a punta del kernel de trabajo.",
 "context_refs":[],"priority":"p1","owner_kind":"$OWNER_KIND","owner_ref":"$OWNER_REF",
 "provenance_kind":"human","provenance_ref":"hub-kernel:B1-e2e",
 "acceptance":[{"criterion_key":"relevo","ordinal":0,"statement":"El fence avanza y el titular cambia","required":true}]}
EOF
# ⛔ EL ID SALE DE ESTA CORRIDA, NO DE UN FICHERO QUE PUEDE SER DE OTRA.
# `$OUT` no se limpia y `mando` solo escribe `*.result.json` cuando ACIERTA, asi que si
# la creacion fallaba el guion leia el item de la corrida ANTERIOR y recorria los pasos
# 2-5 contra un trabajo que no habia creado — pudiendo dar VERDE sobre el. Y con `$OUT`
# bajo un `/tmp` fijo, en una caja compartida ese fichero lo puede plantar otro.
# Reproducido por una revision sobre mi diff. Ahora el directorio es nuevo por corrida.
mando /v1/m/sessions/work-items "$OUT/crear.json" crear || true
ITEM=""
[ -r "$OUT/crear.result.json" ] && ITEM=$(jq_ result_id < "$OUT/crear.result.json")
if [ -z "$ITEM" ] || es_ilegible "$ITEM"; then
  ciego "sin item de ESTA corrida: el resto del recorrido no tiene sujeto"; exit 2
fi
ok "item = $ITEM"

paso "2 · TRANSICION draft -> ready"
printf '{"command":"item.ready"}\n' > "$OUT/ready.json"
V=$(api GET "/v1/m/sessions/work-items/$ITEM" - | jq_ item.version)
# El ETag de este plano es "v<version>", no "<version>": con el formato desnudo el motor
# responde invalid_command, y el primer recorrido de esta bateria se lo comio entero.
# Lo dice el propio check: evidence_ref = "\"v1\"".
mando "/v1/m/sessions/work-items/$ITEM/transitions" "$OUT/ready.json" ready -H "If-Match: \"v$V\"" || true
EST=$(api GET "/v1/m/sessions/work-items/$ITEM" - | jq_ item.status)
[ "$EST" = "ready" ] && ok "estado = ready" || mal "estado = $EST, esperaba ready"

paso "3 · LEASE: la sesion A toma el SUYO, con SU credencial"
# `holder_sid` es autoridad de EJECUCION, no metadato de enrutado: `leasePrincipalMatches`
# exige que el SessionID AUTENTICADO sea exactamente el holder, para que ninguna credencial
# pueda tomar el trabajo de una sesion ajena. Por eso este paso va con el token de A, no con
# el del operador — y por eso sin credenciales de sesion no se puede medir.
if [ "$MODO_SESION" != 1 ]; then
  ciego "lease: hacen falta DOS sesiones gestionadas; con un token de operador el motor rehusa (owner_ineligible, y es la garantia funcionando)"
  ciego "relevo: sin medir (no hubo lease que mover)"
  SIN_LEASE=1
else
  V=$(api GET "/v1/m/sessions/work-items/$ITEM" - | jq_ item.version)
  cat > "$OUT/acquire.json" <<EOF
{"holder_sid":"$SID_A","ttl_seconds":600}
EOF
  MANDO_TOK="$TOKEN_A" mando "/v1/m/sessions/work-items/$ITEM/lease/acquire" "$OUT/acquire.json" acquire \
    -H "If-Match: \"v$V\"" || true
  L=$(api GET "/v1/m/sessions/work-items/$ITEM/lease" -)
  printf '%s\n' "$L" > "$OUT/lease-A.json"
  H1=$(printf '%s' "$L" | jq_ holder_sid); F1=$(printf '%s' "$L" | jq_ fence)
  E1=$(printf '%s' "$L" | jq_ state)
  if [ "$H1" = "$SID_A" ]; then ok "titular A = ${H1:0:20}… (fence $F1, $E1)"
  else mal "titular = $H1, esperaba $SID_A"; SIN_LEASE=1; fi
fi

paso "4 · RELEVO: se mueve la PROPIEDAD a B, y B toma el lease"
# ⛔ EL RELEVO ES PRIMERO DE PROPIEDAD, NO DE LEASE, y esta version del guion existe porque
# la anterior lo tenia al reves. Un `lease.takeover` directo de A a B se RECHAZA
# `owner_ineligible`: una sesion no puede sostener el lease de un item que no es suyo.
# Mover el dueno REVOCA el lease del titular viejo y AVANZA el fence en el mismo acto, y
# solo entonces el nuevo dueno puede adquirir. `takeover` tiene otro sitio: exige
# `principal.Admin` y el fence vigente — un operador retirando un lease de quien SIGUE
# siendo el dueno. La regla vive tambien en modules/sessions/work_lease.go.
if [ "${SIN_LEASE:-0}" = 1 ]; then
  ciego "relevo: sin medir"
else
  V=$(api GET "/v1/m/sessions/work-items/$ITEM" - | jq_ item.version)
  printf '{"owner_kind":"session","owner_ref":"%s"}\n' "$SID_B" > "$OUT/assign.json"
  mando "/v1/m/sessions/work-items/$ITEM/assignments" "$OUT/assign.json" assign -H "If-Match: \"v$V\"" || true
  LR=$(api GET "/v1/m/sessions/work-items/$ITEM/lease" -)
  printf '%s\n' "$LR" > "$OUT/lease-revocado.json"
  ER=$(printf '%s' "$LR" | jq_ state); FR=$(printf '%s' "$LR" | jq_ fence)
  # LA GARANTIA: mover el dueno tiene que CORTAR al titular viejo, no solo re-etiquetar.
  if [ "$ER" = "revoked" ]; then ok "al mover el dueno, el lease de A queda REVOCADO (fence $F1 -> $FR)"
  else mal "tras mover el dueno el lease quedo en '$ER', esperaba 'revoked': el titular viejo NO queda cortado"; fi

  V=$(api GET "/v1/m/sessions/work-items/$ITEM" - | jq_ item.version)
  printf '{"holder_sid":"%s","ttl_seconds":600}\n' "$SID_B" > "$OUT/acquire-b.json"
  MANDO_TOK="$TOKEN_B" mando "/v1/m/sessions/work-items/$ITEM/lease/acquire" "$OUT/acquire-b.json" acquire-b \
    -H "If-Match: \"v$V\"" || true
  L2=$(api GET "/v1/m/sessions/work-items/$ITEM/lease" -)
  printf '%s\n' "$L2" > "$OUT/lease-B.json"
  H2=$(printf '%s' "$L2" | jq_ holder_sid); F2=$(printf '%s' "$L2" | jq_ fence)
  if [ "$H2" = "$SID_B" ]; then ok "titular B = ${H2:0:20}… (fence $F2)"
  else mal "titular = $H2, esperaba $SID_B"; fi
  # EL FENCE MONOTONO ES LA PROPIEDAD ENTERA: sin el, el titular viejo podria seguir
  # escribiendo con su credencial, que es lo que un lease con fence existe para cortar.
  if es_ilegible "$F1" || es_ilegible "$F2"; then
    # Sin esto, un cuerpo ILEGIBLE caia al `else` y publicaba «el titular viejo no queda
    # cortado» — un hallazgo de SEGURIDAD fabricado a partir de un fallo de lectura.
    ciego "no leo el fence en los dos lados del relevo (respuesta ilegible)"
  elif [ "$F2" -gt "$F1" ] 2>/dev/null; then
    ok "el fence AVANZA en el relevo: $F1 -> $F2"
  else
    mal "el fence NO avanza en el relevo: $F1 -> $F2 (el titular viejo no queda cortado)"
  fi
fi

paso "5 · La cadena de eventos, que es lo que hace auditable la orquestacion"
EV=$(api GET "/v1/m/sessions/work-items/$ITEM/events" -)
printf '%s\n' "$EV" > "$OUT/eventos.json"
# ⛔ ESTE PASO PASABA POR NO ENCONTRAR NADA. Con la lista vacia, `sorted([]) == []`, el
# `if` era falso y el guion imprimia «secuencia monotona»: certificaba en VERDE justo el
# fallo que existe para cazar. Lo cazo una revision sobre mi diff, y es la misma clase que
# mi propio test de CSP evita — la direccion de NO-disparo. Ahora exige un SUELO y los
# tipos que el recorrido tuvo que producir.
MIN_EV=2; [ "${SIN_LEASE:-0}" = 1 ] || MIN_EV=5
python3 - "$OUT/eventos.json" "$MIN_EV" "${SIN_LEASE:-0}" <<'PY'
import json,sys
try:
    d=json.load(open(sys.argv[1]))
except Exception as e:
    print(f"  respuesta ilegible: {e}"); sys.exit(2)
minimo=int(sys.argv[2]); sin_lease=sys.argv[3]=='1'
it=d.get('items')
if it is None:
    print("  la respuesta no trae 'items'"); sys.exit(2)
print(f"  eventos: {len(it)} (minimo esperado {minimo})")
for e in it:
    print(f"    seq={str(e.get('seq')):<3} {str(e.get('event_type')):<28} actor={e.get('actor_kind')}")
if len(it) < minimo:
    print(f"  la cadena trae {len(it)} eventos y el recorrido produjo al menos {minimo}"); sys.exit(1)
seqs=[e.get('seq') for e in it]
if seqs != sorted(seqs) or len(set(seqs))!=len(seqs):
    print("  la secuencia no es monotona y sin huecos"); sys.exit(1)
exigidos={'work.item.created','work.item.transitioned'}
if not sin_lease:
    exigidos |= {'work.lease.acquired','work.owner.changed'}
falta=exigidos - {e.get('event_type') for e in it}
if falta:
    print(f"  la cadena no registra {sorted(falta)}: el recorrido los produjo"); sys.exit(1)
PY
rc_ev=$?
case $rc_ev in
  0) ok "cadena de eventos: monotona, con suelo y con los tipos del recorrido" ;;
  2) ciego "cadena de eventos ilegible" ;;
  *) mal "cadena de eventos" ;;
esac

printf '\n\033[1m=== VEREDICTO ===\033[0m\n'
printf '  fallos: %d   ciegos: %d\n' "$fallos" "$ciegos"
printf '  evidencia en: %s\n' "$OUT"
[ "$fallos" -gt 0 ] && exit 1
[ "$ciegos" -gt 0 ] && exit 2
exit 0
