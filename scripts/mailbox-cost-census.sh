#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# CENSO DEL COSTE DE LOS BUZONES. Existe por la misma razon que `misspell-census.sh`:
# una cifra copiada a mano en prosa envejece en silencio, y estas crecen POR HORA.
#
# El predicado, escrito, porque dos medidas honestas discreparon sobre el MISMO commit
# (76,029 GiB y 71,359 GiB, 2026-08-28) y lo que fallaba no era la medida sino la
# definicion:
#
#   tamano   = `git cat-file -s <REV>:<fichero>`      bytes DESCOMPRIMIDOS de esa version
#   versiones= `git rev-list --count <REV> -- <fichero>`  commits que TOCAN el fichero
#   bruto    = tamano x versiones                     COTA SUPERIOR, no disco
#
# `bruto` NO es lo que ocupa: el pack deltifica estas versiones casi por completo. Es lo
# que ocupan SUELTAS, que es el estado real con `gc.auto=0`. Se llama cota y no consumo
# a proposito.
#
# Sale 1 si las filas no suman su total: una particion que no cuadra no es una particion.
set -uo pipefail

REV="${1:-HEAD}"
cd "$(dirname "$0")/.." || exit 2

if ! git cat-file -e "${REV}^{commit}" 2>/dev/null; then
  echo "mailbox-cost-census: '$REV' no es un commit en este clon" >&2; exit 2
fi

mapfile -t BUZONES < <(git ls-tree -r --name-only "$REV" -- sessions/status/inbox 2>/dev/null | grep '\.md$' | sort)
if [ "${#BUZONES[@]}" -eq 0 ]; then
  echo "mailbox-cost-census: no hay buzones bajo sessions/status/inbox en $REV" >&2; exit 2
fi

SHA="$(git rev-parse "$REV")"
printf 'mailbox-cost-census: REV=%s (%s) · %s buzon(es)\n\n' "$REV" "${SHA:0:12}" "${#BUZONES[@]}"
printf '  %-34s %12s %10s %14s\n' 'buzon' 'bytes' 'versiones' 'bruto (GiB)'
printf '  %-34s %12s %10s %14s\n' '----' '----' '----' '----'

tot_bruto=0; tot_ver=0; suma_filas=0
for f in "${BUZONES[@]}"; do
  sz="$(git cat-file -s "${REV}:${f}" 2>/dev/null)" || continue
  n="$(git rev-list --count "$REV" -- "$f")"
  bruto=$(( sz * n ))
  tot_bruto=$(( tot_bruto + bruto )); tot_ver=$(( tot_ver + n )); suma_filas=$(( suma_filas + bruto ))
  printf '  %-34s %12s %10s %14s\n' "$(basename "$f")" "$sz" "$n" \
    "$(awk -v b="$bruto" 'BEGIN{printf "%.3f", b/1073741824}')"
done

printf '  %-34s %12s %10s %14s\n' 'TOTAL' '' "$tot_ver" \
  "$(awk -v b="$tot_bruto" 'BEGIN{printf "%.3f", b/1073741824}')"

# La comprobacion que hace que el censo valga: las filas tienen que sumar el total.
if [ "$suma_filas" -ne "$tot_bruto" ]; then
  echo "mailbox-cost-census: ⛔ las filas NO suman el total ($suma_filas != $tot_bruto)" >&2
  exit 1
fi

echo
echo "  bruto = cota SUPERIOR (tamano actual x versiones), no disco: el pack deltifica"
echo "  estas versiones casi por completo; sueltas ocupan enteras, que es el estado con"
echo "  gc.auto=0. Para el disco real: git count-objects -v"
