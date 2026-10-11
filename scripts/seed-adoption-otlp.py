#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Seed demo adoption metrics through a Claude OTLP source and check telemetry APIs. Exit
# 0: verified; 1: rejected; 2: unable to verify.
# 1. Register a Claude source with http_addr=127.0.0.1:14318 and
# resource_labels=team,project,cost_center; supply the tenant, actor and reason to
# olivares sources set.
# 2. Reload or restart the engine. Reconfiguring a source that binds the same port
# requires another port or a restart.
# Use the telemetry lens for seeded metrics. The same prefix and anchor produce the same
# envelope; a different anchor changes it.
# Use a unique session prefix for concurrent runs: team labels do not distinguish
# natural metric keys. A deduplication verdict also requires a declared
# --sla-persistencia bound.
import argparse
import json
import os
import re
import random
import sys
import time
import urllib.error
import urllib.request

RC_LIMPIO, RC_RECHAZADO, RC_UNAVAILABLE = 0, 1, 2

# Emit the session metrics and dimensions recognized by the adoption contract.
# active_users belongs to the organization analytics API, so this OTLP generator
# excludes it. Use the receiver's tool_name attribute for the stored tool dimension.
MODELOS = ("claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5")
HERRAMIENTAS = ("Edit", "Write", "NotebookEdit")

METRICAS = [
    # Each generator returns dimension/value pairs, one datapoint per dimension
    # combination.
    ("claude_code.session.count", lambda r: [({}, 1)]),
    ("claude_code.commit.count", lambda r: [({}, r.randint(1, 6))]),
    ("claude_code.pull_request.count", lambda r: [({}, r.randint(0, 3))]),
    ("claude_code.lines_of_code.count", lambda r: [
        ({"type": "added"}, r.randint(40, 900)),
        ({"type": "removed"}, r.randint(5, 320)),
    ]),
    ("claude_code.token.usage", lambda r: [
        ({"type": t, "model": m}, r.randint(*rango))
        for m in MODELOS
        for t, rango in (("input", (4_000, 30_000)), ("output", (900, 9_000)),
                         ("cacheRead", (0, 40_000)), ("cacheCreation", (0, 6_000)))
    ]),
    ("claude_code.code_edit_tool.decision", lambda r: [
        ({"tool_name": h, "decision": d}, r.randint(*rango))
        for h in HERRAMIENTAS
        for d, rango in (("accept", (3, 40)), ("reject", (0, 7)))
    ]),
    # Send active time in seconds; the receiver converts it to stored milliseconds.
    ("claude_code.active_time.total", lambda r: [
        ({"type": "user"}, r.randint(600, 9_000)),      # 10 minutes to 2.5 hours per session
        ({"type": "cli"}, r.randint(60, 1_800)),
    ]),
]


def contrato_del_motor(raiz="."):
    """Las metricas del plano OTLP que el MOTOR reconoce, leidas de su contrato.

    Devuelve `(nombres, razon_si_no_pude)`. No adivina: si no puede leer el contrato lo dice, y
    quien llama decide — el sembrador avisa y sigue; su banco lo trata como fallo.
    """
    import os
    ruta = os.path.join(raiz, "modules/claudeadoption/contract.go")
    try:
        src = open(ruta, encoding="utf-8").read()
    except OSError as e:
        return set(), f"could not read {ruta} ({type(e).__name__})"
    bloque = re.search(r"^const \($(.*?)^\)$", src, re.S | re.M)
    if not bloque:
        return set(), f"cannot find the first `const (` block in {ruta}"
    nombres = set(re.findall(r'"(claude_code\.[a-z_.]+)"', bloque.group(1)))
    if not nombres:
        return set(), f"the `const (` block in {ruta} contains no `claude_code.*` names"
    # The contract excludes active_users from the OTLP session metrics.
    return nombres - {"claude_code.active_users"}, ""

EQUIPOS = ["platform", "billing", "growth", "sre", "data", "mobile"]


