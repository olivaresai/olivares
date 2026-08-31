#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-ci-inner-timeouts.sh — un reloj INTERIOR que no puede disparar antes que el techo del paso
# es una rama fail-closed INALCANZABLE POR CONSTRUCCIÓN.
#
# ⛔ VÍCTIMA MEDIDA, y por eso existe: el 2026-08-30 el job `secrets` del candidato 3 (corrida
# 33299071121) salió ROJO porque GitHub mató el paso `gitleaks` a los **60 min** de su
# `timeout-minutes`, ANTES de que su reloj interior —`timeout --signal=TERM --kill-after=60s 80m`—
# pudiera devolver 124/137 y el guion decir «2 · NO PUDE MIRAR: el barrido no cabe». El guion tiene
# su rama honesta escrita y **nadie puede llegar a ella**: 60 < 80 + 1.
#
# `lint:ci-timeout-arithmetic` compara el techo del JOB con la SUMA de sus pasos. No mira DENTRO de
# un `run:`, así que esta clase le es invisible — dos gates, dos preguntas.
#
# DOS FORMAS, y la segunda es la mayoritaria en este árbol:
#   · `timeout [banderas] <dur> …`     — el reloj de coreutils (con su `--kill-after`)
#   · `-timeout <dur>` / `--timeout=<dur>` — el reloj INTERNO de otro comando (`go test`, `kubectl`)
# Las dos prometen un diagnóstico propio —un panic con su stack, un «context deadline exceeded»—
# que sólo llega si al proceso le da tiempo a producirlo.
#
# EL TECHO EFECTIVO es el del paso, o el del job si el paso no lo fija, o 360 (el de GitHub).
# La exigencia: techo ≥ interior + kill-after + margen (`OLIVARES_INNER_MARGEN`, 1 min por defecto).
#
# ⛔ LÍMITES DECLARADOS, MEDIDOS FORMA POR FORMA el 2026-08-30 con un fixture cada una — porque un
# gate que promete más de lo que mide es peor que no tenerlo:
#   VE  `-timeout=45m` (con `=`)          · VE  `GOFLAGS=-timeout=45m go test …`
#   VE  `timeout 7200 …` (sin unidad son SEGUNDOS: 7200 s = 120 min)
#   VE  banderas con valor separado (`timeout --signal TERM 10m`)
#   VE  el reloj que vive DENTRO de una tarea que el paso invoca (`task test:race-hot:modules`):
#       se sigue `task <nombre>` hasta el Taskfile y por sus `deps:` y `cmds: - task:`, en UNA
#       pasada ciclo-segura, y el hallazgo NOMBRA la tarea donde vive el reloj.
#
# ⛔ ESTE BLOQUE DECIA LO CONTRARIO HASTA EL 2026-08-30, y se corrige entero en vez de matizarse:
# afirmaba «NO VE el reloj que vive dentro de una tarea» y que resolverlo «es OTRO gate». Las dos
# frases eran ciertas cuando se escribieron y **falsas desde que el seguidor existe** — y una
# cabecera que promete MENOS de lo que el codigo hace es tan mentirosa como una que promete mas:
# quien la lea creera que tiene que escribir el gate que ya esta debajo.
#
# La medida que justificaba el punto ciego se conserva porque explica POR QUE se cerro: **11 de 738**
# tareas llevan reloj interior y **71 de 282** pasos con `run:` delegan en `task`. Con el seguidor,
# los relojes juzgados pasan de **9 a 19**. El caso que lo forzo: `race-modules` en el candidato 3
# murio con `panic: test timed out after 2h30m0s` — su reloj de 150m bajo un techo de 210, o sea la
# relacion CORRECTA, y nadie la estaba midiendo.
#
# LO QUE SIGUE FUERA DE ALCANCE: los guiones que las tareas llaman. Si una tarea invoca
# `bash scripts/x.sh` y el reloj vive dentro de ese guion, este gate no lo ve. Un piso mas, y se
# dice en vez de prometerse.
#
# rc 0 limpio · 1 hallazgos · 2 NO HE PODIDO MIRAR (sin python3/PyYAML, sin directorio, o menos
# workflows legibles que el mínimo: con cero, «todos caben» sería cierto y vacío a la vez).
set -uo pipefail
RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
cd "$RAIZ" || { echo "check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: no entro en $RAIZ." >&2; exit 2; }
WFDIR="${OLIVARES_INNER_WFDIR:-.github/workflows}"
[ -d "$WFDIR" ] || { echo "check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: no existe $WFDIR." >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: sin python3." >&2; exit 2; }
[ "$#" -eq 0 ] || { echo "check-ci-inner-timeouts: ⛔ recibí argumentos y no honro ninguno; usa OLIVARES_INNER_WFDIR." >&2; exit 2; }

python3 - "$WFDIR" "${OLIVARES_INNER_MIN:-3}" "${OLIVARES_INNER_MARGEN:-1}" <<'PY'
import glob, os, re, sys
try:
    import yaml
except Exception as e:
    print(f"check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: sin PyYAML ({e})", file=sys.stderr); sys.exit(2)

wfdir, minimo, margen = sys.argv[1], int(sys.argv[2]), float(sys.argv[3])
U = {'s': 1/60, 'm': 1, 'h': 60, 'd': 1440}

def dur(tok):
    # ⛔ SE QUITAN LAS COMILLAS. `timeout "$INNER_TIMEOUT" …` sustituido queda `timeout "10m"`, y sin
    # esto el token con comillas no casa como duracion, el reloj se vuelve invisible y el paso sale
    # limpio — el mismo CLEAN sin mirar que esta version viene a cerrar, un nivel mas abajo.
    tok = tok.strip('"\'')
    m = re.fullmatch(r'(\d+)([smhd]?)', tok)
    if not m: return None
    return float(m.group(1)) * U[m.group(2) or 's']

def entorno(d, j, s):
    """env efectivo del paso: workflow < job < paso. Solo valores ESTATICOS."""
    e = {}
    for cap in (d.get('env') or {}, j.get('env') or {}, s.get('env') or {}):
        for k, v in cap.items():
            if isinstance(v, (str, int, float)): e[str(k)] = str(v)
    return e

def sustituye(run, env):
    """Reemplaza $VAR y ${VAR} por su valor ESTATICO. Devuelve (texto, no_resueltas)."""
    pend = set()
    def rep(m):
        nom = m.group(1) or m.group(2)
        if nom in env: return env[nom]
        pend.add(nom); return m.group(0)
    txt = re.sub(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)', rep, run)
    return txt, pend

def relojes(run):
    """Devuelve [(descripcion, minutos, kill_after_min)] de cada reloj interior del bloque."""
    out = []
    # forma 1 · timeout(1): se SALTAN las banderas y la duracion es el primer token que no lo es.
    # Sin ese salto, `--kill-after=60s 80m` se lee como «60 s» — el kill-after disfrazado de reloj.
    # ⛔ HAY BANDERAS QUE SE COMEN EL TOKEN SIGUIENTE. `timeout --signal TERM 10m` (con el valor
    # SEPARADO, no `--signal=TERM`) rompia la version anterior: `TERM` no es una duracion, el bucle
    # cortaba ahi y el reloj salia INVISIBLE — un CLEAN sobre un paso que no cabe. Lo cazo the reviewer.
    CONVALOR = {'-s', '--signal', '-k', '--kill-after'}
    for m in re.finditer(r'(?<![\w./-])timeout\b([^\n]*)', run):
        toks, D, K, saltar = m.group(1).split(), None, 0.0, False
        for i, t in enumerate(toks):
            if saltar:
                saltar = False
                if toks[i-1] in ('-k', '--kill-after'): K = dur(t) or 0.0
                continue
            if t.startswith('-'):
                k = re.search(r'--kill-after=(\d+[smhd]?)|(?:^|,)-k=(\d+[smhd]?)', t)
                if k: K = dur(k.group(1) or k.group(2)) or 0.0
                if t in CONVALOR: saltar = True
                continue
            # Una duracion que empieza por `$` o `${{` NO es «un token cualquiera»: es EL reloj,
            # ilegible. Se devuelve como D=None para que el llamante lo trate como «no pude mirar».
            # `$VAR` (shell) y `{{.VAR}}` (plantilla de go-task) son las DOS formas de que la
            # duracion sea ilegible. Mirar solo la primera dejaba pasar los relojes del Taskfile.
            if t.strip('"\'').startswith('$') or t.strip('"\'').startswith('{{'):
                out.append((f"timeout … {t}", None, K)); D = None; break
            D = dur(t)
            if D is None: continue   # un token que no es duracion no cierra la busqueda
            break
        if D is not None: out.append((f"timeout {toks[0] if toks else ''} …".strip(), D, K))
    # forma 2 · el reloj INTERNO de otro comando
    for m in re.finditer(r'(?<![\w-])(--?timeout[= ])(\S+)', run):
        val = m.group(2)
        if val.startswith('$') or val.startswith('{{'):
            out.append((m.group(0).strip(), None, 0.0)); continue
        D = dur(val)
        if D is not None: out.append((m.group(0).strip(), D, 0.0))
    return out

# ── EL RELOJ QUE VIVE UN PISO MAS ABAJO ────────────────────────────────────────────────────────
# El punto ciego que este gate DECLARABA: `task test:race-hot:modules` no ensena su reloj en el
# `run:`, lo lleva dentro del Taskfile. Hoy tiene caso vivo y medido — `race-modules` en el
# candidato 3 murio con `panic: test timed out after 2h30m0s`, o sea su reloj de 150m disparo bajo
# un techo de 210: la relacion es CORRECTA y **nadie la estaba midiendo**. Basta que alguien baje
# el techo o suba el `-timeout` para que deje de serlo, y el gate no lo diria.
TAREAS = {}
try:
    # El Taskfile es inyectable para que la bateria sea HERMETICA: sin esto sus fixtures leerian
    # el Taskfile REAL de 741 tareas y el caso pasaria o fallaria segun lo que alguien tocara hoy.
    _tf = yaml.safe_load(open(os.environ.get('OLIVARES_INNER_TASKFILE', 'Taskfile.yml'), encoding='utf-8'))
    TAREAS = (_tf.get('tasks') or {}) if isinstance(_tf, dict) else {}
except FileNotFoundError:
    TAREAS = {}
except Exception as e:
    print(f"check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: Taskfile.yml no parsea ({e})", file=sys.stderr)
    sys.exit(2)

def cuerpo_tarea(nombre, vistas=None):
    """Texto de los `cmds` de una tarea Y de todo lo que arrastra por `deps:`/`cmds: - task:`.

    UNA pasada y CICLO-SEGURA: `vistas` corta la recursion, asi que un `a -> b -> a` no cuelga el
    gate. Devuelve (texto, faltantes) — `faltantes` son los nombres de tarea que el Taskfile no
    tiene, y eso NO es «sin reloj»: es «no he podido mirar»."""
    if vistas is None: vistas = set()
    if nombre in vistas: return "", []
    vistas.add(nombre)
    t = TAREAS.get(nombre)
    if t is None: return "", [nombre]
    if not isinstance(t, dict): return str(t), []
    trozos, faltan = [], []
    for dep in (t.get('deps') or []):
        n2 = dep.get('task') if isinstance(dep, dict) else dep
        if n2:
            tx, f2 = cuerpo_tarea(str(n2), vistas); trozos.append(tx); faltan += f2
    for c in (t.get('cmds') or []):
        if isinstance(c, dict):
            n2 = c.get('task')
            if n2:
                tx, f2 = cuerpo_tarea(str(n2), vistas); trozos.append(tx); faltan += f2
        else:
            trozos.append(str(c))
    return "\n".join(trozos), faltan

leidos, hallazgos, mirados, conrun, sinresolver = 0, [], 0, 0, []
for f in sorted(glob.glob(os.path.join(wfdir, '*.yml')) + glob.glob(os.path.join(wfdir, '*.yaml'))):
    try:
        d = yaml.safe_load(open(f, encoding='utf-8'))
    except Exception as e:
        print(f"check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: {f} no parsea ({e})", file=sys.stderr); sys.exit(2)
    leidos += 1
    for jn, j in (d.get('jobs') or {}).items():
        if not isinstance(j, dict): continue
        for s in (j.get('steps') or []):
            if 'run' not in s: continue
            conrun += 1
            # ⛔ EL `env` ESTATICO SE RESUELVE, Y LO QUE NO SE PUEDE RESOLVER ES rc 2, NUNCA CLEAN.
            # Lo cazo the reviewer: `timeout "$INNER_TIMEOUT" …` con `env.INNER_TIMEOUT: 10m`, y un
            # `GOFLAGS: -timeout=45m` con `go test ./...`, salian «0 relojes … limpio». Un reloj que
            # no puedo leer NO es un reloj que quepa: es un «no he podido mirar».
            env = entorno(d, j, s)
            texto, _ = sustituye(str(s['run']), env)
            # los relojes escondidos en el propio `env` (GOFLAGS y compania) cuentan igual
            for k, v in env.items():
                if re.search(r'--?timeout[= ]\S', v): texto += "\n" + v
            # ⛔ SOLO ES «NO RESOLUBLE» SI LA VARIABLE ESTA EN LA POSICION DE LA DURACION. La primera
            # version marcaba cualquier paso con variables en su shell —`$RUNNER_TEMP`, `$rc`— aunque
            # su reloj fuese un literal: tres falsos «no pude mirar» sobre relojes perfectamente
            # legibles. Un «no pude mirar» que se dispara de mas gasta la misma credibilidad que un
            # CLEAN que no mira.
            # ── y ahora lo que el paso DELEGA en `task <nombre>` ───────────────────────────
            delegado, faltan, tareas_vistas = "", [], []
            # ⛔ `task` TIENE QUE ESTAR EN POSICION DE COMANDO. Un `(?<![\w./-])task\s+(\w+)` casa la
            # PROSA de los comentarios —«task no esta en PATH», «task that fails», un `lint:X` de
            # ejemplo— y me dio **24 falsos «no pude mirar»** en el primer intento: el gate diciendo
            # que no puede leer 24 pasos porque no encuentra tareas llamadas `no` y `that`. Se exige
            # inicio de linea o separador de comando, y se descartan las lineas de comentario.
            for _ln in texto.split("\n"):
                if _ln.lstrip().startswith('#'): continue
                for mt in re.finditer(r'(?:^|[;&|]|&&|\|\||\$\()\s*task\s+([A-Za-z][\w:.-]*)', _ln):
                    nom = mt.group(1)
                    tx, f2 = cuerpo_tarea(nom)
                    if tx: delegado += "\n" + tx; tareas_vistas.append(nom)
                    faltan += f2
            if faltan:
                sinresolver.append((os.path.basename(f), jn, str(s.get('id') or s.get('name','·'))[:34],
                                    "tarea(s) que el Taskfile no tiene: " + ", ".join(sorted(set(faltan))[:3])))
                continue
            malt = [(d_, nom) for nom in tareas_vistas
                    for d_, D_, K_ in relojes(cuerpo_tarea(nom)[0]) if D_ is None]
            if malt:
                sinresolver.append((os.path.basename(f), jn, str(s.get('id') or s.get('name','·'))[:34],
                                    "reloj no legible dentro de `task %s`: %s" % (malt[0][1], malt[0][0])))
                continue

            mal = [d_ for d_, D_, K_ in relojes(texto) if D_ is None]
            if mal:
                sinresolver.append((os.path.basename(f), jn, str(s.get('id') or s.get('name','·'))[:34],
                                    "la duracion no es legible: " + " · ".join(mal[:2])))
                continue
            # el reloj delegado se juzga con la MISMA desigualdad, y su descripcion NOMBRA la tarea
            for desc, D, K in relojes(texto) + [(f"{d_} (dentro de `task {nom}`)", D_, K_)
                                                for nom in tareas_vistas
                                                for d_, D_, K_ in relojes(cuerpo_tarea(nom)[0]) if D_ is not None]:
                mirados += 1
                techo = s.get('timeout-minutes') or j.get('timeout-minutes') or 360
                exige = D + K + margen
                if float(techo) < exige:
                    hallazgos.append((os.path.basename(f), jn, str(s.get('id') or s.get('name', '·'))[:34],
                                      techo, desc, D, K, exige))

# ⛔ FAIL-OPEN CERRADO: un arbol cuyos workflows sean TODO `uses:` no tiene nada que juzgar, y
# decir «0 relojes caben» sobre el es un verde que no ha mirado nada. Lo cazo the reviewer (A-01).
# ⛔ LO NO RESOLUBLE SALE 2 ANTES QUE NADA: un CLEAN que no ha podido leer un reloj es la misma
# mentira que un verde sin mirar. Se nombra el paso, para que se pueda arreglar en vez de adivinar.
if sinresolver:
    print(f"check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR {len(sinresolver)} paso(s) con reloj no resoluble:", file=sys.stderr)
    for f_, jn_, sid_, por in sinresolver:
        print(f"    {f_} · {jn_} · {sid_} — {por}", file=sys.stderr)
    print("  Da su duracion como literal, o fija la variable en `env:` con un valor estatico.", file=sys.stderr)
    sys.exit(2)

if conrun == 0:
    print(f"check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: {leidos} workflow(s) legibles y NINGUN paso con `run:`.", file=sys.stderr)
    sys.exit(2)

if leidos < minimo:
    print(f"check-ci-inner-timeouts: ⛔ NO HE PODIDO MIRAR: {leidos} workflow(s) legibles, mínimo {minimo}.", file=sys.stderr)
    sys.exit(2)

if hallazgos:
    # ⛔ LA CIFRA DE LO MIRADO VA TAMBIEN EN EL ROJO. Antes solo salia en el mensaje limpio, asi que
    # con hallazgos el lector sabia CUALES fallan y no SOBRE CUANTOS se juzgo — y sin denominador un
    # «3 fallan» no dice si el gate miro tres relojes o treinta.
    print(f"check-ci-inner-timeouts: ⛔ {len(hallazgos)} de {mirados} reloj(es) interior(es) NO pueden disparar:")
    for f, jn, sid, techo, desc, D, K, exige in hallazgos:
        print(f"    {f} · {jn} · {sid}")
        print(f"      techo del paso {techo} min · interior «{desc}» = {D:g} min"
              + (f" + kill-after {K:g} min" if K else "") + f" · exige ≥ {exige:g}")
    print("  Su rama fail-closed es INALCANZABLE: GitHub mata el paso antes de que el reloj interior")
    print("  pueda devolver su diagnóstico. O sube el techo, o baja el reloj — pero no las dos cifras")
    print("  a la vez sin decir cuál manda.")
    sys.exit(1)
print(f"check-ci-inner-timeouts: limpio — {mirados} reloj(es) interior(es) caben en su techo"
      f" ({conrun} paso(s) con `run:` en {leidos} workflow(s))")
PY
