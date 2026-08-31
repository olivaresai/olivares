#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# race-full-report.sh <repo> <run_id> — la tabla que rellena el acta y que decide si los
# grupos de race-full se reequilibran.
#
# ⛔ LAS CUATRO REGLAS DEL VEREDICTO, escritas aqui porque son la razon de que este guion
# exista y no un `gh api | jq` a mano:
#
#   1. SE LEE POR PASOS, NUNCA POR `conclusion` DEL JOB. Un job `success` puede llevar pasos
#      `skipped`, y un rojo temprano deja SALTADO todo lo que va detras — que no es «fallo»,
#      es «no se midio». Los saltados se cuentan y se nombran.
#   2. `cancelled` ES MUERTE POR TIEMPO, no ausencia de medida. Es lo que le paso a race-root
#      en una corrida alojada del 2026-08-31: el paso salio `cancelled` a los 85 min y el job `failure`.
#      Leerlo como «no concluyente» convierte un techo agotado en un misterio.
#   3. LA DURACION ES LA DEL PASO, no la del job. El job incluye checkout, setup-go, Postgres
#      y provisioning; cargarselos al grupo infla su coste y llevaria a partir el grupo
#      equivocado.
#   4. LOS TECHOS SALEN DE `scripts/race-groups.json`, no se teclean aqui. Un techo escrito
#      dos veces deriva, y este informe existe para decidir con numeros.
#
# Una duracion sin su techo no dice nada, asi que cada fila lleva duracion, techo, margen y
# el % del techo consumido.
#
# Seams de prueba (el banco los usa; en produccion no se definen):
#   OLIVARES_RACE_REPORT_JOBS=<fichero>   el JSON de /actions/runs/<id>/jobs, sin API
#   OLIVARES_RACE_REPORT_JSONL=<fichero>  el race-root.jsonl, sin descargar el artefacto
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
SPEC="${OLIVARES_RACE_GROUPS:-scripts/race-groups.json}"
fail()   { printf 'race-full-report: FAIL — %s\n' "$*" >&2; exit 1; }
cannot() { printf 'race-full-report: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

REPO="${1:-}"; RUN="${2:-}"
JOBS_SRC="${OLIVARES_RACE_REPORT_JOBS:-}"
if [ -z "${JOBS_SRC}" ]; then
  [ -n "${REPO}" ] && [ -n "${RUN}" ] || cannot "uso: $0 <owner/repo> <run_id>"
  command -v gh >/dev/null 2>&1 || cannot "no hay gh y no se ha dado OLIVARES_RACE_REPORT_JOBS"
  JOBS_SRC="$(mktemp "${TMPDIR:-/tmp}/racereport.XXXXXX")"
  trap 'rm -f "${JOBS_SRC}"' EXIT
  gh api "repos/${REPO}/actions/runs/${RUN}/jobs?per_page=100" > "${JOBS_SRC}" \
    || cannot "la API no devolvio los jobs de ${REPO} run ${RUN}"
fi
[ -s "${JOBS_SRC}" ] || cannot "el JSON de jobs esta vacio: ${JOBS_SRC}"
[ -f "${SPEC}" ] || cannot "no encuentro ${SPEC} (de ahi salen los techos)"

# El .jsonl de race-root: si no se da por seam, se intenta el artefacto. Que NO este no es
# un error — el paso puede haber muerto antes de subirlo.
JSONL="${OLIVARES_RACE_REPORT_JSONL:-}"
if [ -z "${JSONL}" ] && [ -n "${REPO}" ] && [ -n "${RUN}" ] && command -v gh >/dev/null 2>&1; then
  d="$(mktemp -d "${TMPDIR:-/tmp}/racertim.XXXXXX")"
  if gh run download "${RUN}" --repo "${REPO}" --name race-root-timings --dir "$d" >/dev/null 2>&1; then
    JSONL="$(find "$d" -name '*.jsonl' -print -quit 2>/dev/null || true)"
  fi
fi

python3 - "${JOBS_SRC}" "${SPEC}" "${JSONL}" <<'PY'
import json, sys, datetime, re

jobs = json.load(open(sys.argv[1], encoding="utf-8")).get("jobs", [])
spec = json.load(open(sys.argv[2], encoding="utf-8"))
jsonl = sys.argv[3] if len(sys.argv) > 3 else ""

if not jobs:
    print("race-full-report: COULD NOT LOOK — la corrida no trae ni un job", file=sys.stderr)
    sys.exit(2)

def when(s):
    if not s:
        return None
    return datetime.datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ")

def mins(a, b):
    a, b = when(a), when(b)
    if a is None or b is None:
        return None
    return (b - a).total_seconds() / 60.0

# Regla 4: los techos salen de la spec.
CEIL_G, TO_G = spec["step_ceiling_minutes"], spec["go_timeout_minutes"]
# ⛔ DOS TECHOS DISTINTOS PARA LA RAIZ, y confundirlos inventa rojos: el race-root de HOY
# es UN job con techo 152/-timeout 130; los turnos del reparto por -run tendran 60/45 CADA
# UNO. La corrida de hoy se mide contra el de hoy.
CEIL_R, TO_R = spec["root_single_step_ceiling_minutes"], spec["root_single_go_timeout_minutes"]
CEIL_RS, TO_RS = spec["root_step_ceiling_minutes"], spec["root_go_timeout_minutes"]

# ⛔ «race this root shard» FALTABA, y el fallo fue mio: escribi la matriz de turnos de la
# raiz y no anadi su paso aqui, asi que el informe leia los SIETE turnos como «COULD NOT
# LOOK: sin paso de medida» — un instrumento ciego justo al cambio que introdujo. Se ve en
# la corrida 33396907337: siete filas sin dato y los grupos con el suyo.
MEASURING = ("race this root shard",
             "race this group",
             "race every workspace module except the cmd/olivares root package",
             "race the full cmd/olivares root suite")

def measuring_step(job):
    for s in job.get("steps", []):
        for m in MEASURING:
            if s.get("name", "").startswith(m):
                return s
    return None

# Regla 2: cancelled es muerte por tiempo.
def verdict(step, dur, ceil):
    c = (step or {}).get("conclusion")
    if c == "success":
        return "verde", False
    if c == "cancelled":
        # ⛔ MATIZ MEDIDO, y corrige la regla tal y como me llego: «cancelled = muerte por
        # tiempo» vale cuando el paso llego a su techo, pero en una corrida alojada del 2026-08-31
        # race-root salio `cancelled` a los 85,8 min de un techo de 152 — el 56 %. Ahi no
        # se agoto nada: lo cancelo algo de FUERA (el otro job murio 10 min antes y la
        # corrida se vino abajo). Llamarlo «muerte por tiempo» mandaria a subir un techo
        # que no era el problema.
        #
        # Lo que NO cambia, que es el fondo de la regla: cancelled NUNCA es verde y NUNCA
        # es una medida.
        if dur is not None and dur >= ceil - 1:
            return "MUERTO POR TIEMPO (cancelled en su techo)", True
        return "CANCELADO DESDE FUERA (no midio; %.0f%% del techo)" % (
            100.0 * dur / ceil if dur is not None and ceil else 0), True
    if c == "failure":
        agotado = dur is not None and dur >= ceil - 1
        return ("MUERTO POR TIEMPO (techo del paso)" if agotado else "ROJO"), True
    if c in ("skipped", None):
        return "NO MEDIDO", True
    return "desconocido: %s" % c, True

rows, bad, notmeasured, encurso = [], 0, 0, 0
for j in sorted(jobs, key=lambda x: x.get("name", "")):
    name = j.get("name", "")
    m = re.match(r"^race-workspace \((.+)\)$", name)
    if m:
        etiqueta, ceil, to = m.group(1), CEIL_G, TO_G
    elif re.match(r"^race-root \((.+)\)$", name):
        etiqueta, ceil, to = name, CEIL_RS, TO_RS
    elif name == "race-workspace":
        # ⛔ SU TECHO NO ESTA EN LA SPEC DE HOY: era 75 en la epoca del paso unico. Comparar
        # contra el de la matriz daria un «margen» inventado, asi que no se compara: el
        # veredicto sale del paso y el techo se imprime como desconocido.
        etiqueta, ceil, to = "workspace (paso unico, pre-matriz)", None, None
    elif name == "race-root":
        etiqueta, ceil, to = "race-root", CEIL_R, TO_R
    else:
        continue
    # ⛔ UN JOB QUE NO HA TERMINADO NO ES UN JOB QUE NO MIDIO. Quien vigile el acto va a
    # correr esto A MITAD de la corrida, y leer «NO MEDIDO» en un grupo que sigue vivo es
    # la lectura que hace parar un acto que iba bien. Se dice EN CURSO y no cuenta como
    # muerte — pero tampoco como verde: la corrida no tiene veredicto todavia.
    if j.get("status") != "completed":
        rows.append((etiqueta, None, ceil, to, "EN CURSO (%s)" % j.get("status"), 0))
        encurso += 1
        continue
    st = measuring_step(j)
    if st is None:
        rows.append((etiqueta, None, ceil, to, "COULD NOT LOOK: sin paso de medida", 0))
        notmeasured += 1
        bad += 1
        continue
    # Regla 3: la duracion es la del PASO.
    dur = mins(st.get("started_at"), st.get("completed_at"))
    v, malo = verdict(st, dur, ceil if ceil is not None else 10**9)
    # Regla 1: los saltados DETRAS de una muerte se cuentan; no son «pasados».
    #
    # ⛔ Y «detras de una muerte» es la mitad que faltaba, medida sobre la corrida
    # alojada: seis de los siete grupos llevan `task test:cloud` SALTADO porque su `if:`
    # sólo casa en uno — es un condicional deliberado, no una baja. Contarlo ponía «+1 paso
    # sin medir» en filas verdes y ensuciaba justo la tabla que se lee para decidir.
    saltados = 0
    if malo:
        visto = False
        for s in j.get("steps", []):
            if s is st:
                visto = True
                continue
            if visto and s.get("conclusion") == "skipped" and not s.get("name", "").startswith("Post "):
                saltados += 1
    rows.append((etiqueta, dur, ceil, to, v, saltados))
    if malo:
        bad += 1
    notmeasured += saltados

print("grupo                          | dur    | techo | margen | %techo | veredicto")
print("-------------------------------+--------+-------+--------+--------+------------------------------")
for etiqueta, dur, ceil, to, v, salt in rows:
    if dur is None or ceil is None:
        d_ = "  —" if dur is None else "%5.1fm" % dur
        c_ = "  —" if ceil is None else "%5d" % ceil
        nota = "" if ceil is not None else "  (techo de esa epoca no esta en la spec de hoy)"
        print("%-30s | %-6s | %5s | %-6s | %-6s | %s%s" % (etiqueta[:30], d_, c_, "  —", "  —", v, nota))
        continue
    marg = ceil - dur
    pct = 100.0 * dur / ceil
    extra = "" if salt == 0 else "  (+%d paso(s) SALTADO(s) = no medidos)" % salt
    print("%-30s | %5.1fm | %5d | %5.1fm | %5.1f%% | %s%s" % (etiqueta[:30], dur, ceil, marg, pct, v, extra))

print()
print("techos leidos de %s: grupos paso %dm / go %dm · raiz paso %dm / go %dm"
      % (sys.argv[2], CEIL_G, TO_G, CEIL_R, TO_R))

# ── race-root: lo que el .jsonl dice y ninguna otra fuente ────────────────────
if jsonl:
    try:
        ev = [json.loads(l) for l in open(jsonl, encoding="utf-8") if l.strip()]
    except Exception as e:                                    # noqa: BLE001
        ev = []
        print("aviso: no pude leer %s (%s)" % (jsonl, e))
    if ev:
        # ⛔ EL PRIMER EVENTO ES LA CIFRA QUE FALTABA PARA LA CUENTA DEL RELOJ: el
        # `-timeout` de Go arranca DESPUES de compilar con -race, asi que el hueco entre
        # el inicio del paso y el primer test que corre ES la compilacion. Sin esto, el
        # margen del techo se elige a ojo.
        raiz = [r for r in rows if r[0] == "race-root"]
        t0 = None
        for j in jobs:
            if j.get("name") == "race-root":
                st = measuring_step(j)
                t0 = when((st or {}).get("started_at"))
        # ⛔ Y la marca se normaliza con cuidado: `go test -json` emite
        # `2026-08-31T10:22:00.123456789Z`, pero tambien la emite SIN fraccion. Partir por
        # el punto y anadir «Z» a secas producia `...00ZZ` en el segundo caso y el parseo
        # fallaba en silencio, dejando la cifra de compilacion fuera del informe — que es
        # justo el numero por el que existe este bloque.
        raw = ev[0].get("Time", "")
        norm = re.sub(r"\.[0-9]+Z?$", "", raw).rstrip("Z")
        first = when(norm + "Z") if norm else None
        if t0 and first:
            print("race-root: primer evento del test a los %.1f min del inicio del paso "
                  "⇒ COMPILACION con -race ~%.1f min (es el sumando que faltaba en "
                  "techo = -timeout + compilacion + margen)" % ((first - t0).total_seconds() / 60.0,
                                                                (first - t0).total_seconds() / 60.0))
        lentos = sorted((e for e in ev if e.get("Action") in ("pass", "fail") and e.get("Test")
                         and e.get("Elapsed") is not None),
                        key=lambda e: -e["Elapsed"])[:10]
        if lentos:
            print("race-root: los 10 tests mas lentos (para el reparto por -run):")
            for e in lentos:
                print("    %8.1fs  %s" % (e["Elapsed"], e["Test"]))
    else:
        print("race-root: el .jsonl no trae eventos legibles — no se infiere nada de el")
else:
    print("race-root: sin .jsonl (artefacto ausente o paso muerto antes de subirlo). "
          "NO se deduce la compilacion de ningun otro sitio.")

print()
# Una muerte es decisiva aunque queden jobs vivos: no hace falta esperar para saber que esa
# corrida ya no sirve para la puerta.
if bad:
    print("race-full-report: %d job(s) NO verdes; %d paso(s) sin medir%s. La corrida NO es "
          "evidencia de nada para la puerta del release."
          % (bad, notmeasured, "; %d aun EN CURSO" % encurso if encurso else ""), file=sys.stderr)
    sys.exit(1)
if encurso:
    print("race-full-report: SIN VEREDICTO TODAVIA — %d de %d job(s) siguen EN CURSO. Los "
          "terminados van verdes. No es un verde de corrida: vuelve a leerlo al acabar."
          % (encurso, len(rows)), file=sys.stderr)
    sys.exit(2)
print("race-full-report: CLEAN — %d job(s), todos verdes por PASOS, 0 pasos sin medir." % len(rows))
PY
