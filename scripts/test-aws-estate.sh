#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Self-test for check-aws-estate.sh. Each case names the guard it would
# kill if deleted.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)"
CHECK="$ROOT/scripts/check-aws-estate.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/aws-estate.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# A copy of the live tree, so mutants do not touch the worktree.
stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/deploy/aws" \
           "$TMP/tree/cloud/engine" \
           "$TMP/tree/cloud/control-plane" \
           "$TMP/tree/.github/workflows" \
           "$TMP/tree/scripts/hcl-module-guard" \
           "$TMP/tree/scripts/aws-apply-guard"
  cp -a "$ROOT/deploy/aws/." "$TMP/tree/deploy/aws/"
  cp "$CHECK" "$TMP/tree/scripts/check-aws-estate.sh"
  cp "$ROOT/scripts/hcl-module-guard/go.mod" \
     "$ROOT/scripts/hcl-module-guard/go.sum" \
     "$ROOT/scripts/hcl-module-guard/main.go" \
     "$TMP/tree/scripts/hcl-module-guard/"
  mkdir -p "$TMP/tree/scripts/lib"
  cp "$ROOT/scripts/lib/gate-bin-cache.sh" "$TMP/tree/scripts/lib/"
  cp "$ROOT/scripts/aws-iam-phase2.sh" "$TMP/tree/scripts/" 2>/dev/null || true
  cp "$ROOT/scripts/aws-apply-guard/go.mod" \
     "$ROOT/scripts/aws-apply-guard/go.sum" \
     "$ROOT/scripts/aws-apply-guard/main.go" \
     "$TMP/tree/scripts/aws-apply-guard/"
  chmod +x "$TMP/tree/scripts/check-aws-estate.sh"
  if [ -f "$ROOT/.github/workflows/aws-terraform.yml" ]; then
    cp "$ROOT/.github/workflows/aws-terraform.yml" "$TMP/tree/.github/workflows/"
  fi
  if [ -f "$ROOT/.github/workflows/aws-images.yml" ]; then
    cp "$ROOT/.github/workflows/aws-images.yml" "$TMP/tree/.github/workflows/"
  fi
  mkdir -p "$TMP/tree/cloud/control-plane/internal/config"
  cp "$ROOT/cloud/control-plane/internal/config/config.go" \
     "$TMP/tree/cloud/control-plane/internal/config/" 2>/dev/null || true
  mkdir -p "$TMP/tree/design"
  cp "$ROOT"/design/aws-apply-role-policy.sandbox*.json "$TMP/tree/design/" 2>/dev/null || true
}

# ⛔ UN MUTANTE QUE NO SE APLICA ACUSA AL GATE DE CIEGO. Mutar YAML con `sed` es como se
# escriben los falsos verdes de esta casa: una regex sobre un árbol indentado acierta en
# el sitio equivocado, el caso «pasa» y nadie mutó nada. `subst` EXIGE que el ancla exista
# y sale 1 si no está, así que un mutante que deja de aplicar tumba la batería en vez de
# aprobarla.
#
# ⚠ Y no usa PyYAML: no está en ningún contenedor de este proyecto ni hay `pip`
# (`scripts/check-ci-env-reach.sh:17-23`, medido el 2026-08-19). Python sí está, y aquí
# sólo hace sustitución de texto anclada — el que parsea YAML de verdad es el guard en Go.
subst() { # subst <fichero> <ancla> <reemplazo> — una vez, y falla si el ancla no existe
  python3 "$ROOT/scripts/lib/subst-once.py" "$1" "$2" "$3"
}

# ⛔ UN `rc` SOLO NO PRUEBA QUE EL MUTANTE MURIERA POR SU PROPIA CAUSA. Un caso que
# acepta «cualquier cosa menos 0» acaba testificando sobre el ENTORNO: un YAML que el
# mutante rompió, un `go build` que no encontró la red, un fichero que no estaba — los
# tres dan rc≠0 y ninguno prueba la invariante que el caso dice probar. Así que cada
# caso nuevo compara el rc EXACTO **y** exige la frase que nombra a su guarda.
expect() { # expect <rc-esperado> <trozo-de-frase> <rótulo>
  local want="$1" needle="$2" label="$3" got
  run || true
  got="$(cat "$TMP/rc")"
  if [ "$got" != "$want" ]; then
    bad "$label — rc=$got, want $want ($(head -c 400 "$TMP/err"))"
    return
  fi
  if [ "$want" != 0 ] && ! command grep -qF -- "$needle" "$TMP/err"; then
    bad "$label — rc=$want but the message does not name its guard; got: $(head -c 400 "$TMP/err")"
    return
  fi
  ok "$label"
}

run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-aws-estate.sh" >/dev/null 2>"$TMP/err" || rc=$?
  printf '%s\n' "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then
  ok "the live shape is CLEAN"
else
  bad "the live shape should be CLEAN ($(cat "$TMP/err"))"
fi

stage
rm -rf "$TMP/tree/deploy/aws/modules/ingress"
expect 1 "the six ratified modules are not all present" "missing module is a finding (mutant: drop a directory)"

stage
# Strip every aws_ resource.
find "$TMP/tree/deploy/aws" -name '*.tf' -exec sed -i 's/resource "aws_/resource "notaws_/g' {} +
expect 1 "zero resource \"aws_\" blocks under deploy/aws" "zero aws_ resources is a finding (control positive)"

stage
sed -i 's/preserve_client_ip = true/preserve_client_ip = false/' \
  "$TMP/tree/deploy/aws/modules/ingress/main.tf"
expect 1 "NLB target group does not set preserve_client_ip = true" "NLB preserve_client_ip=false is a finding"

stage
sed -i 's/proxy_protocol_v2 *= *true/proxy_protocol_v2 = false/' \
  "$TMP/tree/deploy/aws/modules/ingress/main.tf"
expect 1 "NLB target group does not enable proxy_protocol_v2" "NLB proxy_protocol_v2=false is a finding (C04-03)"

stage
# Duplicate argument in the ROOT module block — the 2026-08-20 main.tf
# listed source twice and brace-balance still said CLEAN.
python3 - "$TMP/tree/deploy/aws/main.tf" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read()
old = '  connection_logs_bucket = module.data.alb_conn_bucket_id\n'
new = old + '  source                 = "./modules/ingress"\n'
if old not in text:
    raise SystemExit("connection_logs_bucket line missing")
open(p, "w", encoding="utf-8").write(text.replace(old, new, 1))
PY
expect 1 "invalid HCL" "duplicate root module argument is a finding"

stage
# Counterfactual from the exact-SHA audit of #1490. The former regex stopped at
# the first closing brace in column zero, so this balanced map hid the repeated
# source that follows it.
python3 - "$TMP/tree/deploy/aws/main.tf" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read()
old = '  connection_logs_bucket = module.data.alb_conn_bucket_id\n'
new = old + '''  tags = {
    audit = "counterfactual"
}
  source = "./modules/ingress"
'''
if old not in text:
    raise SystemExit("connection_logs_bucket line missing")
open(p, "w", encoding="utf-8").write(text.replace(old, new, 1))
PY
run || true
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "real HCL traversal finds a duplicate after a nested map"
else
  bad "nested-map duplicate source rc=$(cat "$TMP/rc"), want 1 ($(cat "$TMP/err"))"
fi

stage
cat >"$TMP/tree/deploy/aws/duplicate.tf" <<'TF'
module "ingress" {
  source                 = "./modules/ingress"
  access_logs_bucket     = module.data.plane_bucket_id
  connection_logs_bucket = module.data.alb_conn_bucket_id
}
TF
run || true
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "duplicate module labels across root .tf files are a finding"
else
  bad "cross-file duplicate module rc=$(cat "$TMP/rc"), want 1 ($(cat "$TMP/err"))"
