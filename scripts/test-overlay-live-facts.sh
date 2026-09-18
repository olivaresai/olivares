#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-overlay-live-facts.sh. Both firing directions, hermetic.
#
# ⛔ POR QUE LOS FIXTURES SON SINTETICOS Y NO LOS BYTES REALES DEL OVERLAY: la fuente
# Enterprise es `LicenseRef-Olivares-Commercial` y este repositorio es AGPL. Copiar
# `enterprise/activation/catalog.go` a `test/fixtures/` para tener «los bytes de verdad»
# seria publicar codigo comercial en el arbol abierto — el defecto que `check-license-
# boundary.sh` existe para impedir. Los fixtures reproducen las FORMAS que el lector juzga.
#
# ⇒ Y por eso la corrida contra los BLOBS REALES no desaparece: vive en la evidencia del
#   encargo (an internal design note (not shipped)), con los mismos mutantes
#   aplicados a los blobs reales de `origin/main`, sus SHA y sus codigos de salida. Una
#   bateria hermetica prueba el LECTOR; aquella prueba que el lector lee bien el sujeto.
#
# Republish uses ordinary commits, a local bare origin and the official
# `scripts/fetch-overlay-seal.sh`. Seals are not handwritten. The five token-census
# false CLEANs, unknown readable structure per judged body, and comment/gofmt no-fire
# are permanent cases here.

set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT (trinquete `lint:git-env`). Este guion empareja `mktemp -d` con
# git, y git EXPORTA `GIT_DIR` a los hooks desde un worktree enlazado. `GIT_DIR` manda sobre
# `-C`, asi que sin sanear, un `git -C "$tmp" ...` de banco de pruebas actua sobre el
# repositorio VIVO.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-overlay-live-facts.sh"
SEALLIB="$ROOT/scripts/lib/overlay-seal.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/livefacts.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
# Fixture observations belong to the staged tree, never the parent publication.
unset OLIVARES_OVERLAY_OBS_DIR
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

GIT="git -c user.email=t@t -c user.name=t -c commit.gpgsign=false"
ACT="act-$$-fixture"

# ── el overlay de juguete ─────────────────────────────────────────────────────────────────
write_sources() {
	local ent="$1"
	mkdir -p "$ent/enterprise/rtbf" "$ent/enterprise/activation" "$ent/cmd-overlay/olivares"

	# These are declarations in the generated fixture, not licences of this script.
	# Match test-export-license.sh's runtime assembly; keep fixture bytes unchanged.
	printf '// %s: %s\n' 'SPDX-FileCopyrightText' '2026 Olivares.AI' \
		'SPDX-License-Identifier' 'LicenseRef-Olivares-Commercial' >"$ent/enterprise/rtbf/legalhold.go"
	cat >>"$ent/enterprise/rtbf/legalhold.go" <<'EOF'

//go:build enterprise

package rtbf

// EvaluateOverride is NO-GATE. An addonGate/EntitlementFunc consult HERE would let a lapsed
// grant freeze the control itself, so the words below are a DECOY on purpose: this battery
// asserts that naming them in prose changes nothing.
func (h *LegalHoldOverride) EvaluateOverride(_ context.Context, holdID string, reason string, approvers int) (*OverrideDecision, error) {
	if holdID == "" {
		return nil, fmt.Errorf("rtbf: holdID is required for override evaluation")
	}
	decision := &OverrideDecision{RequiredApprovers: h.MinApprovers, ProvidedApprovers: approvers}
	if !h.Enabled {
		decision.Allowed = false
		decision.Reason = "legal hold overrides are disabled by policy"
		return decision, nil
	}
	if h.RequireJustification && reason == "" {
		decision.Allowed = false
		decision.Reason = "a justification is required for legal hold overrides"
		return decision, nil
	}
	if approvers < h.MinApprovers {
		decision.Allowed = false
		decision.Reason = "insufficient approvers"
		return decision, nil
	}
	decision.Allowed = true
	decision.Reason = "override approved"
	return decision, nil
}
EOF

	cat >"$ent/cmd-overlay/olivares/durablebus_enterprise.go" <<'EOF'
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build enterprise && addon_ids

package main

func durableLicensed(licenseFile, dataDir string, getenv func(string) string) bool {
	src, err := resolveLicense(licenseFile, dataDir, getenv)
	if err != nil || strings.TrimSpace(src.Blob) == "" {
		return false
	}
	pub := license.DefaultPublicKey()
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	holder := newLicenseHolder(pub, src, nil, slog.Default())
	c, ok := holder.claims()
	if !ok || c.Status(time.Now()) == license.StatusExpired {
		return false
	}
	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)
}
EOF

	cat >"$ent/cmd-overlay/olivares/addonpacks_enterprise.go" <<'EOF'
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build enterprise

package main

import (
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/enterprise/activation"
	"github.com/olivaresai/olivares/enterprise/addongate"
)

var setCodePacks = map[string]activation.Pack{
	"biz":  activation.PackBusiness,
	"reg":  activation.PackRegulated,
	"airs": activation.PackAIRuntimeSecurity,
	"cp":   activation.PackCompliancePacks,
	"ids":  activation.PackIdentityScale,
	"ent":  activation.PackEnterprise,
}

func packProductID(p activation.Pack) string {
	switch p {
	case activation.PackBusiness:
		return "self_hosted.business"
	case activation.PackRegulated:
		return "self_hosted.business.addons.regulated"
	case activation.PackAIRuntimeSecurity:
		return "self_hosted.business.addons.ai-runtime-security"
	case activation.PackCompliancePacks:
		return "self_hosted.business.addons.compliance-packs"
	case activation.PackIdentityScale:
		return "self_hosted.business.addons.identity-scale"
	case activation.PackEnterprise:
		return "self_hosted.enterprise"
	}
	return ""
}

func attestedPacks(c license.Claims) (map[activation.Pack]bool, bool) {
	out := map[activation.Pack]bool{}
	vocabulary := false
	for _, f := range c.Features {
		code, found := strings.CutPrefix(strings.ToLower(f), "set:")
		if !found {
			continue
		}
		vocabulary = true
		if p, known := setCodePacks[code]; known {
			out[p] = true
		}
	}
	return out, vocabulary
}

func purchaseViewFromClaims(claims licenseClaimsFunc) activation.PurchaseView {
	return func(p activation.Pack) bool {
		if claims == nil {
			return false
		}
		c, ok := claims()
		if !ok {
			return false
		}
		packs, vocabulary := attestedPacks(c)
		if vocabulary {
			return packs[p]
		}
		switch p {
		case activation.PackBusiness, activation.PackRegulated, activation.PackAIRuntimeSecurity,
			activation.PackCompliancePacks, activation.PackIdentityScale:
			return true
		}
		return false
	}
}

func purchaseViewFromGrants(grants addongate.EntitlementFunc) activation.PurchaseView {
	return func(p activation.Pack) bool {
		if grants == nil {
			return false
		}
		list, ok := grants()
		if !ok || list == nil {
			return false
		}
		if len(list) == 0 {
			return false
		}
		want := packProductID(p)
		if want == "" {
			return false
		}
		for _, g := range list {
			if g.ProductID == want {
				return true
			}
		}
		return false
	}
}

