#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-cosign-contract-verdicts.sh — prueba que check-cosign-contract.sh distingue sus TRES
# respuestas, no dos (C15-P6).
#
# ⛔ POR QUÉ ESTA BATERÍA Y NO UNA LÍNEA EN EL GATE. Hasta el 2026-08-18 el gate salía **1**
#    cuando cosign no estaba en PATH. El argumento escrito era bueno —el pipeline de firma lo
#    necesita, así que no es un «skip»— y la codificación era mala: un 1 dice «el contrato de
#    firma está ROTO» cuando lo cierto es que **no se ha podido probar**. Un gate que contesta
#    «roto» a una ausencia enseña a ignorarlo, y un gate ignorado deja de ser un control.
#
#    La política NO cambió: 2 sigue siendo no-cero y sigue tumbando el job igual. Lo que cambió es
#    que el código dice CUÁL de las dos cosas pasó. Esta batería es lo que impide que vuelva a
#    fundirse, porque la diferencia entre 1 y 2 no la ve ningún lint de forma.
#
# ⛔ LOS SEÑUELOS VAN FUERA DE /tmp, Y ESO ES UNA MEDIDA, NO UNA PREFERENCIA: en esta caja `/tmp`
#    está montado NOEXEC, así que un `cosign` de mentira creado allí devuelve **126** y la celda
#    mediría el montaje en vez del gate. Van bajo el árbol de trabajo.
#
# Salida: 0 todas pasan · 1 alguna falla · 2 no se ha podido montar el banco de pruebas.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-cosign-contract.sh"
[ -r "$GATE" ] || {
	echo "test-cosign-contract-verdicts: ⛔ NO HE PODIDO MIRAR: no existe $GATE" >&2
	exit 2
}

BANCO="$(mktemp -d "$RAIZ/.cosign-verdicts-XXXXXX")" || {
	echo "test-cosign-contract-verdicts: ⛔ NO HE PODIDO MIRAR: no se pudo crear el banco" >&2
	exit 2
}
trap 'rm -rf "$BANCO"' EXIT

# ── El PATH sin cosign ────────────────────────────────────────────────────────────────────
# No basta con «esta caja no tiene cosign»: la celda tiene que valer también donde SÍ lo hay.
# Se construye un PATH con enlaces sólo a lo que el gate usa antes de decidir.
SIN="$BANCO/sin-cosign"
mkdir -p "$SIN"
for h in dirname awk sed mktemp cat rm grep printf env sh bash; do
	ruta="$(command -v "$h" 2>/dev/null)" || continue
	ln -sf "$ruta" "$SIN/$h" 2>/dev/null || true
done
if [ -n "$(command -v cosign 2>/dev/null)" ] && [ -e "$SIN/cosign" ]; then
	echo "test-cosign-contract-verdicts: ⛔ NO HE PODIDO MIRAR: el PATH señuelo trajo cosign" >&2
	exit 2
fi

pasan=0
fallan=0

comprobar() {
	etiqueta="$1"
	esperado="$2"
	obtenido="$3"
	if [ "$obtenido" -eq "$esperado" ]; then
		printf '  ok    %-58s rc=%s\n' "$etiqueta" "$obtenido"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-58s rc=%s (quiere %s)\n' "$etiqueta" "$obtenido" "$esperado"
		fallan=$((fallan + 1))
	fi
}

# ── 1 · Sin binario que probar ⇒ NO HE PODIDO MIRAR (2), nunca «roto» ni «limpio» ─────────
PATH="$SIN" bash "$GATE" >"$BANCO/1.log" 2>&1
comprobar "cosign ausente del PATH es NO HE PODIDO MIRAR" 2 "$?"

# ── 2 · OLIVARES_COSIGN_BIN apuntando a algo no ejecutable ⇒ tampoco se pudo mirar ────────
printf 'no soy ejecutable\n' >"$BANCO/no-exec"
chmod -x "$BANCO/no-exec"
OLIVARES_COSIGN_BIN="$BANCO/no-exec" bash "$GATE" >"$BANCO/2.log" 2>&1
comprobar "OLIVARES_COSIGN_BIN no ejecutable es NO HE PODIDO MIRAR" 2 "$?"

# ── 3 · Un cosign que SÍ está y da otra versión ⇒ eso sí es un hallazgo (1) ───────────────
# ⛔ LA DIRECCIÓN QUE HACE VÁLIDAS A LAS DOS DE ARRIBA. Sin ella, un gate que devolviera 2
#    SIEMPRE las pasaría las dos y no distinguiría nada.
mkdir -p "$BANCO/otra-version"
cat >"$BANCO/otra-version/cosign" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = "version" ] && { echo "GitVersion:    v9.9.9"; exit 0; }
exit 0
EOF
chmod +x "$BANCO/otra-version/cosign"
PATH="$BANCO/otra-version:$SIN" bash "$GATE" >"$BANCO/3.log" 2>&1
comprobar "cosign presente con versión divergente es un HALLAZGO" 1 "$?"

# ── 4 · Un cosign que resuelve y no arranca ⇒ hallazgo (1), no ausencia ───────────────────
# Es el caso medido el 2026-07-25: un shim de contención sin binario detrás. Está PRESENTE, así
# que no es «no he podido mirar»: es un cosign inutilizable, que es un hecho medido.
mkdir -p "$BANCO/roto"
cat >"$BANCO/roto/cosign" <<'EOF'
#!/usr/bin/env bash
echo "shim sin binario detrás" >&2
exit 127
EOF
chmod +x "$BANCO/roto/cosign"
PATH="$BANCO/roto:$SIN" bash "$GATE" >"$BANCO/4.log" 2>&1
comprobar "cosign presente pero inutilizable es un HALLAZGO" 1 "$?"

# ── 5 · El mensaje NOMBRA la respuesta, o el operador no puede actuar sobre ella ──────────
if grep -q "UNVERIFIED" "$BANCO/1.log" 2>/dev/null; then
	printf '  ok    %-58s\n' "el 2 se explica con UNVERIFIED en el mensaje"
	pasan=$((pasan + 1))
else
	printf '  FALLA %-58s\n' "el 2 salió sin decir que NO se pudo probar"
	fallan=$((fallan + 1))
fi
# Y no puede leerse como un pase: un rc distinto de cero con un mensaje que suene a «skip» es
# exactamente lo que esta separación vino a evitar.
if grep -qiE "\bskipp?(ed|ing)\b" "$BANCO/1.log" 2>/dev/null; then
	printf '  FALLA %-58s\n' "el mensaje del 2 se lee como un SKIP"
	fallan=$((fallan + 1))
else
	printf '  ok    %-58s\n' "el mensaje del 2 no se lee como un skip"
	pasan=$((pasan + 1))
fi

echo "test-cosign-contract-verdicts: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
