#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# aws-iam-phase2.sh — la SEGUNDA fase del rol de apply: cambiar el AdministratorAccess de
# bootstrap por las cinco policies de mínimo privilegio y estrechar el `sub` de su trust.
#
# ⛔ QUIÉN LO CORRE, Y POR QUÉ NO ES EL PIPELINE. El pipeline VERIFICA; no muta. No es
# comodidad: `an internal design note (not shipped)` le niega al rol de
# apply toda escritura de IAM **sobre sí mismo** — su trust, el usuario de bootstrap y el
# proveedor OIDC. Un trabajo de CI capaz de hacer esta transición necesitaría un principal
# capaz de adjuntar policies al rol de apply y de reescribir su trust; es decir, un camino
# PERMANENTE para volver a colgarle AdministratorAccess y volver a abrir el `sub`. Ese
# principal convertiría la guardrail en decoración: lo que se niega por un lado se
# concedería por el otro, y encima sin que nadie lo mirara.
#
#   ⇒ la MUTACIÓN la corre el usuario de bootstrap (quien tiene la autoridad), con este
#     guion — reproducible y revisable, en vez de once clics en una consola;
#   ⇒ la VERIFICACIÓN la corre el pipeline en cada dispatch, con el propio rol de apply y
#     de sólo lectura. Es la mitad que no puede depender de que alguien se acuerde.
#
# ⛔ EL ORDEN NO ES ESTÉTICA: SE ADJUNTA PRIMERO Y SE RETIRA AL FINAL.
#   1 · adjuntar `0-guardrails` (la que NIEGA; un Deny sólo gana si está adjunta)
#   2 · adjuntar las otras cuatro
#   3 · estrechar el `sub` de la trust
#   4 · retirar AdministratorAccess  ← EL ÚLTIMO, SIEMPRE
# Así **todo estado intermedio es al menos tan permisivo como el final**: si un paso falla a
# medias, el rol sigue pudiendo terminar un apply en vuelo. Al revés —retirar primero— un
# fallo en el paso 2 deja el rol sin permisos con medio estate creado: el fallo caro.
#
# ⛔ Y LA TRUST ESTRECHA ATA UNA COSA MÁS, QUE NO SE VE. Con el `sub` fijado a
# `ref:refs/heads/main`, el día que alguien añada `environment:` a un job de
# `aws-terraform.yml` el claim pasa a ser `environment:<nombre>`, deja de casar y **el rol
# se vuelve inasumible**. Eso no se ve leyendo el diff del workflow: se ve 40 minutos
# después, en un `sts:AssumeRoleWithWebIdentity` denegado. Por eso la ausencia de
# `environment:` es un invariante del guard del workflow y no un comentario aquí.
#
# USO
#   scripts/aws-iam-phase2.sh check  [<repo> [<dir-de-policies>]]   # sólo lee (por defecto)
#     `check` juzga contra `IAM_PHASE` (1 o 2; 2 si no está). Esa expectativa vive en un
#     commit del workflow y no en un input del dispatch: una expectativa que se teclea al
#     lanzar no la revisa nadie, y la que importa —«ya no hay AdministratorAccess»— se
#     pierde el día que alguien relanza con los valores por defecto.
#   scripts/aws-iam-phase2.sh apply  [<repo> [<dir-de-policies>]]   # la transición
#   scripts/aws-iam-phase2.sh revert [<repo> [<dir-de-policies> [<trust-original.json>]]]
#     `revert` restaura la trust del 4.º argumento si se la das — y `apply` la vuelca antes
#     de tocar nada justo para que la tengas. SIN ese fichero, `revert` RECONSTRUYE la forma
#     ancha: sirve para volver a asumir el rol, pero pierde lo que la original llevara y
#     este guion no sepa escribir. Lo dice al hacerlo.
#
# Ni la cuenta ni el repositorio están escritos aquí, y es a propósito: `scripts/` viaja al
# árbol público. La cuenta se lee de `sts:GetCallerIdentity`; el repositorio, del argumento
# o de `GITHUB_REPOSITORY`.
#
# Tres respuestas: 0 como se esperaba · 1 hallazgo · 2 no he podido mirar.
set -uo pipefail

say()    { printf '%s\n' "$*"; }
fail()   { say "aws-iam-phase2: HALLAZGO — $*" >&2; exit 1; }
cannot() { say "aws-iam-phase2: NO HE PODIDO MIRAR — $*" >&2; exit 2; }

