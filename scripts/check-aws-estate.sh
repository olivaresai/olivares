#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-aws-estate.sh — the ratified AWS estate is present, Fly is gone,
# apply is dispatch+confirm+secrets only, the apply job can actually
# authenticate (OIDC exchange, pinned by commit digest, ordered before the
# first tofu invocation), the S3 backend is locked, and the only path that
# publishes images into the estate's ECR repository is a confirmed dispatch
# that signs what it pushes.
#
# Three answers: 0 clean · 1 finding · 2 could not look.
# It does not call terraform/tofu and it never applies. Root HCL is parsed by
# HashiCorp HCL through the small pinned helper under scripts/hcl-module-guard.

set -euo pipefail

say() { printf '%s\n' "$*"; }
fail() { say "check-aws-estate: FAIL — $*" >&2; exit 1; }
cannot() { say "check-aws-estate: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}"
[ -n "$ROOT" ] || cannot "not inside a git work tree"
cd "$ROOT" || cannot "cannot enter $ROOT"

AWS="$ROOT/deploy/aws"
[ -d "$AWS" ] || cannot "no deploy/aws directory"

# ── Los .tf tienen que CERRAR sus bloques ────────────────────────────────────
# Este gate certifica el estado a base de grep, y un grep encuentra su patron
# igual en un fichero valido que en uno roto. Medido el 2026-08-20: la version
# de modules/ingress/main.tf que estaba EN MAIN llevaba un `access_logs {` sin
# cerrar -- todo lo que venia detras (connection_logs, HSTS, y un segundo
# `tags`) habia quedado DENTRO de ese bloque, dos argumentos duplicados y HCL
# invalido-- y este guion daba rc=0 sobre ella. Un desbalance de llaves no es
# un estilo: es un fichero que terraform no puede leer.
#
# No sustituye a `terraform validate`, que ve mucho mas; lo cubre donde
# terraform no esta instalado, que es aqui y en el gate local de cada carril.
# Se ignoran comentarios y el contenido de las cadenas, que es donde una llave
# suelta seria legitima.
desbalanceados=0
while IFS= read -r tf; do
	bal="$(awk '
		{ line = $0
		  sub(/#.*$/, "", line)
		  gsub(/"[^"]*"/, "\"\"", line)
		  n = gsub(/\{/, "{", line); m = gsub(/\}/, "}", line)
		  d += n - m }
		END { print d + 0 }' "$tf")"
	if [ "$bal" -ne 0 ]; then
		say "check-aws-estate: $tf no cierra sus bloques (desbalance de llaves: $bal)" >&2
		desbalanceados=$((desbalanceados + 1))
	fi
done <<EOF
$(find "$AWS" -name '*.tf' -type f | sort)
EOF
# Se nombran TODOS antes de fallar. Un gate que aborta en el primero rotula su
# frontera, no su causa: el 2026-08-20 modules/data/main.tf tapaba a outputs.tf,
# y arreglar el primero habria "descubierto" el segundo como si fuese nuevo.
[ "$desbalanceados" -eq 0 ] || fail "$desbalanceados fichero(s) .tf no cierran sus bloques (arriba)"

# Root wiring must be valid HCL: duplicate arguments in a module block are
# silent to brace balance, and comments are not active attributes. The helper
# traverses the parsed module bodies, so nested expressions cannot truncate the
# inspected block and log-bucket names in comments cannot satisfy the gate.
command -v go >/dev/null || cannot "no Go toolchain for the HCL parser"
HCL_GUARD="$ROOT/scripts/hcl-module-guard"
[ -r "$HCL_GUARD/go.mod" ] && [ -r "$HCL_GUARD/main.go" ] \
  || cannot "missing HCL parser source under scripts/hcl-module-guard"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base" || cannot "cannot create $_tmp_base"

# Los dos helpers se construyen UNA vez por contenido y no una por invocación. La razón,
# con su medida, está en la cabecera de la biblioteca: este gate y su batería viven en el
# `pre-push` de TODOS los carriles a través de `lint:addon-sets`, y la batería lo invoca
# cincuenta veces. La caché se indexa por SHA-256 de las fuentes, así que un guard mutado
# construye de verdad — que es exactamente lo que la batería necesita.
# shellcheck source=lib/gate-bin-cache.sh
. "$ROOT/scripts/lib/gate-bin-cache.sh" \
  || cannot "missing scripts/lib/gate-bin-cache.sh"
_hcl_guard_bin="$(olivares_cached_gate_bin "$HCL_GUARD" hcl-module-guard)" \
  || cannot "cannot build the pinned HCL parser"
_hcl_rc=0
"$_hcl_guard_bin" "$AWS" || _hcl_rc=$?
case "$_hcl_rc" in
  0) ;;
  1) exit 1 ;;
  2) exit 2 ;;
  *) cannot "HCL parser exited unexpectedly with $_hcl_rc" ;;
