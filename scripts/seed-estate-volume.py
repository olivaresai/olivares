#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Llena la finca de demo hasta el objetivo por superficie, para que una captura ENSEÑE algo.

⛔ POR QUE EXISTE, y es una medida, no una intuicion. El 2026-08-30 a las 13:55Z, sobre motor
   virgen y despues de correr la cadena entera de verificacion (--seed-demo -> seed-demo-work.py
   -> verify-seed-payloads.py -> seed-adoption-otlp.py), 20 superficies tenian contenido y solo
   SEIS llegaban a cuatro filas; nueve tenian exactamente UNA. El verificador siembra una fila por
   superficie A PROPOSITO: acredita que el payload entra, no llena la pantalla. Quien lea su
   «18/18 limpios» como «finca lista» corre la campana de capturas para nada.

CONTRATO DE SALIDA (regla 5 del canon), y las tres son distintas:
  0 · todas las superficies DECLARADAS llegan a su objetivo.
  1 · alguna se queda por debajo — se nombra ella y su objetivo.
  2 · NO HE PODIDO MIRAR: sin fichero de objetivos, sin motor, sin permiso, o declaracion y
      generadores no casan. Un 2 jamas se confunde con un 0.

CUATRO INVARIANTES, y cada una tiene mutante en `scripts/test-seed-estate-volume.sh`:
  · IDEMPOTENTE de verdad: la segunda corrida hace CERO POST. No se logra «con cuidado» sino
    contando: se crean `objetivo - filas_que_ya_hay`, y las filas propias llevan un marcador
    derivado y estable, asi que una corrida a medias se completa en vez de duplicar.
  # export-closure: absent-by-design docs/launch/objetivos-sembrado.json — es el catalogo de
  # objetivos de sembrado del LANZAMIENTO, y esta curacion retira `docs/launch` ENTERO
  # (scripts/export-public.sh). Aqui es DATO, no una llamada: en el arbol publicado la ruta
  # simplemente nunca casa, nada la ejecuta y por tanto no hay llamada que guardar. Declararlo
  # hub-only seria la clase equivocada, y retirar el guion COLGO a sus dos llamadores y esos a
  # TRES mas — la cascada probo que lo ausente es el fichero de datos, no el guion.
  · EL OBJETIVO NO VIVE AQUI. Lo lee de `docs/launch/objetivos-sembrado.json`. Quien lo cambie
    edita datos, no codigo — y el rc 1 nombra la superficie y el numero que no alcanzo.
  · CERO VALORES SECRETOS. Lo unico que viaja son LOCALIZADORES (`ref_kind: env` + el nombre de
    la variable). Un guardian mira CADA cuerpo antes de enviarlo y aborta con 2 si huele a valor.
  · SOLO API PUBLICA. Si una superficie no se puede llenar por API, no se cura aqui: se declara
    en `no_sembrables_por_api` con su ruta y su evidencia, y es un hallazgo para BACKEND.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

# ⛔ REDACCION EN LA FRONTERA, Y COMPARTIDA (the reviewer, A-05). `_pide` devolvia el cuerpo HTTP CRUDO
#    y `salir` lo imprimia: un receptor que refleje la cabecera `Authorization` filtraba el token.
#    La cura no es un `replace` en el sitio que fugo —eso ya se probo en el otro guion y dejo dos
#    caminos abiertos— sino que TODA salida pase por una funcion, y que esa funcion sea LA MISMA
#    que cerro el caso de la adopcion. Vive en `scripts/lib/redaccion.py` para que no vuelva a
#    haber dos implementaciones con agujeros distintos.
#    ⛔ Y LA LIBRERIA SE BUSCA, no se asume al lado: este guion se COPIA (los mutantes del banco
#       viven en un temporal), y derivar la ruta solo de `__file__` la rompe para cualquier copia.
#       Se prueban tres sitios y, si no aparece ninguna, el guion NO CORRE: sin redaccion no hay
#       forma de prometer que un token no salga, y arrancar igual seria justo el fallo que esta
#       libreria existe para cerrar. Fail-closed, y con rc 2, que es «no he podido mirar».
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
    print("seed-estate-volume: ⛔ NO HE PODIDO MIRAR: no encuentro `scripts/lib/redaccion.py`. "
          "Sin redaccion de salidas no arranco: un cuerpo HTTP ajeno puede repetir el token.",
          file=sys.stderr)
    sys.exit(2)
