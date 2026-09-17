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

# ⛔ POR JOB, NO POR FICHERO — y esto se corrigió el 2026-09-02, con la medida delante.
# Estos cinco `grep` miraban el WORKFLOW ENTERO, y con un solo job que aplicaba eso era
# equivalente a mirar el job. Al añadir `apply-production` dejó de serlo: borrar la línea
# `if:` del job del piloto seguía dando verde en esta red, **porque la cadena la aportaba
# el OTRO job**. Un mutante que antes moría aquí pasó a morir dos capas más abajo, y una
# red que se satisface con lo que hay en otro sitio no es una red: es una coincidencia.
#
# Se recorta el fichero por bloques de job (mismo `awk` que aísla `validate` arriba) y se
# pregunta a CADA bloque que contenga un `tofu apply`. La forma exacta de la condición la
# comprueba `scripts/aws-apply-guard` sobre el árbol YAML; esto es la red gruesa, y su
# valor está en correr ANTES y en no depender de que el parser en Go llegue a construirse.
_apply_jobs="$(awk '
  /^  [A-Za-z0-9_-]+:/ { job = $1; sub(/:$/, "", job) }
  job != "" { body[job] = body[job] $0 "\n" }
  END { for (j in body) if (body[j] ~ /(tofu|terraform)[[:space:]]+apply[^-a-z]/) print j }
' "$WF" | sort)"
if [ -n "$_apply_jobs" ]; then
  grep -q 'workflow_dispatch:' "$WF" \
    || fail "apply exists without workflow_dispatch"
  # El token del piloto tiene que EXISTIR en el fichero: es el que nombra el runbook y el
  # que una sesión teclea. Va ANTES del bucle porque su desaparición es un defecto del
  # fichero, no de un job, y nombrarlo así es lo que distingue «lo renombraron» de «este
  # job no lo lleva».
  grep -q 'apply-sandbox-estate' "$WF" \
    || fail "apply exists without the confirmation token apply-sandbox-estate"
  while IFS= read -r _job; do
    [ -n "$_job" ] || continue
    _blk="$(awk -v want="$_job" '
      /^  [A-Za-z0-9_-]+:/ { job = $1; sub(/:$/, "", job) }
      job == want { print }
    ' "$WF")"
    # ⛔ HERE-STRING Y NO TUBERÍA, y no es estilo: `grep -q` sale en cuanto casa, cierra el
    # tubo, y `printf` recibe SIGPIPE ⇒ 141. Bajo `set -o pipefail` el estado de la tubería
    # ES ese 141 aunque `grep` haya encontrado lo que buscaba, así que el `|| fail` de la
    # línea siguiente dispararía **con el árbol bien**: un rojo de flota sobre nada. Lo cazó
    # `lint:sigpipe-booleans` antes de que saliera de esta rama, contando 0 -> 4 tuberías
    # nuevas en este fichero.
    grep -q "github.event_name == 'workflow_dispatch'" <<<"$_blk" \
      || fail "apply job is not limited to workflow_dispatch (job \"$_job\")"
    # ⛔ SÓLO LA PRESENCIA DEL TOKEN, NO LA FORMA DE LA CONDICIÓN — y la división es
    # deliberada, no pereza. Exigir `confirm == '…'` aquí le robaría el sujeto al control
    # que de verdad responde por la forma completa (`scripts/aws-apply-guard`, comparación
    # EXACTA sobre el árbol YAML), y dejaría sin probar el caso que INVIERTE el predicado
    # conservando todas las palabras. Esta red es gruesa a propósito: corre antes y no
    # depende de que el parser en Go llegue a construirse.
    grep -qE "apply-[a-z]+-estate" <<<"$_blk" \
      || fail "apply job carries no confirmation token of the form apply-<estate>-estate (job \"$_job\")"
    grep -q 'AWS_ROLE_ARN' <<<"$_blk" \
      || fail "apply job does not require AWS_ROLE_ARN (job \"$_job\")"
    grep -q 'TF_BACKEND_BUCKET' <<<"$_blk" \
      || fail "apply job does not require TF_BACKEND_BUCKET (job \"$_job\")"
  done <<EOF
$_apply_jobs
EOF
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
# ⛔ TODOS LOS ESTATES, NO SOLO EL PRIMERO. El glob decía `sandbox*` y con eso las piezas de
# `production` no eran sujeto de NINGUNA de las comprobaciones de abajo: ni el tope de 6144
# caracteres, ni el comodín que es AdministratorAccess con otro nombre, ni la cuenta única.
# Un segundo entorno cuyas policies no mirara nadie es peor que no tenerlas, porque parece
# que las tiene.
paths = sorted(glob.glob(os.path.join(design_dir, "aws-apply-role-policy.*.json")))
if not paths:
    print("check-aws-estate: FAIL — no design/aws-apply-role-policy.*.json: the apply "
          "role would have to keep AdministratorAccess with nothing written to replace it",
          file=sys.stderr)
    sys.exit(1)
# Y el conjunto de SANDBOX tiene que seguir existiendo: `aws-iam-phase2.sh` lo usa por
# defecto, así que un árbol con sólo las de producción dejaría al piloto sin nada con que
# sustituir AdministratorAccess mientras este gate diría que hay piezas.
if not [q for q in paths if ".sandbox." in os.path.basename(q)]:
    print("check-aws-estate: FAIL — there are apply-role policy parts but none for sandbox: "
          "aws-iam-phase2.sh defaults to that estate and would find nothing to attach",
          file=sys.stderr)
    sys.exit(1)

QUOTA = 6144           # customer managed policy, whitespace excluded
findings = []
accounts = set()
denies = 0
denies_by_estate = {}


def estate_of(p):
    # an internal design note (not shipped)<estate>.<n>-<nombre>.json
    return os.path.basename(p).split(".")[1]


estates = sorted({estate_of(q) for q in paths})

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
            denies_by_estate[estate_of(path)] = denies_by_estate.get(estate_of(path), 0) + 1
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
# ⛔ EL CONJUNTO DE PRODUCCION ES EL DE SANDBOX CON EL PREFIJO CAMBIADO, Y ESO SE COMPRUEBA.
# Las dos familias describen el MISMO estate: los mismos modulos, los mismos recursos, el
# mismo bucket de estado con otra clave. Se generaron por sustitucion mecanica, y una copia
# generada que nadie vuelve a comparar es una copia que deriva — un permiso anadido al
# piloto para desatascar un apply se queda fuera de produccion, o al reves, y el sintoma
# llega como un AccessDenied a mitad de un apply. Se comprueba en LAS DOS DIRECCIONES: la
# pieza que falta en un lado es tan hallazgo como el contenido que difiere.
SUBS = (("olivares-apply-sandbox", "olivares-apply-production"),
        ("olivares-pilot", "olivares-production"),
        ("cloud/sandbox/", "cloud/production/"))