esac

# Six modules, one directory each. An empty tree is not "zero AWS resources".
want_mods="compute data ingress network observability secrets"
missing=0
for m in $want_mods; do
  if [ ! -d "$AWS/modules/$m" ]; then
    say "check-aws-estate: missing module $m" >&2
    missing=1
  fi
done
[ "$missing" -eq 0 ] || fail "the six ratified modules are not all present"

# Control positive: at least one aws_ resource, so a stub directory cannot pass.
#
# ⛔ El `|| true` NO es higiene: sin el, este gate moria MUDO justo en el caso que
#    la linea de abajo existe para nombrar. Con `set -euo pipefail`, un `grep` sin
#    coincidencias sale 1, la tuberia hereda ese 1 por `pipefail`, la asignacion
#    falla y `set -e` mata el guion ANTES del `fail` — rc=1 y stderr VACIO, que es
#    indistinguible de un fallo de entorno. Medido el 2026-08-31 sembrando cero
#    recursos `aws_`: rc=1, 104 bytes en stdout, 0 en stderr.
aws_n="$( { grep -Rhc 'resource "aws_' "$AWS" --include='*.tf' 2>/dev/null || true; } | awk '{s+=$1} END {print s+0}')"
[ "$aws_n" -gt 0 ] || fail "zero resource \"aws_\" blocks under deploy/aws"

# NLB keeps the collector address (design option 1). Default for IP+TCP is OFF.
grep -q 'preserve_client_ip *= *true' "$AWS/modules/ingress/main.tf" \
  || fail "NLB target group does not set preserve_client_ip = true"

# C04-03: PROXY protocol v2 on the collector NLB (unapplied estate).
grep -Eq 'proxy_protocol_v2 *= *(true|"on")' "$AWS/modules/ingress/main.tf" \
  || fail "NLB target group does not enable proxy_protocol_v2"

# TLS to the target: ALB re-encrypts. HTTP to the task would terminate at the edge only.
grep -q 'protocol *= *"HTTPS"' "$AWS/modules/ingress/main.tf" \
  || fail "ALB target group is not HTTPS (TLS to the target is missing)"

# Leader drain: /readyz 200 is the writer. 503 standby must not take traffic.
grep -q 'matcher *= *"200"' "$AWS/modules/ingress/main.tf" \
  || fail "ALB health matcher is not 200 — standbys would receive traffic"

# Engine start names --dsn. ENGINE_DSN as an env var is not a DSN (the engine
# reads the flag only). A command without the flag is the retired Fly start.
grep -q -- '--dsn' "$AWS/modules/compute/main.tf" \
  || fail "compute task definition does not pass --dsn"

# HA default is two tasks so advisory-lock election has a standby.
# Sin tubería que acabe en `grep -q`: bajo `pipefail`, el consumidor cierra al primer acierto y el
# productor muere con SIGPIPE ⇒ 141 EN ÉXITO. Se captura primero y se decide sobre la cadena.
_desired="$(grep -A4 'variable "desired_count"' "$ROOT/deploy/aws/variables.tf" || true)"
case "$_desired" in
*default*=*2*) : ;;
*)
  fail "desired_count default is not 2 (no standby for leader election)"
  ;;
esac

# Retired Fly descriptors. Historical mentions in sessions/ stay; these two
# files are the live deploy configs.
for f in cloud/engine/fly.toml cloud/control-plane/fly.toml; do
  if [ -e "$ROOT/$f" ]; then
    fail "retired Fly descriptor still present: $f"
  fi
done

WF="$ROOT/.github/workflows/aws-terraform.yml"
[ -f "$WF" ] || fail "no .github/workflows/aws-terraform.yml (C04-02)"

# The validate job (push/PR) must never apply. An apply job may exist
# only if it is dispatch-gated, confirmation-gated, and secret-gated.
validate_block="$(awk '
  /^  validate:/ {p=1; next}
  /^  [A-Za-z0-9_]+:/ && p {exit}
  p {print}
' "$WF")"
if printf '%s\n' "$validate_block" | grep -nE '(tofu|terraform)[[:space:]]+apply\b' >/dev/null; then
  fail "validate job contains an apply — push/PR must stay plan/validate"
fi

if grep -nE '(tofu|terraform)[[:space:]]+apply\b' "$WF" >/dev/null; then
  grep -q 'workflow_dispatch:' "$WF" \
    || fail "apply exists without workflow_dispatch"
  grep -q 'apply-sandbox-estate' "$WF" \
    || fail "apply exists without the confirmation token apply-sandbox-estate"
  grep -q "github.event_name == 'workflow_dispatch'" "$WF" \
    || fail "apply job is not limited to workflow_dispatch"
  grep -q 'AWS_ROLE_ARN' "$WF" \
    || fail "apply job does not require AWS_ROLE_ARN"
  grep -q 'TF_BACKEND_BUCKET' "$WF" \
    || fail "apply job does not require TF_BACKEND_BUCKET"