MODE="${1:-check}"
REPO="${2:-${GITHUB_REPOSITORY:-}}"
POLICY_DIR="${3:-design}"
TRUST_BACKUP="${4:-}"   # sólo `revert`: la trust que `apply` volcó, para restaurarla tal cual
ROLE="olivares-apply-sandbox"
BOOTSTRAP_MANAGED="arn:aws:iam::aws:policy/AdministratorAccess"

case "$MODE" in
check | apply | revert) : ;;
*) cannot "modo '$MODE' desconocido: check, apply o revert" ;;
esac

command -v aws     >/dev/null || cannot "no hay AWS CLI en esta caja"
command -v python3 >/dev/null || cannot "no hay python3"
[ -n "$REPO" ] || cannot "sin repositorio: pásalo como 2.º argumento o en GITHUB_REPOSITORY"

ACCOUNT="$(aws sts get-caller-identity --query Account --output text 2>/dev/null)" \
	|| cannot "sts:GetCallerIdentity no contestó: ¿hay credenciales en el entorno?"
case "$ACCOUNT" in
[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) : ;;
*) cannot "sts:GetCallerIdentity no devolvió una cuenta de 12 dígitos" ;;
esac

# Las cinco piezas se DERIVAN de los ficheros publicados, no se teclean aquí: una lista
# copiada a mano certifica la deriva que existe para cazar. Si el directorio no está —el
# árbol público no lleva `design/`— se dice, no se comprueba a medias.
[ -d "$POLICY_DIR" ] || cannot "no encuentro '$POLICY_DIR': sin las piezas no hay nada que verificar"
PARTS="$(ls "$POLICY_DIR"/aws-apply-role-policy.sandbox.*.json 2>/dev/null | sort)"
[ -n "$PARTS" ] || cannot "no hay piezas 'aws-apply-role-policy.sandbox.*.json' en '$POLICY_DIR'"
NPARTS="$(printf '%s\n' "$PARTS" | wc -l | tr -d ' ')"

policy_name() { # an internal design note (not shipped) -> <rol>-0-guardrails
	local b="${1##*/}"
	b="${b#aws-apply-role-policy.sandbox.}"
	printf '%s-%s' "$ROLE" "${b%.json}"
}

trust_doc() { # trust_doc <sub-exacto|"">  — vacío = la forma ancha del bootstrap
	python3 - "$ACCOUNT" "$REPO" "$1" <<'PY'
import json, sys
account, repo, sub = sys.argv[1], sys.argv[2], sys.argv[3]
cond = ({"StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
                          "token.actions.githubusercontent.com:sub": sub}}
        if sub else
        {"StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com"},
         "StringLike":   {"token.actions.githubusercontent.com:sub": "repo:%s:*" % repo}})
