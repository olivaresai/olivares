#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-step-env-closure.sh — refuse a workflow step that CONSUMES a shell variable its own `env:`
# never DEFINES.
#
# ⛔ POR QUÉ EXISTE: dos defectos míos el mismo día, los dos en el workflow de release, y ninguno
# lo habría cogido ninguna prueba del repositorio.
#
#   1. `actionlint` rechazó `PREFLIGHT_RELEASE_REPO: ${{ needs.preflight.outputs... }}` en un job
#      que no declara `needs:`. Retiré la DEFINICIÓN y dejé el CONSUMIDOR. Con `set -euo pipefail`
#      el guard `[ -n "${PREFLIGHT_RELEASE_REPO:-}" ]` disparaba en CADA corrida: la fase 2 del
#      release moría antes de descargar la evidencia, antes de cosign y antes de firmar.
#   2. Veinte minutos después moví un bloque de verificación a un paso que no derivaba el ancla
#      que ese bloque consume. Mismo defecto, misma sesión.
#
# POR QUÉ NO BASTA `actionlint`, que ya corre en `lint:actions`: actionlint valida las EXPRESIONES
# `${{ }}` y caza el nombre inexistente cuando viene de un CONTEXTO (`needs.x`, `inputs.y`). No
# mira el cuerpo del `run:` como shell, así que una `${VAR}` de shell consumida en un paso cuyo
# `env:` no la declara le es invisible. Ésa es exactamente la forma de los dos defectos.
#
# ⛔ LA HEURÍSTICA ES ESTRECHA A PROPÓSITO, y llegué a ella por el camino largo. Mi primera
# versión acusaba TODA `${VAR}` sin `env:` propio: 41 hallazgos, de los que UNO era real. Al
# arrastrar lo exportado a `$GITHUB_ENV` bajó a 33, y seguían siendo falsos —`read -r LOPORT
# HIPORT < …` asigna en el mismo bloque, y mi regex sólo veía `VAR=` a principio de línea—.
# Distinguirlos de verdad exige PARSEAR SHELL, que es exactamente por lo que `actionlint` no lo
# intenta. Un gate que acusa cuarenta inocentes por un culpable no se usa: se desactiva.
#
# La señal que SÍ es precisa, y la que cazaba mis dos defectos: la variable existe como clave
# `env:` en OTRO paso del mismo fichero y no en el suyo. Eso no es «una variable sin declarar»
# —que puede ser shell legítimo— sino la firma de que alguien MOVIÓ o QUITÓ la definición y dejó
# el consumidor. Es la forma exacta de los dos fallos, y no acusa a nadie más.
#
# QUÉ NO HACE, para que su verde no se cite de más: no ve una variable que nunca estuvo declarada
# en ningún paso (eso es shell y no lo juzga), no resuelve `env:` de job ni de workflow, y no
# entiende `source`. Es una red sobre UNA clase, no una prueba de clausura.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="${OLIVARES_WORKFLOWS_DIR:-${ROOT}/.github/workflows}"

[ -d "$DIR" ] || { echo "check-step-env-closure: NO HE PODIDO MIRAR: no existe $DIR" >&2; exit 2; }

python3 - "$DIR" <<'PY'
import os, re, sys

d = sys.argv[1]
ficheros = sorted(f for f in os.listdir(d) if f.endswith((".yml", ".yaml")))
if not ficheros:
    print("check-step-env-closure: NO HE PODIDO MIRAR: %s no tiene workflows" % d, file=sys.stderr)
    raise SystemExit(2)

roto = 0
mirados = 0
for nombre in ficheros:
    L = open(os.path.join(d, nombre), encoding="utf-8").read().split("\n")

    # Toda clave `env:` que aparece EN CUALQUIER SITIO del fichero: workflow, job o paso.
    # Si un nombre esta aqui, alguien lo declaro alguna vez como variable de entorno.
    declaradas = set(re.findall(r'^\s*([A-Z_][A-Z0-9_]{2,}):\s', "\n".join(L), re.M))

    # env: de nivel WORKFLOW (columna 0): legitimo para todo paso del fichero. Perderlo fue mi
    # segundo falso positivo masivo — quince acusados que estaban perfectamente declarados.
    amplio = set()
    for i, l in enumerate(L):
        if re.match(r'^env:\s*$', l):
            j = i + 1
            while j < len(L) and (not L[j].strip() or L[j].startswith("  ")):
                mm = re.match(r'\s+([A-Z_][A-Z0-9_]*):', L[j])
                if mm:
                    amplio.add(mm.group(1))
                j += 1

    jobs = [i for i, l in enumerate(L) if re.match(r'^  [a-z][a-z0-9_-]*:\s*$', l)]
    jobs.append(len(L))
    for ja, jb in zip(jobs, jobs[1:]):
        # lo que un paso anterior de ESTE job dejo en $GITHUB_ENV sigue disponible despues
        exportadas = set()
        # env: de nivel job
        cuerpo_job = "\n".join(L[ja:jb])
        deljob = set()
        m = re.search(r'^    env:\s*$', cuerpo_job, re.M)
        if m:
            tras = cuerpo_job[m.end():].split("\n")
            for l in tras:
                if l.strip() and not l.startswith("      "):
                    break
                mm = re.match(r'\s*([A-Z_][A-Z0-9_]*):', l)
                if mm:
                    deljob.add(mm.group(1))

        pasos = [i for i in range(ja, jb) if re.match(r'^\s*- name:', L[i])]
        pasos.append(jb)
        for a, b in zip(pasos, pasos[1:]):
            cuerpo = "\n".join(L[a:b])
            mirados += 1
            propio = set(re.findall(r'^\s*([A-Z_][A-Z0-9_]*):\s', cuerpo, re.M))
            for var in sorted(set(re.findall(r'\$\{([A-Z_][A-Z0-9_]{2,})[:}]', cuerpo))):
                if var in propio or var in deljob or var in amplio or var in exportadas:
                    continue
                # LA SEÑAL: declarada como env: en otro sitio del fichero, pero no aqui.
                if var not in declaradas:
                    continue
                linea = next((k for k in range(a, b) if "${" + var in L[k]), a) + 1
                paso = next((L[k].strip()[8:60] for k in range(a, b) if L[k].lstrip().startswith("- name:")), "?")
                print("check-step-env-closure: %s:%d: '%s' se consume aqui y su env: esta en OTRO paso — paso: %s"
                      % (nombre, linea, var, paso), file=sys.stderr)
                roto += 1
            if "GITHUB_ENV" in cuerpo:
                for mm in re.finditer(r'([A-Z_][A-Z0-9_]*)=', cuerpo):
                    exportadas.add(mm.group(1))

if roto:
    print("check-step-env-closure: ⛔ %d consumidor(es) cuya definicion vive en otro paso (%d mirados)"
          % (roto, mirados), file=sys.stderr)
    raise SystemExit(1)
print("check-step-env-closure: LIMPIO — %d paso(s) mirados, ninguna definicion extraviada" % mirados)
PY
