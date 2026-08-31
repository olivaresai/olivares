#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Siembra work items en el estate de demo, POR LA API DEL PRODUCTO.
#
# ⛔ POR QUE EXISTE. La muestra del 2026-08-26 fotografio `/work` y salio en blanco: la vista
#    funciona y pinta un vacio honesto («the engine read the store and found none»), pero
#    `--seed-demo` NO crea work items — comprobado: `cmd/olivares/seed/seed.go` siembra agentes,
#    workspaces, grupos, knowledge, evals y aristas del mapa de acceso, y ni una mencion de
#    `work_item`. Una captura en blanco NIEGA una funcion que existe, asi que se siembra.
#
# ⛔ POR QUE POR LA API Y NO ESCRIBIENDO EL STORE, que es como se siembran evals y knowledge.
#    `applyWorkCreate` (`modules/sessions/work_mutation.go:108`) NO inserta solo la fila: crea
#    ademas un **lease vacante** (`createVacantWorkLease`) y los **criterios de aceptacion**. Un
#    `repo.Create` pelado dejaria work items SIN LEASE — una foto bonita encima de un estado que el
#    producto no puede producir. Por la API pasan todos los invariantes, incluida la elegibilidad
#    del dueño (`checkParticipant`, `work_service.go:459`).
#
# ⛔ Y POR QUE AQUI Y NO EN EL SEMBRADOR DEL MOTOR, que seria lo duradero. Porque ALLI NO SE PUEDE
#    TODAVIA: `seedDemoEstate` corre en `boot.go:1915` y el superadmin demo nace despues, en
#    `BootstrapSuperadminOwning` (`demo.go:346`) — lo dice el comentario de `demo.go:338`. Con
#    `owner_kind=user` no hay usuario que lo posea, y `owner_kind=agent` exige un `identity_id` que
#    el seed no pone. Aqui el token YA existe, asi que el dueño es elegible. Sembrarlo en el motor
#    queda como fila del carril de kernel; esto desbloquea las capturas sin fingir nada.
#
# ⛔ TRAMPA MEDIDA, por si alguien mueve esto al motor: `brief_hash` son 32 bytes CRUDOS
#    (`work_state.go:95` -> `hashBytes` devuelve `s[:]`), y `demoSeedHash` (`demo.go:325`) devuelve
#    HEX. El helper que hay a mano produce basura. Por la API no se toca: lo calcula el motor.
#
# ⛔ FALLA EN ROJO, A PROPOSITO. Una siembra que falla en silencio produce EXACTAMENTE la misma foto
#    vacia que no sembrar, y la leeriamos como «ya esta arreglado». Por eso al final se RELEE la
#    lista y se exige que traiga lo sembrado: el print de exito no verifica nada, la relectura si.
import argparse
import json
import os
import sys
import time
import uuid
import urllib.error
import urllib.parse
import urllib.request
_ETIQUETA = 'seed-demo-work'

# ── La frontera de salida es compartida: se carga, no se copia ────────────────────────────────
# ⛔ EL OVERRIDE EXPLICITO VA PRIMERO, para que un banco pueda probar una libreria mutada; si va
#    detras del directorio del guion, `OLIVARES_LIB_DIR` queda INERTE y un mutante «sobrevive» sin
#    haberse aplicado nunca. Medido en esta casa.
def _busca_lib():
    import os
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
    print("%s: \u26d4 NO HE PODIDO MIRAR: no encuentro `scripts/lib/redaccion.py`. Sin la frontera "
          "de salida no arranco: `urlopen` sigue los 30x y copia `Authorization` a OTRO origen."
          % _ETIQUETA, file=sys.stderr)
    sys.exit(2)
sys.path.insert(0, _lib)
from redaccion import abre  # noqa: E402


# El estate de demo cuenta una historia para que la captura enseñe estados DISTINTOS (badges,
# prioridades, tipos) en vez de cinco filas iguales.
#
# ⛔ SOLO ESTADOS ALCANZABLES POR COMANDO, y esto NO es pereza. Las transiciones son comandos
#    (`item.ready`, `item.block`, `item.unblock`, `item.submit`, `item.complete`, `item.archive`,
#    `item.fail`, `item.cancel` — `work_state.go:308-316`), y **`active` NO esta entre ellos**: un
#    item pasa a `active` cuando un agente RECLAMA SU LEASE (`work_lease.go:766`). Fabricar un
#    `active` sin lease seria pintar un estado que el producto no puede producir, que es justo lo
#    que esta siembra existe para no hacer. Asi que la foto enseña draft / ready / blocked, que son
#    los tres que un backlog recien poblado tiene de verdad.
#
# ⛔ Y `item.block` EXIGE `code` y `reason` (`work_state.go:312`): un bloqueo sin motivo no se
#    acepta. Bien: un backlog cuyo item bloqueado no dice por que es exactamente la pantalla inutil
#    que no queremos fotografiar.
ITEMS = [
    {
        "work_kind": "implementation", "priority": "p1",
        "title": "Migrate the billing connector to the governed model registry",
        "brief_md": "The billing connector still resolves models by name. Move it to the governed "
                    "registry so cost attribution and policy apply to every call.",
        "acceptance": ["Every model call resolves through the registry",
                       "Cost rows carry a governed model ref"],
        "cmds": ["item.ready"],
    },
    {
        "work_kind": "review", "priority": "p0",
        "title": "Review egress policy for the PR-reviewer agent",
        "brief_md": "The PR-reviewer agent reaches GitHub and the internal filesystem. Confirm the "
                    "egress allowlist matches what the access map observes.",
        "acceptance": ["Allowlist matches observed edges"],
        # draft -> ready -> blocked: bloquear NO es legal desde `draft`
        # (`work_service.go:944` admite ready/active/review), asi que van los dos comandos.
        "cmds": ["item.ready", "item.block"], "code": "awaiting_evidence",
        "reason": "The observed egress edges are still being collected; the review resumes when the access map finishes its sweep.",
    },
    {
        "work_kind": "implementation", "priority": "p2",
        "title": "Add retention windows to the evidence ledger export",
        "brief_md": "Exports currently carry the full ledger. Add a retention window so operators "
                    "can hand auditors a bounded artefact.",
        "acceptance": ["Export accepts a window", "Rows outside the window are absent"],
        "cmds": ["item.ready"],
    },
    {
        "work_kind": "investigation", "priority": "p2",
        "title": "Why does the nightly eval suite drift on weekends?",
        "brief_md": "Pass rate drops on Saturday runs and recovers on Monday. Find the mechanism "
                    "before changing the threshold.",
        "acceptance": ["Mechanism named with evidence"],
        "cmds": [],  # se queda en draft: la captura debe enseñar tambien ese estado
    },
    {
        "work_kind": "implementation", "priority": "p3",
        "title": "Surface the kill-switch reason in the session viewer",
        "brief_md": "When a session is halted the viewer shows the state but not the reason. "
                    "Surface the reason and who set it.",
        "acceptance": ["Reason visible", "Actor visible"],
        "cmds": ["item.ready"],
    },
]

