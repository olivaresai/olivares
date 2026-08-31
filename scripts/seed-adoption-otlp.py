#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Siembra la superficie `/adoption` del estate de DEMO por el receptor OTLP del conector claude, y
# VERIFICA el resultado leyendo la propia consola. rc: 0 limpio · 1 rechazado · 2 no he podido mirar.
#
# ⛔ POR QUE EXISTE, Y POR QUE CONTRADICE AL PLAN. `docs/launch/PLAN-SEMBRADO-ESTATE-2026-08-30.md`
#    §4 dice que para `adoption` *«la unica inversion con retorno es extender
#    cmd/olivares/seed/seed.go»*. Medido el 2026-08-30 sobre `main 934c7bb88` con el binario de
#    `08111ace1`: **no hace falta tocar el motor**. La premisa del plan SI es correcta —en el
#    sembrador `MetricSample` aparece 0 veces frente a `CostSample` 3, `Edge` 3 y `Finding` 3, y
#    `/v1/m/adoption/summary` sale a ceros con el estate sembrado entero—, pero la conclusion no:
#    el conector claude trae un receptor OTLP y el arnes de capturas es el OPERADOR de su propio
#    motor, asi que puede encenderlo.
#
# ⛔⛔ Y ANTES DE SEMBRAR NADA, EL HALLAZGO QUE DECIDE SI ESTO SIRVE PARA UNA CAPTURA:
#    `web/src/features/adoption/adoption-view.tsx:232` hace `useState<LensId>('analytics')`. La
#    lente `analytics` es la Admin Analytics API de Anthropic y **sin credencial real se queda a
#    ceros por diseño**. ⇒ se puede sembrar la telemetria entera y perfecta, y la foto de
#    `/adoption` SEGUIRA saliendo vacia, porque el arnes fotografia la lente por defecto. Es la
#    misma clase que la nota del plan sobre `capabilities` («la portada abre en `servers`, que sale
#    vacia»). **Quien capture tiene que seleccionar la lente `telemetry`**; sembrar sin eso es pagar
#    el trabajo y fotografiar el vacio igual.
#
# ⚠ LIMITE DEL MOTOR, DECLARADO PORQUE NO ES MIO DE CURAR (the reviewer, A-02). El control cuenta FILAS
#    PROPIAS filtrando por un `team` con nonce, y eso lo hace atribuible frente a otros sembrados
#    concurrentes… **hasta aqui**: `team` NO entra en la clave natural del ingest
#    (`modules/claudeadoption/ingest.go:138-146`, comentario y `naturalKey`). ⛔ AQUI DECIA «(tenant, dia,
#    session.id)» Y ERA FALSO POR DOS LADOS, corregido con la fuente delante: la clave es
#    `(subjectKind, subjectRef, name, day, canonicalDims(dims))` — es decir, incluye ademas EL
#    NOMBRE DE LA METRICA y LAS DIMENSIONES, y el tenant no aparece en ella (lo aporta el repo).
#    La parte que si sostiene lo que este guion afirma la dice el propio comentario del motor:
#    el VALOR y la etiqueta de EQUIPO se excluyen a proposito — el valor, para que un dia
#    re-tirado haga upsert en vez de una fila nueva; el equipo, por la regla de que una etiqueta
#    no entra en una clave natural. ⇒ otro productor que use MI MISMO `session.id` con la misma
#    metrica y dimensiones puede aliasar mi fila y mi cuenta se lo tragaria. Con el nonce en el
#    prefijo de los ids la colision es remota, pero **remota no es imposible** y el guion no lo
#    puede impedir desde fuera del motor. Se dice aqui en vez de curarse mal.
#
# ── LAS DOS PRECONDICIONES, que este guion NO puede hacer por ti ──────────────────────────────
#
# Son del ARRANQUE del motor, no de la API, y por eso van aqui escritas con su medida en vez de
# intentadas y falladas:
#
#   1 · registrar el conector como fuente durable
#       olivares sources set --name claude-demo --kind claude --tenant <TENANT> \
#         --actor <quien> --reason <por que> \
#         --config http_addr=127.0.0.1:14318 \
#         --config resource_labels=team,project,cost_center
#       ⚠ `--reason` es OBLIGATORIO ademas de `--actor`; sin el sale
#         «reason is required for a privileged local operation» y no escribe nada.
#       ⚠ `resource_labels` viene APAGADO por defecto («Empty = off, minimal-data default»). Sin
#         el, la pestaña de equipos sale con UNA fila de nombre vacio, que la consola pinta como
#         «(unassigned)» — una pantalla que parece rota y no lo esta.
#
#   2 · recargar el motor.  ⛔ POR API NO: `POST /v1/console/runtime/reload` responde
#       `step_up_required` — exige AAL3, un autenticador hardware. Es la misma clase que las
#       invitaciones que `scripts/seed-demo-work.py` ya declara no sembrables. Con `kill -HUP <pid>`
#       si: el log dice «SIGHUP source reload applied added=1» y el puerto pasa a escuchar.
#
# ⛔ TRAMPA DE OPERACION MEDIDA, y cuesta dos recargas en falso: **un conector que LIGA UN PUERTO no
#    se re-configura en sitio**. Al cambiar `resource_labels` conservando `http_addr`, la recarga
#    sale `rejected=1` con *«rotate failed: the connector could not be opened with the supplied
#    configuration»* — deny-closed, correcto, y desde fuera se lee como «la etiqueta no funciona».
#    Comprobado cambiando UNA sola cosa: con otro `http_addr` la misma recarga sale `rotated=1,
#    rejected=0`, el puerto viejo se libera y el nuevo queda escuchando. Es una COLISION DE PUERTO:
#    la rotacion abre el nuevo mientras el viejo aun tiene el socket. ⇒ para re-configurar, cambia
#    el puerto o reinicia el motor.
#
# ⛔⛔ LO QUE ESTE FICHERO AFIRMO Y ERA FALSO, dicho aqui porque lo publique dos veces en el bus:
#    **el receptor NO deduplica por `session.id`.** Contesta 200 y el store SUMA el delta
#    (`modules/claudeadoption/ingest.go:24-31,87-94`), asi que un re-pase con los mismos ids
#    ANADE. Medido: 356 -> 366 -> 376. Lo destapo un contraste, no yo, y mi propio «control» no lo
#    desmentia porque mandaba el mismo sobre dos veces — una REENTREGA, no dos ejecuciones.
#
# ⇒ La idempotencia de hoy es la que el generador puede sostener, y se dice sin adornos: **para el
#   mismo (prefijo, ancla) el sobre es IDENTICO byte a byte y el re-pase no anade nada; con otra
#   ancla es otro sobre y SI anade.** El ancla por defecto es la medianoche UTC de hoy ⇒ idempotente
#   dentro del dia, distinto de un dia a otro. Un arnes que capture a diario es exactamente eso.
#
import argparse
import json
import os
import re
import random
import sys
import time
import urllib.error
import urllib.request