print(json.dumps({"Version": "2012-10-17", "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated":
        "arn:aws:iam::%s:oidc-provider/token.actions.githubusercontent.com" % account},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": cond}]}))
PY
}

# ⛔ EL ÚNICO SITIO DEL FICHERO DONDE SE ESCRIBE EN AWS. Todo verbo mutante pasa por aquí y
# por ninguna otra parte, para que «`check` no muta» sea comprobable leyendo UNA función en
# vez de auditando el guion entero — y para que el gate lo pueda medir contando.
aws_write() {
	[ "$MODE" = check ] && fail "BUG: se ha intentado escribir en modo check ($*)"
	say "  → escritura IAM: $*"
	aws iam "$@" || fail "falló: aws iam $*"
}

attached() { aws iam list-attached-role-policies --role-name "$ROLE" \
	--query 'AttachedPolicies[].PolicyArn' --output text 2>/dev/null | tr '\t' '\n'; }

trust_now() { aws iam get-role --role-name "$ROLE" \
	--query Role.AssumeRolePolicyDocument --output json 2>/dev/null; }

# ⛔ LAS INLINE SON LA PUERTA DE AL LADO. Todo lo de arriba habla de policies GESTIONADAS;
# una policy **inline** en el rol concede lo que le dé la gana y no aparece en
# `list-attached-role-policies`. `0-guardrails` impide que el rol se la ponga a sí mismo,
# pero no que se la ponga el usuario de bootstrap «para desatascar un apply». En fase 2 se
# exige CERO: si hace falta un permiso más, se añade a la pieza que le toca, donde se
# revisa, y no como un parche que nadie vuelve a mirar.
inline_now() {
	local out
	out="$(aws iam list-role-policies --role-name "$ROLE" --query 'PolicyNames' \
		--output text 2>/dev/null)" || return 2
	printf '%s' "$out" | tr '\t' '\n' | command grep -c . || true
}

# Dos documentos de policy son el mismo si lo son como JSON, no como texto: AWS devuelve el
# suyo con otro espaciado y otro orden de claves.
doc_equal() {
	python3 -c '
import json, sys
try:
    a, b = json.loads(sys.argv[1]), json.loads(sys.argv[2])
except Exception:
    sys.exit(1)
sys.exit(0 if json.dumps(a, sort_keys=True) == json.dumps(b, sort_keys=True) else 1)
' "$1" "$2"
}

ATTACHED="$(attached)" || cannot "no pude leer las policies adjuntas de '$ROLE'"
[ -n "$(trust_now)" ]  || cannot "no pude leer la trust de '$ROLE'"

SUB_TARGET="repo:$REPO:ref:refs/heads/main"

# ── la verificación, idéntica en los tres modos: antes de tocar y después ────
#
# ⛔ TRES RESPUESTAS TAMBIÉN AQUÍ DENTRO (M-02). Las lecturas de arriba convierten un fallo
# en rc=2; las de esta función no lo hacían — un `attached` denegado se leía como «lista
# vacía» y un `inline_now` roto como «cero inline». Los dos son la respuesta CÓMODA, y los
# dos se parecen a un hallazgo sin serlo. Ahora cada lectura comprueba su rc y `verify`
# devuelve **2** cuando no ha podido mirar, que `check` propaga tal cual.
#
# ⛔ Y EL CONJUNTO ES EXACTO, NO «AL MENOS» (H-03). Antes se comprobaba que las cinco
# estuvieran y que AdministratorAccess no; **una sexta policy administrativa cualquiera
# pasaba**. Y de las cinco sólo se miraba el ARN: una con el nombre correcto y el documento
# derivado también pasaba. Ahora se exige el conjunto EXACTO y, de cada una, que su versión
# por defecto sea el documento que este repositorio revisó.
verify() { # verify <fase: 1|2> → 0 como se espera · 1 hallazgo · 2 no he podido mirar
	local want="$1" bad=0 arn n p rc doc want_doc ver
	ATTACHED="$(attached)" || return 2
	local -a expected=()
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		n="$(policy_name "$p")"
		arn="arn:aws:iam::$ACCOUNT:policy/$n"
		expected+=("$arn")
		if printf '%s\n' "$ATTACHED" | command grep -qxF "$arn"; then
			if [ "$want" = 2 ]; then
				# El ARN estando no basta: el DOCUMENTO tiene que ser el revisado.
				ver="$(aws iam get-policy --policy-arn "$arn" \
					--query Policy.DefaultVersionId --output text 2>/dev/null)" || return 2
				[ -n "$ver" ] || return 2
				doc="$(aws iam get-policy-version --policy-arn "$arn" --version-id "$ver" \
					--query PolicyVersion.Document --output json 2>/dev/null)" || return 2
				want_doc="$(cat "$p")" || return 2
				if ! doc_equal "$doc" "$want_doc"; then
					say "  ⛔ '$n' está adjunta pero su versión por defecto ($ver) NO es el documento de $p"
					bad=1
				fi
			else
				say "  ⛔ '$n' YA está adjunta y la fase 1 no la lleva"; bad=1
			fi
		else
			[ "$want" = 1 ] || { say "  ⛔ falta '$n'"; bad=1; }
		fi
	done <<-EOF
		$PARTS
	EOF

	if printf '%s\n' "$ATTACHED" | command grep -qxF "$BOOTSTRAP_MANAGED"; then
		[ "$want" = 1 ] || { say "  ⛔ AdministratorAccess SIGUE adjunta"; bad=1; }
	else
		[ "$want" = 2 ] || { say "  ⛔ AdministratorAccess no está y la fase 1 la necesita"; bad=1; }
	fi

	# ⛔ NINGUNA MÁS. «Están las cinco» no es «sólo las cinco»: una IAMFullAccess extra
	# satisfacía la definición anterior entera.
	if [ "$want" = 2 ]; then
		while IFS= read -r arn; do
			[ -n "$arn" ] || continue
			case " ${expected[*]} " in
			*" $arn "*) ;;
			*) say "  ⛔ policy gestionada EXTRA adjunta: $arn"; bad=1 ;;
			esac
		done <<-EOF
			$ATTACHED
		EOF
		n="$(inline_now)"; rc=$?
		[ "$rc" -eq 0 ] || return 2
		[ "$n" -eq 0 ] || { say "  ⛔ el rol lleva $n policy(s) INLINE: conceden fuera de las cinco piezas y no salen en las adjuntas"; bad=1; }
	fi

	doc="$(trust_now)" || return 2
	[ -n "$doc" ] || return 2
	trust_judge "$want" "$doc" || bad=1
	return "$bad"
}

