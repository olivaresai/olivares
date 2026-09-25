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

# ── EL SUJETO DE ESTA BATERÍA VIVE EN `design/`, Y EL ÁRBOL PUBLICADO NO LO TRAE ──
#
# ⛔ MEDIDO EL 2026-09-18 EN EL ÁRBOL PÚBLICO (job `validate` de `aws-terraform.yml`, paso
#    «estate shape selftest»): `stage` copia `design/aws-apply-role-policy.*.json` al árbol
#    de pruebas y en el export curado ese directorio NO EXISTE, así que no copiaba nada. Lo
#    que salía no era un veredicto: tres casos en FAIL por «no design/aws-apply-role-policy
#    .*.json» y un `FileNotFoundError` al mutar una pieza que nunca llegó. Rojo por
#    construcción, en el repositorio que ve cualquiera, y sobre una pregunta que a ese árbol
#    no se le hace.
#
#    El gate que esta batería examina YA contesta bien ahí: sin `design/` dice
#    «apply-role-policy-skipped» y sigue (check-aws-estate.sh, bloque de la policy). Lo que
#    faltaba era que su batería dijese lo mismo en vez de morir. Es la regla de la casa, la
#    que `build:web` aplica en el punto de llamada: el árbol publicado CONTESTA «no aplica»,
#    no se cae.
#
#    Y LA AUSENCIA SOLA NO ES LA DISCRIMINANTE, que es por donde esto se convertiría en un
#    verde falso: un árbol completo que perdiera las piezas sale 2 —NO HE PODIDO MIRAR—, nunca 0. Sólo
#    el marcador que estampa la curación, que el generador se niega a trackear en el árbol fuente,
#    convierte la ausencia en «no aplica». Tres respuestas, las mismas que el gate: 0 limpio
#    · 1 hallazgo · 2 no he podido mirar. Con las piezas presentes esta guarda no se mete en
#    medio: el árbol fuente corre la batería entera y sus mutantes siguen matando (el caso «no
#    least-privilege policy at all is a finding» de más abajo es el que lo prueba).
_pol_parts=( "$ROOT"/design/aws-apply-role-policy.*.json )
if [ ! -e "${_pol_parts[0]}" ]; then
  _pol_missing="design/aws-apply-role-policy.*.json"
  [ -d "$ROOT/design" ] || _pol_missing="design/ (and with it $_pol_missing)"
  if [ -f "$ROOT/.olivares-public-export" ]; then
    _note="NOT APPLICABLE: this tree is the curated public export and does not carry"
    _note="$_note $_pol_missing — the subject of this self-test. The estate-shape gate itself"
    _note="$_note answers 'apply-role-policy-skipped' here for the same reason. Nothing was"
    _note="$_note checked by this leg, and in this tree that is the expected state."
    echo "test-aws-estate: $_note"
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      printf '### %s\n\n%s\n\n' "estate shape selftest: NOT APPLICABLE (public tree)" \
        "$_note" >>"$GITHUB_STEP_SUMMARY"
    fi
    exit 0
  fi
  echo "test-aws-estate: COULD NOT LOOK — $_pol_missing is MISSING and this tree carries no" >&2
  echo "  public-export marker. A complete source tree HAS those parts and a curated export carries the" >&2
  echo "  marker the curation stamps. Refusing to guess: reporting this battery green here would" >&2
  echo "  report it green against no subject." >&2
  exit 2
