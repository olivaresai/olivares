#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# apply-branch-protection.sh — repo settings-as-code.
#
# Companion to scripts/apply-github-presence.sh (which owns description/topics/labels).
# This owns branch protection on `main`, the merge-safety repo settings, and (public
# profile) the release-tag rulesets. Idempotent; safe to re-run. Run it at go-live AFTER
# the first CI run, because required status checks must have reported at least once for
# GitHub to know the contexts.
#
# TWO PROFILES (the previous version hardcoded the public model, so it was unusable on the
# single-writer hub, and its status-check list was incomplete):
#
#   public  (default; for the published olivaresai/olivares)
#     - required checks = the PR-CI job ids (pr-lint, pr-build, pr-test, pr-web). The check
#       context is the job id. ⛔ CORRECTED 2026-08-27: this said "the mainline-ci job ids …
#       so every job always reports", and the second half was FALSE on the surface it governs.
#       mainline-ci has no `pull_request` trigger, so on a pull request its jobs report NEVER —
#       the required set could not be satisfied by any contribution. The regime that does run
#       on a pull request is .github/workflows/pr-ci.yml, and these are its jobs.
#     - 1 approving review, code-owner reviews, strict (up to date), conversations resolved.
#     - release-tag rulesets: v* and chart-v* immutable (no delete / no re-point).
#
#   hub  (for a private single-writer work repo; the repo is resolved from the current
#         git context or passed with --repo — this script never hardcodes a private name)
#     - Actions are DISABLED on the hub and it has a single writer, so requiring status
#       checks or approvals would BLOCK every merge. Hub protection therefore requires NO
#       status checks and 0 approvals (no code-owners); the green gate is the LOCAL
#       pre-push hook. force-push/deletion on main stay blocked.
#     - no tag rulesets (release tags are cut on the public repo, not the hub).
#
# The Terraform provider's own `vX.Y.Z` tag ruleset lives in its SEPARATE repo
# (terraform-provider-olivares, with its own release tooling), NOT here.
#
# Usage:
#   scripts/apply-branch-protection.sh [--profile public|hub] [--repo owner/repo]
#       [--branch main] [--contexts a,b,c] [--approvals N] [--code-owners on|off]
#       [--tags on|off] [--dry-run]
# Defaults: --profile public, --repo olivaresai/olivares, --branch main.
# Needs: gh (authenticated with admin on the target repo). jq for JSON assembly.
set -euo pipefail

PROFILE="public"
REPO=""
BRANCH="main"
CONTEXTS=""       # comma-separated; empty => profile default
APPROVALS=""      # integer; empty => profile default
CODE_OWNERS=""    # on|off; empty => profile default
TAGS=""           # on|off; empty => profile default
DRY_RUN=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --profile)     PROFILE="${2:?}"; shift 2 ;;
    --repo)        REPO="${2:?}"; shift 2 ;;
    --branch)      BRANCH="${2:?}"; shift 2 ;;
    --contexts)    CONTEXTS="${2:?}"; shift 2 ;;
    --approvals)   APPROVALS="${2:?}"; shift 2 ;;
    --code-owners) CODE_OWNERS="${2:?}"; shift 2 ;;
    --tags)        TAGS="${2:?}"; shift 2 ;;
    --dry-run)     DRY_RUN=true; shift ;;
    -h|--help)     sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# --- profile defaults --------------------------------------------------------