func combinedPurchaseView(claims licenseClaimsFunc, grants addongate.EntitlementFunc) activation.PurchaseView {
	fromGrants := purchaseViewFromGrants(grants)
	fromClaims := purchaseViewFromClaims(claims)
	return func(p activation.Pack) bool {
		if grants != nil {
			if list, ok := grants(); ok && list != nil {
				return fromGrants(p)
			}
		}
		return fromClaims(p)
	}
}
EOF

	printf '// %s: %s\n' 'SPDX-FileCopyrightText' '2026 Olivares.AI' \
		'SPDX-License-Identifier' 'LicenseRef-Olivares-Commercial' >"$ent/enterprise/activation/catalog.go"
	cat >>"$ent/enterprise/activation/catalog.go" <<'EOF'

//go:build enterprise

package activation

type Pack string

const (
	PackBusiness          Pack = "business"
	PackRegulated         Pack = "regulated"
	PackAIRuntimeSecurity Pack = "ai-runtime-security"
	PackCompliancePacks   Pack = "compliance-packs"
	PackIdentityScale     Pack = "identity-scale"
	PackEnterprise        Pack = "enterprise"
)

var catalog = []AddonSpec{
	{
		Key: "reporting", Env: "OLIVARES_REPORTING_CONFIG", Pack: PackBusiness,
		Title: "Executive & compliance reporting", Kind: KindFile, Disp: DispActive,
		Default: json.RawMessage(`{}`),
	},
	{
		Key: "doraregister", Pack: PackCompliancePacks,
		Title: "DORA register", Kind: KindWired, Disp: DispActive,
	},
	{
		Key: "iso42001", Pack: PackCompliancePacks,
		Title: "ISO/IEC 42001 AIMS packs", Kind: KindWired, Disp: DispActive,
		Summary: "drafts for the customer's auditor, never certification.",
	},
	{
		Key: "federation-multi-idp", Pack: PackIdentityScale,
		Title: "Multi-IdP federation", Kind: KindConsole, Disp: DispConsole,
	},
}
EOF

	# The private support file of observed-crl-keyring-v1. Its bytes are bound by exact
	# identity and never parsed, so synthetic content is the whole fixture.
	mkdir -p "$ent/enterprise/addongate"
	printf '// %s: %s\n' 'SPDX-FileCopyrightText' '2026 Olivares.AI' \
		'SPDX-License-Identifier' 'LicenseRef-Olivares-Commercial' >"$ent/enterprise/addongate/addongate.go"
	cat >>"$ent/enterprise/addongate/addongate.go" <<'EOF'

//go:build enterprise

package addongate

// Fixture support bytes: the evaluator hashes them and never reads them as Go.
const CRLObservationGrace = 14 * 24 * time.Hour
EOF

	cat >"$ent/cmd-overlay/olivares/wire_enterprise_addon_cp.go" <<'EOF'
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build enterprise && addon_cp

package main

func init() {
	markSubsystemsLinked("cp", "compliancedepth", "doraregister", "iso42001", "oscalingest")
}

func newAIMSPackager() compliance.AIMSPackager {
	return iso42001.NewPackager(addonGate("iso42001"))
}
EOF

	cat >"$ent/cmd-overlay/olivares/wire_enterprise_noaddon_cp.go" <<'EOF'
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build enterprise && !addon_cp

package main

// THE COMPLIANCE-PACKS ADD-ON IS NOT IN THIS BUILD.
func newAIMSPackager() compliance.AIMSPackager {
	return nil
}
EOF
}

# The observed-crl-keyring-v1 durable selection: the exact forwarding wrapper and the helper
# with the verifying holder, shared clock, observed-revocation gate and purchase composition.
write_current_durable() { # write_current_durable <file>
	cat >"$1" <<'EOF'
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build enterprise && addon_ids

package main

import (
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/enterprise/activation"
	"github.com/olivaresai/olivares/enterprise/addongate"
)

func durableLicensed(licenseFile, dataDir string, getenv func(string) string) bool {
	return durableLicensedAt(licenseFile, dataDir, getenv, time.Now)
}

func durableLicensedAt(licenseFile, dataDir string, getenv func(string) string, now func() time.Time) bool {
	src, err := resolveLicense(licenseFile, dataDir, getenv)
	if err != nil || strings.TrimSpace(src.Blob) == "" {
		return false
	}
	holder := newDataDirLicenseHolder(dataDir, src, now, slog.Default())
	c, ok := holder.claims()
	if !ok || c.Status(now()) == license.StatusExpired {
		return false
	}
	gate := addongate.New("durablebus", holder.claims).
		WithRevocation(addongate.CRLView(crlViewFromDataDir(dataDir))).
		WithClock(now)
	if gate.State() == addongate.StateUnentitled {
		return false
	}
	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)
}
EOF
}

# The three Community support files, synthetic for the same reason as the private one.
write_community_support() { # write_community_support <community-tree>
	mkdir -p "$1/cmd/olivares"
	local f
	for f in license_holder license_trust license_crl; do
		printf '%s\n' 'package main' '' "// Fixture support bytes for $f.go: hashed, never parsed or executed." \
			"func ${f}Fixture() {}" >"$1/cmd/olivares/$f.go"
	done
}

# El hub de juguete: un repositorio con el mapa publico commiteado. Su SHA es el que el
# gitlink `public` del overlay DECLARA, que es como el lector real llega al mapa.
HUB_PIN=""
stage() {
	rm -rf "$TMP/tree" "$TMP/ent" "$TMP/remote.git"
	mkdir -p "$TMP/tree/scripts/lib" "$TMP/tree/scripts/overlay-ast" "$TMP/tree/design" "$TMP/tree/commercial"
	cp "$CHECK" "$TMP/tree/scripts/check-overlay-live-facts.sh"
	cp "$SEALLIB" "$TMP/tree/scripts/lib/overlay-seal.sh"
	cp "$ROOT/scripts/lib/git-env.sh" "$TMP/tree/scripts/lib/git-env.sh"
	# ⛔ Y DESDE LT1, LA LIB DE MEDICION Y SU IMPLEMENTACION: el lector hace `source` de
	# scripts/lib/overlay-measurement.sh e invoca scripts/overlay-measure.py. Sin las dos muere
	# en el `source` y esta bateria mediria un fallo de MONTAJE creyendo medir su sujeto.
	cp "$ROOT/scripts/lib/overlay-measurement.sh" "$TMP/tree/scripts/lib/overlay-measurement.sh"
	cp "$ROOT/scripts/overlay-measure.py" "$TMP/tree/scripts/overlay-measure.py"
	cp "$ROOT/scripts/lib/overlay-facts.py" "$TMP/tree/scripts/lib/overlay-facts.py"
	cp "$ROOT/scripts/overlay-ast/"*.go "$ROOT/scripts/overlay-ast/go.mod" "$TMP/tree/scripts/overlay-ast/"
	chmod +x "$TMP/tree/scripts/check-overlay-live-facts.sh"
	cat >"$TMP/tree/commercial/module-slug-package.json" <<'EOF'
{
  "source": "fixture",
  "entries": [
    { "slug": "doraregister", "package": "enterprise/doraregister" },
    { "slug": "iso42001", "package": "enterprise/iso42001" }
  ]
}
EOF
	cat >"$TMP/tree/design/OVERLAY-LIVE-FACTS-fixture.md" <<'EOF'
# fixture contract doc
It does not close the panel, a release or a certification.
It links the historical record kept by the 2026-08-20 actas.
EOF
	$GIT init -q -b main "$TMP/tree"
	write_community_support "$TMP/tree"
	$GIT -C "$TMP/tree" add commercial/module-slug-package.json cmd/olivares
	$GIT -C "$TMP/tree" commit -q -m "public map and support files"
	HUB_PIN="$($GIT -C "$TMP/tree" rev-parse HEAD)"

	$GIT init -q -b main "$TMP/ent"
	write_sources "$TMP/ent"
	$GIT -C "$TMP/ent" add -A -- enterprise cmd-overlay
	$GIT -C "$TMP/ent" update-index --add --cacheinfo "160000,$HUB_PIN,public"
	$GIT -C "$TMP/ent" commit -q -m "overlay base"
	$GIT init -q --bare "$TMP/remote.git"
	$GIT -C "$TMP/ent" remote add origin "$TMP/remote.git"
	pin_reviewed_digests
	republish
}