def sobre_otlp(equipos, por_equipo, dias, prefijo, ancla_ns):
    """Construye un OTLP/JSON de metricas. DOS EJECUCIONES CON LOS MISMOS ARGUMENTOS PRODUCEN EL
    MISMO SOBRE, byte a byte — y eso NO era cierto antes.

    ⛔ AQUI ESTABA EL DEFECTO QUE ME RETRACTE DE HABER PUBLICADO (the reviewer, A-02). La version anterior
       hacia `ahora = time.time_ns()` al construir, asi que dos ejecuciones repetian los
       `session.id` y **cambiaban los timestamps**: los sobres NO eran identicos, el receptor
       contesta 200 y el store SUMA el delta
       (`modules/claudeadoption/ingest.go:24-31,87-94`) ⇒ **el re-pase DUPLICA**. Medido:
       356 -> 366 -> 376 con el mismo prefijo y los mismos ids.

       Yo habia publicado lo contrario dos veces, y mi propio control no lo desmentia porque
       mandaba el MISMO sobre en memoria dos veces — eso es una REENTREGA, no dos ejecuciones.

    ⇒ Ahora todo lo aleatorio se deriva de una semilla ESTABLE por `session.id`, y el reloj entra
      por un ANCLA explicita en vez de por `time.time_ns()`. La idempotencia que esto da es la que
      se puede sostener, dicha sin adornos: **para el mismo (prefijo, ancla) el sobre es identico y
      el re-pase no anade nada; con otra ancla es OTRO sobre y SI anade.** El ancla por defecto es
      la medianoche UTC de hoy, asi que el arnes es idempotente dentro del dia y cambia de dia a
      dia — que es exactamente lo que una captura diaria quiere.
    """
    dia = 86_400_000_000_000
    rms = []
    for equipo in equipos:
        for i in range(por_equipo):
            sid = f"{prefijo}-{equipo}-{i:02d}"
            # Seed randomness by session ID so values and offsets do not depend on
            # generation order.
            r = random.Random(sid)
            ts = ancla_ns - r.randint(0, max(dias - 1, 0)) * dia
            rms.append({
                "resource": {"attributes": [
                    {"key": "session.id", "value": {"stringValue": sid}},
                    {"key": "organization.id", "value": {"stringValue": "acme"}},
                    {"key": "team", "value": {"stringValue": equipo}},
                    {"key": "project", "value": {"stringValue": "acme-platform"}},
                ]},
                "scopeMetrics": [{"scope": {"name": "com.anthropic.claude_code"}, "metrics": [
                    {"name": nombre, "sum": {
                        # Emit one datapoint per dimension combination.
                        "dataPoints": [
                            {"asInt": str(valor),
                             "attributes": [{"key": k, "value": {"stringValue": dims[k]}}
                                            for k in sorted(dims)],
                             "startTimeUnixNano": str(ts - 3_600_000_000_000),
                             "timeUnixNano": str(ts)}
                            for dims, valor in genera(r)],
                        "aggregationTemporality": 1, "isMonotonic": True}}
                    for nombre, genera in METRICAS]}],
            })
    return {"resourceMetrics": rms}


def ancla_de(texto):
    """Convierte `YYYY-MM-DD` (o vacio = hoy) en el nanosegundo de su medianoche UTC."""
    import datetime as _dt
    if texto:
        try:
            d = _dt.datetime.strptime(texto, "%Y-%m-%d").replace(tzinfo=_dt.timezone.utc)
        except ValueError:
            raise SystemExit(salir(RC_UNAVAILABLE, f"--ancla {texto!r} is not YYYY-MM-DD"))
    else:
        hoy = _dt.datetime.now(_dt.timezone.utc)
        d = hoy.replace(hour=0, minute=0, second=0, microsecond=0)
    return int(d.timestamp()) * 1_000_000_000


