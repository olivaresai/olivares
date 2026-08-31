#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/release-preflight.sh — the fail-closed release-target preflight
# (design §C.4). One case per requirement class: BOTH accepted tuples resolve to exactly
# the reviewed coordinates, and every rejected cross-product from design §E.1.7 exits
# nonzero — absent mode, arbitrary mode, wrong repository, branch ref, tag/ref mismatch,
# wrong tag grammar, arbitrary target, production target in rehearsal, invalid or drifted
# publication switches, wrong signing mode, credential half-configs, secrets present in
# rehearsal, missing/equal anchors, missing SLSA acknowledgement, and a missing output
# sink. The §C.4.6 production-surface tripwire is exercised DIRECTLY: the rehearsal
# expectations are injected inputs (the public script embeds no rehearsal destination —
# deny-closed, export gate), so hostile expected tuples reach the tripwire as data.
#
# Also pins the build-phase invariant the preflight relies on: .goreleaser.yaml's
# dockers/docker_manifests sections contain NO `latest` — aliases are promotion-only.
#
# NO `set -e` HERE, DELIBERATELY (see test-pg-test-env.sh: a failing assertion must
# report, not silently truncate the battery). Setup commands carry explicit `|| exit`.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/release-preflight.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-relpf.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-62s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-62s %s\n' "$1" "$2"
	fi
}

# Two distinct, valid 32-byte anchors for the production cases.
LIC="$(head -c 32 /dev/urandom | base64 -w0)" || exit 1
OTA="$(head -c 32 /dev/urandom | base64 -w0)" || exit 1

# §C.4.8 ahora comprueba IDENTIDAD, no sólo forma (scripts/check-release-anchor-identity.sh). Las
# anclas de esta batería son aleatorias por diseño, así que se le da su propia tabla revisada de
# juguete: la batería sigue midiendo el preflight y NO se le desactiva el gate — desactivarlo aquí
# dejaría sin cubrir justo la ruta de producción que el gate existe para proteger.
# ⛔ DENTRO DE $WORK, y sin `trap` propio: la línea 28 ya instala `trap cleanup EXIT HUP INT TERM`,
# y un segundo `trap ... EXIT` NO se encadena — SUSTITUYE al primero. Mi primera versión ponía uno y
# dejaba de correr `cleanup`, o sea filtraba el directorio de trabajo entero en cada pasada. La
# batería seguía en 40/40 porque ninguna aserción mira la limpieza: verde por otra vía. Colgando de
# $WORK, el `cleanup` que ya existe se lo lleva.
PFTBL="$WORK/pf-anchors.md"
{
	printf '| Release | Domain | Public key (base64-std) | SHA-256 fingerprint | `version` prefix |\n'
	printf '|---|---|---|---|---|\n'
	printf '| v26.8.0 | license | `%s` | x | x |\n' "$LIC"
	printf '| v26.8.0 | OTA | `%s` | x | x |\n' "$OTA"
} >"$PFTBL"
# ⛔ Y VAN DENTRO DE LA TUPLA `PROD`, no exportadas: run_pf lanza el preflight con `env -i`, que
# borra el entorno entero. Exportarlas parece funcionar y no llega nada — medido aquí mismo: cinco
# casos de producción en rojo con la tabla puesta y sin usar.
SHA="0123456789abcdef0123456789abcdef01234567"

PROD=(
	RELEASE_MODE=production
	RELEASE_GITHUB_REPO=olivaresai/olivares
	OCI_IMAGE_REPO=ghcr.io/olivaresai/olivares
	SOURCE_REPOSITORY_URL=https://github.com/olivaresai/olivares
	RELEASE_TAG=v26.8.0
	COSIGN_MODE=keyless
	COSIGN_TLOG_UPLOAD=true
	PUBLISH_LATEST=true
	PUBLISH_DOCKERHUB=auto
	PUBLISH_HOMEBREW=auto
	PUBLISH_OTA_STABLE=true
	RUN_SLSA=true
	GITHUB_REPOSITORY=olivaresai/olivares
	GITHUB_REF=refs/tags/v26.8.0
	GITHUB_REF_NAME=v26.8.0
	GITHUB_REF_TYPE=tag
	GITHUB_SHA="$SHA"
	OLIVARES_LICENSE_PUBKEY="$LIC"
	OLIVARES_OTA_PUBKEY="$OTA"
	OLIVARES_ANCHOR_TABLE="$PFTBL"
	OLIVARES_ANCHOR_DIR="$PFTBL.no-ceremony-record"
)