RC_LIMPIO, RC_RECHAZADO, RC_NO_PUDE_MIRAR = 0, 1, 2

# Los nombres son los que el motor declara en los umbrales de `/v1/m/adoption/discrepancy`, no
# invenciones: claude_code.{session,commit,pull_request}.count, .lines_of_code.count, .token.usage.
# ⛔⛔ ESTA LISTA TENIA CINCO DE LAS SIETE, Y NINGUNA LLEVABA DIMENSIONES. El plano OTLP del
#    modulo de adopcion reconoce SIETE metricas de sesion —`modules/claudeadoption/contract.go:12-19`—
#    y el sembrado emitia cinco, sin un solo atributo de datapoint. El dano NO es «faltan dos
#    numeros»: es que el agregador (`modules/claudeadoption/aggregate.go:41-107`) SE DESGLOSA POR
#    ESAS DIMENSIONES, asi que la evidencia sembrada salia con
#      · `accepted` / `rejected` / desglose por herramienta ....... VACIOS (metrica ausente)
#      · tiempo activo ............................................ VACIO  (metrica ausente)
#      · lineas: TODO del lado «anadidas», nunca «borradas» ....... (sin dim `type`)
#      · mezcla de modelos ........................................ VACIA  (sin dim `model`)
#    …y una pagina con cuatro paneles a cero se lee como «el producto no lo trae», no como «el
#    sembrado no lo siembra». Es la peor clase de hueco en una captura que se publica como prueba.
#
#    `claude_code.active_users` NO entra aqui a proposito, y la razon esta en el propio contrato
#    (`contract.go:20-26`): es org-level, viene del ingest de Enterprise Analytics del conector
#    `claude-api` —otro plano—, su unidad son usuarios y NUNCA participa de las lentes de sesion.
#    Sembrarla por OTLP seria inventar una entrega que en produccion no ocurre.
#
#    Los nombres de atributo son los que el receptor LEE, no los que el modulo guarda:
#    `tool_name` -> dim `tool` (`connectors/claude/metrics.go:97-108`, `events.go:114,123,174`).
MODELOS = ("claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5")
HERRAMIENTAS = ("Edit", "Write", "NotebookEdit")

