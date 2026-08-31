# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# act-id.sh — a repository gate. El identificador del ACTO, generado por el gancho una vez por CORRIDA.
#
# ⛔ SE SOBRESCRIBE SIEMPRE, Y ESA ES TODA LA PIEZA. La version anterior escribia
# `OLIVARES_ACT_ID="${OLIVARES_ACT_ID:-...}"`, que CONSERVA lo heredado: dos corridas bajo un
# entorno que ya traiga la variable comparten acto, y el sello de la primera pasa por fresco en la
# segunda sin que nadie haya vuelto a traer el ref — justo lo que el id existe para impedir. Lo
# señalo el contraste `sol max` (A-04, tercera vuelta), y no lo mataba mi banco porque asignaba dos
# ids distintos A MANO en vez de dejar que el gancho los generase.
#
# ⚠ Y el nonce lleva nanosegundos y azar A PROPOSITO: con `pid + epoch` en segundos, dos corridas
# del mismo proceso dentro del mismo segundo producen el MISMO id. Un nonce que puede repetirse no
# es un nonce.
olivares_nuevo_act_id() {
	local head
	head="$(git rev-parse HEAD 2>/dev/null || echo nohead)"
	printf '%s-%s-%s-%s\n' "$head" "$$" "$(date -u +%s%N)" "${RANDOM}${RANDOM}"
}