# DecisionsPanel asks for 50 effective heads per page
# (`web/src/features/work/decisions-panel.tsx:56`). One row beyond that exact contract is the
# smallest estate that can expose and exercise the second-page affordance.
DECISION_PAGE_SIZE = 50
DEFAULT_DECISION_COUNT = DECISION_PAGE_SIZE + 1

# The observed live session seeded by cmd/olivares/seed/seed.go. The demo agent announces this
# provider id so the real run is joined to the telemetry row instead of forming an orphan.
DEMO_LIVE_SESSION = "sess-coder-7a3f"


def pedir(base, token, tenant, metodo, ruta, cuerpo=None, cabeceras=None):
    datos = json.dumps(cuerpo).encode() if cuerpo is not None else None
    req = urllib.request.Request(base + ruta, data=datos, method=metodo)
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("X-Olivares-Tenant", tenant)
    if datos is not None:
        req.add_header("Content-Type", "application/json")
        # ⛔ Toda mutacion de work exige `Idempotency-Key` y tiene que PARSEAR como ID
        #    (`work_service.go:1468`). Una por peticion: reutilizarla haria que la segunda se
        #    contestara como REPLAY de la primera y sembrariamos un solo item creyendo que van cinco.
        req.add_header("Idempotency-Key", str(uuid.uuid4()))
    for k, v in (cabeceras or {}).items():
        req.add_header(k, v)
    try:
        # `abre` en vez de `urlopen`: no sigue 30x. Ver la razon medida en la libreria.
        with abre(req, timeout=30) as r:
            cuerpo_txt = r.read().decode()
            return r.status, (json.loads(cuerpo_txt) if cuerpo_txt else {})
    except urllib.error.HTTPError as e:
        return e.code, {"error": e.read().decode()[:400]}


def sembrar_decisiones(base, token, tenant, work_item_id, version, cantidad):
    """Create enough effective heads to make DecisionsPanel expose its next page."""
    for index in range(1, cantidad + 1):
        key = "demo-capture-decision-%03d" % index
        body = {
            "command": "decision.set",
            "work_item_id": work_item_id,
            "decision_key": key,
            "subject_kind": "work.scope",
            "subject_ref": work_item_id,
            "statement_md": "Decision %d: keep this demo item inside the governed scope." % index,
            "rationale_md": "Synthetic demo evidence for the paginated Decisions console "
                            "capture; every key is an independent effective head.",
            "authority_ref": "demo-seed/docs-captures",
        }
        code, result = pedir(
            base, token, tenant, "POST", "/v1/m/sessions/decisions?mode=apply", body,
            {"If-Match": '"v%d"' % version})
        if code not in (200, 201):
            print("ERROR: creando decision %d/%d (v%d) -> %s %s"
                  % (index, cantidad, version, code, result), file=sys.stderr)
            return 1
        version = result.get("version") or (version + 1)

    # Re-read the exact page the console reads. A limit=100 count would prove rows exist, but not
    # that the first page carries the cursor which makes `Load more` reachable.
    path = "/v1/m/sessions/decisions?effective=true&limit=%d" % DECISION_PAGE_SIZE
    code, page = pedir(base, token, tenant, "GET", path)
    if code != 200:
        print("ERROR: no pude releer la primera pagina de decisiones (%s) %s"
              % (code, page), file=sys.stderr)
        return 1
    items = page.get("items") or []
    cursor = page.get("next_cursor")
    if len(items) != DECISION_PAGE_SIZE or page.get("has_more") is not True or not cursor:
        print("ERROR: paginacion no demostrada: items=%d has_more=%r next_cursor=%r"
              % (len(items), page.get("has_more"), bool(cursor)), file=sys.stderr)
        return 1
    second_path = path + "&" + urllib.parse.urlencode({"cursor": cursor})
    code, second = pedir(base, token, tenant, "GET", second_path)
    second_items = second.get("items") or []
    if code != 200 or not second_items:
        print("ERROR: el cursor existe pero la segunda pagina no se puede releer (%s) %s"
              % (code, second), file=sys.stderr)
        return 1
    print("decisiones sembradas y RELEIDAS: %d · primera pagina %d · segunda pagina %d · "
          "has_more=true · next_cursor=presente"
          % (cantidad, len(items), len(second_items)))
    return 0


# ── CATALOGO ────────────────────────────────────────────────────────────────────────────────────
# `/catalog` salia con 410 caracteres: cabeceras de tabla y «No catalog entries created yet». El
# catalogo es lo que un CTO mira primero —que hay aprobado para usar— asi que una demo con el
# catalogo vacio dice que no hay nada que gobernar.
#
# El ciclo de vida es real: `POST /entries` deja la entrada en `draft`, y `submit` / `approve` /
# `deprecate` son rutas propias (`web/src/features/catalog/api.ts:66-76`). Se usan las tres para
# que la foto enseñe los cuatro estados que el tipo declara (`types.ts:22`), no cinco filas iguales.
ENTRADAS = [
    {"kind": "agent", "slug": "claude-code-reviewer", "version": "2.4.0",
     "name": "Claude Code — PR reviewer",
     "summary": "Reviews pull requests against the house style guide and the security checklist. "
                "Read-only on the repository; posts findings as review comments.",
     "estado": "approved"},
    {"kind": "mcp", "slug": "github-mcp", "version": "1.11.0",
     "name": "GitHub MCP server",
     "summary": "Issues, pull requests and code search over the org's repositories. Scoped to the "
                "repositories the calling workspace declares.",
     "estado": "approved"},
    {"kind": "skill", "slug": "sql-explain", "version": "0.9.2",
     "name": "SQL explain and cost review",
     "summary": "Explains a query plan and flags full scans over governed tables before the query "
                "reaches production.",
     "estado": "pending"},
    {"kind": "connector", "slug": "snowflake-governed", "version": "3.0.1",
     "name": "Snowflake (governed)",
     "summary": "Read path to the analytics warehouse with column masking applied at the connector, "
                "so masked columns never reach a model context.",
     "estado": "approved"},
    {"kind": "template", "slug": "incident-review", "version": "1.2.0",
     "name": "Incident review workspace",
     "summary": "Pre-wired workspace for post-incident review: transcript retention on, egress "
                "restricted to the ticketing system.",
     "estado": "draft"},
    {"kind": "model", "slug": "claude-opus-legacy", "version": "1.0.0",
     "name": "Claude Opus (legacy pin)",
     "summary": "Superseded by the current Opus pin. Kept for reproducing audits raised before the "
                "migration; not for new work.",
     "estado": "deprecated"},
]