# Closed-form digests of the CLEAN fixture, for BOTH reviewed constructions: the staged
# legacy overlay, and the same six sources with the current durable selection. Mutants keep
# this pin: a readable but unreviewed body is 1 even when a named predicate still passes.
# The four support bindings are taken from the clean commits the same way.
pin_reviewed_digests() {
	local bin ast_rc=0 src
	bin="$TMP/overlay-ast-pin"
	( cd "$ROOT/scripts/overlay-ast" && GOWORK=off go build -o "$bin" . ) || {
		bad "could not build overlay-ast to pin fixture digests"
		return 1
	}
	rm -rf "$TMP/current-src"
	for src in enterprise/rtbf/legalhold.go cmd-overlay/olivares/durablebus_enterprise.go \
		cmd-overlay/olivares/addonpacks_enterprise.go enterprise/activation/catalog.go \
		cmd-overlay/olivares/wire_enterprise_addon_cp.go cmd-overlay/olivares/wire_enterprise_noaddon_cp.go; do
		mkdir -p "$(dirname "$TMP/current-src/$src")"
		cp "$TMP/ent/$src" "$TMP/current-src/$src"
	done
	write_current_durable "$TMP/current-src/cmd-overlay/olivares/durablebus_enterprise.go"
	"$bin" "$TMP/ent" >"$TMP/ast-clean.json" 2>"$TMP/ast-clean.err" || ast_rc=$?
	[ "$ast_rc" = 0 ] && { "$bin" "$TMP/current-src" >"$TMP/ast-current.json" 2>>"$TMP/ast-clean.err" || ast_rc=$?; }
	if [ "$ast_rc" != 0 ]; then
		bad "overlay-ast failed on a clean fixture (rc=$ast_rc) [$(cat "$TMP/ast-clean.err")]"
		return 1
	fi
	python3 - "$TMP/ast-clean.json" "$TMP/ast-current.json" "$TMP/reviewed-surface.json" "$TMP/ent" "$TMP/tree" <<'PY'
import hashlib, json, subprocess, sys
legacy_path, current_path, out, ent, tree = sys.argv[1:6]
HELPER = "cmd-overlay/olivares/durablebus_enterprise.go#durableLicensedAt"


def profile(path, construction, absent):
    rep = json.load(open(path, encoding="utf-8"))
    if rep.get("construction") != construction:
        raise SystemExit("clean fixture judged %r, not %s" % (rep.get("construction"), construction))
    failing = sorted(k for k, c in (rep.get("checks") or {}).items() if not c.get("ok"))
    if failing:
        raise SystemExit("clean %s fixture fails %s" % (construction, failing))
    nodes, missing = {}, []
    for key, fn in sorted((rep.get("nodes") or {}).items()):
        if key in absent:
            if fn.get("found"):
                raise SystemExit("%s must be absent from the clean %s fixture" % (key, construction))
            continue
        if fn.get("found") and fn.get("digest"):
            nodes[key] = fn["digest"]
        else:
            missing.append(key)
    if missing or len(nodes) != 11 - len(absent):
        raise SystemExit("clean %s fixture did not produce %d reviewed digests; missing %s"
                         % (construction, 11 - len(absent), missing))
    imports = {p: fr.get("imports") or [] for p, fr in (rep.get("files") or {}).items()}
    return rep, {"nodes": nodes, "imports": imports}


def git(repo, *args):
    return subprocess.run(["git", "-C", repo, *args], capture_output=True, check=True).stdout


def binding(role, repo, path):
    oid = git(repo, "rev-parse", "HEAD:" + path).decode().strip()
    raw = git(repo, "cat-file", "blob", oid)
    return {"role": role, "path": path, "blob": oid, "sha256": hashlib.sha256(raw).hexdigest(),
            "bytes": len(raw), "review_source": git(repo, "rev-parse", "HEAD").decode().strip()}


legacy_rep, legacy = profile(legacy_path, "legacy-direct-v1", {HELPER})
_, current = profile(current_path, "observed-crl-keyring-v1", set())
support = [binding("private", ent, "enterprise/addongate/addongate.go")] + [
    binding("community", tree, "cmd/olivares/%s.go" % f) for f in ("license_holder", "license_trust", "license_crl")]
json.dump({
    "legacy": legacy,
    "current": current,
    "support": support,
    "set_code_packs": legacy_rep.get("setCodePacks") or {},
    "pack_product_ids": legacy_rep.get("packProductIDs") or {},
}, open(out, "w", encoding="utf-8"), indent=2, sort_keys=True)
PY
}

# Commitea lo que haya en el arbol del overlay, mueve `origin/main`, re-sella y re-pina el acta.
#
# ⚠ EL `add` VA ACOTADO A LAS DOS RUTAS DE FUENTE, y no es estetica: el gitlink `public` no
# tiene directorio en este arbol, asi que `git status` lo da por BORRADO y un `add -A` a secas
# lo commitea como tal. Con eso el `ls-tree … public` del lector no encontraba nada y la etapa
# de no-disparo salia 2 en vez de 0 — un fixture roto leyendose como «no he podido mirar».
republish() {
	if [ -n "$($GIT -C "$TMP/ent" status --porcelain -- enterprise cmd-overlay)" ]; then
		$GIT -C "$TMP/ent" add -A -- enterprise cmd-overlay
		$GIT -C "$TMP/ent" commit -q -m mutant
	fi
	$GIT -C "$TMP/ent" push -q origin HEAD:main
	local sha seal_rc=0
	sha="$($GIT -C "$TMP/ent" rev-parse HEAD)"
	OLIVARES_ROOT="$TMP/tree" \
		OLIVARES_ENT_DIR="$TMP/ent" \
		OLIVARES_ACT_ID="$ACT" \
		OLIVARES_OVERLAY_SEAL="$TMP/seal" \
		bash "$ROOT/scripts/fetch-overlay-seal.sh" >"$TMP/seal.out" 2>"$TMP/seal.err" || seal_rc=$?
	if [ "$seal_rc" != 0 ]; then
		bad "official sealer exited $seal_rc [$(tail -1 "$TMP/seal.err" 2>/dev/null)]"
	fi
	write_acta "$sha" "$HUB_PIN"
}

