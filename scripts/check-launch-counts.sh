#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-launch-counts.sh — el material de lanzamiento declara sus PROPIOS recuentos, y nadie los ataba.
#
# ⛔ POR QUÉ EXISTE, medido el 2026-08-19 y no supuesto. Varias piezas de `docs/launch/` publican un
#    número SOBRE SÍ MISMAS: el título de Show HN dice «(77 chars)», cada tuit lleva «— 255 chars»,
#    la descripción de Product Hunt lleva «<!-- 257 chars -->». Ese número es el ÚNICO control de
#    que la pieza cabe en el tope de su plataforma, porque nadie va a recontar a mano el día del
#    lanzamiento: se lee una vez y se confía.
#
#    Al retirar el marco «Claude-first» (AGT-08) hubo que reescribir cuatro de esas piezas, y el
#    acoplamiento mordió de inmediato: `docs/launch/README.md` describía el paquete en TRES sitios
#    y DOS de ellos fijaban «77 chars» del título viejo. El relevo sólo listaba uno. Los otros dos
#    aparecieron al re-medir, no al leer.
#
#    Un recuento desactualizado no falla ruidosamente: **certifica**. `SHOW-HN-DRYRUN.md` es un acta
#    de ensayo — su valor entero es que los números sean ciertos. Un acta que declara 77 sobre un
#    título de 79 no avisa de nada; da por validado un tope que ya no se ha comprobado.
#
# ⭐ Y AQUÍ EL INSTRUMENTO CORRECTO NO ES UN TECHO, es una IGUALDAD, que es lo que lo hace estable.
#    No se vigila «cuántos caracteres tiene» —eso cambia legítimamente cada vez que alguien mejora
#    una frase— sino que **lo declarado coincida con lo real**. Esa invariante no oscila: sólo se
#    rompe cuando alguien edita el texto y olvida el número, que es exactamente el fallo a cazar.
#
# ⚠ Y COMPRUEBA LA OTRA MITAD, que es la que se olvida: que el título tabulado en el acta EXISTA
#    literalmente en `show-hn.md`. Sin eso, el acta puede ser internamente coherente —79 chars y su
#    cadena mide 79— y aun así estar validando un título que ya nadie va a publicar.
#
# Salida: 0 verde · 1 hallazgo · 2 NO HE PODIDO MIRAR (deny-closed: falta un fichero, falta python3,
# o el censo sale vacío — «no he podido mirar» nunca se reporta como «está limpio»).

set -uo pipefail
cd "$(dirname "$0")/.." || { echo "check-launch-counts: cannot reach the repository root" >&2; exit 2; }

command -v python3 >/dev/null 2>&1 || {
  echo "check-launch-counts: 2 · COULD NOT CHECK — python3 is not installed" >&2; exit 2; }

DIR="${LAUNCH_DIR:-docs/launch}"
for f in show-hn.md SHOW-HN-DRYRUN.md social-threads.md product-hunt.md reddit-pack.md; do
  [ -r "$DIR/$f" ] || { echo "check-launch-counts: 2 · COULD NOT CHECK — missing $DIR/$f" >&2; exit 2; }
done

LAUNCH_DIR="$DIR" python3 - <<'PYCHECK'
import io, os, re, sys

D = os.environ["LAUNCH_DIR"]
read = lambda n: io.open(os.path.join(D, n), encoding="utf-8").read()
bad, checked = [], 0

showhn  = read("show-hn.md")
dryrun  = read("SHOW-HN-DRYRUN.md")
social  = read("social-threads.md")
ph      = read("product-hunt.md")
reddit  = read("reddit-pack.md")

# 1 · acta de ensayo de Show HN: lo declarado == lo real, bajo el tope, y el titulo EXISTE en show-hn.md
for m in re.finditer(r"^\| `(Show HN:[^`]+)` \| \*\*(\d+)\*\* \| (\d+) \|", dryrun, re.M):
    title, declared, limit = m.group(1), int(m.group(2)), int(m.group(3))
    checked += 1
    if len(title) != declared:
        bad.append("SHOW-HN-DRYRUN.md declares %d, but the title has %d characters: %s" % (declared, len(title), title))
    if declared > limit:
        bad.append("SHOW-HN-DRYRUN.md: %d exceeds the limit %d: %s" % (declared, limit, title))
    if title not in showhn:
        bad.append("SHOW-HN-DRYRUN.md lists a title absent from show-hn.md: %s" % title)

# 2 · hilos sociales: cada post declara su longitud; la convencion es len() del bloque de codigo
for m in re.finditer(r"\*\*(\d+)/ \([^)]*\) — (\d+) chars\*\*\n```\n(.*?)\n```", social, re.S):
    n, declared, body = m.group(1), int(m.group(2)), m.group(3)
    checked += 1
    if len(body) != declared:
        bad.append("social-threads.md post %s declares %d but measures %d" % (n, declared, len(body)))
    if len(body) > 280:
        bad.append("social-threads.md post %s has %d characters, above the 280-character limit" % (n, len(body)))

# 3 · Product Hunt: tagline con su tope inline, descripcion con su tope y su recuento en comentario
m = re.search(r"\*\*Tagline\*\* \(≤(\d+) chars[^)]*\)\n(.+)", ph)
if m:
    checked += 1
    if len(m.group(2)) > int(m.group(1)):
        bad.append("product-hunt.md tagline measures %d, limit %s" % (len(m.group(2)), m.group(1)))
m = re.search(r"\*\*Description\*\* \(≤(\d+) chars\)\n(.+)\n<!-- (\d+) chars", ph)
if m:
    checked += 1
    limit, text, declared = int(m.group(1)), m.group(2), int(m.group(3))
    if len(text) != declared:
        bad.append("product-hunt.md description declares %d but measures %d" % (declared, len(text)))
    if len(text) > limit:
        bad.append("product-hunt.md description measures %d, limit %d" % (len(text), limit))

# 4 · Reddit: el tope de titulo es de la plataforma (300), no se declara en el fichero
for m in re.finditer(r"^\*\*Title:\*\* (.+)$", reddit, re.M):
    checked += 1
    if len(m.group(1)) > 300:
        bad.append("reddit-pack.md title measures %d, limit 300: %s" % (len(m.group(1)), m.group(1)[:60]))

# suelo de poblacion: un censo vacio significa que el formato cambio, NO que este limpio
if checked < 12:
    print("check-launch-counts: 2 · COULD NOT CHECK — only %d counts found "
          "(expected >=12); the material's format may have changed" % checked, file=sys.stderr)
    sys.exit(2)

if bad:
    for b in bad:
        print("check-launch-counts: ⛔ %s" % b, file=sys.stderr)
    print("check-launch-counts: 1 · %d count(s) do not match out of %d checked" % (len(bad), checked), file=sys.stderr)
    sys.exit(1)

print("check-launch-counts: ✔ %d declared counts match the actual text" % checked)
sys.exit(0)
PYCHECK