fi

# ── El CANJE OIDC, el BLOQUEO del backend y el camino a ECR ───────────────────
#
# ⛔ POR QUÉ ESTO NO ES UN `grep` MÁS EN ESTE FICHERO, y es el mismo argumento que trajo
# el parser de HCL veinte líneas más arriba. Hasta el 2026-08-27 el job `apply` pedía
# `id-token: write` y **no canjeaba el token**: donde tenía que ir el paso había un
# comentario de cuatro líneas diciendo que el pin era del integrador. Una invariante
# escrita como «el fichero menciona configure-aws-credentials» la habría satisfecho ESE
# COMENTARIO — exactamente la clase de falso verde que los mutantes de «nombres en
# comentarios» de la batería existen para cazar. Así que el sujeto se lee como ÁRBOL
# YAML, no como texto: un `uses:` dentro de un comentario no existe para el guard.
#
# Y su segundo sujeto es `.github/workflows/aws-images.yml`, que es el único camino que
# publica imágenes en el ECR de la cuenta. Va en ESTE gate y no en uno nuevo porque su
# invariante es la misma —canje pinchado por digest, ordenado antes de quien lo necesita,
# nada automático tocando AWS— y dos puertas con la misma forma se auditan juntas.
IMG_WF="$ROOT/.github/workflows/aws-images.yml"
[ -f "$IMG_WF" ] || fail "no .github/workflows/aws-images.yml — nothing builds the images the ECR repository exists for"

APPLY_GUARD="$ROOT/scripts/aws-apply-guard"
[ -r "$APPLY_GUARD/go.mod" ] && [ -r "$APPLY_GUARD/main.go" ] \
  || cannot "missing workflow parser source under scripts/aws-apply-guard"
_apply_guard_bin="$(olivares_cached_gate_bin "$APPLY_GUARD" aws-apply-guard)" \
  || cannot "cannot build the pinned workflow parser"
_apply_rc=0
"$_apply_guard_bin" "$WF" "$IMG_WF" || _apply_rc=$?
case "$_apply_rc" in
  0) ;;
  1) exit 1 ;;
  2) exit 2 ;;
  *) cannot "workflow parser exited unexpectedly with $_apply_rc" ;;
esac

# ── La policy de mínimo privilegio que SUSTITUYE a AdministratorAccess ───────
#
# ⛔ POR QUÉ ES VARIOS FICHEROS Y NO UNO, Y NO ES ESTILO: MEDIDO. La policy completa
# derivada del estate son **10 919 caracteres** minificados, y una customer managed policy
# de IAM tiene un tope de **6 144 caracteres sin contar espacios en blanco**; el agregado
# de policies inline de un rol, **10 240**
# (`docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html`, consultado el
# 2026-08-27). ⇒ un fichero único NO SE PUEDE ADJUNTAR por ninguna de las dos vías, y el
# rechazo llega cuando ya está pegando. Se publica partida, y este gate impide que
# alguien la vuelva a juntar. Un rol admite hasta 20 managed policies, así que cinco
# piezas caben con holgura.
#
# QUÉ COMPRUEBA, y qué NO: comprueba que cada pieza es adjuntable y que ninguna es
# AdministratorAccess disfrazada. **NO comprueba que el conjunto BASTE para el apply** —
# eso sólo lo dice un `tofu plan` contra la cuenta, que la orden 12 no autoriza hoy. Esa
# mitad va declarada en `an internal design note (not shipped)`, no dada por hecha aquí.
command -v python3 >/dev/null || cannot "no python3 to read the apply-role policy"
_pol_rc=0
python3 - "$ROOT" <<'POLPY' || _pol_rc=$?
import glob, json, os, re, sys

root = sys.argv[1]
design_dir = os.path.join(root, "design")
# ⛔ `design/` NO SE EXPORTA, y este guion SÍ. Un árbol público que trae `deploy/aws/` y
# `scripts/` pero no `design/` no es un árbol al que le falte la policy: es un árbol al que
# esa pregunta no se le hace. Exigirla allí convertiría el gate en algo que **nadie puede
# poner en verde fuera del hub**, que es la forma de gate que más veces se ha roto aquí.
# La distinción es precisa y no afloja nada: si `design/` existe —o sea, en el hub— y las
# piezas no están, sigue siendo hallazgo. Sólo su AUSENCIA COMPLETA es «sin sujeto».
if not os.path.isdir(design_dir):
    print("apply-role-policy-skipped — no design/ in this tree (the export does not carry it)")
    sys.exit(0)