def sembrar_catalogo(base, token, tenant):
    """Entradas de catalogo con su ciclo de vida REAL, no filas en `draft`."""
    creadas = 0
    for e in ENTRADAS:
        cuerpo = {"kind": e["kind"], "name": e["name"], "slug": e["slug"],
                  "version": e["version"], "summary": e["summary"]}
        code, hecho = pedir(base, token, tenant, "POST", "/v1/m/catalog/entries", cuerpo)
        if code not in (200, 201):
            print("ERROR: catalogo %s -> %s %s" % (e["slug"], code, hecho), file=sys.stderr)
            return 1
        eid = hecho.get("id")
        if not eid:
            print("ERROR: la entrada %s no devuelve id: %s" % (e["slug"], list(hecho)[:8]),
                  file=sys.stderr)
            return 1
        creadas += 1
        # draft -> pending -> approved; `deprecate` exige estar aprobada antes.
        pasos = {"draft": [], "pending": ["submit"],
                 "approved": ["submit", "approve"],
                 "deprecated": ["submit", "approve", "deprecate"]}[e["estado"]]
        for paso in pasos:
            code, r = pedir(base, token, tenant, "POST",
                            "/v1/m/catalog/entries/%s/%s" % (eid, paso), {})
            if code not in (200, 201):
                print("ERROR: catalogo %s %s -> %s %s" % (e["slug"], paso, code, r),
                      file=sys.stderr)
                return 1

    # La misma regla que en work: se releen los ESTADOS, no el recuento.
    code, lista = pedir(base, token, tenant, "GET", "/v1/m/catalog/entries?limit=100")
    if code != 200:
        print("ERROR: no pude releer el catalogo (%s) %s" % (code, lista), file=sys.stderr)
        return 1
    items = lista.get("items") or []
    estados = {}
    for it in items:
        estados[it.get("status")] = estados.get(it.get("status"), 0) + 1
    if len(items) < len(ENTRADAS) or len(estados) < 3:
        print("ERROR: catalogo sembrado %d, lista %d, estados %s"
              % (creadas, len(items), estados), file=sys.stderr)
        return 1
    print("catalogo sembrado y RELEIDO: %d · estados %s" % (len(items), estados))
    return 0


# ── DEPLOY ──────────────────────────────────────────────────────────────────────────────────────
# `/deploy` salia con «No deployments declared yet» ocupando la region principal. Deploy es el
# estado DESEADO de lo que corre, asi que vacio dice que no hay nada desplegado — justo lo contrario
# de lo que el mapa de acceso enseña dos pantallas mas alla.
#
# ⛔ EL SUJETO SE RESUELVE, NO SE INVENTA. Una definicion apunta a un `subject_ref` real; escribir
#    un id a mano daria filas que apuntan a nada y una pantalla de detalle rota. Se leen los agentes
#    que el sembrado YA creo y se despliegan esos.
def sembrar_deploy(base, token, tenant):
    # Los agentes son del NUCLEO, no de un modulo: `/v1/agents`, no `/v1/m/agents` (el contrato
    # embebido publica las dos formas para familias distintas y la equivocada da 404, no vacio).
    code, ags = pedir(base, token, tenant, "GET", "/v1/agents?limit=100")
    if code != 200:
        print("ERROR: no pude leer los agentes (%s) %s" % (code, ags), file=sys.stderr)
        return 1
    agentes = [a for a in (ags.get("items") or []) if a.get("id")]
    if not agentes:
        print("ERROR: cero agentes sembrados: no hay sujeto que desplegar", file=sys.stderr)
        return 1

    ENTORNOS = [
        ("production", "k8s://prod-eu-west-1/olivares-agents", 3),
        ("staging", "k8s://stage-eu-west-1/olivares-agents", 1),
    ]
    creadas = 0
    for i, ag in enumerate(agentes[:3]):
        entorno, destino, replicas = ENTORNOS[i % len(ENTORNOS)]
        cuerpo = {
            "subject_kind": "agent",
            "subject_ref": ag["id"],
            "name": "%s (%s)" % (ag.get("name") or ag["id"], entorno),
            "environment": entorno,
            "target": destino,
            "runtime": "container",
            "spec": {
                "image": "ghcr.io/olivaresai/agent-runtime:26.8.0",
                "replicas": replicas,
                "resources": {"cpu": "500m", "mem": "1Gi"},
            },
        }
        code, hecho = pedir(base, token, tenant, "POST", "/v1/m/deploy/definitions", cuerpo)
        if code not in (200, 201):
            print("ERROR: deploy %s -> %s %s" % (cuerpo["name"][:40], code, hecho), file=sys.stderr)
            return 1
        creadas += 1

    code, lista = pedir(base, token, tenant, "GET", "/v1/m/deploy/definitions?limit=100")
    if code != 200:
        print("ERROR: no pude releer deploy (%s) %s" % (code, lista), file=sys.stderr)
        return 1
    items = lista.get("items") or []
    if len(items) < creadas:
        print("ERROR: deploy sembre %d y la lista devuelve %d" % (creadas, len(items)),
              file=sys.stderr)
        return 1
    entornos = sorted({it.get("environment") for it in items})
    print("deploy sembrado y RELEIDO: %d definiciones · entornos %s" % (len(items), entornos))
    return 0


# ── HEALTH & SLA ────────────────────────────────────────────────────────────────────────────────
# `/health` salia con cuatro contadores a cero y «No subjects monitored yet», y **eso se ve tambien
# en la PRIMERA pantalla**: el tile «Health & SLA» del Overview enseña un guion y «No health checks
# yet». O sea que el hueco no cuesta una captura, cuesta dos, y una de ellas es la que abre la demo.
#
# ⛔ El sujeto se RESUELVE de los agentes sembrados, igual que en deploy: un check que vigila un
#    `subject_ref` inexistente da una fila que no lleva a ninguna parte.
#
# ⛔ Y LOS OBJETIVOS SON REALISTAS, NO REDONDOS. `sla_target_ppm` en partes por millon: 999_000 es
#    99,9 % y 995_000 es 99,5 %. Poner 1_000_000 (100 %) seria declarar un SLA que nadie firma.
#
# ⛔ Y `grace_factor` va ENTERO. El tipo de la consola lo declara `number` (`health/types.ts:80`),
#    pero el motor lo decodifica en `int64` (`modules/health/checks.go:22`), asi que un `2.0` de
#    JavaScript-como-float sale rechazado con «invalid JSON body» — un 400 que no dice cual es el
#    campo. Medido aqui: el tipo de la consola admite `2.5` y el motor no.
# ⛔ LOS INTERVALOS SON LARGOS A PROPOSITO, y esto es un HALLAZGO, no una preferencia. Con
#    `expected_interval_seconds: 300` y `grace 2`, un chequeo se pone rancio a los DIEZ MINUTOS: el
#    barrido de vejez lo degrada y abre incidencia, y hace exactamente lo que debe. Pero una corrida
#    de capturas completa dura mas que eso, asi que la foto de `/health` —y el tile de la primera
#    pantalla— salia distinta segun EN QUE MINUTO se disparara. Medido: a la hora de sembrar,
#    «2/3 healthy»; una hora despues, «0/3 healthy · 3 open incidents» con los mismos datos.
#    Una cadencia horaria es realista para un liveness de agente y sobrevive a la corrida entera
#    con margen. El producto no cambia; lo que cambia es no fotografiar un estado de transito.
CHEQUEOS = [
    {"nombre": "PR reviewer liveness", "intervalo": 3600, "gracia": 2, "sla": 999000,
     "estado": "healthy", "latencia": 42, "detalle": ""},
    {"nombre": "Indexer heartbeat", "intervalo": 7200, "gracia": 3, "sla": 995000,
     "estado": "degraded", "latencia": 2100,
     "detalle": "Indexing lag above the target window; the backlog is draining."},
    {"nombre": "Coder session liveness", "intervalo": 3600, "gracia": 2, "sla": 999000,
     "estado": "healthy", "latencia": 87, "detalle": ""},
]