write_acta() {
	python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" "$1" "$2" "$TMP/reviewed-surface.json" <<'PY'
import json, sys
path, sha, pin, surface_path = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
surface = json.load(open(surface_path, encoding="utf-8"))
json.dump({
    "schema": "overlay-live-facts/v2",
    "lote": "FIXTURE",
    "overlay_main_sha": sha,
    "overlay_main_public_pin": pin,
    "legal_hold_evaluation_consults_purchase": False,
    "durable_selection_reads_term": True,
    "durable_selection_reads_purchase_composition": True,
    "durable_purchase_pack": "identity-scale",
    "purchase_composition_order": ["v3-grants", "set-vocabulary", "legacy-no-vocabulary"],
    "legacy_no_vocabulary_entitles_enterprise": False,
    "iso42001_in_catalog": True,
    "iso42001_pack": "compliance-packs",
    "iso42001_kind": "KindWired",
    "iso42001_disposition": "DispActive",
    "iso42001_cut_build_tag": "enterprise && addon_cp",
    "iso42001_stub_build_tag": "enterprise && !addon_cp",
    "iso42001_in_every_enterprise_artifact": False,
    "iso42001_in_public_slug_map": True,
    "iso42001_public_package": "enterprise/iso42001",
    "iso42001_certification": "none",
    "panel_executed": False,
    "u_f": "UNKNOWN",
    "u_d": "UNKNOWN",
    "reviewed_surface_schema": "overlay-ast/v2",
    "reviewed_digest_schema": "overlay-ast/v1",
    "reviewed_construction_sources": {
        "legacy-direct-v1": {"overlay": sha, "community": pin},
        "observed-crl-keyring-v1": {"overlay": sha, "community": pin},
    },
    "reviewed_constructions": {
        "legacy-direct-v1": {
            "reviewed_node_digests": surface["legacy"]["nodes"],
            "required_absent_nodes": ["cmd-overlay/olivares/durablebus_enterprise.go#durableLicensedAt"],
            "reviewed_imports": surface["legacy"]["imports"],
            "support_blobs": [],
            "observed_revocation": "not_read_by_legacy_durable_selection",
        },
        "observed-crl-keyring-v1": {
            "reviewed_node_digests": surface["current"]["nodes"],
            "required_absent_nodes": [],
            "reviewed_imports": surface["current"]["imports"],
            "support_blobs": surface["support"],
            "observed_revocation": "canonical_observation_grace_at_boot",
        },
    },
    "reviewed_set_code_packs": surface["set_code_packs"],
    "reviewed_pack_product_ids": surface["pack_product_ids"],
}, open(path, "w", encoding="utf-8"), indent=2)
PY
}

acta_edit() {
	python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" "$1" "$2" <<'PY'
import json, sys
path, key, raw = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(path, encoding="utf-8"))
d[key] = json.loads(raw)
json.dump(d, open(path, "w", encoding="utf-8"), indent=2)
PY
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		OLIVARES_ENT_DIR="$TMP/ent" \
		OLIVARES_ACT_ID="$ACT" \
		OLIVARES_OVERLAY_SEAL="$TMP/seal" \
		OLIVARES_OVERLAY_LIVE_FACTS_JSON="design/overlay-live-facts-fixture.json" \
		OLIVARES_OVERLAY_LIVE_FACTS_DOC="design/OVERLAY-LIVE-FACTS-fixture.md" \
		bash "$TMP/tree/scripts/check-overlay-live-facts.sh" >"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

expect() { # expect <rc> <label>
	if [ "$(cat "$TMP/rc")" = "$1" ]; then
		ok "$2"
	else
		bad "$2 — got $(cat "$TMP/rc"), wanted $1 [$(tail -1 "$TMP/err")]"
	fi
}

py_edit() { # py_edit <file> <old> <new>
	python3 - "$1" "$2" "$3" <<'PY'
import sys
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
t = open(p, encoding="utf-8").read()
if old not in t:
    raise SystemExit("fixture edit target not found: %r" % old[:60])
open(p, "w", encoding="utf-8").write(t.replace(old, new, 1))
PY
}

CAT="$TMP/ent/enterprise/activation/catalog.go"
DB="$TMP/ent/cmd-overlay/olivares/durablebus_enterprise.go"
LH="$TMP/ent/enterprise/rtbf/legalhold.go"
AP="$TMP/ent/cmd-overlay/olivares/addonpacks_enterprise.go"
CUT="$TMP/ent/cmd-overlay/olivares/wire_enterprise_addon_cp.go"
STUB="$TMP/ent/cmd-overlay/olivares/wire_enterprise_noaddon_cp.go"

ISO_ROW='	{
		Key: "iso42001", Pack: PackCompliancePacks,
		Title: "ISO/IEC 42001 AIMS packs", Kind: KindWired, Disp: DispActive,
		Summary: "drafts for the customer'"'"'s auditor, never certification.",
	},
'

# Sourced, this file is the fixture library of `test-overlay-candidate-facts.sh`: the same
# synthetic overlay, sealer and acta judge both adapters, and none of the cases below runs.
if [ "${BASH_SOURCE[0]}" != "$0" ]; then
	return 0
fi

# ── 0 · no-fire ───────────────────────────────────────────────────────────────────────────
stage
run
expect 0 "no-fire: the sealed facts match the acta (and prose naming addonGate is a decoy)"

# ── 1 · iso42001 removed ──────────────────────────────────────────────────────────────────
stage
py_edit "$CAT" "$ISO_ROW" ""
republish
run
expect 1 "firing: the iso42001 catalog row is gone"

# ── 2 · iso42001 duplicated ───────────────────────────────────────────────────────────────
stage
py_edit "$CAT" "$ISO_ROW" "$ISO_ROW$ISO_ROW"
republish
run
expect 1 "firing: a duplicated iso42001 row is not a catalog"

# ── 3 · wrong pack ────────────────────────────────────────────────────────────────────────
stage
py_edit "$CAT" 'Key: "iso42001", Pack: PackCompliancePacks,' 'Key: "iso42001", Pack: PackBusiness,'
republish
run
expect 1 "firing: iso42001 moved to another pack"

# ── 4 · the row is gone and a COMMENT names it ────────────────────────────────────────────
stage
py_edit "$CAT" "$ISO_ROW" '	// Key: "iso42001", Pack: PackCompliancePacks, Kind: KindWired, Disp: DispActive
'
republish
run
expect 1 "firing: a commented-out row does not restore the capability"

# ── 5 · durable: unpurchased selection becomes allowed ────────────────────────────────────
stage
py_edit "$DB" '	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)' '	return true'
republish
run
expect 1 "firing: durableLicensed returns an unconditional true (grant bypass)"

# ── 6 · durable: bypass hidden BEFORE the composition ─────────────────────────────────────
stage
py_edit "$DB" '	c, ok := holder.claims()' '	if getenv("OLIVARES_TRIAL") != "" {
		return true
	}
	c, ok := holder.claims()'
republish
run
expect 1 "firing: an early unconditional true is a bypass even with the composition intact"

# ── 7 · durable: back to the universal term ───────────────────────────────────────────────
stage
py_edit "$DB" '	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)' '	return c.Status(time.Now()) != license.StatusExpired'
republish
run
expect 1 "firing: a universal term with no purchase composition"

# ── 8 · durable: term check dropped ───────────────────────────────────────────────────────
stage
py_edit "$DB" '	if !ok || c.Status(time.Now()) == license.StatusExpired {' '	if !ok {'
republish
run
expect 1 "firing: the expiry term is no longer read"

# ── 9 · durable: another pack ─────────────────────────────────────────────────────────────
stage
py_edit "$DB" 'activation.PackIdentityScale)' 'activation.PackBusiness)'
republish
run
expect 1 "firing: the durable bus is selected on a pack the acta does not record"

