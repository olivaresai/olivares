#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-program-anchors.sh — a programme that is not linked from where sessions actually read
# WILL be lost, and this repository has already paid for it twice.
#
# WHY THIS EXISTS. On 2026-08-07 said of the licensing/entitlement programme: "esto lo
# comenté hace semanas con otra sesión pero al parecer no se documentó, analizó correctamente y
# demás", and then "que no se vuelva a 'olvidar' o 'perder'". It is the same shape as the TOPS
# derivation, which four relays carried and the fifth silently dropped — recorded in
# the integrator's relay of 2026-08-07, §5, as a failure of METHOD, not of memory.
#
# THE DIAGNOSIS THAT MATTERS: the loss was never a lack of analysis. Both times the analysis
# existed and was good. What was missing is that nothing linked it from the file every session
# opens first. Documentation that nobody is routed to is indistinguishable from documentation
# that does not exist.
#
# So this gate keeps a small, EXPLICIT list of programme documents anchored in ESTADO-PROYECTO.md
# — the living memory between sessions — and refuses a push that leaves one orphaned. It checks
# two directions, because either alone is a half-measure:
#
#   the document EXISTS      — an anchor pointing at a deleted file routes a session to nothing
#   the anchor EXISTS        — a document nobody links to is the failure being prevented
#
# WHAT IT DELIBERATELY DOES NOT DO: it does not read the CONTENT of either side. It cannot tell a
# living programme from a stale one, and pretending otherwise would be the "gate that reads as
# coverage" this repository keeps finding. It answers exactly one question — is this still
# reachable from where people look — and says so.
#
# Exit 0 = CLEAN. Exit 1 = an anchor or a document is missing (both named).
# Exit 2 = COULD NOT LOOK (no git, no repository, unreadable index). NOT a clean verdict.
set -u
set -o pipefail

say() { printf '%s\n' "$*"; }
cannot_look() {
	say "check-program-anchors: COULD NOT LOOK — $1" >&2
	say "check-program-anchors: this is not a clean verdict." >&2
	exit 2
}

command -v git >/dev/null 2>&1 || cannot_look "no git on PATH"
ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || cannot_look "not inside a git working tree"
cd "$ROOT" || cannot_look "cannot enter $ROOT"

INDEX="ESTADO-PROYECTO.md"
[ -f "$INDEX" ] || cannot_look "$INDEX is missing; it is the living memory this gate anchors to"

# The list is HAND-WRITTEN on purpose, and that is a limitation stated rather than hidden: there
# is no derivable property that says "this document is a programme". Adding a line here is the
# deliberate act of saying "this must not be lost". The cost of forgetting to add one is that
# this gate does not protect it — which is exactly today's situation for everything not listed.
#
# THREE ENTRIES, AND EACH ONE IS HERE BECAUSE SOMETHING WENT WRONG WITHOUT IT (2026-08-08):
#
#   PROGRAMA-LICENCIAS…      the programme that was lost once for want of a link. The original.
#   FORMA-DEL-PRODUCTO…      the product shape SIGNED on 2026-08-08 — the cards, the add-on
#                            groups, the prices. It had ZERO anchors on the board while a June
#                            logbook entry still read "Nada decidido; precios y corte =", so
#                            a session searching the board for prices concluded nothing had been
#                            decided and set out to decide it again.
#   PRICING-CANON.md         the SIGNED INVARIANTS — `commercial_crl_scope: forbidden`,
#                            `remote_kill_switch: false`. Measured the same day: zero anchors,
#                            while `main` was violating the first of them through a five-link
#                            chain nobody had joined up. A document that decides what the product
#                            may never do, reachable from nowhere, is the worst case of all.
PROGRAMMES="
design/PROGRAMA-LICENCIAS-ENTITLEMENT-Y-DISTRIBUCION.md
design/FORMA-DEL-PRODUCTO-DEFINITIVA-2026-08-08.md
design/PRICING-CANON.md
design/DECISION-COMERCIAL-2026-07-27.md
design/REGISTRO-DECISIONES-2026-08-01.md
design/DECISIONES-PERDIDAS-2026-08.md
design/audits/2026-08-06-codex-sol-ultra-suscripciones-sweep.md
design/audits/2026-08-06-codex-sol-ultra-licencias-sweep-2.md
design/PLAN-COMPLETITUD-RELEASE-2026-08-16.md
design/BACKLOG-COMPLETITUD-2026-08-16.md
design/CRITERIOS-RELEASE-2026-08-16.md
design/REPARTO-CARRILES-2026-08-16.md
design/DECISIONES-PENDIENTES-FRAN-2026-08-17.md
"

