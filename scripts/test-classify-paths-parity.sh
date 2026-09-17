#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# Causal coverage / unknown / mutation battery for check-classify-paths-parity.sh.
# Own fixtures only. Does not copy a live result and does not claim list equality.
set -u
SUT="${SUT:-scripts/check-classify-paths-parity.sh}"
HELPER="${HELPER:-scripts/lib/ci-trigger-coverage.py}"
PASS=0
FAIL=0
OWNED_N=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/tcp.XXXXXX") || exit 2
trap 'rm -rf "$TMP"' EXIT

check() {
	local n="$1" e="$2" g="$3"
	if [ "$e" = "$g" ]; then
		PASS=$((PASS + 1))
		printf 'ok   %-72s %s\n' "$n" "$g"
	else
		FAIL=$((FAIL + 1))
		printf 'FAIL %s esperaba [%s], dio [%s]\n' "$n" "$e" "$g"
	fi
}

# Minimal inspectable workflow: main push, dispatch, classify case, ungated secrets.
write_ok() {
	cat >"$1" <<'EOF'
name: t
on:
  workflow_dispatch:
  push:
    branches: [main]
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          design/audits/* | docs/ai-context/* | ESTADO-PROYECTO.md) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
}

corre() {
	# Ordinary checker: data overrides only. Helper is resolved beside the script.
	OLIVARES_CI_FILE="$1" OLIVARES_ROOT="$TMP" \
		bash "$SUT" >"$TMP/out" 2>&1
	echo $?
}

run_owned() {
	# Mutants run an owned copy of checker+helper, never an env executable override.
	OWNED_N=$((OWNED_N + 1))
	local d="$TMP/owned-$OWNED_N"
	mkdir -p "$d/lib" || exit 2
	cp "$SUT" "$d/check-classify-paths-parity.sh" || exit 2
	cp "$2" "$d/lib/ci-trigger-coverage.py" || exit 2
	OLIVARES_CI_FILE="$1" OLIVARES_ROOT="$TMP" \
		bash "$d/check-classify-paths-parity.sh" >"$TMP/out" 2>&1
	echo $?
}

grep_only() {
	# The class of check this witness exists to replace: line-anchored grep.
	if grep -Eq '^[[:space:]]*paths-ignore:' "$1"; then
		echo 1
	else
		echo 0
	fi
}

write_ok "$TMP/ok.yml"
check "(1) inspectable workflow without path filters -> 0" 0 "$(corre "$TMP/ok.yml")"
check "(1b) names coverage, not list equality" 0 \
	"$(grep -q 'main-push eligible' "$TMP/out" && grep -q 'no path filter' "$TMP/out" && ! grep -q 'dicen lo mismo' "$TMP/out"; echo $?)"

# Harmless comments and blank lines must not change a supported verdict.
python3 - "$TMP/ok.yml" "$TMP/comments.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
open(dst, "w", encoding="utf-8").write(
    "# leading comment\n\n" + text.replace("branches: [main]", "branches: [main]  # keep main")
)
PY
check "(2) comments and blank lines still 0" 0 "$(corre "$TMP/comments.yml")"

# Block-style branches and quoted main stay eligible.
cat >"$TMP/block-branches.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches:
      - 'main'
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          design/audits/*) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(3) block-list quoted main still 0" 0 "$(corre "$TMP/block-branches.yml")"

# Current tree: eligibility and retained classifier results.
REAL=".github/workflows/mainline-ci.yml"
check "(4) current workflow -> 0" 0 "$(corre "$REAL")"
elig() {
	python3 "$HELPER" eligible "$REAL" main "$@"
	echo $?
}
cls() {
	python3 "$HELPER" classify "$REAL" "$@"
	echo $?
}
check "(5) session-only main push is eligible" 0 \
	"$(out=$(python3 "$HELPER" eligible "$REAL" main sessions/status/inbox/a.md); echo $?; printf '%s' "$out" >"$TMP/e")"
check "(5b) session-only says yes" yes "$(cat "$TMP/e")"
check "(6) code-only main push is eligible" yes \
	"$(python3 "$HELPER" eligible "$REAL" main core/internal/store/sqlstore/store.go)"
check "(7) all three remaining journal prefixes are eligible" yes \
	"$(python3 "$HELPER" eligible "$REAL" main design/audits/x.md docs/ai-context/example.md ESTADO-PROYECTO.md)"
check "(8) classifier: remaining audits prefix is journal" journal \
	"$(python3 "$HELPER" classify "$REAL" design/audits/x.md)"
check "(9) classifier: docs/ai-context is journal" journal \
	"$(python3 "$HELPER" classify "$REAL" docs/ai-context/example.md)"
check "(10) classifier: ESTADO-PROYECTO.md is journal" journal \
	"$(python3 "$HELPER" classify "$REAL" ESTADO-PROYECTO.md)"
check "(11) classifier: sessions are code" code \
	"$(python3 "$HELPER" classify "$REAL" sessions/foo.md)"
check "(12) classifier: product code is code" code \
	"$(python3 "$HELPER" classify "$REAL" core/x.go)"
check "(13) classifier: mixed journal+code is code" code \
	"$(python3 "$HELPER" classify "$REAL" design/audits/x.md core/x.go)"

# Reintroduced old filters and every path-filter form -> 1.
python3 - "$TMP/ok.yml" "$TMP/old-ignore.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace(
    "    branches: [main]\n",
    "    branches: [main]\n    paths-ignore:\n"
    "      - 'design/audits/**'\n"
    "      - 'ESTADO-PROYECTO.md'\n"
    "      - 'docs/ai-context/**'\n",
    1,
)
open(dst, "w", encoding="utf-8").write(text)
PY
check "(14) reintroduced old paths-ignore -> 1" 1 "$(corre "$TMP/old-ignore.yml")"
check "(14b) names the filter" 0 "$(grep -q 'paths-ignore' "$TMP/out"; echo $?)"
check "(14c) old filters skip a docs-only push" no \
	"$(python3 "$HELPER" eligible "$TMP/old-ignore.yml" main design/audits/x.md)"
check "(14d) old filters still start a code push" yes \
	"$(python3 "$HELPER" eligible "$TMP/old-ignore.yml" main core/x.go)"

python3 - "$TMP/ok.yml" "$TMP/paths.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace(
    "    branches: [main]\n",
    "    branches: [main]\n    paths:\n      - 'core/**'\n",
    1,
)
open(dst, "w", encoding="utf-8").write(text)
PY
check "(15) push paths: filter -> 1" 1 "$(corre "$TMP/paths.yml")"
check "(15b) docs-only would not start under paths:" no \
	"$(python3 "$HELPER" eligible "$TMP/paths.yml" main design/audits/x.md)"

cat >"$TMP/quoted-ignore.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    "paths-ignore":
      - 'sessions/**'
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          design/audits/*) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(16) quoted paths-ignore key -> 1" 1 "$(corre "$TMP/quoted-ignore.yml")"
check "(16b) grep-only overlooks the quoted key and returns 0" 0 "$(grep_only "$TMP/quoted-ignore.yml")"

cat >"$TMP/quoted-paths.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    'paths':
      - 'core/**'
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          design/audits/*) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(17) quoted paths key -> 1" 1 "$(corre "$TMP/quoted-paths.yml")"

cat >"$TMP/flow.yml" <<'EOF'
on:
  workflow_dispatch:
  push: {branches: [main], paths-ignore: [sessions/**]}
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          design/audits/*) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(18) flow-mapping paths-ignore -> 1" 1 "$(corre "$TMP/flow.yml")"
check "(18b) grep-only overlooks the flow mapping and returns 0" 0 "$(grep_only "$TMP/flow.yml")"

cat >"$TMP/flow-paths.yml" <<'EOF'
on:
  workflow_dispatch:
  push: {branches: [main], paths: [core/**]}
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          design/audits/*) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(19) flow-mapping paths -> 1" 1 "$(corre "$TMP/flow-paths.yml")"

# Secrets coupling / removal.
python3 - "$TMP/ok.yml" "$TMP/secrets-needs.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace("  secrets:\n", "  secrets:\n    needs: [classify]\n", 1)
open(dst, "w", encoding="utf-8").write(text)
PY
check "(20) secrets needs classify -> 1" 1 "$(corre "$TMP/secrets-needs.yml")"
check "(20b) names the coupling" 0 "$(grep -q 'needs classify' "$TMP/out"; echo $?)"

python3 - "$TMP/ok.yml" "$TMP/secrets-needs-str.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace("  secrets:\n", "  secrets:\n    needs: classify\n", 1)
open(dst, "w", encoding="utf-8").write(text)
PY
check "(21) secrets needs: classify string -> 1" 1 "$(corre "$TMP/secrets-needs-str.yml")"

python3 - "$TMP/ok.yml" "$TMP/secrets-if.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace(
    "  secrets:\n",
    "  secrets:\n    if: needs.classify.outputs.code == 'true'\n",
    1,
)
open(dst, "w", encoding="utf-8").write(text)
PY
check "(22) secrets job-level if depends on classify -> 1" 1 "$(corre "$TMP/secrets-if.yml")"

python3 - "$TMP/ok.yml" "$TMP/no-secrets.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read().replace("  secrets:\n    steps:\n      - run: echo secrets\n", "")
open(dst, "w", encoding="utf-8").write(text)
PY
check "(23) secrets job removed -> 2" 2 "$(corre "$TMP/no-secrets.yml")"

python3 - "$TMP/ok.yml" "$TMP/no-classify.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
start = text.index("  classify:")
end = text.index("  secrets:")
open(dst, "w", encoding="utf-8").write(text[:start] + text[end:])
PY
check "(24) classify job removed -> 2" 2 "$(corre "$TMP/no-classify.yml")"

check "(25) missing workflow file -> 2" 2 "$(corre "$TMP/missing.yml")"

cat >"$TMP/no-on.yml" <<'EOF'
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          a) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(26) missing on: -> 2" 2 "$(corre "$TMP/no-on.yml")"

# Unsupported shapes must not pass.
cat >"$TMP/anchor.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    extra: &anchor value
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          a) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(27) anchor -> 2" 2 "$(corre "$TMP/anchor.yml")"

cat >"$TMP/alias.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    extra: *anchor
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          a) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(28) alias -> 2" 2 "$(corre "$TMP/alias.yml")"

cat >"$TMP/merge.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    <<: {paths-ignore: [sessions/**]}
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          a) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(29) merge key -> 2" 2 "$(corre "$TMP/merge.yml")"

cat >"$TMP/tag.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    extra: !!str ignored
jobs:
  classify:
    steps:
      - run: |
          case "$f" in
          a) ;;
          *) journal_only=false; break ;;
          esac
  secrets:
    steps:
      - run: echo secrets
EOF
check "(30) tag -> 2" 2 "$(corre "$TMP/tag.yml")"

cat >"$TMP/malformed.yml" <<'EOF'
on:
  workflow_dispatch:
  push:
    branches: [main]
    extra: "unclosed
jobs:
  classify:
    steps:
      - run: echo x
  secrets:
    steps:
      - run: echo secrets
EOF
check "(31) malformed quoted scalar -> 2" 2 "$(corre "$TMP/malformed.yml")"

# Checker mutant: drop the paths token from the filter set. The paths: fixture must catch it.
sed 's/"paths", //' "$HELPER" >"$TMP/mut-no-paths.py"
check "(32) paths-key mutant really differs" 0 \
	"$(cmp -s "$HELPER" "$TMP/mut-no-paths.py" && echo 1 || echo 0)"
M32=$(run_owned "$TMP/paths.yml" "$TMP/mut-no-paths.py")
check "(32b) mutant that ignores paths returns 0 on a paths filter" 0 "$M32"
check "(32c) original still reports that filter" 1 "$(corre "$TMP/paths.yml")"

# Checker mutant: secrets job-level if is never treated as classify coupling.
python3 - "$HELPER" "$TMP/mut-no-if.py" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
old = "def _if_mentions_classify(if_val) -> bool:\n"
new = old + "    return False\n"
if old not in text:
    raise SystemExit("mutant anchor missing")
open(dst, "w", encoding="utf-8").write(text.replace(old, new, 1))
PY
check "(33) secrets-if mutant really differs" 0 \
	"$(cmp -s "$HELPER" "$TMP/mut-no-if.py" && echo 1 || echo 0)"
M33=$(run_owned "$TMP/secrets-if.yml" "$TMP/mut-no-if.py")
check "(33b) mutant that skips secrets if returns 0" 0 "$M33"
check "(33c) original still reports secrets if" 1 "$(corre "$TMP/secrets-if.yml")"

# Ordinary invocation must ignore the retired executable override.
check "(34) ordinary checker does not name OLIVARES_TRIGGER_COVERAGE_PY" 1 \
	"$(grep -F OLIVARES_TRIGGER_COVERAGE_PY "$SUT" >/dev/null; echo $?)"
STOLEN_RC=$(
	OLIVARES_TRIGGER_COVERAGE_PY="$TMP/mut-no-paths.py" \
		OLIVARES_CI_FILE="$TMP/paths.yml" OLIVARES_ROOT="$TMP" \
		bash "$SUT" >"$TMP/stolen.out" 2>&1
	echo $?
)
check "(34b) env override cannot replace the helper; paths filter still 1" 1 "$STOLEN_RC"

# Exact main despite a preceding unsupported positive glob.
python3 - "$TMP/ok.yml" "$TMP/glob-then-main.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
open(dst, "w", encoding="utf-8").write(
    text.replace("branches: [main]", "branches: ['release/*', main]", 1)
)
PY
check "(35) unsupported positive glob then exact main -> 0" 0 "$(corre "$TMP/glob-then-main.yml")"
python3 - "$TMP/ok.yml" "$TMP/main-then-glob.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
open(dst, "w", encoding="utf-8").write(
    text.replace("branches: [main]", "branches: [main, 'release/*']", 1)
)
PY
check "(36) exact main then unsupported positive glob -> 0" 0 "$(corre "$TMP/main-then-glob.yml")"
python3 - "$TMP/ok.yml" "$TMP/glob-only.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
open(dst, "w", encoding="utf-8").write(
    text.replace("branches: [main]", "branches: ['release/*']", 1)
)
PY
check "(37) unsupported positive glob without exact main -> 2" 2 "$(corre "$TMP/glob-only.yml")"

# Negation: exclusion cannot be ruled out.
python3 - "$TMP/ok.yml" "$TMP/neg-main.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
open(dst, "w", encoding="utf-8").write(
    text.replace("branches: [main]", "branches: ['!main']", 1)
)
PY
check "(38) negated main branch pattern -> 2" 2 "$(corre "$TMP/neg-main.yml")"
python3 - "$TMP/ok.yml" "$TMP/main-and-neg.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
open(dst, "w", encoding="utf-8").write(
    text.replace("branches: [main]", "branches: [main, '!main']", 1)
)
PY
check "(39) exact main with a negation stays unknown -> 2" 2 "$(corre "$TMP/main-and-neg.yml")"

# Test-facing eligible helper: !pattern is unknown 2, not a silent non-match.
python3 - "$TMP/ok.yml" "$TMP/neg-paths-ignore.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace(
    "    branches: [main]\n",
    "    branches: [main]\n    paths-ignore:\n      - '!sessions/**'\n",
    1,
)
open(dst, "w", encoding="utf-8").write(text)
PY
check "(40) gate still sees a paths-ignore key -> 1" 1 "$(corre "$TMP/neg-paths-ignore.yml")"
NEG_ELIG_RC=0
NEG_ELIG_OUT=$(python3 "$HELPER" eligible "$TMP/neg-paths-ignore.yml" main sessions/foo.md 2>"$TMP/neg-elig.err") || NEG_ELIG_RC=$?
check "(40b) eligible does not treat bang-pattern as a miss" 2 "$NEG_ELIG_RC"
check "(40c) eligible names unknown, not yes/no" 0 \
	"$(grep -q 'NO HE PODIDO MIRAR' "$TMP/neg-elig.err" && ! grep -qE '^(yes|no)$' <<<"$NEG_ELIG_OUT"; echo $?)"
python3 - "$TMP/ok.yml" "$TMP/neg-paths.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
text = text.replace(
    "    branches: [main]\n",
    "    branches: [main]\n    paths:\n      - '!docs/**'\n",
    1,
)
open(dst, "w", encoding="utf-8").write(text)
PY
NEG_PATHS_RC=0
python3 "$HELPER" eligible "$TMP/neg-paths.yml" main design/audits/x.md >/dev/null 2>"$TMP/neg-paths.err" || NEG_PATHS_RC=$?
check "(41) eligible negated paths filter -> 2" 2 "$NEG_PATHS_RC"

# A preceding positive match must not hide an unsupported later negation.
for FILTER in paths paths-ignore; do
    for PATTERNS in "['docs/**', '!docs/private/**']" "['!docs/private/**', 'docs/**']"; do
        printf 'on:\n  push:\n    branches: [main]\n    %s: %s\njobs: {}\n' "$FILTER" "$PATTERNS" >"$TMP/mixed-negation.yml"
        MIXED_RC=0
        python3 "$HELPER" eligible "$TMP/mixed-negation.yml" main docs/private/key.md >"$TMP/mixed.out" 2>"$TMP/mixed.err" || MIXED_RC=$?
        check "(42) $FILTER mixed negation in either order -> 2" 2 "$MIXED_RC"
    done
done

echo
echo "check-classify-paths-parity selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
