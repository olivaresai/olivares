#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Bateria de aws-iam-phase2.sh — SOLO la eleccion de estate, contra un `aws` FALSO. No toca AWS.
#
# ⛔ POR QUE EXISTE, y es un defecto medido el 2026-09-02, no un caso de laboratorio. El guion se
# invocaba como `OLIVARES_ESTATE=production … check` porque asi lo pedia el encargo, y
# `OLIVARES_ESTATE` **no existia en ningun guion del arbol**, ni aqui ni en `origin/main`: el rol
# estaba fijo en la linea 67. La variable se ignoraba en silencio y la comprobacion medía
# `olivares-apply-sandbox`, asi que toda respuesta «de produccion» obtenida asi era sobre sandbox.
# Ese mismo dia ya existian los DOS roles en la cuenta, o sea que la herramienta con la que se
# estrecha produccion no sabia mirar produccion.
#
# Lo unico que permitia darse cuenta era que la salida NOMBRA el rol. Por eso estos casos asiertan
# el NOMBRE, no el codigo de salida: un rc correcto sobre el sujeto equivocado es el fallo que esto
# viene a cerrar.
set -uo pipefail
export LC_ALL=C

RAIZ="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
SUT="$RAIZ/scripts/aws-iam-phase2.sh"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/iamphase2.XXXXXX")" || exit 2
trap 'rm -rf "$BASE"' EXIT INT TERM

pasados=0; fallados=0
check() { # etiqueta esperado obtenido
	if [ "$2" = "$3" ]; then
		printf '  ok   %-56s %s\n' "$1" "$3"; pasados=$((pasados + 1))
	else
		printf '  FAIL %-56s esperado=%s obtenido=%s\n' "$1" "$2" "$3"; fallados=$((fallados + 1))
	fi
}

# `aws` falso: contesta lo justo para que `check` llegue a imprimir su cabecera y compare la trust.
# Deja la linea de orden en un fichero para poder asertar QUE ROL se consulto de verdad.
mkdir -p "$BASE/bin"
cat >"$BASE/bin/aws" <<'AWS'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${FAKE_AWS_ARGV:-/dev/null}"
case "$*" in
"sts get-caller-identity"*) echo "111122223333" ;;
# La trust ANCHA del bootstrap: existe y no es la de la fase 2, que es el estado real de un rol
# antes de estrecharlo. Con `Statement: []` el guion no llegaba a imprimir la condicion esperada y
# el caso de abajo medía un camino que este banco no alcanza — o sea, no medía nada.
"iam get-role"*) cat <<'DOC'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
 "Principal":{"Federated":"arn:aws:iam::111122223333:oidc-provider/token.actions.githubusercontent.com"},
 "Action":"sts:AssumeRoleWithWebIdentity",
 "Condition":{"StringEquals":{"token.actions.githubusercontent.com:aud":"sts.amazonaws.com"}}}]}
DOC
;;
"iam list-attached-role-policies"*) echo "" ;;
"iam list-role-policies"*)  echo "[]" ;;
"iam get-policy "*)         exit 1 ;;   # ninguna pieza existe todavia
*)                          echo "" ;;
esac
AWS
chmod +x "$BASE/bin/aws"

# Piezas de policy de mentira, para separar «no hay piezas» de «no hay estate».
mkdir -p "$BASE/pol"
for n in 0-guardrails 1-state-and-network; do
	printf '{"Version":"2012-10-17","Statement":[]}\n' >"$BASE/pol/aws-apply-role-policy.sandbox.$n.json"
done

corre() { # corre <estate|""> <dir-policies> -> salida completa
	local est="$1" dir="$2"
	FAKE_AWS_ARGV="$BASE/argv" PATH="$BASE/bin:$PATH" \
		env ${est:+OLIVARES_ESTATE="$est"} bash "$SUT" check ejemplo/repo "$dir" 2>&1
}
rol_consultado() { # el rol que la PRIMERA llamada a iam nombro de verdad
	awk '/iam get-role/{for(i=1;i<=NF;i++) if($i=="--role-name") {print $(i+1); exit}}' "$BASE/argv"
}