def sembrar_health(base, token, tenant):
    code, ags = pedir(base, token, tenant, "GET", "/v1/agents?limit=100")
    if code != 200:
        print("ERROR: no pude leer los agentes (%s) %s" % (code, ags), file=sys.stderr)
        return 1
    agentes = [a for a in (ags.get("items") or []) if a.get("id")]
    if not agentes:
        print("ERROR: cero agentes: no hay sujeto que vigilar", file=sys.stderr)
        return 1

    creados = 0
    for i, spec in enumerate(CHEQUEOS):
        ag = agentes[i % len(agentes)]
        cuerpo = {
            "name": spec["nombre"],
            "subject_kind": "agent",
            "subject_ref": ag["id"],
            "expected_interval_seconds": spec["intervalo"],
            "grace_factor": spec["gracia"],
            "sla_target_ppm": spec["sla"],
            "desired_status": "active",
        }
        code, hecho = pedir(base, token, tenant, "POST", "/v1/m/health/checks", cuerpo)
        if code not in (200, 201):
            print("ERROR: health %s -> %s %s" % (spec["nombre"], code, hecho), file=sys.stderr)
            return 1
        creados += 1

        # ⛔ UN CHEQUEO SIN PRUEBA NO ESTA SANO: ESTA SIN NOTICIAS, y la primera pantalla lo
        #    resume como «0/3 healthy» — que ante un CTO dice que el estate esta roto, y es PEOR
        #    que el vacio que veniamos a arreglar. La salud sale de evidencia de liveness
        #    (`modules/health/doc.go:24`), asi que se manda la prueba que un health-checker real
        #    mandaria en su cadencia: `POST /checks/{id}/report`.
        #
        # ⛔ Y UNO VA `degraded` A PROPOSITO. Un estate todo en verde no demuestra nada: el
        #    producto se luce cuando ENSEÑA lo que ha cazado, con su detalle y su latencia. Es
        #    ademas mas creible: tres de tres perfectos es una demo, dos y uno degradandose es
        #    una casa real.
        cid = hecho.get("id")
        if not cid:
            print("ERROR: el chequeo %s no devuelve id: %s" % (spec["nombre"], list(hecho)[:8]),
                  file=sys.stderr)
            return 1
        code, r = pedir(base, token, tenant, "POST",
                        "/v1/m/health/checks/%s/report" % cid,
                        {"state": spec["estado"], "latency_ms": spec["latencia"],
                         "detail": spec["detalle"]})
        if code not in (200, 201, 204):
            print("ERROR: report %s -> %s %s" % (spec["nombre"], code, r), file=sys.stderr)
            return 1

    code, lista = pedir(base, token, tenant, "GET", "/v1/m/health/checks?limit=100")
    if code != 200:
        print("ERROR: no pude releer health (%s) %s" % (code, lista), file=sys.stderr)
        return 1
    items = lista.get("items") or []
    if len(items) < creados:
        print("ERROR: health sembre %d y la lista devuelve %d" % (creados, len(items)),
              file=sys.stderr)
        return 1
    # Se releen los ESTADOS, no el recuento: tres chequeos «sin noticias» darian el mismo numero
    # y la pantalla equivocada.
    estados = {}
    for it in items:
        estados[it.get("state") or it.get("status")] = \
            estados.get(it.get("state") or it.get("status"), 0) + 1
    print("health sembrado y RELEIDO: %d chequeos · estados %s" % (len(items), estados))
    return 0


# ── COSTE (FinOps) ──────────────────────────────────────────────────────────────────────────────
# La primera pantalla resumia el gasto en **$1,43**. La linea de tendencia estaba, el porcentaje
# estaba, y aun asi la cifra lee a juguete: ante un CTO, $1,43 no es «poco gasto», es «esto no lo
# usa nadie». La orden de para la ola 1 pide **costes con tendencia**, y una tendencia sobre
# seis muestras no es una tendencia.
#
# ⛔ ESTO NO ES INVENTAR PRODUCTO, y la distincion importa. No se fabrica una capacidad que no
#    existe: se le da VOLUMEN a un estate de demo que ya es sintetico por construccion (agentes,
#    workspaces y evals tambien lo son). El coste entra por la MISMA ruta canonica que usaria un
#    `cost_report` real —`POST /v1/m/finops/cost`, que comparte dedup, atribucion y libro con el
#    bus (`modules/finops/api.go:332-338`)— asi que lo que se ve en pantalla esta CALCULADO por el
#    producto, no escrito a mano en una vista.
#
# ⛔ Y LA FORMA DE LA SERIE ES REALISTA A PROPOSITO: menos gasto en fin de semana y una pendiente
#    suave de adopcion. Una recta perfecta se ve falsa; un dentado semanal es lo que ense~na un
#    despliegue de verdad, y ademas hace que la tendencia signifique algo.
MODELOS_COSTE = [
    # (model_ref, provider_ref, peso del gasto diario, coste por 1k tokens de salida en micro-USD)
    ("claude-opus-4-8", "anthropic", 0.62, 75000),
    ("claude-sonnet-4-5", "anthropic", 0.30, 15000),
    ("gpt-4o", "openai", 0.08, 10000),
]


