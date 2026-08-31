#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-prep-gate-impact.sh — the battery for check-prep-gate-impact.sh.
#
# It builds throwaway repositories with FAKE prep gates, so it never runs the real ones and
# never depends on which PRs happen to be open today. Every scenario is a property of the
# tool, not of the queue.
#
# THE PROPERTY THAT MATTERS MOST is the one that nearly shipped a false finding: the mirror
# must be CLEAN between gates. A branch blob can CREATE a file main does not have; restoring
# with `git checkout -- .` alone leaves it behind, the NEXT gate's baseline runs against that
# contamination and comes back red, and the tool then blames the tree ("already red on main")
# for dirt it made itself. Measured on 2026-08-21: two gates were reported stale that were
# CLEAN in a fresh mirror.
set -uo pipefail
export LC_ALL=C
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SUJETO="$HERE/check-prep-gate-impact.sh"
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf 'ok   %-56s %s\n' "$1" "${2:-}"; }
bad()  { FAIL=$((FAIL+1)); printf 'FAIL %-56s %s\n' "$1" "${2:-}"; }
[ -x "$SUJETO" ] || { echo "test-prep-gate-impact: CANNOT LOOK — $SUJETO missing or not executable" >&2; exit 2; }

# escenario <n-gates-verdes> ; imprime el directorio del repo listo
escenario() {
	local d; d="$(mktemp -d "${TMPDIR:-/tmp}/prepimp.XXXXXX")"
	mkdir -p "$d/scripts/lib" "$d/design"
	cp "$SUJETO" "$d/scripts/"
	# El sujeto CARGA scripts/lib/git-env.sh y rehusa (rc=2) si no lo encuentra — es
	# fail-closed a proposito: un saneador ausente es «no he podido aislar». Un banco que
	# no lo copiara mediria esa negativa en vez de la propiedad, y dos casos de esta
	# bateria PASARIAN por la razon equivocada, porque esperan rc=2.
	cp "$HERE/lib/git-env.sh" "$d/scripts/lib/"
	# Relleno: el sujeto REHUSA por debajo de sus suelos, asi que un escenario minimo tiene que
	# traer bastantes guardianes y rutas o mediriamos la negativa en vez de la propiedad.
	local i
	for i in $(seq 1 "${1:-30}"); do
		printf 'design/relleno-%s.md\n' "$i" > "$d/design/relleno-$i.md"
		cat > "$d/scripts/check-relleno$i-prep.sh" <<-EOG
			#!/usr/bin/env bash
			F="\${OLIVARES_R${i}_F:-design/relleno-$i.md}"
			G="\${OLIVARES_R${i}_G:-design/relleno-$i.md}"
			exit 0
		EOG
		chmod +x "$d/scripts/check-relleno$i-prep.sh"
	done
	( cd "$d" && git init -q . &&
	  git -c user.email=t@example.invalid -c user.name=T add -A >/dev/null &&
	  git -c user.email=t@example.invalid -c user.name=T commit -qm base >/dev/null &&
	  git update-ref refs/remotes/origin/main HEAD )
	printf '%s' "$d"
}
rama() { # rama <dir> <mensaje>
	( cd "$1" && git checkout -q -b probando 2>/dev/null || git checkout -q probando
	  git -c user.email=t@example.invalid -c user.name=T add -A >/dev/null
	  git -c user.email=t@example.invalid -c user.name=T commit -qm "$2" >/dev/null )
}
corre() { ( cd "$1" && bash scripts/check-prep-gate-impact.sh "${2:-probando}" 2>&1 ); }
rc_de() { ( cd "$1" && bash scripts/check-prep-gate-impact.sh "${2:-probando}" >/dev/null 2>&1; echo $? ); }

# 1 · UNA RAMA QUE NO TOCA NADA VIGILADO ES LIMPIA, y lo dice con las dos cifras.
d=$(escenario); printf 'algo\n' > "$d/README.md"; rama "$d" "nada vigilado"
out=$(corre "$d"); rc=$(rc_de "$d")
case "$out$rc" in *CLEAN*0) ok "a branch touching nothing watched is CLEAN" "rc=0";;
	*) bad "a branch touching nothing watched is CLEAN" "rc=$rc :: $(printf '%s' "$out" | head -1)";; esac

# 2 · TOCAR NO ES DISPARAR. El guardian mira el CONTENIDO, no el nombre del fichero.
d=$(escenario)
cat > "$d/scripts/check-mirar-prep.sh" <<'EOG'
#!/usr/bin/env bash
F="${OLIVARES_MIRAR_F:-design/vigilado.md}"
grep -q PROHIBIDO "$F" 2>/dev/null && { echo "check-mirar-prep: FAIL — landed"; exit 1; }
echo "check-mirar-prep: CLEAN"; exit 0
EOG
chmod +x "$d/scripts/check-mirar-prep.sh"; printf 'inocuo\n' > "$d/design/vigilado.md"
( cd "$d" && git -c user.email=t@example.invalid -c user.name=T add -A >/dev/null &&
  git -c user.email=t@example.invalid -c user.name=T commit -qm gate >/dev/null && git update-ref refs/remotes/origin/main HEAD )