# The rehearsal identity is INTERNAL and the public preflight embeds none (deny-closed:
# the internal workflow injects its expected tuple). This battery therefore drives the
# rehearsal path with a NEUTRAL stand-in identity — the script's logic is under test,
# not the internal name, which must never appear in a shipped file (export gate).
REH_ID="rehearsal-org/olivares-rehearsal"
REH=(
	RELEASE_MODE=rehearsal
	RELEASE_GITHUB_REPO="$REH_ID"
	OCI_IMAGE_REPO="ghcr.io/$REH_ID"
	SOURCE_REPOSITORY_URL="https://github.com/$REH_ID"
	OLIVARES_REHEARSAL_EXPECTED_REPO="$REH_ID"
	OLIVARES_REHEARSAL_EXPECTED_OCI="ghcr.io/$REH_ID"
	OLIVARES_REHEARSAL_EXPECTED_SOURCE="https://github.com/$REH_ID"
	RELEASE_TAG=v0.0.0-rehearsal.1
	COSIGN_MODE=key
	COSIGN_TLOG_UPLOAD=false
	PUBLISH_LATEST=false
	PUBLISH_DOCKERHUB=false
	PUBLISH_HOMEBREW=false
	PUBLISH_OTA_STABLE=false
	RUN_SLSA=true
	ACKNOWLEDGE_PUBLIC_SLSA_LOG=true
	GITHUB_REPOSITORY="$REH_ID"
	GITHUB_REF=refs/tags/v0.0.0-rehearsal.1
	GITHUB_REF_NAME=v0.0.0-rehearsal.1
	GITHUB_REF_TYPE=tag
	GITHUB_SHA="$SHA"
)

# The preprod identity is INTERNAL for exactly the same reason as the rehearsal one, so this
# battery drives the preprod path with a NEUTRAL stand-in too. It deliberately contains
# "olivares" but NOT "olivaresai": the §C.4.6 tripwire matches the production owner as a
# substring, and a stand-in that accidentally tripped it would make the accepted case red for
# the wrong reason — and, worse, would hide whether the tripwire fires when it should.
PRE_ID="preprod-org/olivares-preprod"
PRE=(
	RELEASE_MODE=preprod
	RELEASE_GITHUB_REPO="$PRE_ID"
	OCI_IMAGE_REPO="ghcr.io/$PRE_ID"
	SOURCE_REPOSITORY_URL="https://github.com/$PRE_ID"
	OLIVARES_PREPROD_EXPECTED_REPO="$PRE_ID"
	OLIVARES_PREPROD_EXPECTED_OCI="ghcr.io/$PRE_ID"
	OLIVARES_PREPROD_EXPECTED_SOURCE="https://github.com/$PRE_ID"
	# The REAL tag grammar: a preprod act rehearses v26.8.0 itself (order 36).
	RELEASE_TAG=v26.8.0
	COSIGN_MODE=keyless
	COSIGN_TLOG_UPLOAD=true
	PUBLISH_LATEST=true
	PUBLISH_DOCKERHUB=false
	PUBLISH_HOMEBREW=false
	PUBLISH_OTA_STABLE=true
	RUN_SLSA=true
	ACKNOWLEDGE_PUBLIC_SLSA_LOG=true
	OLIVARES_LICENSE_PUBKEY="$LIC"
	OLIVARES_OTA_PUBKEY="$OTA"
	GITHUB_REPOSITORY="$PRE_ID"
	GITHUB_REF=refs/tags/v26.8.0
	GITHUB_REF_NAME=v26.8.0
	GITHUB_REF_TYPE=tag
	GITHUB_SHA="$SHA"
)

n=0
# run_pf BASE overrides… — runs the preflight in a clean env; base env first, overrides
# last (later env assignments win). Sets rc, out (GITHUB_OUTPUT file), err (stderr file).
run_pf() {
	base="$1"
	shift
	n=$((n + 1))
	out="$WORK/out.$n"
	err="$WORK/err.$n"
	: >"$out"
	if [ "$base" = prod ]; then
		env -i PATH="$PATH" "${PROD[@]}" GITHUB_OUTPUT="$out" "$@" bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$err"
	elif [ "$base" = pre ]; then
		env -i PATH="$PATH" "${PRE[@]}" GITHUB_OUTPUT="$out" "$@" bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$err"
	else
		env -i PATH="$PATH" "${REH[@]}" GITHUB_OUTPUT="$out" "$@" bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$err"
	fi
	rc=$?
}