sb = {os.path.basename(q).split(".", 2)[2]: q for q in paths if ".sandbox." in os.path.basename(q)}
pr = {os.path.basename(q).split(".", 2)[2]: q for q in paths if ".production." in os.path.basename(q)}
if pr:
    for missing in sorted(set(sb) - set(pr)):
        findings.append("design/aws-apply-role-policy.production.%s is missing while its "
                        "sandbox twin exists: production would attach fewer parts than the "
                        "estate needs" % missing)
    for extra in sorted(set(pr) - set(sb)):
        findings.append("design/aws-apply-role-policy.production.%s has no sandbox twin: the "
                        "two estates describe the same infrastructure, so a part that exists "
                        "on one side only is drift, not design" % extra)
    for name in sorted(set(sb) & set(pr)):
        want = open(sb[name], encoding="utf-8").read()
        for a, b in SUBS:
            want = want.replace(a, b)
        got = open(pr[name], encoding="utf-8").read()
        if got != want:
            findings.append("design/aws-apply-role-policy.production.%s is not its sandbox "
                            "twin under the estate-prefix substitution: the two sets have "
                            "drifted, and the symptom of that is an AccessDenied halfway "
                            "through an apply" % name)

# ⛔ POR ESTATE, NO EN LA UNION — y esto lo destapo un mutante que dejo de morder. Mientras
# el gate leia SOLO las piezas de sandbox, «cero Deny en el conjunto» era una pregunta bien
# planteada. Al ensanchar el glob a todos los estates, borrar las guardas del piloto dejaba
# de disparar porque las de PRODUCCION seguian contando: un rol sin guardas, y el gate en
# verde. Cada estate tiene su propio rol y su propia trust, asi que cada uno responde por
# sus Deny.
for est in estates:
    if denies_by_estate.get(est, 0) == 0:
        findings.append("no Deny statement anywhere in the %s set: nothing stops that apply "
                        "role from rewriting its own trust policy or deleting the state bucket"
                        % est)

if findings:
    for f in findings:
        print("check-aws-estate: FAIL — " + f, file=sys.stderr)
    sys.exit(1)
print("apply-role-policy-ok — %d estate(s), %d attachable part(s), largest %d/%d chars, "
      "%d Deny guardrail(s)"
      % (len(estates), len(paths),
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

# ⛔ EL CHEQUEO DE SALUD TIENE QUE APUNTAR A UNA RUTA QUE EL PLANO DE CONTROL SIRVA, Y CON SU
# PROTOCOLO. Este control existe porque el estate llego a `main` pidiendo **HTTPS `/readyz`** a
# un binario que sirve **texto plano** y no registra `/readyz`: dos defectos con el MISMO
# sintoma —ningun objetivo llega jamas a sano y el servicio no arranca en su primer apply— y
# ninguno visible en un `tofu validate`, porque cada mitad es correcta EN SU FICHERO. El
# terraform no puede leer el Go y el Go no sabe que existe un balanceador; **nadie miraba el
# par**. Medido el 2026-09-02 sobre `main`.
#
# ⛔ Y LAS RUTAS SE LEEN DONDE EL BINARIO LAS CONSTRUYE, CON EL PARSER DE GO, Y SOLO LAS DEL
# HANDLER QUE SE DEVUELVE. Hasta el 2026-09-06 este bloque buscaba `mux.Handle*(` en
# `cmd/cloud-cp/main.go` y nada mas; desde `a63c5cfb44` (2026-09-05, credencial dedicada para
# las lecturas del portal) las rutas se registran en `internal/httpapi.NewRouter` y `main.go`
# solo la llama, asi que el lector contaba cero rutas y el gate entero salia «no he podido
# mirar» —44 casos de su bateria con el— sobre un arbol que servia `/health` igual que antes.
# La primera correccion seguia la cadena con expresiones regulares sobre un texto sin
# comentarios, y la revision independiente de `cf45f7f699` la tumbo dos veces el mismo dia:
# (1) sumaba los registros de TODOS los `http.NewServeMux()` del cuerpo de la funcion, asi que
# `return http.NewServeMux()` —un mux recien creado, sin nada— seguia «sirviendo» `/health`;
# (2) el texto conservaba las cadenas, asi que `` _ = `mux.Handle("GET /health", health)` ``
# contaba como registro. Un `httptest` contra el paquete real devolvia 404 en los dos casos.
# Ahora lo lee `scripts/served-routes-guard` con `go/parser`: el `http.Server` cuyo `Addr` es
# `cfg.ListenAddr` → la variable que le sirve de `Handler` → o bien un `http.NewServeMux()`
# con sus registros en `main.go`, o bien una funcion de un paquete del propio modulo
# (resuelto por el `import` y el `module` del go.mod) y, de esa funcion, **solo el mux que
# devuelve**. Un `_test.go` no es el binario. Una cadena o comentario no es un registro.
#
# ⛔ Y UN NODO DE LLAMADA EN EL ARBOL TAMPOCO ES UN REGISTRO EJECUTADO. La segunda revision
# independiente (sobre `9b2a26baa1`) puso el registro de salud dentro de `if false { … }` y
# dentro de una funcion anonima que nadie llama: el lector visitaba todos los nodos de llamada
# del cuerpo y los acreditaba, y el httptest contra el paquete real devolvia 404. El modelo
# de ejecucion que este gate sostiene es LINEAL y pequeño a proposito: se leen solo las
# sentencias del NIVEL SUPERIOR del cuerpo, en orden; el mux se define una vez con un
# `x := http.NewServeMux()` desnudo; un registro es una sentencia `x.Handle(...)` /
# `x.HandleFunc(...)` entre la definicion y la frontera; la frontera es el `return x` que
# tiene que ser la ULTIMA sentencia (en `main`, la primera sentencia que entrega `x` como
# `Handler` de un `http.Server`). Cualquier otra mencion del mux —dentro de un if, for,
# switch, select, defer, go, bloque, funcion anonima (definirla no la ejecuta), reasignada,
# copiada, pasada a otra llamada, registrada tras la frontera o antes de la definicion— deja
# el cuerpo FUERA del modelo: «no he podido mirar» con la linea y el contexto nombrados. No se
# infiere control de flujo: no se «demuestra» que `if false` no ejecute; se dice que no se ha
# mirado. Un mux devuelto SIN registros es un hallazgo, no una duda: el servidor sirve nada.
#
# ⛔ Y LAS TRANSFERENCIAS SE LEEN ANTES QUE EL MUX, Y EL SERVIDOR ES UNA VARIABLE SEGUIDA. La
# revision raiz de `ee185bda96` dejo dos falsos CLEAN mas: un `goto afterHealth` que salta el
# registro de salud SIN nombrar el mux —el lector solo miraba sentencias que lo mencionaban— y
# un `srv.Handler = http.NotFoundHandler()` tras el literal que vinculaba el mux —el lector
# veia el literal una vez y no seguia la variable—. Ahora un `goto` o una etiqueta en
# cualquier sentencia del cuerpo (fuera de funciones anonimas), o un `panic`/`os.Exit`
# incondicional del nivel superior antes de la frontera, es «no he podido mirar»; y el
# `http.Server` de `cfg.ListenAddr` tiene que construirse en una asignacion simple del nivel
# superior de `main` y no volver a usarse salvo como receptor de un metodo suyo
# (`srv.ListenAndServe()`, `srv.Shutdown(ctx)`): asignar un campo, reasignarlo, copiarlo,
# pasarlo a otra llamada o construirlo dentro de un `if` es «no he podido mirar». Lo que se
# afirma y lo que no: que los metodos de `*http.Server` no cambian el Handler es el limite
# sobre el que descansa esa tolerancia; no se afirma nada sobre terminacion, TLS ni
# middleware. La bateria compila el router real y el bloque real del servidor (httptest) para
# probar que los testigos son regresiones de verdad, no solo veredictos.
# La ruta de salud tiene ademas que admitir GET y no estar detras de una credencial: el
# balanceador sondea sin cabeceras.
#
# ⛔ SI EL SUJETO NO ESTA, SE SALTA — y se dice cual falta. Mismo precedente que la cobertura
# de env de mas arriba: el arbol EXPORTADO no lleva `cloud/control-plane`, y convertir su
# ausencia en «no he podido mirar» tumbaria el gate entero alli. Saltado y NOMBRADO no es
# lo mismo que aprobado.
_hc_tf="$ROOT/deploy/aws/modules/ingress/main.tf"
_hc_go="$ROOT/cloud/control-plane/cmd/cloud-cp/main.go"
if [ ! -f "$_hc_tf" ]; then
  say "check-aws-estate: health-check pair — sin deploy/aws/modules/ingress/main.tf, el par no se puede comprobar"
elif [ ! -f "$_hc_go" ]; then
  say "check-aws-estate: health-check pair — sin cloud/control-plane/cmd/cloud-cp/main.go, el par no se puede comprobar"
else
  ROUTES_GUARD="$ROOT/scripts/served-routes-guard"
  [ -r "$ROUTES_GUARD/go.mod" ] && [ -r "$ROUTES_GUARD/main.go" ] \
    || cannot "missing served-routes parser source under scripts/served-routes-guard"
  _routes_guard_bin="$(olivares_cached_gate_bin "$ROUTES_GUARD" served-routes-guard)" \
    || cannot "cannot build the pinned served-routes parser"
  _hc_facts="$(mktemp "$_tmp_base/served-routes.XXXXXX")" \
    || cannot "cannot create a scratch file under $_tmp_base"
  _hc_rc=0
  "$_routes_guard_bin" "$ROOT" cloud/control-plane cloud/control-plane/cmd/cloud-cp/main.go \
    >"$_hc_facts" 2>"$_hc_facts.err" || _hc_rc=$?
  case "$_hc_rc" in
    0) ;;
    2) _hc_reason="$(cat "$_hc_facts.err")"; rm -f "$_hc_facts" "$_hc_facts.err"; cannot "$_hc_reason" ;;
    *) rm -f "$_hc_facts" "$_hc_facts.err"; cannot "the served-routes parser exited unexpectedly with $_hc_rc" ;;
  esac
  _hc="$(python3 - "$_hc_tf" "$_hc_facts" <<'HCPY'