fi

stage
printf '%s\n' '{"module":{"ingress":{"source":"./modules/ingress"}}}' \
  >"$TMP/tree/deploy/aws/duplicate.tf.json"
run || true
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "root .tf.json cannot bypass the native HCL guard"
else
  bad "root .tf.json rc=$(cat "$TMP/rc"), want 1 ($(cat "$TMP/err"))"
fi

stage
# Names in comments are not active wiring. Both module variables default to an
# empty string, which disables the corresponding log blocks.
sed -i \
  -e 's/^  access_logs_bucket /# access_logs_bucket /' \
  -e 's/^  connection_logs_bucket /# connection_logs_bucket /' \
  "$TMP/tree/deploy/aws/main.tf"
run || true
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "comment-only ingress log buckets are a finding"
else
  bad "comment-only log buckets rc=$(cat "$TMP/rc"), want 1 ($(cat "$TMP/err"))"
fi

stage
sed -i \
  -e 's/module.data.plane_bucket_id/""/' \
  -e 's/module.data.alb_conn_bucket_id/""/' \
  "$TMP/tree/deploy/aws/main.tf"
run || true
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "empty ingress log buckets are a finding"
else
  bad "empty log buckets rc=$(cat "$TMP/rc"), want 1 ($(cat "$TMP/err"))"
fi

stage
# Whitespace and indentation do not change the HCL body. A structural guard
# must accept an active argument even when it is not aligned with its siblings.
sed -i 's/^  access_logs_bucket     =/access_logs_bucket=/' \
  "$TMP/tree/deploy/aws/main.tf"
if run; then
  ok "no-fire: HCL argument formatting does not change active wiring"
else
  bad "format-only HCL change fired rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# Un .tf que no cierra sus bloques. El gate certifica el estado a base de grep,
# y un grep encuentra su patron igual en un fichero valido que en uno roto.
stage
sed -i '0,/^}$/{/^}$/d}' "$TMP/tree/deploy/aws/modules/data/outputs.tf"
if run; then
  bad "un .tf con una llave sin cerrar stayed CLEAN"
else
  ok "un .tf desbalanceado is a finding"
fi

stage
sed -i 's/protocol *= *"HTTPS"/protocol = "HTTP"/' \
  "$TMP/tree/deploy/aws/modules/ingress/main.tf"
expect 1 "ALB target group is not HTTPS" "ALB HTTP target (no TLS to the task) is a finding"

stage
sed -i 's/--dsn/--not-dsn/' "$TMP/tree/deploy/aws/modules/compute/main.tf"
expect 1 "compute task definition does not pass --dsn" "missing --dsn is a finding (the retired Fly start)"

stage
printf 'app = "x"\n' > "$TMP/tree/cloud/engine/fly.toml"
expect 1 "retired Fly descriptor still present: cloud/engine/fly.toml" "retired Fly descriptor is a finding"

stage
printf 'jobs:\n  x:\n    run: tofu apply -auto-approve\n' \
  > "$TMP/tree/.github/workflows/aws-terraform.yml"
expect 1 "apply exists without workflow_dispatch" "unguarded tofu apply is a finding (mutant: apply with no dispatch/confirm)"

stage
# Apply inside the push/PR job. The dispatch job may apply; this one must not.
sed -i 's/tofu validate/tofu apply -auto-approve/' \
  "$TMP/tree/.github/workflows/aws-terraform.yml"
expect 1 "validate job contains an apply" "apply in the validate job is a finding (mutant: push path applies)"

stage
sed -i "s/apply-sandbox-estate/please-apply/g" \
  "$TMP/tree/.github/workflows/aws-terraform.yml"
expect 1 "apply exists without the confirmation token apply-sandbox-estate" "apply without apply-sandbox-estate is a finding"

stage
rm "$TMP/tree/scripts/hcl-module-guard/main.go"
run || true
if [ "$(cat "$TMP/rc")" = 2 ]; then
  ok "missing HCL parser source is COULD NOT LOOK"
else
  bad "missing HCL parser rc=$(cat "$TMP/rc"), want 2 ($(cat "$TMP/err"))"
fi

stage
rm -rf "$TMP/tree/deploy/aws"
if run; then
  bad "missing deploy/aws stayed CLEAN"
else
  rc=$?
  # `grep -q` sobre un FICHERO no tiene productor que matar, pero el lint no puede distinguirlo
  # del caso de tubería sin leer el contexto, y una regla que se salta por contexto deja de ser
  # regla. Se lee el fichero y se decide sobre la cadena: mismo veredicto, sin la forma vigilada.
  _err="$(cat "$TMP/err" 2>/dev/null || true)"
  case "$_err" in *'COULD NOT LOOK'*) _sinmirar=1 ;; *) _sinmirar=0 ;; esac
  if [ "$rc" -eq 2 ] || [ "$_sinmirar" -eq 1 ]; then
    ok "missing deploy/aws is COULD NOT LOOK"
  else
    bad "missing deploy/aws should be exit 2, got $rc"
  fi
fi


# ═══ EL CANJE OIDC DEL JOB `apply` ═══════════════════════════════════════════
#
# ⛔ EL PRIMER CASO ES EL DEFECTO QUE OCURRIÓ DE VERDAD, no una variante inventada.
# Hasta el 2026-08-27 el job `apply` pedía `id-token: write` y donde tenía que ir el paso
# había ESTE comentario: «OIDC pin is the integrator's: this lote does not invent a
# configure-aws-credentials digest». Un gate escrito como «el fichero menciona
# configure-aws-credentials» lo habría aprobado. Éste tiene que rechazarlo.
WF_T="$TMP/tree/.github/workflows/aws-terraform.yml"
WF_I="$TMP/tree/.github/workflows/aws-images.yml"

stage
subst "$WF_T" \
  '      - name: assume the sandbox apply role (OIDC → STS)
        uses: aws-actions/configure-aws-credentials@' \
  '      # OIDC pin is the integrator: this lote does not invent a
      # configure-aws-credentials digest.
      # uses: aws-actions/configure-aws-credentials@'
expect 1 "has no aws-actions/configure-aws-credentials step" "the credential exchange living only in a COMMENT is a finding (the 2026-08-27 defect itself)"

stage
subst "$WF_T" \
  'aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3' \
  'aws-actions/configure-aws-credentials@v6'
expect 1 "which is not a 40-hex commit OID" "a moving TAG instead of a commit digest is a finding"

stage
subst "$WF_T" '          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1' \
              '          role-to-assume: arn:aws:iam::000000000000:role/whatever
          aws-region: us-east-1'
expect 1 "which does not come from AWS_ROLE_ARN" "a role that does not come from AWS_ROLE_ARN is a finding"

stage
subst "$WF_T" '          aws-region: us-east-1
          # El nombre de sesión' '          # El nombre de sesión'
expect 1 "configure-aws-credentials has no aws-region" "the exchange without aws-region is a finding"

stage
subst "$WF_T" '    permissions:
      contents: read
      id-token: write' '    permissions:
      contents: read'
expect 1 "does not request id-token: write" "apply without id-token: write is a finding (the OIDC token cannot be minted)"

# EL ORDEN ES LA MITAD DE LA INVARIANTE. Un canje presente pero colocado DESPUÉS del
# `tofu init` satisface cualquier comprobación de presencia y no sirve absolutamente
# de nada: el backend S3 se lee antes.
stage
python3 "$ROOT/scripts/lib/subst-once.py" --move-step "$WF_T" 'assume the sandbox apply role'
expect 1 "BEFORE the credential exchange" "the exchange placed AFTER tofu is a finding (ordering, not presence)"