# ⛔ EL JUEZ DE LA TRUST, Y LOS DOS DEFECTOS QUE TENÍA — los dos ALTOS del contraste.
#
# H-01, y era fatal: recibía la trust **por una tubería** mientras el programa le llegaba
# por un heredoc. `python3 - … <<PY` YA usa el stdin para el programa, así que
# `json.load(sys.stdin)` leía el resto de ese heredoc —nada— y el juez **nunca vio una
# trust en su vida**. Fallaba siempre, así que el control era inalcanzable: en fase 2
# ningún dispatch habría pasado nunca. Ahora el documento viaja como ARGUMENTO.
#
# H-02: comparaba «los `:sub` y `:aud` que aparezcan por ahí», sin mirar QUÉ statement los
# lleva. Medido por el contraste sobre esa misma lógica: aceptaba `Principal: "*"`,
# aceptaba las condiciones buenas en un `Deny` **con un `Allow` incondicional al lado**,
# aceptaba `Action` con `sts:AssumeRole` añadido y aceptaba el `aud` bajo
# `StringNotEquals`. Cuatro trusts abiertas dando verde.
#
# ⇒ ahora se compara una FORMA CERRADA: un statement y sólo uno, `Allow`, el principal
# federado exacto de esta cuenta, la acción exacta, y el bloque de condición **completo**
# igual al esperado. Nada de acumular claves: si el documento no es ése, es hallazgo.
trust_judge() { # trust_judge <fase> <documento-json>
	python3 -c '
import json, sys
phase, doc, account, repo, want_sub = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
try:
    d = json.loads(doc)
except Exception as e:
    print("  ⛔ la trust no es JSON legible:", e); sys.exit(1)
prov = "arn:aws:iam::%s:oidc-provider/token.actions.githubusercontent.com" % account
if phase == "2":
    want_cond = {"StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
                                  "token.actions.githubusercontent.com:sub": want_sub}}
else:
    want_cond = {"StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com"},
                 "StringLike":   {"token.actions.githubusercontent.com:sub": "repo:%s:*" % repo}}
st = d.get("Statement")
if not isinstance(st, list) or len(st) != 1:
    print("  ⛔ la trust tiene %s statement(s): se exige exactamente uno, porque un segundo "
          "statement puede conceder por su cuenta" % (len(st) if isinstance(st, list) else "?"))
    sys.exit(1)
s = st[0]
extra = set(s) - {"Effect", "Principal", "Action", "Condition", "Sid"}
if extra:
    print("  ⛔ el statement trae campos que este juez no sabe evaluar:", sorted(extra)); sys.exit(1)
if s.get("Effect") != "Allow":
    print("  ⛔ el Effect del statement es", s.get("Effect")); sys.exit(1)
if s.get("Principal") != {"Federated": prov}:
    print("  ⛔ el Principal no es exactamente el proveedor OIDC de esta cuenta:",
          json.dumps(s.get("Principal"))); sys.exit(1)
if s.get("Action") != "sts:AssumeRoleWithWebIdentity":
    print("  ⛔ la Action no es exactamente sts:AssumeRoleWithWebIdentity:",
          json.dumps(s.get("Action"))); sys.exit(1)
if s.get("Condition") != want_cond:
    print("  ⛔ la Condition no es la esperada para la fase", phase)
    print("     esperada:", json.dumps(want_cond, sort_keys=True))
    print("     hallada :", json.dumps(s.get("Condition"), sort_keys=True)); sys.exit(1)
print("  trust: un Allow, principal y accion exactos, condicion exacta de la fase", phase)
' "$1" "$2" "$ACCOUNT" "$REPO" "$SUB_TARGET"
}

case "$MODE" in
check)
	say "aws-iam-phase2: cuenta $ACCOUNT · rol $ROLE · $NPARTS piezas · repo $REPO"
	WANT="${IAM_PHASE:-2}"
	case "$WANT" in 1 | 2) : ;; *) cannot "IAM_PHASE='$WANT' no es 1 ni 2" ;; esac
	rc=0; verify "$WANT" || rc=$?
	case "$rc" in
	0) say "aws-iam-phase2: en la FASE $WANT que este repositorio declara."; exit 0 ;;
	1) say "aws-iam-phase2: NO está en la fase $WANT declarada (arriba está la diferencia)."; exit 1 ;;
	# ⛔ Y ÉSTA ES LA TERCERA, que antes se perdía: una lectura denegada o transitoria NO
	# es «está en otra fase». Se dice, y quien la reciba decide — el paso del workflow
	# para el job igual, pero por la razón correcta y con el mensaje correcto.
	*) cannot "no pude LEER el estado del rol (permiso, red o API): no es un veredicto de fase" ;;
	esac
	;;
