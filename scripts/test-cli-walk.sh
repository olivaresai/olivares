#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería de scripts/cli-walk.mjs. Cada caso CONSTRUYE un binario falso con la forma que afirma;
# ninguno lee el binario real, y ninguno da por bueno un veredicto que no haya provocado.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="$HERE/scripts/cli-walk.mjs"
BASH_BIN="$(command -v bash)"
# EL SCRATCH SE ELIGE, NO SE IMPONE. El contraste `sol max` de senalo que fijar el scratch bajo
# ROOT_DIR sin mirar rechaza en una raiz no escribible —un checkout de CI read-only— y ademas ANULA
# un TMPDIR bueno que el runner pudiera aportar. Asi que se prueban candidatos en orden y gana el
# primero que sirva de verdad: escribible Y ejecutable, que es lo que estos casos necesitan. Si
# ninguno sirve, es 2 NO PUDE MIRAR y no un rojo. El orden respeta al llamante primero.
elige_scratch() {
	local base d
	for base in "${TMPDIR:-}" "$HERE/.cli-walk-tmp" /var/tmp; do
		[ -n "$base" ] || continue
		mkdir -p "$base" 2>/dev/null || continue
		d="$(TMPDIR="$base" mktemp -d 2>/dev/null)" || continue
		printf '#!/bin/sh\nexit 0\n' > "$d/.probe" 2>/dev/null
		chmod +x "$d/.probe" 2>/dev/null
		if "$d/.probe" >/dev/null 2>&1; then rm -f "$d/.probe"; printf '%s' "$d"; return 0; fi
		rm -rf "$d" 2>/dev/null
	done
	return 1
}
W="$(elige_scratch)" || W=""
if [ -z "$W" ]; then
	echo "test-cli-walk: 2 COULD NOT LOOK — no usable scratch directory (tried TMPDIR=${TMPDIR:-unset}, $HERE/.cli-walk-tmp, /var/tmp): no writable executable location" >&2
	exit 2
fi
trap 'rm -rf "$W"' EXIT

# Todos los casos CONSTRUYEN un binario senuelo y lo EJECUTAN. Si $W cae en un montaje
# noexec —lo normal para /tmp en estos contenedores— cli-walk no arranca ni uno y los
# quince salen rc=2. Medido el 2026-08-23: se reportaba como «3 pasan, 12 fallan», o sea
# un problema de ENTORNO disfrazado de doce hallazgos del producto, que es peor que no
# correr — manda a depurar doce defectos que no existen. Un banco que no puede mirar lo
# DICE. Con TMPDIR bajo un montaje ejecutable los quince pasan.
# ⛔ Y LA SONDA DE ARRIBA NO BASTA, porque no es representativa del sujeto. Lo probo el contraste
# `sol max` de esta misma rama: el SUT es `scripts/cli-walk.mjs`, que necesita NODE, y una sonda que
# solo demuestra que arranca un `#!/bin/sh` pasa en una caja sin node. Medido el 2026-08-23 quitando
# node del PATH: `0 pasan, 15 fallan`, todos con rc=127 -- QUINCE rojos falsos, que es exactamente la
# enfermedad que este bloque existe para curar, sólo que peor que la original. Una sonda se calibra
# contra lo que el sujeto hace, no contra lo que es comodo comprobar.
printf 'process.exit(0)\n' > "$W/.probe.mjs" 2>/dev/null
if ! command -v node >/dev/null 2>&1 || ! node "$W/.probe.mjs" >/dev/null 2>&1; then
	echo "test-cli-walk: 2 COULD NOT LOOK — subject is $SUT and node cannot execute in this environment" >&2
	exit 2