# ⛔ LA DIRECCIÓN DE NO DISPARO QUE IMPORTA: el job de push/PR no toma credenciales.
# Sin este caso, «cablear OIDC» se podría satisfacer poniéndolo en el job equivocado —
# el que dispara cualquier rama — y el gate diría CLEAN.
stage
subst "$WF_T" '      - name: estate shape (no apply)' \
  '      - name: sneak credentials into the push path
        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3
        with:
          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1
      - name: estate shape (no apply)'
expect 1 'assumes an AWS role, but only "apply" may' "credentials in the validate job (push/PR path) are a finding"

# ═══ BLOQUEO DE ESTADO DEL BACKEND — AHORA EN HCL, NO EN TEXTO DE SHELL ══════
#
# ⛔ AQUÍ HABÍA UN CASO QUE MUTABA EL `-backend-config` DEL WORKFLOW, y se retira JUNTO CON
# la invariante que probaba: el contraste `sol max` (C-01) demostró que esa forma la fingía
# un `echo`. El bloqueo vive ahora en `deploy/aws/versions.tf` y lo verifica el parser HCL;
# sus mutantes de disparo están abajo, con los del contraste.

# NO DISPARO: DynamoDB es la otra forma legítima de bloquear. El gate exige BLOQUEO, no una
# implementación concreta — si exigiera `use_lockfile` obligaría a rediseñar a quien
# eligiese la tabla con su razón escrita.
stage
subst "$TMP/tree/deploy/aws/versions.tf" '    use_lockfile = true' '    dynamodb_table = "olivares-tflock"'
if run; then
  ok "no-fire: a DynamoDB lock table is also state locking"
else
  bad "DynamoDB locking fired rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# ═══ EL CAMINO A ECR ═════════════════════════════════════════════════════════
stage
rm -f "$WF_I"
expect 1 "no .github/workflows/aws-images.yml" "no aws-images.yml is a finding (the ECR repository would have nothing to hold)"

stage
subst "$WF_I" 'on:
  workflow_dispatch:' 'on:
  push:
    tags: ["v*"]
  workflow_dispatch:'
expect 1 "the only trigger that may reach ECR is workflow_dispatch" "a tag push that would reach ECR is a finding (orden 12: nothing automatic touches AWS)"

stage
subst "$WF_I" "github.event.inputs.confirm == 'push-images-to-ecr'" "true"
expect 1 "does not require the confirmation token push-images-to-ecr" "publishing to ECR without the confirmation token is a finding"

stage
subst "$WF_I" '          bash scripts/cosign-verified.sh sign --yes --upload=true "${CP_REF}@${CP_DIGEST}"' \
             '          echo "not signing the control plane today"'
subst "$WF_I" '          bash scripts/cosign-verified.sh sign --yes --upload=true "${ENGINE_REF}@${ENGINE_DIGEST}"' \
             '          echo "not signing the engine today"'
expect 1 "publishes without a cosign-verified.sh sign COMMAND" "pushing unsigned images is a finding"

stage
subst "$WF_I" '        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3' \
             '        uses: aws-actions/configure-aws-credentials@main'
expect 1 "which is not a 40-hex commit OID" "a branch pin on the ECR path is a finding"

# ═══ LA TERCERA RESPUESTA, POR SU PROPIO CAMINO ══════════════════════════════
stage
rm "$TMP/tree/scripts/aws-apply-guard/main.go"
expect 2 "missing workflow parser source" "missing workflow parser source is COULD NOT LOOK, not a finding"

# NO DISPARO, Y ES UN PUNTO CIEGO DECLARADO: el comentario `# vN.N.N` es documentación
# para quien bumpee, no la invariante. Este guard comprueba la FORMA del digest y NO
# puede comprobar que sea el commit que la etiqueta nombra — eso exige red, y un gate
# del carril rápido no la tiene. Se escribe como caso para que nadie lea el verde como
# si sí lo comprobara.
stage
subst "$WF_T" '@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3' \
             '@e6de054238d6b7531b4efff3b6587d9aade6a06c # v9.9.9-inventada'
if run; then
  ok "declared blind spot: a wrong version COMMENT does not fire (the digest is what is pinned)"
else
  bad "version comment fired rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi


# ═══ LA POLICY QUE SUSTITUYE A AdministratorAccess ═══════════════════════════
POL_G="$TMP/tree/design/aws-apply-role-policy.sandbox.0-guardrails.json"
POL_C="$TMP/tree/design/aws-apply-role-policy.sandbox.3-compute-and-edge.json"

stage
rm -f "$TMP/tree"/design/aws-apply-role-policy.sandbox*.json
expect 1 "no design/aws-apply-role-policy.sandbox*.json" \
  "no least-privilege policy at all is a finding (AdministratorAccess would stay by default)"

# ⛔ EL MUTANTE QUE JUSTIFICA TODO EL PARTIDO: una pieza por encima del tope de IAM no se
# puede adjuntar, y AWS sólo lo dice cuando ya está pegando. 6 144 caracteres sin
# espacios (reference_iam-quotas.html). Se infla con una acción larga repetida, no con
# relleno: el mutante tiene que ser JSON válido y una policy plausible.
stage
python3 - "$POL_C" <<'INFLATE'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
st = d["Statement"][0]
acts = st["Action"] if isinstance(st["Action"], list) else [st["Action"]]
st["Action"] = acts + ["ecr:DescribeImageReplicationStatus%03d" % i for i in range(200)]
open(p, "w", encoding="utf-8").write(json.dumps(d, indent=2) + "\n")
INFLATE
expect 1 "over the 6144-character managed-policy quota" \
  "a part over the IAM managed-policy quota is a finding (it cannot be attached at all)"

stage
subst "$POL_C" '      "Action": [
        "ecr:*"
      ],' '      "Action": "*",'
expect 1 'allows Action "*" — that IS AdministratorAccess' \
  "Action \"*\" is a finding (renaming AdministratorAccess is not replacing it)"

stage
# ⛔ EL IDENTIFICADOR DE CUENTA SE DERIVA DEL PROPIO FICHERO, NO SE ESCRIBE AQUÍ. `scripts/`
# viaja al árbol público y el número de cuenta es identidad de infraestructura: escribirlo lo
# publica. Derivarlo tiene además la ventaja de que el caso sobrevive a un cambio de cuenta,
# que un literal no.
_acct="$(python3 -c 'import re,sys;print(re.search(r"arn:aws:ecr:[a-z0-9-]+:([0-9]{12}):", open(sys.argv[1]).read()).group(1))' "$POL_C")"
subst "$POL_C" '        "ecr:*"
      ],
      "Resource": "arn:aws:ecr:us-east-1:'"$_acct"':repository/olivares-pilot*"' \
              '        "ecr:*"
      ],
      "Resource": "*"'
expect 1 'on Resource "*" — a service-wide wildcard with no resource bound' \
  "a service-wide wildcard over every resource is a finding"

stage
# ⛔ EL IDENTIFICADOR DE CUENTA SE DERIVA DEL PROPIO FICHERO, NO SE ESCRIBE AQUÍ. `scripts/`
# viaja al árbol público y el número de cuenta es identidad de infraestructura: escribirlo lo
# publica. Derivarlo tiene además la ventaja de que el caso sobrevive a un cambio de cuenta,
# que un literal no.
_acct="$(python3 -c 'import re,sys;print(re.search(r"arn:aws:ecr:[a-z0-9-]+:([0-9]{12}):", open(sys.argv[1]).read()).group(1))' "$POL_C")"
subst "$POL_C" "arn:aws:ecr:us-east-1:${_acct}:repository/olivares-pilot*" \
              'arn:aws:ecr:us-east-1:999999999999:repository/olivares-pilot*'
expect 1 "different AWS accounts" \
  "a part pointing at another AWS account is a finding"