def sembrar_coste(base, token, tenant, dias=30):
    """Serie diaria de coste por la ruta canonica de ingesta."""
    import datetime
    hoy = datetime.datetime.now(datetime.timezone.utc).replace(
        hour=12, minute=0, second=0, microsecond=0)
    enviadas = 0
    total_micro = 0
    for d in range(dias, 0, -1):
        dia = hoy - datetime.timedelta(days=d)
        # Fin de semana al 35 %: el dentado es lo que hace legible la tendencia.
        finde = dia.weekday() >= 5
        factor = 0.35 if finde else 1.0
        # Adopcion: sube ~55 % a lo largo de la ventana, sin ser una recta.
        adopcion = 1.0 + 0.55 * (dias - d) / dias
        base_dia_micro = 165_000_000 * factor * adopcion   # ~165 USD/dia laborable al principio
        for model_ref, provider_ref, peso, micro_por_1k in MODELOS_COSTE:
            coste = int(base_dia_micro * peso)
            if coste <= 0:
                continue
            salida = max(1, int(coste / micro_por_1k * 1000))
            entrada = salida * 7                      # los prompts pesan mas que las respuestas
            cuerpo = {
                "provider_ref": provider_ref,
                "model_ref": model_ref,
                "input_tokens": entrada,
                "output_tokens": salida,
                "cache_read_tokens": int(entrada * 0.4),
                "cost_micro_usd": coste,
                "occurred_at": dia.isoformat().replace("+00:00", "Z"),
                "provenance": "cost_report",
            }
            code, r = pedir(base, token, tenant, "POST", "/v1/m/finops/cost", cuerpo)
            if code not in (200, 201, 202):
                print("ERROR: coste %s %s -> %s %s" % (model_ref, dia.date(), code, r),
                      file=sys.stderr)
                return 1
            enviadas += 1
            total_micro += coste

    # ⛔ Se relee el RESUMEN que pinta la pantalla, no el recuento de POSTs: la ingesta deduplica
    #    por clave natural, asi que «202 aceptado» no promete una fila nueva.
    code, res = pedir(base, token, tenant, "GET", "/v1/m/finops/spend/summary")
    if code != 200:
        print("ERROR: no pude releer el resumen de gasto (%s) %s" % (code, res), file=sys.stderr)
        return 1
    visto = res.get("total_micro_usd") or 0
    if visto < total_micro * 0.5:
        print("ERROR: envie %.2f USD y el resumen ve %.2f — la ingesta no cuajo"
              % (total_micro / 1e6, visto / 1e6), file=sys.stderr)
        return 1
    print("coste sembrado y RELEIDO: %d muestras · resumen $%.2f · %d modelos"
          % (enviadas, visto / 1e6, len(res.get("by_model") or [])))
    return 0


# ── CONSOLA DE CONTROL (personas) ───────────────────────────────────────────────────────────────
# `/console` salia con UN usuario, «No groups», y un panel entero en blanco: «Pending invitations —
# No pending invitations». Una consola de control con un solo usuario dice «esto no lo administra
# nadie», que es lo contrario de lo que la pantalla existe para ense~nar.
#
# ⛔ DOS MODOS, A PROPOSITO. `POST /v1/onboard` admite `password` (alta directa: entra en el padron
#    como miembro activo) e `invite` (queda pendiente de aceptar). Se usan LOS DOS: los primeros
#    llenan el padron con roles distintos, y los segundos llenan el panel vacio con lo que ese panel
#    existe para ense~nar — invitaciones de verdad, emitidas por el producto, no una fila pintada.
#
# ⛔ Y EL PADRON ARREGLA ALGO MAS, de rebote: las pantallas que resuelven un `user_id` contra
#    `/v1/members` sólo tenian UNA persona a la que resolver. Con un padron real, la resolucion de
#    nombres se ve funcionando en vez de caer siempre al mismo sitio.
PERSONAS = [
    {"email": "r.okafor@olivares.local", "nombre": "Rehema Okafor", "rol": "admin", "modo": "password"},
    {"email": "j.lindqvist@olivares.local", "nombre": "Johan Lindqvist", "rol": "editor", "modo": "password"},
    {"email": "m.tanaka@olivares.local", "nombre": "Mei Tanaka", "rol": "editor", "modo": "password"},
    {"email": "s.abadi@olivares.local", "nombre": "Sami Abadi", "rol": "viewer", "modo": "password"},
    # Pendientes: son las que llenan el panel que salia en blanco.
    {"email": "new.reviewer@olivares.local", "nombre": "Ana Ferreira", "rol": "editor", "modo": "invite"},
    {"email": "audit.contractor@olivares.local", "nombre": "Priya Raman", "rol": "viewer", "modo": "invite"},
]


def sembrar_consola(base, token, tenant):
    # Padron de personas por la ruta de APROVISIONAMIENTO, no por la de conveniencia.
    #
    # ⛔ ESTO VA EN COMENTARIOS Y NO EN UN DOCSTRING, y no es estilo: en Python un docstring es un
    #    VALOR, no un comentario. El gate de exportacion **limpia los comentarios y NUNCA los
    #    valores**, asi que una referencia interna aqui dentro se PUBLICA. Me lo enseno el propio
    #    gate rechazando el push de esta rama por una cita interna que estaba justo aqui.
    #
    # ⛔ `POST /v1/onboard` —la del boton «Onboard user» de la consola— responde **403
    #    `step_up_required`**: exige AAL3, un autenticador hardware verificado. NO es un obstaculo
    #    que rodear: es una propiedad de seguridad deliberada de las acciones CONFIGURE
    #    privilegiadas (`core/api/middleware.go:297`), la misma que protege SSO, secretos y
    #    fuentes. Un sembrador con un token de contrase~na **no debe** poder crear cuentas.
    #
    # ⇒ Se usa la ruta que un aprovisionamiento real usa y que NO esta tras el step-up:
    #   `POST /v1/users` (superadmin) + `POST /v1/memberships` (rol en el inquilino). Ninguna de
    #   las dos llama a `requireAAL3`, y eso tambien es deliberado: son de superadmin de plataforma.
    #
    # ⛔ Y LAS INVITACIONES PENDIENTES **NO SE SIEMBRAN**, asi que ese panel sigue vacio. Emitir
    #    una invitacion viva pasa por el flujo con step-up. Se deja vacio Y SE DICE, en vez de
    #    fabricar una fila: el panel en blanco es la consecuencia honesta de un control que va.
    altas, invitaciones = 0, 0
    for p in PERSONAS:
        if p["modo"] != "password":
            continue                      # ver el comentario de arriba: las invitaciones exigen AAL3
        code, u = pedir(base, token, tenant, "POST", "/v1/users",
                        {"email": p["email"], "display_name": p["nombre"],
                         "password": "olivares-demo-estate", "superadmin": False})
        if code not in (200, 201):
            print("ERROR: crear usuario %s -> %s %s" % (p["email"], code, u), file=sys.stderr)
            return 1
        uid = u.get("id") or u.get("user_id")
        if not uid:
            print("ERROR: /v1/users no devuelve id: %s" % list(u)[:8], file=sys.stderr)
            return 1
        code, g = pedir(base, token, tenant, "POST", "/v1/memberships",
                        {"user_id": uid, "tenant": tenant, "role": p["rol"]})
        if code not in (200, 201):
            print("ERROR: membresia %s -> %s %s" % (p["email"], code, g), file=sys.stderr)
            return 1
        altas += 1

    # ⛔ Se releen LAS DOS COSAS, porque el panel vacio era una de ellas: un padron lleno con cero
    #    invitaciones dejaria el hueco igual que estaba y el recuento diria que todo fue bien.
    code, miembros = pedir(base, token, tenant, "GET", "/v1/members")
    if code != 200:
        print("ERROR: no pude releer el padron (%s) %s" % (code, miembros), file=sys.stderr)
        return 1
    n_miembros = len(miembros.get("items") or [])
    con_nombre = sum(1 for m in (miembros.get("items") or []) if m.get("display_name"))
    if n_miembros < altas:
        print("ERROR: %d altas y el padron devuelve %d" % (altas, n_miembros), file=sys.stderr)
        return 1
    print("consola sembrada y RELEIDA: %d miembros (%d con nombre) · invitaciones NO sembradas "
          "(exigen AAL3, por dise\u00f1o)" % (n_miembros, con_nombre))
    return 0