fi
# ==== ANCLA: fin de la guarda de arbol ====
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/aws-estate.XXXXXX")"
# Go writes module-cache files read-only. `rm -rf` on EXIT would then
# return 1 after a green compiled fixture and the selftest would lie.
trap 'chmod -R u+w "$TMP" 2>/dev/null || true; rm -rf "$TMP"' EXIT

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
  # ⛔ Y `cmd/cloud-cp/main.go`, porque es el SEGUNDO sujeto del par chequeo↔ruta: sin el, esa
  # comprobacion se SALTA y sus casos saldrian verdes por no haber mirado, que es justo la
  # clase de falso verde que este banco existe para cortar.
  mkdir -p "$TMP/tree/cloud/control-plane/cmd/cloud-cp"
  cp "$ROOT/cloud/control-plane/cmd/cloud-cp/main.go" \
     "$TMP/tree/cloud/control-plane/cmd/cloud-cp/" 2>/dev/null || true
  # ⛔ Y EL PAQUETE DONDE `main.go` CONSTRUYE SUS RUTAS, con su `_test.go` y el `go.mod` del
  # modulo. Desde a63c5cfb44 las rutas viven en `internal/httpapi.NewRouter`: sin el paquete el
  # gate contesta «no he podido mirar» y TODOS los casos posteriores a ese bloque testificarian
  # sobre eso (44 lo hicieron el 2026-09-06). El `_test.go` se copia a proposito, para probar
  # que una ruta registrada solo en un test NO cuenta como servida; el `go.mod` es lo que
  # permite resolver el import al directorio sin adivinar la disposicion.
  mkdir -p "$TMP/tree/cloud/control-plane/internal/httpapi"
  cp "$ROOT"/cloud/control-plane/internal/httpapi/*.go \
     "$TMP/tree/cloud/control-plane/internal/httpapi/" 2>/dev/null || true
  cp "$ROOT/cloud/control-plane/go.mod" "$TMP/tree/cloud/control-plane/" 2>/dev/null || true
  # Y el parser de rutas servidas, que es quien lee el par por el lado del Go. Sin su fuente
  # el gate contesta «no he podido mirar» y un caso lo prueba (r).
  mkdir -p "$TMP/tree/scripts/served-routes-guard"
  cp "$ROOT/scripts/served-routes-guard/go.mod" "$ROOT/scripts/served-routes-guard/main.go" \
     "$TMP/tree/scripts/served-routes-guard/"
  # ⛔ Y el SQL de los roles, que es el SUJETO de la cobertura de credenciales de la tarea de
  # un solo uso. Sin el, esa comprobacion se SALTA y sus casos saldrian verdes por no haber
  # mirado — la misma clase de falso verde que `main.go` trajo aqui arriba.
  mkdir -p "$TMP/tree/cloud/control-plane/deploy"
  cp "$ROOT/cloud/control-plane/deploy/cloud-control-roles.sql" \
     "$TMP/tree/cloud/control-plane/deploy/" 2>/dev/null || true
  # Y el Dockerfile de la imagen de roles, que es el tercer sujeto del par: sin el, los casos
  # que lo mutan saldrian verdes por no haber mirado.
  cp "$ROOT/cloud/control-plane/deploy/Dockerfile.roles" \
     "$TMP/tree/cloud/control-plane/deploy/" 2>/dev/null || true
  mkdir -p "$TMP/tree/design"
  # ⛔ TODOS los conjuntos, no sólo el de sandbox: con el glob viejo las piezas de
  # `production` no llegaban al árbol de pruebas, así que los casos que las mutan habrían
  # salido verdes por no haber sujeto — un falso verde de manual.
  cp "$ROOT"/design/aws-apply-role-policy.*.json "$TMP/tree/design/" 2>/dev/null || true
  # Y el runbook del piloto, que es el otro sujeto del par runbook↔workflow. Sin el, esa
  # comprobacion se SALTA y sus casos saldrian verdes por no haber mirado.
  cp "$ROOT/design/AWS-RUNBOOK-DESPACHO-SANDBOX.md" "$TMP/tree/design/" 2>/dev/null || true
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

# ⛔ LO COMPILADO NECESITA EL GRAFO PINNADO ANTES DE GOPROXY=off. El bloque vive
# en una funcion para que un invocador COMPILED_ONLY (la regresion de preparacion
# rehusada) no acredite los 200 mutantes estaticos como "runtime cases".
# shellcheck source=lib/aws-estate-compiled-modcache.sh
. "$ROOT/scripts/lib/aws-estate-compiled-modcache.sh" \
  || { echo "test-aws-estate: missing scripts/lib/aws-estate-compiled-modcache.sh" >&2; exit 2; }

aws_estate_run_compiled_witnesses() {
  local HEALTH_REG='mux.Handle("GET /health", health)'
  local SRV_LIT='srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}'
  local MODULE="$TMP/module"
  local cache="" prep_err="$TMP/compiled-prep.err"
  local _ctrl_log _ctrl_rc

  if [ "${OLIVARES_AWS_ESTATE_SKIP_REFUSED_PREP_CONTROL:-}" != 1 ] \
     && { [ "${OLIVARES_AWS_ESTATE_COMPILED_ONLY:-}" != 1 ] \
          || [ -n "${OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC:-}" ]; }; then
    _ctrl_log="$TMP/refused-prep-control.log"
    # `if ! cmd; then rc=$?` is always 0: the negation succeeded. Capture
    # the child's status from the else of a positive `if cmd`.
    if bash "$ROOT/scripts/test-aws-estate-compiled-modcache.sh" >"$_ctrl_log" 2>&1; then
      ok "compiled-modcache bootstrap: refused preparation is NOT MEASURED, 0 runtime cases"
    else
      _ctrl_rc=$?
      bad "compiled-modcache bootstrap regression — rc=$_ctrl_rc; $(tail -c 400 "$_ctrl_log")"
    fi
    if [ -n "${OLIVARES_AWS_ESTATE_COMPILED_MODCACHE_FORCE_RC:-}" ]; then
      return 0
    fi
  fi

  if ! cache="$(aws_estate_prepare_compiled_modcache \
      "$ROOT/cloud/control-plane" "$TMP/compiled-modcache" 2>"$prep_err")"; then
    bad "compiled witnesses NOT MEASURED — $(tr '\n' ' ' <"$prep_err" | tail -c 400)"
    return 0
  fi
  AWS_ESTATE_COMPILED_GOMODCACHE_EFFECTIVE="$cache"

  stage_module() {
    rm -rf "$MODULE"
    cp -a "$ROOT/cloud/control-plane" "$MODULE"
    rm -f "$MODULE"/internal/httpapi/gate_witness_test.go "$MODULE"/cmd/cloud-cp/gate_witness_test.go
  }
  compiled() { # compiled <rc-esperado> <needle> <rotulo> <paquete> <test>
    local want="$1" needle="$2" label="$3" pkg="$4" name="$5" got out
    local gomodcache="${AWS_ESTATE_COMPILED_GOMODCACHE_EFFECTIVE:-}"
    if [ -z "$gomodcache" ]; then
      bad "$label — NOT MEASURED: compiled() invoked without a prepared module cache"
      return
    fi
    # Sin tuberia que acabe en `grep -q` (SIGPIPE en exito bajo pipefail): la salida va a un
    # fichero y se lee de ahi.
    out="$(cd "$MODULE" && GOWORK=off GOPROXY=off GOMODCACHE="$gomodcache" \
      GOFLAGS="-mod=mod -p=2" GOMAXPROCS=2 \
      go test -count=1 -run "^${name}\$" -v "./$pkg" 2>&1)" && got=0 || got=$?
    printf '%s\n' "$out" >"$TMP/compiled.out"
    if command grep -qE 'module lookup disabled by GOPROXY=off|\[setup failed\]' "$TMP/compiled.out"; then
      bad "$label — NOT MEASURED (compiled fixture setup; module graph unavailable), not a product verdict: $(tail -c 400 "$TMP/compiled.out")"
      return
    fi
    if [ "$got" != "$want" ] || ! command grep -qF -- "$needle" "$TMP/compiled.out"; then
      bad "$label — rc=$got, want $want with '$needle'; got: $(tail -c 400 "$TMP/compiled.out")"
      return
    fi
    ok "$label"
  }
  local ROUTER_TEST='package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares-cloud-cp/internal/config"
)

func TestGateHealthWitness(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router := NewRouter(config.Config{AdminAPIKey: "gate-admin", CloudCPPortalReadKey: "gate-portal"}, h, h, h, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	t.Logf("router GET /health status=%d", rec.Code)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d want 204", rec.Code)
	}
}
'
  # (am) el router real, compilado, sirve GET /health
  stage_module
  printf '%s' "$ROUTER_TEST" >"$MODULE/internal/httpapi/gate_witness_test.go"
  compiled 0 "status=204" "compilado: el router real responde 204 a GET /health" internal/httpapi TestGateHealthWitness

  # (an) el testigo raiz F1 compilado: el goto deja /health en 404 — la regresion es real
  stage_module
  printf '%s' "$ROUTER_TEST" >"$MODULE/internal/httpapi/gate_witness_test.go"
  python3 "$ROOT/scripts/lib/subst-once.py" "$MODULE/internal/httpapi/routes.go" "$HEALTH_REG" 'goto afterHealth
	mux.Handle("GET /health", health)
afterHealth: ;'
  compiled 1 "got 404 want 204" "compilado: con el goto el router real responde 404" internal/httpapi TestGateHealthWitness

  # (ao)-(ap) el bloque REAL del servidor de main, extraido tal cual (la sentencia del literal y
  # lo que la siga hasta la linea en blanco), dentro de un test del propio paquete main: el
  # Handler que el servidor conserva responde 204; con la sustitucion raiz F2, 404.
  server_block_test() { # server_block_test <main.go> → escribe el test con el bloque extraido
    python3 - "$1" "$MODULE/cmd/cloud-cp/gate_witness_test.go" <<'PY'
import re, sys
src, dst = sys.argv[1:3]
text = open(src, encoding="utf-8").read()
m = re.search(r"(?ms)^\tsrv := &http\.Server\{Addr: cfg\.ListenAddr, Handler: mux\}\n(.*?)^\n", text)
if not m:
    sys.exit("server block anchor absent in " + src)
head = "\n".join([
    "package main", "",
    "import (", "\t\"net/http\"", "\t\"net/http/httptest\"", "\t\"testing\"", "",
    "\t\"github.com/olivaresai/olivares-cloud-cp/internal/config\"", ")", "",
    "func TestGateServerBlockWitness(t *testing.T) {",
    "\tcfg := config.Config{ListenAddr: \"127.0.0.1:0\"}",
    "\tmux := http.NewServeMux()",
    "\tmux.Handle(\"GET /health\", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))",
    ""])
tail = "\n".join([
    "\trec := httptest.NewRecorder()",
    "\tsrv.Handler.ServeHTTP(rec, httptest.NewRequest(\"GET\", \"/health\", nil))",
    "\tt.Logf(\"server block GET /health status=%d\", rec.Code)",
    "\tif rec.Code != http.StatusNoContent {",
    "\t\tt.Fatalf(\"got %d want 204\", rec.Code)",
    "\t}", "}", ""])
open(dst, "w", encoding="utf-8").write(head + m.group(0) + tail)
PY
  }
  stage_module
  server_block_test "$MODULE/cmd/cloud-cp/main.go"
  compiled 0 "status=204" "compilado: el bloque real del servidor conserva el Handler (204)" cmd/cloud-cp TestGateServerBlockWitness

  stage_module
  python3 "$ROOT/scripts/lib/subst-once.py" "$MODULE/cmd/cloud-cp/main.go" "$SRV_LIT" 'srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	srv.Handler = http.NotFoundHandler()'
  server_block_test "$MODULE/cmd/cloud-cp/main.go"
  compiled 1 "got 404 want 204" "compilado: con srv.Handler sustituido el bloque real responde 404" cmd/cloud-cp TestGateServerBlockWitness
  rm -rf "$MODULE"
}

if [ "${OLIVARES_AWS_ESTATE_COMPILED_ONLY:-}" = 1 ]; then
  aws_estate_run_compiled_witnesses
  printf 'check-aws-estate selftest: %d passed, %d failed\n' "$pass" "$fail"
  [ "$fail" -eq 0 ]
  exit $?
fi

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
# había ESTE comentario: «OIDC pin is the maintainer's: this lote does not invent a
# configure-aws-credentials digest». Un gate escrito como «el fichero menciona
# configure-aws-credentials» lo habría aprobado. Éste tiene que rechazarlo.
WF_T="$TMP/tree/.github/workflows/aws-terraform.yml"
WF_I="$TMP/tree/.github/workflows/aws-images.yml"

stage
subst "$WF_T" \
  '      - name: assume the sandbox apply role (OIDC → STS)
        uses: aws-actions/configure-aws-credentials@' \
  '      # OIDC pin is the maintainer: this lote does not invent a
      # configure-aws-credentials digest.
      # uses: aws-actions/configure-aws-credentials@'
expect 1 "has no aws-actions/configure-aws-credentials step" "the credential exchange living only in a COMMENT is a finding (the 2026-08-27 defect itself)"

stage
subst "$WF_T" \
  'aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0' \
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
        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
        with:
          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1
      - name: estate shape (no apply)'
expect 1 'assumes an AWS role, but only' "credentials in the validate job (push/PR path) are a finding"

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
# ⛔ Y LA TERCERA, o esto deja de probar lo que dice. Este caso afirma «NINGUNA firma», y con
# una imagen mas en el workflow quitar dos deja una: entonces muerde el control de CUENTA
# —que tambien es cierto— y el rotulo pasa a nombrar otra guarda. Un mutante dimensionado
# para dos imagenes deja de ser total en cuanto hay tres.
subst "$WF_I" '          bash scripts/cosign-verified.sh sign --yes --upload=true "${ROLES_REF}@${ROLES_DIGEST}"' \
             '          echo "not signing the roles image today"'
expect 1 "publishes without a cosign-verified.sh sign COMMAND" "pushing unsigned images is a finding"

stage
subst "$WF_I" '        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0' \
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
subst "$WF_T" '@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0' \
             '@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v9.9.9-inventada'
if run; then
  ok "declared blind spot: a wrong version COMMENT does not fire (the digest is what is pinned)"
else
  bad "version comment fired rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi


# ═══ LA POLICY QUE SUSTITUYE A AdministratorAccess ═══════════════════════════
POL_G="$TMP/tree/design/aws-apply-role-policy.sandbox.0-guardrails.json"
POL_C="$TMP/tree/design/aws-apply-role-policy.sandbox.3-compute-and-edge.json"

stage
rm -f "$TMP/tree"/design/aws-apply-role-policy.*.json
expect 1 "no design/aws-apply-role-policy.*.json" \
  "no least-privilege policy at all is a finding (AdministratorAccess would stay by default)"

# ⛔ Y LA AUSENCIA PARCIAL, que con un solo conjunto no existía: quitar SÓLO las de sandbox
# deja piezas en el árbol, así que el control de «¿hay alguna?» diría que sí mientras
# `aws-iam-phase2.sh` —que por defecto es sandbox— no encuentra nada que adjuntar.
stage
rm -f "$TMP/tree"/design/aws-apply-role-policy.sandbox.*.json
expect 1 "none for sandbox" \
  "policy parts that exist but none for sandbox is a finding (partial absence)"

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

# ⛔ SE BORRAN LOS DOS GEMELOS. Desde que el gate mira todos los estates, quitar sólo el
# de sandbox lo caza el control de DERIVA (una pieza sin gemelo), que es cierto y es otra
# guarda; y el «cero Deny» de sandbox lo tapaban los Deny de producción hasta que ese
# recuento pasó a ser por estate. El mutante que aísla ESTA invariante retira la pieza de
# guardas de los dos lados a la vez.
stage
rm -f "$POL_G" "${POL_G/.sandbox./.production.}"
expect 1 "no Deny statement anywhere in the" \
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
subst "$WF_T" '        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
        with:
          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1' \
  '        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
        if: env.SOMETHING == '"'"'yes'"'"'
        with:
          role-to-assume: ${{ env.AWS_ROLE_ARN }}
          aws-region: us-east-1'
expect 1 "guards the credential exchange with" \
  "an if: on the credential exchange is a finding (a skipped exchange is no exchange)"

# 2 · `continue-on-error` se traga el fallo del canje y deja correr al apply.
stage
subst "$WF_T" '        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
        with:' \
  '        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
        continue-on-error: true
        with:'
expect 1 "sets continue-on-error on the credential exchange" \
  "continue-on-error on the exchange is a finding (a failed exchange would not stop the apply)"

# 3 · DOS pasos de credenciales: el segundo decide qué rol queda puesto, así que juzgar
#     sólo el primero deja el que manda sin mirar.
stage
subst "$WF_T" '      - name: tofu apply (sandbox estate only)' \
  '      - name: a second exchange nobody looked at
        uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
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
expect 1 "signs with an explicit --upload=true only" \
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
expect 1 "signs with an explicit --upload=true only" \
  "dropping --upload=true on ONE image is a finding (E-01: 1 of 2 is not 2 of 2)"

# E-01/b · FIRMAR Y NO LEER DE VUELTA es una afirmación, no una prueba.
stage
# Las TRES: «nunca lee de vuelta» exige que no quede ninguna. Con dos sustituciones quedaba
# una y mordia el control de cuenta, que dice otra cosa.
subst "$WF_I" '          bash scripts/cosign-verified.sh verify \' '          : skip-verify \'
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
# ⛔ EL ANCLA NO PUEDE PARTIR LA TABLA `estate → SUB_TARGET`. Anclaba en
# `ROLE="olivares-apply-sandbox"`, que desde el 2026-09-02 vive DENTRO de esa tabla, así
# que el mutante la rompía y el hallazgo que salía era «no hay SUB_TARGET para sandbox»
# — cierto, y de otra guarda. Un mutante que cambia de tema aprueba el gate sin probarlo.
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  'BOOTSTRAP_MANAGED="arn:aws:iam::aws:policy/AdministratorAccess"' \
  'BOOTSTRAP_MANAGED="arn:aws:iam::aws:policy/AdministratorAccess"
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
# ⛔ EN LOS DOS GEMELOS, y por la razón de arriba: mutar uno solo lo caza el control de
# deriva —que sale ANTES y es de otra guarda—, y el defecto entre ficheros que este caso
# existe para probar no llegaría a medirse.
for _pol in "$TMP/tree/design/aws-apply-role-policy.sandbox.0-guardrails.json" \
            "$TMP/tree/design/aws-apply-role-policy.production.0-guardrails.json"; do
  subst "$_pol" \
    '"Sid": "ReadItselfSoTheVerificationCanExist",
      "Effect": "Allow",' \
    '"Sid": "ReadItselfSoTheVerificationCanExist",
      "Effect": "Deny",'
done
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


# ⛔ EL PAR CHEQUEO-DE-SALUD ↔ RUTA SERVIDA. El estate llego a `main` pidiendo HTTPS `/readyz`
# a un binario que sirve texto plano y no registra esa ruta: ningun objetivo llegaba nunca a
# sano y el servicio no arrancaba en su primer apply. Ninguna de las dos mitades es incorrecta
# EN SU FICHERO —el terraform no lee Go y el Go no sabe que hay un balanceador—, asi que el
# defecto solo existe en el PAR, y por eso se prueban las tres derivas por separado.

# (a) la ruta que nadie sirve
stage
subst "$TMP/tree/deploy/aws/modules/ingress/main.tf" 'path     = "/health"' 'path     = "/readyz"'
expect 1 "NO la sirve" \
  "un health_check contra una ruta que el plano de control no registra es un hallazgo"

# (b) HTTPS pedido a un binario que no habla TLS
stage
subst "$TMP/tree/deploy/aws/modules/ingress/main.tf" 'protocol = "HTTP"' 'protocol = "HTTPS"'
expect 1 "no habla TLS" \
  "pedir HTTPS al backend sin ListenAndServeTLS en su fuente es un hallazgo"

# (c) ⛔ Y LA DIRECCION CONTRARIA, que es la que se olvida: el dia que el plano de control
#     aprenda TLS, dejar el chequeo en claro tiene que ser hallazgo TAMBIEN. Sin este caso el
#     gate solo empuja hacia abajo y bendice el texto plano para siempre.
stage
subst "$TMP/tree/cloud/control-plane/cmd/cloud-cp/main.go" 'srv.ListenAndServe()' 'srv.ListenAndServeTLS("","")'
expect 1 "habla TLS y el health_check pide HTTP" \
  "si el binario habla TLS, un chequeo en claro es un hallazgo"

# ⛔ (d)-(l) LAS RUTAS SE LEEN DONDE EL BINARIO LAS CONSTRUYE. Desde a63c5cfb44 `main.go` no
# registra ninguna: llama a `httpapi.NewRouter`, y el lector que solo miraba `main.go` contaba
# cero y salia «no he podido mirar» sobre un arbol que servia `/health`. Cada caso de abajo
# corta una deriva DISTINTA de esa cadena y exige la frase de su guarda.
ROUTES="$TMP/tree/cloud/control-plane/internal/httpapi/routes.go"
ROUTES_TEST="$TMP/tree/cloud/control-plane/internal/httpapi/routes_test.go"
CP_MAIN="$TMP/tree/cloud/control-plane/cmd/cloud-cp/main.go"
HEALTH_REG='mux.Handle("GET /health", health)'

# (d) el paquete de rutas pierde la ruta de salud
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle("GET /healthz", health)'
expect 1 "NO la sirve" \
  "la ruta de salud borrada del paquete de rutas es un hallazgo, aunque main.go no cambie"

# (e) el paquete la sirve pero el servidor de cfg.ListenAddr sirve OTRO mux. El mutante se
#     escribe COMPILABLE: el mux de metricas se define y registra ANTES del literal del servidor
#     que lo sirve, porque el modelo lineal lee el orden y un uso antes de la definicion es «no
#     he podido mirar», no un hallazgo.
METRICS_DEF='	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
'
SRV_LIT='srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}'
stage
subst "$CP_MAIN" "$METRICS_DEF" ''
subst "$CP_MAIN" "$SRV_LIT" 'metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: metricsMux}'
expect 1 "NO la sirve" \
  "el servidor en cfg.ListenAddr sirviendo el mux de metricas es un hallazgo: /health vive en otro handler"

# (e2) la frontera de orden en main: un registro DESPUES de asociar el mux a un servidor
#      queda fuera del modelo lineal, aunque sea la ruta de salud
stage
subst "$CP_MAIN" "$METRICS_DEF" ''
subst "$CP_MAIN" "$SRV_LIT" 'metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: metricsMux}
	metricsMux.Handle("GET /health", health)'
expect 2 "despues de asociar" \
  "un registro tras asociar el mux al servidor es COULD NOT LOOK: la frontera de orden de main"

# (f) main.go delega y el paquete no esta: no se puede mirar, no se aprueba
stage
rm -f "$ROUTES"
expect 2 "no encuentro func NewRouter" \
  "sin la fuente del paquete que construye las rutas el veredicto es COULD NOT LOOK"

# (g) la ruta solo existe en un _test.go
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle("GET /healthz", health)'
cat >>"$ROUTES_TEST" <<'GO'

func routerForTests(health http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", health)
	return mux
}
GO
expect 1 "NO la sirve" \
  "una ruta registrada solo en un _test.go no es una ruta del binario"

# (h) la ruta de salud detras de la credencial de admin
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle("GET /health", admin.APIKeyMiddleware(cfg.AdminAPIKey)(health))'
expect 1 "detras de una credencial" \
  "una ruta de salud que exige credencial es un hallazgo: el balanceador sondea sin cabeceras"

# (i) no-fire: el registro repartido en varias lineas sigue siendo el mismo registro
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle(
		"GET /health",
		health,
	)'
expect 0 "" "no-fire: el registro de la ruta de salud en varias lineas sigue CLEAN"

# (j) la ruta solo admite POST y el chequeo hace GET
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle("POST /health", health)'
expect 1 "solo la registra para POST" \
  "una ruta de salud registrada solo para POST es un hallazgo: el chequeo hace GET"

# (k) main.go ya no tiene un servidor en cfg.ListenAddr que este gate sepa seguir
stage
subst "$CP_MAIN" 'Addr: cfg.ListenAddr' 'Addr: cfg.ListenAddress'
expect 2 "http.Server con Addr: cfg.ListenAddr" \
  "sin el http.Server de cfg.ListenAddr la cadena no se puede seguir: COULD NOT LOOK"

# (l) sin go.mod el import no se resuelve a un directorio: no se adivina la disposicion
stage
rm -f "$TMP/tree/cloud/control-plane/go.mod"
expect 2 "go.mod" \
  "sin el go.mod del modulo el import de httpapi no se resuelve: COULD NOT LOOK"

# ⛔ (m)-(r) LO QUE LA REVISION INDEPENDIENTE DE cf45f7f699 TUMBO, Y LO QUE EL MODELO ACOTADO
# NO SIGUE. Dos mutaciones compilables sobre el paquete real dejaban el gate en CLEAN mientras
# un httptest contra NewRouter devolvia 404: devolver un mux recien creado (los registros iban
# a otro mux) y citar el registro dentro de una cadena raw. Cada caso exige la frase de SU
# guarda; los de «no he podido mirar» prueban que una cadena que el modelo no sigue no se
# aprueba por no haberla mirado.

# (m) la funcion devuelve un http.NewServeMux() recien creado: los registros van a otro mux
stage
subst "$ROUTES" '	return mux
}' '	return http.NewServeMux()
}'
expect 1 "mux que no se devuelve" \
  "devolver un mux recien creado no acredita los registros del mux abandonado: hallazgo"

# (n) el registro de salud citado en una cadena raw no es un registro
stage
subst "$ROUTES" "$HEALTH_REG" '_ = `mux.Handle("GET /health", health)`'
expect 1 "NO la sirve" \
  "un mux.Handle dentro de una cadena raw no es codigo ejecutable: hallazgo"

# (o) el mux se pasa a otra llamada: el modelo acotado no sigue lo que esa llamada hace
stage
subst "$ROUTES" "$HEALTH_REG" 'mount(mux, health)'
expect 2 "se escapa" \
  "un mux entregado a otra funcion es COULD NOT LOOK, no un pase"

# (p) la funcion devuelve el mux envuelto: lo que hace el envoltorio no se sabe
stage
subst "$ROUTES" '	return mux
}' '	return admin.APIKeyMiddleware(cfg.AdminAPIKey)(mux)
}'
expect 2 "este gate solo sigue un ServeMux declarado en la funcion" \
  "un handler devuelto envuelto es COULD NOT LOOK, no un pase"

# (q) dos caminos devuelven cosas distintas: ambiguo
stage
subst "$ROUTES" '	mux := http.NewServeMux()' '	if cfg.AdminAPIKey == "" {
		return http.NewServeMux()
	}
	mux := http.NewServeMux()'
expect 2 "ambiguo" \
  "una funcion que devuelve un handler distinto segun el camino es COULD NOT LOOK"

# (r) sin la fuente del parser de rutas no se mira
stage
rm -f "$TMP/tree/scripts/served-routes-guard/main.go"
expect 2 "served-routes" \
  "sin la fuente del parser de rutas servidas el veredicto es COULD NOT LOOK"

# (s) no-fire: un comentario y una cadena que nombran otra ruta no cambian lo servido
stage
subst "$ROUTES" "$HEALTH_REG" '// mux.Handle("GET /readyz", health) — decoy in a comment
	_ = "mux.Handle(\"GET /readyz\", health)"
	mux.Handle("GET /health", health)'
expect 0 "" "no-fire: rutas citadas en comentarios y cadenas no cuentan ni a favor ni en contra"

# ⛔ (t)-(ab) EL CONTEXTO DE EJECUCION. La revision independiente de 9b2a26baa1 puso el registro
# de salud dentro de `if false { … }` y dentro de una funcion anonima que nadie llama: el lector
# visitaba todos los nodos de llamada del cuerpo y los acreditaba, mientras un httptest contra
# el paquete real devolvia 404. El modelo lineal solo lee sentencias del nivel superior entre la
# definicion del mux y el `return` final; cualquier otra mencion del mux es «no he podido
# mirar» con el contexto nombrado. Un registro que no se ejecuta no se acredita, y tampoco se
# «demuestra» que no se ejecute: se dice que esta fuera del modelo.

# (t) testigo A de la revision: el registro dentro de un if
stage
subst "$ROUTES" "$HEALTH_REG" 'if false { mux.Handle("GET /health", health) }'
expect 2 "contexto if (" \
  "review: un registro dentro de un if no se acredita — COULD NOT LOOK nombrando el if"

# (u) testigo B de la revision: el registro dentro de una funcion anonima que nadie llama
stage
subst "$ROUTES" "$HEALTH_REG" '_ = func() { mux.Handle("GET /health", health) }'
expect 2 "funcion anonima" \
  "review: definir una funcion anonima no la ejecuta — COULD NOT LOOK nombrando la funcion"

# (v) defer, (w) go, (x) for: los tres cambian CUANDO o SI se registra
stage
subst "$ROUTES" "$HEALTH_REG" 'defer mux.Handle("GET /health", health)'
expect 2 "contexto defer (" \
  "un registro diferido esta fuera del modelo lineal: COULD NOT LOOK"

stage
subst "$ROUTES" "$HEALTH_REG" 'go mux.Handle("GET /health", health)'
expect 2 "contexto go (" \
  "un registro en una goroutine esta fuera del modelo lineal: COULD NOT LOOK"

stage
subst "$ROUTES" "$HEALTH_REG" 'for range 1 {
		mux.Handle("GET /health", health)
	}'
expect 2 "contexto for range (" \
  "un registro dentro de un bucle esta fuera del modelo lineal: COULD NOT LOOK"

# (y) la frontera de orden del router: un return antes del final deja registros detras
stage
subst "$ROUTES" "$HEALTH_REG" 'return mux
	mux.Handle("GET /health", health)'
expect 2 "no es su ultima sentencia" \
  "un return que no es la ultima sentencia rompe el camino lineal: COULD NOT LOOK"

# (z) el mux copiado a otra variable: la copia registraria fuera del modelo
stage
subst "$ROUTES" "$HEALTH_REG" 'alias := mux
	alias.Handle("GET /health", health)'
expect 2 "se escapa" \
  "copiar el mux a otra variable lo saca del modelo lineal: COULD NOT LOOK"

# (aa) control positivo: el mismo router con el mux renombrado sigue CLEAN
stage
python3 - "$ROUTES" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
for old, new in (("mux := http.NewServeMux()", "served := http.NewServeMux()"),
                 ("mux.Handle", "served.Handle"), ("return mux", "return served")):
    if old not in s:
        sys.exit("anchor absent: " + old)
    s = s.replace(old, new)
open(p, "w", encoding="utf-8").write(s)
PY
expect 0 "" "control positivo: el router real con el mux renombrado sigue CLEAN"

# (ab) control positivo: sentencias intermedias que no tocan el mux —incluida una funcion
#      anonima invocada— no rompen el camino lineal
stage
subst "$ROUTES" 'mux.Handle("/admin/", admin.APIKeyMiddleware(cfg.AdminAPIKey)(administrative))' 'authed := admin.APIKeyMiddleware(cfg.AdminAPIKey)(administrative)
	func() {}()
	mux.Handle("/admin/", authed)'
expect 0 "" "control positivo: sentencias intermedias sin el mux mantienen el camino lineal CLEAN"

# ⛔ (ac)-(ap) TRANSFERENCIAS DE CONTROL Y EL VINCULO DEL SERVIDOR. La revision raiz de
# ee185bda96 dejo dos falsos CLEAN: un `goto` que salta el registro de salud sin nombrar el
# mux, y un `srv.Handler = http.NotFoundHandler()` despues del literal que vinculaba el mux. El
# modelo lineal lee ahora las transferencias de control en TODA sentencia antes de mirar el
# mux, y el servidor seleccionado es una variable seguida: su literal se construye en una
# asignacion simple del nivel superior y todo uso posterior es una llamada a un metodo suyo.
# Lo que no encaja es «no he podido mirar» con la linea y el contexto; un salto no se
# «demuestra» tomado ni no tomado.

# (ac) testigo raiz F1: goto y etiqueta alrededor del registro de salud, sin nombrar el mux
stage
subst "$ROUTES" "$HEALTH_REG" 'goto afterHealth
	mux.Handle("GET /health", health)
afterHealth: ;'
expect 2 "goto afterHealth" \
  "root: un goto que salta el registro de salud es COULD NOT LOOK, no un pase"

# (ad) una etiqueta sola es destino de salto: fuera del modelo aunque nadie salte
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle("GET /health", health)
afterHealth:'
expect 2 "etiqueta afterHealth" \
  "una etiqueta en el cuerpo del router es COULD NOT LOOK"

# (ae) un panic incondicional antes del return: el router nunca devuelve
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle("GET /health", health)
	panic("boot")'
expect 2 "termina en la linea" \
  "un terminador incondicional antes de la frontera es COULD NOT LOOK"

# (af) testigo raiz F2: el Handler del servidor sustituido tras el literal
stage
subst "$CP_MAIN" "$SRV_LIT" 'srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	srv.Handler = http.NotFoundHandler()'
expect 2 "la asociacion Handler del servidor seleccionado no se conserva" \
  "root: sustituir srv.Handler tras el literal es COULD NOT LOOK, no un pase"

# (ag) el servidor reasignado entero
stage
subst "$CP_MAIN" "$SRV_LIT" 'srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	srv = &http.Server{Addr: cfg.MetricsAddr, Handler: http.NotFoundHandler()}'
expect 2 "la asociacion Handler del servidor seleccionado no se conserva" \
  "reasignar el servidor seleccionado es COULD NOT LOOK"

# (ah) el servidor entregado a otra funcion: lo que haga con el no se sabe
stage
subst "$CP_MAIN" "$SRV_LIT" 'srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	tune(srv)'
expect 2 "la asociacion Handler del servidor seleccionado no se conserva" \
  "pasar el servidor a otra llamada es COULD NOT LOOK"

# (ai) el literal del servidor construido dentro de un if: construccion condicional
stage
subst "$CP_MAIN" "$SRV_LIT" 'var srv *http.Server
	if cfg.ListenAddr != "" {
		srv = &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	}'
expect 2 "construccion condicional" \
  "un literal http.Server dentro de un if es COULD NOT LOOK"

# (aj) el Handler vinculado por asignacion de campo, no por el literal
stage
subst "$CP_MAIN" "$SRV_LIT" 'srv := &http.Server{Addr: cfg.ListenAddr}
	srv.Handler = mux'
expect 2 "no es una variable simple" \
  "un literal sin Handler que se rellena despues es COULD NOT LOOK"

# (ak) control positivo: un metodo seguro del servidor tras el literal sigue CLEAN
stage
subst "$CP_MAIN" "$SRV_LIT" 'srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	srv.SetKeepAlivesEnabled(true)'
expect 0 "" "control positivo: una llamada a un metodo del servidor conserva el vinculo"

# (al) robustez del lector: un Handle sin argumentos no puede tumbarlo; es opaco y la
#      ruta de salud, ausente, es un hallazgo
stage
subst "$ROUTES" "$HEALTH_REG" 'mux.Handle()'
expect 1 "NO la sirve" \
  "un mux.Handle() sin argumentos es opaco, no un panic: la salud ausente es hallazgo"

# Both main paths must reject transfers before the selected server. The delegated
# path used to skip these statements because they never mentioned the mux.
for _main_kind in delegated inline; do
  for _main_transfer in positive panic return goto; do
    stage
    if [[ "$_main_kind" == inline ]]; then
      subst "$CP_MAIN" 'mux := httpapi.NewRouter(cfg, health, webhookHandler, adminHandler, tenantStore)' \
        'mux := http.NewServeMux()
	mux.Handle("GET /health", health)
	_ = httpapi.NewRouter
	_ = adminHandler'
    fi
    case "$_main_transfer" in
      positive)
        expect 0 "" "main $_main_kind: the unchanged setup path stays readable"
        ;;
      panic|return)
        _main_stmt='panic("gate fixture stop")'
        [[ "$_main_transfer" == return ]] && _main_stmt='return'
        subst "$CP_MAIN" "$SRV_LIT" "$_main_stmt
	$SRV_LIT"
        expect 2 "main termina en la linea" "main $_main_kind: $_main_transfer before server is unknown"
        ;;
      goto)
        subst "$CP_MAIN" "$SRV_LIT" "goto gateBeforeServer
	panic(\"skipped\")
gateBeforeServer: ;
	$SRV_LIT"
        expect 2 "goto gateBeforeServer" "main $_main_kind: transfer is unknown even without a mux use"
        ;;
    esac
  done
done

# ⛔ (am)-(ap) LO COMPILADO. Un veredicto del gate sobre un mutante solo vale si el mutante
# es una regresion real. El router real y el bloque real del servidor se compilan y se
# consultan en proceso (httptest): 204 con la fuente, 404 con los testigos raiz. No se
# ejecuta main ni su arranque. Si no compila, el caso es un fallo del banco, no un verde.
# El grafo pinnado se prepara ANTES de GOPROXY=off; sin el, NOT MEASURED, no un 404.
aws_estate_run_compiled_witnesses

# ⛔ LA CREDENCIAL DEL MASTER DE RDS, Y LAS TRES PUERTAS POR LAS QUE SE ESCAPA. El permiso
# que la tarea de un solo uso necesita es SUPERUSUARIO sobre la base de datos. La forma
# barata de arreglar un fallo suyo es colgarlo del rol `-exec` que ya existe, y entonces el
# servicio que atiende trafico se queda con esa autoridad para siempre por un paso que corre
# una vez. Cada caso mata una puerta distinta; el ultimo prueba la direccion de NO disparo,
# sin la cual esta pata bendeciria cualquier arbol que no traiga la tarea.

# (a) el ARN nombrado desde la policy del rol que asumen los servicios
stage
subst "$TMP/tree/deploy/aws/modules/compute/main.tf" \
  '          var.cp_secrets_enabled ? var.cp_runtime_secret_arn : "",' \
  '          var.cp_secrets_enabled ? var.cp_runtime_secret_arn : "",
          var.master_user_secret_arn,'
expect 1 "solo puede alcanzarlo la tarea de un solo uso" \
  "el ARN del master en la policy del rol -exec es un hallazgo"

# (b) el rol de un solo uso prestado a una task definition de servicio. Es otra puerta: (a)
#     no lo ve, porque el ARN no cambia de sitio -- lo que cambia es quien asume el rol.
stage
subst "$TMP/tree/deploy/aws/modules/compute/main.tf" \
  '  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn
  container_definitions = jsonencode([{
    name      = "control-plane"' \
  '  execution_role_arn       = aws_iam_role.roles_oneshot[0].arn
  task_role_arn            = aws_iam_role.task.arn
  container_definitions = jsonencode([{
    name      = "control-plane"'
expect 1 "existe para que NO lo asuma ningun servicio" \
  "prestar el rol de un solo uso a un servicio es un hallazgo"

# (c) comodin en la accion: anade escribir y borrar la credencial del master
stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'WILDACTION'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
# Anclado DENTRO del bloque de la tarea de un solo uso: la misma linea existe en
# `execution_secrets`, y `subst` habria mutado esa.
i = s.index('resource "aws_iam_role_policy" "roles_oneshot_master"')
head, tail = s[:i], s[i:]
old = 'Action = ["secretsmanager:GetSecretValue"]'
if tail.count(old) != 1:
    sys.exit("el ancla de la accion no esta una sola vez en roles_oneshot_master")
open(p, "w", encoding="utf-8").write(head + tail.replace(old, 'Action = ["secretsmanager:*"]', 1))
WILDACTION
expect 1 "lleva comodin" \
  "un comodin en la accion sobre la credencial del master es un hallazgo"

# (d) comodin en el recurso: extiende la lectura a TODAS las ranuras de secretos
stage
subst "$TMP/tree/deploy/aws/modules/compute/main.tf" \
  'Resource = compact([var.master_user_secret_arn, var.cp_databases_secret_arn])' \
  'Resource = ["*"]'
expect 1 "no es exactamente la entrada del master" \
  "un comodin en el recurso de esa accion es un hallazgo"

# (e) ⛔ LA DIRECCION DE NO DISPARO. Un arbol sin la tarea de un solo uso es legitimo -- era
#     el estado antes de que existiera-- y esta pata tiene que callarse ahi. Sin este caso,
#     un control que solo sabe decir que si no prueba nada.
stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'ONESHOTCUT'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
marca = "# ─── La tarea de UN SOLO USO"
if marca not in s:
    sys.exit("el ancla del bloque roles_oneshot no esta: el mutante no se aplico")
open(p, "w", encoding="utf-8").write(s[:s.index(marca)])
ONESHOTCUT
expect 0 "" "sin tarea de un solo uso la pata calla, y lo dice (direccion de no disparo)"

# ⛔ Y LA COBERTURA DE CREDENCIALES DE ESA TAREA. `cloud-control-roles.sql` exige un
# `-v <rol>_password` por rol de login, y con una de menos la tarea NO PUEDE provisionar los
# roles. Lo encontro el contraste `sol max` del 2026-09-02 sobre el commit que escribio la
# tarea: llevaba solo `PGMASTER`.
#
# ⛔ CORREGIDO EL MISMO DIA: aqui decia «AVISA Y SALE CON CODIGO 0», y eso es FALSO en este
# arbol — se curo el 2026-08-17 (`280326b45`), y hoy las diez guardas del SQL ejecutan
# `SELECT 1/0` bajo ON_ERROR_STOP. El caso no cambia: descubrir la credencial que falta sobre
# el arbol cuesta segundos, y descubrirla en el `run-task` cuesta un estate ya aplicado.

# K-01 · ⛔ EL `kms:Decrypt` SOBRE `*`, que es como nacio esta policy. `ViaService` acota el
#        SERVICIO que hace la llamada, no la clave, ni la cuenta, ni el secreto: con un
#        comodin ahi el rol alcanza cualquier clave cuya otra mitad de autorizacion lo
#        admita. Lo midio el contraste `sol max` del 2026-09-02 y la cura fue nombrar la CMK.
stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'KMSWILD'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index('resource "aws_iam_role_policy" "roles_oneshot_master"')
head, tail = s[:i], s[i:]
old = "Resource = [var.secrets_kms_key_arn]"
if tail.count(old) != 1:
    sys.exit("el ancla del recurso de kms no esta una sola vez en roles_oneshot_master")
open(p, "w", encoding="utf-8").write(head + tail.replace(old, 'Resource = ["*"]', 1))
KMSWILD
expect 1 "vuelve a estar sobre" \
  "el kms:Decrypt de la tarea de un solo uso sobre * es un hallazgo"

# K-02 · Y su ausencia: sin `kms:Decrypt` no puede leer la ranura cifrada con nuestra CMK.
stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'KMSGONE'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index('resource "aws_iam_role_policy" "roles_oneshot_master"')
head, tail = s[:i], s[i:]
old = 'Action   = ["kms:Decrypt"]'
if tail.count(old) != 1:
    sys.exit("el ancla de la accion de kms no esta una sola vez en roles_oneshot_master")
open(p, "w", encoding="utf-8").write(head + tail.replace(old, 'Action   = ["kms:DescribeKey"]', 1))
KMSGONE
expect 1 "ya no declara un statement de kms:Decrypt" \
  "quitar el kms:Decrypt de la tarea de un solo uso es un hallazgo"

# R-01 · La forma exacta en que nacio el defecto: solo la credencial del master.
stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'ONLYMASTER'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index('resource "aws_ecs_task_definition" "roles_oneshot"')
head, tail = s[:i], s[i:]
a = tail.index("secrets = concat(")
b = tail.index("logConfiguration", a)
open(p, "w", encoding="utf-8").write(
    head + tail[:a]
    + 'secrets = [{ name = "PGMASTER", valueFrom = var.master_user_secret_arn }]\n    '
    + tail[b:])
ONLYMASTER
expect 1 "no puede provisionar los roles" \
  "la tarea de un solo uso con solo la credencial del master es un hallazgo"

# R-02 · Y una sola de menos, que es como llegaria de verdad: alguien edita la lista.
stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'ONELESS'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
# La MISMA lista de diez sufijos la tiene la task definition del plano de control, asi que
# el ancla se toma dentro del bloque de la tarea de un solo uso o se muta la equivocada.
i = s.index('resource "aws_ecs_task_definition" "roles_oneshot"')
head, tail = s[:i], s[i:]
old = '"NOTIFIER_URL", "POLLER_URL", "RESOLVER_URL", "SWEEPER_URL", "TENANT_URL",'
if tail.count(old) != 1:
    sys.exit("el ancla de la lista de credenciales no esta una sola vez en roles_oneshot")
open(p, "w", encoding="utf-8").write(
    head + tail.replace(old, '"NOTIFIER_URL", "POLLER_URL", "RESOLVER_URL", "SWEEPER_URL",', 1))
ONELESS
expect 1 "recibe 9 credencial(es) de rol y el SQL exige 10" \
  "una credencial de rol de menos que las que el SQL exige es un hallazgo"

# R-03 · ⛔ LA CUENTA LA MANDA EL SQL, NO UNA LISTA DE ESTE GUION. Si el SQL pierde un rol,
#        suplir diez para nueve no es un defecto — y este caso es el que impide que alguien
#        «arregle» el control escribiendo el diez a mano, que es la forma de gate que mas
#        veces se ha roto en esta casa.
stage
subst "$TMP/tree/cloud/control-plane/deploy/cloud-control-roles.sql" \
  '\if :{?cloud_cp_billing_password}' '\if :{?cloud_cp_billing_password_RETIRED}'
expect 0 "" "si el SQL pierde un rol, suplir de mas no dispara (la cuenta sale del SQL)"


# ═══ DOS ENTORNOS: EL SEGUNDO NO ENTRA POR UNA PUERTA MAS BARATA ═════════════
#
# ⛔ Actions no tiene herencia de job, asi que `apply` y `apply-production` son copias. Lo
# que impide que deriven no es un comentario: es que el guard las compara paso a paso. Y lo
# que ata cada job a su rol es el PAR environment ↔ `sub` de la trust, que vive repartido
# entre el workflow y `scripts/aws-iam-phase2.sh` y que nadie lee a la vez.

# P-01 · Un pin subido en un job y no en el otro: un apply correria codigo sin revisar.
stage
python3 - "$WF_T" <<'PINDRIFT'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index("apply-production:")
head, tail = s[:i], s[i:]
old = "e1253824e5c10ff9df46874f81ed3ec929e19cfd"
if tail.count(old) != 1:
    sys.exit("el ancla del pin de OIDC no esta una sola vez en apply-production")
open(p, "w", encoding="utf-8").write(head + tail.replace(old, "1" * 40, 1))
PINDRIFT
expect 1 "a pin bumped on one environment and not on the other" \
  "un pin de accion distinto entre los dos jobs de apply es un hallazgo"

# P-02 · Y la comprobacion que desaparece de uno solo. Es la direccion cara: el job que la
#        pierde es el que aplica PRODUCCION.
stage
python3 - "$WF_T" <<'SHADROP'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index("apply-production:")
head, tail = s[:i], s[i:]
old = '          echo "${TOFU_SHA256}  $RUNNER_TEMP/tofu.zip" | sha256sum -c -\n'
if tail.count(old) != 1:
    sys.exit("el ancla del sha256 de tofu no esta una sola vez en apply-production")
open(p, "w", encoding="utf-8").write(head + tail.replace(old, "", 1))
SHADROP
expect 1 "runs different commands in the two apply jobs" \
  "quitar la verificacion sha256 de tofu solo en production es un hallazgo"

# P-03 · ⛔ LA DIRECCION DE NO DISPARO DE LA PARIDAD: los comentarios difieren A PROPOSITO
#        —la razon de cada paso se escribe UNA vez— y compararlos obligaria a duplicar la
#        prosa, que es el defecto contrario. Un comentario nuevo en un job no es deriva.
stage
python3 - "$WF_T" <<'COMMENTONLY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index("apply-production:")
head, tail = s[:i], s[i:]
old = "          set -euo pipefail\n"
if old not in tail:
    sys.exit("no encuentro donde meter un comentario en apply-production")
open(p, "w", encoding="utf-8").write(
    head + tail.replace(old, old + "          # una nota que solo esta en este job\n", 1))
COMMENTONLY
expect 0 "" "un comentario que solo esta en un job NO es deriva (direccion de no disparo)"

# H-01 · ⛔ EL OLVIDO, que es el modo de fallo real y el que la primera version de este
#        control NO mataba. Los defaults de la RAIZ son los nombres de PRODUCCION (decision
#        de y correcta: son los definitivos), asi que un job de apply cuyo hostname
#        resuelva a "" cae en ellos. `${{ inputs.hostname }}` a secas no esta vacio como
#        TEXTO —parece puesto— y resuelve a "" con una entrada en blanco: dos estates
#        pidiendo el mismo certificado ACM y el mismo CNAME de validacion.
stage
subst "$WF_T" \
  "      TF_VAR_hostname: \${{ github.event.inputs.hostname || 'api.cloud.olivaresai.dev' }}" \
  "      TF_VAR_hostname: \${{ github.event.inputs.hostname }}"
expect 1 "an expression with no non-empty fallback" \
  "el hostname del piloto sin respaldo cae al default de produccion y es un hallazgo"

# H-02 · La misma caida por otra via: un respaldo que existe y esta vacio.
stage
subst "$WF_T" \
  "      TF_VAR_hostname: \${{ github.event.inputs.hostname || 'api.cloud.olivaresai.dev' }}" \
  "      TF_VAR_hostname: \${{ github.event.inputs.hostname || '' }}"
expect 1 "an expression with no non-empty fallback" \
  "un respaldo vacio es la misma caida y tambien es un hallazgo"

# H-03 · Y la colision directa: los dos estates sobre el mismo nombre. ACM valida por DNS,
#        asi que se pelean por el mismo certificado y el mismo CNAME.
stage
subst "$WF_T" \
  "      TF_VAR_hostname: \${{ github.event.inputs.hostname || 'api.cloud.olivaresai.dev' }}" \
  "      TF_VAR_hostname: api.cloud.olivares.ai"
expect 1 "resolves TF_VAR_hostname to the same name as job" \
  "los dos jobs de apply sobre el mismo hostname es un hallazgo"

# H-04 · Un job de apply al que le falta el hostname del colector.
stage
subst "$WF_T" '      TF_VAR_ingest_hostname: ingest.cloud.olivares.ai
' ''
expect 1 "does not set TF_VAR_ingest_hostname" \
  "un job de apply sin ingest_hostname cae al default de la raiz y es un hallazgo"

# P-04 · El token de produccion cambiado por el del piloto: dos jobs, una sola puerta.
stage
subst "$WF_T" "github.event.inputs.confirm == 'apply-production-estate'" \
              "github.event.inputs.confirm == 'apply-sandbox-estate'"
expect 1 "the only condition that may open it is" \
  "abrir production con el token del piloto es un hallazgo"

# P-05 · El `environment` retirado de production, con su trust fijada en `environment:`.
#        Falla en STS con el dispatch ya lanzado; aqui falla en el diff.
stage
subst "$WF_T" '    environment: production
' ''
expect 1 "declares no \`environment\` but the trust of estate" \
  "quitar el environment de production, con la trust en environment:, es un hallazgo"

# P-06 · Y la mitad de enfrente del mismo par: la trust movida a un ref mientras el job
#        sigue declarando environment. El invariante es el PAR, no cada lado.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  'SUB_TARGET="repo:$REPO:environment:production"' \
  'SUB_TARGET="repo:$REPO:ref:refs/heads/main"'
expect 1 "switches the OIDC \`sub\` claim" \
  "mover la trust de production a un ref deja el environment huerfano y es un hallazgo"

# P-07 · Un estate cuyo nombre en el workflow y en la trust no coinciden. Ni la presencia
#        de environment ni la de un SUB_TARGET bastan: tienen que ser la MISMA cadena.
stage
subst "$TMP/tree/scripts/aws-iam-phase2.sh" \
  'SUB_TARGET="repo:$REPO:environment:production"' \
  'SUB_TARGET="repo:$REPO:environment:prod"'
expect 1 "the two names have to be the same string" \
  "environment y trust con nombres distintos es un hallazgo"

# P-08 · Las piezas de policy de produccion, derivadas de las de sandbox. Un permiso
#        anadido a un lado para desatascar un apply y no al otro llega como un AccessDenied
#        a mitad de camino, que es la peor hora para descubrirlo.
stage
subst "$TMP/tree/design/aws-apply-role-policy.production.3-compute-and-edge.json" \
  '"ecs:CreateCluster"' '"ecs:CreateCluster",
        "ecs:DeleteCluster"'
expect 1 "is not its sandbox twin under the estate-prefix substitution" \
  "una pieza de production que deriva de su gemela de sandbox es un hallazgo"

# P-09 · Y la pieza que falta de un lado, que la comparacion de contenido no ve.
stage
rm -f "$TMP/tree/design/aws-apply-role-policy.production.1-state-and-network.json"
expect 1 "is missing while its sandbox twin exists" \
  "una pieza de production que falta es un hallazgo"

# P-10 · ⛔ Y LA DIRECCION DE NO DISPARO DE TODO ESTE BLOQUE: un arbol con UN SOLO entorno
#        —el estado anterior a este trabajo— tiene que salir limpio. Sin este caso, las
#        nueve comprobaciones de arriba bendecirian cualquier arbol por no tener sujeto.
stage
rm -f "$TMP/tree"/design/aws-apply-role-policy.production.*.json
python3 - "$WF_T" <<'DROPPROD'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
marca = "  # ─── PRODUCCION. MISMO ESTATE, OTRO TODO LO DEMAS"
if marca not in s:
    sys.exit("el ancla del job de produccion no esta: el mutante no se aplico")
open(p, "w", encoding="utf-8").write(s[:s.index(marca)])
DROPPROD
expect 0 "" "un arbol con un solo entorno sigue limpio (direccion de no disparo)"

# ═══ LA IMAGEN DE LA TAREA DE UN SOLO USO ════════════════════════════════════
#
# ⛔ Tres hechos en tres ficheros que nadie lee a la vez: que la task definition DECLARE lo que
# corre, que la imagen que nombra EXISTA, y que su cliente de Postgres sea el major de la RDS.
# La imagen trae un `CMD` seguro, pero el sitio donde eso se revisa en un diff es el estate.

TD_T="$TMP/tree/deploy/aws/modules/compute/main.tf"
DF_T="$TMP/tree/cloud/control-plane/deploy/Dockerfile.roles"

# I-01 · La task definition sin `command`: lo que corre deja de estar en el diff del estate.
stage
subst "$TD_T" '    command    = ["/opt/olivares/roles-oneshot.sh"]
' ''
expect 1 "no declara \`command\`" \
  "la task definition de la tarea de un solo uso sin command es un hallazgo"

# I-02 · Y sin nombrar el lanzador: correr el SQL a pelo devuelve el cero mentiroso.
stage
subst "$TD_T" '"/opt/olivares/roles-oneshot.sh"' '"/opt/olivares/otra-cosa.sh"'
expect 1 "no invoca roles-oneshot.sh" \
  "una task definition que no invoca el lanzador es un hallazgo (y una MENCION en un comentario no basta)"

# I-03 · La imagen que nadie construye. El sintoma llegaria como un `run-task` sin imagen, con
#        el estate ya aplicado.
stage
rm -f "$DF_T"
expect 1 "nombraria una imagen que nadie construye" \
  "sin el Dockerfile de la imagen de roles es un hallazgo"

# I-04 · ⛔ EL PAR IMAGEN ↔ RDS. El Dockerfile promete en un comentario que su major es el de la
#        RDS; esto es lo que hace que la promesa valga algo. Un cliente por debajo del servidor
#        puede no entender su protocolo, y el desacuerdo solo existe ENTRE los dos ficheros.
# ⛔ EL MAJOR SE MIDE POR LA ASERCION, NO POR LA ETIQUETA, y este caso cambio por eso. La
#    version anterior mutaba `FROM postgres:16-alpine@` a `15-alpine@` — y eso NO cambia la
#    imagen: en `FROM etiqueta@digest` manda el DIGEST y la etiqueta es decorativa. Medido el
#    2026-09-02: `crane config postgres:16-alpine@sha256:18cfe3ef…` da `PG_MAJOR=17`. Un caso
#    que mutara la etiqueta estaria probando que el gate lee una decoracion.
stage
# ⚠ Y EL MUTANTE VA EN PYTHON, NO EN `subst`: el ancla contiene comillas SIMPLES —el
#   `awk '{print $3}'` de la asercion— asi que envolverla en comillas simples las cierra a
#   media cadena y el `$3` lo expande el shell. Bajo `set -u` eso sale como «$3: unbound
#   variable» y la bateria muere ANTES de llegar a su caso: un mutante que no se aplica no
#   prueba nada, y este ni siquiera llegaba a intentarlo.
python3 - "$DF_T" <<'MAJOR15'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = '| cut -d. -f1)" = "16"'
if s.count(old) != 1:
    sys.exit("el ancla del major no esta una sola vez")
open(p, "w", encoding="utf-8").write(s.replace(old, '| cut -d. -f1)" = "15"', 1))
MAJOR15
expect 1 "y la RDS declara el motor" \
  "un cliente de Postgres por debajo del major de la RDS es un hallazgo"

# I-04b · Sin la asercion no hay nada que compare los BYTES: el build aceptaria cualquier major.
stage
python3 - "$DF_T" <<'NOASSERT'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = 'RUN test "$(psql --version'
i = s.find(old)
if i < 0 or s.find(old, i + 1) >= 0:
    sys.exit("el ancla de la asercion del major no esta una sola vez")
j = s.index("\n", i)
open(p, "w", encoding="utf-8").write(s[:i] + "RUN true # asercion retirada" + s[j:])
NOASSERT
expect 1 "no comprueba la version de" \
  "sin la asercion de major el build acepta cualquier base y es un hallazgo"

# I-04c · ⛔ RETIRADO Y DICHO POR QUE, en vez de borrado: probaba «declarar el major en un `ARG`
#         y no comprobarlo», y el `ARG` ya no existe — el contraste `sol max` (F-06) midio que un
#         `ARG` lo sobreescribe quien construye con `--build-arg`, asi que la asercion se podia
#         desactivar desde fuera. Hoy el numero es un literal dentro del `RUN`, y su ausencia la
#         cubre I-04b y su regreso como `ARG` lo cubre I-09.

# I-17 · ⛔⛔ UN PASO QUE PUEDE FALLAR NO CUALIFICA NADA. Comprobar que la verificacion de firma
#        EXISTE no dice nada si se le permite fallar: `continue-on-error: true` deja aplicar
#        despues de que cosign diga que no, y los controles de presencia siguen viendo el paso y
#        siguen en verde. Lo midio el contraste `sol max` (B-03) con un mutante de UNA linea.
stage
python3 - "$WF_T" <<'TOLERATE'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "      - name: the images this apply consumes carry our signature\n"
if s.count(old) != 2:
    sys.exit("el ancla del paso de verificacion no esta dos veces")
open(p, "w", encoding="utf-8").write(s.replace(old, old + "        continue-on-error: true\n", 1))
TOLERATE
expect 1 "sets continue-on-error" \
  "un paso de un job privilegiado que tolera su propio fallo es un hallazgo"

# I-18 · ⛔ Y EL CANAL QUE HARIA INUTIL LA VERIFICACION. `TF_VAR_*` es la precedencia MAS BAJA
#        de OpenTofu por encima de los defaults: un `terraform.tfvars` o cualquier
#        `*.auto.tfvars` GANA, asi que el digest verificado no tiene por que ser el aplicado —
#        el contraste lo midio con un tag SIN FIRMA que quedaba CLEAN (B-02).
stage
python3 - "$WF_T" <<'NOTFVARS'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
n = re.subn(r"(?m)^[[:space:]]*for _f in [^\n]*tfvars[^\n]*$\n", "", s)
if n[1] == 0:
    n = re.subn(r"(?m)^\s*for _f in [^\n]*tfvars[^\n]*$\n", "", s)
if n[1] == 0:
    sys.exit("el ancla del bucle de tfvars no esta")
open(p, "w", encoding="utf-8").write(n[0])
NOTFVARS
expect 1 "takes precedence over TF_VAR" \
  "retirar el rechazo de tfvars es un hallazgo (el valor verificado dejaria de ser el aplicado)"

# I-12 · ⛔ LO QUE UN APPLY CONSUME TIENE QUE ESTAR FIJADO POR DIGEST. Una referencia por
#        ETIQUETA aplica sin protestar y deja de fijar lo que se despliega; una errata sale como
#        una task definition que no puede tirar de su imagen, con el estate YA aplicado (F-09).
stage
python3 - "$WF_T" <<'NOPIN'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "              *@sha256:*) ;;"
if s.count(old) != 2:
    sys.exit("el ancla de la comprobacion de digest no esta dos veces")
open(p, "w", encoding="utf-8").write(s.replace(old, "              *) ;;", 2))
NOPIN
expect 1 "are pinned by digest" \
  "un apply que no exige digest en sus imagenes es un hallazgo"

# I-13 · Y la firma del digest que ESE apply consume. `aws-images.yml` firma lo que publica,
#        pero esa promesa acababa en el registro: nada la ataba a lo que se teclea aqui.
stage
python3 - "$WF_T" <<'NOVERIFY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "            bash scripts/cosign-verified.sh verify \\\n"
if s.count(old) != 2:
    sys.exit("el ancla del verify del apply no esta dos veces")
open(p, "w", encoding="utf-8").write(s.replace(old, "            : skip-verify \\\n", 2))
NOVERIFY
expect 1 "without reading their signature back" \
  "un apply que no verifica la firma de lo que despliega es un hallazgo"

# I-10 · ⛔ LA COMPENSACION, que un CONTEO no ve. Firmar DOS veces la misma imagen y ninguna
#        vez otra da el mismo total: con `uploaded < built` el guard callaba y quedaba una
#        imagen sin firma en ECR (contraste `sol max`, F-04). Se comparan CONJUNTOS de destinos.
stage
subst "$WF_I" 'bash scripts/cosign-verified.sh sign --yes --upload=true "${ROLES_REF}@${ROLES_DIGEST}"' \
              'bash scripts/cosign-verified.sh sign --yes --upload=true "${CP_REF}@${CP_DIGEST}"'
expect 1 "DISTINCT target" \
  "firmar dos veces una imagen y ninguna otra es un hallazgo (la cuenta cuadra)"

# I-11 · Y la misma compensacion en la verificacion.
stage
subst "$WF_I" '            "${ROLES_REF}@${ROLES_DIGEST}" >/dev/null' \
              '            "${CP_REF}@${CP_DIGEST}" >/dev/null'
expect 1 "DISTINCT signature" \
  "verificar dos veces una firma y otra nunca es un hallazgo"

# I-07 · ⛔ EL DIGEST TIENE QUE PODER LLEGAR. `aws-images.yml` construye, firma y publica la
#        imagen e imprime su digest para pegarlo en el dispatch — y si el dispatch no declara la
#        entrada, ese digest NO LLEGA A NINGUN SITIO: la funcion queda inalcanzable con el gate
#        en verde. Le paso a `roles_task_image` (contraste `sol max`, F-01).
stage
python3 - "$WF_T" <<'NOINPUT'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
i = s.index("      roles_task_image:")
j = s.index("\n", s.index('default: ""', i)) + 1
open(p, "w", encoding="utf-8").write(s[:i] + s[j:])
NOINPUT
expect 1 "no la ofrece como entrada" \
  "una variable de imagen de la raiz sin entrada de dispatch es un hallazgo"

# I-08 · Y la mitad de enfrente: la entrada existe y ningun job la exporta a OpenTofu, asi que
#        se teclea y no llega.
stage
python3 - "$WF_T" <<'NOTFVAR'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "      TF_VAR_roles_task_image: ${{ github.event.inputs.roles_task_image }}\n"
if s.count(old) != 2:
    sys.exit("el ancla de TF_VAR_roles_task_image no esta dos veces")
open(p, "w", encoding="utf-8").write(s.replace(old, "", 2))
NOTFVAR
expect 1 "ningun job de apply la exporta" \
  "una entrada de dispatch que ningun job exporta a TF_VAR es un hallazgo"

# I-09 · ⛔ Y LA ASERCION DEL MAJOR NO PUEDE SER UN `ARG`: quien construye lo sobreescribe con
#        `--build-arg` y la desactiva desde fuera sin tocar el fichero (F-06).
stage
python3 - "$DF_T" <<'ARGBACK'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
# ⚠ SE AÑADE el ARG y se CONSERVA la asercion: sustituirla disparaba la guarda de «no hay
# asercion», que es cierta y es OTRA — y el caso habria cambiado de tema sin fallar.
old = 'RUN test "$(psql --version'
i = s.find(old)
if i < 0:
    sys.exit("el ancla de la asercion del major no esta")
open(p, "w", encoding="utf-8").write(s[:i] + "ARG EXPECTED_PG_MAJOR=16\n" + s[i:])
ARGBACK
expect 1 "vuelve a declarar" \
  "un major sobreescribible por --build-arg es un hallazgo"

# I-06 · ⛔ Y QUE ALGUIEN LA CONSTRUYA. El Dockerfile puede existir y la task definition
#        nombrarla mientras el workflow no la toca: entonces `roles_task_image` nombra algo que
#        ningun pipeline publica. `aws-apply-guard` cuenta las imagenes y exige una firma por
#        cada una, pero no sabe CUALES son — al retirar el paso bajan a dos y alli todo cuadra.
stage
subst "$WF_I" '            --file cloud/control-plane/deploy/Dockerfile.roles \'               '            --file cloud/control-plane/Dockerfile \'
expect 1 "no construye cloud/control-plane/deploy/Dockerfile.roles" \
  "un workflow que no construye la imagen de roles es un hallazgo"

# I-14 · ⛔ SOLO CLIENTE. La imagen lleva dentro la credencial del MASTER, asi que no puede
#        traer el SERVIDOR: `postgres`, `initdb`, `pg_ctl` son superficie de administracion de
#        base de datos junto a la credencial que la administra (contraste `sol max`, F-08).
stage
subst "$DF_T" 'FROM alpine:3.22@' 'FROM postgres:16-alpine@'
expect 1 "vuelve a partir de una imagen" \
  "volver a una base con el servidor de Postgres es un hallazgo"

# I-15 · Y el paquete pinchado por version: sin `=`, el cliente puede cambiar entre dos builds
#        del MISMO commit, que es deriva sin diff.
stage
subst "$DF_T" 'postgresql16-client=16.15-r0' 'postgresql16-client'
expect 1 "sin fijar version con" \
  "instalar el cliente sin fijar su version es un hallazgo"

# I-16 · Y que el build COMPRUEBE que no hay binarios de servidor: «el paquete -client no
#        deberia traerlos» no es una comprobacion, y una dependencia puede arrastrarlos.
stage
python3 - "$DF_T" <<'NOSERVERCHECK'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
n = re.subn(r"(?m)^RUN for b in postgres initdb pg_ctl.*$", "RUN true", s)
if n[1] != 1:
    sys.exit("el ancla de la comprobacion de binarios de servidor no esta una sola vez")
open(p, "w", encoding="utf-8").write(n[0])
NOSERVERCHECK
expect 1 "no comprueba en el build que la imagen NO trae" \
  "no comprobar la ausencia de binarios de servidor es un hallazgo"

# I-05 · La base sin pinchar por digest: una etiqueta es un puntero movil, y quien la controle
#        decide que codigo corre con la credencial del master.
stage
python3 - "$DF_T" <<'UNPIN'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
# El nombre de la base cambio con F-08 (`postgres:16-alpine` -> `alpine`), asi que el ancla se
# escribe sin nombrarla: lo que importa es que el `FROM` pierda su digest, no cual sea la imagen.
n = re.subn(r"(?m)^(FROM \S+?)@sha256:[0-9a-f]{64}$", r"\1", s)
if n[1] != 1:
    sys.exit("el ancla del digest de la base no esta una sola vez")
open(p, "w", encoding="utf-8").write(n[0])
UNPIN
expect 1 "no fija su base por digest" \
  "la base de la imagen de roles sin digest es un hallazgo"

# ═══ EL RUNBOOK DEL PILOTO Y LA ZONA A LA QUE MANDA ══════════════════════════
#
# ⛔ Tres veces el mismo rancio en dos dias sobre el fichero que gobierna el primer apply, y la
# tercera a cuatro secciones de la correccion de la segunda: corregir una descripcion NO
# arrastra a sus hermanas. Coste medido: los CNAME del piloto en la zona VIVA —la de los cinco
# `MX` del correo de la casa— con los nombres de produccion.

RB_T="$TMP/tree/design/AWS-RUNBOOK-DESPACHO-SANDBOX.md"

# B-01 · La zona de altas, sola. Es la mitad que decide DONDE se escribe.
# ⛔ RE-APUNTADO POR CLOUD-08, no borrado: la zona ya no va literal en la invocacion —va en
#    `"$ZONE"`, porque la linea base se toma en el mismo acto—, asi que mutar la invocacion ya no
#    es la deriva posible. La equivalente es mover la ASIGNACION, y eso es exactamente lo que
#    hace B-08 mas abajo. Este caso pasa a probar la otra mitad: que una zona en variable SIN
#    asignacion visible no cuela.
stage
python3 - "$RB_T" <<'NOASSIGN'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
n = re.subn(r"(?m)^ZONE=[A-Za-z0-9.-]+$\n", "", s)
if n[1] != 1:
    sys.exit("el ancla de la asignacion de ZONE no esta una sola vez")
open(p, "w", encoding="utf-8").write(n[0])
NOASSIGN
expect 1 "ningun bloque de codigo la asigna" \
  "una zona en variable sin asignacion visible es un hallazgo"

# B-02 · Y los nombres, solos. Es la otra mitad y falla distinto: escribe en la zona buena un
#        nombre que el piloto no usa.
stage
subst "$RB_T" 'api.cloud.olivaresai.dev    <alb_dns_name> "$BASE"' 'api.cloud.olivares.ai    <alb_dns_name> "$BASE"'
expect 1 "es un COMANDO del runbook del PILOTO y nombra" \
  "un comando del runbook del piloto con el nombre de produccion es un hallazgo"

# B-03 · ⛔ LA PEOR DE LAS CUATRO, porque NO FALLA: una consulta de ACM filtrada por el dominio
#        de produccion devuelve VACIO contra el piloto, y el vacio se lee como «el certificado
#        no esta». Un comando roto avisa; este no.
stage
subst "$RB_T" 'DomainName==`api.cloud.olivaresai.dev`' 'DomainName==`api.cloud.olivares.ai`'
expect 1 "es un COMANDO del runbook del PILOTO y nombra" \
  "la consulta de ACM por el dominio de produccion es un hallazgo (devuelve vacio, no falla)"

# B-06 · ⛔⛔ UNA LINEA BASE ES UNA FOTO, Y UNA FOTO ENVEJECE. `cloud-dns-add.sh` para si la zona
#        ya difiere de la linea base que se le pasa, asi que citar una con FECHA FIJA en el
#        runbook bloquea el alta sobre una zona SANA. Medido el 2026-09-02 (CLOUD-08): la que
#        este runbook citaba tenia CERO registros y la zona ya tenia TRES `AAAA` legitimos
#        posteriores — la primera alta habria muerto con «la zona ya difiere de su linea base».
stage
subst "$RB_T" '  api.cloud.olivaresai.dev    <alb_dns_name> "$BASE"' \
              '  api.cloud.olivaresai.dev    <alb_dns_name> /workspace/.secrets/dns-baseline-olivaresai.dev-20260828T0950Z.json'
expect 1 "linea base con FECHA FIJA" \
  "citar una linea base con fecha fija es un hallazgo (una foto vieja bloquea una zona sana)"

# B-07 · Y que el runbook TOME la linea base: sin ese paso, quien lo siga usara una de otro dia
#        o ninguna, y la comparacion del guion deja de significar algo.
stage
python3 - "$RB_T" <<'NOBASE'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
n = re.subn(r"(?m)^BASE=.*$\n", "", s)
if n[1] != 1:
    sys.exit("el ancla de la toma de linea base no esta una sola vez")
open(p, "w", encoding="utf-8").write(n[0])
NOBASE
expect 1 "ningun bloque de codigo TOMA la linea base" \
  "un runbook con altas y sin tomar la linea base es un hallazgo"

# B-08 · La zona viaja en una variable desde CLOUD-08, y la variable NO se cree a ciegas: se
#        busca su asignacion y se compara ESE valor.
stage
subst "$RB_T" 'ZONE=olivaresai.dev' 'ZONE=olivares.ai'
expect 1 "asigna ZONE=" \
  "una ZONE asignada a la zona viva es un hallazgo"

# B-04 · ⛔ DIRECCION DE NO DISPARO, y sin ella la pata seria inservible: la PROSA del runbook
#        nombra produccion A PROPOSITO —la tabla de reparto, el parrafo de los defaults y el
#        bloque historico que se conserva—. Prohibirselo seria obligarle a callar lo que hay que
#        decir. Solo se miran los bloques de codigo.
stage
python3 - "$RB_T" <<'PROSEONLY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
marca = "\n### 1-b"
if marca not in s:
    sys.exit("no encuentro donde anadir prosa en el runbook")
i = s.index(marca)
open(p, "w", encoding="utf-8").write(
    s[:i] + "\n> Nota de prueba: produccion vive en api.cloud.olivares.ai e "
            "ingest.cloud.olivares.ai.\n" + s[i:])
PROSEONLY
expect 0 "" "la prosa que nombra produccion NO dispara (direccion de no disparo)"

# B-05 · Y sin el runbook, la pata se salta y lo dice. No es lo mismo que aprobar.
stage
rm -f "$RB_T"
expect 0 "" "sin el runbook del piloto la pata se SALTA, no aprueba"


# ═══ LA GUARDA DE ÁRBOL DE ESTA PROPIA BATERÍA ═══════════════════════════════
#
# ⛔ EL SUJETO ES EL BLOQUE DE GUARDA DE ARRIBA, RECORTADO POR ANCLA Y NO REESCRITO. Una
#    copia a mano del guard probaría la copia: estos casos cortan ESTE fichero justo debajo
#    de su ancla y le pegan una línea que dice que la guarda dejó pasar. Si el ancla se
#    mueve o se duplica, el recorte sale 1 y la batería cae — un caso que deja de medir
#    tiene que tumbarla, no aprobarla. Y el recorte para antes de la batería de verdad, así
#    que ninguno de estos casos la vuelve a lanzar dentro de sí misma.
guard_copy() { # guard_copy <root-de-usar-y-tirar>: esta batería cortada tras su guarda
  mkdir -p "$1/scripts"
  python3 - "$ROOT/scripts/test-aws-estate.sh" "$1/scripts/test-aws-estate.sh" <<'CUT'
import re, sys
src, dst = sys.argv[1], sys.argv[2]
s = open(src, encoding="utf-8").read()
pat = re.compile(r"^# ={4} ANCLA: fin de la guarda de arbol ={4}$", re.M)
hits = pat.findall(s)
if len(hits) != 1:
    sys.exit("el ancla de la guarda de arbol aparece %d veces, no 1: el caso no mide nada"
             % len(hits))
head = s[:pat.search(s).start()]
open(dst, "w", encoding="utf-8").write(
    head + 'echo "test-aws-estate: GUARD PASSED — the battery would run from here"\nexit 0\n')
CUT
}

guard_case() { # guard_case <rótulo> <rc-esperado> <trozo-de-frase> <marcador:si|no> <piezas:si|no>
  local label="$1" want="$2" needle="$3" marker="$4" parts="$5"
  local d="$TMP/guard-$want-$marker-$parts" rc=0 out
  rm -rf "$d"
  guard_copy "$d"
  [ "$marker" = no ] || printf 'olivares-public-export-marker v1\n' >"$d/.olivares-public-export"
  if [ "$parts" = si ]; then
    mkdir -p "$d/design"
    cp "$ROOT/design/aws-apply-role-policy.sandbox.0-guardrails.json" "$d/design/"
  fi
  out="$(bash "$d/scripts/test-aws-estate.sh" 2>&1)" || rc=$?
  if [ "$rc" != "$want" ]; then
    bad "$label — rc=$rc, want $want ($(printf '%s' "$out" | head -c 400))"
    return
  fi
  case "$out" in
    *"$needle"*) ok "$label" ;;
    *) bad "$label — rc=$want but the message does not name its reason; got: $(printf '%s' "$out" | head -c 400)" ;;
  esac
}

# G-01 · El árbol publicado CONTESTA. Éste es el defecto que se midió: sin la guarda este
#        mismo paso moría en el repositorio público con tres FAIL y un FileNotFoundError.
guard_case "the curated public export answers NOT APPLICABLE and exits 0" \
  0 "NOT APPLICABLE" si no

# G-02 · Y LA OTRA DIRECCIÓN, que es la que impide que esto sea un verde falso: sin piezas y
#        sin marcador nadie sabe qué árbol es esto, así que no se aprueba — se rehúsa.
guard_case "no policy parts and no export marker is COULD NOT LOOK, not a skip" \
  2 "COULD NOT LOOK" no no

# G-03 · Con las piezas presentes la guarda NO se mete en medio: el árbol fuente corre la batería
#        entera. Sin este caso, una guarda demasiado ancha dejaría el banco del árbol fuente en cero
#        casos y «0 passed, 0 failed» saldría verde.
guard_case "with the policy parts present the guard falls through (the source tree runs the battery)" \
  0 "GUARD PASSED" no si

# G-04 · El marcador NO es una contraseña: un árbol que SÍ trae el sujeto se examina, lleve
#        el fichero que lleve. La ausencia del sujeto es la condición; el marcador sólo
#        decide si esa ausencia es sancionada.
guard_case "the marker does not silence a tree that does carry the parts" \
  0 "GUARD PASSED" si si

printf 'check-aws-estate selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