fi
rm -f "$W/.probe.mjs"
pasa=0; falla=0; infra=0
check() { # <nombre> <rc esperado> <rc obtenido> [texto que DEBE aparecer] [salida]
	local n="$1" e="$2" o="$3" t="${4:-}" s="${5:-}"
	# ⛔ 127 NO ES UN VEREDICTO. Es «command not found»: node roto a mitad, un `grep` que no esta.
	# El contraste de lo cazo — la sonda pasa y aun asi la infraestructura puede caerse DESPUES,
	# y sin esto cada caida se contaba como hallazgo del producto y el push se rechazaba por ella.
	[ "$o" = 127 ] && infra=$((infra+1))
	if [ "$e" != "$o" ]; then printf '  FAIL  %-56s rc=%s (expected %s)\n' "$n" "$o" "$e"; falla=$((falla+1)); return; fi
	if [ -n "$t" ] && ! grep -qF "$t" <<<"$s"; then printf '  FAIL  %-56s does not report «%s»\n' "$n" "$t"; falla=$((falla+1)); return; fi
	printf '  ok    %-56s rc=%s\n' "$n" "$o"; pasa=$((pasa+1))
}

# binario_falso <fichero> <ayuda_rota:0|1> <rc_bandera> <cuerpo_extra>
binario_falso() {
	local f="$1" rota="$2" rcflag="$3" extra="${4:-}"
	cat >"$f" <<FAKE
#!/usr/bin/env bash
ultimo="\${@: -1}"
if [ "\$ultimo" = "--help" ]; then
  if [ "$rota" = "1" ] && [ "\$1" = "roto" ]; then echo "sin usage"; exit 0; fi
  if [ \$# -eq 1 ]; then
    printf 'Comandos:\n  alfa    primero\n  roto    segundo\n  ls      tercero\n\nUsage:\n  x [command]\n'
  else
    # El senuelo DECLARA --server: sin eso el recorrido lo clasifica como LOCAL y no lo lleva al
    # motor, que es justo la distincion que la seccion LOCAL vs RED comprueba mas abajo.
    printf 'Usage:\n  x %s\n\nFlags:\n      --server string   base\n' "\$1"
  fi
  exit 0
fi
case "\$ultimo" in --esta-bandera-no-existe-jamas) exit $rcflag ;; esac
$extra
exit 0
FAKE
	chmod +x "$f"
}

echo "CLEAN — a binary meeting its own contract passes"
binario_falso "$W/ok" 0 2
s="$(OLIVARES_CLI_BIN="$W/ok" node "$SUT" 2>&1)"; rc=$?
check "a consistent binary is clean" 0 "$rc" "CLEAN" "$s"
check "discovers commands from the binary itself" 0 "$rc" "command(s) discovered" "$s"
check "SAYS that no engine was traversed" 0 "$rc" "NO OLIVARES_CLI_BASE" "$s"

echo "RED — contract violations are NAMED"
binario_falso "$W/ayuda" 1 2
s="$(OLIVARES_CLI_BIN="$W/ayuda" node "$SUT" 2>&1)"; rc=$?
check "help without 'Usage:' is a finding" 1 "$rc" "[help] roto" "$s"
binario_falso "$W/flag" 0 1
s="$(OLIVARES_CLI_BIN="$W/flag" node "$SUT" 2>&1)"; rc=$?
check "unknown flag that does NOT return 2 is a finding" 1 "$rc" "the binary promises 2" "$s"
check "names the returned exit code" 1 "$rc" "returned 1" "$s"

echo "ENGINE — 404 is a finding; usage errors are NOT coverage"
binario_falso "$W/e404" 0 2 'case "$1" in ls) echo "GET /v1/m/x: 404 not found" ; exit 6 ;; esac'
s="$(OLIVARES_CLI_BIN="$W/e404" OLIVARES_CLI_BASE=https://127.0.0.1:1 node "$SUT" 2>&1)"; rc=$?
check "an engine 404 is a finding" 1 "$rc" "[route] ls" "$s"
binario_falso "$W/e401" 0 2 'case "$1" in ls) echo "401 unauthorized" ; exit 3 ;; esac'
s="$(OLIVARES_CLI_BIN="$W/e401" OLIVARES_CLI_BASE=https://127.0.0.1:1 node "$SUT" 2>&1)"; rc=$?
check "a 401 is NOT a finding: the guard works" 0 "$rc" "CLEAN" "$s"
check "scope is declared" 0 "$rc" "NETWORK command(s) reached" "$s"