# Load the shared redactor before any request. Register known sensitive values and
# redact all output; abort if the library is unavailable.
def _busca_lib():
    # An explicit OLIVARES_LIB_DIR takes precedence over discovered library paths.
    candidatos = [os.environ.get("OLIVARES_LIB_DIR", ""),
                  os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib")]
    raiz = os.getcwd()
    for _ in range(6):
        candidatos.append(os.path.join(raiz, "scripts", "lib"))
        raiz = os.path.dirname(raiz) or "/"
    for c in candidatos:
        if c and os.path.isfile(os.path.join(c, "redaccion.py")):
            return c
    return None


_lib = _busca_lib()
if _lib is None:
    print("seed-adoption-otlp: ⛔ COULD NOT LOOK: cannot find `scripts/lib/redaccion.py`. "
          "Without output redaction, execution is refused.", file=sys.stderr)
    sys.exit(2)
sys.path.insert(0, _lib)
from redaccion import Redactor, abre  # noqa: E402

_RED = Redactor()


def recuerda_sensibles(url):
    """Declara lo sensible de una URL. El trabajo lo hace la libreria compartida."""
    _RED.recuerda_url(url)


def recuerda_secreto(*piezas):
    """Declara secretos que el guion CONOCE —su token, su tenant— para taparlos aunque el texto que
    los repita venga de fuera. Es lo que cierra el caso de una cabecera reflejada por el receptor."""
    _RED.recuerda(*piezas)


def redacta(texto, url=""):
    """Tapa credenciales en un texto ya compuesto. ⛔ SE APLICA EN LA FRONTERA, NO EN CADA LLAMADA:
    enunciar bien el principio y aplicarlo en dos puntos de llamada es exactamente como se fugo la
    primera vez."""
    if url:
        _RED.recuerda_url(url)
    return _RED(texto)


def di(*partes):
    """La UNICA salida por stdout de este guion. Redacta antes de imprimir, siempre."""
    print(redacta(" ".join(str(x) for x in partes)))


def sanea(url):
    """⛔ LA URL NO SE IMPRIME ENTERA (the reviewer, A-01). Una credencial embebida en el userinfo o en la
    query reaparecia en stderr tal cual. Se conserva esquema, host y puerto; el resto se tapa."""
    try:
        from urllib.parse import urlsplit
        u = urlsplit(url)
        host = u.hostname or "?"
        puerto = f":{u.port}" if u.port else ""
        cred = "<hidden-credential>@" if u.username or u.password else ""
        cola = " (+hidden path/query)" if (u.query or (u.path or "/") != "/") else ""
        return f"{u.scheme}://{cred}{host}{puerto}{cola}"
    except Exception:
        return "<unreadable-url>"


def postear(url, cuerpo):
    # Construct the request inside try so malformed URLs pass through redacted error
    # handling.
    recuerda_sensibles(url)
    try:
        req = urllib.request.Request(url, data=json.dumps(cuerpo).encode(), method="POST")
        req.add_header("Content-Type", "application/json")
        # The shared opener rejects redirects.
        with abre(req, timeout=60) as r:
            return r.status, redacta(r.read().decode()[:200])
    except urllib.error.HTTPError as e:
        # Redact response bodies before returning them to a caller.
        return e.code, redacta(e.read().decode()[:200])
    except Exception as e:
        # Capture the error text here; raise the replacement exception outside the
        # handler.
        motivo = f"{type(e).__name__}: {e}"
    # Raising outside the handler avoids retaining sensitive text in an exception
    # context.
    raise SystemExit(salir(RC_UNAVAILABLE, redacta(
        f"cannot reach the OTLP receiver at {sanea(url)}: {motivo}\n"
        "  Register the source and send SIGHUP to the engine (preconditions 1 and 2).", url)))


def leer(base, ruta, token, tenant, intentos=3):
    """Lee una ruta de la consola. UN atasco transitorio no es ceguera; tres seguidos, si.

    ⛔ MEDIDO, Y CASI LO LEO AL REVES: el mismo `summary` que un `curl` devolvia en **10 ms** hizo
       expirar este lector a los 30 s dos veces seguidas — con nueve carriles en la caja y load1 en
       10,6. Subir el plazo a ciegas habria tapado un atasco real; tratarlo como «no puedo mirar» a
       la primera convierte carga ajena en un veredicto mio. Se reintenta, y si los tres fallan
       ENTONCES es rc 2, que es lo que significa de verdad: no he podido mirar.
    """
    # Register the token and tenant so reflected values are redacted even without a
    # known format.
    recuerda_secreto(token, tenant)

    recuerda_sensibles(base)
    ultimo = None
    for intento in range(intentos):
        try:
            # Construct the request inside try so malformed URLs are handled and redacted.
            req = urllib.request.Request(base.rstrip("/") + ruta)
            req.add_header("Authorization", "Bearer " + token)
            req.add_header("X-Olivares-Tenant", tenant)
            with abre(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except Exception as e:
            ultimo = e
            if intento + 1 < intentos:
                time.sleep(2 * (intento + 1))
    raise SystemExit(salir(RC_UNAVAILABLE, redacta(
        f"cannot read {ruta} after {intentos} attempts: "
        f"{type(ultimo).__name__}: {ultimo}", base)))


def sesiones_de(d, ruta="/v1/m/adoption/summary"):
    """Saca `telemetry.totals.sessions` DISTINGUIENDO ausente de cero.

    ⛔ `d.get("sessions", 0)` convierte «el motor ya no devuelve ese campo» en «no hay sesiones», y
       a partir de ahi el guion acusa al SEMBRADO de lo que es un cambio de respuesta — la misma
       familia que los lectores me encontraron en el hermano (A-03: culpar a una causa que no se ha
       medido). Un campo ausente es «no he podido mirar»; un campo a cero es un veredicto.
    """
    tot = (d.get("telemetry") or {}).get("totals")
    if not isinstance(tot, dict):
        raise SystemExit(salir(RC_UNAVAILABLE,
                               f"{ruta} contains no `telemetry.totals`: the response format "
                               "changed, so the session count cannot be determined"))
    if "sessions" not in tot:
        raise SystemExit(salir(RC_UNAVAILABLE,
                               f"{ruta} contains `telemetry.totals` WITHOUT `sessions`: missing does not mean zero"))
    return tot["sessions"]


def sesiones_de_fila(t, ruta="/v1/m/adoption/teams"):
    """Sesiones de UNA fila de equipo, distinguiendo ausente de cero.

    ⛔ TRES SITIOS DE ESTE FICHERO HACIAN `(t.get("totals") or {}).get("sessions") or 0`, que es
       EXACTAMENTE lo que la docstring de `sesiones_de` condena veinte lineas mas arriba: convierte
       «el motor ya no devuelve ese campo» en «no hay sesiones». Y la excepcion que `mias()` declara
       —«ausente aqui SI es cero»— cubre que la FILA no exista, no que a una fila existente le falte
       el campo: eso segundo es un cambio de contrato, y leerlo como cero hace que el guion acuse al
       SEMBRADO de algo que no ha medido. En `flacos` ademas se convierte en un rc 1.
    """
    tot = t.get("totals")
    if not isinstance(tot, dict):
        raise SystemExit(salir(RC_UNAVAILABLE,
                               f"{ruta}: team row {t.get('team')!r} contains no `totals`: "
                               "the response format changed, so its session count cannot be determined"))
    if "sessions" not in tot:
        raise SystemExit(salir(RC_UNAVAILABLE,
                               f"{ruta}: team row {t.get('team')!r} contains `totals` WITHOUT "
                               "`sessions`: missing does not mean zero"))
    return tot["sessions"]


def equipos_de(d, ruta="/v1/m/adoption/teams"):
    """Igual para la lista de equipos: `or []` convertiria una clave ausente en «no hay equipos»,
    y el guion culparia a `resource_labels` de un cambio de contrato."""
    if "teams" not in d:
        raise SystemExit(salir(RC_UNAVAILABLE,
                               f"{ruta} contains no `teams` key: missing does not mean an empty list"))
    if not isinstance(d["teams"], list):
        raise SystemExit(salir(RC_UNAVAILABLE, f"{ruta}: `teams` is not a list"))
    return d["teams"]


def salir(rc, msg):
    """La UNICA salida por stderr. Redacta SIEMPRE — quien llame no tiene que acordarse."""
    etiqueta = "⛔ COULD NOT LOOK" if rc == RC_UNAVAILABLE else "FAIL"
    print(redacta(f"seed-adoption-otlp: {etiqueta}: {msg}"), file=sys.stderr)
    return rc


def control_dedup(a):
    """Prueba la idempotencia REPRODUCIENDO DOS EJECUCIONES, no una reentrega.

    ⛔ LA VERSION ANTERIOR MEDIA OTRA COSA Y POR ESO BLINDO UNA AFIRMACION FALSA. Mandaba **el mismo
       sobre en memoria dos veces**: eso es una REENTREGA, y una reentrega identica efectivamente no
       dobla. Pero dos EJECUCIONES no producian sobres identicos —el generador llamaba a
       `time.time_ns()`— asi que en la vida real el re-pase DUPLICABA, y yo habia publicado lo
       contrario apoyandome en este control. Medido despues: 356 -> 366 -> 376.

    Ahora el control construye el sobre **dos veces, por separado**, y lo PRIMERO que exige es que
    sean identicos: si el generador vuelve a depender del reloj, esta mitad se pone roja sola. Luego
    manda, mide, vuelve a construir y a mandar, y exige que la cifra no se mueva.

    Las tres aserciones, y cada una tapa un agujero distinto:
      1 · dos construcciones separadas -> sobres IDENTICOS  (si no, no hay idempotencia posible)
      2 · la primera entrega SUBE la cifra en su tamaño     (si no, el receptor no esta ingiriendo
                                                             y the remaining results would be meaningless)
      3 · la segunda EJECUCION no la mueve                  (la idempotencia propiamente dicha)
    """
    ancla = ancla_de(a.ancla)
    # A unique team and session prefix isolate this probe's counts from concurrent
    # producers.
    import uuid as _uuid
    marca = f"{a.prefijo}-ctl-{_uuid.uuid4().hex[:8]}"
    equipos, por_equipo = [marca], 10
    primero_sobre = sobre_otlp(equipos, por_equipo, a.dias, marca, ancla)
    segundo_sobre = sobre_otlp(equipos, por_equipo, a.dias, marca, ancla)
    if json.dumps(primero_sobre, sort_keys=True) != json.dumps(segundo_sobre, sort_keys=True):
        return salir(RC_RECHAZADO,
                     "two builds of the SAME envelope differ: the generator once again "
                     "depends on the clock, making idempotency impossible")

    def mias():
        """Sesiones de MI equipo. Cero si aun no existe: ausente aqui SI es cero, porque la fila
        la crea este control y antes de crearla no tiene por que estar."""
        eq = equipos_de(leer(a.base_url, "/v1/m/adoption/teams", a.token, a.tenant))
        for t in eq:
            if (t.get("team") or "") == marca:
                # An existing row with a missing field is a response-contract error.
                return sesiones_de_fila(t)
        return 0

    # Compare consecutive post-wait readings; a pre-delivery reading cannot establish
    # stability.
    def quieta(presupuesto=30, minimo=0):
        """Devuelve (valor, segundos) cuando DOS lecturas post-espera coinciden; (None, s) si no.

        Se siembra con `None`, no con una lectura previa: asi la primera lectura de despues nunca
        puede satisfacer la condicion por si sola.

        ⛔ `minimo` ES UN SUELO Y `presupuesto` UN TECHO, y confundirlos me costo un falso verde
           MEDIDO: mi primera cura pasaba el suelo como `presupuesto`, que es el MAXIMO de vueltas,
           asi que la funcion seguia devolviendo en cuanto dos lecturas coincidian —a los 2 s—
           antes de que el duplicado aterrizase a los 3. Contra un receptor que NO deduplica, el
           control decia «0 -> 10 -> 10, idempotente» mientras el store acababa con 20 filas. Un
           techo no obliga a esperar; solo un suelo lo hace.
        """
        v, t = None, 0
        for _ in range(presupuesto):
            time.sleep(1)
            t += 1
            n = mias()
            if v is not None and n == v and t >= minimo:
                return n, t
            v = n
        return None, t

    def espera_movimiento(desde, presupuesto=30):
        """Espera a VER moverse la cifra desde `desde`. Devuelve (valor, segundos) o (None, s).

        ⛔ Es la mitad que faltaba. Sin ella, «el receptor no ha ingerido nada todavia» y «el
           receptor no ingiere» son indistinguibles, y el guion elegia el veredicto mas duro (rc 1,
           RECHAZADO) contra un motor sano. Lo que no se puede distinguir se declara rc 2.
        """
        t = 0
        for _ in range(presupuesto):
            time.sleep(1)
            t += 1
            n = mias()
            if n != desde:
                return n, t
        return None, t

    antes, _ = quieta()
    if antes is None:
        return salir(RC_UNAVAILABLE, "the rows from this run did not stabilize before the control")
    if antes != 0:
        return salir(RC_UNAVAILABLE, f"team {marca} already had {antes} sessions before "
                                       "starting: the nonce collided, so results cannot be attributed")
    # Both deliveries must be accepted before their counts can support a deduplication
    # verdict.
    cod1, cue1 = postear(a.otlp, primero_sobre)
    if not (200 <= cod1 < 300):
        return salir(RC_RECHAZADO,
                     f"the receiver rejected the first control delivery: HTTP {cod1} {cue1}")
    # Observe the first delivery before testing the second; a timeout does not
    # distinguish delay from loss.
    visto, t_uno = espera_movimiento(antes)
    if visto is None:
        return salir(RC_UNAVAILABLE,
                     f"the receiver acknowledged the first envelope, but no rows appeared in {t_uno}s "
                     f"for team {marca}: lack of ingestion cannot be distinguished from delay; "
                     "implicating the engine would invent a cause")
    uno, t_quieta = quieta()
    cod2, cue2 = postear(a.otlp, segundo_sobre)
    if not (200 <= cod2 < 300):
        return salir(RC_RECHAZADO,
                     f"the receiver rejected the second control delivery: HTTP {cod2} {cue2}. Cannot "
                     "declare idempotency for a duplicate that never arrived: no change "
                     "and rejection produce the same symptom")
    # The duplicate wait covers the observed first-delivery delay, a five-second minimum
    # and the declared persistence bound. Without that bound, unchanged counts cannot
    # prove deduplication.
    suelo = max(5, (t_uno + t_quieta) * 2, int(getattr(a, "sla_persistencia", 0) or 0))
    dos, _ = quieta(presupuesto=suelo + 30, minimo=suelo)
    if uno is None or dos is None:
        return salir(RC_UNAVAILABLE, "the rows from this run did not stabilize during the control")
    di(f"control-dedup: rows from THIS RUN for team {marca}: {antes} -> {uno} (first run of "
          f"{por_equipo}) -> {dos} (second run)")
    if uno != por_equipo:
        return salir(RC_RECHAZADO, f"the first run left {uno} rows from this run; expected "
                                   f"{por_equipo}: the receiver is not ingesting this run, so "
                                   "the remaining results would be meaningless")
    if dos != uno:
        return salir(RC_RECHAZADO, f"the second RUN changed this run's rows from {uno} to {dos}: seeding "
                                   "is NOT idempotent, and a harness run for every capture would accumulate rows")
    sla = int(getattr(a, "sla_persistencia", 0) or 0)
    if sla <= 0:
        return salir(RC_UNAVAILABLE,
                     f"the second run did not change this run's rows in {suelo}s, which does NOT prove "
                     "idempotency: waiting longer does not prove nothing will arrive. With "
                     "`--sla-persistencia N` — the receiver GUARANTEED persistence bound — "
                     "this observation establishes a verdict. Without it, no verdict is given.")
    di(f"control-dedup: ok — two identical builds; the count increases once, and the second run does not "
       f"change it in {suelo}s, covering the declared persistence SLA ({sla}s)")
    return RC_LIMPIO


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("base_url", help="the console/engine, e.g. http://127.0.0.1:18789")
    ap.add_argument("token")
    ap.add_argument("tenant")
    ap.add_argument("--otlp", default="http://127.0.0.1:14318/v1/metrics",
                    help="the connector receiver (precondition 1)")
    ap.add_argument("--equipos", type=int, default=6)
    ap.add_argument("--por-equipo", type=int, default=8)
    ap.add_argument("--dias", type=int, default=30)
    ap.add_argument("--sla-persistencia", type=int, default=0,
                    help="seconds the receiver GUARANTEES for persisting acknowledged data. Without this "
                         "bound, `--control-dedup` CANNOT declare idempotency: no observed change "
                         "does not mean no future change, and waiting longer does not prove absence.")
    ap.add_argument("--control-dedup", action="store_true",
                    help="test idempotency with TWO RUNS and exit: two identical "
                         "builds, an increase in the first run, and no change in the second")
    ap.add_argument("--ancla", default="",
                    help="UTC date (YYYY-MM-DD) anchoring timestamps; empty = today. The envelope "
                         "is identical for the same (prefix, anchor), so rerunning adds nothing")
    ap.add_argument("--prefijo", default="demo",
                    help="session.id prefix — STABLE between runs, which makes "
                         "the envelope identical between runs — idempotency depends on THIS, "
                         "rather than receiver deduplication, which is unavailable)")
    a = ap.parse_args()

    # Register the token before requests so unstructured echoes are redacted.
    _RED.recuerda(a.token)

    if a.control_dedup:
        return control_dedup(a)

    equipos = EQUIPOS[:max(1, min(a.equipos, len(EQUIPOS)))]
    esperadas = len(equipos) * a.por_equipo

    antes = leer(a.base_url, "/v1/m/adoption/summary", a.token, a.tenant)
    tel_antes = sesiones_de(antes)

    sobre = sobre_otlp(equipos, a.por_equipo, a.dias, a.prefijo, ancla_de(a.ancla))
    codigo, cuerpo = postear(a.otlp, sobre)
    if not (200 <= codigo < 300):
        return salir(RC_RECHAZADO, f"the receiver rejected the envelope: HTTP {codigo} {cuerpo}")

    # Acknowledgement can precede persistence. Require two matching post-delivery
    # readings and record whether the count moved.
    tel, estable, se_movio = None, False, False
    for _ in range(30):
        time.sleep(1)
        d = leer(a.base_url, "/v1/m/adoption/summary", a.token, a.tenant)
        nuevo = sesiones_de(d)
        if nuevo != tel_antes:
            se_movio = True
        if tel is not None and nuevo == tel:
            estable = True
            break
        tel = nuevo
    if not estable:
        return salir(RC_UNAVAILABLE,
                     f"the session count kept changing after 30 s (latest {tel}): "
                     "ingestion did not stabilize, so no verdict can be given")

    equipos_vistos = equipos_de(leer(a.base_url, "/v1/m/adoption/teams", a.token, a.tenant))
    con_nombre = [t for t in equipos_vistos if (t.get("team") or "").strip()]
    # Judge only the teams generated here. Report unrelated small teams without
    # attributing them to this envelope.
    mios = {e for e in equipos}
    flacos = [t.get("team") for t in con_nombre
              if t.get("team") in mios and sesiones_de_fila(t) < a.por_equipo]
    ajenos_flacos = [t.get("team") for t in con_nombre
                     if t.get("team") not in mios and sesiones_de_fila(t) < a.por_equipo]
    # Request the telemetry lens explicitly. A missing days field is a response-contract
    # error.
    _trend = leer(a.base_url, "/v1/m/adoption/trend?lens=telemetry", a.token, a.tenant)
    if "days" not in _trend:
        return salir(RC_UNAVAILABLE,
                     "/v1/m/adoption/trend?lens=telemetry contains no `days` key: missing does not mean "
                     "zero days; the response format changed")
    dias_tel = len(_trend["days"] or [])
    delta = tel - tel_antes

    di(f"seed-adoption-otlp: receiver {sanea(a.otlp)} -> HTTP {codigo}")
    di(f"  sessions (telemetry lens) {tel_antes} -> {tel}   (delta {delta:+d}; this envelope contains "
          f"{esperadas} with prefix {a.prefijo!r})")
    di(f"  named teams               {len(con_nombre)} of {len(equipos_vistos)} rows; "
          f"{len(flacos)} from THIS RUN below {a.por_equipo} sessions")
    if ajenos_flacos:
        di(f"  ⓘ and {len(ajenos_flacos)} OTHER team(s) below that threshold "
              f"({', '.join(sorted(x for x in ajenos_flacos if x)[:4])}): appear in the capture but are NOT "
              "from this seeding run, so they do not determine its verdict.")
    di(f"  trend days                 {dias_tel}  (with ?lens=telemetry; without it, the default "
          "lens is `analytics`, which shows 0)")
    di("  ⛔ FOR CAPTURES: the console opens with the `analytics` lens, which remains zero by "
          "design (Admin Analytics API). Select `telemetry` to avoid an empty capture.")
    if delta == 0:
        # A zero delta can mean a repeated envelope or absent persistence; report the
        # observation without choosing a cause.
        if not se_movio:
            di(f"  ⓘ ZERO delta and the count NEVER changed during the window: two causes produce "
                  f"this symptom and CANNOT be distinguished here — (a) rerunning an envelope with prefix "
                  f"{a.prefijo!r}, byte-for-byte identical to an earlier delivery, as expected; or (b) the "
                  "receiver acknowledged 200 without persisting anything. The check below answers the "
                  "relevant capture question: whether the SCREEN contains data.")
        else:
            di(f"  ⓘ ZERO delta, but the count DID change during the window and returned to "
                  f"{tel}: ingestion occurred and something offset it. No cause is attributed here.")
    elif delta > esperadas:
        # An excess delta cannot be attributed to this envelope without the isolated
        # deduplication probe.
        di(f"  ⚠ delta {delta:+d} for an envelope of {esperadas}: attribution is UNVERIFIED. It could be "
              "older ingestion completing or duplication; they cannot be distinguished here. "
              "For an idempotency verdict, use `--control-dedup`, which reproduces TWO RUNS.")

    # These aggregate checks establish visible data, not attribution of existing rows to
    # this envelope.
    if tel < esperadas:
        return salir(RC_RECHAZADO,
                     f"the engine acknowledged the envelope, but only {tel} of {esperadas} sessions are visible")
    if not con_nombre:
        return salir(RC_RECHAZADO,
                     "sessions exist but there are NO named teams: `resource_labels` is missing from the "
                     "source, or reload returned `rejected=1` because of a port collision (see the header)")
    if len(con_nombre) < len(equipos):
        return salir(RC_RECHAZADO,
                     f"only {len(con_nombre)} named teams out of {len(equipos)} in the envelope")
    if flacos:
        return salir(RC_RECHAZADO,
                     f"teams below {a.por_equipo} sessions: {flacos}")
    if dias_tel == 0:
        return salir(RC_RECHAZADO, "sessions and teams exist, but the trend shows 0 days")
    return RC_LIMPIO


if __name__ == "__main__":
    sys.exit(main())