echo "release-preflight — accepted tuples and rejected cross-products (§C.4, §E.1.7)"

# --- ACCEPTED: production resolves EXACTLY the production coordinates -------------------
run_pf prod GITHUB_STEP_SUMMARY="$WORK/summary.prod"
[ "$rc" -eq 0 ]
check "production profile is accepted" "exit 0" $?
grep -qx 'release_github_repo=olivaresai/olivares' "$out" &&
	grep -qx 'oci_image_repo=ghcr.io/olivaresai/olivares' "$out" &&
	grep -qx 'source_repository_url=https://github.com/olivaresai/olivares' "$out" &&
	grep -qx 'release_github_owner=olivaresai' "$out" &&
	grep -qx 'release_github_name=olivares' "$out"
check "production outputs are exactly the reviewed tuple" "owner/name/oci/source" $?
grep -qx 'release_version=26.8.0' "$out" && grep -qx 'release_tag=v26.8.0' "$out"
check "the version output strips the v" "release_version=26.8.0" $?
grep -qx 'publish_latest=true' "$out" && grep -qx 'publish_dockerhub=auto' "$out" &&
	grep -qx 'cosign_mode=keyless' "$out" && grep -qx 'run_slsa=true' "$out"
check "production switches pass through validated" "latest/dockerhub/cosign/slsa" $?
[ -s "$WORK/summary.prod" ] && grep -q 'olivaresai/olivares' "$WORK/summary.prod"
check "the §C.4.9 summary is written when a sink exists" "summary present" $?

# --- ACCEPTED: rehearsal resolves ONLY the disposable coordinates -----------------------
run_pf reh GITHUB_STEP_SUMMARY="$WORK/summary.reh"
[ "$rc" -eq 0 ]
check "rehearsal profile is accepted" "exit 0" $?
grep -qx "release_github_repo=$REH_ID" "$out" &&
	grep -qx "oci_image_repo=ghcr.io/$REH_ID" "$out"
check "rehearsal outputs are exactly the injected disposable tuple" "repo/oci" $?
grep -qx 'publish_latest=false' "$out" && grep -qx 'publish_dockerhub=false' "$out" &&
	grep -qx 'publish_homebrew=false' "$out" && grep -qx 'publish_ota_stable=false' "$out"
check "every rehearsal publication switch resolves false" "4x false" $?
grep -qx 'cosign_mode=key' "$out" && grep -qx 'cosign_tlog_upload=false' "$out"
check "rehearsal signs key-mode with no tlog" "key/false" $?

# --- REJECTED cross-products ------------------------------------------------------------
run_pf prod RELEASE_MODE=
[ "$rc" -ne 0 ]
check "absent mode is an error, never a fallback" "RELEASE_MODE=" $?

run_pf prod RELEASE_MODE=staging
[ "$rc" -ne 0 ]
check "an arbitrary mode is rejected" "staging" $?

run_pf prod GITHUB_REPOSITORY="$REH_ID"
[ "$rc" -ne 0 ]
check "production profile outside olivaresai/olivares is rejected" "wrong repo" $?

run_pf reh GITHUB_REPOSITORY=olivaresai/olivares
[ "$rc" -ne 0 ]
check "rehearsal profile inside the PRODUCTION repo is rejected" "hard guard" $?

run_pf reh GITHUB_REPOSITORY=someone/fork
[ "$rc" -ne 0 ]
check "rehearsal profile inside a fork is rejected" "hard guard" $?

run_pf prod GITHUB_REF_TYPE=branch
[ "$rc" -ne 0 ]
check "a branch ref is rejected" "ref_type" $?

run_pf prod GITHUB_REF=refs/tags/v26.8.1
[ "$rc" -ne 0 ]
check "ref and declared tag must be the same tag ref" "mismatch" $?

run_pf reh RELEASE_TAG=v26.8.0 GITHUB_REF=refs/tags/v26.8.0 GITHUB_REF_NAME=v26.8.0
[ "$rc" -ne 0 ]
check "a production-looking tag is rejected in rehearsal" "tag grammar" $?

run_pf prod RELEASE_TAG=v0.0.0-rehearsal.1 GITHUB_REF=refs/tags/v0.0.0-rehearsal.1 GITHUB_REF_NAME=v0.0.0-rehearsal.1
[ "$rc" -ne 0 ]
check "a rehearsal tag is rejected in production" "tag grammar" $?

