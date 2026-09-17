#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation matrix for the cosign ACQUISITION POLICY gate
# (cmd/olivares/tools/checkcosignpins, invoked through scripts/check-cosign-pins.sh).
#
# WHY THIS EXISTS. The gate protects one property: no job in this repository may obtain a
# cosign other than the version whose signing contract is proven by
# check-cosign-contract.sh. Three implementations claimed that property and did not hold
# it; each was broken by an adversarial review using valid YAML that `actionlint` accepts.
# Every row below is one of those demonstrated defects, or one of the coverage probes the
# third review ran. A regression turns this file red.
#
# THE TEST FILE ITSELF WAS ALSO WRONG, TWICE, AND THAT IS FIXED HERE:
#   * a rejection case asserted only a NONZERO EXIT. The gate exits nonzero when it finds
#     no installer at all, so a scope regression satisfied every scope case. Each failure
#     case now asserts a SEMANTIC diagnostic substring.
#   * the "YAML-escaped action name" fixture contained an ordinary hyphen, so it tested
#     nothing. It now uses a real double-quoted YAML escape (-), which decodes to
#     `cosign-installer` and which a raw substring search cannot see.
#   * accepted cases asserted nothing about coverage, so a gate that silently stopped
#     seeing installers would still pass. They now assert the installer COUNT.
set -euo pipefail

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-cosign-pins.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-cosign-pins.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

SHA="6f9f17788090df1f26f669e9d70d6ae9567deba6"
OTHER_SHA="398d4b0eeef1380460a10c8013a76f728fb906ac"
APPROVED="v2.6.4"
BAD="v3.0.6"

pass=0
fail=0
CASE_DIR=""

# A canonical, correct installer. Every attack case that is not itself about the installer
# ships one, so a case can never be satisfied by the "no installer found anywhere" failure.
control_workflow() {
	cat <<YAML
jobs:
  control:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
YAML
}

begin() {
	CASE_DIR="$(mktemp -d "$WORK/case.XXXXXX")"
	mkdir -p "$CASE_DIR/.github/workflows" "$CASE_DIR/.github/actions"
}

add() { # add <relative-path>  (body on stdin)
	local p="$CASE_DIR/$1"
	mkdir -p "$(dirname "$p")"
	cat >"$p"
}

run_gate() { COSIGN_PINS_ROOT="$CASE_DIR" bash "$GATE" 2>&1; }

# accept <name> <expected-installer-count>
accept() {
	local name="$1" want_n="$2" out rc
	set +e
	out="$(run_gate)"
	rc=$?
	set -e
	if [ "$rc" -ne 0 ]; then
		fail=$((fail + 1))
		printf '  FAIL  %-56s wanted accept, rc=%d\n' "$name" "$rc"
		printf '%s\n' "$out" | sed 's/^/          /'
		return
	fi
	if ! grep -qF "OK ($want_n installer step(s)" <<<"$out"; then
		fail=$((fail + 1))
		printf '  FAIL  %-56s accepted but reported the wrong count (wanted %s)\n' "$name" "$want_n"
		printf '%s\n' "$out" | sed 's/^/          /'
		return
	fi
	pass=$((pass + 1))
	printf '  ok    %-56s accepted, %s installer(s)\n' "$name" "$want_n"
}

# reject <name> <expected-diagnostic-substring>
reject() {
	local name="$1" want_msg="$2" out rc
	set +e
	out="$(run_gate)"
	rc=$?
	set -e
	if [ "$rc" -eq 0 ]; then
		fail=$((fail + 1))
		printf '  FAIL  %-56s wanted reject, was ACCEPTED\n' "$name"
		return
	fi
	if ! grep -qF -- "$want_msg" <<<"$out"; then
		fail=$((fail + 1))
		printf '  FAIL  %-56s rejected for the WRONG reason\n' "$name"
		printf '        wanted a diagnostic containing: %s\n' "$want_msg"
		printf '%s\n' "$out" | sed 's/^/          /'
		return
	fi
	pass=$((pass + 1))
	printf '  ok    %-56s rejected (%s)\n' "$name" "$want_msg"
}

echo "cosign acquisition-policy gate — mutation matrix"

