#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# r8-curar-superficies.sh — clasifica las superficies de un `manifest.json` de capturas y, si se le
# da un manifiesto anterior, dice cuáles CAMBIARON de verdad.
#
# ⛔ POR QUÉ ESTO ES UN GUION Y NO UNA LECTURA A OJO. La curación de las superficies vacías se ha
#    hecho tres veces a mano y las tres veces produjo una CIFRA —«8 placeholders», «19 vistas sin
#    filas», «23 con filas 0») que nadie pudo reproducir después, porque el criterio vivía en la
#    cabeza de quien miraba. Una cifra sin su predicado envejece en silencio: es el mismo defecto
#    que este repositorio ya documenta para el conteo de gates del hook.
#
# ⚠ Las palabras entrecomilladas de arriba —«placeholders», «vistas sin filas»— son CITAS de
#    aquellos informes, no clases que este guion produzca. Los veredictos que emite son EXACTAMENTE
#    cuatro y estan en `clasifica()`: CON DATOS, HUECO DE SEMBRADO, SIN TABLA y SIN TABLA NI TEXTO.
#    Lo aclaro porque un lector entendio «placeholder» como alcance declarado y busco su predicado:
#    si una palabra en la cabecera se lee como contrato, la cabecera esta mal escrita.
#
# ⛔ Y NO ADJUDICA. Igual que el arnés que produce el manifiesto, esto CLASIFICA y nombra; decidir
#    si una pantalla vacía es un hueco de sembrado o el producto correcto es de quien conoce el
#    dominio. Lo que el guion garantiza es que la pregunta llegue con el dato al lado.
set -u -o pipefail

MAN="${1:-}"
VIEJO="${2:-}"
if [ -z "$MAN" ] || [ ! -f "$MAN" ]; then
	echo "usage: bash scripts/r8-curar-superficies.sh <manifest.json> [previous-manifest.json]" >&2
	exit 2
fi

python3 - "$MAN" "$VIEJO" <<'PY'
import json, sys

nuevo, viejo = sys.argv[1], (sys.argv[2] or None)

def tomas(p):
    with open(p, encoding="utf8") as fh:
        return json.load(fh).get("take", [])

# ⛔ SOLO EL TEMA CLARO. Cada superficie se captura en claro y oscuro: contarlas las dos veces
#    duplicaria cada veredicto y haria que "23 superficies" y "46 tomas" se usaran como sinonimos.
t = [x for x in tomas(nuevo) if x.get("theme") == "light"]

# El umbral de texto es una AYUDA DE TRIAJE, no un veredicto, y por eso se imprime el numero al
# lado: el propio arnes midio `killswitch` en 1672 caracteres e `inference-proxy` en 2274 — dos
# formularios sin una sola tabla. Lo que separa los casos es `tablas_vacias`, no el texto.
UMBRAL_TEXTO = 400

# ⛔⛔ EL VEREDICTO DESCRIBE LO MEDIDO, NO LO QUE CONCLUYE. Este cuarto caso se llamaba
#     «PANTALLA VACIA» y era una AFIRMACION que la medida no sostiene: aqui solo se cuentan FILAS
#     DE TABLA, asi que una lista de TARJETAS o de definiciones —que no tiene `tbody tr`— cae en
#     el mismo cubo que una pantalla de verdad vacia.
#
#     Medido el 2026-08-30 abriendo las SEIS imagenes que marco: `tenants` mostraba su
#     organizacion con sus acciones, `settings` el perfil entero, `status-page` cinco componentes
#     con su estado, `login` y `setup` sus formularios completos. **Cinco de seis estaban bien.**
#     La unica que pedia algo —`communications-protocol-bindings`— tampoco estaba vacia: decia
#     «Select a workspace», que es del ARNES.
#
#     ⇒ Se renombra a `SIN TABLA NI TEXTO`, que es exactamente lo que se ha medido, y el dueño
#       dice «MIRA EL PNG». Sigue contando para el rc —hay que mirarla— pero ya no ASEGURA que
#       este vacia. La clase hermana, `HUECO DE SEMBRADO`, SI es fiable: verificada en las cuatro,
#       todas con su tabla montada y su estado vacio explicito.
def clasifica(x):
    filas = x.get("filas", 0)
    tv = x.get("tablas_vacias", 0)
    txt = x.get("texto_main", 0)
    if filas > 0:
        return "CON DATOS", ""
    if tv >= 1:
        # Cabeceras de columna presentes y ni una fila: no hay que interpretar nada.
        return "HUECO DE SEMBRADO", "backend (seeded per route)"
    if txt >= UMBRAL_TEXTO:
        # Sin tablas y con contenido: es un formulario o un panel. No es un hueco.
        return "SIN TABLA", ""
    return "SIN TABLA NI TEXTO", "CHECK THE PNG: this class is correct in 1 of 6 cases"

