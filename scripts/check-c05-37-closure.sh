#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c05-37-closure.sh — C05-37 no se declara verde sin una traza NOMBRADA que lleve el HOST.
#
# ⛔ QUÉ AFIRMA ESTE GATE, Y QUÉ NO. Es un guardián de CIERRE, no un monitor de salud. Un `0`
# significa «el contrato no se contradice con la evidencia que él mismo nombra», NUNCA «la
# cadena de entrega está funcionando ahora mismo». Con los dos booleanos en `false` sale `0` sin
# mirar ninguna traza, que es lo correcto para un guardián de cierre y sería un verde falso si
# alguien lo presentara como salud. Lo escribo aquí porque el contraste `sol max` señaló que la
# distinción no estaba dicha en ningún sitio (an internal design note (not shipped)).
#
# ⛔ POR QUE ESTE GATE NO ES EL QUE PEDIA EL BRIEF, Y LA DIFERENCIA ES UNA MEDIDA.
#
# El brief C05-37 pedia «un probe que falle si el runbook declara verde sin SELECT con host
# licenses.olivares.ai». Ese probe NO SE PUEDE ESCRIBIR: `webhook_events.endpoint` guarda la
# RUTA (`/webhooks/dodo`), no el host — lo escribe la constante `DODO_WEBHOOK_SOURCE.endpoint`
# en `commercial/license-worker/src/store/db.ts`, y no hay ninguna columna de host. Una fila de
# esa tabla NO SABE por que hostname entro. Un gate escrito contra ese criterio saldria verde
# mirando una columna que no puede contestar la pregunta.
#
# La evidencia que SI lleva el host es la traza del Worker (`wrangler tail --format json`).
#
# ⛔ QUE ACREDITA EXACTAMENTE, dicho tras la segunda pasada del contraste (P-02): *«una peticion
# con estas cabeceras, a este host y esta ruta, que el Worker ACEPTO con 202»*. **No acredita que
# la enviara Svix**: el `webhook-id` y el `user-agent` viven en una traza que produce quien
# declara el verde, asi que una sonda propia bien vestida pasaria. Eso no se arregla dentro del
# artefacto; lo cerraria una procedencia externa —un delivery log del proveedor ligado al id—, y
# hoy no la hay (`/webhooks/{id}/attempts` no existe en la API de Dodo: 403 con HTML). Se rebaja
# el claim en vez de fingir que se cierra.
#
# ⛔ UNA SONDA PROPIA NO ES EL REMITENTE. El 2026-08-27 una verificacion con peticiones propias
# dio por buena una cadena que llevaba 100 minutos rota, porque desde esta caja el camino SI
# funciona: lo que no entra es el remitente. Por eso se exige el `webhook-id` del emisor real Y
# su user-agent: un identificador a solas es un campo que quien fabrica la traza controla.
#
# ⛔ Y LLEGAR NO ES SER ACEPTADO. El 2026-08-28T17:06Z, con la regla del borde ya arreglada, las
# entregas SI llegaban y el Worker las contestaba 401 porque el secreto del endpoint no era el
# suyo. Un gate que solo mirase la llegada habria declarado verde una cadena que no entrega nada.
#
# ⛔ Y UN VERDE NOMBRA SU EVIDENCIA. Antes bastaba con que ALGUNA traza del directorio tuviera
# una llegada buena, asi que un verde podia cabalgar para siempre sobre una medida historica —
# incluida una tomada en una ventana transitoria que despues se deshizo. Ahora el contrato
# nombra `trace` y `webhook_id`, y mover el verde obliga a nombrar la medida nueva.
#
# Salidas: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
set -uo pipefail

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-c05-37-closure: ⛔ NO HE PODIDO MIRAR — no estoy en un repositorio y no me han dado OLIVARES_ROOT" >&2; exit 2; }
cd "$RAIZ" || { echo "check-c05-37-closure: ⛔ NO HE PODIDO MIRAR — no puedo entrar en $RAIZ" >&2; exit 2; }

command -v python3 >/dev/null 2>&1 || { echo "check-c05-37-closure: ⛔ NO HE PODIDO MIRAR — no hay python3 en el PATH" >&2; exit 2; }

JSON="${OLIVARES_C0537_JSON:-design/c05-37-closure.json}"
[ -r "$JSON" ] || { echo "check-c05-37-closure: ⛔ NO HE PODIDO MIRAR — no puedo leer $JSON" >&2; exit 2; }

python3 - "$JSON" <<'PY'
import json, pathlib, sys

N = "check-c05-37-closure"

def cannot(m):
    print("%s: ⛔ NO HE PODIDO MIRAR — %s" % (N, m), file=sys.stderr)
    raise SystemExit(2)