paths = sorted(glob.glob(os.path.join(design_dir, "aws-apply-role-policy.sandbox*.json")))
if not paths:
    print("check-aws-estate: FAIL — no design/aws-apply-role-policy.sandbox*.json: the apply "
          "role would have to keep AdministratorAccess with nothing written to replace it",
          file=sys.stderr)
    sys.exit(1)

QUOTA = 6144           # customer managed policy, whitespace excluded
findings = []
accounts = set()
denies = 0

for path in paths:
    rel = os.path.relpath(path, root)
    raw = open(path, encoding="utf-8").read()
    try:
        doc = json.loads(raw)
    except Exception as exc:                       # noqa: BLE001 — el mensaje es el valor
        findings.append("%s is not valid JSON: %s" % (rel, exc))
        continue
    if doc.get("Version") != "2012-10-17":
        findings.append("%s has no 2012-10-17 Version — IAM would refuse it" % rel)
    statements = doc.get("Statement")
    if not isinstance(statements, list) or not statements:
        findings.append("%s has no Statement list" % rel)
        continue

    chars = len(re.sub(r"\s", "", raw))
    if chars > QUOTA:
        findings.append(
            "%s is %d characters excluding whitespace, over the %d-character managed-policy "
            "quota: it cannot be attached at all" % (rel, chars, QUOTA))

    for st in statements:
        sid = st.get("Sid", "<no Sid>")
        effect = st.get("Effect")
        actions = st.get("Action", [])
        if isinstance(actions, str):
            actions = [actions]
        resources = st.get("Resource", [])
        if isinstance(resources, str):
            resources = [resources]

        if effect == "Deny":
            denies += 1
        elif effect == "Allow":
            # ⛔ La forma exacta de AdministratorAccess. Una policy que la contenga no
            # sustituye nada: la renombra.
            if "*" in actions:
                findings.append("%s/%s allows Action \"*\" — that IS AdministratorAccess" % (rel, sid))
            # Y su variante por servicio: `s3:*` sobre `*` es AdministratorAccess de S3.
            wild = [a for a in actions if a.endswith(":*")]
            if wild and resources == ["*"]:
                findings.append(
                    "%s/%s allows %s on Resource \"*\" — a service-wide wildcard with no "
                    "resource bound is not least privilege" % (rel, sid, ", ".join(sorted(wild))))
        else:
            findings.append("%s/%s has Effect %r" % (rel, sid, effect))

        for arn in resources:
            for acct in re.findall(r"^arn:aws:[a-z0-9-]*:[a-z0-9-]*:(\d{12}):", arn):
                accounts.add(acct)

# Una pieza que apunte a OTRA cuenta es un error que ningún tope de tamaño ve, y el número
# no se escribe aquí: se DERIVA y se exige que sea uno solo. Así el gate no lleva el
# identificador de cuenta dentro (`scripts/` sí se exporta; `design/` no).
if len(accounts) > 1:
    findings.append("the parts name %d different AWS accounts (%s): one of them is not ours"
                    % (len(accounts), ", ".join(sorted(accounts))))
if denies == 0:
    findings.append("no Deny statement anywhere in the set: nothing stops the apply role from "
                    "rewriting its own trust policy or deleting the state bucket")

if findings:
    for f in findings:
        print("check-aws-estate: FAIL — " + f, file=sys.stderr)
    sys.exit(1)
print("apply-role-policy-ok — %d attachable part(s), largest %d/%d chars, %d Deny guardrail(s)"
      % (len(paths),
         max(len(re.sub(r"\s", "", open(p, encoding="utf-8").read())) for p in paths),
         QUOTA, denies))
POLPY
case "$_pol_rc" in
  0) ;;
  1) exit 1 ;;
  *) cannot "the apply-role policy check exited with $_pol_rc" ;;
esac

# ── El guion que hace la transición de IAM, mirado por su ESTRUCTURA ─────────
#
# ⛔ ESTE BLOQUE NO LLAMA A AWS Y NO PUEDE. No hay credenciales en un gate y no debe
# haberlas: lo que se comprueba aquí es que el guion NO PUEDA hacer lo que no debe, que
# es una propiedad del fichero y no de la cuenta. Las cuatro invariantes existen porque
# cada una tiene un modo de fallo caro y silencioso:
#
#   · un verbo mutante escrito FUERA del envoltorio se salta la única guarda que impide
#     que `check` escriba — y `check` es lo que corre el pipeline en cada dispatch;
#   · el envoltorio sin su guarda convierte `check` en `apply` el día que alguien
#     reordene un `case`;
#   · retirar AdministratorAccess ANTES de adjuntar deja el rol sin permisos con medio
#     estate creado: el estado intermedio tiene que ser SIEMPRE el más permisivo;
#   · una cuenta de 12 dígitos escrita aquí viaja al árbol público, porque `scripts/`
#     se exporta y `design/` no.
_iam_rc=0
python3 - "$ROOT" <<'IAMPY' || _iam_rc=$?
import json, os, re, sys

