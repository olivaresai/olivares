# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Agrupa el residuo de los TMPDIR en FAMILIAS y senala las que parecen una fuga.

QUE MIDE, DICHO SIN ADORNOS. Observa DOS veces quien esta vivo, censa las entradas de primer
nivel de cada raiz, descarta las que un proceso sostiene, revalida que las demas sigan existiendo
al decidir, y cuenta cuantas quedan y cuanto pesan. Eso es todo lo que mide.

QUE NO MIDE, y por eso el veredicto se llama SOSPECHA y no FUGA. No observa reapariciones en el
TIEMPO: una sola foto no distingue "este gate creo 120 ficheros ahora" de "120 quedaron de ayer".
No conoce al creador. No prueba que una familia sea una fuga ni que no sea una cache. Un productor
que cierra sus descriptores entre fases y reabre por nombre parece muerto mientras trabaja.

⛔ ESTE FICHERO AFIRMABA "no tiene falsos positivos por construccion". ERA FALSO, y lo refuto por
EJECUCION una revision independiente del 2026-09-02,
que reprodujo las dos direcciones del error sobre la version anterior:

  . productor vivo sin descriptor abierto en el instante de la foto      -> decia FUGA
  . `cwd` vivo en un DESCENDIENTE de la entrada, o ejecutable en /proc/PID/exe -> decia FUGA
  . 120 entradas censadas y BORRADAS antes de decidir                    -> decia FUGA y afirmaba
                                                                            que "siguen ahi"
  . 119 huerfanas y UNA hermana viva                                     -> absolvia a las 119
  . TMPDIR == /tmp, la raiz contada dos veces                            -> duplicaba n y bytes
  . descendientes ilegibles                                             -> 0 MiB y CLEAN

