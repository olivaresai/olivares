#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cloud-availability-claims.sh — a public surface may not promise the HOSTED cloud in the
# present tense while the pricing canon still says that tier is `launch-gated`.
#
# ⛔ POR QUÉ EXISTE, medido el 2026-08-28 y con las dos frases delante.
#
#   docs/launch/faq-objections.md  — «that Cloud Standard tier is the hosted option: v1 is not
#                                     self-host-only … launching together with the public release.
#                                     No waitlist, no early-access gating.»
#   docs/launch/blog-launch-post.md — «Olivares AI Cloud Standard is also offered ($199/mo) for teams
#                                     who want us to run it — same product, our infrastructure.»
#
# Y al mismo tiempo, en el árbol: `deploy/aws/main.tf` declara «⛔ NEVER APPLIED», el backend de cloud
# sólo tiene un E2E LOCAL, y `cloud/staging/polar-sandbox.md` dice que **sólo una URL desplegada
# probará la entrega alojada**.
#
# ⛔⛔ LO QUE HABÍA EN SU LUGAR ERA UN COMENTARIO, Y UN COMENTARIO NO HA PARADO NUNCA UN PUSH.
# `faq-objections.md` lleva «S-12 gate: … Do not publish it anywhere before that flip». Eso (a) es
# prosa, no un control, y (b) cubre el PRECIO, que es la mitad barata — la afirmación de
# DISPONIBILIDAD, que es la que un desconocido lee el día del lanzamiento, no tenía nada.
#
# ⭐ LA CONDICIÓN SE DERIVA, NO SE COPIA, y es todo el diseño de este gate. El sujeto es
# `availability:` de `cloud.standard` en `an internal design note (not shipped)`. Mientras valga `launch-gated`, la
# afirmación en presente es falsa y el gate la rechaza; **en cuanto el apply real lo voltee, el gate se
# apaga solo.** No hay fecha, ni cifra, ni allowlist que alguien tenga que acordarse de tocar — que es
# exactamente por lo que el comentario S-12 iba a caducar en silencio.
#
# TRES RESPUESTAS: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
# El 2 incluye «no encuentro el canon», «no encuentro cloud.standard» y «no sé leer su availability»:
# una condición ILEGIBLE no es «no aplica». Deny-closed.

set -uo pipefail
LC_ALL=C; export LC_ALL

say()    { printf '%s\n' "$*"; }
fail()   { say "check-cloud-availability-claims: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cloud-availability-claims: ⛔ NO HE PODIDO MIRAR — $*" >&2; exit 2; }

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || cannot "no existe $RAIZ"

CANON="${OLIVARES_PRICING_CANON:-design/PRICING-CANON.md}"
[ -r "$CANON" ] || cannot "no leo $CANON — sin el canon no sé si el tier está abierto o cerrado"
command -v python3 >/dev/null 2>&1 || cannot "no hay python3"

# ── 1 · La condición, leída del canon ────────────────────────────────────────────────────────
# Se busca la clave `cloud.standard:` y, DENTRO de su bloque (por indentación), su `availability:`.
# Un `availability:` de otra oferta no vale: el sujeto es el tier alojado, no el catálogo.
AVAIL="$(python3 - "$CANON" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8").read().split("\n")
start = None
indent = None
for i, l in enumerate(text):
    m = re.match(r"^(\s*)cloud\.standard:\s*(#.*)?$", l)
    if m:
        start, indent = i, len(m.group(1))
        break
if start is None:
    print("NOKEY"); raise SystemExit(0)
for l in text[start + 1:]:
    if l.strip() and (len(l) - len(l.lstrip())) <= indent:
        break                      # salimos del bloque de cloud.standard
    m = re.match(r"^\s*availability:\s*([A-Za-z0-9._-]+)", l)
    if m:
        print(m.group(1)); raise SystemExit(0)
print("NOAVAIL")
PY
)" || cannot "el parser del canon falló"

case "$AVAIL" in
  NOKEY)   cannot "el canon no contiene la clave 'cloud.standard' — no puedo derivar la condición" ;;
  NOAVAIL) cannot "'cloud.standard' no declara 'availability' — una condición ausente no es un permiso" ;;
  "")      cannot "'availability' de cloud.standard salió vacío" ;;
esac

if [ "$AVAIL" != "launch-gated" ]; then
  say "check-cloud-availability-claims: OK — cloud.standard availability=$AVAIL (ya no está launch-gated);"
  say "                                 el gate se apaga solo, como está diseñado."
  exit 0
fi

# ── 2 · Las superficies públicas ─────────────────────────────────────────────────────────────
# Enumeradas a propósito. Un `find` ancho arrastraría `design/` y `sessions/`, que son internos y
# donde esta discusión TIENE que poder escribirse en presente sin que el gate la rechace.
mapfile -t SURF < <(
  { git ls-files 'docs/launch/*.md' 'README.md' 'README.*.md' \
                 'docs-site/src/content/docs/**/*.md' 'docs-site/src/content/docs/**/*.mdx' 2>/dev/null || true; } | sort -u
)
[ "${#SURF[@]}" -ge 8 ] || cannot "sólo ${#SURF[@]} superficie(s) públicas — el barrido no llega, y un denominador vacío hace perfecto cualquier numerador"