stage
rm -f "$POL_G"
expect 1 "no Deny statement anywhere in the set" \
  "losing the guardrail Denies is a finding (the role could rewrite its own trust)"

stage
subst "$POL_C" '"Version": "2012-10-17"' '"Version": "2008-10-17"'
expect 1 "has no 2012-10-17 Version" \
  "a policy version IAM would refuse is a finding"


# ═══ LOS SEIS HUECOS QUE ENCONTRÓ LA PASADA ADVERSARIAL SOBRE EL PROPIO GUARD ═════
#
# No salieron de un contraste externo: salieron de preguntarle al guard «¿qué construcción
# de Actions, LEGÍTIMA Y EJECUTABLE, se te escapa?». Cada uno tiene su caso porque un
# hueco cerrado sin mutante es una afirmación, no una verificación.

# 1 · Un canje CONDICIONAL puede no ocurrir, y el paso siguiente corre igual.
stage
subst "$WF_T" '        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3
        with:
          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1' \
  '        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3
        if: env.SOMETHING == '"'"'yes'"'"'
        with:
          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1'
expect 1 "guards the credential exchange with" \
  "an if: on the credential exchange is a finding (a skipped exchange is no exchange)"

# 2 · `continue-on-error` se traga el fallo del canje y deja correr al apply.
stage
subst "$WF_T" '        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3
        with:' \
  '        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3
        continue-on-error: true
        with:'
expect 1 "sets continue-on-error on the credential exchange" \
  "continue-on-error on the exchange is a finding (a failed exchange would not stop the apply)"

# 3 · DOS pasos de credenciales: el segundo decide qué rol queda puesto, así que juzgar
#     sólo el primero deja el que manda sin mirar.
stage
subst "$WF_T" '      - name: tofu apply (sandbox estate only)' \
  '      - name: a second exchange nobody looked at
        uses: aws-actions/configure-aws-credentials@e6de054238d6b7531b4efff3b6587d9aade6a06c # v6.2.3
        with:
          role-to-assume: arn:aws:iam::000000000000:role/somebody-elses
          aws-region: us-east-1
      - name: tofu apply (sandbox estate only)'
expect 1 "which does not come from AWS_ROLE_ARN" \
  "a SECOND credential step is judged too (the last one wins at runtime)"

# 4 · NO DISPARO: el nombre de una acción de GitHub no distingue mayúsculas, así que una
#     grafía legítima no puede salir roja. Sin esto el guard rechazaba trabajo correcto.
stage
subst "$WF_T" 'uses: aws-actions/configure-aws-credentials@' 'uses: AWS-Actions/Configure-AWS-Credentials@'
if run; then
  ok "no-fire: the action name is matched case-insensitively, as GitHub resolves it"
else
  bad "case-variant action name fired rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# 5 · NO DISPARO, Y ES EL FALSO POSITIVO QUE ME MORDIÓ A MÍ: el paso «install OpenTofu»
#     termina en `tofu version`, y una versión anterior de este guard lo contaba como un
#     paso que necesita credenciales — acusando al orden de estar mal estándolo bien.
#     `version` no lee el backend. El caso fija esa frontera.
#
#     ⚠ Y desde el 2026-08-28 el binario se invoca por su RUTA (`$RUNNER_TEMP/bin/tofu`),
#     porque el paso dejó de usar `sudo`. El ancla se actualizó con él: un mutante que deja
#     de aplicarse acusa al gate de ciego, y este caso es de NO disparo, o sea que el fallo
#     habría sido un verde silencioso.
stage
subst "$WF_T" '          "$RUNNER_TEMP/bin/tofu" version' '          "$RUNNER_TEMP/bin/tofu" version
          tofu -help'
if run; then
  ok "no-fire: tofu version/-help do not need credentials and do not count for ordering"
else
  bad "tofu version counted as a credentialed step rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# 6 · La forma con banderas intercaladas y con continuación de línea. Las dos existen y la
#     primera versión del regex las perdía enteras: el binario en una línea, el subcomando
#     en la siguiente, y ningún paso de tofu detectado ⇒ ni orden ni bloqueo comprobados.
stage
python3 "$ROOT/scripts/lib/subst-once.py" --move-step "$WF_T" 'assume the sandbox apply role'
subst "$WF_T" '          tofu apply -input=false -auto-approve' \
             '          tofu \
            -chdir=. \
            apply -input=false -auto-approve'
expect 1 "BEFORE the credential exchange" \
  "tofu backslash-newline -chdir=. apply still counts for the ordering invariant"


# ═══ LA CACHÉ DE BINARIOS NO PUEDE SERVIR UN GUARD RANCIO ════════════════════
#
# ⛔ Los helpers Go se construyen UNA vez por CONTENIDO (`scripts/lib/gate-bin-cache.sh`),
# porque construirlos una vez por invocación costaba 5 min 20 s de CPU en esta batería y
# eso lo paga el `pre-push` de todos los carriles. Una caché mal indexada convertiría el
# guard en un adorno: seguiría corriendo, con el binario de ayer. Este caso lo prueba por
# el único camino que no se puede fingir — muta la FRASE que el guard imprime y exige que
# la nueva salga. Si la caché sirviera el binario viejo, saldría la vieja.
stage
# ⛔ EL ANCLA VA AL `Printf`, NO A LA CADENA SUELTA. La primera versión decía
# `'apply-wiring-ok'` a secas y `subst` sustituye la PRIMERA aparición: desde que el guard
# documenta en sus comentarios lo que el contraste midió, la primera aparición es un
# COMENTARIO. El binario se reconstruía —correctamente— y el mensaje no cambiaba, así que
# el caso acusaba a la caché de servir un binario rancio que no estaba sirviendo. Es la
# misma clase que esta rama entera persigue: un ancla que casa con la prosa y no con el
# código. Lo cazó el propio caso antes de publicar.
subst "$TMP/tree/scripts/aws-apply-guard/main.go" '"%s: apply-wiring-ok — privileged' '"%s: apply-wiring-REBUILT — privileged'
run || true
# ⛔ SIN TUBERÍA QUE ACABE EN `grep -q`, y me mordió al escribir este caso: bajo `pipefail`
# el consumidor cierra al primer acierto, el productor muere con SIGPIPE y la tubería sale
# 141 EN ÉXITO. Es la misma trampa que `check-aws-estate.sh` documenta sobre sí mismo doce
# líneas más abajo de su propio `desired_count`. Se captura primero y se decide sobre la
# cadena.
_rebuilt_out="$(OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-aws-estate.sh" 2>/dev/null || true)"
case "$_rebuilt_out" in
*apply-wiring-REBUILT*) _rebuilt=1 ;;
*) _rebuilt=0 ;;
esac
if [ "$(cat "$TMP/rc")" = 0 ] && [ "$_rebuilt" -eq 1 ]; then
  ok "a mutated guard is REBUILT, not served from the binary cache"
else
  bad "the binary cache served a stale guard (rc=$(cat "$TMP/rc"), rebuilt=$_rebuilt)"
fi


# ═══ LA PUERTA (`needs:`), QUE ERA UN COMENTARIO Y NO UN CONTROL ═════════════
#
# ⛔ `aws-terraform.yml` llevaba escrito, con todas las letras, que sin `needs: validate`
# «un dispatch confirmado podía APLICAR SOBRE AWS con el gate del estate en rojo … el
# efecto externo ya se había producido». Ese razonamiento vivía en un COMENTARIO y no lo
# comprobaba nadie: borrar la línea dejaba el gate en verde. Un diagnóstico en un
# comentario no es un control.
stage
subst "$WF_T" '    needs: validate
    if: github.event_name' '    if: github.event_name'