# ── 10 · legal hold made conditional on a commercial grant ────────────────────────────────
stage
py_edit "$LH" '	decision := &OverrideDecision{' '	if err := addonGate("rtbf").Authorize(context.Background(), "legalhold-override"); err != nil {
		return nil, err
	}
	decision := &OverrideDecision{'
republish
run
expect 1 "firing: the legal-hold evaluation consults a commercial entitlement"

# ── 11 · legal hold loses a governance denial ─────────────────────────────────────────────
stage
py_edit "$LH" '	if approvers < h.MinApprovers {' '	if false {'
republish
run
expect 1 "firing: the approver floor is gone from EvaluateOverride"

# ── 12 · the legacy no-vocabulary read starts entitling the negotiated pack ───────────────
stage
py_edit "$AP" '			activation.PackCompliancePacks, activation.PackIdentityScale:' '			activation.PackCompliancePacks, activation.PackIdentityScale, activation.PackEnterprise:'
republish
run
expect 1 "firing: legacy compatibility now grants the enterprise pack"

# ── 13 · an EMPTY v3 grant list stops refusing ────────────────────────────────────────────
stage
py_edit "$AP" '		if len(list) == 0 {
			return false
		}
' ''
republish
run
expect 1 "firing: removing the empty-list guard is unverified structure (production loop-end false is equivalent; return true is not)"

# ── 14 · the v3 list stops winning over the claims read ───────────────────────────────────
stage
py_edit "$AP" '		if grants != nil {
			if list, ok := grants(); ok && list != nil {
				return fromGrants(p)
			}
		}
' ''
republish
run
expect 1 "firing: the composition no longer prefers a real v3 grant list"

# ── 15 · the cut: the no-addon build ships the module ─────────────────────────────────────
stage
py_edit "$STUB" '	return nil' '	return iso42001.NewPackager(addonGate("iso42001"))'
republish
run
expect 1 "firing: the !addon_cp build no longer cuts the module out"

# ── 16 · the acta claims a panel it does not have ─────────────────────────────────────────
stage
acta_edit panel_executed true
run
expect 1 "firing: panel_executed cannot be flipped by this contract"

stage
acta_edit iso42001_certification '"ISO 42001"'
run
expect 1 "firing: the pack builds drafts; certification is never claimed here"

stage
acta_edit iso42001_in_every_enterprise_artifact true
run
expect 1 "firing: KindWired is not 'in every artifact'"

# ── 17 · THE REPLACED SAME-SHA ORACLE, IN ITS THREE SURVIVING ANSWERS ─────────────────────
#
# ⛔ ESTA SECCION DECIA «un pin rancio es un hallazgo que el re-medidor puede curar», y probaba
# UNA sola cosa: que un `overlay_main_sha` distinto del vivo daba 1. ESE oraculo ERA el
# acoplamiento que LT1 retira — obligaba a reescribir diez campos versionados en siete actas
# cada vez que el overlay avanzaba sin que cambiara nada de lo que este contrato juzga. No se
# borra: se PARTE en las tres preguntas que sí discriminan, y las tres se miden aqui.
#
#   (a) la linea base es un ANCESTRO del main capturado y las propiedades siguen ciertas -> 0,
#       y los bytes del acta no se tocan. Esta es LA REFORMA, y se mide con un hijo de ARBOL
#       IDENTICO, que es el caso exacto que costaba las reescrituras;
#   (b) la linea base es un commit REAL sin historia compartida (force-push que conserva el
#       contenido) -> 1, con la razon nombrada;
#   (c) la linea base no es ni siquiera un objeto de este almacen -> 2, «no he podido mirar»,
#       porque no se ha podido validar el registro contra nada. NUNCA 0.
stage
_parent="$($GIT -C "$TMP/ent" rev-parse HEAD)"
_child="$(GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
	$GIT -C "$TMP/ent" commit-tree "$($GIT -C "$TMP/ent" rev-parse 'HEAD^{tree}')" \
	-p "$_parent" -m 'same-tree child of the overlay base')"
$GIT -C "$TMP/ent" reset -q --hard "$_child"
republish
acta_edit overlay_main_sha "\"$_parent\""
run
expect 0 "no-fire (THE BEHAVIOUR CHANGE): a same-tree child advances the overlay and the acta, pinning its ANCESTOR, still passes untouched"

stage
_orphan="$(GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
	$GIT -C "$TMP/ent" commit-tree "$($GIT -C "$TMP/ent" rev-parse 'HEAD^{tree}')" \
	-m 'orphan carrying the identical tree')"
acta_edit overlay_main_sha "\"$_orphan\""
run
expect 1 "firing: a baseline that is a real commit with NO shared history is a finding, not a harmless advance"

stage
acta_edit overlay_main_sha '"0123456789012345678901234567890123456789"'
run
expect 2 "firing: a baseline that is not an object in this store is COULD NOT LOOK, never a pass"

# ── 18 · a Community pin the overlay does not declare ─────────────────────────────────────
stage
acta_edit overlay_main_public_pin "\"$($GIT -C "$TMP/tree" rev-parse HEAD)\""
python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_main_public_pin"] = "9999999999999999999999999999999999999999"
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
run
expect 1 "firing: the acta records a Community pin the overlay does not declare"

# ── 19 · a WORKING TREE change moves nothing ──────────────────────────────────────────────
stage
py_edit "$CAT" "$ISO_ROW" ""
run
expect 0 "no-fire: a dirty overlay working tree cannot move a verdict taken on sealed blobs"

# ── 20 · reader failures and the seal are 2, never 0 and never 1 ──────────────────────────
stage
rm -f "$TMP/ent/cmd-overlay/olivares/addonpacks_enterprise.go"
republish
run
expect 2 "LOOK: a source missing from the pinned commit is 'could not look'"

stage
printf '%s\n' 'package main' 'func (' >"$AP"
republish
run
expect 2 "LOOK: a malformed Go AST is 'could not look'"

stage
python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_main_public_pin"] = "9999999999999999999999999999999999999999"
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
$GIT -C "$TMP/ent" update-index --add --cacheinfo "160000,9999999999999999999999999999999999999999,public"
$GIT -C "$TMP/ent" commit -q -m "unknown community pin"
republish
python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_main_public_pin"] = "9999999999999999999999999999999999999999"
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
run
expect 2 "LOOK: a declared Community commit that is not in the object store is 'could not look'"

stage
printf '%s %s %s rc=0\n' "$(date -u +%s)" "another-act" "$($GIT -C "$TMP/ent" rev-parse HEAD)" >"$TMP/seal"
run
expect 2 "LOOK: a seal from another act is not freshness"

stage
printf '%s %s %s rc=1\n' "$(date -u +%s)" "$ACT" "$($GIT -C "$TMP/ent" rev-parse HEAD)" >"$TMP/seal"
run
expect 2 "LOOK: a seal that records a FAILED fetch is not freshness"

stage
rm -f "$TMP/seal"
run
expect 2 "LOOK: no seal at all is not freshness"

stage
rc=0
OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" OLIVARES_ACT_ID="$ACT" \
	OLIVARES_OVERLAY_SEAL="$TMP/seal" \
	OLIVARES_OVERLAY_LIVE_FACTS_JSON="design/overlay-live-facts-fixture.json" \
	OLIVARES_OVERLAY_LIVE_FACTS_DOC="design/OVERLAY-LIVE-FACTS-fixture.md" \
	bash "$TMP/tree/scripts/check-overlay-live-facts.sh" >"$TMP/out" 2>"$TMP/err" || rc=$?
echo "$rc" >"$TMP/rc"
expect 2 "LOOK: no overlay named is 'could not look', never CLEAN"

stage
rm -f "$TMP/tree/design/overlay-live-facts-fixture.json"
run
expect 2 "LOOK: a missing acta is 'could not look'"