printf 'sigue inocuo\n' > "$d/design/vigilado.md"; rama "$d" "toca y no dispara"
out=$(corre "$d"); rc=$(rc_de "$d")
case "$out$rc" in *touch*check-mirar-prep*0) ok "touching a watched file is not tripping it" "rc=0";;
	*) bad "touching a watched file is not tripping it" "rc=$rc :: $(printf '%s' "$out" | grep -E 'TRIP|touch|CLEAN' | head -1)";; esac

# 3 · Y DISPARAR SE REPORTA CON SU RUTA Y SU PRIMERA LINEA. Sin esto, (2) lo cumpliria un
#     guion que nunca ejecuta nada.
printf 'PROHIBIDO\n' > "$d/design/vigilado.md"; rama "$d" "dispara"
out=$(corre "$d"); rc=$(rc_de "$d")
case "$out" in *TRIP*check-mirar-prep*) [ "$rc" = 1 ] && ok "a branch that trips a gate is reported, rc=1" "$rc" || bad "a branch that trips a gate is reported, rc=1" "rc=$rc";;
	*) bad "a branch that trips a gate is reported, rc=1" "$(printf '%s' "$out" | grep -E 'TRIP|touch|CLEAN' | head -1)";; esac

# 4 · EL ESPEJO QUEDA LIMPIO ENTRE GUARDIANES. La rama CREA un fichero que main no tiene; si la
#     restauracion lo dejara, la linea base del guardian siguiente saldria roja por suciedad
#     propia y el veredicto seria «ya rojo en main». Es el defecto que casi se publica.
d=$(escenario)
cat > "$d/scripts/check-aaa-prep.sh" <<'EOG'
#!/usr/bin/env bash
F="${OLIVARES_AAA_F:-design/uno.md}"
exit 0
EOG
cat > "$d/scripts/check-zzz-prep.sh" <<'EOG'
#!/usr/bin/env bash
F="${OLIVARES_ZZZ_F:-design/uno.md}"
[ -e "design/intruso.md" ] && { echo "check-zzz-prep: FAIL — leftover"; exit 1; }
exit 0
EOG
chmod +x "$d/scripts/check-aaa-prep.sh" "$d/scripts/check-zzz-prep.sh"
printf 'uno\n' > "$d/design/uno.md"
( cd "$d" && git -c user.email=t@example.invalid -c user.name=T add -A >/dev/null &&
  git -c user.email=t@example.invalid -c user.name=T commit -qm gates >/dev/null && git update-ref refs/remotes/origin/main HEAD )
printf 'dos\n' > "$d/design/uno.md"; printf 'intruso\n' > "$d/design/intruso.md"
# `intruso.md` tiene que estar VIGILADO por el primer guardian para que el sujeto lo coloque.
sed -i 's#OLIVARES_AAA_F:-design/uno.md#OLIVARES_AAA_F:-design/uno.md}"\nI="${OLIVARES_AAA_I:-design/intruso.md#' "$d/scripts/check-aaa-prep.sh"
rama "$d" "crea un fichero que main no tiene"
out=$(corre "$d"); rc=$(rc_de "$d")
# ⛔ El patron NO puede ser `*"already red"*`: la LINEA DE RESUMEN siempre lleva «N already
# red on origin/main», asi que casaria consigo misma y el caso fallaria siempre. Se mira la
# marca de linea `stale `, que solo aparece cuando hay un veredicto de ese tipo.
sospechoso=$(printf '%s\n' "$out" | grep -cE '^stale |CANNOT LOOK' || true)
if [ "${sospechoso:-1}" -ne 0 ]; then
	bad "the mirror is clean between gates" "$(printf '%s' "$out" | grep -E '^stale |CANNOT LOOK' | head -1)"
elif [ "$rc" -le 1 ]; then
	ok "the mirror is clean between gates" "no false 'already red'"
else
	bad "the mirror is clean between gates" "rc=$rc"
fi

# 5 · SUELOS: una derivacion que encuentra poco REHUSA en vez de decir «limpio».
d=$(escenario 2); printf 'algo\n' > "$d/README.md"; rama "$d" "pocos gates"
rc=$(rc_de "$d")
[ "$rc" = 2 ] && ok "too few gates derived is CANNOT LOOK, not clean" "rc=2" || bad "too few gates derived is CANNOT LOOK, not clean" "rc=$rc"

# 6 · UN REF QUE NO EXISTE ES «NO PUDE MIRAR», no «no toca nada».
d=$(escenario); printf 'algo\n' > "$d/README.md"; rama "$d" "x"
rc=$(rc_de "$d" no-existe-esta-rama)
[ "$rc" = 2 ] && ok "an unresolvable ref is CANNOT LOOK" "rc=2" || bad "an unresolvable ref is CANNOT LOOK" "rc=$rc"

printf '\nprep-gate-impact: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
