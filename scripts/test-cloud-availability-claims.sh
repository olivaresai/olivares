#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-cloud-availability-claims.sh — la batería de `check-cloud-availability-claims.sh`.
#
# ⛔ POR QUÉ VA PRIMERO Y SIN `|| true`. Que el gate no bloquee puede ser una decisión; que su batería
# no bloquee sería no tener batería. Y este gate tiene un modo de fallo silencioso propio: **si deja de
# encontrar la clave del canon, o si su lista de patrones deja de casar, sale 2 o 0 y parece sano.**
# Un gate cuyo sujeto ha desaparecido no protege nada y no se queja.
#
# Cada celda comprueba un CÓDIGO DE SALIDA EXACTO, nunca «distinto de cero»: un caso que acepta
# cualquier cosa menos 0 aprueba también un rc=2 de entorno, y entonces la batería testifica sobre la
# caja en vez de sobre el gate.
#
# Salidas: 0 todas verdes · 1 alguna celda roja · 2 no he podido montar la batería.

set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
SUT="$RAIZ/scripts/check-cloud-availability-claims.sh"
[ -x "$SUT" ] || { echo "test-cloud-availability-claims: ⛔ NO HE PODIDO MIRAR: no ejecuto $SUT" >&2; exit 2; }
CANON_REAL="$RAIZ/design/PRICING-CANON.md"
[ -r "$CANON_REAL" ] || { echo "test-cloud-availability-claims: ⛔ NO HE PODIDO MIRAR: no leo el canon real" >&2; exit 2; }

BANCO="$(mktemp -d "${TMPDIR:-/tmp}/testcloud.XXXXXX")" || exit 2
trap 'rm -rf "$BANCO"' EXIT
pasa=0; falla=0

celda() { # celda <nombre> <rc esperado> <fichero de canon>
  local nombre="$1" esperado="$2" canon="$3" rc
  OLIVARES_CLONE="$RAIZ" OLIVARES_PRICING_CANON="$canon" bash "$SUT" >"$BANCO/out" 2>&1
  rc=$?
  if [ "$rc" -eq "$esperado" ]; then
    printf '  ok    %-56s rc=%s\n' "$nombre" "$rc"; pasa=$((pasa+1))
  else
    printf '  FAIL  %-56s rc=%s (esperado %s)\n' "$nombre" "$rc" "$esperado"; falla=$((falla+1))
    sed 's/^/          /' "$BANCO/out" | tail -4
  fi
}

# ── 1 · CONTROL POSITIVO POR MUTACIÓN, y NO por el estado del árbol ──────────────────────────
# ⛔ LA PRIMERA VERSIÓN DE ESTA CELDA MEDÍA EL ÁRBOL: exigía que el gate encontrara las 18
#    afirmaciones reales. Funcionó exactamente una vez — hasta que se condicionaron, en el mismo
#    commit que las arregla. Un control positivo que depende de que el defecto SIGA AHÍ se apaga
#    el día que alguien hace el trabajo, y entonces el gate se queda sin prueba de que ve su
#    sujeto justo cuando ya nadie lo va a mirar.
#
#    Ahora se PLANTA la afirmación en una superficie real y se comprueba que dispara. El árbol
#    limpio deja de ser un problema: pasa a ser la otra mitad del control.
VICTIMA="$RAIZ/docs/launch/README.md"
if [ ! -f "$VICTIMA" ]; then
  echo "test-cloud-availability-claims: ⛔ NO HE PODIDO MIRAR: no existe $VICTIMA para plantar el mutante" >&2
  exit 2
fi
cp "$VICTIMA" "$BANCO/victima.orig" || exit 2
restaurar() { cp "$BANCO/victima.orig" "$VICTIMA" 2>/dev/null; rm -rf "$BANCO"; }
trap restaurar EXIT

# (a) árbol tal cual: tiene que salir LIMPIO. Si no, es que quedó una afirmación sin condicionar.
OLIVARES_CLONE="$RAIZ" bash "$SUT" >"$BANCO/limpio" 2>&1
rc_limpio=$?
if [ "$rc_limpio" -eq 0 ]; then
  printf '  ok    %-56s rc=0\n' "el árbol de hoy no promete el cloud alojado"; pasa=$((pasa+1))