import re, sys
tf, facts_path = sys.argv[1], sys.argv[2]
def cannot(msg):
    print("CANNOT " + msg); raise SystemExit(0)
def fail(msg):
    print("FAIL " + msg); raise SystemExit(0)
s = open(tf, encoding="utf-8").read()
m = re.search(r'resource "aws_lb_target_group" "http" \{(.*?)\n\}', s, re.S)
if not m:
    cannot("no encuentro el target group http")
hc = re.search(r'health_check \{(.*?)\}', m.group(1), re.S)
if not hc:
    fail("el target group http no declara health_check")
proto = re.search(r'protocol\s*=\s*"([A-Z]+)"', hc.group(1))
path = re.search(r'path\s*=\s*"([^"]+)"', hc.group(1))
if not proto or not path:
    fail("el health_check no declara protocol y path")
proto, path = proto.group(1), path.group(1)

# Los hechos que imprime served-routes-guard: WHERE, RETURNED, ROUTE*, OPAQUE, TLS.
where, returned, rutas, opaque, tls = "", "", [], None, None
for line in open(facts_path, encoding="utf-8").read().splitlines():
    f = line.split("\t")
    if f[0] == "WHERE" and len(f) == 2:
        where = f[1]
    elif f[0] == "RETURNED" and len(f) == 2:
        returned = f[1]
    elif f[0] == "ROUTE" and len(f) == 3:
        rutas.append((f[1], f[2]))
    elif f[0] == "OPAQUE" and len(f) == 2:
        opaque = int(f[1])
    elif f[0] == "TLS" and len(f) == 2:
        tls = f[1] == "1"
    else:
        cannot("hecho ilegible del served-routes parser: %r" % line)
if not where or not returned or opaque is None or tls is None:
    cannot("el served-routes parser no imprimio todos los hechos (WHERE/RETURNED/OPAQUE/TLS)")
if not rutas:
    if opaque:
        cannot("no he sabido leer ninguna ruta del plano de control en %s: %d registro(s) con patron "
               "no literal" % (where, opaque))
    if returned == "fresh":
        fail("el health_check apunta a %s y el plano de control NO la sirve: %s devuelve un "
             "http.NewServeMux() recien creado, sin ningun registro; lo que se registra en su "
             "cuerpo va a un mux que no se devuelve" % (path, where))
    fail("el health_check apunta a %s y el plano de control NO la sirve: el mux `%s` que devuelve "
         "%s no tiene ningun registro" % (path, returned, where))
def parts(pattern):
    mm = re.fullmatch(r"(?:([A-Z]+)\s+)?(\S+)", pattern)
    return (mm.group(1), mm.group(2)) if mm else (None, pattern)
served = sorted({parts(p)[1] for p, _ in rutas})
hits = [(parts(p)[0], h) for p, h in rutas if parts(p)[1] == path]
if not hits:
    fail("el health_check apunta a %s y el plano de control NO la sirve (sirve: %s; leido en %s, "
         "mux devuelto %s%s)"
         % (path, ", ".join(served), where, returned,
            "; %d registro(s) con patron no literal que este gate no lee" % opaque if opaque else ""))
gets = [h for meth, h in hits if meth in (None, "GET")]
if not gets:
    fail("el health_check hace GET a %s y el plano de control solo la registra para %s"
         % (path, ", ".join(sorted(meth for meth, _ in hits if meth))))
guarded = [h for h in gets if re.search(r"APIKey|Middleware|Auth", h)]
if len(guarded) == len(gets):
    fail("la ruta de salud %s esta detras de una credencial (%s) y el balanceador sondea sin "
         "cabeceras: ningun objetivo llegaria a sano" % (path, ", ".join(guarded)))
# ⛔ Y EL PROTOCOLO, en LAS DOS DIRECCIONES: el binario habla TLS solo si alguien llama
# ListenAndServeTLS. Sin la segunda mitad este gate solo empujaria hacia abajo y bendeciria
# el texto plano para siempre.
if proto == "HTTPS" and not tls:
    fail("el health_check pide HTTPS y el plano de control no habla TLS "
         "(ni ListenAndServeTLS ni X509KeyPair en su fuente)")
