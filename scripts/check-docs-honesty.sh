#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Regression guard for C3/C6 documentation-honesty fixes. Fails CI if a
# public doc reintroduces an over-claim: (C3) the modules catalog claiming the
# actuation half is plain "v1" where the shipped binary 503s / deny-closes, or
# (C6) the gitops README asserting the Helm chart "is published" while the
# release is still DRAFT (no chart-v* tag cut). Pure content lint — no network.
#
# Updated 2026-06-08: the Actuate taxonomy is now three-way — `v1` (live in
# the default binary), `on-demand` (backend built and wired, deny-closed/degraded
# until an operator provisions it), and `seam` (no backend at all). The earlier
# guard asserted XVI voice = "v1 | v1" ("voice realtime dispatch is live"), but a
# stock `serve --seed-demo` boot WARNs "voice: no dispatcher wired … an approved
# open is declared, not actuated" — voice dispatch is deny-closed without
# OLIVARES_VOICE_DISPATCH_CONFIG, exactly like IV orchestration (both deny-closed).
# So voice is `on-demand`, not live, and the positive control moved to XI FinOps
# (budget enforcement is always wired in wire.go and denies at the cap with no
# operator config). The guard now enforces the verified truth, not the stale claim.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
OVERVIEW="$ROOT/docs-site/src/content/docs/reference/modules/overview.md"
GITOPS="$ROOT/deploy/gitops/README.md"

fail() { echo "docs-honesty: FAIL — $1" >&2; exit 1; }

# grep exits 0 (match), 1 (no match) and >=2 on ERROR. Every check below treated 1 and
# >=2 as the same answer, and the two shapes fail differently:
#
#   `grep -q X "$F" || fail "..."`        -> a TRUE diagnostic about the WRONG thing. With
#      a grep that exits 2 this gate said "overview.md lost the catalog layout", sending
#      a reader to fix a file that is perfectly fine.
#   `grep -Eq X "$F" && fail "..." || true` -> worse: the negative check is SKIPPED in
#      silence and the gate passes, because `&&` does not fire on 2 and `|| true` eats it.
#
# `has` keeps 0/1 as answers and turns anything else into a refusal that says the claims
# are unverified. Measured 2026-08-01 with a grep stub exiting 2.
has() { # has <grep-args...>  -> 0 match, 1 no match, exit 2 on tool failure
  # The status is captured on the SAME line as grep. Written as
  # `if grep "$@"; then return 0; fi` followed by `st=$?`, the `$?` read is the status
  # of the completed `if` (0 when the then-branch did not run), not of grep — which
  # reported every failure as "grep failed (exit 0)" and broke the gate outright.
  st=0
  grep "$@" || st=$?
  [ "$st" -eq 0 ] && return 0
  [ "$st" -eq 1 ] && return 1
  echo "docs-honesty: grep failed (exit $st) on: $*" >&2
  echo "  the documentation claims are UNVERIFIED — a tool failure, not a doc defect." >&2
  exit 2
}

# ---- C3: modules catalog keeps the honest Actuate column ---------------------
# Re-baselined 2026-07-03: #136 replaced the two-half
# `| Govern/Observe | Actuate |` shorthand with a grouped
# `| Module | Actuate | Purpose |` catalog. The guard now asserts the SAME
# honesty facts against that layout (the earlier patterns silently rotted —
# this script was unwired until added the lint:docs-honesty target).
[ -f "$OVERVIEW" ] || fail "missing $OVERVIEW"
has -q '| Module | Actuate | Purpose |' "$OVERVIEW" \
  || fail "overview.md lost the '| Module | Actuate | Purpose |' catalog layout — the honest Actuate column must not disappear (C3)"
# VII deploy and X models must NOT claim live actuation: apply/retire and model
# execution return 503 until an operator provisions them.
has -Eq '^\| \[Deployment & integration\][^|]*\| on-demand \(503\) \|' "$OVERVIEW" \
  || fail "overview.md row VII (deploy) must mark Actuate as 'on-demand (503)' — the executor is built but deploy apply/retire returns 503 until it is provisioned (C3)"
has -Eq '^\| \[Model & provider management\][^|]*\| on-demand \(503\) \|' "$OVERVIEW" \
  || fail "overview.md row X (models) must mark Actuate as 'on-demand (503)' — model execution 503s until an inference credential is provisioned (C3)"
# IV orchestration fire and XVI voice dispatch are deny-closed until a
# dispatcher is provisioned.
has -Eq '^\| \[Orchestration & A2A\][^|]*\| on-demand \|' "$OVERVIEW" \
  || fail "overview.md row IV (orchestration) must mark Actuate as 'on-demand' (fire is deny-closed until a dispatcher is provisioned) (C3)"
