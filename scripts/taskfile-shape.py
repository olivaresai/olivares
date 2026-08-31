# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Imprime las tareas del Taskfile cuya FORMA esta rota.

Medido el 2026-08-20 sobre `main`: CATORCE tareas habian perdido sus claves.
El YAML seguia parseando -- por eso nadie lo veia -- pero SIETE tenian como
valor una CADENA con la prosa y el comando pegados, y `task` ejecuta una
tarea-cadena como una orden de shell; y NUEVE conservaban solo `desc:`, asi que
correrlas no hacia nada y salian 0. Nueve gates muertos que se leian como verdes.

Salida: una linea `CLASE<TAB>tarea` por hallazgo. Sin hallazgos, no imprime nada.

⛔ POR QUE HAY UN CAMINO SIN PyYAML, Y NO ES COMODIDAD (2026-08-20, mismo dia).
Con `import yaml` como unica via, este fichero imprimia `NOPUEDO` en cuanto la
biblioteca faltaba, `check-taskfile-graph.sh` lo convertia -- correctamente -- en
exit 2, y el hook, que falla cerrado con razon, **bloqueaba TODO push desde ese
contenedor**. Medido sobre `origin/main` puro, sin ningun cambio local:

    check-taskfile-graph: NO HE PODIDO MIRAR la forma: NOPUEDO  No module named 'yaml'
    task: Failed to run task "lint:taskfile-graph": exit status 2

Y en este contenedor **no hay `pip` en ninguna forma** (`pip`, `pip3`,
`python3 -m pip`), asi que no era una dependencia que el carril pudiera resolver.

Ninguna de las tres piezas se portaba mal: el helper avisa, el gate rehusa y el
hook falla cerrado. Lo que faltaba era que el helper pudiera MIRAR sin la
biblioteca. Y puede: el `Taskfile.yml` de este repositorio es un mapa plano de dos
espacios de indentacion, y las dos clases que este guion reporta se deciden con la
indentacion y la forma `clave:`, sin resolver YAML de verdad.

LIMITES DEL CAMINO DE REPUESTO, declarados porque no se pueden comprobar aqui:
  * Es una LECTURA CONSERVADORA, no un parser de YAML. Si encuentra algo que no
    sabe leer -- indentacion distinta de dos espacios, `tasks:` en flujo, un
    ancla o un merge key -- **no adivina: imprime NOPUEDO** y el gate vuelve a
    rehusar, que es el estado de hoy y nunca un falso verde.
  * NO he podido contrastarlo contra PyYAML en este contenedor, porque PyYAML es
    justamente lo que falta. Se usa SOLO cuando la biblioteca no esta, o sea
    donde hoy no hay ninguna respuesta; con PyYAML presente el comportamiento es
    byte a byte el de antes.