if proto == "HTTP" and tls:
    fail("el plano de control habla TLS y el health_check pide HTTP en claro")
print("health-check-pair-ok — %s %s, y el plano de control la sirve (%d ruta(s) en el mux `%s` que "
      "devuelve %s)" % (proto, path, len(rutas), returned, where))
HCPY
)"
  rm -f "$_hc_facts" "$_hc_facts.err"
  case "$_hc" in
    CANNOT*) cannot "${_hc#CANNOT }" ;;
    FAIL*)   fail "${_hc#FAIL }" ;;
    *)       say "$_hc" ;;
  esac
fi

# ⛔ LA CREDENCIAL DEL MASTER DE RDS SOLO LA ALCANZA LA TAREA DE UN SOLO USO. Este control
# existe porque el permiso que esa tarea necesita es SUPERUSUARIO sobre la base de datos, y
# la forma barata de "arreglar" un fallo suyo es colgarlo del rol `-exec` que ya existe —
# con lo que el servicio que atiende trafico se queda con esa autoridad PARA SIEMPRE, a
# cambio de un paso que corre UNA VEZ. La decision esta escrita junto al codigo
# (`modules/compute/main.tf`, bloque roles_oneshot) y en
# `an internal design note (not shipped)` §2; lo que faltaba era algo que la sostuviera
# cuando nadie mire el comentario. Se comprueban las TRES derivas por separado, porque son
# defectos distintos con el mismo final: el ARN nombrado desde otro recurso (da igual que sea
# una policy IAM o un `secrets` de container definition), el rol prestado a otro recurso, y
# el alcance de su propia policy ensanchado con un comodin.
_ms="$(python3 - "$ROOT" <<'MSPY'
import re, sys, os
root = sys.argv[1]
tf = os.path.join(root, "deploy/aws/modules/compute/main.tf")
if not os.path.isfile(tf):
    print("SKIP sin deploy/aws/modules/compute/main.tf, el alcance no se puede comprobar")
    raise SystemExit(0)
s = open(tf, encoding="utf-8").read()

# Los bloques de primer nivel, contando llaves con los comentarios y las cadenas
# neutralizados -- misma tecnica que el control de balance de mas arriba, y por la misma
# razon: una llave dentro de una cadena no abre nada.
def neutral(line):
    line = re.sub(r"#.*$", "", line)
    return re.sub(r'"[^"]*"', '""', line)

bloques, cur, depth = [], None, 0
for i, line in enumerate(s.splitlines(), 1):
    n = neutral(line)
    if depth == 0:
        m = re.match(r'\s*resource\s+"([a-z0-9_]+)"\s+"([A-Za-z0-9_]+)"\s*\{', line)
        if m:
            cur = {"tipo": m.group(1), "nombre": m.group(2), "linea": i, "cuerpo": []}
    if cur is not None:
        cur["cuerpo"].append(line)
        depth += n.count("{") - n.count("}")
        if depth == 0:
            cur["cuerpo"] = "\n".join(cur["cuerpo"])
            bloques.append(cur); cur = None
    else:
        depth += n.count("{") - n.count("}")
        if depth < 0: depth = 0

if not bloques:
    print("CANNOT no he sabido leer ningun bloque resource de modules/compute/main.tf")
    raise SystemExit(0)

hallazgos = []
uno = [b for b in bloques if b["nombre"] == "roles_oneshot"
       or b["nombre"].startswith("roles_oneshot_")]

# (1) La credencial del MASTER solo puede aparecer en los recursos de la tarea de un solo
#     uso. Da igual por que puerta llegue -- una policy IAM o un `secrets` de container
#     definition--: en cualquiera de las dos, el que la alcanza es un servicio de larga
#     duracion, y eso es superusuario permanente sobre la base de datos.
for b in bloques:
    if "var.master_user_secret_arn" in b["cuerpo"] and b not in uno:
        hallazgos.append('%s:%d %s "%s" nombra var.master_user_secret_arn, y ese secreto es '
                         "SUPERUSUARIO de la base de datos: solo puede alcanzarlo la tarea de "
                         "un solo uso (recursos roles_oneshot*), nunca un recurso que un "
                         "servicio de larga duracion asuma"
                         % ("deploy/aws/modules/compute/main.tf", b["linea"], b["tipo"], b["nombre"]))

# (2) Y el rol de ejecucion de esa tarea no lo toma prestado nadie: si otro recurso lo
#     nombra, la separacion del diseno se deshace sin que (1) lo vea.
for b in bloques:
    if "aws_iam_role.roles_oneshot" in b["cuerpo"] and b not in uno:
        hallazgos.append('%s:%d %s "%s" nombra aws_iam_role.roles_oneshot: ese rol existe '
                         "para que NO lo asuma ningun servicio, y prestarselo le da la "
                         "credencial del master para siempre"
                         % ("deploy/aws/modules/compute/main.tf", b["linea"], b["tipo"], b["nombre"]))

# (3) Y dentro de su propia policy, el alcance no se ensancha: ni comodin en la accion ni
#     `*` en el recurso. Un `secretsmanager:*` sobre esa entrada anade escritura y borrado
#     de la credencial del master; un `Resource = ["*"]` la extiende a TODAS las ranuras.
pol = [b for b in uno if b["tipo"] == "aws_iam_role_policy"]
if pol:
    cuerpo = pol[0]["cuerpo"]
    st = re.search(r'Sid\s*=\s*"ReadOnlyTheOneRdsMasterEntry"(.*?)\},', cuerpo, re.S)
    if not st:
        hallazgos.append("la policy de la tarea de un solo uso ya no declara el statement "
                         "ReadOnlyTheOneRdsMasterEntry: no se puede comprobar su alcance")
    else:
        acc = re.search(r"Action\s*=\s*\[([^\]]*)\]", st.group(1))
        # `compact([...])` es la forma que toma en cuanto la lista tiene mas de un ARN: uno
        # de ellos puede llegar vacio y `compact` lo quita. Se acepta envuelto o desnudo, y
        # lo que se mira sigue siendo el CONTENIDO de la lista.
        res = re.search(r"Resource\s*=\s*(?:compact\()?\[([^\]]*)\]", st.group(1))
        if not acc or not res:
            hallazgos.append("el statement ReadOnlyTheOneRdsMasterEntry no declara Action y "
                             "Resource como listas: no se puede comprobar su alcance")
        else:
            if "*" in acc.group(1):
                hallazgos.append("la accion sobre la credencial del master lleva comodin (%s): "
                                 "un comodin ahi anade escritura y borrado del secreto que "
                                 "manda sobre la base de datos entera" % acc.group(1).strip())
            if "var.master_user_secret_arn" not in res.group(1) or '"*"' in res.group(1):
                hallazgos.append("el recurso de esa accion no es exactamente la entrada del "
                                 "master (%s): acotarlo por ARN es lo que separa leer una "
                                 "credencial de leerlas todas" % res.group(1).strip())
    # ⛔ Y LA MITAD DE KMS, que es por donde volvio a abrirse. La primera version de esta
    # policy llevaba `kms:Decrypt` sobre `Resource = ["*"]` con una condicion `ViaService`,
    # y el contraste `sol max` del 2026-09-02 midio que eso no acota lo que parece:
    # `ViaService` limita el SERVICIO que hace la llamada, no la clave, ni la cuenta, ni el
    # secreto. Un `*` ahi alcanza cualquier clave cuya otra mitad de autorizacion lo admita.
    # La cura fue nombrar la CMK; este control es lo que impide que el `*` vuelva.
    kst = re.search(r'Action\s*=\s*\["kms:Decrypt"\](.*?)\n\s{6}\}', cuerpo, re.S)
    if not kst:
        hallazgos.append("la policy de la tarea de un solo uso ya no declara un statement de "
                         "kms:Decrypt: sin el no puede leer la ranura cifrada con nuestra CMK, "
                         "y con un Resource ancho leeria mucho mas que ella")
    else:
        kres = re.search(r"Resource\s*=\s*(?:compact\()?\[([^\]]*)\]", kst.group(1))
        if not kres:
            hallazgos.append("el statement de kms:Decrypt no declara Resource como lista: no "
                             "se puede comprobar su alcance")
        elif '"*"' in kres.group(1):
            hallazgos.append("el kms:Decrypt de la tarea de un solo uso vuelve a estar sobre "
                             '"*" (%s): `ViaService` acota el SERVICIO, no la clave ni la '
                             "cuenta, asi que un comodin ahi alcanza cualquier clave cuya otra "
                             "mitad de autorizacion lo admita" % kres.group(1).strip())
