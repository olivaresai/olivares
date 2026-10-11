#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-launch-image-refs.sh — ninguna imagen del material de lanzamiento apunta a un fichero ausente.
#
# ⛔ POR QUÉ ES UN GATE Y NO UNA SONDA DE UNA VEZ. C-27 de los criterios de release pide «0
#    referencias a imágenes inexistentes», y hasta hoy se medía a mano. Una comprobación que sólo
#    existe en la cabeza de quien la corrió no vuelve a correr: el 2026-08-18 el material tenía NUEVE
#    rotas y nadie lo sabía porque nada lo miraba.
#
# ⛔⛔ Y EXCLUYE LOS CODE SPANS, que es la mitad del trabajo. Mi primera medida contó como rota
#     `![...](./assets/*.png)` dentro de un `` ` `` en `reddit-pack.md`, donde el texto CITA la
#     sintaxis para explicar una regla —«Images must be real, hosted captures — not placeholders»—.
#     No es una referencia: es prosa sobre referencias. Publiqué «nueve rotas» y una era mi propio
#     regex leyendo documentación como si fuera markup.
#
# ⚠ Y LA AUSENCIA DEL FICHERO NO ES EL ÚNICO FALLO POSIBLE, aunque sea el que esto mide. Una imagen
#   que EXISTE puede no sostener lo que su `alt` afirma —`killswitch-light.png` decía «No active
#   stops» bajo un pie que anunciaba el paro ENGAGED—, y eso ningún guion lo decide. Este gate
#   contesta «¿está el fichero?»; el otro juicio es de quien mira la imagen, y su registro vive en
#   `docs/launch/assets/README.md`.
#
# Salida: la lista de rotas · rc 0 verde · 1 hay rotas nuevas · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-launch-image-refs: ⛔ COULD NOT CHECK: missing $RAIZ" >&2
	exit 2
}
DIR="${OLIVARES_LAUNCH_DIR:-docs/launch}"
DECLARADAS="${OLIVARES_LAUNCH_IMG_DEBT:-docs/launch/broken-image-refs.tsv}"

[ -d "$DIR" ] || {
	echo "check-launch-image-refs: ⛔ COULD NOT CHECK: missing $DIR/" >&2
	exit 2
}

python3 - "$DIR" "$DECLARADAS" <<'PY'
import os, re, sys, glob

dir_, debt_path = sys.argv[1], sys.argv[2]
source_files = sorted(glob.glob(os.path.join(dir_, '**', '*.md'), recursive=True))
# CONTROL POSITIVO: cero ficheros no es «cero rotas», es no haber mirado.
if not source_files:
    print(f'check-launch-image-refs: ⛔ COULD NOT CHECK: no .md files in {dir_}/', file=sys.stderr)
    raise SystemExit(2)

# Los code spans se BORRAN antes de buscar, para que la prosa que CITA la sintaxis no cuente.
SPAN = re.compile(r'`[^`\n]*`')
IMG = re.compile(r'!\[([^\]]*)\]\(([^)\s]+)')

rotas, total = [], 0
for f in source_files:
    texto = SPAN.sub('', open(f, encoding='utf8', errors='replace').read())
    for m in IMG.finditer(texto):
        alt, ref = m.group(1), m.group(2)
        total += 1
        if ref.startswith(('http://', 'https://', 'data:')):
            continue
        destino = os.path.normpath(os.path.join(os.path.dirname(f), ref))
        if not os.path.exists(destino):
            rotas.append((f, ref, alt))

declaradas = set()
if os.path.exists(debt_path):
    for linea in open(debt_path, encoding='utf8'):
        linea = linea.rstrip('\n')
        if not linea.strip() or linea.lstrip().startswith('#'):
            continue
        campos = linea.split('\t')
        if len(campos) < 3 or not campos[2].strip():
            print(f'check-launch-image-refs: ⛔ COULD NOT CHECK: line without a reason in {debt_path}: {linea!r}',
                  file=sys.stderr)
            raise SystemExit(2)
        declaradas.add((campos[0], campos[1]))

nuevas = [r for r in rotas if (r[0], r[1]) not in declaradas]
muertas = sorted(declaradas - {(r[0], r[1]) for r in rotas})

print(f'check-launch-image-refs: {total} image reference(s) · {len(rotas)} broken · '
      f'{len(declaradas)} declared')
rc = 0
for f, ref, alt in nuevas:
    print(f'check-launch-image-refs: ⛔ {f} → {ref}', file=sys.stderr)
    print(f'    alt: {alt[:100]}', file=sys.stderr)
    rc = 1
if nuevas:
    print('  Capture the image, or declare the missing references in ' + debt_path + ' with a reason; an unexplained absence is'
          ' with a reason; an unexplained absence is indistinguishable from an oversight.', file=sys.stderr)
for f, ref in muertas:
    print(f'check-launch-image-refs: ⛔ declared as missing but now exists: {f} → {ref}', file=sys.stderr)
    rc = 1
if muertas:
    print('  Remove it from the backlog in this commit: a list that never shrinks keeps reporting gaps'
          ' that have already been filled.', file=sys.stderr)
if rc == 0:
    print('check-launch-image-refs: ✔ broken references exactly match the declared list')
raise SystemExit(rc)
PY
