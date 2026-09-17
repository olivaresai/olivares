#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Regression guard for the EU CRA readiness pack. Pure POSIX sh, no
# network. It checks only that the repo carries the expected CRA readiness
# document, release/update pointers, and a fresh re-verification date.
set -eu

ROOT="${CRA_PACK_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
CRA="$ROOT/docs/CRA-READINESS.md"
UPGRADE="$ROOT/docs/UPGRADE-AND-ROLLBACK.md"
GORELEASER="$ROOT/.goreleaser.yaml"

fail() { echo "cra-pack: FAIL: $1" >&2; exit 1; }
warn() { echo "cra-pack: WARN: $1" >&2; }

# ⛔ TRES RESPUESTAS, NO DOS. `blind` marca una comprobación que NO SE PUDO HACER — no una
#    que falló. Sin `date -d` de GNU (el defecto en macOS/BSD) este guion decía
#    «FAIL: GNU date with -d is required», que se lee como «el paquete CRA está roto» cuando lo
#    cierto es que la FRESCURA no se miró y todo lo demás sí. Un punto ciego no es un defecto y
#    tampoco es un verde.
#
#    No sale de inmediato a propósito: el bloque de la fecha está EN MEDIO, así que abortar ahí se
#    llevaría por delante las comprobaciones de UPGRADE y goreleaser, que sí se pueden hacer. Se
#    anota, se sigue, y el veredicto final es PARCIAL con rc=2 — el mismo idioma que el gate pesado
#    usa cuando no alcanza a Postgres.
BLIND=""
blind() { BLIND="$1"; echo "cra-pack: ⛔ NO HE PODIDO MIRAR: $1" >&2; }

[ -f "$CRA" ] || fail "missing docs/CRA-READINESS.md"

grep -q '^### Template: early warning (≤24h)$' "$CRA" \
  || fail "missing early-warning template heading"
grep -q '^### Template: notification (≤72h)$' "$CRA" \
  || fail "missing notification template heading"
grep -q '^### Template: final report (two distinct triggers)$' "$CRA" \
  || fail "missing final-report template heading"
grep -q '^| Release line | First placed on EU market | End of support |$' "$CRA" \
  || fail "missing support-period declarations table header"

# The date may carry a parenthetical after it ("2026-07-28 (Article 14/16 mechanics …)");
# anchoring at end-of-line broke silently when that context was added.
reverify_date="$(sed -n 's/^Last re-verification: \([0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]\).*$/\1/p' "$CRA" | head -n 1)"
[ -n "$reverify_date" ] || fail "missing Last re-verification: YYYY-MM-DD line"

if date -u -d '1970-01-01' +%s >/dev/null 2>&1; then
  reverify_epoch="$(date -u -d "$reverify_date" +%s 2>/dev/null)" \
    || fail "Last re-verification date is unparseable: $reverify_date"
  roundtrip="$(date -u -d "$reverify_date" +%F 2>/dev/null)" \
    || fail "Last re-verification date is unparseable: $reverify_date"
  [ "$roundtrip" = "$reverify_date" ] \
    || fail "Last re-verification date is unparseable: $reverify_date"

  now_epoch="$(date -u +%s)"
  [ "$reverify_epoch" -le "$now_epoch" ] \
    || fail "Last re-verification date is in the future: $reverify_date"

  age_days="$(((now_epoch - reverify_epoch) / 86400))"
  [ "$age_days" -le 400 ] \
    || fail "Last re-verification date is older than 400 days: $reverify_date"
  if [ "$age_days" -gt 180 ]; then
    warn "Last re-verification date is older than 180 days: $reverify_date"
  fi
else
  blind "GNU date with -d is not available on this host, so the CRA re-verification freshness was NOT checked. This says nothing about whether it is fresh; the rest of the pack is still verified below."
fi

[ -f "$UPGRADE" ] || fail "missing docs/UPGRADE-AND-ROLLBACK.md"
# Number-agnostic: the section index shifts when the doc grows (it already moved 9→13
# and this guard rotted silently — the heading TEXT is the contract, not its position).
grep -qE '^## [0-9]+\. Security updates — CRA statement$' "$UPGRADE" \
  || fail "missing Security updates — CRA statement heading"

[ -f "$GORELEASER" ] || fail "missing .goreleaser.yaml"
release_block="$(awk '
  /^release:$/ { in_release=1 }
  /^changelog:$/ { in_release=0 }
  in_release { print }
' "$GORELEASER")"
printf '%s\n' "$release_block" | grep -q '^  header: |$' \
  || fail ".goreleaser.yaml release block has no header template"
printf '%s\n' "$release_block" | grep -qi 'support period' \
  || fail ".goreleaser.yaml release header does not mention the support period"

if [ -n "$BLIND" ]; then
  echo "cra-pack: PARCIAL — document, templates, table and release/update pointers verified; FRESHNESS NOT CHECKED ($BLIND)" >&2
  exit 2
fi
echo "cra-pack: OK (document present, templates/table/freshness checked, release/update pointers present)"