echo "LOCAL vs NETWORK — a command without --server is not a traversal failure"
#    Medido el 2026-08-19: la primera version pasaba --server a TODO mandato de lectura, y
#    `audit ls`, `connector ls`, `keys ls` y `migrate status` respondian «unknown flag: --server».
#    Son LOCALES. Contarlos como «les faltan argumentos» inventaba la causa: el argumento SOBRABA.
cat >"$W/mixto" <<'FAKE2'
#!/usr/bin/env bash
ultimo="${@: -1}"
if [ "$ultimo" = "--help" ]; then
  if [ $# -eq 1 ]; then printf 'Comandos:
  ls      de red
  status  local

Usage:
  x [command]
'; exit 0; fi
  case "$1" in
    ls)     printf 'Usage:
  x ls

Flags:
      --server string   base
' ;;
    status) printf 'Usage:
  x status

Flags:
      --data-dir string   local
' ;;
    *)      printf 'Usage:
  x %s
' "$1" ;;
  esac
  exit 0
fi
case "$ultimo" in --esta-bandera-no-existe-jamas) exit 2 ;; esac
case "$1" in
  ls)     echo "401 unauthorized"; exit 3 ;;
  status) echo "Error: unknown flag: --server"; exit 2 ;;
esac
exit 0
FAKE2
chmod +x "$W/mixto"
s="$(OLIVARES_CLI_BIN="$W/mixto" OLIVARES_CLI_BASE=https://127.0.0.1:1 node "$SUT" 2>&1)"; rc=$?
check "local command is NOT counted as a traversal usage error" 0 "$rc" "1 read command(s) are LOCAL" "$s"
check "network command DOES reach the engine" 0 "$rc" "1 of 1 NETWORK command(s) reached" "$s"

echo "⛔ THE CASE THIS SCRIPT PREVENTS FROM RECURRING"
#    Todos los mandatos de lectura salen con usage error: NO llegaron al motor. Contar eso como
#    cobertura fue el error medido el 2026-08-19 («0 hallazgos en 33», 25 de ellos sin llegar).
cat >"$W/vacuo" <<'FAKE3'
#!/usr/bin/env bash
ultimo="${@: -1}"
if [ "$ultimo" = "--help" ]; then
  if [ $# -eq 1 ]; then printf 'Comandos:
  ls      de red

Usage:
  x [command]
'; exit 0; fi
  printf 'Usage:
  x %s

Flags:
      --server string   base
' "$1"; exit 0
fi
case "$ultimo" in --esta-bandera-no-existe-jamas) exit 2 ;; esac
case "$1" in ls) exit 2 ;; esac
exit 0
FAKE3
chmod +x "$W/vacuo"
s="$(OLIVARES_CLI_BIN="$W/vacuo" OLIVARES_CLI_BASE=https://127.0.0.1:1 node "$SUT" 2>&1)"; rc=$?
check "with an engine but ZERO commands reaching it, returns 2, not 0" 2 "$rc" "COULD NOT LOOK" "$s"

echo "COULD NOT LOOK — never a pass"
s="$(node "$SUT" 2>&1)"; rc=$?
check "missing OLIVARES_CLI_BIN returns 2" 2 "$rc" "missing OLIVARES_CLI_BIN" "$s"
s="$(OLIVARES_CLI_BIN="$W/no-existe" node "$SUT" 2>&1)"; rc=$?
check "a nonexistent binary returns 2" 2 "$rc" "does not exist" "$s"
printf '#!/usr/bin/env bash\nprintf "Usage:\\n  x\\n"\nexit 0\n' >"$W/sinhijos"; chmod +x "$W/sinhijos"
s="$(OLIVARES_CLI_BIN="$W/sinhijos" node "$SUT" 2>&1)"; rc=$?
check "zero discovered commands returns 2, not 0" 2 "$rc" "does not demonstrate a clean result" "$s"

echo
echo "cli-walk self-test: $pasa passed, $falla failed"
if [ "$infra" -gt 0 ]; then
	echo "test-cli-walk: 2 COULD NOT LOOK — $infra case(s) returned 127 (command not found): host issue, not product issue" >&2
	exit 2
fi
[ "$falla" -eq 0 ]