def fail(m):
    print("%s: ⛔ HALLAZGO — %s" % (N, m), file=sys.stderr)
    raise SystemExit(1)

# ⛔ Toda forma inesperada de la traza sale por `cannot`, NUNCA por una excepcion de Python.
# Una excepcion no capturada sale con codigo 1, o sea «hallazgo», y este arbol tiene escrito que
# confundir «no he podido mirar» con «roto» cuesta tanto como confundirlo con «limpio».
def need(v, tipo, donde):
    if not isinstance(v, tipo):
        cannot("%s no es %s sino %r" % (donde, getattr(tipo, "__name__", tipo), type(v).__name__))
    return v

try:
    d = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot("%s no es JSON legible: %s" % (sys.argv[1], e))
need(d, dict, "el contrato")

if d.get("schema") != "c05-37-closure/v1":
    cannot("esquema desconocido %r" % d.get("schema"))
for k in ("sandbox_green", "production_green"):
    if not isinstance(d.get(k), bool):
        cannot("%s tiene que ser booleano, es %r" % (k, d.get(k)))

runbook = pathlib.Path(str(d.get("runbook", "")))
if not runbook.is_file():
    cannot("no encuentro el runbook %s" % runbook)
try:
    doc = runbook.read_text(encoding="utf-8")
except Exception as e:
    cannot("no puedo leer %s: %s" % (runbook, e))

# 1 · La PROSA no puede ir por delante de la medida, y se comprueba POR CADA booleano abierto.
#     Antes solo miraba cuando los DOS estaban en false: con sandbox ya cerrado, una frase que
#     diera por cerrado lo de produccion habria pasado sin que nada la viera.
claims = need(d.get("doc_must_not_claim_while_false") or [], list, "doc_must_not_claim_while_false")
if not claims:
    cannot("doc_must_not_claim_while_false esta vacio: sin formas prohibidas el gate no mira nada")
if not (d["sandbox_green"] and d["production_green"]):
    for c in claims:
        if str(c).lower() in doc.lower():
            fail("%s afirma %r con el contrato todavia abierto (sandbox=%s, produccion=%s)"
                 % (runbook, c, d["sandbox_green"], d["production_green"]))

# ⛔ TIPOS DEL CONTRATO. Sin esto, un `host` o un `status` que fuesen listas u objetos podian
# satisfacer la igualdad de Python contra un evento con el MISMO valor raro y dar un verde.
for k, t in (("required_host_sandbox", str), ("required_host_production", str),
             ("required_path", str), ("required_method", str), ("required_status", int),
             ("required_user_agent_substring", str), ("sender_webhook_id_prefix", str),
             ("trace_dir", str)):
    if k in d and not isinstance(d[k], t) or isinstance(d.get(k), bool):
        cannot("%s tiene que ser %s, es %r" % (k, t.__name__, type(d.get(k)).__name__))
if not isinstance(d.get("required_status"), int):
    cannot("required_status es obligatorio y entero: sin el, una llegada RECHAZADA contaria como verde")

prefix = str(d.get("sender_webhook_id_prefix") or "msg_")
own = str(d.get("own_probe_webhook_id_marker") or "replay")
path_req = str(d.get("required_path") or "")
method_req = d.get("required_method")
status_req = d.get("required_status")
ua_req = str(d.get("required_user_agent_substring") or "")
if not path_req or not method_req:
    cannot("el contrato no declara required_path/required_method")

def leer_traza(rel):
    f = pathlib.Path(str(rel))
    # ⛔ La traza tiene que vivir DENTRO del directorio de evidencia declarado. Sin esto, el
    # contrato podia apuntar a cualquier fichero del arbol y el verde dejaba de ser auditable
    # donde se busca la evidencia.
    tdir = str(d.get("trace_dir") or "")
    if not tdir:
        cannot("el contrato no declara trace_dir")
    try:
        f.relative_to(tdir)
    except ValueError:
        cannot("la traza nombrada (%s) no vive bajo trace_dir (%s)" % (f, tdir))
    if not f.is_file():
        cannot("la traza nombrada por el contrato no existe: %s" % f)
    try:
        data = json.loads(f.read_text(encoding="utf-8"))
    except Exception as e:
        cannot("la traza %s no es JSON legible: %s" % (f, e))
    need(data, list, "la traza %s" % f)
    return f, data

def revisar_forma(data, f):
    """⛔ TODA la traza se revisa ANTES de buscar. Revisando sobre la marcha, `[bueno, []]`
    salia 0 y `[[], bueno]` salia 2: el mismo defecto perdonado o no segun el ORDEN."""
    for ev in data:
        if not isinstance(ev, dict):
            cannot("la traza %s trae un evento que no es un objeto: %r" % (f, type(ev).__name__))
        h = ev.get("headers")
        if h is not None and not isinstance(h, dict):
            cannot("un evento de %s trae headers que no son un objeto: %r" % (f, type(h).__name__))
        u = ev.get("url")
        if u is not None and not isinstance(u, str):
            cannot("un evento de %s trae url que no es cadena: %r" % (f, type(u).__name__))
        if isinstance(h, dict) and h.get("webhook-id") is not None and not isinstance(h.get("webhook-id"), str):
            cannot("un evento de %s trae webhook-id que no es cadena" % f)