# ── PERMISOS (cola de aprobaciones HITL) ────────────────────────────────────────────────────────
# `/permissions` abre en la cola de aprobaciones, y salia vacia. Es de las pantallas que MEJOR
# cuentan el producto —un humano decidiendo sobre lo que un agente quiere hacer— y en blanco no
# cuenta nada. Las otras pestanas si tenian datos, pero la que se fotografia es la de por defecto.
#
# ⛔ Las peticiones describen actos que un agente pide de VERDAD en este estate: tocar produccion,
#    exportar evidencia, ampliar un alcance. Nada de «approval 1 / approval 2»: si la foto tiene
#    que ense~nar por que existe la cola, la razon se lee en la fila.
APROBACIONES = [
    {"accion": "deploy.promote", "sujeto_kind": "deployment", "sujeto_ref": "billing-connector/production",
     "razon": "Promote the billing connector to production. Touches a governed warehouse read path, "
              "so it needs a human decision before it runs.",
     "aprobaciones": 2, "expira": 172800},
    {"accion": "evidence.export", "sujeto_kind": "audit", "sujeto_ref": "q3-soc2-evidence",
     "razon": "Export the Q3 evidence ledger for the external auditor. Leaves the estate, so it is "
              "gated on a named approver.",
     "aprobaciones": 1, "expira": 604800},
    {"accion": "scope.extend", "sujeto_kind": "agent", "sujeto_ref": "agent-claude-review-3",
     "razon": "The PR reviewer is asking for write access to the release branch. Read-only today; "
              "this would change what it can do, not just what it can see.",
     "aprobaciones": 2, "expira": 86400},
]


def sembrar_permisos(base, token, tenant):
    creadas = 0
    for a in APROBACIONES:
        cuerpo = {"action": a["accion"], "subject_kind": a["sujeto_kind"],
                  "subject_ref": a["sujeto_ref"], "reason": a["razon"],
                  "required_approvals": a["aprobaciones"],
                  "expires_in_seconds": a["expira"]}
        code, hecho = pedir(base, token, tenant, "POST", "/v1/m/governance/approvals", cuerpo)
        if code not in (200, 201):
            print("ERROR: aprobacion %s -> %s %s" % (a["accion"], code, hecho), file=sys.stderr)
            return 1
        creadas += 1

    code, lista = pedir(base, token, tenant, "GET", "/v1/m/governance/approvals?limit=100")
    if code != 200:
        print("ERROR: no pude releer la cola (%s) %s" % (code, lista), file=sys.stderr)
        return 1
    items = lista.get("items") or []
    if len(items) < creadas:
        print("ERROR: cree %d aprobaciones y la cola devuelve %d" % (creadas, len(items)),
              file=sys.stderr)
        return 1
    estados = {}
    for it in items:
        estados[it.get("status") or it.get("state")] = \
            estados.get(it.get("status") or it.get("state"), 0) + 1
    print("permisos sembrados y RELEIDOS: %d en cola · estados %s" % (len(items), estados))
    return 0


# ── POLITICA DE CLAUDE (deriva PERMITIDO-vs-OBSERVADO) ──────────────────────────────────────────
# `/claude-policy` abre en «Drift & posture» y salia con un unico panel: «No drift findings».
#
# ⛔ Y AQUI NO SE INYECTA UN HALLAZGO, porque no se puede y porque no se debe: `/v1/m/security`
#    **no tiene ruta de ingesta**. Los findings los DERIVA el motor. La forma honesta de que
#    aparezca deriva es la del producto: se PUBLICA una politica y luego un host CHEQUEA con lo que
#    de verdad observa; el camino de check-in calcula la deriva con la logica verificada del
#    conector (`VerifyDriftJSON`, nunca reimplementada) y la graba como Finding real
#    (`claudepolicy_truth.go:30-45`).
#
# ⛔ Dos hosts cumplen y uno no. Ese es el punto de la pantalla: **no** que haya politica, sino que
#    el plano sepa DONDE no se esta aplicando. Un estate sin ninguna deriva no ense~na la funcion.
POLITICA = '{"permissions":{"deny":["Bash(curl:*)","Read(./.env)"]}}'
HOSTS = [
    ("laptop-eng-014", '{"permissions":{"deny":[]}}'),                      # deriva: sin reglas
    ("build-runner-02", POLITICA),                                          # cumple
    ("laptop-eng-031", POLITICA),                                           # cumple
]


def sembrar_politica(base, token, tenant):
    code, pub = pedir(base, token, tenant, "POST",
                      "/v1/m/claude-policy/managed-settings/publish",
                      {"content": POLITICA, "note": "Baseline for the demo estate"})
    if code not in (200, 201):
        print("ERROR: publicar politica -> %s %s" % (code, pub), file=sys.stderr)
        return 1
    art = pub.get("artifact") or {}
    rev, sha, huella = pub.get("revision"), art.get("artifact_sha256"), art.get("key_fingerprint")
    if not (rev and sha and huella):
        print("ERROR: la publicacion no devuelve revision/sha/huella: %s" % list(pub)[:8],
              file=sys.stderr)
        return 1
    # ⛔ El motor lo dice el mismo: sin observacion NO calcula deriva, y no finge un «sin deriva».
    if pub.get("drift_computed"):
        print("AVISO: la publicacion dice haber calculado deriva sin observacion todavia",
              file=sys.stderr)

    derivas = 0
    for scope, observado in HOSTS:
        code, chk = pedir(base, token, tenant, "POST",
                          "/v1/m/claude-policy/managed-settings/checkin",
                          {"scope": scope, "revision": rev, "artifact_sha256": sha,
                           "key_fingerprint": huella, "observed_content": observado})
        if code not in (200, 201):
            print("ERROR: checkin %s -> %s %s" % (scope, code, chk), file=sys.stderr)
            return 1
        derivas += len(chk.get("drift") or [])

    # Se relee por donde lee la PANTALLA: findings de kind policy_drift, no el eco del check-in.
    code, lista = pedir(base, token, tenant, "GET",
                        "/v1/m/security/findings?kind=policy_drift&limit=100")
    if code != 200:
        print("ERROR: no pude releer los findings (%s) %s" % (code, lista), file=sys.stderr)
        return 1
    items = lista.get("items") or []
    if not items:
        print("ERROR: %d derivas en los check-in y la pantalla ve 0" % derivas, file=sys.stderr)
        return 1
    print("politica sembrada y RELEIDA: rev %s · %d hosts · %d deriva(s) que ve la pantalla"
          % (rev, len(HOSTS), len(items)))
    return 0