# The PINNED prerelease policy (P2-03): deny-closed until widens the signing
# identity. If this case ever needs to change, the identity regexps change WITH it.
run_pf prod RELEASE_TAG=v26.8.0-rc.1 GITHUB_REF=refs/tags/v26.8.0-rc.1 GITHUB_REF_NAME=v26.8.0-rc.1
[ "$rc" -ne 0 ] && grep -q 'production tag contract' "$err"
check "a prerelease tag is rejected in production (policy pinned)" "deny-closed" $?

run_pf reh OCI_IMAGE_REPO=ghcr.io/evil/olivares
[ "$rc" -ne 0 ]
check "an arbitrary OCI target is rejected (§C.4.1)" "not the tuple" $?

run_pf reh RELEASE_GITHUB_REPO=olivaresai/olivares
[ "$rc" -ne 0 ]
check "the production target inside rehearsal is rejected" "not the tuple" $?

run_pf prod PUBLISH_DOCKERHUB=yes
[ "$rc" -ne 0 ]
check "a publication switch outside true/false/auto is rejected" "enum" $?

run_pf reh PUBLISH_LATEST=true
[ "$rc" -ne 0 ]
check "rehearsal PUBLISH_LATEST=true is rejected" "no latest ever" $?

run_pf prod PUBLISH_LATEST=false
[ "$rc" -ne 0 ]
check "a DRIFTED production switch is rejected too" "latest=false" $?

run_pf reh COSIGN_MODE=keyless
[ "$rc" -ne 0 ]
check "rehearsal with keyless signing is rejected (§C.4.5)" "key required" $?

run_pf reh COSIGN_TLOG_UPLOAD=true
[ "$rc" -ne 0 ]
check "rehearsal with tlog upload is rejected (§C.4.5)" "no public record" $?

run_pf prod DOCKERHUB_USERNAME=user
[ "$rc" -ne 0 ]
check "a Docker Hub half-config is rejected (§C.4.7)" "username only" $?

run_pf prod DOCKERHUB_TOKEN=tok
[ "$rc" -ne 0 ]
check "the other half-config is rejected too" "token only" $?

run_pf reh DOCKERHUB_USERNAME=user DOCKERHUB_TOKEN=tok
[ "$rc" -ne 0 ]
check "ANY Docker Hub secret in rehearsal is rejected (§C.4.7)" "no secrets" $?

run_pf reh HOMEBREW_TAP_GITHUB_TOKEN=tok
[ "$rc" -ne 0 ]
check "a Homebrew tap token in rehearsal is rejected (§C.4.7)" "no secrets" $?

run_pf prod OLIVARES_LICENSE_PUBKEY=
[ "$rc" -ne 0 ]
check "production without the license anchor is rejected (§C.4.8)" "anchor" $?

run_pf prod OLIVARES_OTA_PUBKEY="$LIC"
[ "$rc" -ne 0 ]
check "equal anchors are rejected (§C.4.8)" "distinct pairs" $?

run_pf reh OLIVARES_LICENSE_PUBKEY=not-base64 OLIVARES_OTA_PUBKEY="$OTA"
[ "$rc" -ne 0 ]
check "malformed DECLARED rehearsal anchors are rejected" "well-formed or absent" $?

run_pf reh ACKNOWLEDGE_PUBLIC_SLSA_LOG=false
[ "$rc" -ne 0 ] && grep -qi 'non-deletable\|permanent' "$err"
check "rehearsal without the SLSA public-log acknowledgement is refused" "ack" $?

run_pf prod RUN_SLSA=false
[ "$rc" -ne 0 ]
check "RUN_SLSA=false is unreachable (elevated, unapproved)" "no partial mode" $?

# GITHUB_OUTPUT missing: outputs ARE the downstream contract, so no sink = no run.
n=$((n + 1))
env -i PATH="$PATH" "${PROD[@]}" bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$WORK/err.$n"
[ $? -ne 0 ]
check "a missing GITHUB_OUTPUT sink is rejected (§C.4.10)" "outputs mandatory" $?

# --- DENY-CLOSED: a rehearsal with no injected expectations cannot run ------------------
# The public script embeds NO rehearsal destination (export gate); the internal workflow
# must inject its expected tuple. Absent expectations = no rehearsal, ever.
run_pf reh OLIVARES_REHEARSAL_EXPECTED_REPO= OLIVARES_REHEARSAL_EXPECTED_OCI= OLIVARES_REHEARSAL_EXPECTED_SOURCE=
[ "$rc" -ne 0 ] && grep -q 'deny-closed' "$err"
check "a rehearsal WITHOUT injected expectations refuses (deny-closed)" "no embedded name" $?