missing_doc=0
missing_anchor=0
checked=0

for doc in $PROGRAMMES; do
	[ -n "$doc" ] || continue
	checked=$((checked + 1))
	if [ ! -f "$doc" ]; then
		say "check-program-anchors: MISSING DOCUMENT — $doc" >&2
		say "  $INDEX points at it and it is not there: a session following that link finds nothing." >&2
		missing_doc=$((missing_doc + 1))
		continue
	fi
	# Fixed-string search: a path is not a pattern, and `.` in a filename must not match anything.
	#
	# AND IT LOOKS AT WHAT A READER WOULD SEE. A mention inside an HTML comment satisfied this
	# check while routing nobody: `<!-- an internal design note (not shipped) -->` is invisible in the rendered
	# board, so the document was "anchored" to a line no human reads. Measured 2026-08-08 by an
	# adversarial contrast. Same family as the two mistakes this repository made today in the
	# other direction — counting the prose that DESCRIBES a defect as the defect — and it lands
	# here as the mirror: prose that LOOKS like an anchor and is not one.
	#
	# HTML comments are stripped before searching, single and multi-line. Nothing else is: a path
	# inside a fenced code block still routes a reader, and a footnote is still a link.
	# ⛔ NOT A PIPE. `sed … | grep -q` under `set -o pipefail` returns 141 ON SUCCESS: grep -q
	# closes the pipe at the first match, sed dies of SIGPIPE, and pipefail reports the
	# pipeline as failed. The first cut of this fix did exactly that and turned all three
	# anchored documents into ORPHANED on a correct tree — the gate crying at everything, which
	# is the failure mode its own header warns about. It is a catalogued trap in this repository
	# and it bit while fixing something else. The visible half is read into a variable, and grep
	# gets its own status.
	# TWO EXPRESSIONS, and the single-line one has to come first. A sed RANGE looks for its end
	# starting at the NEXT line, so `\|<!--|,\|-->|d` on a one-line comment never finds its close
	# and deletes to END OF FILE. Measured: an anchor added below such a comment vanished and the
	# document read as orphaned. The first expression removes complete one-line comments; the
	# second removes genuinely multi-line ones.
	visible="$(sed -e 's/<!--[^>]*-->//g' -e '\|<!--|,\|-->|d' "$INDEX")" \
		|| cannot_look "could not read $INDEX through the comment filter"
	# NO PIPE AT ALL, and the first two attempts at this both had one. `X | grep -q` under
	# `set -o pipefail` returns 141 ON SUCCESS — grep -q closes the pipe at the first match, the
	# writer dies of SIGPIPE, and pipefail reports the pipeline as failed. Swapping `sed | grep`
	# for `printf | grep` changed nothing, because the pipe was the problem, not sed. Shell
	# pattern matching needs no subprocess and has no status to get wrong. Fixed-string by
	# construction: `case` globs the pattern, so the path is quoted and `.` stays a dot.
	case "$visible" in
	*"$doc"*) ;;
	*)
		say "check-program-anchors: ORPHANED — $doc is not linked from $INDEX" >&2
		say "" >&2
		say "  consequence: the document exists and nothing routes anyone to it. That is how this" >&2
		say "               programme was lost the first time, and how the TOPS derivation was lost" >&2
		say "               across five relays — not for want of analysis, for want of a link." >&2
		say "  repair     : add a pointer to $doc in the HEADER of $INDEX, where a session reads" >&2
		say "               before anything else. Not in a section it has to scroll to." >&2
		missing_anchor=$((missing_anchor + 1))
		;;
	esac
done

if [ "$checked" -eq 0 ]; then
	# An empty list is not a pass: it means this gate is grading nothing while looking green.
	cannot_look "the programme list is empty; this gate would certify nothing"
fi

if [ "$missing_doc" -ne 0 ] || [ "$missing_anchor" -ne 0 ]; then
	say "check-program-anchors: DIRTY — $missing_doc missing document(s), $missing_anchor orphaned." >&2
	exit 1
fi

say "check-program-anchors: CLEAN — $checked programme document(s) exist and are linked from $INDEX."
exit 0