# --- accepted ------------------------------------------------------------------------
begin; control_workflow | add .github/workflows/a.yml
accept "canonical installer, approved version and revision" 1

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA} # v4.1.2
        with:
          # a reviewed justification lives here in the real workflows
          cosign-release: '${APPROVED}'
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
YAML
accept "two canonical installers, comments between" 2

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: cosign verify-blob --key k --signature s file
      - run: bash scripts/check-cosign-contract.sh
YAML
accept "ordinary cosign USE is not mistaken for acquisition" 1

# --- the failure the gate exists for --------------------------------------------------
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
      - run: echo next
YAML
reject "no with: mapping (takes action default)" "no \`with:\` mapping"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          install-dir: /usr/local/bin
YAML
reject "with: present but no cosign-release input" "no \`cosign-release\` input"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${BAD}'
YAML
reject "wrong cosign version" "want \"${APPROVED}\""

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${OTHER_SHA}
        with:
          cosign-release: '${APPROVED}'
YAML
reject "unreviewed installer revision" "want the reviewed"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@v4
        with:
          cosign-release: '${APPROVED}'
YAML
reject "floating action ref instead of a SHA" "want the reviewed"

# --- round-1 bypasses -------------------------------------------------------------------
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      -
        uses: sigstore/cosign-installer@${SHA}
      - uses: some/other-action@${SHA}
        with:
          cosign-release: '${APPROVED}'
YAML
reject "R1: bare dash, next action owns the pin" "no \`with:\` mapping"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        env:
          cosign-release: '${APPROVED}'
YAML
reject "R1: env: masquerading as with:" "no \`with:\` mapping"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: &installer sigstore/cosign-installer@${SHA}
      - uses: *installer
YAML
reject "R1: anchored + aliased uses scalar" "no \`with:\` mapping"

# --- round-2 bypasses -------------------------------------------------------------------
# A REAL YAML escape: - decodes to '-', so GitHub runs the installer while a raw
# substring search for "cosign-installer" finds nothing.
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: "sigstore/cosign-installer@${SHA}"
YAML
reject "R2: YAML-escaped action name (\\u002D)" "no \`with:\` mapping"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          install-dir: |
            /usr/local/bin
            cosign-release: '${APPROVED}'
YAML
reject "R2: fake pin inside an install-dir block scalar" "no \`cosign-release\` input"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release:
            - '${APPROVED}'
YAML
reject "R2: cosign-release is a sequence, not a scalar" "does not resolve to a plain scalar"

begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: \${{ env.COSIGN_VERSION }}
YAML
reject "R2: expression instead of a literal version" "want \"${APPROVED}\""

# --- round-3 coverage probes: other ways to OBTAIN cosign --------------------------------
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: go install github.com/sigstore/cosign/v3/cmd/cosign@v3.0.6
YAML
reject "R3: go install of cosign" "go install"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: "printf '#'; curl -sSL https://github.com/sigstore/cosign/releases/download/v3.0.6/cosign-linux-amd64 -o /usr/local/bin/cosign"
YAML
reject "R3: curl of a release asset behind a quoted '#'" "download of a cosign release asset"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: apt-get install -y cosign
YAML
reject "R3: package-manager installation" "package-manager installation"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: docker://gcr.io/projectsigstore/cosign:v3.0.6
YAML
reject "R3: docker:// step image that ships cosign" "supplies cosign outside the reviewed installer"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    container:
      image: gcr.io/projectsigstore/cosign:v3.0.6
    steps:
      - run: cosign version
YAML
reject "R3: job container image that ships cosign" "job container uses image"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<YAML
jobs:
  j:
    uses: evil/repo/.github/workflows/x.yml@${SHA}
YAML
reject "R3: unreviewed external reusable workflow" "reviewed allowlist"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: ./tools/cosign-action
YAML
add tools/cosign-action/action.yml <<YAML
name: local
description: installs cosign
runs:
  using: composite
  steps:
    - uses: sigstore/cosign-installer@${SHA}
      shell: bash
YAML
reject "R3: local action OUTSIDE .github/actions is resolved" "no \`with:\` mapping"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: ./tools/js-action
YAML
add tools/js-action/action.yml <<'YAML'
name: js
description: opaque
runs:
  using: node20
  main: index.js
YAML
reject "R3: local JavaScript action is declared unreadable" "cannot read what its code installs"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: ./tools/missing-action
YAML
reject "R3: local action with no metadata fails closed" "cannot be verified"