else:
    if any("var.master_user_secret_arn" in b["cuerpo"] for b in uno):
        hallazgos.append("los recursos roles_oneshot* nombran la credencial del master y no "
                         "hay ninguna aws_iam_role_policy propia que acote su alcance")

# (4) ⛔ Y LA COBERTURA DE CREDENCIALES. `cloud-control-roles.sql` exige un `-v <rol>_password=…`
#     por cada rol de login, y con una de menos la tarea NO PUEDE provisionar los roles.
#
#     ⛔ CORREGIDO EL 2026-09-02: aqui decia «cuando falta uno avisa y SALE CON CODIGO 0 … el
#     paso sale en verde sin hacer nada», y eso es FALSO en este arbol. Se curo el 2026-08-17
#     (`280326b45`) y hoy las diez guardas del SQL ejecutan `SELECT 1/0` bajo ON_ERROR_STOP.
#     Lo levanto el contraste `sol max` (F-10). **El control no cambia y su valor tampoco**:
#     descubrir la credencial que falta AQUI, sobre el arbol, cuesta segundos; descubrirla en
#     el `run-task` cuesta un estate ya aplicado y un plano de control que no arranca.
#
#     Se cuenta contra el SQL y NO contra una lista escrita aqui: una lista caduca en
#     silencio el dia que el SQL gane un rol, y una lista que caduca es la forma de gate que
#     mas veces se ha roto en esta casa. El nombre de la clave del secreto NO se compara con
#     el del rol a proposito — `cloud_cp_admin_ro` viaja en `DATABASE_ADMIN_URL` y
#     `cloud_cp_webhook_idempotency` en `DATABASE_IDEMPOTENCY_URL`, asi que un mapa de
#     nombres seria justo esa lista que caduca. Lo que se exige es que haya una credencial
#     por rol, y esa cuenta si la sostiene el SQL.
sqlp = os.path.join(root, "cloud/control-plane/deploy/cloud-control-roles.sql")
tdef = [b for b in uno if b["tipo"] == "aws_ecs_task_definition"]
if tdef and os.path.isfile(sqlp):
    sql = open(sqlp, encoding="utf-8").read()
    roles = set(re.findall(r"\\if :\{\?([a-z_]+)_password\}", sql))
    if not roles:
        hallazgos.append("no he sabido leer ningun rol de %s: sin ese numero la cobertura de "
                         "credenciales de la tarea de un solo uso no se puede comprobar"
                         % os.path.relpath(sqlp, root))
    else:
        cuerpo = tdef[0]["cuerpo"]
        m = re.search(r"secrets\s*=(.*?)\n\s*logConfiguration", cuerpo, re.S)
        trozo = m.group(1) if m else cuerpo
        # Las diez viajan como `DATABASE_<X>_URL` generadas por un `for`, asi que se cuentan
        # los sufijos de la lista, no las apariciones de `name =`.
        keys = set(re.findall(r'"([A-Z][A-Z0-9_]*_URL)"', trozo))
        if len(keys) < len(roles):
            hallazgos.append("la tarea de un solo uso recibe %d credencial(es) de rol y el SQL "
                             "exige %d: con una de menos no puede provisionar los roles, y eso "
                             "se descubre en el `run-task` con el estate ya aplicado"
                             % (len(keys), len(roles)))

# (5) EL `command`, LA IMAGEN Y SU MAJOR. Tres hechos en tres ficheros distintos que nadie lee
#     a la vez, que es la definicion de invariante que necesita un gate:
#
#     · la task definition tiene que DECLARAR que corre el lanzador. La imagen trae un `CMD`
#       seguro, pero el sitio donde eso se revisa en un diff es el estate;
#     · tiene que existir el Dockerfile de esa imagen, o `roles_task_image` nombraria algo que
#       nadie construye — y el sintoma llegaria como un `run-task` sin imagen, con el estate ya
#       aplicado;
#     · y su major de Postgres tiene que ser el de la RDS. El Dockerfile lo promete en un
#       comentario; esto es lo que hace que la promesa valga algo.
if tdef:
    cuerpo_td = tdef[0]["cuerpo"]
    for campo in ("entryPoint", "command"):
        if not re.search(r"(?m)^\s*%s\s*=" % campo, cuerpo_td):
            hallazgos.append("la task definition de la tarea de un solo uso no declara `%s`: la "
                             "imagen trae un CMD seguro, pero lo que corre este paso se revisa "
                             "en el diff del estate, no en el registro de imagenes" % campo)
    # ⛔ EN EL VALOR DE `command`, NO EN EL BLOQUE. Esta comprobacion decia «roles-oneshot.sh
    # aparece en el cuerpo», y su propio mutante la desmintio en cuanto el bloque gano un
    # COMENTARIO que nombra el lanzador: la task definition podia invocar otra cosa y el gate
    # seguia en verde, satisfecho por la prosa. Es la misma clase que «una mencion dentro de un
    # echo no es una invocacion», cometida al escribir el control que la persigue.
    mc = re.search(r"(?m)^\s*command\s*=\s*(\[[^\]]*\])", cuerpo_td)
    if mc and "roles-oneshot.sh" not in mc.group(1):
        hallazgos.append("el `command` de la tarea de un solo uso no invoca roles-oneshot.sh "
                         "(%s): sin el lanzador, el SQL corre a pelo y vuelve a poder salir con "
                         "codigo 0 sin crear ni un rol" % mc.group(1).strip())