# ── 21 · FIVE behaviour-changing discriminators the token census called CLEAN ─────────────
stage
py_edit "$AP" '				return fromGrants(p)' '				_ = fromGrants(p)
				return fromClaims(p)'
republish
run
expect 1 "firing: v3 branch calls fromGrants but always returns fromClaims"

stage
py_edit "$AP" '		if len(list) == 0 {
			return false
		}
' '		if len(list) == 0 {
			return true
		}
'
republish
run
expect 1 "firing: empty v3 list returns true (bought nothing, granted everything)"

stage
py_edit "$LH" '	if !h.Enabled {' '	if !h.Enabled && false {'
republish
run
expect 1 "firing: disabled legal-hold guard is forced false"

stage
py_edit "$LH" '	if holdID == "" {
		return nil, fmt.Errorf("rtbf: holdID is required for override evaluation")
	}
' '	if holdID == "" {
		return nil, fmt.Errorf("rtbf: holdID is required for override evaluation")
	}
	if os.Getenv("OLIVARES_PAID") != "yes" {
		return nil, fmt.Errorf("rtbf: paid entitlement required")
	}
'
republish
run
expect 1 "firing: EvaluateOverride gains an opaque paid gate (no blacklist name required)"

stage
py_edit "$CUT" 'func newAIMSPackager() compliance.AIMSPackager {
	return iso42001.NewPackager(addonGate("iso42001"))
}' 'func newAIMSPackager() compliance.AIMSPackager {
	if false {
		return iso42001.NewPackager(addonGate("iso42001"))
	}
	return nil
}'
republish
run
expect 1 "firing: AIMS composition survives inside if false and the function returns nil"

# ── 22 · readable but unreviewed structure, one body each, is 1 never 0 ───────────────────
stage
py_edit "$DB" '	holder := newLicenseHolder(pub, src, nil, slog.Default())' '	if getenv("DEBUG") == "1" {
		return false
	}
	holder := newLicenseHolder(pub, src, nil, slog.Default())'
republish
run
expect 1 "firing: extra deny in durableLicensed is estructura no verificada"

stage
py_edit "$AP" '		return fromClaims(p)' '		_ = p
		return fromClaims(p)'
republish
run
expect 1 "firing: extra statement in combinedPurchaseView is estructura no verificada"

stage
py_edit "$AP" '		want := packProductID(p)' '		if false {
			return false
		}
		want := packProductID(p)'
republish
run
expect 1 "firing: extra guard in purchaseViewFromGrants is estructura no verificada"

stage
py_edit "$LH" '	decision := &OverrideDecision{RequiredApprovers: h.MinApprovers, ProvidedApprovers: approvers}' '	if reason == "skip" {
		return nil, fmt.Errorf("rtbf: skip")
	}
	decision := &OverrideDecision{RequiredApprovers: h.MinApprovers, ProvidedApprovers: approvers}'
republish
run
expect 1 "firing: extra branch in EvaluateOverride is estructura no verificada"

stage
py_edit "$CUT" '	return iso42001.NewPackager(addonGate("iso42001"))' '	_ = 0
	return iso42001.NewPackager(addonGate("iso42001"))'
republish
run
expect 1 "firing: extra statement in newAIMSPackager is estructura no verificada"

# ── 23 · equivalent term wrap is unverified structure, not a behavioural claim ────────────
stage
py_edit "$DB" '	if !ok || c.Status(time.Now()) == license.StatusExpired {' '	if !ok || (false && c.Status(time.Now()) == license.StatusExpired) {'
republish
run
expect 1 "firing: wrapping StatusExpired in false && … is unverified structure"

# ── 24 · comments and extra blank lines (gofmt) must not move the verdict ─────────────────
stage
py_edit "$DB" '	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)' '	// decoy: return true; fromGrants; addonGate; OLIVARES_PAID; iso42001.NewPackager
	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)'
republish
run
expect 0 "no-fire: a comment inside durableLicensed cannot move a closed-form digest"

stage
py_edit "$LH" '	decision := &OverrideDecision{RequiredApprovers: h.MinApprovers, ProvidedApprovers: approvers}
	if !h.Enabled {' '	decision := &OverrideDecision{RequiredApprovers: h.MinApprovers, ProvidedApprovers: approvers}

	if !h.Enabled {'
republish
run
expect 0 "no-fire: extra blank lines (gofmt) cannot move EvaluateOverride"

# Independent review additions. Both sources are parseable Go and therefore must
# be classified as unverified structure (1), never as a reader failure (2).
stage
py_edit "$AP" '		if grants != nil {
			if list, ok := grants(); ok && list != nil {
				return fromGrants(p)
			}
		}
		return fromClaims(p)' ''
republish
run
printf 'independent_mutant=empty-combined-closure commit=%s observed_exit=%s\n' \
	"$($GIT -C "$TMP/ent" rev-parse HEAD)" "$(cat "$TMP/rc")"
[ "$(cat "$TMP/rc")" = 1 ] || sed 's/^/reader-detail: /' "$TMP/err" >&2
expect 1 "firing: a parseable empty combinedPurchaseView closure is unverified structure"

# A bare return is valid syntax even though this bool-returning declaration would
# fail type-checking. Its AST remains readable and outside the reviewed closed form.
stage
py_edit "$DB" '	return combinedPurchaseView(holder.claims, holder.grants)(activation.PackIdentityScale)' '	return'
republish
run
printf 'independent_mutant=bare-durable-return commit=%s observed_exit=%s\n' \
	"$($GIT -C "$TMP/ent" rev-parse HEAD)" "$(cat "$TMP/rc")"
[ "$(cat "$TMP/rc")" = 1 ] || sed 's/^/reader-detail: /' "$TMP/err" >&2
expect 1 "firing: a parseable bare return in durableLicensed is unverified structure"

# ── 26 · setCodePacks / packProductID Identity & Scale, SHA-only remeasure ───────────────
other_nodes_unchanged() { # other_nodes_unchanged <mutated-key>
	local ast_rc=0
	"$TMP/overlay-ast-pin" "$TMP/ent" >"$TMP/ast-mut.json" 2>"$TMP/ast-mut.err" || ast_rc=$?
	if [ "$ast_rc" != 0 ]; then
		bad "overlay-ast failed on the mutant (rc=$ast_rc) [$(cat "$TMP/ast-mut.err")]"
		return
	fi
	if python3 - "$TMP/reviewed-surface.json" "$TMP/ast-mut.json" "$1" <<'PY'
import json, sys
pinned = json.load(open(sys.argv[1], encoding="utf-8"))["legacy"]["nodes"]
rep = json.load(open(sys.argv[2], encoding="utf-8"))
mut = sys.argv[3]
moved = []
for key, want in pinned.items():
    if key == mut:
        continue
    got = ((rep.get("nodes") or {}).get(key) or {}).get("digest")
    if got != want:
        moved.append(key)
if moved:
    raise SystemExit("moved: " + ", ".join(moved))
if ((rep.get("nodes") or {}).get(mut) or {}).get("digest") == pinned.get(mut):
    raise SystemExit("mutated node digest did not change")
PY
	then
		ok "other reviewed nodes unchanged after $1"
	else
		bad "other reviewed nodes moved for $1"
	fi
}

stage
py_edit "$AP" '	"ids":  activation.PackIdentityScale,' '	"ids":  activation.PackBusiness,'
republish
run
expect 1 "firing: set:ids mapped to PackBusiness (Identity & Scale drift)"
other_nodes_unchanged "cmd-overlay/olivares/addonpacks_enterprise.go#setCodePacks"

stage
py_edit "$AP" '		return "self_hosted.business.addons.identity-scale"' '		return "self_hosted.business.addons.regulated"'
republish
run
expect 1 "firing: packProductID Identity & Scale product string drifted"
other_nodes_unchanged "cmd-overlay/olivares/addonpacks_enterprise.go#packProductID"

# ── 27 · missing target, missing acta key, and both (the set cannot shrink) ──────────────
stage
py_edit "$AP" 'func packProductID(p activation.Pack) string {
	switch p {
	case activation.PackBusiness:
		return "self_hosted.business"
	case activation.PackRegulated:
		return "self_hosted.business.addons.regulated"
	case activation.PackAIRuntimeSecurity:
		return "self_hosted.business.addons.ai-runtime-security"
	case activation.PackCompliancePacks:
		return "self_hosted.business.addons.compliance-packs"
	case activation.PackIdentityScale:
		return "self_hosted.business.addons.identity-scale"
	case activation.PackEnterprise:
		return "self_hosted.enterprise"
	}
	return ""
}