has -Eq '^\| \[Voice & realtime agents\][^|]*\| on-demand \|' "$OVERVIEW" \
  || fail "overview.md row XVI (voice) must mark Actuate as 'on-demand' — voice dispatch is deny-closed until a dispatcher is provisioned, exactly like IV orchestration (C3)"
if has -Eq '^\| \[(Deployment & integration|Voice & realtime agents|Orchestration & A2A)\][^|]*\| live \|' "$OVERVIEW"; then
  fail "overview.md marks deploy/voice/orchestration Actuate as 'live' — they are on-demand and must not claim live actuation (C3)"
fi
# Positive control: a genuinely-live actuation module must still read 'live', so
# the fix cannot over-correct into hiding real actuation. FinOps budget
# enforcement is always wired (wire.go) and denies at the cap with no operator
# config.
has -Eq '^\| \[Cost & AI FinOps\][^|]*\| live \|' "$OVERVIEW" \
  || fail "overview.md row XI (finops) must keep Actuate 'live' — budget enforcement denies at the cap with no provisioning (C3)"

# ---- C6: gitops README does not claim the chart is already published --------
[ -f "$GITOPS" ] || fail "missing $GITOPS"
if has -q 'is published as an \*\*OCI' "$GITOPS"; then
  fail "gitops/README.md claims the chart 'is published' — it is DRAFT (no chart-v* tag); use conditional/future wording (C6)"
fi
has -q 'will be published' "$GITOPS" \
  || fail "gitops/README.md must state the chart 'will be published' once release-chart.yml runs (C6)"
has -q 'registry path is \*\*empty\*\*' "$GITOPS" \
  || fail "gitops/README.md must note the registry path is empty until the chart-v* tag is cut (C6)"
# Body intact: the engine entry-points must survive the rewrite.
has -q 'kustomize build --enable-helm' "$GITOPS" \
  || fail "gitops/README.md lost the Kustomize entry point — the C6 rewrite must keep Argo/Flux/Kustomize guidance (C6)"

# ---- supply-chain wording honesty ------------------------------------
LIVE_FILES="$(mktemp)"
SLSA_HITS="$(mktemp)"
FIPS_RAW_HITS="$(mktemp)"
FIPS_HITS="$(mktemp)"
trap 'rm -f "$LIVE_FILES" "$SLSA_HITS" "$FIPS_RAW_HITS" "$FIPS_HITS"' EXIT HUP INT TERM

collect_live_files() {
  find "$ROOT" -maxdepth 1 -type f \( -name '*.md' -o -name '*.yaml' -o -name '*.yml' \) \
    ! -name 'ESTADO-PROYECTO.md' ! -name 'CHANGELOG.md'
  find "$ROOT/docs" -maxdepth 1 -type f -name '*.md' ! -name '[0-9][0-9]-*.md'

  for dir in \
    "$ROOT/docs/trust" \
    "$ROOT/docs/adr" \
    "$ROOT/docs/ai-context" \
    "$ROOT/docs/launch" \
    "$ROOT/docs-site/src/content" \
    "$ROOT/web/src" \
    "$ROOT/deploy" \
    "$ROOT/packaging" \
    "$ROOT/scripts" \
    "$ROOT/.github"; do
    [ -d "$dir" ] || continue
    find "$dir" -type f ! -path '*/node_modules/*' ! -path '*/dist/*'
  done
}