else
  printf '  FAIL  %-56s rc=%s (esperado 0)\n' "el árbol de hoy no promete el cloud alojado" "$rc_limpio"
  grep -E '^  ⛔' "$BANCO/limpio" | head -4 | sed 's/^/          /'
  falla=$((falla+1))
fi

# (b) con la afirmación PLANTADA, tiene que disparar. Éste es el control que prueba que el gate
#     sigue viendo su sujeto, y no depende de que el árbol esté sucio.
printf '\nCloud Standard ships with the public release, no waitlist.\n' >> "$VICTIMA"
OLIVARES_CLONE="$RAIZ" bash "$SUT" >"$BANCO/mutado" 2>&1
rc_mut=$?
cp "$BANCO/victima.orig" "$VICTIMA"
if [ "$rc_mut" -eq 1 ]; then
  printf '  ok    %-56s rc=1\n' "una afirmación PLANTADA se caza (el gate ve su sujeto)"; pasa=$((pasa+1))
else
  printf '  FAIL  %-56s rc=%s (esperado 1)\n' "una afirmación PLANTADA se caza (el gate ve su sujeto)" "$rc_mut"
  printf '        El gate ha dejado de ver la forma que existe para vigilar: eso es peor que un rojo.\n'
  falla=$((falla+1))
fi

# (b-bis) a repository gate — UNA FRASE POR CADA FORMA NUEVA, y no es ceremonia: el hueco que abrio este
#         encargo fue exactamente esto — el array tenia «is ALSO offered» y no «is offered», y
#         una plantada de the planner paso en verde. Un caso por forma es lo unico que impide que
#         la siguiente variante vuelva a entrar por la misma puerta.
#         ⛔ Cada frase se planta CON «cloud» en la misma linea a proposito: son patrones de
#         CONTEXTO, y sin el sujeto no deben disparar — eso lo prueba (b-ter).
for frase in \
  'Olivares AI Cloud Standard is offered to every tenant.' \
  'The hosted cloud is available today for new accounts.' \
  'Our cloud offering is available now without a waitlist.' \
  'Cloud Standard is open in early availability for teams.' \
  'The hosted cloud tier is generally available.' \
  'You can buy Cloud Standard from the hosted console.' \
  'Cloud Standard checkout is open on the hosted plan.' \
  'OLIVARES CLOUD STANDARD IS OFFERED TODAY.'
do
  printf '\n%s\n' "$frase" >> "$VICTIMA"
  OLIVARES_CLONE="$RAIZ" bash "$SUT" >"$BANCO/mut-frase" 2>&1
  rc_f=$?
  cp "$BANCO/victima.orig" "$VICTIMA"
  corta="$(printf '%s' "$frase" | cut -c1-44)"
  if [ "$rc_f" -eq 1 ]; then
    printf '  ok    %-56s rc=1\n' "planta: ${corta}"; pasa=$((pasa+1))
  else
    printf '  FAIL  %-56s rc=%s (esperado 1)\n' "planta: ${corta}" "$rc_f"
    printf '        Esa forma de prometer el cloud alojado NO la ve el gate.\n'
    falla=$((falla+1))
  fi
done

# (b-ter) EL CONTROL NEGATIVO, que es lo que separa un gate util de uno ruidoso: la MISMA frase
#         sin sujeto cloud/hosted NO debe disparar. Sin esto, ampliar el vocabulario habria
#         convertido en hallazgo un texto legitimo —«generally available» sobre lo que ofrecen
#         los HYPERSCALERS, en where-it-fits-with-your-idp.md:16— y un gate ruidoso se apaga.
printf '\nThe registries the hyperscalers have made generally available are consumed read-only.\n' >> "$VICTIMA"
OLIVARES_CLONE="$RAIZ" bash "$SUT" >"$BANCO/mut-neg" 2>&1
rc_neg=$?
cp "$BANCO/victima.orig" "$VICTIMA"
if [ "$rc_neg" -eq "$rc_limpio" ]; then
  printf '  ok    %-56s rc=%s\n' "sin sujeto cloud/hosted NO dispara (control negativo)" "$rc_neg"; pasa=$((pasa+1))