# ⛔ UN UMBRAL INVISIBLE ES PEOR QUE NO TENERLO. Salio en la primera corrida contra datos reales:
#    `work-decisions` dio `texto_main = 395` con el umbral en 400 — CINCO caracteres separaban
#    «SIN TABLA NI TEXTO» de «SIN TABLA», o sea un hueco que se escala de uno que no. Un veredicto que
#    depende de cinco caracteres no es falso, pero fingir que es firme si lo es. Se marcan los que
#    caen al filo para que quien lea sepa cuales NO se sostienen solos.
MARGEN = 100

def al_filo(x):
    return x.get("filas", 0) == 0 and x.get("tablas_vacias", 0) == 0 and abs(x.get("texto_main", 0) - UMBRAL_TEXTO) <= MARGEN

filas_out = []
for x in sorted(t, key=lambda y: y.get("id", "")):
    v, duenyo = clasifica(x)
    if al_filo(x):
        duenyo = (duenyo + " ").strip() + " ⚠ AL FILO of the threshold"
    filas_out.append((x.get("id", "?"), v, x.get("filas", 0), x.get("tablas_vacias", 0), x.get("texto_main", 0), duenyo))

print(f"surfaces (light theme): {len(filas_out)}")
print()
print(f"  {'surface':30} {'verdict':18} {'rows':>5} {'empty.t':>7} {'text':>6}  owner")
for i, v, f, tv, txt, d in filas_out:
    print(f"  {i:30} {dict([('CON DATOS', 'HAS DATA'), ('HUECO DE SEMBRADO', 'SEEDING GAP'), ('SIN TABLA', 'NO TABLE'), ('SIN TABLA NI TEXTO', 'NO TABLE OR TEXT')]).get(v, v):18} {f:5} {tv:7} {txt:6}  {d.replace('AL FILO', 'BORDERLINE')}")

print()
for v in ("CON DATOS", "HUECO DE SEMBRADO", "SIN TABLA", "SIN TABLA NI TEXTO"):
    n = sum(1 for r in filas_out if r[1] == v)
    print(f"  {dict([('CON DATOS', 'HAS DATA'), ('HUECO DE SEMBRADO', 'SEEDING GAP'), ('SIN TABLA', 'NO TABLE'), ('SIN TABLA NI TEXTO', 'NO TABLE OR TEXT')]).get(v, v):18} {n}")

n_filo = sum(1 for r in filas_out if "AL FILO" in r[5])
if n_filo:
    print()
    print(f"  ⚠ {n_filo} verdict(s) BORDERLINE (text within {MARGEN} of the threshold {UMBRAL_TEXTO}):")
    print("    these need review before being escalated as gaps.")

# ── PANELES VACIOS ───────────────────────────────────────────────────────────────────────────
# ⛔ `empty_panels` NO ES UNA LISTA DE HUECOS: son tres clases con duenos distintos, y hasta ahora
#    se leian como una sola. Cruzando la lista con la toma de cada superficie (que es lo que el
#    manifiesto ya permite y nadie hacia) salen:
#
#      · panel vacio Y tabla sin filas  -> es el hueco de sembrado, ya nombrado arriba.
#      · panel vacio SIN tabla          -> un panel o tarjeta que sale vacia y no tiene tabla
#                                          donde sembrar: o es correcto, o el hueco esta en el
#                                          componente, no en el estate.
#      · panel vacio en superficie CON DATOS -> la mas interesante y la que no se miraba: la
#                                          pantalla trae filas y AUN ASI le falta un panel. No es
#                                          «vacia»: es INCOMPLETA, y por eso no aparece en ninguna
#                                          busqueda de pantallas vacias.
with open(nuevo, encoding="utf8") as fh:
    _d = json.load(fh)
_ep = _d.get("empty_panels", [])
if _ep:
    _porid = {x.get("id"): x for x in _d.get("take", []) if x.get("theme") == "light"}
    _clases = {"hueco de sembrado": [], "panel sin tabla": [], "superficie CON datos": []}
    for e in _ep:
        x = _porid.get(e.get("id"), {})
        if x.get("filas", 0) > 0:
            _clases["superficie CON datos"].append(e.get("id"))
        elif x.get("tablas_vacias", 0) >= 1:
            _clases["hueco de sembrado"].append(e.get("id"))
        else:
            _clases["panel sin tabla"].append(e.get("id"))
    print()
    print(f"empty panels: {len(_ep)} surface(s), in THREE classes with different owners")
    for k, v in _clases.items():
        print(f"  {dict([('hueco de sembrado', 'seeding gap'), ('panel sin tabla', 'panel without a table'), ('superficie CON datos', 'surface WITH data')]).get(k, k):22} {len(v):3}  {', '.join(sorted(v)) if v else '—'}")
    if _clases["superficie CON datos"]:
        print("  ⚠ the final class does NOT contain empty screens: they have rows but are still missing")
        print("    a panel, so searches for empty screens miss them.")

