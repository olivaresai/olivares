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
# Updated 2026-09-24: the chart's publication is UNVERIFIED, not absent. This repository never ran
# its chart publisher (no chart-v* tag, no publisher run, no chart release asset), and the
# registry refuses the reads that could settle its side. So the Helm surfaces must say
# "unverified", may state an absence only within that demonstrated scope ("from this
# repository"), and the READMEs name the witness result `publication-unverified`.
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
HELM_README="$ROOT/deploy/helm/README.md"
HELM_CHART="$ROOT/deploy/helm/olivares/Chart.yaml"

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
has -q 'Publication is \*\*unverified\*\*' "$GITOPS" \
  || fail "gitops/README.md must state that the chart's publication is **unverified** until a chart-v* tag publishes it (C6)"
if has -Eq 'registry path is \*\*empty\*\*|not published( to [^.]*)? yet' "$GITOPS"; then
  fail "gitops/README.md states an absence nobody observed; the chart's publication is unverified (C6)"
fi
# Body intact: the engine entry-points must survive the rewrite.
has -q 'kustomize build --enable-helm' "$GITOPS" \
  || fail "gitops/README.md lost the Kustomize entry point — the C6 rewrite must keep Argo/Flux/Kustomize guidance (C6)"

# ---- C6b: the READMEs name the witness's Helm result (2026-09-24) --------------
# In README.md and its six translations the chart's OCI registry state is spoken of on one line.
# That line names the witness result, `publication-unverified`: in any language "not published
# yet" is an absence nobody observed. Every line that mentions OCI must carry the token.
READMES_SEEN=0
HITS_OCI="$(mktemp)"
for readme in "$ROOT/README.md" "$ROOT"/README.??.md; do
  [ -f "$readme" ] || fail "missing $readme"
  READMES_SEEN=$((READMES_SEEN + 1))
  has -q '`publication-unverified`' "$readme" \
    || fail "${readme#$ROOT/} must name the witness result \`publication-unverified\` on its Kubernetes line (C6b)"
  : > "$HITS_OCI"
  has -n 'OCI' "$readme" > "$HITS_OCI" || true
  if has -v 'publication-unverified' "$HITS_OCI" > /dev/null; then
    fail "${readme#$ROOT/}: a line about the chart's OCI registry state must name the witness result \`publication-unverified\` (C6b):
$(grep -v 'publication-unverified' "$HITS_OCI")"
  fi
done
rm -f "$HITS_OCI"
[ "$READMES_SEEN" -eq 7 ] || fail "expected README.md and six translations, found $READMES_SEEN (C6b)"

# ---- DIST-24-07: source chart is not a published OCI channel ----------------
for path in "$HELM_README" "$HELM_CHART"; do
  [ -f "$path" ] || fail "missing $path"
done
has -q 'its publication to the public OCI registry is \*\*unverified\*\*' "$HELM_README" \
  || fail "deploy/helm/README.md must state its publication to the public OCI registry is **unverified** until REL-87 publishes it"
# An absence only within its demonstrated scope: this repository published nothing. Through a
# file, not a pipe: a pipe would hide a failing first grep behind the second one's answer.
HITS_ABSENCE="$(mktemp)"
has 'not published to the public OCI registry' "$HELM_README" > "$HITS_ABSENCE" || true
if has -qv 'from this repository' "$HITS_ABSENCE"; then
  rm -f "$HITS_ABSENCE"
  fail "deploy/helm/README.md states an absence beyond its demonstrated scope; only this repository's publication is observed, and the registry side is unverified"
fi
rm -f "$HITS_ABSENCE"
has -q 'REL-87' "$HELM_README" \
  || fail "deploy/helm/README.md lost the named publication act REL-87"
if has -Eq 'chart (is|has been) published( and consumed)? as an OCI artifact' "$HELM_CHART"; then
  fail "Chart.yaml claims present-tense OCI publication before REL-87"
fi
has -q 'REL-87 has not published it yet' "$HELM_CHART" \
  || fail "Chart.yaml must distinguish the planned OCI producer from live publication"