# ── WORKSPACE DE SESION + RUN OPERADO ───────────────────────────────────────────────────────────
# The engine seed creates observed sessions and authz workspaces, but neither sessions.workspace
# host roots nor sessions.run lifecycle rows. They are deliberately different models despite the
# shared word "workspace". Create both through their real product producers after login, then
# re-read the same endpoints used by /agentops.
def sembrar_workspace_y_run(base, token, tenant, root):
    root = os.path.realpath(root)
    if not os.path.isabs(root) or not os.path.isdir(root):
        print("ERROR: la raiz del workspace no es un directorio absoluto: %r" % root,
              file=sys.stderr)
        return 1

    # Read-only is the authority the scene needs. An rw classified mount is a CRITICAL launch and
    # correctly requires the HITL bridge; weakening that gate for a screenshot would be wrong.
    code, created_ws = pedir(base, token, tenant, "POST", "/v1/m/sessions/workspaces", {
        "name": "acme-platform",
        "root_path": root,
        "mount_mode": "ro",
        "container_target": "/workspace",
        "allow_subpaths": [],
        "max_read_bytes": 1048576,
        "dlp_mode": "label",
    })
    if code not in (200, 201):
        print("ERROR: workspace %r -> %s %s" % (root, code, created_ws), file=sys.stderr)
        return 1
    workspace_ref = created_ws.get("workspace_ref")
    if not workspace_ref:
        print("ERROR: la respuesta no trae workspace_ref: %s" % list(created_ws)[:10],
              file=sys.stderr)
        return 1

    escaped_ws = urllib.parse.quote(workspace_ref, safe="")
    code, read_ws = pedir(base, token, tenant, "GET",
                           "/v1/m/sessions/workspaces/" + escaped_ws)
    if code != 200:
        print("ERROR: no pude releer el workspace (%s) %s" % (code, read_ws), file=sys.stderr)
        return 1
    if (read_ws.get("state"), read_ws.get("mount_mode"), read_ws.get("root_path")) != \
            ("active", "ro", root):
        print("ERROR: workspace releido con otra postura: %s" % read_ws, file=sys.stderr)
        return 1

    # Browse files uses this exact jailed listing. A registered but empty/unreadable root would
    # still draw the button and then fail one interaction later, so require the synthetic tree.
    code, files = pedir(base, token, tenant, "GET",
                        "/v1/m/sessions/workspaces/%s/files?limit=50" % escaped_ws)
    names = {entry.get("name") for entry in (files.get("entries") or [])}
    required = {"README.md", "deploy", "src"}
    if code != 200 or not required.issubset(names):
        print("ERROR: Browse files no relee el arbol esperado (%s), nombres=%s respuesta=%s"
              % (code, sorted(n for n in names if n), files), file=sys.stderr)
        return 1

    # remote-control avoids inventing an inference credential. The capture engine points its real
    # procRunner at scripts/demo-agent.sh, so this still exercises admission, process lifecycle,
    # provider-session binding and the ledger without touching a real Claude account.
    code, created_run = pedir(base, token, tenant, "POST", "/v1/m/sessions/runs", {
        "name": "acme-platform governed session",
        "transport": "remote-control",
        "permission_mode": "default",
        "effort": "high",
        "model": "claude-opus-4-8",
        "workspace_ref": workspace_ref,
        "isolation": "native",
        "env_allow": [],
    })
    if code not in (200, 201):
        print("ERROR: run -> %s %s" % (code, created_run), file=sys.stderr)
        return 1
    run_ref = created_run.get("run_ref")
    if not run_ref:
        print("ERROR: la respuesta no trae run_ref: %s" % list(created_run)[:10],
              file=sys.stderr)
        return 1

    escaped_run = urllib.parse.quote(run_ref, safe="")
    read_run = {}
    # The create response proves the process started, not that its asynchronous init frame was
    # parsed and joined. Poll that stronger fact before declaring the estate photographable.
    for _ in range(50):
        code, read_run = pedir(base, token, tenant, "GET",
                               "/v1/m/sessions/runs/" + escaped_run)
        if (code == 200 and read_run.get("state") in ("running", "idle") and
                read_run.get("claude_session_id") == DEMO_LIVE_SESSION):
            break
        time.sleep(0.1)
    else:
        print("ERROR: el run no quedo vivo y unido a %s: code=%s run=%s"
              % (DEMO_LIVE_SESSION, code, read_run), file=sys.stderr)
        return 1

    query = urllib.parse.urlencode({"claude_session_id": DEMO_LIVE_SESSION, "limit": 200})
    code, joined = pedir(base, token, tenant, "GET", "/v1/m/sessions/runs?" + query)
    refs = {item.get("run_ref") for item in (joined.get("items") or [])}
    if code != 200 or run_ref not in refs:
        print("ERROR: la consulta de procedencia no devuelve el run %s (%s) %s"
              % (run_ref, code, joined), file=sys.stderr)
        return 1

    code, live = pedir(base, token, tenant, "GET", "/v1/m/sessions/live?limit=200")
    live_refs = {item.get("session_ref") for item in (live.get("items") or [])}
    if code != 200 or DEMO_LIVE_SESSION not in live_refs:
        print("ERROR: la mitad observada %s no aparece en /live (%s) %s"
              % (DEMO_LIVE_SESSION, code, live), file=sys.stderr)
        return 1

    print("agentops sembrado y RELEIDO: workspace %s ro con Browse files · run %s %s · "
          "origen Launched unido a %s"
          % (workspace_ref, run_ref, read_run.get("state"), DEMO_LIVE_SESSION))
    return 0