# ⛔ THE PUBLIC CONTEXTS WERE UNSATISFIABLE UNTIL 2026-08-27, AND THIS LINE IS THE FIX.
#
# They were the ten `mainline-ci` job ids. `mainline-ci` runs on `workflow_dispatch` and on
# `push` to main — it has no `pull_request` trigger, and `workflow_dispatch` cannot run
# against a fork ref at all. So required contexts that CANNOT REPORT on a pull request were
# being demanded of every pull request: the first external contribution would have been born
# with no checks and permanently unmergeable, the merge button saying "Expected — waiting for
# status to be reported" about jobs nothing would ever start. That is not a strict gate; it is
# a gate whose only answer is an admin override, and an override is not a gate.
#
# They are now the job ids of `.github/workflows/pr-ci.yml`, which DOES run on `pull_request`
# in the public repository. What that costs in coverage is stated rather than implied: the
# race detector, fuzzing, FIPS, the SAST and secret sweeps, the examples matrix and the Helm
# render stay in `mainline-ci`, which a maintainer dispatches on the merge candidate
# (docs/RELEASE-GO-LIVE-RUNBOOK.md §4). The alternative — keep the impossible list — buys no
# coverage at all, because nothing in it ever runs.
#
# THE TWO LISTS MUST NOT DRIFT: the PR-CI regime gate derives pr-ci.yml's job ids and
# fails if they are not exactly this set. A required context naming a job that does not exist
# blocks every merge; a job that exists and is not required lets a red one through.
#
# WHAT SURVIVES FROM THE OLD COMMENT, because it is the reason this list is checked at all:
# a job skipped by a job-level `if:` does NOT block a required check, so a set that can all
# skip at once evaporates into green. Every pr-ci job carries the same repository guard, and
# that guard is what check-pr-ci-regime.sh pins — a rename would otherwise disable the whole
# regime in silence. Measured on the hub 2026-08-01 and fixed there the same day.
DEFAULT_PUBLIC_CONTEXTS="pr-lint,pr-build,pr-test,pr-web"
case "$PROFILE" in
  public)
    REPO="${REPO:-olivaresai/olivares}"
    CONTEXTS="${CONTEXTS:-$DEFAULT_PUBLIC_CONTEXTS}"
    APPROVALS="${APPROVALS:-1}"
    CODE_OWNERS="${CODE_OWNERS:-on}"
    TAGS="${TAGS:-on}"
    ;;
  hub)
    # Resolve the private hub repo from the current git context (never hardcode it —
    # this script ships publicly). Pass --repo to override.
    REPO="${REPO:-$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)}"
    [ -n "$REPO" ] || { echo "hub profile: run from the repo dir or pass --repo owner/repo" >&2; exit 2; }
    # CORRECTED 2026-08-02. This said "none: Actions are off on the hub" and set no
    # contexts at all. Both halves are false now, and the combination was armed:
    # running this script would have WIPED the protection the hub actually relies on.
    # Actions have been ON here since the self-hosted runners landed, and the live
    # protection requires these four with strict: true. They are recorded here so
    # settings-as-code reproduces reality instead of overwriting it.
    CONTEXTS="${CONTEXTS:-classify,control-plane,race-hot,web}"
    # 0 IS the correct value here, and it is not laziness. Every lane in this repo
    # pushes as the same identity, and GitHub refuses to let an author approve their
    # own pull request ("Can not approve your own pull request", measured 2026-08-01
    # on #458) — so any value above 0 would block EVERY merge on the hub. The >=1 that
    # belongs in the public profile above is exactly right there and impossible here.
    APPROVALS="${APPROVALS:-0}"
    CODE_OWNERS="${CODE_OWNERS:-off}"
    TAGS="${TAGS:-off}"
    ;;
  *) echo "unknown profile: $PROFILE (want public|hub)" >&2; exit 2 ;;
esac

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

# --- assemble the branch-protection payload with jq (safe quoting) -----------
# required_status_checks: null when no contexts (hub); else strict + the contexts.
if [ -z "$CONTEXTS" ]; then
  RSC='null'
else
  RSC="$(jq -cn --arg csv "$CONTEXTS" '
    { strict: true,
      checks: ($csv | split(",") | map({context: (. | gsub("^\\s+|\\s+$";""))})) }')"
fi

CODEOWNER_BOOL=false
[ "$CODE_OWNERS" = "on" ] && CODEOWNER_BOOL=true

PROTECTION="$(jq -cn \
  --argjson rsc "$RSC" \
  --argjson approvals "$APPROVALS" \
  --argjson codeowners "$CODEOWNER_BOOL" '
{
  required_status_checks: $rsc,
  enforce_admins: false,
  required_pull_request_reviews: {
    dismiss_stale_reviews: true,
    require_code_owner_reviews: $codeowners,
    required_approving_review_count: $approvals
  },
  required_conversation_resolution: true,
  required_linear_history: false,
  allow_force_pushes: false,
  allow_deletions: false,
  restrictions: null
}')"

echo "==> profile=$PROFILE repo=$REPO branch=$BRANCH approvals=$APPROVALS code-owners=$CODE_OWNERS tags=$TAGS"
echo "==> required check contexts: ${CONTEXTS:-<none>}"

if $DRY_RUN; then
  echo "--- DRY RUN: branch protection payload ---"
  echo "$PROTECTION" | jq .
else
  echo "==> ${REPO}@${BRANCH}: branch protection"
  echo "$PROTECTION" | gh api -X PUT "repos/${REPO}/branches/${BRANCH}/protection" --input - >/dev/null
fi

# --- merge-safety repo settings ---------------------------------------------
if $DRY_RUN; then
  echo "--- DRY RUN: would PATCH repos/${REPO} merge settings (squash+merge on, rebase off, delete-branch-on-merge on, auto-merge on, wiki/projects off) ---"
else
  echo "==> ${REPO}: merge-safety repo settings"
  gh api -X PATCH "repos/${REPO}" \
    -F allow_squash_merge=true \
    -F allow_merge_commit=true \
    -F allow_rebase_merge=false \
    -F delete_branch_on_merge=true \
    -F allow_auto_merge=true \
    -F has_wiki=false \
    -F has_projects=false >/dev/null
fi