# ⛔ SI `cloud/control-plane` NO ESTA, SE SALTA — y no es cortesia: **el export publico NO lo
# lleva** (`scripts/export-public.sh` copia `examples deploy scripts oscap packaging`, y `cloud`
# no esta en esa lista). Sin esta guarda, las tres comprobaciones de abajo convertirian un arbol
# exportado legitimo en un hallazgo y tumbarian el gate ENTERO en publico — la misma clase que
# ya mordio a esta casa con un gate cuya especificacion vivia en una ruta excluida. Saltado y
# NOMBRADO no es lo mismo que aprobado, y lo dice el caso de la bateria que lo prueba.
dockerfile = os.path.join(root, "cloud/control-plane/deploy/Dockerfile.roles")
datatf = os.path.join(root, "deploy/aws/modules/data/main.tf")
cp_present = os.path.isdir(os.path.join(root, "cloud/control-plane/deploy"))
if uno and not cp_present:
    pass  # el export no trae cloud/control-plane: no hay sujeto que mirar
elif uno and not os.path.isfile(dockerfile):
    hallazgos.append("hay tarea de un solo uso y no existe "
                     "cloud/control-plane/deploy/Dockerfile.roles: `roles_task_image` nombraria "
                     "una imagen que nadie construye")
elif uno and os.path.isfile(datatf):
    # ⛔ Y QUE ALGUIEN LA CONSTRUYA. El Dockerfile puede existir y la task definition nombrarla
    # mientras el workflow que publica imagenes no la toca: entonces `roles_task_image` nombra
    # algo que ningun pipeline produce, y el sintoma llega como un `run-task` sin imagen con el
    # estate ya aplicado. `aws-apply-guard` cuenta las imagenes que el workflow construye y
    # exige una firma por cada una, pero no sabe CUALES son — si alguien retira este paso, alli
    # bajan a dos y todo cuadra. Esta es la mitad que dice que la de roles es una de ellas.
    imgwf = os.path.join(root, ".github/workflows/aws-images.yml")
    if os.path.isfile(imgwf):
        iw = open(imgwf, encoding="utf-8").read()
        # Anclado a la linea del flag de buildx, no a la mencion: el nombre del fichero aparece
        # tambien en los comentarios que explican por que la imagen es dedicada.
        if not re.search(r"(?m)^\s*--file\s+cloud/control-plane/deploy/Dockerfile\.roles\s*\\?\s*$", iw):
            hallazgos.append("aws-images.yml no construye cloud/control-plane/deploy/Dockerfile.roles "
                             "con un `--file`: la task definition nombra una imagen que ningun "
                             "pipeline publica, y eso se descubre en el `run-task`, con el estate "
                             "ya aplicado")

    df = open(dockerfile, encoding="utf-8").read()
    d = open(datatf, encoding="utf-8").read()
    # ⛔ EL MAJOR SE LEE DE LA ASERCION, NO DE LA ETIQUETA — y la diferencia esta MEDIDA. En
    # `FROM etiqueta@digest` manda el DIGEST y Docker no comprueba que la etiqueta le
    # corresponda: `crane config postgres:16-alpine@sha256:18cfe3ef…` da `PG_MAJOR=17`. Un
    # gate que lea el numero de la etiqueta compara una DECORACION, y este lo hacia.
    #
    # ⚠ Y AQUI VA EL PUNTO CIEGO, porque un control que no lo declara miente: **este gate no
    # puede resolver el digest** — eso exige red, y un gate con red da veredictos distintos por
    # caja y «no pude mirar» para siempre en el export. Lo que comprueba es que el Dockerfile
    # LLEVE la asercion de build y que su numero sea el de la RDS; que la imagen sea de verdad
    # ese major lo comprueba el BUILD, que es el unico sitio donde estan los bytes. Ninguna de
    # las dos mitades cierra esto sola.
    # ⛔ EL LITERAL DEL `RUN`, NO UN `ARG`. Esto leia `ARG EXPECTED_PG_MAJOR=…`, y un `ARG` lo
    # sobreescribe quien construye con `--build-arg`: la asercion se podia desactivar desde
    # fuera sin tocar el Dockerfile (contraste `sol max`, F-06). Se lee el numero que va en la
    # instruccion que EJECUTA, que nadie puede mover sin que salga en el diff.
    # ⛔ SE LE PREGUNTA A LA HERRAMIENTA, NO A UNA VARIABLE DE ENTORNO. Esto leia
    # `RUN test "$PG_MAJOR" = "N"`, que valia mientras la base fuera `postgres:*-alpine` —esa
    # imagen publica `PG_MAJOR`—. Desde que la base es `alpine` + `postgresql16-client` (F-08,
    # sacar el SERVIDOR de una imagen que lleva la credencial del master) esa variable no
    # existe, y la asercion pregunta al `psql` instalado. Es estrictamente mejor: una variable
    # de entorno la pone la imagen; esto lee la herramienta que se va a usar.
    # ⛔ SE LE PREGUNTA A LA HERRAMIENTA, NO A UNA VARIABLE DE ENTORNO. Esto leia
    # `RUN test "$PG_MAJOR" = "N"`, que valia mientras la base fuera `postgres:*-alpine` —esa
    # imagen publica `PG_MAJOR`—. Desde que la base es `alpine` + `postgresql16-client` (F-08,
    # sacar el SERVIDOR de una imagen que lleva la credencial del master) esa variable no
    # existe, y la asercion pregunta al `psql` instalado. Es estrictamente mejor: una variable
    # de entorno la pone la imagen; esto lee la herramienta que se va a usar.
    #
    # ⚠ Y LAS DOS FAMILIAS VAN EN CADENAS SEPARADAS A PROPOSITO. Al meter las invariantes de la
    # imagen solo-cliente DENTRO de la cadena `elif` del major, `elif m.group(1) != …` dejo de
    # colgar de su `if not m:` y paso a colgar del `if` nuevo — asi que con la asercion ausente
    # este gate REVENTABA con un AttributeError en vez de contestar. Un gate que se rompe no da
    # su tercera respuesta: no da ninguna.
    m = re.search(r'(?m)^RUN\s+test "\$\(psql --version[^"]*\)" = "(\d+)"', df)
    r = re.search(r'(?m)^\s*engine_version\s*=\s*"(\d+)', d)
    if not m:
        hallazgos.append("Dockerfile.roles no comprueba la version de `psql` contra un literal "
                         "en un `RUN`: sin esa asercion el build acepta cualquier major, porque "
                         "en `FROM etiqueta@digest` manda el digest y la etiqueta es decorativa")
    elif not r:
        hallazgos.append("no he sabido leer engine_version de modules/data/main.tf")
    elif m.group(1) != r.group(1):
        hallazgos.append("la imagen de la tarea de un solo uso trae el cliente de Postgres %s y "
                         "la RDS declara el motor %s: un cliente por debajo del servidor puede "
                         "no entender su protocolo, y el desacuerdo solo existe ENTRE los dos "
                         "ficheros" % (m.group(1), r.group(1)))

    if re.search(r'(?m)^ARG\s+EXPECTED_PG_MAJOR', df):
        hallazgos.append("Dockerfile.roles vuelve a declarar `ARG EXPECTED_PG_MAJOR`: un ARG lo "
                         "sobreescribe quien construye con `--build-arg`, asi que la asercion se "
                         "podria desactivar desde fuera sin tocar el fichero")

    # ⛔ SOLO CLIENTE, Y EL PAQUETE PINCHADO. Los dos son del mismo hallazgo (F-08): esta imagen
    # lleva la credencial del MASTER dentro, asi que no puede traer el servidor —`postgres`,
    # `initdb`, `pg_ctl`— ni una version de cliente que se mueva sola.
    if re.search(r'(?m)^FROM\s+postgres:', df):
        hallazgos.append("Dockerfile.roles vuelve a partir de una imagen `postgres:*`, que trae "
                         "el SERVIDOR entero (postgres, initdb, pg_ctl): eso es superficie de "
                         "administracion de base de datos en un contenedor que lleva dentro la "
                         "credencial del master")
    _apk = re.search(r'(?m)^RUN apk add[^\n]*?(postgresql\d+-client)(=\S+)?', df)
    if not _apk:
        hallazgos.append("Dockerfile.roles no instala un `postgresql<N>-client`: sin cliente no "
                         "hay `psql`, y la tarea no puede aplicar el SQL")
    elif not _apk.group(2):
        hallazgos.append("Dockerfile.roles instala `%s` sin fijar version con `=`: el paquete "
                         "puede cambiar entre dos builds del mismo commit" % _apk.group(1))
    if not re.search(r'(?m)^RUN for b in postgres initdb pg_ctl', df):
        hallazgos.append("Dockerfile.roles no comprueba en el build que la imagen NO trae "
                         "binarios de servidor: «el paquete -client no deberia traerlos» no es "
                         "una comprobacion, y una dependencia puede arrastrarlos")

    if not re.search(r"(?m)^FROM\s+\S+@sha256:[0-9a-f]{64}", df):
        hallazgos.append("Dockerfile.roles no fija su base por digest: una etiqueta es un "
                         "puntero movil, y quien la controle decide que codigo corre con la "
                         "credencial del master de la base de datos")