expect 1 'job "apply" does not declare `needs: validate`' \
  "apply without needs: validate is a finding (an if decides WHETHER, needs decides WHEN)"

stage
subst "$WF_I" '  push:
    needs: validate' '  push:'
expect 1 'job "push" does not declare `needs: validate`' \
  "the ECR push without needs: validate is a finding (it would publish over a red gate)"

# ⛔ `design/` NO SE EXPORTA y este guion SÍ. Un árbol público con `deploy/aws/` y
# `scripts/` pero sin `design/` no es un árbol al que le falte la policy: es un árbol al
# que esa pregunta no se le hace. Exigirla allí haría un gate que nadie puede poner en
# verde fuera del hub — la forma de gate que más veces se ha roto en esta casa.
stage
rm -rf "$TMP/tree/design"
if run; then
  ok "no-fire: a tree without design/ (the export) is not a tree missing the policy"
else
  bad "the export tree cannot pass the estate gate rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# Y la contraparte, para que el no-disparo de arriba no se lea como una puerta trasera:
# con `design/` PRESENTE y las piezas fuera, sigue siendo hallazgo. Ese caso ya existe
# arriba («no least-privilege policy at all»), y esta línea sólo dice dónde mirar.


# ═══ LOS MUTANTES DEL CONTRASTE `sol max` DEL 2026-08-27 ═════════════════════
#
# ⛔ CADA UNO ES UN FALSO VERDE QUE EL CONTRASTE MIDIÓ SOBRE ESTE GUARD, no una variante
# inventada. Su informe (`an internal design note (not shipped)`) los
# reprodujo uno a uno con `go run` y una mutación en memoria, y los seis salieron
# `apply-wiring-ok`. Están aquí porque un arreglo sin su mutante es una afirmación.

# C-01/a · BORRAR LA PUERTA ENTERA del job `apply`.
#
# ⚠ Y AQUÍ SE DICE QUÉ GUARDA LO CAZA, PORQUE NO ES LA NUEVA. Borrar la línea entera se
# lleva por delante `github.event_name == 'workflow_dispatch'`, y eso ya lo exigía —desde
# antes de esta rama— la comprobación de `grep` de `check-aws-estate.sh`, que corre PRIMERO
# y sale antes de que el guard en Go llegue a mirar. El caso se queda con la frase de la
# guarda que de verdad muerde: un caso que espera otra estaría verde por la razón
# equivocada, que es justo lo que la comprobación de frase existe para impedir.
# El invariante NUEVO —la condición completa— lo ejercita el caso de abajo, que INVIERTE el
# predicado conservando esa cadena y por tanto pasa por delante de la guarda de shell.
stage
subst "$WF_T" "    if: github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'apply-sandbox-estate'
" ""
expect 1 "apply job is not limited to workflow_dispatch" \
  "deleting the apply confirmation condition is a finding (caught by the shell layer, first)"

# C-01/a-bis · LA MISMA SUPRESIÓN EN EL WORKFLOW DE IMÁGENES, donde NO hay guarda de shell:
# aquí el único que puede verlo es el invariante nuevo.
stage
subst "$WF_I" "    if: github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'push-images-to-ecr'
" ""
expect 1 "the only condition that may open it is" \
  "deleting the ECR confirmation condition is a finding (only the new exact-if invariant sees it)"

# C-01/b · NEGAR EL PREDICADO conservando todas las palabras. Una comprobación por
# subcadena —la forma natural de escribirla— acepta esto.
stage
subst "$WF_T" "github.event.inputs.confirm == 'apply-sandbox-estate'" \
              "github.event.inputs.confirm != 'apply-sandbox-estate'"
expect 1 "the only condition that may open it is" \
  "inverting the confirmation predicate is a finding (same words, opposite meaning)"

# C-01/c · FINGIR EL BLOQUEO CON UN `echo`. Ya no se puede: la invariante vive en HCL.
stage
subst "$TMP/tree/deploy/aws/versions.tf" "    use_lockfile = true" "    use_lockfile = false"
expect 1 "declares no state locking" \
  "use_lockfile = false in HCL is a finding (an echo can no longer fake it)"

stage
subst "$TMP/tree/deploy/aws/versions.tf" '  backend "s3" {
    use_lockfile = true
  }' '  backend "s3" {}'
expect 1 "declares no state locking" \
  "a backend with no locking at all is a finding"

# C-01/d · FINGIR LA FIRMA CON UN `echo`. El guard exigía una SUBCADENA en el `run`.
stage
subst "$WF_I" '          bash scripts/cosign-verified.sh sign --yes --upload=true "${CP_REF}@${CP_DIGEST}"' \
             '          echo "bash scripts/cosign-verified.sh sign --yes --upload=true (not executed)"'
expect 1 "signs without an explicit --upload=true on both images" \
  "a cosign call replaced by an echo is a finding (C-01: mention is not invocation)"

# C-01/e · UNA PUERTA VACÍA. `needs: validate` seguía valiendo con un `validate` que no
# corría el gate: se comprobaba el NOMBRE.
stage
subst "$WF_I" '      - name: estate shape and delivery wiring (no apply, no AWS)
        run: bash scripts/check-aws-estate.sh' \
             '      - name: a gate that gates nothing
        run: echo "check-aws-estate.sh"'
expect 1 "runs no \`check-aws-estate.sh\` command" \
  "a validate job that does not RUN the gate is a finding (C-01: the name is not the gate)"

# C-02/a · UNA MATRIZ elige el rol y el guard lee una sola forma.
stage
subst "$WF_T" '  apply:' '  apply:
    strategy:
      matrix:
        AWS_ROLE_ARN: ["arn:aws:iam::1:role/a", "arn:aws:iam::2:role/b"]'
expect 1 "declares a \`strategy\`" \
  "a matrix on the privileged job is a finding (C-02: it chose the assumed role)"

# C-02/b · UN `env` SUSTITUYE LA CADENA DE CREDENCIALES que el canje acaba de poner.
stage
subst "$WF_T" '      TF_VAR_hostname:' '      AWS_ACCESS_KEY_ID: ${{ secrets.SOMETHING_ELSE }}
      TF_VAR_hostname:'
expect 1 "overrides the credential chain the OIDC exchange just installed" \
  "a credential env override in the privileged job is a finding (C-02)"

stage
subst "$WF_I" '          CERT_OIDC_ISSUER: https://token.actions.githubusercontent.com' \
             '          CERT_OIDC_ISSUER: https://token.actions.githubusercontent.com
          COSIGN_REPOSITORY: someone/else'
expect 1 "overrides the credential chain the OIDC exchange just installed" \
  "COSIGN_REPOSITORY as step env is a finding (it redirects where the signature lands)"

# C-03 · `continue-on-error` DE JOB: el workflow pasa con el job privilegiado en rojo.
stage
subst "$WF_T" '    needs: validate' '    continue-on-error: true
    needs: validate'
run || true
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "job-level continue-on-error is a finding (C-03: green over a partial apply)"
else
  bad "job-level continue-on-error rc=$(cat "$TMP/rc"), want 1 ($(cat "$TMP/err"))"
fi

# C-02/c · UN WORKFLOW REUTILIZABLE es código que este guard no lee, con estos permisos.
stage
# export-closure: fixture .github/workflows/somewhere-else.yml — no existe ni en el export ni en el
# hub, y no debe existir: es el CUERPO del mutante, la ruta que un job privilegiado NO puede
# delegar. Si algún día ese fichero apareciera de verdad, dejaría de ser un fixture y pasaría a ser
# una dependencia — que es justo lo que `check-export-closure.sh:556-557` distingue.
subst "$WF_I" '  push:
    needs: validate' '  push:
    needs: validate
    uses: ./.github/workflows/somewhere-else.yml'