# ---------------------------------------------------------------- (1) por defecto: sandbox
: >"$BASE/argv"
out="$(corre "" "$BASE/pol")"
check "(1) sin OLIVARES_ESTATE, el rol es el de sandbox" "olivares-apply-sandbox" "$(rol_consultado)"
case "$out" in *"rol olivares-apply-sandbox"*) d=si ;; *) d=no ;; esac
check "(1) y la cabecera lo NOMBRA" si "$d"

# ------------------------------------- (2) production: otro rol, y no se cuela el de sandbox
: >"$BASE/argv"
mkdir -p "$BASE/pol-prod"
for n in 0-guardrails 1-state-and-network; do
	printf '{"Version":"2012-10-17","Statement":[]}\n' >"$BASE/pol-prod/aws-apply-role-policy.production.$n.json"
done
out="$(corre production "$BASE/pol-prod")"
check "(2) con OLIVARES_ESTATE=production, el rol es el de produccion" \
	"olivares-apply-production" "$(rol_consultado)"
case "$out" in *"olivares-apply-sandbox"*) d=si ;; *) d=no ;; esac
check "(2) y NO aparece sandbox por ningun lado" no "$d"

# ⛔ El sujeto OIDC tambien cambia: produccion se estrecha por ENTORNO, no por rama. Compararla
# contra el sujeto de sandbox daria «la Condition no es la esperada» SIEMPRE, y un rojo permanente
# es indistinguible de un hallazgo: se aprende a ignorarlo, que es como muere un control.
case "$out" in *"environment:production"*) d=si ;; *) d=no ;; esac
check "(2) y la trust esperada es la de ENTORNO" si "$d"
case "$out" in *"ref:refs/heads/main"*) d=si ;; *) d=no ;; esac
check "(2) no la de rama" no "$d"

# ------------------- (3) un estate sin fase 2 escrita: rc 2 y lo NOMBRA. No cae a sandbox.
: >"$BASE/argv"
out="$(corre production "$BASE/pol")"; rc=$?
check "(3) production sin sus piezas -> no cae a las de sandbox" "" "$(rol_consultado)"
case "$out" in *"aws-apply-role-policy.production"*) d=si ;; *) d=no ;; esac
check "(3) y dice que faltan las de production" si "$d"

# --------------------------------- (4) estate desconocido: rc 2 ANTES de tocar nada de AWS
: >"$BASE/argv"
out="$(corre marte "$BASE/pol")"; rc=$?
check "(4) estate inventado -> rc 2" 2 "$rc"
llamadas=$(wc -l <"$BASE/argv")
check "(4) y sin una sola llamada a AWS" 0 "$((llamadas))"

# ---------------------------------------------------------------------------- el mutante
# Se vuelve a fijar el rol, que es la conducta anterior. El caso (2) tiene que dejar de distinguir.
MUT="$BASE/mut.sh"
# ⛔ EL MUTANTE MUERDE LA TABLA DE ESTATES, que es donde vive la decision. Se le quita la fila de
# production, o sea que production cae al `*)` … salvo que ese `*)` es `cannot`, asi que en vez de
# eso se le hace apuntar al rol de sandbox: la conducta EXACTA de antes de parametrizar (rol fijo).
sed 's|^production) ROLE="olivares-apply-production"|production) ROLE="olivares-apply-sandbox"|' \
	"$SUT" >"$MUT"
cmp -s "$SUT" "$MUT" && d=NO-DIFIERE || d=ok
check "(M) el mutante REALMENTE difiere" ok "$d"
: >"$BASE/argv"
FAKE_AWS_ARGV="$BASE/argv" PATH="$BASE/bin:$PATH" OLIVARES_ESTATE=production \
	bash "$MUT" check ejemplo/repo "$BASE/pol-prod" >/dev/null 2>&1
check "(M) con el rol fijo, production vuelve a medir sandbox" "olivares-apply-sandbox" "$(rol_consultado)"

echo "aws-iam-phase2: $pasados passed, $fallados failed"
[ "$fallados" -eq 0 ] || exit 1
exit 0