begin
add .github/workflows/a.yml <<YAML
jobs:
  control:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
---
jobs:
  second:
    steps:
      - uses: "sigstore/cosign-installer@${SHA}"
YAML
reject "R3: multi-document file is refused, not half-checked" "multi-document YAML"

# --- scope: each attack ships a valid control AND asserts a semantic diagnostic ---------
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yaml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
YAML
reject "scope: .yaml workflow is scanned" "b.yaml"

begin
control_workflow | add .github/workflows/a.yml
add .github/actions/probe/action.yml <<YAML
name: probe
description: composite probe
runs:
  using: composite
  steps:
    - uses: sigstore/cosign-installer@${SHA}
      shell: bash
YAML
reject "scope: composite action.yml is scanned" "action.yml"

begin
control_workflow | add .github/workflows/a.yml
add .github/actions/probe/action.yaml <<YAML
name: probe
description: composite probe
runs:
  using: composite
  steps:
    - uses: sigstore/cosign-installer@${SHA}
      shell: bash
YAML
reject "scope: composite action.yaml is scanned" "action.yaml"

# --- fail closed on unreadable input ----------------------------------------------------
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs: [ this is not
  valid: yaml: at all
YAML
reject "undecodable YAML fails closed" "cannot parse as YAML"

# --- a tree with no installer at all must not report success ----------------------------
begin
add .github/workflows/a.yml <<'YAML'
jobs:
  j:
    steps:
      - run: echo nothing to see
YAML
reject "no installer anywhere is a failure, not a pass" "no sigstore/cosign-installer step found"

# --- round-4 bypasses: alias, matrix-as-data, and root escape ----------------------------
# These are the constructs that broke the third yaml.Node walk. Every one of them is valid
# YAML that actionlint accepts, and every one of them was ACCEPTED by the previous gate.

# F1. The anchor is a correctly pinned installer; the alias use site has no `with:`, so it
# runs the action revision's DEFAULT version. The previous walk required `uses` to be a
# ScalarNode and never followed Alias, so the second step was invisible.
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: &installer sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
      - uses: *installer
YAML
reject "R4: approved anchor, aliased second use takes the default" "no \`with:\` mapping"

# The same alias used twice WITH pins is legitimate and must still be accepted, counted
# twice — otherwise the fix above would have been bought by breaking valid YAML.
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: &installer sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
      - uses: *installer
        with:
          cosign-release: '${APPROVED}'
YAML
accept "R4: aliased installer with its own pin is valid and counted" 2

# An aliased whole STEP mapping carries its own `with:`, so both uses are pinned.
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - &step
        uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
      - *step
YAML
accept "R4: aliased whole step keeps its pin" 2

# An aliased steps SEQUENCE must be walked at EVERY job that uses it — asserted as a
# COUNT, because a rejection here would also be produced by not walking the alias at all.
begin
add .github/workflows/a.yml <<YAML
jobs:
  a:
    steps: &steps
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
  b:
    steps: *steps
YAML
accept "R4: aliased steps sequence is walked at each use" 2

# F2. Matrix `include` objects are DATA. GitHub never executes them, so they must not buy
# installer credit — the previous walk counted every mapping in the document as a step.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<YAML
jobs:
  j:
    strategy:
      matrix:
        include:
          - uses: sigstore/cosign-installer@${SHA}
            with:
              cosign-release: '${APPROVED}'
    steps:
      - run: cosign version
YAML
reject "R4: matrix data does not buy installer credit" "referenced outside a reviewed step"

# F2. A container image selected from the matrix: the expression cannot be resolved
# statically, so it is refused rather than silently treated as clean.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    strategy:
      matrix:
        image: [gcr.io/projectsigstore/cosign:v3.0.6]
    container:
      image: ${{ matrix.image }}
    steps:
      - run: cosign version
YAML
reject "R4: matrix-selected job container is refused as unresolvable" "is built from an expression"

# The same hole from the other side: the literal that WOULD be selected is itself reported.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    strategy:
      matrix:
        image: [gcr.io/projectsigstore/cosign:v3.0.6]
    steps:
      - run: echo ${{ matrix.image }}
YAML
reject "R4: a cosign image in matrix values is reported" "matrix value"

# A dynamic SERVICE image is equally unresolvable.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    services:
      helper:
        image: ${{ inputs.helper_image }}
    steps:
      - run: echo hi
YAML
reject "R4: dynamic service image is refused" "service container image"

# A dynamic docker:// STEP image is equally unresolvable.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: docker://${{ matrix.image }}
YAML
reject "R4: dynamic docker:// step image is refused" "is built from an expression"

# F4. `uses: ../x` leaves the repository. A gate whose verdict depends on files it does not
# gate is not gating the tree; the target is refused, never read.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: ./../escaped-action
YAML
mkdir -p "$WORK/escaped-action"
cat >"$WORK/escaped-action/action.yml" <<'YAML'
name: outside
description: outside the repository
runs:
  using: composite
  steps:
    - run: echo unreviewed
      shell: bash
YAML
reject "R4: local action escaping the root via ../ is refused" "OUTSIDE the repository root"

# F4. The same escape through a SYMLINK, which lexical cleaning cannot see.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: ./linked-action
YAML
mkdir -p "$WORK/linked-target"
cat >"$WORK/linked-target/action.yml" <<'YAML'
name: outside
description: reached through a symlink
runs:
  using: composite
  steps:
    - run: echo unreviewed
      shell: bash
YAML
ln -s "$WORK/linked-target" "$CASE_DIR/linked-action"
reject "R4: local action reached through a symlink out of the tree" "OUTSIDE the repository root"

# F4. A local REUSABLE WORKFLOW gets the same containment.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    uses: ./../escaped.yml
YAML
cat >"$WORK/escaped.yml" <<'YAML'
jobs:
  x:
    steps:
      - run: echo unreviewed
YAML
reject "R4: local reusable workflow escaping the root is refused" "OUTSIDE the repository root"

# A local action that names ITSELF must terminate, not recurse forever.
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - uses: ./tools/selfref
YAML
add tools/selfref/action.yml <<'YAML'
name: selfref
description: names itself
runs:
  using: composite
  steps:
    - uses: ./tools/selfref
    - run: echo done
      shell: bash
YAML
accept "R4: a self-referencing local action terminates" 1

# --- round-4 fail-closed: duplicates, root kind, empty input ----------------------------
# yaml.v3 is first-wins on duplicate keys, so an approved pin followed by an unapproved one
# validated the first and reported nothing about the second.
begin
add .github/workflows/a.yml <<YAML
jobs:
  j:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
          cosign-release: '${BAD}'
YAML
reject "R4: duplicate mapping keys are refused, not first-wins" "duplicate mapping key"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
- this document root
- is a sequence
YAML
reject "R4: a non-mapping document root fails closed" "document root is not a mapping"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
# only a comment: nothing this gate can interpret
YAML
reject "R4: an empty or comment-only file is reported, not ignored" "empty or comment-only"

# --- round-4 acquisition coverage: verbs the previous patterns missed -------------------
begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: gh release download v3.0.6 -R sigstore/cosign -p 'cosign-linux-amd64'
YAML
reject "R4: gh release download of cosign" "gh release download"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: go run github.com/sigstore/cosign/v3/cmd/cosign@latest version
YAML
reject "R4: go run of cosign" "go run"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: git clone --depth 1 https://github.com/sigstore/cosign
YAML
reject "R4: git clone with flags before the URL" "clone of the cosign source"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: pip3 install cosign
YAML
reject "R4: pip3 (not just pip) installation" "pip installation"

begin
control_workflow | add .github/workflows/a.yml
add .github/workflows/b.yml <<'YAML'
jobs:
  j:
    steps:
      - run: pacman -S --noconfirm cosign
YAML
reject "R4: pacman flag-style install" "package-manager installation"

# The counterpart: tightening the package-manager pattern to one command must not have
# been bought by leaving a real installation uncaught, and must not fire on legitimate use.
begin
add .github/workflows/a.yml <<YAML
jobs:
  control:
    steps:
      - uses: sigstore/cosign-installer@${SHA}
        with:
          cosign-release: '${APPROVED}'
      - run: apt-get update && apt-get install -y jq && cosign verify-blob --key k f
YAML
accept "R4: package update THEN ordinary cosign use is not acquisition" 1

echo
echo "cosign acquisition-policy gate: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