expect 1 "is a reusable workflow" \
  "a privileged job that delegates to a reusable workflow is a finding (C-02)"

# E-01 · FIRMAR SIN PUBLICAR, EN VERDE. `--upload` vale true por defecto SÓLO si el flag no
# es explícito; cosign v2 vincula `COSIGN_UPLOAD` cuando falta.
stage
subst "$WF_I" '--yes --upload=true "${ENGINE_REF}@${ENGINE_DIGEST}"' '--yes "${ENGINE_REF}@${ENGINE_DIGEST}"'
expect 1 "signs without an explicit --upload=true on both images" \
  "dropping --upload=true on ONE image is a finding (E-01: 1 of 2 is not 2 of 2)"

# E-01/b · FIRMAR Y NO LEER DE VUELTA es una afirmación, no una prueba.
stage
subst "$WF_I" '          bash scripts/cosign-verified.sh verify \' '          : skip-verify \'
subst "$WF_I" '          bash scripts/cosign-verified.sh verify \' '          : skip-verify \'
expect 1 "never reads the signature back" \
  "signing without a verify read-back is a finding (E-01)"

# ═══ LA FASE 2 DE IAM: EL GUION QUE LA HACE Y EL PASO QUE LA VERIFICA ════════
#
# ⛔ Los cinco primeros son sobre el FICHERO y los cuatro últimos sobre el WORKFLOW, y
# están juntos porque son UNA invariante repartida en dos sitios: un rol que ya no lleva
# AdministratorAccess sólo sigue sirviendo si algo lo comprueba antes de cada apply.

# G-01 · Sin guion no hay ni transición ni verificación, y el gate lo tiene que decir.
stage
rm -f "$TMP/tree/scripts/aws-iam-phase2.sh"
expect 1 "scripts/aws-iam-phase2.sh is missing" \
  "no phase-2 script at all is a finding (the estate documents a phase nothing performs)"

# G-02 · El envoltorio SIN su guarda: `check` —lo que corre el pipeline— pasaría a escribir.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  '[ "$MODE" = check ] && fail "BUG: se ha intentado escribir en modo check ($*)"' \
  ': # sin guarda'
expect 1 "is not INSIDE the aws_write body" \
  "gutting the aws_write guard is a finding (check runs on every dispatch)"

# G-03 · Un verbo mutante escrito FUERA del envoltorio se salta esa guarda por completo.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  'attached() { aws iam list-attached-role-policies --role-name "$ROLE" \' \
  '	aws iam detach-role-policy --role-name "$ROLE" --policy-arn x