root = sys.argv[1]
rel = "scripts/aws-iam-phase2.sh"
path = os.path.join(root, rel)
findings = []
if not os.path.exists(path):
    print("check-aws-estate: FAIL — %s is missing: the estate documents an IAM phase 2 "
          "and nothing performs or verifies it" % rel, file=sys.stderr)
    sys.exit(1)
src = open(path, encoding="utf-8").read()
code = "\n".join(l for l in src.splitlines() if not l.lstrip().startswith("#"))

# ⛔ LISTA BLANCA, NO LISTA NEGRA — M-03 del contraste, y el cambio de polaridad es el
# arreglo entero. La versión anterior buscaba SIETE verbos mutantes literales, y el
# contraste midió NUEVE formas de shell que se le escapaban: `aws iam "detach-role-policy"`,
# `aws iam "$verb"`, `"$AWS" iam …`, un alias, `eval "aws iam $verb"`, una continuación de
# línea entre `iam` y el verbo, `detach-role"-"policy`, y `create-role`, que ni siquiera
# estaba en la lista. Una lista negra de siete no puede cerrar un espacio infinito, y el
# gate estaba AFIRMANDO haber probado el embudo.
#
# Invertido: **toda aparición del token `aws` en texto ejecutable tiene que estar en una
# línea reconocida**, y sólo hay dos formas reconocidas — la línea única del envoltorio, y
# una lectura de la lista corta de abajo. Cualquier otra cosa que mencione `aws` es
# hallazgo, incluidas las nueve de arriba (`AWS=aws` lleva el token; `aws iam "$verb"` es
# una línea que no casa con ninguna lectura). Y `eval` queda prohibido en este fichero: no
# hay forma honesta de leer lo que construye.
READS = ("get-role", "get-role-policy", "list-attached-role-policies", "list-role-policies",
         "get-policy", "get-policy-version", "get-caller-identity")
read_re = re.compile(r"\baws (iam|sts) (%s)\b" % "|".join(re.escape(r) for r in READS))
funnel_re = re.compile(r'^\s*aws iam "\$@"\s*(\|\|.*)?$')

if re.search(r"(?m)^[^#]*\beval\b", code):
    findings.append("%s uses `eval`: what it builds cannot be read by this gate, so the "
                    "funnel stops being provable" % rel)

# El token que dispara la sospecha son LOS DOS, `aws` e `iam`, y con `:` excluido a ambos
# lados: `arn:aws:iam::…` es un dato y no una invocación, mientras que `"$AWS" iam …` no
# lleva `aws` en minúscula y se habría escapado mirando sólo el primero.
suspect_re = re.compile(r"(?<![A-Za-z0-9_.:-])(aws|iam)(?![A-Za-z0-9_.:-])")

funnel_lines = 0
for line in code.splitlines():
    if not suspect_re.search(line):
        continue
    if funnel_re.match(line):
        funnel_lines += 1
        continue
    if read_re.search(line):
        continue
    # La sonda de presencia, con su forma EXACTA y sin argumentos: no invoca nada.
    if re.match(r"^\s*command -v aws\s+>/dev/null\b", line):
        continue
    findings.append("%s mentions the aws CLI on a line this gate does not recognise as "
                    "either the single write funnel or one of the reads it allows: %r — a "
                    "blacklist of verbs cannot close this, so the allowlist is the check"
                    % (rel, line.strip()[:80]))
if funnel_lines != 1:
    findings.append("%s has %d line(s) matching the aws_write funnel `aws iam \"$@\"`; there "
                    "must be exactly one, or `writes go through one place` is not a fact"
                    % (rel, funnel_lines))

# ⛔ Y LA GUARDA, DENTRO DEL CUERPO DE SU FUNCIÓN. Buscarla en cualquier parte del fichero
# dejaba pasar una copia muerta en un comentario o en otra función (M-03).
m = re.search(r"(?ms)^aws_write\(\)\s*\{(.*?)^\}", code)
if not m:
    findings.append("%s has no aws_write function body this gate can read" % rel)
elif not re.search(r'\[\s*"\$MODE"\s*=\s*check\s*\]\s*&&\s*fail\b', m.group(1)):
    findings.append("%s: the check-mode refusal is not INSIDE the aws_write body — a copy "
                    "elsewhere in the file would keep this green while the wrapper writes"
                    % rel)