"""
import re
import sys

RUTA = sys.argv[1]


def por_yaml():
    """El camino de siempre. Devuelve la lista de hallazgos, o None si no puede."""
    try:
        import yaml
    except Exception:
        return None
    try:
        doc = yaml.safe_load(open(RUTA, encoding="utf-8")) or {}
    except Exception as exc:
        print("PARSE\t%s" % str(exc).replace("\n", " ")[:120])
        raise SystemExit(0)
    out = []
    for nombre, cuerpo in (doc.get("tasks") or {}).items():
        if not isinstance(cuerpo, dict):
            out.append(("CADENA", nombre))
        elif not any(k in cuerpo for k in ("cmds", "cmd", "deps")):
            out.append(("SINCMD", nombre))
    return out


CLAVE_TAREA = re.compile(r"^  ([A-Za-z][\w:.\-]*):\s*$")
# ⛔ HUECO CERRADO EL 2026-08-20 al revisar el desbloqueo. `CLAVE_TAREA` solo ve la clave
# SOLA en su linea, asi que una tarea-cadena escrita EN UNA LINEA — `  nombre: prosa …` —
# no se reconocia ni como tarea y la lectura plana la dejaba pasar EN SILENCIO. Y esa es
# justo la forma PELIGROSA: es la que `task` manda al shell. Verificado plantando
# `lint:probe-cadena: esto es prosa - bash …`: con PyYAML salia CADENA y sin PyYAML NO
# salia nada. Con esta segunda expresion, las dos vias coinciden.
CLAVE_TAREA_EN_LINEA = re.compile(r"^  ([A-Za-z][\w:.\-]*):[ \t]+\S")
CLAVE_HIJA = re.compile(r"^    ([A-Za-z][\w.\-]*):")
EJECUTABLE = ("cmds", "cmd", "deps")


def por_indentacion():
    """Lectura conservadora sin PyYAML. Devuelve hallazgos, o None si no se fia."""
    try:
        lineas = open(RUTA, encoding="utf-8").read().split("\n")
    except Exception as exc:
        print("PARSE\t%s" % str(exc).replace("\n", " ")[:120])
        raise SystemExit(0)

    # Formas que esta lectura NO sabe decidir. Ante cualquiera, se rehusa entera:
    # media respuesta sobre la forma de un Taskfile es peor que ninguna.
    if re.search(r"^tasks:\s*\{", "\n".join(lineas), re.M):
        return None
    for l in lineas:
        if re.match(r"^\t", l) or re.match(r"^  [A-Za-z][\w:.\-]*:\s*[&*]", l):
            return None

    dentro = False
    hallazgos = []
    tarea = None
    ejecutable = False
    tenia_hijas = False
    for l in lineas:
        if re.match(r"^tasks:\s*$", l):
            dentro = True
            continue
        if dentro and re.match(r"^[A-Za-z]", l):  # otra clave de primer nivel
            break
        if not dentro:
            continue
        mi = CLAVE_TAREA_EN_LINEA.match(l)
        if mi:
            # Tarea con valor en la MISMA linea: su valor no es un mapa por
            # construccion, asi que es CADENA sin necesidad de mirar hijas.
            if tarea is not None:
                if not tenia_hijas:
                    hallazgos.append(("CADENA", tarea))
                elif not ejecutable:
                    hallazgos.append(("SINCMD", tarea))
            hallazgos.append(("CADENA", mi.group(1)))
            tarea = None
            ejecutable = False
            tenia_hijas = False
            continue
        m = CLAVE_TAREA.match(l)
        if m:
            if tarea is not None:
                if not tenia_hijas:
                    hallazgos.append(("CADENA", tarea))
                elif not ejecutable:
                    hallazgos.append(("SINCMD", tarea))
            tarea = m.group(1)
            ejecutable = False
            tenia_hijas = False
            continue
        if tarea is None:
            continue
        h = CLAVE_HIJA.match(l)
        if h:
            tenia_hijas = True
            if h.group(1) in EJECUTABLE:
                ejecutable = True
    if tarea is not None:
        if not tenia_hijas:
            hallazgos.append(("CADENA", tarea))
        elif not ejecutable:
            hallazgos.append(("SINCMD", tarea))
    return hallazgos


# ⛔ HUECO CERRADO EL 2026-08-21. Este guion contaba la forma de las TAREAS y no veia una clave
#    DUPLICADA DENTRO de una tarea. La union mecanica de Taskfile.yml las produce: al integrar
#    #1308 en el lote 46, `lint:c13-06-canon-proposals:selftest` acabo con DOS `desc:`, cada una
#    diciendo algo distinto y util.
#
#    Lo grave es CUANDO se caza: PyYAML con carga permisiva no se queja, mi conteo de claves de
#    tarea tampoco, y solo lo detecta `task --list` — que es de lo ultimo que se corre. Un lote
#    entero puede montarse encima. Con esto sale en la misma pasada que el resto de la forma.
def claves_repetidas_en_tarea(lineas):
    hallazgos = []
    tarea, vistas = None, {}
    for n, l in enumerate(lineas, 1):
        m = CLAVE_TAREA.match(l)
        if m:
            tarea, vistas = m.group(1), {}
            continue
        if tarea is None:
            continue
        if l.strip() and not l.startswith("    "):
            tarea = None
            continue
        m2 = re.match(r"^    ([A-Za-z][\w-]*):", l)
        if not m2:
            continue
        k = m2.group(1)
        if k in vistas:
            hallazgos.append(("DUPCLAVE", "%s :: %s (lineas %d y %d)" % (tarea, k, vistas[k], n)))
        else:
            vistas[k] = n
    return hallazgos


res = por_yaml()
if res is None:
    res = por_indentacion()
if res is None:
    print("NOPUEDO\tsin PyYAML y la forma del fichero no es la que se sabe leer sin ella")
    raise SystemExit(0)

def tareas_repetidas(lineas):
    """Claves de TAREA definidas dos veces en el mismo mapa.

    ⛔ POR QUE HACE FALTA, con la medida (2026-08-21). `yaml.safe_load` de Python las ACEPTA
       —gana la ultima— y `task --list-all` tambien, asi que dos instrumentos decian CLEAN sobre
       un Taskfile con `lint:eco-11-reserve-funded` definida en la 2762 y otra vez en la 3305.
       Lo unico que lo vio fue un parser ESTRICTO de Go (`checkpgwiring`), y por un camino que
       no tiene nada que ver: rehuso el fichero entero y dejo `lint:pg-env` con 67 fallos, que se
       leen como un problema de Postgres. Un duplicado de tarea no es cosmetico: la union
       mecanica de dos ramas lo produce y la segunda definicion GANA en silencio.
    """
    hallazgos, vistas = [], {}
    for n, l in enumerate(lineas, 1):
        m = CLAVE_TAREA.match(l)
        if not m:
            continue
        k = m.group(1)
        if k in vistas:
            hallazgos.append(("DUPTAREA", "%s (lineas %d y %d)" % (k, vistas[k], n)))
        else:
            vistas[k] = n
    return hallazgos


res = list(res) + tareas_repetidas(
    open(RUTA, encoding="utf-8").read().split("\n")
) + claves_repetidas_en_tarea(
    open(RUTA, encoding="utf-8").read().split("\n")
)

for clase, nombre in res:
    print("%s\t%s" % (clase, nombre))