# Its full-line comments too (2026-09-24): they are evidence about the chart and must not call it
# published. Its YAML values (coordinates, annotations) are product data and are not read here.
CHART_COMMENTS="$(mktemp)"
has -E '^[[:space:]]*#' "$HELM_CHART" > "$CHART_COMMENTS" || true
if has -Eiq 'chart( itself)? (is|has been)( still)? published' "$CHART_COMMENTS"; then
  rm -f "$CHART_COMMENTS"
  fail "Chart.yaml comments call the chart published; its OCI publication is unverified (DIST-24-07)"
fi
rm -f "$CHART_COMMENTS"

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

# ---- C6c: no remote chart COMMAND on a published doc (2026-09-24) -----------
# While the chart's publication is unverified, a remote `helm install|upgrade|pull|show|template
# … oci://ghcr.io/olivaresai/charts` command is a claim that the chart can be pulled, wherever a
# reader copies it from: the root READMEs, INSTALL.md, the chart README, the gitops README and
# every doc under docs/ that the export publishes. The command shape is matched, with a command
# wrapped over `\` continuations joined first; the coordinate alone is not a claim (INSTALL.md
# names it to say it is unverified). Limits, stated: a CRLF continuation is not joined; a `#` or
# `|` between the verb and the coordinate ends the match; a command assembled from variables is
# not seen; under docs/ only md, mdx and txt are read. A symlinked entry is a document too: the
# curation decides on its own path first, and it is read only when it resolves to a regular file
# inside the tree that the export also publishes. C6c does not read the witness, so a published
# chart would need this rule changed with it.
#
# export-closure: absent-by-design scripts/export-public.sh — DATA, read and never run: its
# *_BLOCK and DOCS_KEEP lists say which docs the export drops, by the rule the export applies.
# In a public export it is absent and every doc present counts as published (over-reports, never
# under-reports). A curation this reader cannot follow is UNVERIFIED, never "all published".
REMOTE_CMD_RX='helm[ 	]+(install|upgrade|pull|show|template)[^|#]*oci://ghcr[.]io/olivaresai/charts'
EXPORTER="$ROOT/scripts/export-public.sh"
C6C="$(mktemp -d)"
trap 'rm -rf "$LIVE_FILES" "$SLSA_HITS" "$FIPS_RAW_HITS" "$FIPS_HITS" "$C6C"' EXIT HUP INT TERM
c6c_unverified() { echo "docs-honesty: $1; the remote-command claims are UNVERIFIED." >&2; exit 2; }
: > "$C6C/curation"
if [ -e "$EXPORTER" ] || [ -L "$EXPORTER" ]; then
  [ -r "$EXPORTER" ] || c6c_unverified "cannot read $EXPORTER (which docs the export publishes)"
  if has -Eq '^[[:space:]]*[A-Z_]+(_BLOCK|_KEEP)\+=' "$EXPORTER"; then
    c6c_unverified "$EXPORTER: a curation list is appended to"
  fi
  # The grammar is check-emitted-urls.sh's publication_rule(), no wider: each list is defined
  # once, at the start of a line, as `NAME=()` or as `NAME=(` whose body closes on a line that
  # starts with `)`. Inline, indented, repeated, appended or open lists are UNVERIFIED.
  awk -v names='TOP_BLOCK DOCS_BLOCK SCRIPTS_BLOCK GITHUB_BLOCK COMMERCIAL_BLOCK MISC_BLOCK DOCS_KEEP' '
    BEGIN { n = split(names, l, " "); for (i = 1; i <= n; i++) want[l[i]] = 1 }
    {
      line = $0; sub(/^[ \t]+/, "", line)
      if (match(line, /^[A-Z_]+[+]?=/)) {
        name = substr(line, 1, RLENGTH); plus = (name ~ /[+]=$/); sub(/[+]?=$/, "", name)
        if (name in want) {
          if (plus || k != "") exit 3
          seen[name]++
          if ($0 == name "=(") { k = (name == "DOCS_KEEP") ? "K" : "B"; next }
          if (index($0, name "=()") == 1) next
          exit 3
        }
      }
    }
    k != "" && /^\)/ { k = ""; next }
    k != "" { sub(/#.*/, ""); for (i = 1; i <= NF; i++) print k, $i }
    END { if (k != "") exit 3; for (x in want) if (seen[x] != 1) exit 3 }
  ' "$EXPORTER" > "$C6C/curation" \
    || c6c_unverified "$EXPORTER: a curation list is written in a form this reader does not follow (inline, indented, defined twice, appended to or left open)"
  if has -Ev '^[BK] [A-Za-z0-9._/@+*-]+$' "$C6C/curation" > /dev/null; then
    c6c_unverified "$EXPORTER: a curation entry this reader cannot match as the shell does"
  fi
  has -q '^B docs' "$C6C/curation" || c6c_unverified "$EXPORTER: no curation entry reaches docs/"
fi
c6c_published() { # c6c_published <path from ROOT>: 0 when the export publishes it
  if has -qxF "K $1" "$C6C/curation"; then return 0; fi
  while read -r kind entry; do
    [ "$kind" = B ] || continue
    # The entry IS a glob, matched as the export matches it.
    case "$1" in $entry | $entry/*) return 1 ;; esac
  done < "$C6C/curation"
  return 0
}
C6C_ROOT="$(readlink -f -- "$ROOT")" || c6c_unverified "cannot resolve $ROOT"
{
  for f in "$ROOT/README.md" "$ROOT"/README.??.md "$ROOT/INSTALL.md" \
      "$ROOT/deploy/helm/README.md" "$ROOT/deploy/gitops/README.md"; do
    if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\n' "$f"; fi
  done
  find "$ROOT/docs" \( -type f \( -name '*.md' -o -name '*.mdx' -o -name '*.txt' \) -o -type l \) -print
} > "$C6C/docs" || c6c_unverified "cannot enumerate the READMEs, INSTALL.md, the chart and gitops READMEs and docs/"
: > "$C6C/hits"
while IFS= read -r f; do
  rel="${f#"$ROOT"/}"
  # The curation decides first: a curated-out path is never read, whatever it points to.
  c6c_published "$rel" || continue
  if [ -L "$f" ]; then
    # A linked directory is not walked (no loop, no escape), so what it holds is unread.
    [ ! -d "$f" ] || c6c_unverified "$rel is a linked directory; the census does not follow it"
    case "$f" in *.md | *.mdx | *.txt) ;; *) continue ;; esac
    [ -e "$f" ] || c6c_unverified "$rel is a dangling link; a document the census cannot read"
    target="$(readlink -f -- "$f")" || c6c_unverified "cannot resolve the link $rel"
    case "$target" in "$C6C_ROOT"/*) ;; *) c6c_unverified "$rel resolves outside the tree; $target is not read" ;; esac
    [ -f "$target" ] || c6c_unverified "$rel does not resolve to a regular document"
    c6c_published "${target#"$C6C_ROOT"/}" \
      || c6c_unverified "$rel resolves to curated-out content; it is not read, and in the export the link dangles"
  fi
  awk -v rel="$rel" -v rx="$REMOTE_CMD_RX" '
    { if (buf == "") start = FNR }
    /\\$/ { buf = buf substr($0, 1, length($0) - 1) " "; next }
    { buf = buf $0; if (buf ~ rx) print rel ":" start ": " buf; buf = "" }
    END { if (buf != "" && buf ~ rx) print rel ":" start ": " buf }
  ' "$f" >> "$C6C/hits" || c6c_unverified "could not read $rel"
done < "$C6C/docs"
[ ! -s "$C6C/hits" ] || fail "a published doc offers a remote chart command while the chart's publication is unverified (C6c):
$(cat "$C6C/hits")"

echo "docs-honesty: OK across $scanned live files (C3 modules catalog + C6 gitops README + supply-chain wording)"