attached() { aws iam list-attached-role-policies --role-name "$ROLE" \'
expect 1 "does not recognise as either the single write funnel" \
  "a mutating verb written outside the wrapper is a finding"

# G-04 · EL ORDEN. Retirar AdministratorAccess antes de terminar de adjuntar deja el rol
# sin permisos con medio estate creado — el fallo caro que el orden existe para evitar.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  '	say "aws-iam-phase2: transición 1 → 2 en la cuenta $ACCOUNT"' \
  '	say "aws-iam-phase2: transición 1 → 2 en la cuenta $ACCOUNT"
	aws_write detach-role-policy --role-name "$ROLE" --policy-arn "$BOOTSTRAP_MANAGED"'
expect 1 "before finishing the attachments" \
  "detaching AdministratorAccess before the attachments is a finding (ordering)"

# G-05 · `scripts/` se exporta y `design/` no: una cuenta escrita aquí viaja al público.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  'ROLE="olivares-apply-sandbox"' \
  'ROLE="olivares-apply-sandbox"
ACCOUNT_FIJA="123456789012"'
expect 1 "contains the 12-digit literal" \
  "a hardcoded account id in an exported script is a finding"

# G-06 · `environment:` CAMBIA el `sub` del token y la trust lo fija: el rol se vuelve
# inasumible, y el fallo llega en STS con el dispatch ya lanzado, no en el diff.
stage
subst "$WF_T" '    env:
      AWS_ROLE_ARN:' '    environment: production
    env:
      AWS_ROLE_ARN:'
expect 1 "switches the OIDC \`sub\` claim" \
  "an environment on the privileged job is a finding (it breaks the pinned trust)"

# G-07 · Sin fase declarada, el paso de verificación no tiene contra qué juzgar.
stage
subst "$WF_T" '      IAM_PHASE: "1"' '      IAM_PHASE_DISABLED: "1"'
expect 1 "declares no IAM_PHASE" \
  "removing the declared IAM phase is a finding (the check would pass on anything)"

# G-08 · Una fase que no es 1 ni 2 no es una expectativa: es una errata que pasa.
stage
subst "$WF_T" '      IAM_PHASE: "1"' '      IAM_PHASE: "3"'
expect 1 "which is neither 1 nor 2" \
  "an IAM phase outside {1,2} is a finding"

# G-09 · Y el ancla, otra vez: `echo "bash …"` CONTIENE la subcadena y no comprueba nada.
# Es la misma lección que ya costó dos falsos verdes en las anclas de cosign.
stage
subst "$WF_T" '        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design' \
  '        run: echo "bash scripts/aws-iam-phase2.sh check ran"'
expect 1 "has no dedicated IAM phase-check step" \
  "echoing the check instead of running it is a finding (anchored, not substring)"


# G-10 · Y LA MISMA PROHIBICIÓN POR LA PUERTA DE ATRÁS: `environment` inyectado por CLAVE DE
# FUSIÓN de YAML (`<<: *ancla`). No es un caso inventado — es la forma que el contraste
# `sol max` fue a sondear, y la pregunta que responde no es «¿lo prohíbo?» sino «¿mi parser
# ve lo mismo que verá GitHub?». `yaml.v3` resuelve `<<`, así que el campo llega poblado y
# la comprobación muerde. Sin este caso, la clase depende de que nadie cambie de parser.
stage
subst "$WF_T" '  apply:
' '  _tpl: &envtpl
    environment: production
  apply:
    <<: *envtpl
'
expect 1 "switches the OIDC \`sub\` claim" \
  "an environment injected through a YAML merge key is a finding (the parser resolves <<)"


# G-11 · QUITAR EL DENY NO CONCEDE, y este caso es el que lo demuestra. El paso de
# verificación falla CERRADO; si el conjunto de policies no permite las lecturas que el
# guion hace sobre el rol, en fase 2 **ningún apply podría volver a arrancar**. El defecto
# no está en ninguno de los dos ficheros: está entre ellos. Aquí se voltea el `Effect` del
# Allow de auto-lectura, que es la forma mínima de romperlo sin tocar el resto.
stage
subst "$TMP/tree/design/aws-apply-role-policy.sandbox.0-guardrails.json" \
  '"Sid": "ReadItselfSoTheVerificationCanExist",
      "Effect": "Allow",' \
  '"Sid": "ReadItselfSoTheVerificationCanExist",
      "Effect": "Deny",'
expect 1 "no apply would ever start again" \
  "a policy set that does not Allow the script's own self-reads is a finding (cross-file)"


# ⛔ LAS NUEVE FORMAS QUE EL CONTRASTE MIDIÓ COMO INVISIBLES (M-03). La versión anterior de
# este gate perseguía SIETE verbos literales, y `sol max` enseñó nueve maneras normales de
# shell de escribir la misma escritura sin que ninguna casara. El arreglo fue cambiar la
# POLARIDAD —lista blanca en vez de lista negra— y eso hay que demostrarlo en los casos que
# ANTES pasaban, no en el que ya se cazaba. Una lista negra de siete no cierra un espacio
# infinito; una lista blanca de siete lecturas sí.
_g03() { # _g03 <rótulo> <línea mutante>
  stage
  subst "$TMP/tree/scripts/aws-iam-phase2.sh" 'policy_name() {' "$2
policy_name() {"
  expect 1 "does not recognise as either the single write funnel" "$1"
}
_g03 "a quoted verb is a finding (the old regex needed it bare)" \
  'aws iam "detach-role-policy" --role-name "$ROLE" --policy-arn x'
_g03 "a verb held in a variable is a finding" \
  'verb=detach-role-policy; aws iam "$verb" --role-name "$ROLE" --policy-arn x'
_g03 "the CLI held in a variable is a finding (no lowercase aws on that line)" \
  'AWSBIN=aws; "$AWSBIN" iam detach-role-policy --role-name "$ROLE" --policy-arn x'
_g03 "a verb split by shell concatenation is a finding" \
  'aws iam detach-role"-"policy --role-name "$ROLE" --policy-arn x'
_g03 "a verb that was never on the blacklist is a finding (create-role)" \
  'aws iam create-role --role-name other --assume-role-policy-document x'
_g03 "the command inside a printf is still a finding (a command substitution would run)" \
  'printf "%s" "aws iam detach-role-policy"'

# `eval` tiene su propia frase porque tiene su propia razón: no hay forma honesta de leer
# lo que construye, así que el gate deja de poder afirmar nada sobre el embudo.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" 'policy_name() {' 'eval "$cmd"
policy_name() {'
expect 1 "uses \`eval\`" \
  "eval anywhere in the transition script is a finding (its argument cannot be read)"


# ⛔ LOS SEIS FALSOS VERDES DE H-04, TAL COMO EL CONTRASTE LOS MIDIÓ. Los seis devolvían
# `apply-wiring-ok` con la comprobación anterior, que inferría EJECUCIÓN y ORDEN de un
# texto. Un `run` es un programa y este guard no ejecuta programas, así que la comprobación
# dejó de leerlos: el paso tiene que SER la forma canónica, sin `if`, sin tolerancia, sin
# `env` propio, sin `shell`, y justo detrás del último canje. Los seis viven aquí para que
# el arreglo se pruebe donde falló y no donde ya acertaba.
_h04() { # _h04 <rótulo> <frase> <bloque que sustituye al run canónico>
  stage
  subst "$WF_T" '        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design' "$3"
  expect 1 "$2" "$1"
}
_h04 "an if: on the phase check is a finding (a skipped check is not a passed one)" \
  'guards the IAM phase check with `if:' \
  '        if: false
        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design'
_h04 "continue-on-error on the phase check is a finding (its failure is the point)" \
  'sets continue-on-error on the IAM phase check' \
  '        continue-on-error: true
        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design'
_h04 "the check defined in a function nobody calls is a finding" \
  'has no dedicated IAM phase-check step' \
  '        run: |
          check_phase() { bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design; }
          echo listo'
_h04 "tofu and the check in the SAME run is a finding (same index is not after)" \
  'has no dedicated IAM phase-check step' \
  '        run: |
          tofu apply -auto-approve
          bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design'
_h04 "a step-level IAM_PHASE is a finding (it overrides the job value the guard reads)" \
  'gives the IAM phase check its own env' \
  '        env:
          IAM_PHASE: "3"
        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design'
_h04 "tofu invoked by absolute path is a finding (the detector excluded the slash)" \
  'has no dedicated IAM phase-check step' \
  '        run: /usr/local/bin/tofu apply -auto-approve'


# ═══ EL JUEZ DE LA TRUST, CONTRA LAS CUATRO QUE ACEPTABA ════════════════════
#
# ⛔ H-01 era fatal y H-02 peor. El juez recibía la trust por una TUBERÍA mientras su
# programa le llegaba por un heredoc: `python3 - … <<PY` ya usa el stdin para el programa,
# así que `json.load(sys.stdin)` leía el resto de ese heredoc —nada— y **el juez nunca vio
# una trust**. Fallaba siempre: el control era inalcanzable y en fase 2 ningún dispatch
# habría pasado jamás. Y al arreglarlo aparecía H-02: comparaba «los `:sub` y `:aud` que
# haya por ahí» sin mirar qué statement los lleva, así que el contraste midió CUATRO trusts
# abiertas dando ACCEPT — `Principal: "*"`, las condiciones buenas en un `Deny` con un
# `Allow` incondicional al lado, `Action` con `sts:AssumeRole` añadido, y el `aud` bajo
# `StringNotEquals`.
#
# El juez se extrae del guion con ancla —si el ancla se mueve, esto se cae en vez de
# aprobar— y se corre contra las fixtures. `python3 fichero.py` no necesita bit de
# ejecución, que en esta caja importa: el scratchpad está montado noexec.
judge_fixture() { # judge_fixture <rc-esperado> <fase> <json> <rótulo>
  # ⛔ El rc se CAPTURA, no se hereda: con `set -e` un juez que rechaza —que es lo que
  # estos casos buscan— mataría la batería entera en el primer acierto.
  local got=0
  python3 "$TMP/judge.py" "$2" "$3" 727732213253 o/r "repo:o/r:ref:refs/heads/main" \
    >"$TMP/jout" 2>&1 || got=$?
  if [ "$got" = "$1" ]; then ok "$4"; else
    bad "$4 — rc=$got, want $1 ($(head -c 200 "$TMP/jout"))"; fi
}

stage
python3 - "$ROOT/scripts/aws-iam-phase2.sh" "$TMP/judge.py" <<'EXTRACT'
import re, sys
src = open(sys.argv[1], encoding="utf-8").read()
m = re.search(r"(?s)trust_judge\(\) \{[^\n]*\n\tpython3 -c '\n(.*?)\n' \"\$1\"", src)
if not m:
    print("no encuentro el cuerpo de trust_judge con su ancla", file=sys.stderr)
    raise SystemExit(2)
open(sys.argv[2], "w", encoding="utf-8").write(m.group(1).replace("'\\''", "'") + "\n")
EXTRACT
CANON_TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::727732213253:oidc-provider/token.actions.githubusercontent.com"},"Action":"sts:AssumeRoleWithWebIdentity","Condition":{"StringEquals":{"token.actions.githubusercontent.com:aud":"sts.amazonaws.com","token.actions.githubusercontent.com:sub":"repo:o/r:ref:refs/heads/main"}}}]}'
judge_fixture 0 2 "$CANON_TRUST" \
  "the canonical phase-2 trust is accepted (the positive control)"
judge_fixture 1 2 "$(printf '%s' "$CANON_TRUST" | sed 's|{"Federated":"[^"]*"}|"*"|')" \
  "Principal: \"*\" is rejected (the contrast measured it ACCEPTED)"
judge_fixture 1 2 "$(printf '%s' "$CANON_TRUST" | sed 's|"Action":"sts:AssumeRoleWithWebIdentity"|"Action":["sts:AssumeRoleWithWebIdentity","sts:AssumeRole"]|')" \
  "an extra sts:AssumeRole in Action is rejected (measured ACCEPTED)"
judge_fixture 1 2 "$(printf '%s' "$CANON_TRUST" | sed 's|"StringEquals":{"token|"StringNotEquals":{"token|')" \
  "the aud under StringNotEquals is rejected (measured ACCEPTED)"
judge_fixture 1 2 "$(printf '%s' "$CANON_TRUST" | sed 's|"Effect":"Allow"|"Effect":"Deny"|;s|\]}$|,{"Effect":"Allow","Principal":{"Federated":"arn:aws:iam::727732213253:oidc-provider/token.actions.githubusercontent.com"},"Action":"sts:AssumeRoleWithWebIdentity"}]}|')" \
  "good conditions on a Deny beside an unconditional Allow is rejected (measured ACCEPTED)"
judge_fixture 1 2 "$(printf '%s' "$CANON_TRUST" | sed 's|ref:refs/heads/main|ref:refs/heads/*|')" \
  "a wildcard sub is rejected in phase 2"
judge_fixture 1 1 "$CANON_TRUST" \
  "the narrow phase-2 trust is NOT phase 1 (the direction that must also fail)"


# ⛔ UN OUTPUT QUE NADIE IMPRIME NO EXISTE. `deploy/aws/outputs.tf` publica los tres CNAME
# para que nadie los deduzca, y el apply corre en un runner con el estado en S3: sin el paso
# que los vuelca al resumen, leerlos exige credenciales y un `tofu output` a mano. Con eso,
# la partición en dos fases que este repositorio documenta es INEJECUTABLE — y el defecto no
# se ve en ningún fichero por separado, se ve al ir a despachar.
stage
subst "$WF_T" '            tofu output' '            echo "(outputs omitidos)"'
expect 1 "never publishes \`tofu output\` into GITHUB_STEP_SUMMARY" \
  "an apply that never publishes its outputs is a finding (the CNAMEs stay in S3)"

# Y la FORMA importa: `-json` NO redacta lo marcado `sensitive`, la humana sí. Un output
# sensible que alguien añada mañana se publicaría en claro en el resumen del job.
stage
subst "$WF_T" '            tofu output' '            tofu output -json'
expect 1 "never publishes \`tofu output\` into GITHUB_STEP_SUMMARY" \
  "tofu output -json is not accepted (it prints sensitive values the human form redacts)"


# ⛔ EL MUTANTE DEL DEFECTO QUE YA OCURRIÓ. El run 33212068653 murió en «install OpenTofu»
# con «sudo: a terminal is required to read the password», y lo que lo convierte en clase
# es que el MISMO paso pasó en `validate` y murió en `apply`: el pool de runners no es
# homogéneo. Un paso que necesita root es una moneda al aire, así que reintentar parece un
# arreglo y no lo es. El gate se prueba con el defecto que costó el despacho, no con uno
# parecido.
stage
subst "$WF_T" '          unzip -o "$RUNNER_TEMP/tofu.zip" -d "$RUNNER_TEMP/bin" tofu' \
  '          sudo unzip -o "$RUNNER_TEMP/tofu.zip" -d /usr/local/bin tofu'
expect 1 "invokes sudo" \
  "a privileged step that needs root is a finding (it passed on one runner and died on another)"


# ⛔ EL MUTANTE DEL SEGUNDO DEFECTO QUE YA OCURRIÓ. El run 33240917638 murió en la
# comprobación de fase con «no hay AWS CLI en esta caja»: el runner no lo traía y el
# control —que falla cerrado, como promete— paró el despacho entero. La respuesta no es
# aflojar el control: es que el job instale la herramienta que su propio control necesita.
# Los dos casos cubren las dos formas de romperlo: quitarlo, y ponerlo demasiado tarde.
stage
subst "$WF_T" '      - name: install the AWS CLI (pinned, no root)' \
  '      - name: install the AWS CLI (DISABLED)'
subst "$WF_T" '          "$RUNNER_TEMP/awscli-src/aws/install" --update \' \
  '          : skip-install \'
expect 1 "never installs the AWS CLI" \
  "running the phase check without installing the CLI is a finding (a control that cannot run)"

# Y el orden: instalarlo DESPUÉS de la comprobación existe y no sirve.
stage
subst "$WF_T" '          "$RUNNER_TEMP/awscli-src/aws/install" --update \
            -i "$RUNNER_TEMP/awscli" -b "$RUNNER_TEMP/bin"' \
  '          : moved-below \'
subst "$WF_T" '        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design' \
  '        run: bash scripts/aws-iam-phase2.sh check "$GITHUB_REPOSITORY" design

      - name: install the AWS CLI, too late
        run: |
          "$RUNNER_TEMP/awscli-src/aws/install" --update -i "$RUNNER_TEMP/awscli" -b "$RUNNER_TEMP/bin"'
expect 1 "AFTER the phase check" \
  "installing the CLI after the check it serves is a finding (it exists and does not help)"


# ⛔ LOS CUATRO SITIOS DEL DEFECTO QUE COSTÓ EL PRIMER APPLY DE VERDAD. El run 33244273912
# produjo su plan entero —99 recursos— y murió con «Invalid count argument»: el `count`
# leía un ARN que sale de otro módulo del MISMO apply. El barrido encontró cuatro de la
# misma clase, y tres esperaban en los applies 2 y 3 — el listener HTTPS habría muerto 75
# minutos después. Cada mutante restaura UNO de los cuatro, porque arreglar el que dispara
# y no la clase es descubrir el siguiente a la peor hora posible.
# ⛔ Cada caso exige LA FRASE DE SU GUARDA, no una común: los tres los caza el mismo gate
# por caminos distintos —uno por la referencia directa, otro por el cruce con la llamada del
# root— y una frase compartida no distinguiría cuál de los dos murió.
_pt() { # _pt <rótulo> <frase> <fichero> <ancla> <mutante>
  stage
  subst "$TMP/tree/deploy/aws/$3" "$4" "$5"
  expect 1 "$2" "$1"
}
_pt "a count reading a module-produced ARN is a finding (the one that fired)" \
  "the fact lives in the CALL, not in the module" \
  modules/compute/main.tf \
  '  count = var.dsn_secret_enabled ? 1 : 0' \
  '  count = var.dsn_secret_arn == "" ? 0 : 1'
_pt "the HTTPS listener count on an apply-time cert is a finding (it waited for phase 2)" \
  "count and for_each must be known at PLAN time" \
  modules/ingress/main.tf \
  '  count             = local.have_cert ? 1 : 0
  load_balancer_arn = aws_lb.alb.arn
  port              = 443' \
  '  count             = try(aws_acm_certificate_validation.alb[0].certificate_arn, "") == "" ? 0 : 1
  load_balancer_arn = aws_lb.alb.arn
  port              = 443'
_pt "a dynamic for_each on a module-produced target group is a finding (it waited for phase 3)" \
  "count and for_each must be known at PLAN time" \
  modules/compute/main.tf \
  '    for_each = var.attach_alb_target_group ? [var.alb_target_group_arn] : []' \
  '    for_each = module.ingress.http_target_group_arn == "" ? [] : [1]'


# ⛔ LO QUE EL PLANO DE CONTROL EXIGE Y LO QUE SU TASK DEFINITION LE DA — dos ficheros que
# nadie lee a la vez, y hasta el 2026-08-29 la task definition no daba NINGUNA de las
# diecinueve: el estate aplicaba limpio y el servicio no podía levantar. Los dos casos
# cubren las dos direcciones, que son fallos distintos.
stage
subst "$TMP/tree/deploy/aws/modules/compute/main.tf" '"RESEND_API_KEY",' ''
expect 1 "refuses to boot without" \
  "dropping one variable config.go requires is a finding (the service would not start)"

# Y la contraria: `DATABASE_URL` se RECHAZA, no se ignora. Ponerla «por si acaso» tumba el
# arranque, así que su PRESENCIA es el hallazgo.
stage
subst "$TMP/tree/deploy/aws/modules/compute/main.tf" '"ADMIN_API_KEY",' '"ADMIN_API_KEY", "DATABASE_URL",'
expect 1 "REFUSES to boot when it is present" \
  "supplying DATABASE_URL is a finding (config.go refuses it rather than ignoring it)"

# Y el «no he podido mirar»: sin el fuente del plano de control, esto se SALTA — que no es
# lo mismo que aprobar. El árbol exportado no lo lleva.
stage
rm -rf "$TMP/tree/cloud"
expect 0 "" "no cloud/control-plane in the tree is a SKIP, not a pass"


printf 'check-aws-estate selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