Todo lo que sigue es la respuesta a ese contraste. Lo que NO se ha resuelto se dice en el veredicto
en vez de taparse: sin registro de actividad de gates (PID + raiz temporal) o sin una ventana de
antiguedad, la cardinalidad y el tamano no pueden dar un veredicto categorico.
"""
import os
import re
import subprocess
import sys
from collections import defaultdict

lista, vivos_f = sys.argv[1], sys.argv[2]
RAIZ = os.environ["RAIZ"]


def entero(nombre, defecto):
    """Un umbral que no es un numero es NO PUDE MIRAR, no un valor por defecto silencioso."""
    v = os.environ.get(nombre, "")
    if v == "":
        return defecto
    try:
        n = int(v)
    except ValueError:
        print(
            "check-disk-residue: NO PUDE MIRAR - %s=%r no es un entero." % (nombre, v),
            file=sys.stderr,
        )
        sys.exit(2)
    if n < 1:
        print(
            "check-disk-residue: NO PUDE MIRAR - %s=%d tiene que ser >= 1." % (nombre, n),
            file=sys.stderr,
        )
        sys.exit(2)
    return n


N_MIN = entero("OLIVARES_RESIDUE_MIN_COUNT", 20)
MIB_MIN = entero("OLIVARES_RESIDUE_MIN_MIB", 50)
N_SOLO = entero("OLIVARES_RESIDUE_COUNT_ONLY", 100)

with open(vivos_f, encoding="utf-8", errors="replace") as fh:
    vivos_brutos = [x for x in fh.read().split("\n") if x]

# Una ruta viva cubre a la entrada que la contiene: un `cwd` en `familia.X/sub/f` sostiene
# `familia.X`. Se guarda tambien la forma canonica, porque /proc devuelve el destino resuelto
# mientras `find` enumera el enlace.
vivos = set()
for v in vivos_brutos:
    vivos.add(v)
    try:
        vivos.add(os.path.realpath(v))
    except OSError:
        pass


def sostenida(ruta):
    """True si algun proceso sostiene esta entrada o algo por debajo de ella."""
    cands = {ruta}
    try:
        cands.add(os.path.realpath(ruta))
    except OSError:
        pass
    for c in cands:
        if c in vivos:
            return True
        pref = c.rstrip("/") + "/"
        for v in vivos:
            if v.startswith(pref):
                return True
    return False


def familia(base):
    """Normaliza el segmento aleatorio de las plantillas de `mktemp` que usa esta casa.

    Cubre `nombre.XXXXXX`, `nombre-XXXXXX`, `nombre.XXXXXX.ext` y `mktemp` a secas. NO pretende
    cubrir todas las formas imaginables: lo que no reconoce queda como familia propia y por tanto
    NO dispara, que es la direccion segura de fallar para un instrumento que acusa.
    """
    f = re.sub(r"^tmp[A-Za-z0-9_]{6,12}$", "tmpXXXXXX", base)
    if f != base:
        return f
    f = re.sub(r"([._-])[A-Za-z0-9]{6,12}(\.[A-Za-z0-9]{1,5})$", r"\1XXXXXX\2", base)
    if f != base:
        return f
    return re.sub(r"([._-])[A-Za-z0-9]{6,12}$", r"\1XXXXXX", base)


class Cobertura:
    """Lo que no se pudo leer NO se convierte en cero: se cuenta y degrada el veredicto."""

    def __init__(self):
        self.ilegibles = 0
        self.desaparecidas = 0


cob = Cobertura()
vistos_inodos = set()


def peso(ruta):
    """Bytes reales. Deduplica por (st_dev, st_ino): un hard link no se cuenta dos veces."""
    try:
        st = os.lstat(ruta)
    except OSError:
        cob.ilegibles += 1
        return 0
    if os.path.islink(ruta):
        return 0
    if not os.path.isdir(ruta):
        clave = (st.st_dev, st.st_ino)
        if clave in vistos_inodos:
            return 0
        vistos_inodos.add(clave)
        return st.st_size
    total = 0
    fallo = []
    for raiz, _, ficheros in os.walk(ruta, onerror=lambda e: fallo.append(e)):
        for x in ficheros:
            q = os.path.join(raiz, x)
            try:
                st = os.lstat(q)
            except OSError:
                cob.ilegibles += 1
                continue
            clave = (st.st_dev, st.st_ino)
            if clave in vistos_inodos:
                continue
            vistos_inodos.add(clave)
            total += st.st_size
    if fallo:
        cob.ilegibles += len(fallo)
    return total


# --- censo: la clave lleva la RAIZ, para que dos raices no se sumen en una familia falsa.
fam = defaultdict(lambda: [0, 0, 0])  # n, bytes, sostenidas-descartadas
with open(lista, encoding="utf-8", errors="replace") as fh:
    rutas = []
    for ln in fh.read().splitlines():
        if not ln:
            continue
        raiz, _, p = ln.partition("\t")
        rutas.append((raiz, p))

for raiz, p in rutas:
    if not os.path.lexists(p):          # revalidacion: desaparecio entre el censo y la decision
        cob.desaparecidas += 1
        continue
    clave = (raiz, familia(os.path.basename(p)))
    if sostenida(p):                    # se descarta la ENTRADA, no la familia entera
        fam[clave][2] += 1
        continue
    fam[clave][0] += 1
    fam[clave][1] += peso(p)


def productor(f):
    """(donde, None) si se puede atribuir; (None, motivo) si no. AMBIGUA si hay varias."""
    pref = re.split(r"[._-]XXXXXX", f)[0]
    if f.startswith("tmpXXXXXX") or len(pref) < 4:
        return None, "sin plantilla: `mktemp` a secas no deja rastro en el nombre"
    # ⛔ NO se comprueba `isdir(RAIZ/.git)`: EN UN WORKTREE `.git` ES UN FICHERO, no un
    # directorio, y esa guarda dejaba sin atribuir todo lo que corriera desde un worktree -que es
    # donde corre cada carril-. Se pregunta a git, que es quien lo sabe.
    try:
        chk = subprocess.run(
            ["git", "-C", RAIZ, "rev-parse", "--git-dir"],
            capture_output=True, text=True, timeout=30,
        )
    except Exception as e:
        return None, "NO PUDE MIRAR: no pude preguntar a git (%s)" % type(e).__name__
    if chk.returncode != 0:
        return None, "NO PUDE MIRAR: %s no es un arbol de trabajo git" % RAIZ
    try:
        r = subprocess.run(
            ["git", "-C", RAIZ, "grep", "-n", "--", "mktemp.*" + re.escape(pref)],
            capture_output=True, text=True, timeout=60,
        )
    except Exception as e:
        return None, "NO PUDE MIRAR: git grep no pudo ejecutarse (%s)" % type(e).__name__
    if r.returncode not in (0, 1):
        return None, "NO PUDE MIRAR: git grep salio %d" % r.returncode
    hits = []
    for ln in r.stdout.splitlines():
        partes = ln.split(":", 2)
        if len(partes) >= 3 and partes[0].startswith(("scripts/", ".githooks/", "Taskfile")):
            if re.search(re.escape(pref) + r"[._-]?X{4,}", partes[2]):   # la plantilla ENTERA
                hits.append(partes[0] + ":" + partes[1])
    if not hits:
        return None, "ningun guion del arbol crea esa plantilla: el productor esta FUERA del arbol"
    if len(hits) > 1:
        return "AMBIGUA: " + ", ".join(hits[:4]), None
    return hits[0], None


sospechas = []
for (raiz, f), (n, sz, viv) in fam.items():
    mib = sz / 1048576
    if n >= N_SOLO or (n >= N_MIN and mib >= MIB_MIN):
        sospechas.append((n, mib, f, raiz, viv))
sospechas.sort(key=lambda x: (-x[0], -x[1]))

cabecera = "check-disk-residue: %d familia(s) . umbrales: %d x %d MiB, o %d por recuento" % (
    len(fam), N_MIN, MIB_MIN, N_SOLO,
)
if cob.ilegibles or cob.desaparecidas:
    cabecera += " . COBERTURA PARCIAL: %d ilegible(s), %d desaparecida(s) entre censo y decision" % (
        cob.ilegibles, cob.desaparecidas,
    )
print(cabecera)

if not sospechas:
    if cob.ilegibles:
        print(
            "check-disk-residue: NO PUDE MIRAR - %d entrada(s) ilegible(s): un CLEAN sobre una "
            "lectura parcial seria un verde a ciegas." % cob.ilegibles,
            file=sys.stderr,
        )
        sys.exit(2)
    print("check-disk-residue: CLEAN - ninguna familia pasa los umbrales sobre lo que SE PUDO leer.")
    sys.exit(0)

print("check-disk-residue: SOSPECHA - familias que merecen que su duenio las mire:", file=sys.stderr)
for n, mib, f, raiz, viv in sospechas:
    loc, motivo = productor(f)
    quien = loc if loc else "SIN ATRIBUIR (%s)" % motivo
    extra = " [%d entrada(s) viva(s) excluida(s)]" % viv if viv else ""
    print("    %5d x %-34s %8.1f MiB  en %s%s   %s" % (n, f, mib, raiz, extra, quien), file=sys.stderr)
print(
    "  SOSPECHA, no fuga demostrada: esto es cardinalidad y tamano en UNA ventana. No mide\n"
    "  reapariciones en el tiempo ni conoce al creador; un productor que cierra entre fases\n"
    "  parece muerto mientras trabaja. Lo categorico exige registro de actividad o antiguedad.",
    file=sys.stderr,
)
sys.exit(1)