# ── 3 · Los patrones, cada uno con su razón ──────────────────────────────────────────────────
# NO es un regex ingenioso: es una lista, porque una lista se puede discutir y un regex no. Cada
# entrada es una forma MEDIDA en el árbol o su traducción directa en la familia README.
PATRONES=(
  'no waitlist'                                   # la promesa más fuerte: niega la puerta entera
  'no early-access gating'                        # su gemela
  'launching together with the public release'    # fija la disponibilidad a una fecha
  'ships both ways'                               # afirma que las dos vías existen HOY
  'sin lista de espera'
  'ohne Warteliste'
  "sans liste d'attente"
  'без листа ожидания'
  '順番待ちなし'
  '无需等候名单'
)
# Las dos formas que sólo son un hallazgo JUNTO a «cloud» u «hosted» en la misma línea: solas son
# ambiguas y un gate que las persiga a secas produce ruido y acaba en `|| true`.
PATRONES_CONTEXTO=(
  'is also offered'
  'hosted option'
  # ⛔ a repository gate — EL VOCABULARIO ERA DEMASIADO ESTRECHO Y LO DEMOSTRO UNA PLANTADA. the planner puso
  #    «Cloud Standard is offered today» en un borrador y el gate salio rc 0: cazaba la forma
  #    «is ALSO offered» y no «is offered», que es la misma promesa sin el adverbio. Un gate de
  #    vocabulario no protege una AFIRMACION, protege las maneras de escribirla que alguien
  #    enumero — y cada forma que falta es una puerta abierta que parece cerrada.
  'is offered'
  'is available today'
  'is available now'
  'open in early availability'
  'generally available'
  'you can buy'
  'checkout is open'
)

# ⛔ DOS PASADAS DE `grep`, NO UNA POR FICHERO Y PATRÓN. La primera versión hacía un bucle
# anidado —891 superficies × 12 patrones ≈ 10 700 procesos— y no terminaba en dos minutos. Un gate
# que tarda más que el push que protege acaba en `|| true`, que es como mueren los gates de esta
# casa. `grep -f` acepta la lista entera de una vez y `-H` la etiqueta con su fichero.
TMPD="$(mktemp -d "${TMPDIR:-/tmp}/cloudclaims.XXXXXX")" || cannot "no puedo crear el temporal"
trap 'rm -rf "$TMPD"' EXIT
printf '%s\n' "${PATRONES[@]}"          > "$TMPD/p.txt"
printf '%s\n' "${PATRONES_CONTEXTO[@]}" > "$TMPD/pc.txt"

hallazgos=0
while IFS= read -r hit; do
  [ -n "$hit" ] || continue
  say "  ⛔ ${hit%%:*}: afirmación de disponibilidad alojada en presente, con cloud.standard launch-gated"
  say "       $(printf '%s' "$hit" | cut -c1-140)"
  hallazgos=$((hallazgos + 1))
done < <(grep -nHF -f "$TMPD/p.txt" -- "${SURF[@]}" 2>/dev/null || true)

# Sólo cuentan si la MISMA línea habla de cloud/hosted. Solas producen ruido, y un gate ruidoso
# se desactiva entero.
# ⛔ Y ESA REGLA ES LO QUE HACE SEGURO AMPLIAR EL VOCABULARIO, no una cortesía: «generally
#    available» aparece HOY en una superficie —`where-it-fits-with-your-idp.md:16`, sobre lo que
#    los HYPERSCALERS ofrecen— y esa línea no dice cloud ni hosted, así que no dispara. Medido
#    sobre las 949 superficies antes de añadirla: si la frase fuese al array incondicional
#    (`PATRONES`), ese texto legítimo saldría como hallazgo y el gate acabaría en `|| true`.
#    La `-i` es de a repository gate: «Is Offered» y «IS OFFERED» son la misma promesa.
while IFS= read -r hit; do
  [ -n "$hit" ] || continue
  # ⛔ EL SUJETO TAMBIEN VA EN MINUSCULAS, y lo cazo mi propia bateria: la `-i` del grep hacia
  #    insensible el PATRON, pero este `case` seguia siendo `*[Cc]loud*` — que casa «cloud» y
  #    «Cloud» y NO «CLOUD». Una plantada en mayusculas salia rc 0: media insensibilidad es
  #    una puerta abierta con aspecto de cerrada, que es el defecto entero de a repository gate otra vez.
  hit_min="$(printf '%s' "$hit" | tr '[:upper:]' '[:lower:]')"
  case "$hit_min" in
    *cloud*|*hosted*)
      # ⛔ EL MENSAJE NOMBRA LA FRASE QUE CASO, no dos de las nueve del array: un aviso que cita
      #    «is also offered»/«hosted option» cuando lo que salto fue «open in early availability»
      #    manda al lector a buscar un texto que no esta en su linea.
      say "  ⛔ ${hit%%:*}: frase de disponibilidad alojada junto a cloud/hosted, con el tier launch-gated"
      say "       $(printf '%s' "$hit" | cut -c1-140)"
      hallazgos=$((hallazgos + 1)) ;;
  esac
done < <(grep -nHFi -f "$TMPD/pc.txt" -- "${SURF[@]}" 2>/dev/null || true)

if [ "$hallazgos" -gt 0 ]; then
  say ""
  say "check-cloud-availability-claims: $hallazgos afirmación(es) de disponibilidad ALOJADA en presente," >&2
  say "  y design/PRICING-CANON.md dice cloud.standard availability=launch-gated." >&2
  say "  El tier está DECIDIDO; lo que no está es desplegado — deploy/aws/main.tf dice «NEVER APPLIED»." >&2
  say "  Redacta la frase condicionada al flip, o voltea el availability del canon cuando el apply" >&2
  say "  sea real. Este gate se apaga SOLO cuando eso pase: no hay nada que recordar." >&2
  exit 1
fi

say "check-cloud-availability-claims: OK — ${#SURF[@]} superficie(s) públicas, ninguna promete el cloud alojado en presente (availability=$AVAIL)."
exit 0