def es_llegada_aceptada(ev, host, wid_req):
    """Todas las condiciones, y cada una existe por un defecto medido."""
    h = ev.get("headers")
    if not isinstance(h, dict):
        return False
    if h.get("host") != host:                       # host EXACTO, no contencion
        return False
    if ev.get("method") != method_req:
        return False
    url = ev.get("url")
    if not isinstance(url, str):
        return False
    # RUTA EXACTA, y comparada como RUTA. Con `endswith` bastaba `/lo-que-sea/webhooks/dodo`,
    # que es otra superficie. Y reconstruir `https://<host><ruta>` para compararlo entero
    # comprobaba el host DOS veces: la comprobacion de ruta tapaba a la de host, asi que un
    # mutante que aceptara el host por contencion sobrevivia. Cada propiedad se mira una vez.
    sin_query = url.split("?", 1)[0].split("#", 1)[0]
    resto = sin_query.split("://", 1)[1] if "://" in sin_query else sin_query
    barra = resto.find("/")
    ruta = resto[barra:] if barra >= 0 else "/"
    autoridad = resto[:barra] if barra >= 0 else resto
    if ruta != path_req:
        return False
    # ⛔ Y el host de la URL tiene que ser el MISMO que la cabecera. Una traza internamente
    # contradictoria —cabecera del host bueno, url de otro— pasaba mirando solo la cabecera.
    if autoridad.split("@")[-1].split(":")[0] != host:
        return False
    wid = h.get("webhook-id")
    if not isinstance(wid, str) or not wid.startswith(prefix):
        return False
    if own and own in wid:                          # una sonda nuestra no es el remitente
        return False
    ua = h.get("user-agent")
    if ua_req and (not isinstance(ua, str) or ua_req not in ua):
        return False
    # Un evento por lo demas valido y SIN `status` es una traza incompleta, no un rechazo: sale
    # por «no he podido mirar». Un status distinto (401, 500) si es un rechazo y es hallazgo.
    if "status" not in ev:
        cannot("un evento por lo demas valido no trae `status`: la traza esta incompleta")
    if ev.get("status") != status_req:
        return False
    if wid_req is not None and wid != wid_req:
        return False
    return True

problemas = []
for flag, hostkey, evkey, quien in (
        ("sandbox_green", "required_host_sandbox", "sandbox_evidence", "sandbox"),
        ("production_green", "required_host_production", "production_evidence", "produccion")):
    if not d[flag]:
        continue
    host = d.get(hostkey)
    if not host:
        cannot("%s es true pero %s no esta declarado" % (flag, hostkey))
    ev_decl = d.get(evkey)
    if not isinstance(ev_decl, dict):
        # ⛔ Un verde SIN evidencia nombrada es «no he podido mirar», no un hallazgo: lo que
        # falta es el puntero, y sin el no se sabe si la cadena esta bien o mal.
        cannot("%s es true y el contrato no NOMBRA su evidencia en %s "
               "(hace falta {trace, webhook_id})" % (flag, evkey))
    wid_req = ev_decl.get("webhook_id")
    if not isinstance(wid_req, str) or not wid_req:
        cannot("%s no declara un webhook_id" % evkey)
    f, data = leer_traza(ev_decl.get("trace"))
    revisar_forma(data, f)
    encontrado = None
    for ev in data:
        if es_llegada_aceptada(ev, host, wid_req):
            encontrado = ev
            break
    if encontrado is None:
        problemas.append(
            "%s=true y la traza que el contrato nombra (%s) NO contiene la llegada aceptada que "
            "declara: %s %s a %s, user-agent %r, webhook-id %s, status %s"
            % (flag, f, method_req, path_req, host, ua_req, wid_req, status_req))
    else:
        print("%s: %s VERDE — %s %s%s %s status=%s @ %s (%s)"
              % (N, quien, encontrado.get("method"), host, path_req, wid_req,
                 encontrado.get("status"), encontrado.get("ts_utc", "?"), f))

if problemas:
    fail("; ".join(problemas))

print("%s: OK — sandbox_green=%s production_green=%s, y cada verde NOMBRA la traza que lo sostiene. "
      "Esto es un guardian de CIERRE: no afirma que la cadena funcione ahora."
      % (N, d["sandbox_green"], d["production_green"]))
PY
exit $?