format_hits() {
  while IFS= read -r line; do
    case "$line" in
      "$ROOT"/*) printf '%s\n' "${line#$ROOT/}" ;;
      *) printf '%s\n' "$line" ;;
    esac
  done < "$1"
}

collect_live_files | sort -u > "$LIVE_FILES"

SLSA_PATTERN='SLSA[- ]?L(evel[- ]?)?''3|SLSA[- ]?Build[- ]?Level[- ]?''3|SLSA compl''iant'
FIPS_PATTERN='FIPS[- ]?(140-3[- ]?)?(valid''ated|cert''ified)'
FIPS_CONTEXT_PATTERN='module|#5247|v1\.0\.0|CMVP|not.{0,20}valid''ated'

# grep exits 0 with matches, 1 with none, and >=2 on ERROR — an unreadable file, a
# vanished path, a build whose -E cannot compile the pattern. Until 2026-08-01 both
# loops below ended in `|| true`, which made those three outcomes one outcome: a file
# the scanner could not read was counted as a file containing no unnormalized claim,
# and the gate printed "docs-honesty: OK". Measured by planting an unnormalized
# build-level claim in a live doc: readable, the gate failed and named it; with the file
# at mode 000, the gate passed. A claim gate that cannot read a file must not vouch for
# it. (The planted string is not reproduced here: scripts/ is itself scanned, which is
# why every pattern above is split with quotes.)
# -H forces the filename onto every hit. Invoked one file at a time, GNU grep omits it,
# so the hits files held bare "<lineno>:<text>" and format_hits() — whose whole job is to
# strip the "$ROOT/" prefix — had nothing to strip. A gate that reports an unnormalized
# claim without naming the document it is in sends the reader hunting.
scan_file() { # scan_file <pattern> <file> <hits-file>
  grep -HEIn "$1" "$2" >> "$3" || {
    st=$?
    [ "$st" -eq 1 ] || {
      echo "docs-honesty: could not scan $2 (grep exit $st); the wording is UNVERIFIED." >&2
      exit 2
    }
  }
}

scanned=0
while IFS= read -r file; do
  scan_file "$SLSA_PATTERN" "$file" "$SLSA_HITS"
  scanned=$((scanned + 1))
done < "$LIVE_FILES"

[ "$scanned" -gt 0 ] || {
  echo "docs-honesty: the live-file collection produced nothing to scan; UNVERIFIED." >&2
  exit 2
}

[ ! -s "$SLSA_HITS" ] || fail "supply-chain wording has unnormalized SLSA claims:
$(format_hits "$SLSA_HITS")"

while IFS= read -r file; do
  scan_file "$FIPS_PATTERN" "$file" "$FIPS_RAW_HITS"
done < "$LIVE_FILES"

if [ -s "$FIPS_RAW_HITS" ]; then
  grep -Eiv "$FIPS_CONTEXT_PATTERN" "$FIPS_RAW_HITS" > "$FIPS_HITS" || true
fi

[ ! -s "$FIPS_HITS" ] || fail "supply-chain wording has unanchored FIPS validation claims:
$(format_hits "$FIPS_HITS")"

# ---- C15-P10: ninguna copy publicada inventa un aprobador que el motor no exige ----
# Medido el 2026-08-19: `docs-site/.../kill-switch-drill.md:17` prometia
# «(two distinct humans, then a post-review by a third)», y el motor NO exige un tercero:
# `modules/governance/risktier.go:52` fija `criticalApprovalFloor = 2`, y el post-review es un
# FINDING recordatorio despues del commit (`modules/governance/sweep.go:164`), no una aprobacion.
# El evidence pack existe; el tercer aprobador no. La descripcion del propio fichero ya decia lo
# correcto —«the incident leaves an evidence pack»—, asi que la copy se contradecia a si misma.
#
# ⭐ EL SUELO SE LEE DEL CODIGO, no se escribe aqui. Si alguien sube el floor a 3, esta regla deja
# de aplicar SOLA en vez de convertirse en un guardian que miente en la otra direccion — que es
# exactamente como envejecen las cifras en prosa que este repositorio lleva el dia corrigiendo.
FLOOR_SRC="modules/governance/risktier.go"
floor="$(grep -oE 'criticalApprovalFloor = [0-9]+' "$FLOOR_SRC" 2>/dev/null | grep -oE '[0-9]+$' | head -1)"
case "$floor" in
  ''|*[!0-9]*)
    echo "docs-honesty: no he podido leer criticalApprovalFloor en $FLOOR_SRC" >&2
    echo "  sin el suelo del motor no puedo juzgar la copy: es NO HE PODIDO MIRAR, no un verde." >&2
    exit 2
    ;;
esac
if [ "$floor" -lt 3 ]; then
  # ⛔ Y EN LOS SEIS IDIOMAS, porque el patron ingles NO los ve y eso quedo MEDIDO. El 2026-08-19
  # este guion respondio «OK across 3072 live files» con la afirmacion falsa reintroducida en
  # japones — mutacion hecha antes de escribir esta linea, no supuesta. `lint:translation-drift`
  # tampoco la caza: mide obsolescencia, no afirmaciones. Un gate que dice «3072 ficheros» y sólo
  # sabe leer uno de siete idiomas etiqueta como cubierto lo que no mira.
  #
  # Las seis redacciones salen del PR que las retiro (#1047), no de una traduccion inventada aqui.
  # ⚠ Y ESTO NO ES COMPLETO POR CONSTRUCCION: una traduccion futura puede decirlo de otra forma.
  # Es un suelo comprobado, no un techo; el techo seria comparar SIGNIFICADO, que ningun grep hace.
  if has -rniE 'post.?review by a third|third approver|approval (from|by) a third|durch einen dritten|por un tercero|par un troisième|三人目|由第三人|третьим лицом|третьего' \
      docs-site/src/content/ docs/ web/src/ README.md; then
    fail "hay copy publicada que promete un TERCER aprobador y el motor exige $floor
  ($FLOOR_SRC: criticalApprovalFloor). El post-review es un recordatorio despues del commit,
  no una aprobacion: prometerlo como control es prometer algo que no existe."
  fi
fi

echo "docs-honesty: OK across $scanned live files (C3 modules catalog + C6 gitops README + supply-chain wording)"