# (6) ⛔ TODA VARIABLE DE IMAGEN DE LA RAIZ TIENE QUE PODER RECIBIR UN VALOR. `aws-images.yml`
#     construye, firma y publica una imagen, e imprime su digest para pegarlo en el dispatch del
#     apply — y si ese dispatch no declara la entrada, **el digest no llega a ningun sitio**: la
#     imagen existe, la variable existe, el modulo la consume, y la funcion es INALCANZABLE con
#     este gate diciendo CLEAN. Le paso a `roles_task_image` (contraste `sol max`, F-01) y es la
#     MISMA clase que `deploy/aws/variables.tf` documenta de las cuatro que la raiz no pasaba.
#
#     Se deriva de las variables de la RAIZ y no de una lista: el dia que haya una cuarta imagen,
#     esto la pide sin que nadie lo recuerde.
rootvars = os.path.join(root, "deploy/aws/variables.tf")
wfpath = os.path.join(root, ".github/workflows/aws-terraform.yml")
if os.path.isfile(rootvars) and os.path.isfile(wfpath):
    rv = open(rootvars, encoding="utf-8").read()
    wf = open(wfpath, encoding="utf-8").read()
    imgvars = sorted(set(re.findall(r'(?m)^variable "([a-z0-9_]*image)"', rv)))
    for v in imgvars:
        if not re.search(r"(?m)^\s*%s:\s*$" % re.escape(v), wf):
            hallazgos.append("deploy/aws/variables.tf declara `%s` y el dispatch de "
                             "aws-terraform.yml no la ofrece como entrada: el digest que "
                             "aws-images.yml publica no tendria por donde llegar, y la funcion "
                             "queda inalcanzable con este gate en verde" % v)
        elif not re.search(r"(?m)^\s*TF_VAR_%s:" % re.escape(v), wf):
            hallazgos.append("el dispatch ofrece `%s` y ningun job de apply la exporta como "
                             "TF_VAR_%s: la entrada se teclea y no llega a OpenTofu" % (v, v))

if hallazgos:
    print("FAIL " + hallazgos[0])
    raise SystemExit(0)
if not uno:
    print("master-secret-scope-ok — no hay tarea de un solo uso en el arbol, y la credencial "
          "del master no la nombra ningun recurso de compute")
else:
    print("master-secret-scope-ok — la credencial del master la alcanzan %d recurso(s), todos de "
          "la tarea de un solo uso, con la lectura acotada por ARN y sin comodines, y la tarea "
          "recibe una credencial por cada rol que el SQL exige"
          % len([b for b in uno if "var.master_user_secret_arn" in b["cuerpo"]]))
MSPY
)"
case "$_ms" in
  SKIP*)   say "check-aws-estate: master secret scope — ${_ms#SKIP }" ;;
  CANNOT*) cannot "${_ms#CANNOT }" ;;
  FAIL*)   fail "${_ms#FAIL }" ;;
  *)       say "$_ms" ;;
esac

# ⛔ EL RUNBOOK DEL PILOTO NO PUEDE MANDAR SUS COMANDOS A LA ZONA DE PRODUCCIÓN. Este control
# existe porque el mismo defecto apareció TRES veces en dos días sobre el fichero que gobierna
# el primer apply, y la tercera vez estaba a cuatro secciones de la corrección de la segunda:
# corregir una descripción NO arrastra a sus hermanas.
#
# Lo que costaba, medido el 2026-09-02 y no supuesto: siguiendo el runbook tal cual estaba, el
# APPLY 1 habría creado los CNAME del piloto en la zona VIVA —la que lleva los cinco `MX` del
# correo de la casa— con los nombres de PRODUCCIÓN, y el registro de validación de ACM del
# piloto no se habría creado en ninguna parte. Y la cuarta ocurrencia era la peor de todas
# porque NO fallaba: una consulta de ACM filtrada por el dominio de producción devuelve
# VACÍO contra el piloto, y un vacío ahí se lee como «el certificado no está».
#
# ⛔ SE PREGUNTA POR EL RESULTADO, NO POR EL TEXTO, que es lo único que separa esta clase: los
# nombres del piloto se DERIVAN de a qué resuelve el job `apply` cuando nadie teclea nada, y la
# zona de altas se deriva del nombre. Una lista escrita aquí sería el tercer sitio que se
# desincroniza. Y sólo se miran los BLOQUES DE CÓDIGO: la prosa del runbook describe producción
# a propósito, y prohibírselo sería obligarle a callar lo que hay que decir.
_rb="$(python3 - "$ROOT" <<'RBPY'
import os, re, sys
root = sys.argv[1]
wf  = os.path.join(root, ".github/workflows/aws-terraform.yml")
doc = os.path.join(root, "design/AWS-RUNBOOK-DESPACHO-SANDBOX.md")
if not os.path.isdir(os.path.join(root, "design")):
    print("runbook-hostnames-skipped — no design/ in this tree (the export does not carry it)")
    raise SystemExit(0)
for f in (wf, doc):
    if not os.path.isfile(f):
        print("SKIP sin %s, el par no se puede comprobar" % os.path.relpath(f, root))
        raise SystemExit(0)