# --- §C.4.6 production-surface tripwire, exercised DIRECTLY through the inputs ----------
# Expectations are inputs now, so the tripwire is testable without mutating a copy: an
# expected tuple naming a production surface passes §C.4.1 equality (declared == expected)
# and only the tripwire can reject it — if any of these go green, it is decorative.
oai="olivaresai/olivares-rehearsal"
run_pf reh OLIVARES_REHEARSAL_EXPECTED_REPO="$oai" RELEASE_GITHUB_REPO="$oai" GITHUB_REPOSITORY="$oai"
[ "$rc" -ne 0 ] && grep -q 'production surface' "$err"
check "an olivaresai-named rehearsal destination still fails (§C.4.6 live)" "tripwire" $?
run_pf reh OLIVARES_REHEARSAL_EXPECTED_OCI="docker.io/$REH_ID" OCI_IMAGE_REPO="docker.io/$REH_ID"
[ "$rc" -ne 0 ] && grep -q 'production surface' "$err"
check "a docker.io rehearsal destination fails the same tripwire" "tripwire" $?

# --- preprod: the third reviewed profile (orders 36/37) --------------------------------
# Order 36 forbids first-trying a release in public, so the whole act runs in preprod first.
# The profile is DENY-CLOSED like rehearsal — the public script embeds no preprod name — but
# it rehearses the REAL tag, the latest alias and the OTA channel, which rehearsal does not.
run_pf pre
check "preprod with an injected tuple and sandbox anchors is accepted" "exit 0" $([ "$rc" -eq 0 ] && echo 0 || echo 1)
[ "$rc" -ne 0 ] && sed -n '1,3p' "$err"

grep -q "release_github_repo=$PRE_ID" "$out" &&
	grep -q "oci_image_repo=ghcr.io/$PRE_ID" "$out" &&
	grep -q 'release_version=26.8.0' "$out"
check "preprod outputs carry the injected tuple and the real version" "tuple/26.8.0" $?

# DENY-CLOSED: without the injection there is no name to fall back to.
run_pf pre OLIVARES_PREPROD_EXPECTED_REPO= OLIVARES_PREPROD_EXPECTED_OCI= OLIVARES_PREPROD_EXPECTED_SOURCE=
[ "$rc" -ne 0 ] && grep -q 'embeds no preprod name' "$err"
check "a preprod act WITHOUT injected expectations refuses (deny-closed)" "no embedded name" $?

# §C.4.6 must fire for preprod exactly as it does for rehearsal.
run_pf pre OLIVARES_PREPROD_EXPECTED_REPO=olivaresai/olivares RELEASE_GITHUB_REPO=olivaresai/olivares GITHUB_REPOSITORY=olivaresai/olivares
[ "$rc" -ne 0 ] && grep -q 'production surface' "$err"
check "an olivaresai-named preprod destination fails the tripwire (§C.4.6)" "tripwire" $?

run_pf pre OLIVARES_PREPROD_EXPECTED_OCI="docker.io/$PRE_ID" OCI_IMAGE_REPO="docker.io/$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'production surface' "$err"
check "a docker.io preprod destination fails the same tripwire" "tripwire" $?

# §C.4.2: a run can only mutate itself. A tampered repository VARIABLE dies here, which is
# what makes variable-borne expectations safe in the first place.
run_pf pre GITHUB_REPOSITORY=someone-else/other
[ "$rc" -ne 0 ] && grep -q 'but the preprod profile targets' "$err"
check "a preprod run whose repository is not the declared destination refuses (§C.4.2)" "self only" $?

# ⛔ LA RUTA DE FIRMA ES LA MISMA QUE EN PRODUCCIÓN, Y ESO SE PRUEBA. Ensayar con otra ruta
# de firma no ensaya la ruta: en modo clave `release.yml` no tiene material de clave con que
# firmar, `promote-latest` verificaría keyless lo firmado con clave, y goreleaser no emitiría
# el `.pem` que la fase 2 exige por fichero — el acto entero moriría. Estos dos casos impiden
# que alguien "suavice" preprod a modo clave creyendo que evita una fuga que SLSA produce de
# todas formas.
run_pf pre COSIGN_MODE=key
[ "$rc" -ne 0 ] && grep -q "COSIGN_MODE must be 'keyless'" "$err"
check "preprod REFUSES key mode (it would break the very act it rehearses)" "keyless only" $?