METRICAS = [
    # (nombre, generador) — el generador devuelve UNA LISTA de (dims, valor): una metrica con
    # desglose produce un datapoint por combinacion, que es como llega de un cliente real.
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
    # ⛔ EN SEGUNDOS, NO EN MILISEGUNDOS, y lo destapo una medida contra el motor real, no una
    #    lectura. `adoptionMetricUnit` declara la unidad ALMACENADA («ms»), pero el receptor
    #    CONVIERTE: `connectors/claude/metrics.go:151` hace `dp.GetAsInt() * 1000  // seconds ->
    #    ms`. Sembrando ms, el agregado salia MIL VECES de mas: 62.481.616.000 ms = 723 dias de
    #    actividad para 12 sesiones. Una captura con ese numero es PEOR que una a cero — la de
    #    cero se lee como «falta dato», y esta se lee como dato.
    ("claude_code.active_time.total", lambda r: [
        ({"type": "user"}, r.randint(600, 9_000)),      # 10 min - 2,5 h por sesion
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
        return set(), f"no he podido leer {ruta} ({type(e).__name__})"
    bloque = re.search(r"^const \($(.*?)^\)$", src, re.S | re.M)
    if not bloque:
        return set(), f"no encuentro el primer bloque `const (` de {ruta}"
    nombres = set(re.findall(r'"(claude_code\.[a-z_.]+)"', bloque.group(1)))
    if not nombres:
        return set(), f"el bloque `const (` de {ruta} no trae ni un nombre `claude_code.*`"
    # Fuera del plano OTLP por decision del propio contrato, no por conveniencia mia.
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
            # Semilla ESTABLE: del propio id, no de un contador compartido. Asi el valor de cada
            # metrica y el desfase de dias no dependen del ORDEN en que se generen.
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
                        # UN DATAPOINT POR COMBINACION DE DIMENSIONES, que es como llega de un
                        # cliente real y lo que el agregador necesita para desglosar.
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
            raise SystemExit(salir(RC_NO_PUDE_MIRAR, f"--ancla {texto!r} no es YYYY-MM-DD"))
    else:
        hoy = _dt.datetime.now(_dt.timezone.utc)
        d = hoy.replace(hour=0, minute=0, second=0, microsecond=0)
    return int(d.timestamp()) * 1_000_000_000


# Piezas sensibles vistas en esta corrida. Se llenan al conocer una URL y se tapan en TODA salida,
# venga por donde venga — incluida una excepcion que nadie capturo.
# ⛔ LA REDACCION VIVE EN `scripts/lib/redaccion.py`, COMPARTIDA, Y AQUI SOLO SE ENVUELVE. Esta
#    funcion nacio dos veces en esta casa —aqui y en `seed-estate-volume.py`— y a cada copia se le
#    escapo algo distinto: a esta, las credenciales sin forma conocida y las cabeceras reflejadas;
#    a aquella, el cuerpo HTTP crudo. Dos implementaciones del mismo control es como se pierde la
#    segunda cura. the reviewer pidio UNA frontera y esto es UNA.
#
#    La libreria se BUSCA, no se asume al lado: este guion se copia (los mutantes del banco viven
#    en un temporal). Si no aparece, el guion NO ARRANCA: sin redaccion no hay forma de prometer
#    que un secreto no salga, y arrancar igual seria el fallo que la libreria existe para cerrar.
def _busca_lib():
    # ⛔ EL OVERRIDE EXPLICITO VA PRIMERO. Lo tenia detras del directorio del guion, y eso hacia
    #    INERTE a `OLIVARES_LIB_DIR`: el banco preparaba una libreria mutada, la exportaba, y el
    #    guion seguia cargando la real — el mutante «sobrevivia» sin haberse aplicado nunca. Un
    #    override que no gana no es un override.
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
    print("seed-adoption-otlp: ⛔ NO HE PODIDO MIRAR: no encuentro `scripts/lib/redaccion.py`. "
          "Sin redaccion de salidas no arranco.", file=sys.stderr)
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
        cred = "<credencial-oculta>@" if u.username or u.password else ""
        cola = " (+ruta/consulta ocultas)" if (u.query or (u.path or "/") != "/") else ""
        return f"{u.scheme}://{cred}{host}{puerto}{cola}"
    except Exception:
        return "<url-ilegible>"


def postear(url, cuerpo):
    # ⛔ EL `Request` SE CONSTRUYE DENTRO DEL `try`, y no es estilo: fuera, un
    #    `Request('://usuario:sk-…@host/x')` lanza `ValueError: unknown url type: <la URL entera>`
    #    que NADIE capturaba, asi que salia con la credencial literal. Reproducido.
    recuerda_sensibles(url)
    try:
        req = urllib.request.Request(url, data=json.dumps(cuerpo).encode(), method="POST")
        req.add_header("Content-Type", "application/json")
        # `abre` en vez de `urlopen`: no sigue 30x. Ver la razon medida en la libreria.
        with abre(req, timeout=60) as r:
            return r.status, redacta(r.read().decode()[:200])
    except urllib.error.HTTPError as e:
        # ⛔ EL CUERPO SE REDACTA EN EL ORIGEN, no sólo al imprimirlo: un receptor que conteste 400
        #    repitiendo la URL mete la credencial en el cuerpo, y `main` lo imprime. Comprobado con
        #    un servidor local que devuelve `rejected endpoint https://usuario:sk-…@…`.
        return e.code, redacta(e.read().decode()[:200])
    except Exception as e:
        # ⛔ EL MENSAJE SE COMPONE AQUI Y SE LANZA FUERA, y no es estilo: es el A-01 de the reviewer sobre
        #    `817bc4a4d`. Un `raise SystemExit(...)` DENTRO del manejador **encadena** la excepcion
        #    original en `__context__`, y esa lleva el secreto aunque el mensaje ya no lo lleve:
        #    `ValueError: unknown url type: '://usuario:<secreto>@host/x'` sigue colgando del
        #    `SystemExit`. Quien lo capture y mire `e.__context__` —o cualquier volcado que siga la
        #    cadena— ve la credencial que tanto trabajo costo tapar en el texto.
        #
        #    Y mi banco no lo veia porque hacia `except SystemExit: pass`: **descartaba justo el
        #    objeto que la llevaba**. Redactar el mensaje y dejar el contexto colgando es tapar la
        #    puerta y dejar la ventana.
        motivo = f"{type(e).__name__}: {e}"
    # Fuera del manejador: aqui no hay excepcion activa, asi que el `SystemExit` nace SIN contexto.
    raise SystemExit(salir(RC_NO_PUDE_MIRAR, redacta(
        f"no alcanzo el receptor OTLP en {sanea(url)}: {motivo}\n"
        "  ¿registraste la fuente y mandaste SIGHUP al motor? (precondiciones 1 y 2)", url)))


def leer(base, ruta, token, tenant, intentos=3):
    """Lee una ruta de la consola. UN atasco transitorio no es ceguera; tres seguidos, si.

    ⛔ MEDIDO, Y CASI LO LEO AL REVES: el mismo `summary` que un `curl` devolvia en **10 ms** hizo
       expirar este lector a los 30 s dos veces seguidas — con nueve carriles en la caja y load1 en
       10,6. Subir el plazo a ciegas habria tapado un atasco real; tratarlo como «no puedo mirar» a
       la primera convierte carga ajena en un veredicto mio. Se reintenta, y si los tres fallan
       ENTONCES es rc 2, que es lo que significa de verdad: no he podido mirar.
    """
    # El guion SI conoce su token: declararlo es lo unico que tapa un cuerpo ajeno que lo repita
    # sin cabecera delante, sin depender de que tenga forma de nada conocido.
    recuerda_secreto(token, tenant)

    recuerda_sensibles(base)
    ultimo = None
    for intento in range(intentos):
        try:
            # Dentro del `try` por la misma razon que en `postear`: construirlo fuera deja un
            # `ValueError` con la URL entera fuera de todo manejo.
            req = urllib.request.Request(base.rstrip("/") + ruta)
            req.add_header("Authorization", "Bearer " + token)
            req.add_header("X-Olivares-Tenant", tenant)
            with abre(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except Exception as e:
            ultimo = e
            if intento + 1 < intentos:
                time.sleep(2 * (intento + 1))
    raise SystemExit(salir(RC_NO_PUDE_MIRAR, redacta(
        f"no puedo leer {ruta} tras {intentos} intentos: "
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
        raise SystemExit(salir(RC_NO_PUDE_MIRAR,
                               f"{ruta} no trae `telemetry.totals`: la respuesta ha cambiado de "
                               "forma y NO puedo decir cuantas sesiones hay"))
    if "sessions" not in tot:
        raise SystemExit(salir(RC_NO_PUDE_MIRAR,
                               f"{ruta} trae `telemetry.totals` SIN `sessions`: ausente no es cero"))
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
        raise SystemExit(salir(RC_NO_PUDE_MIRAR,
                               f"{ruta}: la fila del equipo {t.get('team')!r} no trae `totals`: "
                               "la respuesta cambio de forma y NO puedo decir cuantas sesiones tiene"))
    if "sessions" not in tot:
        raise SystemExit(salir(RC_NO_PUDE_MIRAR,
                               f"{ruta}: la fila del equipo {t.get('team')!r} trae `totals` SIN "
                               "`sessions`: ausente no es cero"))
    return tot["sessions"]


def equipos_de(d, ruta="/v1/m/adoption/teams"):
    """Igual para la lista de equipos: `or []` convertiria una clave ausente en «no hay equipos»,
    y el guion culparia a `resource_labels` de un cambio de contrato."""
    if "teams" not in d:
        raise SystemExit(salir(RC_NO_PUDE_MIRAR,
                               f"{ruta} no trae la clave `teams`: ausente no es lista vacia"))
    if not isinstance(d["teams"], list):
        raise SystemExit(salir(RC_NO_PUDE_MIRAR, f"{ruta}: `teams` no es una lista"))
    return d["teams"]


def salir(rc, msg):
    """La UNICA salida por stderr. Redacta SIEMPRE — quien llame no tiene que acordarse."""
    etiqueta = "⛔ NO HE PODIDO MIRAR" if rc == RC_NO_PUDE_MIRAR else "FALLO"
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
                                                             y el resto no significaria nada)
      3 · la segunda EJECUCION no la mueve                  (la idempotencia propiamente dicha)
    """
    ancla = ancla_de(a.ancla)
    # ⛔ EL EQUIPO ES EL MARCADOR, Y ESO CONVIERTE EL CONTROL EN «FILAS PROPIAS». La version
    #    anterior medía `summary.telemetry.totals.sessions` — un AGREGADO del tenant entero, que
    #    otros sembrados mueven a la vez: no contaba ni identificaba lo mio, y por eso un delta
    #    ajeno se me colaba como si fuera mi resultado (asi explique un +90 que no era mio). Aqui
    #    el nombre del equipo lleva el nonce de la invocacion, asi que `/adoption/teams` devuelve
    #    UNA fila que es exclusivamente de este control y se cuenta sola.
    import uuid as _uuid
    marca = f"{a.prefijo}-ctl-{_uuid.uuid4().hex[:8]}"
    equipos, por_equipo = [marca], 10
    primero_sobre = sobre_otlp(equipos, por_equipo, a.dias, marca, ancla)
    segundo_sobre = sobre_otlp(equipos, por_equipo, a.dias, marca, ancla)
    if json.dumps(primero_sobre, sort_keys=True) != json.dumps(segundo_sobre, sort_keys=True):
        return salir(RC_RECHAZADO,
                     "dos construcciones del MISMO sobre salen distintas: el generador vuelve a "
                     "depender del reloj y no hay idempotencia posible")

    def mias():
        """Sesiones de MI equipo. Cero si aun no existe: ausente aqui SI es cero, porque la fila
        la crea este control y antes de crearla no tiene por que estar."""
        eq = equipos_de(leer(a.base_url, "/v1/m/adoption/teams", a.token, a.tenant))
        for t in eq:
            if (t.get("team") or "") == marca:
                # La fila EXISTE: si le falta el campo es un cambio de contrato, no un cero.
                return sesiones_de_fila(t)
        return 0

    # ⛔ «AUN NO HA CAMBIADO» NO ES «YA NO CAMBIA», y esta funcion las confundia. Tomaba `v =
    #    mias()` ANTES de dormir y devolvia en cuanto dos lecturas coincidian; si la persistencia
    #    tardaba mas de un segundo, las dos valian lo de antes del POST, «coincidian», y declaraba
    #    quietud sin haber visto moverse nada. El sesgo es lo peor: si el retraso afecta a la
    #    PRIMERA entrega el control sale ROJO acusando a un motor sano, y si afecta a la SEGUNDA
    #    sale VERDE dando la idempotencia por buena mientras el store acaba con el doble de filas.
    #    El fallo se inclina siempre hacia «idempotente», que es justo lo que este control existe
    #    para no creerse.
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
        return salir(RC_NO_PUDE_MIRAR, "mis propias filas no se estabilizaron antes del control")
    if antes != 0:
        return salir(RC_NO_PUDE_MIRAR, f"el equipo {marca} ya tenia {antes} sesiones antes de "
                                       "empezar: el nonce ha colisionado y no puedo atribuir nada")
    # ⛔ SE MIRA EL CODIGO DE CADA ENTREGA, y no es celo: `main` ya lo hacia y este control no.
    #    Certificar idempotencia sobre una entrega que el receptor RECHAZO es certificar sobre algo
    #    que no paso. Y con la segunda es peor que con la primera: un 400 en el duplicado da
    #    exactamente el mismo sintoma que la idempotencia —la cifra no se mueve— y con un SLA
    #    declarado se leeria como veredicto bueno. Un rechazo no es una prueba de nada.
    cod1, cue1 = postear(a.otlp, primero_sobre)
    if not (200 <= cod1 < 300):
        return salir(RC_RECHAZADO,
                     f"el receptor rechazo la 1.a entrega del control: HTTP {cod1} {cue1}")
    # La PRIMERA entrega tiene que VERSE llegar. Si en el presupuesto entero no aparece ni una
    # fila, no sabemos si el receptor descarta o si solo va lento: eso es rc 2, no un rechazo.
    visto, t_uno = espera_movimiento(antes)
    if visto is None:
        return salir(RC_NO_PUDE_MIRAR,
                     f"el receptor acuso el primer sobre y en {t_uno}s no ha aparecido ni una fila "
                     f"del equipo {marca}: no puedo distinguir «no ingiere» de «va lento», y "
                     "acusar al motor sin poder distinguirlo seria inventarme la causa")
    uno, t_quieta = quieta()
    cod2, cue2 = postear(a.otlp, segundo_sobre)
    if not (200 <= cod2 < 300):
        return salir(RC_RECHAZADO,
                     f"el receptor rechazo la 2.a entrega del control: HTTP {cod2} {cue2}. No puedo "
                     "declarar idempotencia sobre un duplicado que no llego a entrar: «no se movio» "
                     "y «lo rechazaron» dan el mismo sintoma")
    # ⛔ LA ESPERA DEL DUPLICADO SE MIDE CONTRA LO QUE TARDO LA PRIMERA, no contra una constante.
    #    Concluir «no se movio» antes de que a la primera le hubiera dado tiempo a llegar es
    #    declarar idempotencia por impaciencia. Suelo de 5 s para que un receptor instantaneo no
    #    fije un listron de cero.
    # ⛔ ESPERAR MAS NO DEMUESTRA AUSENCIA, Y ESO NO SE ARREGLA SUBIENDO EL SUELO. Mi cura anterior
    #    derivaba el suelo de lo que tardo la primera entrega (6 s aqui) y el lector lo rompio con
    #    un retraso de 7 s SOLO en la segunda: el control decia «0 -> 10 -> 10, idempotente» y un
    #    segundo despues el store llegaba a 20. Cualquier suelo que yo elija se rompe con un
    #    retraso un poco mayor: **no se puede probar que algo no va a llegar esperandolo**.
    #
    #    Asi que la garantia no sale de mi paciencia: sale de un SLA DECLARADO por quien conoce el
    #    receptor. Con `--sla-persistencia N`, «no se movio en N s» SI es un veredicto, porque N lo
    #    respalda alguien. Sin el, esto es rc 2 —«no puedo dar un veredicto»— y no rc 0. Es la regla
    #    5 del canon: lo que no se puede distinguir se declara, no se elige.
    suelo = max(5, (t_uno + t_quieta) * 2, int(getattr(a, "sla_persistencia", 0) or 0))
    dos, _ = quieta(presupuesto=suelo + 30, minimo=suelo)
    if uno is None or dos is None:
        return salir(RC_NO_PUDE_MIRAR, "mis propias filas no se estabilizaron durante el control")
    di(f"control-dedup: filas PROPIAS del equipo {marca}: {antes} -> {uno} (1.a ejecucion de "
          f"{por_equipo}) -> {dos} (2.a ejecucion)")
    if uno != por_equipo:
        return salir(RC_RECHAZADO, f"la 1.a ejecucion dejo {uno} filas propias y esperaba "
                                   f"{por_equipo}: el receptor no esta ingiriendo lo mio, asi que "
                                   "el resto no significaria nada")
    if dos != uno:
        return salir(RC_RECHAZADO, f"la 2.a EJECUCION movio mis filas de {uno} a {dos}: el sembrado "
                                   "NO es idempotente y un arnes que corra en cada captura acumulara")
    sla = int(getattr(a, "sla_persistencia", 0) or 0)
    if sla <= 0:
        return salir(RC_NO_PUDE_MIRAR,
                     f"la 2.a ejecucion no movio mis filas en {suelo}s, y eso NO es prueba de "
                     "idempotencia: esperar mas no demuestra que nada vaya a llegar. Con "
                     "`--sla-persistencia N` —el plazo que el receptor GARANTIZA para persistir— "
                     "esta misma observacion si es un veredicto. Sin el, no lo doy.")
    di(f"control-dedup: ok — dos construcciones identicas, sube una vez y la 2.a ejecucion no "
       f"mueve en {suelo}s, que cubre el SLA de persistencia declarado ({sla}s)")
    return RC_LIMPIO


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("base_url", help="la consola/motor, p.ej. http://127.0.0.1:18789")
    ap.add_argument("token")
    ap.add_argument("tenant")
    ap.add_argument("--otlp", default="http://127.0.0.1:14318/v1/metrics",
                    help="el receptor del conector (precondicion 1)")
    ap.add_argument("--equipos", type=int, default=6)
    ap.add_argument("--por-equipo", type=int, default=8)
    ap.add_argument("--dias", type=int, default=30)
    ap.add_argument("--sla-persistencia", type=int, default=0,
                    help="segundos que el receptor GARANTIZA para persistir lo que acusa. Sin este "
                         "plazo, `--control-dedup` NO puede declarar idempotencia: «no se movio» "
                         "no es «no se va a mover», y esperar mas no demuestra ausencia.")
    ap.add_argument("--control-dedup", action="store_true",
                    help="prueba la idempotencia con DOS EJECUCIONES y sale: dos construcciones "
                         "identicas, sube en la 1.a y no se mueve en la 2.a")
    ap.add_argument("--ancla", default="",
                    help="dia UTC (YYYY-MM-DD) al que se anclan los sellos; vacio = hoy. El sobre "
                         "es identico para el mismo (prefijo, ancla), y por eso el re-pase no anade")
    ap.add_argument("--prefijo", default="demo",
                    help="prefijo de session.id — ESTABLE entre corridas, que es lo que hace "
                         "identico el sobre entre corridas — y de ESO depende la idempotencia, "
                         "no de ninguna deduplicacion del receptor, que no la hay)")
    a = ap.parse_args()

    # EL TOKEN SE RECUERDA ANTES DE HABLAR CON NADIE. Defensa en profundidad, y dicha por lo que
    # es: cubre un token que asome en un mensaje SIN `Bearer` ni cabecera delante, que es lo unico
    # que ni el patron de cabecera ni el de `Bearer` de `scripts/lib/redaccion.py` alcanzan.
    #
    # ⛔ AQUI SE JUSTIFICABA CON UN CASO QUE NO ES SUYO, y lo corrijo con la medida delante. Decia
    #    que sin esta llamada se fugaria el `ValueError: Invalid header value b'Bearer <el token>'`
    #    de un token con un caracter de control. **FALSO, medido ejecutando el guion contra un
    #    destino muerto: con la llamada y sin ella el token sale `<oculto>` igual**, porque a ese
    #    texto lo tapa el patron `Bearer` de la libreria. Es decir, el mutante de esta linea
    #    SOBREVIVE, y sobrevivia tambien cuando el banco decia acreditarla — su testigo recordaba
    #    el token a mano en vez de ejercer `main`, asi que medía la libreria, no esta llamada.
    #
    #    Se queda porque cuesta una linea y cierra un hueco real, pero NO se apunta un merito que
    #    no tiene: lo que corta el caso citado esta en la libreria, y el banco lo acredita ALLI.
    #
    #    Sus hermanos `seed-estate-volume` y `verify-seed-payloads` ya lo hacian; a este se le
    #    olvido, que es la forma en que una frontera compartida deja de serlo.
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
        return salir(RC_RECHAZADO, f"el receptor rechazo el sobre: HTTP {codigo} {cuerpo}")

    # ⛔ EL RECEPTOR ACUSA ANTES DE PERSISTIR, asi que leer una vez mide la carrera y no el
    #    resultado. Se espera a que la cifra se ESTABILICE —dos lecturas seguidas iguales—, no a
    #    que supere un umbral: superar el umbral lo hace solo un estate ya poblado, sin que este
    #    sobre haya llegado. Y si no se estabiliza en el presupuesto, es «no he podido mirar».
    # ⛔ SE SIEMBRA CON `None`, NO CON LA LECTURA PREVIA AL POST. Decia `tel = tel_antes`, asi que
    #    la PRIMERA comparacion enfrentaba una lectura de despues del POST contra una de ANTES: si
    #    a un segundo no se habia movido nada, declaraba «estable» sin que ninguna lectura se
    #    hubiera movido. El comentario de arriba NOMBRA ese peligro —«superar el umbral lo hace
    #    solo un estate ya poblado, sin que este sobre haya llegado»— y la cura elegida no lo
    #    cerraba: hacen falta DOS lecturas POSTERIORES que coincidan, y se anota si alguna se movio.
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
        return salir(RC_NO_PUDE_MIRAR,
                     f"la cifra de sesiones seguia moviendose tras 30 s (ultima {tel}): la "
                     "ingesta no se estabilizo, asi que no puedo dar un veredicto")

    equipos_vistos = equipos_de(leer(a.base_url, "/v1/m/adoption/teams", a.token, a.tenant))
    con_nombre = [t for t in equipos_vistos if (t.get("team") or "").strip()]
    # ⛔ SOLO SE JUZGAN LOS EQUIPOS QUE ESTE GUION SIEMBRA. `flacos` salia de TODOS los equipos con
    #    nombre del tenant, y su rc 1 dice «el sembrado salio corto»: una fila AJENA preexistente
    #    —de otro carril, de una demo anterior, de un cliente— con pocas sesiones tumbaba el
    #    veredicto de un sembrado que habia ido bien. Es la clase «el universo de la medida no es el
    #    universo de la afirmacion»: se afirma sobre lo sembrado y se medía sobre todo el tenant.
    #
    #    Las ajenas NO se ignoran: se cuentan y se dicen, porque para una CAPTURA importan —una fila
    #    flaca sale en la foto— pero no son culpa de este sembrado ni deben decidir su rc.
    mios = {e for e in equipos}
    flacos = [t.get("team") for t in con_nombre
              if t.get("team") in mios and sesiones_de_fila(t) < a.por_equipo]
    ajenos_flacos = [t.get("team") for t in con_nombre
                     if t.get("team") not in mios and sesiones_de_fila(t) < a.por_equipo]
    # ⛔ `trend` SIN `?lens=telemetry` devuelve la lente `analytics`, que esta vacia por diseño. Leer
    #    la de por defecto y concluir «no hay datos» es el mismo error que fotografiarla.
    # ⛔ Y AQUI IGUAL: `.get("days") or []` leia «la respuesta ya no trae `days`» como «cero dias de
    #    tendencia», y la comprobacion de abajo culpaba al sembrado de un cambio de contrato.
    _trend = leer(a.base_url, "/v1/m/adoption/trend?lens=telemetry", a.token, a.tenant)
    if "days" not in _trend:
        return salir(RC_NO_PUDE_MIRAR,
                     "/v1/m/adoption/trend?lens=telemetry no trae la clave `days`: ausente no es "
                     "cero dias, es que la respuesta cambio de forma")
    dias_tel = len(_trend["days"] or [])
    delta = tel - tel_antes

    di(f"seed-adoption-otlp: receptor {sanea(a.otlp)} -> HTTP {codigo}")
    di(f"  sesiones (lente telemetry) {tel_antes} -> {tel}   (delta {delta:+d}; este sobre trae "
          f"{esperadas} con prefijo {a.prefijo!r})")
    di(f"  equipos con nombre         {len(con_nombre)} de {len(equipos_vistos)} filas; "
          f"{len(flacos)} MIOS por debajo de {a.por_equipo} sesiones")
    if ajenos_flacos:
        di(f"  ⓘ y {len(ajenos_flacos)} equipo(s) AJENOS por debajo de ese umbral "
              f"({', '.join(sorted(x for x in ajenos_flacos if x)[:4])}): salen en la foto pero NO "
              "son de este sembrado, asi que no deciden su veredicto.")
    di(f"  dias de tendencia          {dias_tel}  (con ?lens=telemetry; sin el, la lente por "
          "defecto es `analytics` y sale 0)")
    di("  ⛔ PARA LA CAPTURA: la consola abre en la lente `analytics`, que sigue a ceros por "
          "diseño (Admin Analytics API). Selecciona `telemetry` o la foto sale vacia igual.")
    if delta == 0:
        # ⛔ AQUI SE AFIRMABA UNA CAUSA QUE NADIE HABIA MEDIDO, y dos ramas mas abajo este mismo
        #    `elif` hace lo contrario y bien («NO SE EXPLICA: SE DICE QUE NO SE PUEDE ATRIBUIR»).
        #    Un delta de cero tiene DOS causas que dan el mismo sintoma y que desde aqui no se
        #    distinguen: el re-pase de un sobre identico ya entregado, y un receptor que acusa 200
        #    y no persiste nada (la recarga rechazada por colision de puerto, el store apagado —
        #    las dos trampas que la cabecera de este fichero documenta). Escribir la primera como
        #    hecho convertia el sintoma de un fallo en una nota tranquilizadora.
        if not se_movio:
            di(f"  ⓘ delta CERO y la cifra NO se movio ni una vez en la ventana: dos causas dan "
                  f"este mismo sintoma y desde aqui NO se distinguen — (a) re-pase de un sobre de "
                  f"{a.prefijo!r} byte a byte identico ya entregado, que es lo esperado, y (b) el "
                  "receptor acuso 200 y no persistio nada. Lo que si se comprueba abajo, y es la "
                  "pregunta que importa para una captura, es si la PANTALLA tiene datos.")
        else:
            di(f"  ⓘ delta CERO pero la cifra SI se movio durante la ventana y volvio a "
                  f"{tel}: hubo ingesta y algo la compenso. No lo atribuyo desde aqui.")
    elif delta > esperadas:
        # ⛔ NO SE EXPLICA: SE DICE QUE NO SE PUEDE ATRIBUIR. Con este mismo sintoma —delta +90
        #    sobre un sobre de 48— yo escribi «es atraso de ingesta», no lo probe, y con esa
        #    historia blinde una afirmacion FALSA de deduplicacion. Un delta mayor que el sobre
        #    puede ser atraso de ingesta ajena o puede ser que algo este duplicando; este guion NO
        #    lo puede distinguir desde aqui, y decirlo es la unica respuesta honesta.
        di(f"  ⚠ delta {delta:+d} sobre un sobre de {esperadas}: NO puedo atribuirlo. Puede ser "
              "ingesta anterior cayendo o puede ser duplicacion, y desde aqui no se distingue. "
              "Para el veredicto de idempotencia, `--control-dedup`, que reproduce DOS EJECUCIONES.")

    # ⛔ LO QUE ESTE GUION NO PUEDE AFIRMAR, dicho en vez de disimulado: la superficie de adopcion no
    #    expone las sesiones una a una, asi que **no puedo atribuir a ESTE sobre las filas que veo**.
    #    Sobre un estate ya poblado por otra via, las comprobaciones de abajo pasarian igual. Lo que
    #    si contestan —y es la pregunta que importa para una captura— es si la PANTALLA tiene datos.
    if tel < esperadas:
        return salir(RC_RECHAZADO,
                     f"el motor acuso el sobre pero solo veo {tel} sesiones de {esperadas}")
    if not con_nombre:
        return salir(RC_RECHAZADO,
                     "hay sesiones pero NINGUN equipo con nombre: falta `resource_labels` en la "
                     "fuente, o la recarga salio `rejected=1` por colision de puerto (ver cabecera)")
    if len(con_nombre) < len(equipos):
        return salir(RC_RECHAZADO,
                     f"solo {len(con_nombre)} equipos con nombre de los {len(equipos)} del sobre")
    if flacos:
        return salir(RC_RECHAZADO,
                     f"equipos por debajo de {a.por_equipo} sesiones: {flacos}")
    if dias_tel == 0:
        return salir(RC_RECHAZADO, "sesiones y equipos si, pero la tendencia sale a 0 dias")
    return RC_LIMPIO


if __name__ == "__main__":
    sys.exit(main())