' ''
republish
run
expect 1 "firing: missing reviewed target packProductID"

stage
python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["reviewed_constructions"]["legacy-direct-v1"]["reviewed_node_digests"].pop(
    "cmd-overlay/olivares/addonpacks_enterprise.go#attestedPacks", None)
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
run
expect 1 "firing: missing acta key attestedPacks cannot shrink the reviewed set"

stage
py_edit "$AP" 'func attestedPacks(c license.Claims) (map[activation.Pack]bool, bool) {
	out := map[activation.Pack]bool{}
	vocabulary := false
	for _, f := range c.Features {
		code, found := strings.CutPrefix(strings.ToLower(f), "set:")
		if !found {
			continue
		}
		vocabulary = true
		if p, known := setCodePacks[code]; known {
			out[p] = true
		}
	}
	return out, vocabulary
}

' ''
republish
python3 - "$TMP/tree/design/overlay-live-facts-fixture.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["reviewed_constructions"]["legacy-direct-v1"]["reviewed_node_digests"].pop(
    "cmd-overlay/olivares/addonpacks_enterprise.go#attestedPacks", None)
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
run
expect 1 "firing: missing target and missing acta key cannot shrink the reviewed set"

stage
py_edit "$AP" '"github.com/olivaresai/olivares/enterprise/activation"' '"github.com/example/not-activation"'
republish
run
expect 1 "firing: the same function AST with an altered package import is not the reviewed decision"

# ── 25 · restored, still CLEAN ────────────────────────────────────────────────────────────
stage
run
expect 0 "no-fire: the restored fixture is CLEAN again"

# ── 28 · inherited Git selectors cannot redirect the sealed reader ────────────────────────
# Own fixture overlay + Community repos only. A decoy is poisoned into GIT_DIR,
# GIT_WORK_TREE and GIT_INDEX_FILE. Isolation must keep the CLEAN sealed read
# and leave the decoy byte/ref/config/index identical. Removing ONLY the source
# of lib/git-env.sh must make that same positive fail or resolve the decoy.
stage
DECOY="$TMP/decoy"
rm -rf "$DECOY"
mkdir -p "$DECOY"
$GIT init -q -b poison "$DECOY"
printf 'decoy-not-overlay\n' >"$DECOY/README"
$GIT -C "$DECOY" add README
$GIT -C "$DECOY" commit -q -m decoy
$GIT -C "$DECOY" update-ref refs/remotes/origin/main "$($GIT -C "$DECOY" rev-parse HEAD)"
$GIT -C "$DECOY" config user.name DECOY-MUST-NOT-MOVE
$GIT -C "$DECOY" config user.email decoy@example.invalid
DECOY_GIT="$($GIT -C "$DECOY" rev-parse --absolute-git-dir)"
DECOY_INDEX="$DECOY_GIT/index"
DECOY_SHA="$($GIT -C "$DECOY" rev-parse HEAD)"
FIXTURE_SHA="$($GIT -C "$TMP/ent" rev-parse HEAD)"

decoy_fingerprint() {
	local d="$1" gd
	gd="$(git -C "$d" rev-parse --absolute-git-dir)"
	{
		printf 'HEAD=%s\n' "$(git -C "$d" rev-parse HEAD 2>&1)"
		printf 'refs=%s\n' "$(git -C "$d" for-each-ref --format='%(refname)=%(objectname)' 2>&1 | LC_ALL=C sort | tr '\n' ' ')"
		printf 'config=%s\n' "$(git -C "$d" config --local --list 2>&1 | LC_ALL=C sort | tr '\n' ' ')"
		printf 'index-stage=%s\n' "$(git -C "$d" ls-files --stage 2>&1 | LC_ALL=C sort | tr '\n' ' ')"
		printf 'index-file=%s\n' "$(sha256sum "$gd/index" 2>/dev/null | awk '{print $1}')"
		find "$d" -type f ! -name '*.lock' -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
	}
}

before_decoy="$(decoy_fingerprint "$DECOY")"
rc=0
OLIVARES_ROOT="$TMP/tree" \
	OLIVARES_ENT_DIR="$TMP/ent" \
	OLIVARES_ACT_ID="$ACT" \
	OLIVARES_OVERLAY_SEAL="$TMP/seal" \
	OLIVARES_OVERLAY_LIVE_FACTS_JSON="design/overlay-live-facts-fixture.json" \
	OLIVARES_OVERLAY_LIVE_FACTS_DOC="design/OVERLAY-LIVE-FACTS-fixture.md" \
	GIT_DIR="$DECOY_GIT" \
	GIT_WORK_TREE="$DECOY" \
	GIT_INDEX_FILE="$DECOY_INDEX" \
	bash "$TMP/tree/scripts/check-overlay-live-facts.sh" >"$TMP/out" 2>"$TMP/err" || rc=$?
echo "$rc" >"$TMP/rc"
expect 0 "isolation: poisoned GIT_DIR/WORK_TREE/INDEX still reads the sealed fixture as CLEAN"
after_decoy="$(decoy_fingerprint "$DECOY")"
if [ "$before_decoy" = "$after_decoy" ]; then
	ok "isolation: decoy remains byte/ref/config/index identical"
else
	bad "isolation: decoy moved under poisoned selectors"
fi
case "$(cat "$TMP/out")$(cat "$TMP/err")" in
*"${FIXTURE_SHA:0:9}"*) ok "isolation: CLEAN names the fixture overlay pin, not the decoy" ;;
*) bad "isolation: CLEAN output did not name the fixture overlay pin" ;;
esac
case "$(cat "$TMP/out")$(cat "$TMP/err")" in
*"$DECOY_SHA"*) bad "isolation: CLEAN output named the decoy SHA (wrong source)" ;;
*) ok "isolation: CLEAN output does not name the decoy SHA" ;;
esac