# ⛔ EL ORDEN: adjuntar primero, retirar AdministratorAccess al final. Un fallo a medias
# tiene que dejar el rol MÁS permisivo, nunca menos — al revés, se queda sin permisos con
# medio estate creado.
#
# ⚠ Y `attach-role-policy` es SUBCADENA de `detach-role-policy`: buscar el primero con
# `find` casaba dentro del segundo y el veredicto salía del sitio equivocado. Se busca con
# frontera de palabra, que es la diferencia entre medir el orden y medir una coincidencia.
attach_re = re.compile(r"(?<![a-z-])attach-role-policy\b")
detach_re = re.compile(r"(?<![a-z-])detach-role-policy\b")
body = code.split("\napply)", 1)
if len(body) < 2:
    findings.append("%s has no `apply)` branch: there is nothing to order" % rel)
else:
    branch = body[1].split("\nrevert)", 1)[0]
    m_at = attach_re.search(branch)
    m_tr = re.search(r"update-assume-role-policy\b", branch)
    m_de = detach_re.search(branch)
    if not (m_at and m_tr and m_de):
        findings.append("%s: the apply branch does not do all three legs (attach, trust, "
                        "detach)" % rel)
    elif not (m_at.start() < m_tr.start() < m_de.start()):
        findings.append("%s removes AdministratorAccess before finishing the attachments: a "
                        "failure halfway leaves the role unable to finish an apply already in "
                        "flight, with half the estate created" % rel)

# ⛔ QUITAR EL DENY NO CONCEDE. Ésta es la invariante que faltaba y que habría dejado la
# fase 2 INUTILIZABLE: el paso de verificación falla cerrado, así que si el conjunto de
# policies no ALLOW-ea las lecturas que el guion hace sobre el rol, en fase 2 **ningún
# apply podría volver a correr**. El defecto no se ve en ninguno de los dos ficheros por
# separado — sólo cruzándolos, que es justo lo que un gate hace y una lectura no.
verbs = sorted(set(re.findall(r"aws iam ([a-z][a-z-]+) --(?:role-name|policy-arn)", code)))
policy_dir = os.path.join(root, "design")
if verbs and os.path.isdir(policy_dir):
    import glob
    need = {"iam:" + "".join(w.capitalize() for w in v.split("-")) for v in verbs}
    role_suffix = ":role/olivares-apply-sandbox"
    granted = set()
    for pf in sorted(glob.glob(os.path.join(policy_dir,
                                            "aws-apply-role-policy.sandbox.*.json"))):
        for st in json.load(open(pf, encoding="utf-8")).get("Statement") or []:
            if st.get("Effect") != "Allow":
                continue
            acts = st.get("Action") or []
            acts = acts if isinstance(acts, list) else [acts]
            res = st.get("Resource") or []
            res = res if isinstance(res, list) else [res]
            # Las lecturas de policy caen sobre los ARNs de las CINCO piezas, no sobre el
            # del rol: un Allow que cubra cualquiera de los dos sujetos vale para su verbo.
            if not any(r == "*" or r.endswith(role_suffix) or ":policy/" in r for r in res):
                continue
            for a in acts:
                granted.update(need if a in ("*", "iam:*") else {a})
    missing = sorted(need - granted)
    if missing:
        findings.append("%s reads the role with %s but the policy set never Allows %s on the "
                        "apply role: in phase 2 the fail-closed check could not run, and no "
                        "apply would ever start again"
                        % (rel, ", ".join(verbs), ", ".join(missing)))

for m in re.finditer(r"(?<![0-9])[0-9]{12}(?![0-9])", src):
    findings.append("%s contains the 12-digit literal %s: scripts/ is exported to the public "
                    "tree and design/ is not — the account id does not belong here, it comes "
                    "from sts:GetCallerIdentity" % (rel, m.group(0)))

if findings:
    for f in findings:
        print("check-aws-estate: FAIL — " + f, file=sys.stderr)
    sys.exit(1)
print("iam-phase2-ok — writes funnelled through one wrapper that refuses in check mode, "
      "attach-before-detach ordering held, every self-read it performs is Allowed by the "
      "policy set, no account literal")
IAMPY
case "$_iam_rc" in
  0) ;;
  1) exit 1 ;;
  *) cannot "the IAM phase-2 script check exited with $_iam_rc" ;;
esac

# ── `count` y `for_each` NO pueden mirar lo que otro recurso producirá ───────
#
# ⛔ ESTE GATE EXISTE PORQUE EL DEFECTO YA OCURRIÓ, y costó el primer apply de verdad. El
# run 33244273912 produjo su plan entero —99 recursos— y murió con «Invalid count argument»
# en `modules/compute/main.tf`: el `count` leía `var.dsn_secret_arn`, que sale de
# `module.secrets` EN EL MISMO APPLY, así que en tiempo de plan es desconocido.
#
# Y no era uno: el barrido encontró CUATRO sitios de la misma clase, tres de ellos
# esperando en los applies 2 y 3 — el `count` del listener HTTPS sobre `local.cert_arn`
# habría muerto 75 minutos después, y los `dynamic` de los target groups en el tercero.
# Arreglar sólo el que disparó habría descubierto el siguiente a la peor hora posible.
#
# La regla: **el booleano decide, el ARN es un valor**. Lo que decide cuántas instancias
# hay tiene que conocerse al planificar — una variable del llamador, no un atributo que el
# apply producirá. `tofu validate` NO ve esto (no evalúa el grafo), y por eso el gate mira
# el texto: es lo único que se puede comprobar sin credenciales ni estado.
_cnt_rc=0
python3 - "$ROOT" <<'CNTPY' || _cnt_rc=$?
import os, re, sys
root = sys.argv[1]
base = os.path.join(root, "deploy", "aws")
findings = []