apply)
	say "aws-iam-phase2: transición 1 → 2 en la cuenta $ACCOUNT"
	# 1 y 2 · adjuntar, la guardrail la PRIMERA — el `sort` de arriba la deja delante.
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		n="$(policy_name "$p")"; arn="arn:aws:iam::$ACCOUNT:policy/$n"
		if printf '%s\n' "$ATTACHED" | command grep -qxF "$arn"; then
			say "  ya adjunta: $n"
		else
			aws iam get-policy --policy-arn "$arn" >/dev/null 2>&1 \
				|| aws_write create-policy --policy-name "$n" --policy-document "file://$p"
			aws_write attach-role-policy --role-name "$ROLE" --policy-arn "$arn"
		fi
	done <<-EOF
		$PARTS
	EOF
	# 3 · estrechar la trust — imprimiendo ANTES la que se va a sustituir.
	#
	# ⛔ `revert` RECONSTRUYE una trust ancha; NO restaura ésta byte a byte. Si la de hoy
	# lleva algo que este guion no sabe escribir —un statement más, otra condición, otro
	# `Principal`—, se perdería al revertir y nadie lo notaría. Así que se vuelca aquí,
	# donde queda en el log de quien la corre, y ésa es la copia que vale.
	say "  ⛔ GUARDA ESTA TRUST: es la de AHORA y `revert` no la reproduce, la reconstruye."
	trust_now
	aws_write update-assume-role-policy --role-name "$ROLE" \
		--policy-document "$(trust_doc "$SUB_TARGET")"
	# 4 · Y AL FINAL, retirar AdministratorAccess
	if printf '%s\n' "$ATTACHED" | command grep -qxF "$BOOTSTRAP_MANAGED"; then
		aws_write detach-role-policy --role-name "$ROLE" --policy-arn "$BOOTSTRAP_MANAGED"
	fi
	rc=0; verify 2 || rc=$?
	[ "$rc" -eq 0 ] || fail "la transición corrió y NO puedo declarar la fase 2 (rc=$rc de la verificación)"
	say "aws-iam-phase2: FASE 2 hecha y verificada."
	;;
revert)
	# El inverso, y por la misma razón: se devuelve lo permisivo ANTES de quitar nada.
	say "aws-iam-phase2: revirtiendo 2 → 1 en la cuenta $ACCOUNT"
	aws_write attach-role-policy --role-name "$ROLE" --policy-arn "$BOOTSTRAP_MANAGED"
	# ⛔ M-01: si tienes la trust que `apply` volcó, ÉSA es la que se restaura. Sin ella se
	# reconstruye la forma ancha, que es equivalente para asumir el rol pero **no es la
	# misma**: cualquier cosa que llevara y este guion no sepa escribir se pierde. El
	# cuarto argumento existe para no tener que elegir entre revertir y conservar.
	if [ -n "$TRUST_BACKUP" ]; then
		[ -r "$TRUST_BACKUP" ] || cannot "no puedo leer la trust de respaldo '$TRUST_BACKUP'"
		say "  restaurando la trust de '$TRUST_BACKUP' (la original, no una reconstrucción)"
		aws_write update-assume-role-policy --role-name "$ROLE" \
			--policy-document "file://$TRUST_BACKUP"
	else
		say "  ⚠ sin respaldo: se RECONSTRUYE la trust ancha, no se restaura la anterior"
		aws_write update-assume-role-policy --role-name "$ROLE" --policy-document "$(trust_doc "")"
	fi
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		arn="arn:aws:iam::$ACCOUNT:policy/$(policy_name "$p")"
		printf '%s\n' "$ATTACHED" | command grep -qxF "$arn" \
			&& aws_write detach-role-policy --role-name "$ROLE" --policy-arn "$arn"
	done <<-EOF
		$PARTS
	EOF
	rc=0; verify 1 || rc=$?
	[ "$rc" -eq 0 ] || fail "la reversión corrió y NO puedo declarar la fase 1 (rc=$rc de la verificación)"
	say "aws-iam-phase2: FASE 1 restaurada y verificada."
	;;
esac