run_pf pre COSIGN_TLOG_UPLOAD=false
[ "$rc" -ne 0 ] && grep -q 'COSIGN_TLOG_UPLOAD' "$err"
check "preprod REQUIRES the transparency log, like production" "tlog on" $?

# §C.4.7: preprod publishes a real-shaped tag and a real channel, so a publication secret
# sitting in it is the one ingredient that turns a rehearsal into a real publication.
run_pf pre DOCKERHUB_USERNAME=someone DOCKERHUB_TOKEN=secret
[ "$rc" -ne 0 ] && grep -q 'holds Docker Hub credentials' "$err"
check "a preprod repository holding Docker Hub credentials refuses (§C.4.7)" "no secrets" $?

run_pf pre HOMEBREW_TAP_GITHUB_TOKEN=secret
[ "$rc" -ne 0 ] && grep -q 'Homebrew tap token' "$err"
check "a preprod repository holding a Homebrew tap token refuses (§C.4.7)" "no secrets" $?

# The SLSA generators write PUBLIC records naming the running repository: a non-production
# act must say out loud that it accepts them.
run_pf pre ACKNOWLEDGE_PUBLIC_SLSA_LOG=
[ "$rc" -ne 0 ] && grep -q 'acknowledge_public_slsa_log' "$err"
check "preprod without the public-log acknowledgement refuses" "acknowledgement" $?

# §C.4.8: an act that publishes an update channel with no anchor pins nothing.
run_pf pre OLIVARES_OTA_PUBKEY=
[ "$rc" -ne 0 ] && grep -q 'preprod requires BOTH anchor variables' "$err"
check "preprod with no OTA anchor refuses (§C.4.8)" "anchors required" $?

# ⛔ EL CASO QUE PRUEBA LA TRAMPA DEL TERNARIO DE ACTIONS (release.yml, bloque `profile`).
# Cuando el repositorio declara el perfil preprod pero olvida una expectativa, la variable
# llega vacía y el ternario `cond && vars.X || 'olivaresai/olivares'` devuelve el LITERAL DE
# PRODUCCIÓN como destino declarado. Este caso reproduce exactamente esa forma —perfil
# preprod, expectativas vacías, destino de producción declarado— y exige que muera deny-closed
# ANTES de que §C.4.6 o cualquier otra cosa tenga que salvarlo. Sin esta prueba, la afirmación
# "el preflight lo caza" sería una promesa hecha en un fichero sobre el comportamiento de otro.
run_pf pre OLIVARES_PREPROD_EXPECTED_REPO= OLIVARES_PREPROD_EXPECTED_OCI= OLIVARES_PREPROD_EXPECTED_SOURCE= \
	RELEASE_GITHUB_REPO=olivaresai/olivares OCI_IMAGE_REPO=ghcr.io/olivaresai/olivares \
	SOURCE_REPOSITORY_URL=https://github.com/olivaresai/olivares
[ "$rc" -ne 0 ] && grep -q 'embeds no preprod name' "$err"
check "preprod with EMPTY expectations and a production destination dies deny-closed" "ternary trap" $?

# The tag grammar is the PRODUCTION one, not the rehearsal one — that is the point.
run_pf pre RELEASE_TAG=v0.0.0-rehearsal.1 GITHUB_REF=refs/tags/v0.0.0-rehearsal.1 GITHUB_REF_NAME=v0.0.0-rehearsal.1
[ "$rc" -ne 0 ] && grep -q 'tag contract' "$err"
check "a rehearsal-shaped tag is refused in preprod (it rehearses the REAL tag)" "real grammar" $?

# --- build-phase invariant: no `latest` anywhere in the shared docker plan --------------
hits="$(awk '
	/^[a-z_]+:/ { section = $1 }
	(section == "dockers:" || section == "docker_manifests:") {
		line = $0
		sub(/#.*/, "", line)
		if (line ~ /latest/) print FILENAME ":" FNR ": " line
	}
' "$ROOT/.goreleaser.yaml")"
[ -z "$hits" ]
check "no latest tag exists in the shared build phase (aliases are promotion-only)" "goreleaser plan" $?
[ -n "$hits" ] && printf '%s\n' "$hits"

echo ""
echo "release-preflight battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