sys.path.insert(0, _lib)
from redaccion import Redactor, abre, instala_excepthook  # noqa: E402

redacta = Redactor()

RC_LIMPIO, RC_POR_DEBAJO, RC_NO_PUDE_MIRAR = 0, 1, 2


def di(*partes):
    """La UNICA salida por stdout. Redacta antes de imprimir, siempre."""
    print(redacta(" ".join(str(x) for x in partes)))

OBJETIVOS_POR_DEFECTO = "docs/launch/objetivos-sembrado.json"
PREFIJO_POR_DEFECTO = "demo-vol"

# ── Guardian de secretos ──────────────────────────────────────────────────────────────────────
# Mismo criterio que `verify-seed-payloads.py`, y por la misma razon medida: la primera version de
# aquel comparaba `k.lower()` contra un conjunto que solo tenia `apikey`, asi que `apiToken` pasaba
# limpio. Las claves se normalizan a alfanumericos; las formas se enumeran y se NOMBRAN, porque un
# guardian que no dice que forma le corto no se puede depurar.
CLAVES_DE_VALOR_SECRETO = {
    "password", "passphrase", "secret", "clientsecret", "apikey", "apitoken", "token",
    "authtoken", "accesstoken", "refreshtoken", "bearer", "privatekey", "credential",
    "credentials", "sessionkey", "signingkey", "webhooksecret",
}
FORMAS_DE_CREDENCIAL = [
    ("aws-access-key", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
    ("webhook-secret", re.compile(r"\bwhsec_[A-Za-z0-9+/=_-]{16,}")),
    ("dodo-key", re.compile(r"\bdodo_(?:test|live)_[A-Za-z0-9+/=_-]{16,}")),
    ("stripe-like-key", re.compile(r"\bsk_(?:test_|live_)?[A-Za-z0-9]{20,}")),
    ("openai-like-key", re.compile(r"\bsk-[A-Za-z0-9_-]{20,}")),
    ("github-token", re.compile(r"\bgh[pousr]_[A-Za-z0-9]{20,}")),
    ("jwt", re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}")),
    ("private-key-pem", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")),
]


def _clave_normalizada(k: str) -> str:
    return "".join(c for c in str(k).lower() if c.isalnum())


def huele_a_secreto(nodo, ruta="cuerpo"):
    """Devuelve (motivo, donde) si el cuerpo lleva un VALOR secreto; si no, (None, None).

    ⛔ La excepcion es estrecha y esta nombrada: `secret_refs[].ref` es el NOMBRE de una variable
       de entorno, no su valor, y ese es justamente el patron que queremos ensenar en la captura.
       Se exime la clave `ref` cuando su hermana `ref_kind` dice `env`, y NADA MAS.
    """
    if isinstance(nodo, dict):
        exenta = "ref" if str(nodo.get("ref_kind", "")).lower() == "env" else None
        for k, v in nodo.items():
            if k == exenta:
                continue
            if isinstance(v, str) and _clave_normalizada(k) in CLAVES_DE_VALOR_SECRETO:
                return (f"la clave `{k}` lleva un VALOR, no un localizador", f"{ruta}.{k}")
            m, d = huele_a_secreto(v, f"{ruta}.{k}")
            if m:
                return m, d
    elif isinstance(nodo, list):
        for i, v in enumerate(nodo):
            m, d = huele_a_secreto(v, f"{ruta}[{i}]")
            if m:
                return m, d
    elif isinstance(nodo, str):
        for nombre, rx in FORMAS_DE_CREDENCIAL:
            if rx.search(nodo):
                return (f"el texto casa la forma `{nombre}`", ruta)
    return (None, None)


# ── El motor ──────────────────────────────────────────────────────────────────────────────────
class Motor:
    """Cliente minimo. Toda respuesta que no sepa leer es un 2, nunca un 0."""

    def __init__(self, base, token, tenant):
        self.base = base.rstrip("/")
        self.token = token
        self.tenant = tenant
        # El guion SI conoce su token: declararlo es lo que tapa un cuerpo ajeno que lo refleje,
        # sin depender de que el token tenga forma de nada conocido.
        redacta.recuerda(token, tenant)
        redacta.recuerda_url(base)

    def _pide(self, ruta, cuerpo=None, metodo="GET"):
        # ⛔ EL `Request` SE CONSTRUYE DENTRO DEL `try` (the reviewer, A-05), y es la MISMA clase que ya
        #    pago la adopcion en su v6: fuera, un `Request` sobre una URL malformada lanza
        #    `ValueError: unknown url type: <la URL entera>` que NADIE captura, sale por el
        #    traceback y **evita la frontera** — `di`/`salir` redactan lo que pasa por ellas, y una
        #    excepcion no capturada no pasa por ninguna. La redaccion mas cuidada del mundo no tapa
        #    lo que sale por un camino que no cruza.
        datos = json.dumps(cuerpo).encode() if cuerpo is not None else None
        try:
            pet = urllib.request.Request(self.base + ruta, data=datos, method=metodo)
            pet.add_header("Authorization", "Bearer " + self.token)
            pet.add_header("X-Olivares-Tenant", self.tenant)
            if datos is not None:
                pet.add_header("Content-Type", "application/json")
            # `abre` en vez de `urlopen`: no sigue 30x. Ver la razon medida en la libreria.
            with abre(pet, timeout=60) as r:
                return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()[:300]
        except Exception as e:  # red caida, DNS, TLS: no he podido mirar
            return 0, f"{type(e).__name__}: {e}"[:300]

    def listar(self, ruta, bajo):
        """(filas, None) o (None, motivo). Una lista que no se puede leer NO es una lista vacia."""
        st, cuerpo = self._pide(ruta)
        if st != 200:
            return None, f"GET {ruta} -> {st} {cuerpo[:120]}"
        try:
            d = json.loads(cuerpo)
        except Exception:
            return None, f"GET {ruta} -> 200 con un cuerpo que no es JSON"
        filas = d.get(bajo)
        if not isinstance(filas, list):
            return None, f"GET {ruta} -> 200 pero `{bajo}` no es una lista (claves: {sorted(d)[:6]})"
        return filas, None

    def crear(self, ruta, cuerpo):
        motivo, donde = huele_a_secreto(cuerpo)
        if motivo:
            raise Secreto(f"{ruta}: {motivo} en {donde}")
        st, resp = self._pide(ruta, cuerpo, "POST")
        return st, resp


class Secreto(Exception):
    """Un valor secreto a punto de salir por el cable. Aborta la corrida entera con rc 2."""


# ── Los generadores ───────────────────────────────────────────────────────────────────────────
# Cada superficie declara el CAMPO que lleva su marcador y una fabrica `(marca, i, ctx) -> cuerpo`.
# El conocimiento de los payloads sale de `verify-seed-payloads.py`, que los acredito UNO A UNO
# contra motor vivo (18 declarados / 18 ejercidos). Aqui solo se repiten con variedad.
#
# ⛔ EL ORDEN IMPORTA Y NO ES ESTETICO: `health` y `killswitch` cuelgan de un SUJETO distinto cada
#    una — medido el 2026-08-30, un segundo check sobre el mismo agente da 409 conflict — asi que
#    `agents` va primero y las dos consumen los agentes que aquel dejo.
SUFIJOS = ["invoice auditor", "PR reviewer", "incident triage", "contract reader", "SOC analyst",
           "release notes", "data mapper", "cost watcher", "on-call summarizer", "docs linter",
           "schema migrator", "access reviewer", "log summarizer", "risk scorer"]
ENTORNOS = ["production", "staging", "canary"]
SEVERIDADES = ["high", "critical", "medium"]


def _cuerpo_agents(marca, i, ctx):
    return {"name": f"Claude Code — {SUFIJOS[i % len(SUFIJOS)]}", "kind": "claude_code",
            "external_id": marca}


def _cuerpo_alerting(marca, i, ctx):
    return {"name": marca, "destination": "siem", "min_severity": SEVERIDADES[i % 3]}


def _cuerpo_guardian(marca, i, ctx):
    acciones = ["stop_agent", "quarantine_nhi", "stop_estate"]
    clases = ["prompt_injection", "secret_exfiltration", "tool_abuse"]
    return {"name": marca, "action": acciones[i % 3], "mode": "approval",
            "match_kinds": clases[i % 3], "min_severity": SEVERIDADES[i % 3],
            "note": f"Contain a {clases[i % 3].replace('_', ' ')} finding before it spreads."}


def _cuerpo_routine(marca, i, ctx):
    return {"name": marca, "scope_kind": "tenant", "max_cadence_seconds": 300 * (i + 1),
            "max_active_routines": 5 + i, "require_approval": bool(i % 2)}


def _cuerpo_artifacts(marca, i, ctx):
    # ⛔ Conjunto CERRADO por el motor, y no es el que supuse: `prompt` y `tool` dan 400. Medido
    #    el 2026-08-30: «artifact_class must be skill, mcpb_extension, mcp_app_template or agents_md».
    clases = ["skill", "mcpb_extension", "mcp_app_template", "agents_md"]
    return {"artifact_class": clases[i % 4], "name": marca, "version": f"1.{i}.0"}


def _cuerpo_bindings(marca, i, ctx):
    fuentes = ["mcp.github", "mcp.slack", "mcp.jira", "mcp.postgres", "mcp.s3", "mcp.gdrive",
               "mcp.confluence", "mcp.pagerduty"]
    return {"source_type": "mcp", "source_ref": f"{fuentes[i % len(fuentes)]}#{marca}",
            "scope_tree": "workspace", "scope_ref": ctx["workspace_slug"],
            # `forbid`, no `deny`: medido, «effect must be allow or forbid».
            "effect": "allow" if i % 3 else "forbid", "enabled": True,
            "note": "Reviewer reaches this source under the workspace scope."}


def _cuerpo_capabilities(marca, i, ctx):
    # ⛔ `secret_refs` viaja con un LOCALIZADOR (`ref_kind: env` + el NOMBRE de la variable) y
    #    jamas con un valor. Es justo el patron que la captura tiene que ensenar.
    return {"server_ref": marca, "transport": ["stdio", "http"][i % 2], "enabled": True,
            "secret_refs": [{"name": f"MCP_TOKEN_{i}", "ref_kind": "env",
                             "ref": f"MCP_TOKEN_{i}", "hint": "provisioned by the platform"}],
            "note": "Managed MCP server; the platform injects the credential at start."}


def _cuerpo_redteam(marca, i, ctx):
    # Un objetivo por AGENTE: el motor da 409 «a target for this agent_ref already exists».
    return {"agent_ref": ctx["libres"]["redteam"].pop(0), "name": marca}


def _cuerpo_knowledge(marca, i, ctx):
    clases = ["internal", "confidential", "public"]
    return {"name": marca, "classification": clases[i % 3], "residency_region": "global",
            "embed_policy": "auto"}


def _cuerpo_deploy(marca, i, ctx):
    # ⛔ `target` TIENE UN TOPE DE 39 CARACTERES y el motor lo rechaza diciendo otra cosa: «target
    #    and source_ref must not contain a credential». Medido el 2026-08-30 con frontera exacta
    #    (39 pasa, 40 corta) y con control de forma: un segmento unico de 39 pasa, y 17 segmentos
    #    de dos caracteres que suman 56 cortan. No es una heuristica de credenciales, es longitud.
    destino = f"k8s://{ENTORNOS[i % 3][:4]}-euw1/agents"
    assert len(destino) < 40, destino
    return {"subject_kind": "agent", "subject_ref": ctx["agentes"][i % len(ctx["agentes"])],
            "name": marca, "environment": ENTORNOS[i % 3], "target": destino,
            "spec": {"image": f"ghcr.io/acme/agent:{1 + i}.0", "replicas": 1 + (i % 3)}}


def _cuerpo_approvals(marca, i, ctx):
    acciones = ["deploy.promote", "policy.publish", "killswitch.release"]
    return {"subject_kind": "deployment", "subject_ref": marca, "action": acciones[i % 3],
            "reason": "Promote a governed path; needs a second pair of eyes."}


def _cuerpo_health(marca, i, ctx):
    # Un check por SUJETO: el motor da 409 sobre un agente que ya tiene uno.
    return {"name": marca, "subject_kind": "agent", "subject_ref": ctx["libres"]["health"].pop(0),
            "expected_interval_seconds": 900 * (1 + i % 4)}


# ⛔ LA RAZON VARIA POR INDICE, Y NO ES ADORNO. Aqui habia UNA cadena constante, asi que
#    /security salia con SEIS filas «Kill switch ENGAGED» identicas — palabra por palabra.
#    Una captura asi no ensena que el producto registre POR QUE se corto cada agente: ensena
#    un bucle. Lo marco the planner revisando las 142 el 2026-08-31.
#    Son razones de gobierno plausibles y distintas entre si; el numero de sujetos lo pone el
#    llamador, y la lista cicla si pide mas de las que hay.
_RAZONES_KILLSWITCH = [
    "Suspected prompt-injection via an untrusted PR body.",
    "Egress to an unapproved model endpoint during a governed session.",
    "Budget ceiling exceeded three times inside the rolling window.",
    "Wrote to a resource outside its declared read/write map.",
    "Requirements file replaced while the session was live.",
    "MCP server added at runtime with no admission record.",
]


def _cuerpo_killswitch(marca, i, ctx):
    return {"scope_kind": "agent", "scope_ref": ctx["libres"]["killswitch"].pop(0),
            "reason": _RAZONES_KILLSWITCH[i % len(_RAZONES_KILLSWITCH)]}


# ⛔ EL CUARTO CAMPO ES EL MODO DEL MARCADOR, Y EXISTE POR UN DEFECTO QUE ME ENCONTRE YO
#    releyendo esto. La version anterior guardaba solo `(campo, ruta, fabrica)` y el bucle
#    preguntaba `if marca in usadas`, con `usadas` construida por IGUALDAD EXACTA sobre ese campo.
#    Medido: esa comprobacion estaba MUERTA en 2 de 13 superficies —`console-bindings` guarda
#    `mcp.github#<marca>` y `killswitch` guarda el ID DE UN AGENTE en `scope_ref`—, asi que en
#    ellas nunca podia reconocer una fila propia.
#
#    Y lo mas util del hallazgo es como se me escapo la primera vez: mi sonda pregunto si la marca
#    estaba CONTENIDA en el valor y el guion pregunta si es IGUAL. Dos predicados distintos, un
#    falso verde. Por eso el modo se DECLARA y una guarda lo comprueba: `igual` la marca es el
#    valor entero; `contiene` va dentro de un valor mas largo; `sujeto` la fila no lleva marca
#    porque su unicidad la da el sujeto, y ahi la idempotencia es la lista de sujetos libres.
GENERADORES = {
    "agents":           ("external_id", "/v1/agents", _cuerpo_agents, "igual"),
    "alerting":         ("name", "/v1/m/notify/routes", _cuerpo_alerting, "igual"),
    "guardian-rules":   ("name", "/v1/m/governance/guardian/rules", _cuerpo_guardian, "igual"),
    "routine-policies": ("name", "/v1/m/governance/routine-policies", _cuerpo_routine, "igual"),
    "agent-artifacts":  ("name", "/v1/m/models/agent-artifacts", _cuerpo_artifacts, "igual"),
    "console-bindings": ("source_ref", "/v1/m/sourcescope/bindings", _cuerpo_bindings, "contiene"),
    "capabilities":     ("server_ref", "/v1/m/capabilities/configs", _cuerpo_capabilities, "igual"),
    "redteam":          ("name", "/v1/m/redteam/targets", _cuerpo_redteam, "igual"),
    "knowledge":        ("name", "/v1/m/knowledge/kbs", _cuerpo_knowledge, "igual"),
    "deploy":           ("name", "/v1/m/deploy/definitions", _cuerpo_deploy, "igual"),
    "approvals":        ("subject_ref", "/v1/m/governance/approvals", _cuerpo_approvals, "igual"),
    "health":           ("name", "/v1/m/health/checks", _cuerpo_health, "igual"),
    "killswitch":       ("scope_ref", "/v1/m/governance/killswitch", _cuerpo_killswitch, "sujeto"),
}
MODOS = ("igual", "contiene", "sujeto")


def marca_reconocible(modo, marca, valores):
    """¿Reconoce el guion una fila PROPIA entre `valores`? Un modo desconocido dice que NO."""
    if modo == "igual":
        return marca in valores
    if modo == "contiene":
        return any(marca in v for v in valores)
    return False  # `sujeto`: la unicidad la da el sujeto, no la marca


def comprueba_marcadores():
    """Guarda estructural: cada generador tiene que cumplir el modo que DECLARA.

    Es lo que impide que el defecto vuelva. Un cambio en un generador que deje de poner la marca
    en su campo —o que la envuelva— rompe aqui, con nombre, en vez de dejar muerta una comprobacion
    de idempotencia que nadie mira.
    """
    testigo = "MARCA-TESTIGO"
    ctx = {"workspace_slug": "ws", "agentes": [f"ag-{i}" for i in range(40)],
           "libres": {k: [f"ag-{i}" for i in range(40)] for k in SUJETO_UNICO}}
    malas = []
    for sid in ORDEN:
        campo, _ruta, fabrica, modo = GENERADORES[sid]
        if modo not in MODOS:
            malas.append(f"`{sid}` declara un modo desconocido: {modo!r}")
            continue
        valor = str(fabrica(testigo, 0, ctx).get(campo, "<AUSENTE>"))
        if modo == "igual" and valor != testigo:
            malas.append(f"`{sid}` declara `igual` y su campo `{campo}` guarda {valor[:40]!r}")
        elif modo == "contiene" and testigo not in valor:
            malas.append(f"`{sid}` declara `contiene` y su campo `{campo}` guarda {valor[:40]!r}")
        elif modo == "sujeto" and testigo in valor:
            malas.append(f"`{sid}` declara `sujeto` pero SI lleva la marca en `{campo}`")
    return malas
# El orden de siembra: los que fabrican sujetos, antes que los que los consumen.
ORDEN = ["agents", "alerting", "guardian-rules", "routine-policies", "agent-artifacts",
         "console-bindings", "capabilities", "knowledge", "approvals", "deploy", "redteam",
         "health", "killswitch"]

# ⛔ TRES SUPERFICIES CUELGAN DE UN SUJETO UNICO, y el motor lo impone con 409: una sola fila por
#    agente. No es una peculiaridad de una, son tres y con el campo distinto cada una, asi que se
#    declara aqui en vez de repetirse en el bucle. Se descubrio por una corrida REAL: `redteam`
#    se quedaba en 1 de 6 creando CERO y sin decir por que, porque el bucle trataba el 409 como
#    «ya estaba» y seguia girando. Un 409 repetido no es exito: es un sujeto agotado.
SUJETO_UNICO = {"health": "subject_ref", "killswitch": "scope_ref", "redteam": "agent_ref"}


def salir(rc, msg):
    etiqueta = "⛔ NO HE PODIDO MIRAR" if rc == RC_NO_PUDE_MIRAR else "POR DEBAJO DEL OBJETIVO"
    print(redacta(f"seed-estate-volume: {etiqueta}: {msg}"), file=sys.stderr)
    return rc


def carga_objetivos(ruta):
    """(declaradas, no_sembrables, None) o (None, None, motivo). Fail-CLOSED en las dos direcciones."""
    try:
        with open(ruta, encoding="utf-8") as f:
            d = json.load(f)
    except FileNotFoundError:
        return None, None, f"no encuentro el fichero de objetivos `{ruta}`"
    except json.JSONDecodeError as e:
        return None, None, f"`{ruta}` no es JSON valido: {e}"
    sup = d.get("superficies")
    if not isinstance(sup, list) or not sup:
        return None, None, f"`{ruta}` no trae una lista `superficies` con contenido"
    decl = {}
    for s in sup:
        for campo in ("id", "objetivo", "listar", "bajo"):
            if campo not in s:
                return None, None, f"una superficie de `{ruta}` no declara `{campo}`: {s}"
        if not isinstance(s["objetivo"], int) or s["objetivo"] < 1:
            return None, None, f"`{s['id']}` declara un objetivo que no es un entero positivo"
        decl[s["id"]] = s
    # ⛔ LAS DOS DIRECCIONES, y la segunda es la que un mutante rompe sin que se note: una
    #    superficie con generador y SIN declarar se sembraria sin objetivo y sin aparecer en el
    #    reparto — es decir, trabajo invisible que nadie podria auditar.
    faltan_gen = sorted(set(decl) - set(GENERADORES))
    if faltan_gen:
        return None, None, f"declaradas en `{ruta}` y SIN generador en el guion: {faltan_gen}"
    faltan_decl = sorted(set(GENERADORES) - set(decl))
    if faltan_decl:
        return None, None, f"con generador en el guion y SIN declarar en `{ruta}`: {faltan_decl}"
    return decl, d.get("no_sembrables_por_api", []), None


def main(argv):
    # ⛔ La frontera cubre TAMBIEN lo que salga por una excepcion no capturada. Se instala aqui y no
    #    en el import: un banco que importe este guion como modulo no debe heredar el hook.
    instala_excepthook(redacta, "seed-estate-volume")
    p = argparse.ArgumentParser(description="Llena la finca de demo hasta el objetivo por superficie.")
    p.add_argument("base"); p.add_argument("token"); p.add_argument("tenant")
    p.add_argument("--workspace", default="billing")
    p.add_argument("--objetivos", default=OBJETIVOS_POR_DEFECTO)
    p.add_argument("--prefijo", default=PREFIJO_POR_DEFECTO)
    p.add_argument("--solo-medir", action="store_true",
                   help="cuenta y reparte SIN enviar un solo POST (cero efectos)")
    a = p.parse_args(argv[1:])

    decl, no_sembrables, motivo = carga_objetivos(a.objetivos)
    if motivo:
        return salir(RC_NO_PUDE_MIRAR, motivo)
    malas = comprueba_marcadores()
    if malas:
        return salir(RC_NO_PUDE_MIRAR, "un generador no cumple el modo de marcador que declara, "
                                       "asi que su comprobacion de idempotencia estaria muerta: "
                                       + "; ".join(malas))
    di(f"seed-estate-volume: objetivos de `{a.objetivos}` · {len(decl)} superficies "
          f"declaradas · {len(no_sembrables)} no sembrables por API")

    m = Motor(a.base, a.token, a.tenant)
    ctx = {"workspace_slug": a.workspace, "agentes": [], "libres": {}}

    filas, mal = m.listar("/v1/agents", "items")
    if mal:
        return salir(RC_NO_PUDE_MIRAR, f"no puedo listar los agentes, que son la raiz de otras cuatro "
                                       f"superficies: {mal}")

    reparto, creados_total, fallos = [], 0, []
    # ⛔ SE ITERA SOBRE LO DECLARADO, no sobre ORDEN a secas, y esto es la cura de A-04
    #    (the reviewer sobre `fc3394154`). Antes, `decl[sid]` con una superficie sin declarar reventaba
    #    en `KeyError` — y el banco daba por bueno a su mutante porque aceptaba CUALQUIER rc≠2. Un
    #    mutante acreditado por el rc de un crash no acredita la guarda: acredita que el guion se
    #    rompe. Saltando en silencio, retirar la guarda produce el defecto DE VERDAD —la superficie
    #    se siembra fuera del reparto o no se siembra— y eso si se puede exigir por su diagnostico.
    for sid in [x for x in ORDEN if x in decl]:
        s = decl[sid]
        campo, ruta, fabrica, modo = GENERADORES[sid]
        filas, mal = m.listar(s["listar"], s["bajo"])
        if mal:
            return salir(RC_NO_PUDE_MIRAR, f"`{sid}`: {mal}")
        antes = len(filas)
        objetivo = s["objetivo"]
        faltan = max(0, objetivo - antes)

        ags, mal_ag = m.listar("/v1/agents", "items")
        if mal_ag:
            return salir(RC_NO_PUDE_MIRAR, f"`{sid}` cuelga de los agentes y no puedo listarlos: {mal_ag}")
        ctx["agentes"] = [x["id"] for x in ags]
        if sid in SUJETO_UNICO:
            campo_sujeto = SUJETO_UNICO[sid]
            usados = {f.get(campo_sujeto) for f in filas}
            libres = [x for x in ctx["agentes"] if x not in usados]
            ctx["libres"][sid] = list(libres)
            if faltan > len(libres):
                fallos.append(f"`{sid}` cuelga de un sujeto UNICO (`{campo_sujeto}`): objetivo "
                              f"{objetivo}, hay {antes} y solo quedan {len(libres)} agentes sin "
                              f"una; para subirlo hay que sembrar mas agentes primero")
                faltan = len(libres)
        elif sid == "deploy" and not ctx["agentes"]:
            return salir(RC_NO_PUDE_MIRAR, "`deploy` cuelga de un agente y no hay ninguno")

        creados, conflictos, ultimo = 0, 0, ""
        if not a.solo_medir:
            usadas = {str(f.get(campo, "")) for f in filas}
            i = 0
            while creados < faltan and i < faltan + objetivo + 50:
                marca = f"{a.prefijo}-{sid}-{i:02d}"
                i += 1
                if marca_reconocible(modo, marca, usadas):
                    continue  # ya la sembre en una corrida anterior: se completa, no se duplica
                try:
                    st, resp = m.crear(ruta, fabrica(marca, creados, ctx))
                except Secreto as e:
                    return salir(RC_NO_PUDE_MIRAR, f"guardian de secretos: {e}")
                except IndexError:
                    fallos.append(f"`{sid}`: me quede sin sujetos libres a la {creados + 1}a fila")
                    break
                if st in (200, 201):
                    creados += 1
                elif st == 409:
                    # ⛔ NO se traga: se cuenta y se NOMBRA si al final falta gente. La primera
                    #    version hacia `continue` a secas y una superficie podia quedarse en 1 de
                    #    6 creando cero SIN UNA SOLA LINEA que dijera por que.
                    conflictos += 1
                    ultimo = resp[:110]
                    continue
                else:
                    fallos.append(f"`{sid}`: POST {ruta} -> {st} {resp[:110]}")
                    break
            if creados < faltan and conflictos and not any(f.startswith(f"`{sid}`") for f in fallos):
                fallos.append(f"`{sid}`: {conflictos} conflictos 409 y solo {creados} de {faltan} "
                              f"filas creadas — el servidor rechaza duplicados: {ultimo}")

        despues_filas, mal = m.listar(s["listar"], s["bajo"])
        if mal:
            return salir(RC_NO_PUDE_MIRAR, f"`{sid}`: no puedo releer tras sembrar: {mal}")
        despues = len(despues_filas)
        creados_total += creados
        reparto.append((sid, antes, creados, despues, objetivo))

    di()
    di(f"  {'SUPERFICIE':18s} {'ANTES':>6s} {'CREADAS':>8s} {'DESPUES':>8s} {'OBJETIVO':>9s}  VEREDICTO")
    debajo = []
    for sid, antes, creados, despues, objetivo in reparto:
        v = "ok" if despues >= objetivo else "⛔ POR DEBAJO"
        if despues < objetivo:
            debajo.append((sid, despues, objetivo))
        di(f"  {sid:18s} {antes:6d} {creados:8d} {despues:8d} {objetivo:9d}  {v}")
    di(f"\n  {creados_total} filas creadas en esta corrida"
       + ("  (modo --solo-medir: cero POST)" if a.solo_medir else ""))
    for f in fallos:
        di(f"  ⚠ {f}")
    for ns in no_sembrables:
        di(f"  ⓘ `{ns.get('id')}` NO se siembra por API ({ns.get('ruta')}): {ns.get('evidencia', '')[:100]}")

    if debajo:
        return salir(RC_POR_DEBAJO, "; ".join(f"`{s}` se queda en {d} de {o}" for s, d, o in debajo))
    di("  ⇒ todas las superficies declaradas llegan a su objetivo.")
    return RC_LIMPIO


if __name__ == "__main__":
    sys.exit(main(sys.argv))