else
  printf '  FAIL  %-56s rc=%s (esperado %s)\n' "sin sujeto cloud/hosted NO dispara" "$rc_neg" "$rc_limpio"
  printf '        El vocabulario ampliado esta cazando texto legitimo: eso acaba en || true.\n'
  falla=$((falla+1))
fi

# (c) y el control de que la restauración funcionó: si no, la celda (a) de la próxima corrida
#     mediría el mutante de ésta.
if diff -q "$BANCO/victima.orig" "$VICTIMA" >/dev/null 2>&1; then
  printf '  ok    %-56s\n' "la víctima queda restaurada byte a byte"; pasa=$((pasa+1))
else
  printf '  FAIL  %-56s\n' "la víctima NO quedó restaurada — el árbol queda sucio"; falla=$((falla+1))
fi

# ── 2 · MUTANTE que APAGA el gate: el availability de cloud.standard deja de ser launch-gated.
python3 - "$CANON_REAL" "$BANCO/flip.md" <<'PY'
import io, re, sys
lines = io.open(sys.argv[1], encoding="utf-8").read().split("\n")
i = next(k for k, l in enumerate(lines) if re.match(r"^\s*cloud\.standard:", l))
ind = len(lines[i]) - len(lines[i].lstrip())
n = 0
for k in range(i + 1, len(lines)):
    if lines[k].strip() and (len(lines[k]) - len(lines[k].lstrip())) <= ind:
        break
    if "availability:" in lines[k]:
        lines[k] = re.sub(r"availability:\s*\S+", "availability: launch", lines[k]); n += 1; break
# ⛔ CONTROL DE INYECCIÓN: un mutante que no se aplica hace que el gate parezca ciego cuando no lo es.
assert n == 1, f"el mutante NO se inyectó (n={n})"
io.open(sys.argv[2], "w", encoding="utf-8").write("\n".join(lines))
PY
[ -s "$BANCO/flip.md" ] || { echo "test-cloud-availability-claims: ⛔ NO HE PODIDO MIRAR: el mutante 1 no se escribió" >&2; exit 2; }
celda "mutante · availability volteado ⇒ el gate se apaga solo" 0 "$BANCO/flip.md"

# ── 3 · La clave desaparece: NO HE PODIDO MIRAR, jamás un verde.
sed 's/^  cloud\.standard:/  cloud.renombrado:/' "$CANON_REAL" > "$BANCO/nokey.md"
celda "sin la clave cloud.standard ⇒ no he podido mirar" 2 "$BANCO/nokey.md"

# ── 4 · La clave está pero sin availability: tampoco es un permiso.
python3 - "$CANON_REAL" "$BANCO/noavail.md" <<'PY'
import io, re, sys
lines = io.open(sys.argv[1], encoding="utf-8").read().split("\n")
i = next(k for k, l in enumerate(lines) if re.match(r"^\s*cloud\.standard:", l))
ind = len(lines[i]) - len(lines[i].lstrip())
out, n = [], 0
for k, l in enumerate(lines):
    if k > i and n == 0 and "availability:" in l and (len(l) - len(l.lstrip())) > ind:
        n += 1; continue
    out.append(l)
assert n == 1, f"el mutante 3 NO se inyectó (n={n})"
io.open(sys.argv[2], "w", encoding="utf-8").write("\n".join(out))
PY
celda "sin availability ⇒ no he podido mirar" 2 "$BANCO/noavail.md"

# ── 5 · El canon no existe: la tercera respuesta, otra vez.
celda "sin canon ⇒ no he podido mirar" 2 "$BANCO/no-existe.md"

echo "test-cloud-availability-claims: $pasa pasadas, $falla fallidas"
[ "$falla" -eq 0 ] || exit 1
exit 0