# --- release-tag rulesets (public profile only) ------------------------------
# Make v* and chart-v* release tags IMMUTABLE: block deletion and non-fast-forward
# (re-pointing) — the runbook's "never re-point a consumed tag" invariant, enforced.
apply_tag_ruleset() {
  local name="$1" pattern="$2"
  local body
  body="$(jq -cn --arg name "$name" --arg pat "$pattern" '
    { name: $name, target: "tag", enforcement: "active",
      conditions: { ref_name: { include: [$pat], exclude: [] } },
      rules: [ {type: "deletion"}, {type: "non_fast_forward"} ] }')"
  if $DRY_RUN; then
    echo "--- DRY RUN: tag ruleset $name ($pattern) ---"; echo "$body" | jq .
    return
  fi
  # Idempotent: update the existing ruleset of this name, else create it.
  local id
  id="$(gh api "repos/${REPO}/rulesets" --jq ".[] | select(.name==\"${name}\") | .id" 2>/dev/null | head -1 || true)"
  if [ -n "${id:-}" ]; then
    echo "==> updating tag ruleset ${name} (id ${id})"
    echo "$body" | gh api -X PUT "repos/${REPO}/rulesets/${id}" --input - >/dev/null
  else
    echo "==> creating tag ruleset ${name}"
    echo "$body" | gh api -X POST "repos/${REPO}/rulesets" --input - >/dev/null
  fi
}

if [ "$TAGS" = "on" ]; then
  apply_tag_ruleset "release-tags-v"     "refs/tags/v*"
  apply_tag_ruleset "release-tags-chart" "refs/tags/chart-v*"
else
  echo "==> tag rulesets skipped (profile/flag). The provider vX.Y.Z ruleset lives in the terraform-provider-olivares repo, not here."
fi

echo "==> done. Manual / separate steps:"
echo "    - enable GitHub Discussions (Settings > General > Features) — no stable API toggle"
echo "    - install the DCO GitHub App (https://github.com/apps/dco) so the Signed-off-by check is required"
echo "    - enable 'Require signed commits' ONLY if you adopt GPG/SSH signing org-wide (DCO is the default)"
# ⛔ NO `gh pr checks` AQUI, Y LA DIFERENCIA PUEDE APAGAR LA PROTECCION. Medido el 2026-08-24
# con el token de esta caja: `gh pr checks <PR>` imprime «no checks reported on the branch» y
# sale con **rc=0**, mientras la API que hay debajo responde
#     403 Resource not accessible by personal access token
# — tambien para `main`, o sea que no es que la PR no tenga checks: es que el token no puede
# leerlos. El instrumento traduce una PROHIBICION en una AUSENCIA, y encima con codigo de exito.
#
# Por que importa justo en este guion: el paso siguiente que recomendaba era «reconcile
# --contexts si un job cambio de id». Un operador que lea «no hay contextos» concluye que TODOS
# los declarados estan obsoletos y los quita — desactivando los checks requeridos de la rama
# protegida a partir de una medida que nunca se hizo.
#
# `actions/runs` si lee con este mismo token, y de ahi salen los nombres de job tal cual los
# usa la proteccion.
# ⛔ Y LA RECETA DE ABAJO NO PUEDE SER `?per_page=1`, que fue mi primera version y reintroducia
# el MISMO dano que este bloque existe para evitar. Un contraste externo lo midio:
#   · `actions/runs` sin filtro devuelve el ULTIMO run del repositorio, que puede ser de otro
#     push, de otra PR o de UN SOLO workflow — o sea una lista de jobs INCOMPLETA;
#   · `/jobs` PAGINA, asi que aun con el run correcto se pierden nombres a partir de 30;
#   · y una lista incompleta leida como completa hace exactamente lo que temiamos: reconciliar
#     `--contexts` borrando contextos validos de los workflows que no salieron.
# Una medida parcial leida como total es peor que no medir, porque autoriza a actuar.
echo "    - VALIDATE against a real PR once CI has run. ⛔ NOT with 'gh pr checks': under a token"
echo "      without checks:read it prints 'no checks reported' and exits 0 while the API says 403."
echo "      Use the runs API, which distinguishes 'no puedo mirar' from 'no hay' — but scope it"
echo "      to the PR head and enumerate EVERY run and EVERY page, or the list is partial:"
echo "        SHA=\$(gh api \"repos/${REPO}/pulls/<PR>\" --jq .head.sha)"
echo "        gh api --paginate \"repos/${REPO}/actions/runs?event=pull_request&head_sha=\$SHA\" \\"
echo "          --jq '.workflow_runs[].id' | while read -r R; do"
echo "            gh api --paginate \"repos/${REPO}/actions/runs/\$R/jobs\" --jq '.jobs[].name'"
echo "          done | sort -u"
echo "      If any of those 403s, you have NOT measured the contexts — do not touch --contexts."
echo "      A PARTIAL list read as complete is worse than no measurement: it authorises removal."
echo "      Reconcile --contexts only against names you actually read. (The hub has Actions off,"
echo "      so validate on the public repo.)"