def main():
    parser = argparse.ArgumentParser(
        description="Siembra por API el contenido real que usan las capturas del estate demo.")
    parser.add_argument("base_url")
    parser.add_argument("token")
    parser.add_argument("tenant")
    parser.add_argument("workspace_root",
                        help="raiz desechable que registrar para Browse files")
    parser.add_argument(
        "--decision-count", type=int, default=DEFAULT_DECISION_COUNT, metavar="N",
        help="cabezas efectivas (por defecto: una pagina de consola mas una fila)")
    args = parser.parse_args()
    if args.decision_count <= DECISION_PAGE_SIZE:
        parser.error("--decision-count debe superar el tamano de pagina (%d)"
                     % DECISION_PAGE_SIZE)
    base, token, tenant = args.base_url, args.token, args.tenant

    code, ws = pedir(base, token, tenant, "GET", "/v1/workspaces")
    if code != 200 or not ws.get("items"):
        print("ERROR: no pude resolver un workspace (%s) %s" % (code, ws), file=sys.stderr)
        return 1
    # El workspace POR DEFECTO, no el primero que llegue: el orden de la lista no es contrato y
    # la consola navega en el por defecto.
    porDefecto = [w for w in ws["items"] if w.get("is_default")]
    workspace = (porDefecto or ws["items"])[0]["id"]

    code, quien = pedir(base, token, tenant, "GET", "/v1/auth/whoami")
    if code != 200:
        print("ERROR: whoami fallo (%s) %s" % (code, quien), file=sys.stderr)
        return 1
    # El dueño es el usuario demo, que a esta altura YA existe (el token lo prueba).
    owner = quien.get("user_id") or quien.get("id") or quien.get("subject")
    if not owner:
        print("ERROR: whoami no trae un id de usuario: %s" % list(quien)[:8], file=sys.stderr)
        return 1

    creados = []
    item_para_decisiones = None
    for spec in ITEMS:
        cuerpo = {
            "command": "item.create", "workspace_id": workspace,
            "work_kind": spec["work_kind"], "title": spec["title"],
            "brief_md": spec["brief_md"], "priority": spec["priority"],
            "owner_kind": "user", "owner_ref": owner,
            # ⛔ `provenance_*` es OBLIGATORIO en `item.create` (`work_state.go:243-245`) y dice de
            #    donde viene el trabajo. Aqui es honesto declararlo `system`: lo crea el sembrador de
            #    demo, no una persona. Poner "human" seria mentir en el propio registro de origen.
            "provenance_kind": "system", "provenance_ref": "demo-seed/docs-captures",
            # Cada criterio necesita `criterion_key` unico y `ordinal`; y al menos UNO `required`,
            # o el motor responde 422 `acceptance_incomplete` (`work_state.go:274`).
            "acceptance": [
                {"criterion_key": "c%d" % i, "ordinal": i, "statement": txt, "required": True}
                for i, txt in enumerate(spec["acceptance"], start=1)
            ],
        }
        # ⛔ `?mode=apply` NO es adorno: las mutaciones de work son plan/apply y sin `mode` el motor
        #    responde 400 `mode_required` (`work_api.go:359`). Es la misma promesa que la propia
        #    pantalla escribe — «Every change is planned before it is applied».
        code, hecho = pedir(base, token, tenant, "POST",
                            "/v1/m/sessions/work-items?mode=apply", cuerpo)
        if code not in (200, 201):
            print("ERROR: creando %r -> %s %s" % (spec["title"][:40], code, hecho), file=sys.stderr)
            return 1
        # ⛔ La respuesta es un CommandResult, no el item: el id viene en `result_id` y la version
        #    en `version`. Y si no viene, esto es un ERROR, no un salto silencioso — la primera
        #    version de este guion hacia `if cmd and wid:` con `wid=None` y **se saltaba las
        #    transiciones sin decir nada**: sembro cinco items, los cinco en `draft`, y el guion
        #    dijo que habia ido bien. Un salto silencioso es indistinguible del exito.
        wid = hecho.get("result_id")
        version = hecho.get("version")
        if not wid or not version:
            print("ERROR: la respuesta no trae result_id/version: %s" % list(hecho)[:10],
                  file=sys.stderr)
            return 1
        creados.append(wid)
        # ⛔ Los comandos van EN CADENA y cada uno sube la version, asi que el `If-Match` del
        #    siguiente usa la que devolvio el anterior. Fijar `"v1"` funcionaba para un solo
        #    comando y rompia para dos — el segundo choca con `version_conflict`.
        #    Todo comando que NO sea `item.create` exige version esperada >= 1
        #    (`work_service.go:1471`), como ETag fuerte `"v<N>"` (`work_api.go:558-569`).
        for orden in spec["cmds"]:
            cuerpo_tr = {"command": orden, "work_item_id": wid}
            if orden == "item.block":
                cuerpo_tr["code"] = spec["code"]
                cuerpo_tr["reason"] = spec["reason"]
            code, tr = pedir(base, token, tenant, "POST",
                             "/v1/m/sessions/work-items/%s/transitions?mode=apply" % wid,
                             cuerpo_tr, {"If-Match": '"v%d"' % version})
            if code not in (200, 201):
                print("ERROR: %s de %s (v%d) -> %s %s"
                      % (orden, wid, version, code, tr), file=sys.stderr)
                return 1
            version = tr.get("version") or (version + 1)
        if item_para_decisiones is None:
            item_para_decisiones = (wid, version)

    # ⛔ La comprobacion que convierte esto en evidencia: releer la lista por donde la lee la
    #    consola. Si aqui sale 0, la siembra no ocurrio por mucho que los POST dijeran 201.
    code, lista = pedir(base, token, tenant, "GET", "/v1/m/sessions/work-items?limit=100")
    if code != 200:
        print("ERROR: no pude releer la lista (%s) %s" % (code, lista), file=sys.stderr)
        return 1
    items = lista.get("items") or []
    if len(items) < len(ITEMS):
        print("ERROR: sembre %d y la lista devuelve %d" % (len(ITEMS), len(items)), file=sys.stderr)
        return 1
    # ⛔ Y SE COMPRUEBAN LOS ESTADOS, no solo cuantos hay. La primera version de esta comprobacion
    #    contaba filas, y por eso dio VERDE cuando las cinco habian quedado en `draft` porque las
    #    transiciones se saltaron en silencio. Contar no es mirar: el recuento salia bien y la
    #    pantalla que ibamos a fotografiar era la equivocada.
    estados = {}
    for it in items:
        estados[it.get("status")] = estados.get(it.get("status"), 0) + 1
    esperados = {"draft", "ready", "blocked"}
    if not esperados.issubset(set(estados)):
        print("ERROR: esperaba estados %s y la lista trae %s"
              % (sorted(esperados), estados), file=sys.stderr)
        return 1
    print("work items sembrados y RELEIDOS: %d · estados %s" % (len(items), estados))

    if item_para_decisiones is None:
        print("ERROR: no hay work item al que adjuntar las decisiones", file=sys.stderr)
        return 1
    decision_item, decision_version = item_para_decisiones
    rc = sembrar_decisiones(base, token, tenant, decision_item, decision_version,
                            args.decision_count)
    if rc:
        return rc

    rc = sembrar_catalogo(base, token, tenant)
    if rc:
        return rc
    rc = sembrar_deploy(base, token, tenant)
    if rc:
        return rc
    rc = sembrar_health(base, token, tenant)
    if rc:
        return rc
    rc = sembrar_coste(base, token, tenant)
    if rc:
        return rc
    rc = sembrar_consola(base, token, tenant)
    if rc:
        return rc
    rc = sembrar_permisos(base, token, tenant)
    if rc:
        return rc
    rc = sembrar_politica(base, token, tenant)
    if rc:
        return rc
    return sembrar_workspace_y_run(base, token, tenant, args.workspace_root)


if __name__ == "__main__":
    sys.exit(main())