# Primero, QUÉ VARIABLES ALIMENTA EL ROOT con valores que el apply produce. Se lee de las
# llamadas a módulo de `deploy/aws/main.tf`: `nombre = module.x.y` o `= aws_z.w.attr`.
apply_fed = set()
rootmain = os.path.join(base, "main.tf")
if os.path.exists(rootmain):
    cur = None
    for line in open(rootmain, encoding="utf-8"):
        code = line.split("#", 1)[0]
        mm = re.match(r'\s*module\s+"([a-z0-9_-]+)"', code)
        if mm:
            cur = mm.group(1)
            continue
        if cur and re.match(r"\s*\}", code):
            cur = None
            continue
        ma = re.match(r"\s*([a-z0-9_]+)\s*=\s*(.+)$", code)
        if cur and ma and re.search(r"(?<![\w.])module\.|(?<![\w.])aws_[a-z0-9_]+\.", ma.group(2)):
            apply_fed.add((cur, ma.group(1)))
            apply_fed.add(("*", ma.group(1)))

# Referencia a algo que el apply produce: una salida de módulo, o un recurso `aws_*` que no
# venga precedido de `var.`/`local.`/`data.` (esos son entradas o datos ya resueltos).
prod = re.compile(r"(?<![\w.])module\.|(?<![\w.])aws_[a-z0-9_]+\.")
for dirpath, _dirs, files in os.walk(base):
    for fn in sorted(files):
        if not fn.endswith(".tf"):
            continue
        rel = os.path.relpath(os.path.join(dirpath, fn), root)
        for i, line in enumerate(open(os.path.join(dirpath, fn), encoding="utf-8"), 1):
            code = line.split("#", 1)[0]
            m = re.match(r"\s*(count|for_each)\s*=\s*(.+)$", code)
            if not m:
                continue
            expr = m.group(2)
            # ⛔ SÓLO LA CONDICIÓN, NO EL VALOR — y esta distinción la fijó una medida, no
            # una intuición: `tofu plan` en frío acepta
            # `for_each = var.flag ? [var.arn_desconocido] : []`, porque lo que tiene que
            # conocerse es CUÁNTAS instancias hay, y eso lo decide el `?`. La primera
            # versión de este gate marcaba esa línea y habría obligado a retorcer un
            # arreglo correcto. Lo que se mira es el trozo ANTES del `?`; si no hay
            # ternario, la expresión entera es la condición.
            cond = expr.split("?", 1)[0] if "?" in expr else expr
            # ⛔ Y LA MITAD QUE FALTABA, que es justo la que disparó: una VARIABLE puede
            # estar alimentada desde una salida de módulo, y ese hecho **no está en el
            # módulo** — está en la llamada, en `deploy/aws/main.tf`. Mirando sólo el
            # módulo, `count = var.dsn_secret_arn == "" ? 0 : 1` parece inocente. Lo
            # descubrió el mutante de este mismo gate: pasaba en verde.
            for vname in re.findall(r"(?<![\w.])var\.([a-z0-9_]+)", cond):
                if (rel.split(os.sep)[2] if len(rel.split(os.sep)) > 2 else "", vname) in apply_fed \
                   or ("*", vname) in apply_fed:
                    findings.append("%s:%d %s reads var.%s, and the root feeds it a value the "
                                    "apply produces: the fact lives in the CALL, not in the "
                                    "module, so reading only this file cannot see it"
                                    % (rel, i, m.group(1), vname))
            if prod.search(cond):
                findings.append("%s:%d %s reads a value the apply produces (%s): count and "
                                "for_each must be known at PLAN time, so the flag decides and "
                                "the ARN is only a value"
                                % (rel, i, m.group(1), expr.strip()[:56]))
if findings:
    for f in findings:
        print("check-aws-estate: FAIL — " + f, file=sys.stderr)
    sys.exit(1)
print("plan-time-counts-ok — no count/for_each depends on a value the apply produces, "
      "counting the %d module input(s) the root feeds from one" % (len(apply_fed) // 2))
CNTPY
case "$_cnt_rc" in
  0) ;;
  1) exit 1 ;;
  *) cannot "the plan-time count check exited with $_cnt_rc" ;;
