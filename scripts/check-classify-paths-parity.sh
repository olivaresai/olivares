#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Las dos listas de rutas de bitacora de `mainline-ci.yml` tienen que decir LO MISMO.
#
# ⛔ EL PROPIO WORKFLOW LO EXIGE, y hasta hoy no habia quien lo comprobara. Junto a `paths-ignore`:
#
#   «The list IS `classify`'s own, copied from its `case` rather than invented here.
#    If a new documentation path is added, the two move together or this fires again
#    on a prose commit.»
#
# Un invariante escrito y sin testigo es una costumbre. Medido el 2026-08-30: `classify` eximia
# CUATRO familias y `paths-ignore` TRES — `docs/ai-context/**` entro solo en `classify` el
# 2026-08-23. Consecuencia: un push de solo `docs/ai-context/**` SI crea corrida, `classify` dice
# `false` y las 41 patas gateadas se saltan ⇒ **una corrida entera que no mide nada**.
#
# Veredictos: 0 = las dos listas coinciden · 1 = difieren (se nombra cada lado) · 2 = NO HE PODIDO
# MIRAR (no encuentro una de las dos listas: es lo que pasa si alguien renombra el job o el bloque,
# y entonces callarse seria peor que gritar).
set -u
RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
F="${OLIVARES_CI_FILE:-$RAIZ/.github/workflows/mainline-ci.yml}"
[ -r "$F" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no leo $F." >&2; exit 2; }

# `paths-ignore:` del disparador de push. Se normaliza `**` -> `*` para poder comparar con el `case`.
# ⛔ LA SANGRIA SE MIDE, NO SE MIRA. Escribi este extractor contra 6 espacios porque asi lo vi en
# pantalla — y lo que estaba viendo era MI PROPIO `sed 's/^/  /'` sumado a los 4 reales. Dos veces
# seguidas, incluido un `cat -A` que tambien pase por el sed. Los valores de hoy, medidos con
# `match($0,/^ */)`: `paths-ignore` 4, sus items 6, el `case` 12. Se toleran variaciones para no
# atarse a un formateo.
# ⛔ Y SE SALTAN LOS COMENTARIOS DENTRO DE LA LISTA. La primera version paraba en el primer `#`, y
# este fichero comenta CADA decision: al añadir el item con su razon encima, el extractor dejaba de
# verlo y el testigo se quedaba en rojo para siempre — un falso positivo permanente, que es peor que
# no tener testigo porque se aprende a ignorarlo.
IGN=$(awk '/^ *paths-ignore:/{f=1;next} f&&/^ *#/{next} f&&/^ *- /{sub(/^ *- /,"");gsub(/^.|.$/,"",$0);print;next} f&&NF{exit}' "$F" \
      | sed "s/\*\*$/*/" | sort -u)
# el `case` de `classify`: la rama que declara las rutas de bitacora
CAS=$(awk '/journal_only=false/{exit} /^ *[a-z].*\)[ ]*;;/{print}' "$F" \
      | sed 's/)[ ]*;;.*//' | tr '|' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -v '^$' | sort -u)

[ -n "$IGN" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no encuentro la lista \`paths-ignore\`." >&2; exit 2; }
[ -n "$CAS" ] || { echo "check-classify-paths-parity: NO HE PODIDO MIRAR: no encuentro el \`case\` de \`classify\`." >&2; exit 2; }

echo "check-classify-paths-parity: paths-ignore=$(printf '%s\n' "$IGN" | wc -l) · classify=$(printf '%s\n' "$CAS" | wc -l)"
SOLO_CAS=$(comm -13 <(printf '%s\n' "$IGN") <(printf '%s\n' "$CAS"))
SOLO_IGN=$(comm -23 <(printf '%s\n' "$IGN") <(printf '%s\n' "$CAS"))
if [ -z "$SOLO_CAS" ] && [ -z "$SOLO_IGN" ]; then
  echo "check-classify-paths-parity: limpio — las dos listas dicen lo mismo."; exit 0
fi
echo "check-classify-paths-parity: ⛔ HALLAZGO — las dos listas han DERIVADO." >&2
# ⛔ CON COMILLAS. Sin ellas, `printf ... $SOLO_CAS` hace EXPANSION DE RUTAS y `docs/ai-context/*`
# sale convertido en los once ficheros de ese directorio: el hallazgo deja de ser legible justo
# cuando importa. Medido al calibrar este guion.
[ -n "$SOLO_CAS" ] && { echo "  solo en el \`case\` de classify (exime, pero la corrida SI se crea):" >&2
                        printf '%s\n' "$SOLO_CAS" | sed 's/^/      /' >&2; }
[ -n "$SOLO_IGN" ] && { echo "  solo en \`paths-ignore\` (no crea corrida, pero classify lo trataria como codigo):" >&2
                        printf '%s\n' "$SOLO_IGN" | sed 's/^/      /' >&2; }
echo "  El workflow lo prohibe en su propio comentario: «the two move together or this fires" >&2
echo "  again on a prose commit»." >&2
exit 1