# ── A que resuelve cada job, que es lo unico que vale ────────────────────────
src = open(wf, encoding="utf-8").read()
def job_body(name):
    m = re.search(r"(?m)^  %s:\n(.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)" % re.escape(name), src, re.S)
    return m.group(1) if m else ""
def resolves(body, key):
    m = re.search(r"(?m)^\s*%s:\s*(.+?)\s*$" % re.escape(key), body)
    if not m:
        return None
    v = m.group(1)
    if "${{" not in v:
        return v.strip().strip("'\"")
    i = v.rfind("||")
    if i < 0:
        return ""
    t = v[i + 2:].strip().rstrip("}").strip().strip("'\"")
    return t

pilot = {k: resolves(job_body("apply"), "TF_VAR_" + k) for k in ("hostname", "ingest_hostname")}
prod  = {k: resolves(job_body("apply-production"), "TF_VAR_" + k) for k in ("hostname", "ingest_hostname")}
if not all(pilot.values()):
    print("CANNOT no he sabido leer a que resuelven los hostnames del job `apply`")
    raise SystemExit(0)

# ── Solo los BLOQUES DE CODIGO del runbook: la prosa describe produccion a proposito ──
lines = open(doc, encoding="utf-8").read().splitlines()
code, inside = [], False
for i, l in enumerate(lines, 1):
    if l.lstrip().startswith("```"):
        inside = not inside
        continue
    if inside:
        code.append((i, l))

rel = os.path.relpath(doc, root)
hallazgos = []
for name in [v for v in prod.values() if v] :
    for i, l in code:
        if name in l:
            hallazgos.append("%s:%d es un COMANDO del runbook del PILOTO y nombra %s, que es de "
                             "PRODUCCION: contra el piloto ese comando no falla, devuelve vacio o "
                             "escribe en la zona equivocada" % (rel, i, name))
# La zona del guion de altas se DERIVA del hostname del piloto, no se teclea.
zona = ".".join(pilot["hostname"].split(".")[-2:])
# ⛔ LA ZONA PUEDE VENIR EN UNA VARIABLE, Y ESO ES MEJOR, NO PEOR. El runbook pasa `"$ZONE"` a
# las tres altas desde que la linea base se toma en el mismo acto (CLOUD-08), asi que un
# literal ya no es la unica forma correcta — pero la variable no se cree a ciegas: se busca su
# ASIGNACION en los mismos bloques de codigo y se compara ESE valor. Un `ZONE=` que nadie
# comprueba seria justo el agujero que esta pata existe para cerrar.
asignada = None
for i, l in code:
    ma = re.match(r"\s*ZONE=([A-Za-z0-9.-]+)\s*$", l)
    if ma:
        asignada = ma.group(1)
        if asignada != zona:
            hallazgos.append("%s:%d asigna ZONE=%s y los nombres del piloto viven en %s: la zona, "
                             "los nombres y la linea base tienen que viajar juntos"
                             % (rel, i, asignada, zona))
for i, l in code:
    m = re.search(r"cloud-dns-add\.sh\s+(\S+)", l)
    if not m:
        continue
    arg = m.group(1).strip('"')
    if arg in ("$ZONE", "${ZONE}"):
        if asignada is None:
            hallazgos.append("%s:%d da de alta en la zona $ZONE y ningun bloque de codigo la "
                             "asigna: una variable sin asignacion visible no es una zona, es una "
                             "suposicion" % (rel, i))
        continue
    if arg != zona:
        hallazgos.append("%s:%d da de alta en la zona %s y los nombres del piloto viven en %s: "
                         "la zona, los nombres y la linea base tienen que viajar juntos"
                         % (rel, i, arg, zona))

# ⛔⛔ Y LA LINEA BASE NO PUEDE LLEVAR FECHA FIJA. Es una FOTO de la zona, y `cloud-dns-add.sh`
# para si la zona ya difiere de ella — asi que una foto de hace dias bloquea el alta sobre una
# zona SANA. Medido el 2026-09-02 (CLOUD-08): la linea base que este runbook citaba registro
# CERO registros y la zona tenia ya TRES `AAAA` legitimos posteriores; la primera alta habria
# muerto con «la zona ya difiere de su linea base».
#
# La cura no es actualizar la fecha —volveria a caducar—: es tomarla EN EL MISMO ACTO. Asi que
# lo que se prohibe aqui es la fecha fija, y lo que se exige es que el bloque la tome.
fechada = re.compile(r"dns-baseline-[A-Za-z0-9.-]+-\d{8}T\d{4,6}Z\.json")
# ⛔ SOBRE TODA LINEA DEL BLOQUE, no solo las que llevan `cloud-dns-add.sh`. El filtro anterior
# exigia que la linea nombrara el guion, y el mutante puso la ruta fechada en una linea de
# CONTINUACION —el comando ocupa tres lineas y el `\` esta en la primera—, asi que la
# comprobacion la saltaba. Un patron que asume que una invocacion cabe en una linea no ve los
# comandos de verdad, que casi nunca caben.
for i, l in code:
    if fechada.search(l):
        hallazgos.append("%s:%d nombra una linea base con FECHA FIJA: una linea base es una foto "
                         "de la zona, y `cloud-dns-add.sh` para si la zona ya difiere de ella, "
                         "asi que una foto vieja bloquea el alta sobre una zona sana. Se toma en "
                         "el mismo acto y se pasa esa ruta" % (rel, i))
if any("cloud-dns-add.sh" in l for _i, l in code):
    if not any(re.match(r"\s*BASE=", l) for _i, l in code):
        hallazgos.append("%s: hay altas de CNAME y ningun bloque de codigo TOMA la linea base: "
                         "sin ese paso, quien siga el runbook usara una foto de otro dia o "
                         "ninguna" % rel)
if hallazgos:
    print("FAIL " + hallazgos[0])
    raise SystemExit(0)
usados = sum(1 for n in pilot.values() for i, l in code if n in l)
print("runbook-hostnames-ok — los comandos del runbook del piloto usan sus %d nombre(s) y la zona "
      "%s, derivados del job `apply` y no copiados" % (len({v for v in pilot.values()}), zona))
RBPY
)"
case "$_rb" in
  SKIP*)   say "check-aws-estate: runbook hostnames — ${_rb#SKIP }" ;;
  CANNOT*) cannot "${_rb#CANNOT }" ;;
  FAIL*)   fail "${_rb#FAIL }" ;;
  *)       say "$_rb" ;;
esac

say "check-aws-estate: CLEAN — 6 modules, ${aws_n} aws_ resource(s), root module args unique, NLB preserve_client_ip, ALB HTTPS+200, --dsn present, Fly descriptors gone, apply is dispatch+confirm+secrets only, OIDC exchange pinned by digest and ordered before tofu, backend locked, images signed on the way to ECR, least-privilege apply policy split into attachable parts, IAM phase declared and verified before tofu, no count/for_each on apply-time values, control-plane env fully supplied, RDS master credential reachable only from the one-shot roles task, pilot runbook commands on the pilot\u0027s own zone."
exit 0
