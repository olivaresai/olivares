#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c05-37-closure.sh — C05-37 needs a named trace that includes the host before closure.
#
# This is a closure guard, not a health monitor: 0 means the contract does not contradict
# its named evidence, not that delivery works now. Both booleans false return 0 without
# reading a trace. The distinction was missing in the `sol max` review
# (an internal design note (not shipped)).
#
# The brief requested a SELECT proving host licenses.olivares.ai, but `webhook_events.endpoint`
# stores only the route `/webhooks/dodo`, from `DODO_WEBHOOK_SOURCE.endpoint` in
# `commercial/license-worker/src/store/db.ts`. There is no host column, so a database
# row cannot prove the entry hostname. Worker traces (`wrangler tail --format json`) can.
#
# The trace proves a request with these headers, host and route was accepted with 202
# (review P-02). It cannot prove Svix sent it: a self-probe can supply `webhook-id` and
# `user-agent`. External provenance, such as a provider delivery log tied to the id,
# would establish that; Dodo's `/webhooks/{id}/attempts` API returned 403 HTML instead.
#
# On 2026-08-27, self-probes falsely cleared a delivery chain broken for 100 minutes:
# the local path worked while the sender's path did not. Require the real sender's
# webhook-id and user-agent, while retaining the provenance limit above.
# Arrival alone also fails: on 2026-08-28T17:06Z the fixed edge delivered requests, but
# the Worker rejected them with 401 because the endpoint secret did not match.
#
# The contract names `trace` and `webhook_id`; a new green declaration must name new
# evidence instead of reusing any historical successful trace, including a transient one.
# Exit: 0 clean · 1 finding · 2 could not check.
set -uo pipefail

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-c05-37-closure: ⛔ COULD NOT CHECK — outside a repository and OLIVARES_ROOT is not set" >&2; exit 2; }
cd "$RAIZ" || { echo "check-c05-37-closure: ⛔ COULD NOT CHECK — cannot enter $RAIZ" >&2; exit 2; }

command -v python3 >/dev/null 2>&1 || { echo "check-c05-37-closure: ⛔ COULD NOT CHECK — python3 is not in PATH" >&2; exit 2; }

JSON="${OLIVARES_C0537_JSON:-design/c05-37-closure.json}"
[ -r "$JSON" ] || { echo "check-c05-37-closure: ⛔ COULD NOT CHECK — cannot read $JSON" >&2; exit 2; }

python3 - "$JSON" <<'PY'
import json, pathlib, sys

N = "check-c05-37-closure"

def cannot(m):
    print("%s: ⛔ COULD NOT CHECK — %s" % (N, m), file=sys.stderr)
    raise SystemExit(2)

def fail(m):
    print("%s: ⛔ FINDING — %s" % (N, m), file=sys.stderr)
    raise SystemExit(1)

# ⛔ Toda forma inesperada de la traza sale por `cannot`, NUNCA por una excepcion de Python.
# Una excepcion no capturada sale con codigo 1, o sea «hallazgo», y este arbol tiene escrito que
# confundir «no he podido mirar» con «roto» cuesta tanto como confundirlo con «limpio».
def need(v, tipo, donde):
    if not isinstance(v, tipo):
        cannot("%s must be %s, got %r" % (donde, getattr(tipo, "__name__", tipo), type(v).__name__))
    return v

try:
    d = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot("%s is not readable JSON: %s" % (sys.argv[1], e))
need(d, dict, "the contract")

if d.get("schema") != "c05-37-closure/v1":
    cannot("unknown schema %r" % d.get("schema"))
for k in ("sandbox_green", "production_green"):
    if not isinstance(d.get(k), bool):
        cannot("%s must be boolean, got %r" % (k, d.get(k)))

runbook = pathlib.Path(str(d.get("runbook", "")))
if not runbook.is_file():
    cannot("cannot find runbook %s" % runbook)
try:
    doc = runbook.read_text(encoding="utf-8")
except Exception as e:
    cannot("cannot read %s: %s" % (runbook, e))

# 1 · La PROSA no puede ir por delante de la medida, y se comprueba POR CADA booleano abierto.
#     Antes solo miraba cuando los DOS estaban en false: con sandbox ya cerrado, una frase que
#     diera por cerrado lo de produccion habria pasado sin que nada la viera.
claims = need(d.get("doc_must_not_claim_while_false") or [], list, "doc_must_not_claim_while_false")
if not claims:
    cannot("doc_must_not_claim_while_false is empty: without prohibited patterns this check verifies nothing")
if not (d["sandbox_green"] and d["production_green"]):
    for c in claims:
        if str(c).lower() in doc.lower():
            fail("%s claims %r while the contract remains open (sandbox=%s, production=%s)"
                 % (runbook, c, d["sandbox_green"], d["production_green"]))