# ⛔ EL CONTROL CAUSAL MUTA LOS **DOS** SITIOS DE AISLAMIENTO, Y ESO ES UNA CORRECCION CON SU
# MEDIDA. Hasta LT1 el aislamiento vivia SOLO en este lector, asi que quitarselo era quitar la
# propiedad. Desde LT1 el Modulo (`scripts/lib/overlay-measurement.sh`) carga `git-env.sh`
# ANTES de su primera operacion git (F9), asi que el lector puede perder el suyo y el Modulo
# restablece el aislamiento antes de cualquier lectura de los dos almacenes: mutar solo el
# lector dejo de ser causal. Y no fallo en silencio — fallo INTERMITENTEMENTE, que es peor:
# medido dos veces seguidas sobre el MISMO arbol, `63 passed, 2 failed` y luego `65 passed, 0
# failed`. Un control que a veces mata y a veces no, no es un control.
#
# ⇒ Se muta LA PROPIEDAD, que hoy son dos sitios. El mutante debe fallar SIEMPRE: sin ninguno
#   de los dos, el primer `rev-parse` de dos almacenes lee el SEÑUELO y la captura se cae.
MUT="$TMP/check-overlay-live-facts.mutant.sh"
MUTLIB="$TMP/tree/scripts/lib/overlay-measurement.sh"
cp "$MUTLIB" "$MUTLIB.orig"
sed -e 's#^\. "\$_olivares_git_env" .*#true || {#' \
	"$TMP/tree/scripts/check-overlay-live-facts.sh" >"$MUT"
sed -i -e 's#^\tif ! \. "\$lib/git-env.sh"; then#\tif ! true; then#' "$MUTLIB"
if grep -q 'true || {' "$MUT" && ! grep -q '^\. "\$_olivares_git_env"' "$MUT" \
	&& grep -q 'if ! true; then' "$MUTLIB"; then
	ok "isolation: mutant removed the git-env source from BOTH the reader and the Module"
else
	bad "isolation: mutation did not apply — causal control is dead"
fi
if bash -n "$MUT" 2>/dev/null; then
	ok "isolation: mutant still parses"
else
	bad "isolation: mutant does not parse — causal control is dead"
fi
mut_rc=0
OLIVARES_ROOT="$TMP/tree" \
	OLIVARES_ENT_DIR="$TMP/ent" \
	OLIVARES_ACT_ID="$ACT" \
	OLIVARES_OVERLAY_SEAL="$TMP/seal" \
	OLIVARES_OVERLAY_LIVE_FACTS_JSON="design/overlay-live-facts-fixture.json" \
	OLIVARES_OVERLAY_LIVE_FACTS_DOC="design/OVERLAY-LIVE-FACTS-fixture.md" \
	GIT_DIR="$DECOY_GIT" \
	GIT_WORK_TREE="$DECOY" \
	GIT_INDEX_FILE="$DECOY_INDEX" \
	bash "$MUT" >"$TMP/mut.out" 2>"$TMP/mut.err" || mut_rc=$?
cp "$MUTLIB.orig" "$MUTLIB"
if [ "$mut_rc" != 0 ]; then
	ok "isolation causal: removing ONLY isolation makes the same positive fail (rc=$mut_rc)"
else
	# The mutant's own words, or a future flake names nothing and costs the same hour twice.
	bad "isolation causal: mutant still CLEAN — isolation is not load-bearing [$(tail -1 "$TMP/mut.err" 2>/dev/null)]"
fi
case "$(cat "$TMP/mut.out")$(cat "$TMP/mut.err")" in
*"$DECOY_SHA"* | *"cannot resolve origin/main"*)
	ok "isolation causal: mutant resolved the decoy (wrong source) or failed to read origin/main"
	;;
*)
	if [ "$mut_rc" != 0 ]; then
		ok "isolation causal: mutant failed the sealed positive without isolation"
	else
		bad "isolation causal: mutant neither failed nor named the decoy [$(tail -1 "$TMP/mut.err" 2>/dev/null)]"
	fi
	;;
esac
after_mut_decoy="$(decoy_fingerprint "$DECOY")"
if [ "$before_decoy" = "$after_mut_decoy" ]; then
	ok "isolation causal: decoy still unmodified after the mutant read"
else
	bad "isolation causal: mutant wrote into the decoy"
fi

# ── 29 · observed-crl-keyring-v1 on the sealed main: one whole profile, exact support ────
# The main adapter's retained result line, read as data.
result_facts() { # result_facts <python expression over r>
	python3 - "$TMP/out" "$1" <<'PY'
import json, sys
lines = [l for l in open(sys.argv[1], encoding="utf-8") if l.startswith("overlay-facts-result/v1 ")]
r = json.loads(lines[-1].split(" ", 1)[1]) if lines else None
print("<no result>" if r is None else eval(sys.argv[2], {"r": r}))
PY
}

# Commit the Community support change in the working tree, point the overlay's gitlink at it,
# re-seal, and re-pin the acta baseline to that gitlink (the Module requires the pair).
repin_community() {
	local pin
	$GIT -C "$TMP/tree" add -A -- cmd/olivares
	$GIT -C "$TMP/tree" commit -q -m "support change"
	pin="$($GIT -C "$TMP/tree" rev-parse HEAD)"
	$GIT -C "$TMP/ent" update-index --add --cacheinfo "160000,$pin,public"
	$GIT -C "$TMP/ent" commit -q -m "gitlink to the support change"
	republish
	write_acta "$($GIT -C "$TMP/ent" rev-parse HEAD)" "$pin"
}

SHAPE='(r["construction"], len(r["inputs"]), len(r["support"]), sum(n["present"] for n in r["nodes"].values()), len(r["checks"]), r["community"]["source"])'

stage
run
expect 0 "no-fire: the legacy construction on the sealed main"
if [ "$(result_facts "$SHAPE")" = "('legacy-direct-v1', 7, 0, 10, 40, 'captured-main-gitlink')" ]; then
	ok "legacy result: 7 inputs, no support, 10 present nodes (the helper explicitly absent), 40 checks"
else
	bad "legacy result shape is $(result_facts "$SHAPE")"
fi

stage
write_current_durable "$DB"
republish
run
expect 0 "no-fire: observed-crl-keyring-v1 (wrapper, helper, four exact support files) on the sealed main"
if [ "$(result_facts "$SHAPE")" = "('observed-crl-keyring-v1', 11, 4, 11, 40, 'captured-main-gitlink')" ]; then
	ok "current result: 11 inputs, 4 support identities, 11 present nodes, 40 checks"
else
	bad "current result shape is $(result_facts "$SHAPE")"
fi

stage
write_current_durable "$DB"
py_edit "$DB" 'getenv, time.Now)' 'getenv, nil)'
republish
run
expect 1 "firing: the current wrapper forwards a nil clock"

stage
write_current_durable "$DB"
republish
printf '\n// an unreviewed byte\n' >>"$TMP/tree/cmd/olivares/license_trust.go"
repin_community
run
expect 1 "firing: a byte-changed Community support file at the declared gitlink is unreviewed"

stage
write_current_durable "$DB"
republish
rm -f "$TMP/tree/cmd/olivares/license_crl.go"
repin_community
run
expect 2 "LOOK: a Community support file missing at the declared gitlink is could-not-look"

stage
rm -f "$TMP/tree/cmd/olivares/license_crl.go"
repin_community
run
expect 0 "no-fire: the legacy construction reads no support file"

stage
write_current_durable "$DB"
printf '\n// an unreviewed byte\n' >>"$TMP/ent/enterprise/addongate/addongate.go"
republish
run
expect 1 "firing: a byte-changed private support file is unreviewed"

stage
write_current_durable "$TMP/current-durable.go"
python3 - "$DB" "$TMP/current-durable.go" <<'PY'
import sys
legacy, current = sys.argv[1], sys.argv[2]
helper = open(current, encoding="utf-8").read()
helper = helper[helper.index("func durableLicensedAt("):]
open(legacy, "a", encoding="utf-8").write("\n" + helper)
PY
republish
run
expect 1 "firing: the legacy body beside the current helper is neither construction"

echo "check-overlay-live-facts selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
exit 0
