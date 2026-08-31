#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-inbox-headings.sh — the mailbox files have exactly five section headings, and a
# sixth breaks the document for anyone who walks it by heading.
#
# WHY THIS EXISTS, AND WHY IT IS A CHECK AND NOT A FIX. The same artefact appeared THREE
# times on 2026-08-04: a message body quoting the literal name of a section at the start of
# a line — `## Atendidos` — which markdown then reads as a real section. The first two times
# it was repaired by hand and came back. The third time it came back through a rebase that
# took the other side. Repairing the case does not close the class.
#
# The consequence is not cosmetic. Acknowledgement, archiving and every script that splits
# these files by `^## ` sees a section that nobody wrote; on 2026-08-04 one such split
# deleted 240 lines and seven messages before it was caught.
#
# THE RULE, also written in each file's "Cómo escribir aquí": inside a message body there is
# no `##`. Only `###` delimits entries and only `##` delimits file sections. To quote a
# section name, indent it or put it mid-sentence — never at the start of a line.
#
# IT CAME BACK A FOURTH TIME, and the reason matters for whoever maintains this. The literal
# lives inside a message that DESCRIBES the bug by quoting it. Each lane holds its own copy of
# the mailbox, so a rebase that takes the other side replays the unrepaired text. Repairing it
# here does not stop that; running this check does. Treat a recurrence as normal traffic, not
# as a regression: fix the line, keep going. The check is the control, not the repair.
#
# Exit 0 = every mailbox has exactly its five structural headings.
# Exit 1 = a mailbox has a heading that is not one of them; the offender is named.
# Exit 2 = the check could not look (missing directory, unreadable file). NOT the same as
#          clean, and it must never be reported as such.
set -u

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "check-inbox-headings: not inside a git repository — could not look" >&2
	exit 2
}
INBOX="$ROOT/sessions/status/inbox"

[ -d "$INBOX" ] || {
	echo "check-inbox-headings: '$INBOX' does not exist — could not look" >&2
	exit 2
}

# The five headings a mailbox is allowed to carry, in the order the protocol fixes.
ALLOWED='^## (Cómo escribir aquí|Qué SÍ mandar aquí|Qué NO|Pendientes|Atendidos)$'

rc=0
checked=0
for f in "$INBOX"/*.md; do
	[ -e "$f" ] || continue
	[ -r "$f" ] || {
		echo "check-inbox-headings: '$f' is not readable — could not look" >&2
		exit 2
	}
	checked=$((checked + 1))
	while IFS= read -r line; do
		n="${line%%:*}"
		text="${line#*:}"
		printf '%s\n' "$text" | grep -qE "$ALLOWED" && continue
		echo "check-inbox-headings: ${f#"$ROOT"/}:$n — heading that is not one of the five:" >&2
		echo "    $text" >&2
		echo "    A message body must not start a line with '##'. Indent the quote, use bold," >&2
		echo "    or write the section name mid-sentence." >&2
		rc=1
	done < <(grep -n '^## ' "$f" || true)
	# ⛔ Y QUE LAS CINCO ESTEN, no solo que no sobre ninguna. Hasta hoy este gate comprobaba una
	# sola direccion —que toda `##` fuese de la lista— y su mensaje decia «five structural
	# headings each», que es una afirmacion que el codigo no hacia. Medido el 2026-08-19: un
	# `sed` mio convirtio las CINCO a negrita por accidente y este gate respondio **OK**,
	# diciendo literalmente que cada buzon tenia sus cinco. Un buzon sin sus secciones deja de
	# ser un buzon: nadie sabe donde escribir ni donde estan los pendientes.
	faltan=""
	for h in "Cómo escribir aquí" "Qué SÍ mandar aquí" "Qué NO" "Pendientes" "Atendidos"; do
		grep -qxF "## $h" "$f" || faltan="$faltan\n    ## $h"
	done
	if [ -n "$faltan" ]; then
		echo "check-inbox-headings: ${f#"$ROOT"/} — le FALTAN secciones estructurales:" >&2
		printf '%b\n' "$faltan" >&2
		echo "    Un buzon sin sus cinco secciones no dice donde escribir ni que esta pendiente." >&2
		rc=1
	fi
done

# ── El SELLO de las entradas que ESTE push anade ────────────────────────────────
#
# ⛔ QUE DEFECTO CIERRA, medido el 2026-08-28: **21 cabeceras**, repartidas por seis buzones de
#    sesion distintos (tres de ellos con 3, 3 y 2), llevan la cadena `__SELLO__` LITERAL, sin
#    sustituir. **El buzon se ordena por ese sello**, asi que esas entradas son inordenables:
#    quien recorra el buzon por fecha no sabe donde ponerlas.
#
#    Y la asimetria es la razon de que este control viva aqui: `publish-inbox.sh` **SI** exige el
#    sello —rechaza una cabecera sin el, y tambien una con sello adelantado— pero este gate **NO
#    lo comprobaba**. La guarda vivia en un camino y no en el otro, asi que quien escribe el buzon
#    directo, sin el guion, se la saltaba. Un control que solo protege al que ya hace lo correcto
#    no protege nada.
#
# ⛔ SOLO SE EXIGE EN LO QUE EL PUSH ANADE, y no es indulgencia: las 21 historicas son asientos
#    ajenos ya publicados. Un gate que enrojeciera por ellas bloquearia a los cinco carriles por
#    deuda que su autor no puede reparar desde su rama — que es exactamente el fallo por el que
#    `lint:unpublished-work` se retiro de este hook DOS veces. Se mira el diff contra el tronco.
TRONCO="${OLIVARES_INBOX_TRUNK:-origin/main}"
BASE="$(git merge-base "$TRONCO" HEAD 2>/dev/null || true)"
if [ -z "$BASE" ]; then
	echo "check-inbox-headings: sin base contra '$TRONCO' — no compruebo sellos (could not look)" >&2
else
	# `-U0` para que el diff no arrastre cabeceras de CONTEXTO como si fueran anadidas.
	sin_sello=0
	while IFS= read -r linea; do
		[ -n "$linea" ] || continue
		cab="${linea#+}"
		# Sello valido: `### AAAA-MM-DDThh:mm` (con o sin `Z`), al principio de la cabecera.
		printf '%s
' "$cab" | grep -qE '^### [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}' && continue
		# La forma antigua, solo fecha, tambien se acepta: existe en el historico y ordena igual.
		printf '%s
' "$cab" | grep -qE '^### [0-9]{4}-[0-9]{2}-[0-9]{2} ' && continue
		echo "check-inbox-headings: entrada NUEVA sin sello ordenable:" >&2
		echo "    $cab" >&2
		sin_sello=$((sin_sello + 1))
	done < <(git diff -U0 "$BASE"...HEAD -- "$INBOX" 2>/dev/null | grep '^+### ' || true)
	if [ "$sin_sello" -gt 0 ]; then
		echo "check-inbox-headings: $sin_sello cabecera(s) que este push anade no llevan sello" >&2
		echo "    Un buzon se ORDENA por el sello: sin el, la entrada no tiene sitio." >&2
		echo "    Publica con 'scripts/publish-inbox.sh', que lo exige y lo comprueba contra el reloj." >&2
		rc=1
	fi
fi

[ "$checked" -gt 0 ] || {
	echo "check-inbox-headings: no mailbox files found — could not look" >&2
	exit 2
}

if [ "$rc" -eq 0 ]; then
	echo "check-inbox-headings: OK — $checked mailbox(es), five structural headings each"
fi
exit "$rc"