# ⛔ TIPOS DEL CONTRATO. Sin esto, un `host` o un `status` que fuesen listas u objetos podian
# satisfacer la igualdad de Python contra un evento con el MISMO valor raro y dar un verde.
for k, t in (("required_host_sandbox", str), ("required_host_production", str),
             ("required_path", str), ("required_method", str), ("required_status", int),
             ("required_user_agent_substring", str), ("sender_webhook_id_prefix", str),
             ("trace_dir", str)):
    if k in d and not isinstance(d[k], t) or isinstance(d.get(k), bool):
        cannot("%s must be %s, got %r" % (k, t.__name__, type(d.get(k)).__name__))
if not isinstance(d.get("required_status"), int):
    cannot("required_status must be an integer: without it, a rejected delivery would pass")

prefix = str(d.get("sender_webhook_id_prefix") or "msg_")
own = str(d.get("own_probe_webhook_id_marker") or "replay")
path_req = str(d.get("required_path") or "")
method_req = d.get("required_method")
status_req = d.get("required_status")
ua_req = str(d.get("required_user_agent_substring") or "")
if not path_req or not method_req:
    cannot("the contract does not declare required_path/required_method")

def leer_traza(rel):
    f = pathlib.Path(str(rel))
    # ⛔ La traza tiene que vivir DENTRO del directorio de evidencia declarado. Sin esto, el
    # contrato podia apuntar a cualquier fichero del arbol y el verde dejaba de ser auditable
    # donde se busca la evidencia.
    tdir = str(d.get("trace_dir") or "")
    if not tdir:
        cannot("the contract does not declare trace_dir")
    try:
        f.relative_to(tdir)
    except ValueError:
        cannot("the named trace (%s) is not under trace_dir (%s)" % (f, tdir))
    if not f.is_file():
        cannot("the trace named by the contract is missing: %s" % f)
    try:
        data = json.loads(f.read_text(encoding="utf-8"))
    except Exception as e:
        cannot("trace %s is not readable JSON: %s" % (f, e))
    need(data, list, "trace %s" % f)
    return f, data

def revisar_forma(data, f):
    """⛔ TODA la traza se revisa ANTES de buscar. Revisando sobre la marcha, `[bueno, []]`
    salia 0 y `[[], bueno]` salia 2: el mismo defecto perdonado o no segun el ORDEN."""
    for ev in data:
        if not isinstance(ev, dict):
            cannot("trace %s contains a non-object event: %r" % (f, type(ev).__name__))
        h = ev.get("headers")
        if h is not None and not isinstance(h, dict):
            cannot("an event in %s has non-object headers: %r" % (f, type(h).__name__))
        u = ev.get("url")
        if u is not None and not isinstance(u, str):
            cannot("an event in %s has a non-string URL: %r" % (f, type(u).__name__))
        if isinstance(h, dict) and h.get("webhook-id") is not None and not isinstance(h.get("webhook-id"), str):
            cannot("an event in %s has a non-string webhook-id" % f)

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
        cannot("an otherwise valid event has no `status`: the trace is incomplete")
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
        cannot("%s is true, but %s is not declared" % (flag, hostkey))
    ev_decl = d.get(evkey)
    if not isinstance(ev_decl, dict):
        # ⛔ Un verde SIN evidencia nombrada es «no he podido mirar», no un hallazgo: lo que
        # falta es el puntero, y sin el no se sabe si la cadena esta bien o mal.
        cannot("%s is true, but the contract does not name its evidence in %s "
               "(requires {trace, webhook_id})" % (flag, evkey))
    wid_req = ev_decl.get("webhook_id")
    if not isinstance(wid_req, str) or not wid_req:
        cannot("%s declares no webhook_id" % evkey)
    f, data = leer_traza(ev_decl.get("trace"))
    revisar_forma(data, f)
    encontrado = None
    for ev in data:
        if es_llegada_aceptada(ev, host, wid_req):
            encontrado = ev
            break
    if encontrado is None:
        problemas.append(
            "%s=true, but the trace named by the contract (%s) does not contain the accepted delivery it "
            "declares: %s %s to %s, user-agent %r, webhook-id %s, status %s"
            % (flag, f, method_req, path_req, host, ua_req, wid_req, status_req))
    else:
        print("%s: %s PASS — %s %s%s %s status=%s @ %s (%s)"
              % (N, quien, encontrado.get("method"), host, path_req, wid_req,
                 encontrado.get("status"), encontrado.get("ts_utc", "?"), f))

if problemas:
    fail("; ".join(problemas))

print("%s: OK — sandbox_green=%s production_green=%s, and each pass names its supporting trace. "
      "This check verifies recorded completion; it does not claim the chain works now."
      % (N, d["sandbox_green"], d["production_green"]))
PY
exit $?