# ── RANCIDEZ ─────────────────────────────────────────────────────────────────────────────────
# ⛔ SE COMPARA POR `sha256` DE LA IMAGEN, no por fecha. Una captura re-tomada sobre un arbol que
#    no cambio produce el MISMO png y no es "nueva"; una con fecha de hoy y sha identico es
#    exactamente lo que no hay que volver a publicar. La fecha dice cuando se corrio, no contra que.
if viejo:
    ant = {(x.get("id"), x.get("theme")): x.get("sha256") for x in tomas(viejo)}
    act = {(x.get("id"), x.get("theme")): x.get("sha256") for x in tomas(nuevo)}
    cambiadas = sorted(k for k in act if k in ant and act[k] != ant[k])
    iguales = sorted(k for k in act if k in ant and act[k] == ant[k])
    nuevas = sorted(k for k in act if k not in ant)
    idas = sorted(k for k in ant if k not in act)
    print()
    print(f"staleness compared with {viejo}")
    print(f"  changed {len(cambiadas)} · unchanged {len(iguales)} · new {len(nuevas)} · removed {len(idas)}")
    for etiq, conj in (("new", nuevas), ("removed", idas)):
        for k in conj[:12]:
            print(f"    {etiq:14} {k[0]} ({k[1]})")
else:
    print()
    print("  (no previous manifest: staleness is unmeasured, not absent)")

# ⛔ MEDIDO SOBRE LAS 134 TOMAS DE LA CORRIDA DEL 2026-08-30 (67 superficies, tema claro), que es
#    el unico universo real que ha visto este guion — hasta ahora sus cifras salian de fixtures:
#
#      CON DATOS 29 · HUECO DE SEMBRADO 5 · SIN TABLA 26 · SIN TABLA NI TEXTO 7   (suma 67)
#      rc 1 con 11 pidiendo adjudicacion · 2 AL FILO excluidas del recuento
#
#    Y el dato que obliga a la cautela del rotulo: de las SEIS que el veredicto marco entonces,
#    CINCO estaban bien al abrir su PNG. La clase hermana, HUECO DE SEMBRADO, acerto en las CUATRO.
#
# ⛔ CONTRATO DE SALIDA, QUE ESTE GUION NO TENIA. Salia 0 SIEMPRE, incluso listando nueve huecos
#    de sembrado: un hallazgo que sale 0 no es un hallazgo, es una impresion por pantalla, y quien
#    lo llame desde un guion no puede distinguir «no hay nada» de «hay nueve». Lo midio the reviewer.
#
#    rc 0 = ninguna superficie pide adjudicacion
#    rc 1 = hay hallazgos (huecos de sembrado o «sin tabla ni texto»), listados arriba
#    rc 2 = no he podido mirar (falta el manifiesto o no se puede leer) — ya sale antes
#
#    ⚠ Y las de «AL FILO» NO cuentan por si solas: son un aviso sobre la firmeza del veredicto,
#      no un hallazgo. Si lo fueran, mover el umbral cambiaria el rc sin que el arbol cambiase.
#
#    ⛔⛔ Y ESTA LINEA NO LO CUMPLIA. Contaba TODA `SIN TABLA NI TEXTO`, incluidas las marcadas AL
#      FILO, asi que un manifiesto cuyo unico hallazgo fuera una superficie al filo salia rc 1 —
#      contra el parrafo de justo arriba. La frase decia una cosa y el codigo hacia otra: la
#      MISMA clase que este guion existe para cerrar en el JSX, cometida en el contrato del
#      propio guion, y dentro de la cura que hice para esa clase. Lo midio el contraste.
#      El marcador vive en la columna del dueño (r[5]), que es donde `al_filo()` lo escribe.
_hallazgos = sum(
    1
    for r in filas_out
    if r[1] in ("HUECO DE SEMBRADO", "SIN TABLA NI TEXTO") and "AL FILO" not in r[5]
)
if _hallazgos:
    print()
    print(f"  ⇒ {_hallazgos} surface(s) require assessment (seeding gaps + no table or text).")
    print("    ⚠ no table or text does NOT mean empty: this script only counts TABLE rows,")
    print("      which card and definition lists do not have. Measured on 2026-08-30 across")
    print("      six flagged surfaces: FIVE were correct. Check the PNG before escalating any.")
    sys.exit(1)
print()
print("  ⇒ no surface requires assessment.")
sys.exit(0)
PY