esac

# ── Lo que el plano de control EXIGE vs lo que su task definition le da ──────
#
# ⛔ UN HECHO EN DOS FICHEROS DERIVA, y aquí el precio de la deriva es que el servicio no
# arranca. `Load()` acumula las variables ausentes y **se niega a arrancar**; la task
# definition de `deploy/aws` es quien se las da. Los dos ficheros no se leen a la vez, y
# hasta el 2026-08-29 la task definition **no daba ninguna**: el estate aplicaba limpio y el
# plano de control no podía levantar.
#
# La lista NO se copia: se deriva de las llamadas a `get()` del propio `config.go`, que es
# lo que decide qué es obligatorio. El fichero avisa de esto en su cabecera —«THIS LIST IS
# PROSE AND PROSE DRIFTS»— así que copiar su prosa sería repetir el defecto que denuncia.
#
# Se salta cuando `cloud/control-plane` no está (el árbol exportado no lo lleva): eso es
# «no puedo mirar», no «está bien».
_cpv_rc=0
python3 - "$ROOT" <<'CPVPY' || _cpv_rc=$?
import json, os, re, sys
root = sys.argv[1]
cfg = os.path.join(root, "cloud", "control-plane", "internal", "config", "config.go")
tf  = os.path.join(root, "deploy", "aws", "modules", "compute", "main.tf")
if not os.path.exists(cfg) or not os.path.exists(tf):
    print("cp-env-coverage: SKIP — cloud/control-plane or the compute module is not in this tree")
    sys.exit(0)
src = open(cfg, encoding="utf-8").read()
# Sólo el cuerpo: un `get("X")` citado en un comentario no es un requisito.
body = "\n".join(l for l in src.splitlines() if not l.lstrip().startswith("//"))
required = set(re.findall(r'\bget\("([A-Z][A-Z0-9_]*)"\)', body))
if not required:
    print("check-aws-estate: FAIL — could not derive any required variable from config.go: the "
          "probe changed or the file did, and an empty list would pass everything",
          file=sys.stderr)
    sys.exit(1)
tft = open(tf, encoding="utf-8").read()
cp = tft.split('name      = "control-plane"', 1)
if len(cp) < 2:
    print("check-aws-estate: FAIL — no control-plane container in the compute module",
          file=sys.stderr)
    sys.exit(1)
block = cp[1].split("portMappings", 1)[0]
# ⛔ TODOS los literales en MAYÚSCULAS del bloque, no sólo los que siguen a `name =`. La
# primera versión de este extractor sólo miraba `name = "X"` y acusó de faltar SEIS
# variables que el testigo renderizado demostraba presentes: las que un `for` genera con
# `name = k` no llevan su nombre pegado a `name`. Un extractor que no ve la forma en que el
# fichero está escrito acusa al código de un defecto que no tiene.
named = set(re.findall(r'"([A-Z][A-Z0-9_]{2,})"', block))
# Los diez DSN se componen con `DATABASE_${k}` sobre una lista de sufijos: se recomponen igual.
for suf in re.findall(r'"([A-Z][A-Z0-9_]*_URL)"', block):
    named.add("DATABASE_" + suf)
missing = sorted(required - named)
findings = []
if missing:
    findings.append("the control-plane task definition does not supply %d variable(s) that "
                    "config.go refuses to boot without: %s"
                    % (len(missing), ", ".join(missing)))
# ⛔ Y la dirección contraria, que es un fallo distinto: `DATABASE_URL` se RECHAZA, no se
# ignora. Ponerla «por si acaso» tumba el arranque, así que su presencia es un hallazgo.
if "DATABASE_URL" in named:
    findings.append("the control-plane task definition supplies DATABASE_URL, and config.go "
                    "REFUSES to boot when it is present: it named a role that no longer exists")
if findings:
    for f in findings:
        print("check-aws-estate: FAIL — " + f, file=sys.stderr)
    sys.exit(1)
print("cp-env-coverage-ok — all %d variables config.go requires are supplied, and the refused "
      "DATABASE_URL is absent" % len(required))
CPVPY
case "$_cpv_rc" in
  0) ;;
  1) exit 1 ;;
  *) cannot "the control-plane env coverage check exited with $_cpv_rc" ;;
esac

say "check-aws-estate: CLEAN — 6 modules, ${aws_n} aws_ resource(s), root module args unique, NLB preserve_client_ip, ALB HTTPS+200, --dsn present, Fly descriptors gone, apply is dispatch+confirm+secrets only, OIDC exchange pinned by digest and ordered before tofu, backend locked, images signed on the way to ECR, least-privilege apply policy split into attachable parts, IAM phase declared and verified before tofu, no count/for_each on apply-time values, control-plane env fully supplied."
exit 0
