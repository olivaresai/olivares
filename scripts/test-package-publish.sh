#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Hermetic R2 double for DIST-24-06 F2. No network or real credential is used.
set -euo pipefail
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || { echo "test-package-publish: cannot isolate git environment" >&2; exit 2; }
unset _olivares_git_env
LC_ALL=C
export LC_ALL

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || {
	echo 'test-package-publish: NO HE PODIDO MIRAR — TMPDIR must be an existing absolute directory' >&2
	exit 2
}
for tool in awk bash cat chmod cmp cp dirname find grep mkdir mktemp rm sed sort wc; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-package-publish: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done
cleanup() {
	case "$scratch" in "$tmp_root"/package-publish-test.*) rm -rf -- "$scratch" ;; *) ;; esac
}
scratch="$(mktemp -d "$tmp_root/package-publish-test.XXXXXX")"; trap cleanup EXIT INT TERM
mkdir -p "$scratch/bin" "$scratch/store" "$scratch/tree" "$scratch/evidence"

cat >"$scratch/bin/wrangler" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == --version ]]; then printf '%s\n' 4.100.0; exit 0; fi
[[ "${1:-}" == r2 && "${2:-}" == object ]] || exit 9
verb="${3:-}"; ref="${4:-}"; shift 4
[[ "$ref" != /* && "$ref" != *..* ]] || exit 9
file=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in --file) file="${2:-}"; shift 2 ;; *) shift ;; esac
done
target="$FAKE_STORE/$ref"
mkdir -p "$(dirname "$target")"
case "$verb" in
put)
  printf 'put\t%s\n' "$ref" >>"$FAKE_LOG"
  if [[ -n "${FAKE_FAIL_KEY:-}" && "$ref" == *"$FAKE_FAIL_KEY" ]]; then exit 7; fi
  cp "$file" "$target"
  if [[ -n "${FAKE_CORRUPT_KEY:-}" && "$ref" == *"$FAKE_CORRUPT_KEY" ]]; then printf 'corrupt\n' >>"$target"; fi
  ;;
get)
  if [[ -n "${FAKE_GET_ERROR_KEY:-}" && "$ref" == "$FAKE_GET_ERROR_KEY" ]]; then
    echo 'simulated R2 transport failure' >&2
    exit 8
  fi
  [[ -f "$target" ]] || { echo 'The specified key does not exist. [code: 10007]' >&2; exit 1; }
  cp "$target" "$file"
  ;;
delete)
  rm -f -- "$target"
  ;;
*) exit 9 ;;
esac
FAKE
chmod +x "$scratch/bin/wrangler"

make_file() {
	local rel="$1"
	mkdir -p "$scratch/tree/$(dirname "$rel")"
	printf 'fixture %s\n' "$rel" >"$scratch/tree/$rel"
}
for rel in \
	olivares-packages.gpg olivares-packages.asc olivares-packages-apk.rsa.pub \
	keys/olivares-package-repository.asc keys/olivares-packages-apk.rsa.pub \
	stable/apt/dists/stable/InRelease stable/apt/dists/stable/Release stable/apt/dists/stable/Release.gpg \
	stable/rpm/x86_64/repodata/repomd.xml stable/rpm/x86_64/repodata/repomd.xml.asc \
	stable/rpm/aarch64/repodata/repomd.xml stable/rpm/aarch64/repodata/repomd.xml.asc \
	stable/apk/x86_64/APKINDEX.tar.gz stable/apk/aarch64/APKINDEX.tar.gz \
	stable/repository-manifest.json stable/repository-manifest.json.asc \
	security/apt/dists/security/InRelease security/apt/dists/security/Release security/apt/dists/security/Release.gpg \
	security/rpm/x86_64/repodata/repomd.xml security/rpm/x86_64/repodata/repomd.xml.asc \
	security/rpm/aarch64/repodata/repomd.xml security/rpm/aarch64/repodata/repomd.xml.asc \
	security/apk/x86_64/APKINDEX.tar.gz security/apk/aarch64/APKINDEX.tar.gz \
	security/repository-manifest.json security/repository-manifest.json.asc \
	stable/apt/dists/stable/main/binary-amd64/Packages.gz \
	stable/apt/pool/main/o/olivares/olivares.deb; do
	make_file "$rel"
done

export FAKE_STORE="$scratch/store"
export FAKE_LOG="$scratch/wrangler.log"
export CLOUDFLARE_API_TOKEN=test-only
export CLOUDFLARE_ACCOUNT_ID=0123456789abcdef0123456789abcdef
export OLIVARES_PACKAGE_PUBLISH_APPROVED=1
export OLIVARES_WRANGLER_BIN="$scratch/bin/wrangler"
publisher="$root/scripts/publish-package-repositories.sh"
run_publish() {
	bash "$publisher" --bucket olivares-packages --tree "$scratch/tree" \
		--staging-id run-42-attempt-1 "$@"
}
checks=0
ok() { checks=$((checks + 1)); printf 'ok %02d - %s\n' "$checks" "$1"; }

run_publish --mode stage >"$scratch/out"
[[ "$(find "$scratch/store" -type f | wc -l)" -eq 0 ]]
grep -F 'DRY-RUN' "$scratch/out" >/dev/null
ok 'dry-run writes zero R2 objects'

run_publish --mode stage --apply >"$scratch/out"
[[ -f "$scratch/store/olivares-packages/staging/run-42-attempt-1/.inventory.sha256" ]]
[[ ! -e "$scratch/store/olivares-packages/stable" ]]
ok 'stage is reread under staging and leaves canonical stable absent'

run_publish --mode verify-stage --apply >"$scratch/out"
grep -F 'VERIFIED' "$scratch/out" >/dev/null
ok 'remote staging inventory verifies byte-for-byte'

set +e
run_publish --mode promote --apply --evidence-dir "$scratch/evidence" >"$scratch/out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 1 && ! -e "$scratch/store/olivares-packages/stable" ]]
grep -F 'missing clean-client evidence' "$scratch/out" >/dev/null
ok 'mutant without client evidence cannot make an index visible in stable'

for family in apt rpm apk; do printf '%s\n' run-42-attempt-1 >"$scratch/evidence/$family.ok"; done

# A partial red must restore bytes that existed before the attempt and remove
# every newly exposed canonical object. Staging is the durable recovery source.
previous="$scratch/store/olivares-packages/stable/apt/dists/stable/main/binary-amd64/Packages.gz"
mkdir -p "$(dirname "$previous")"
printf '%s\n' 'previous canonical package' >"$previous"
export FAKE_FAIL_KEY=stable/apt/dists/stable/InRelease
set +e
run_publish --mode promote --apply --evidence-dir "$scratch/evidence" >"$scratch/out" 2>&1
rc=$?
set -e
unset FAKE_FAIL_KEY
[[ "$rc" -eq 1 ]]
grep -F 'rolling canonical objects back' "$scratch/out" >/dev/null
grep -Fx 'previous canonical package' "$previous" >/dev/null
unexpected="$(find "$scratch/store/olivares-packages" -type f \
	! -path "$scratch/store/olivares-packages/staging/*" ! -path "$previous" -print -quit)"
[[ -z "$unexpected" ]]
ok 'mutant partial promotion restores prior bytes and removes new canonical roots'

export FAKE_GET_ERROR_KEY=olivares-packages/stable/apt/dists/stable/InRelease
set +e
run_publish --mode promote --apply --evidence-dir "$scratch/evidence" >"$scratch/out" 2>&1
rc=$?
set -e
unset FAKE_GET_ERROR_KEY
[[ "$rc" -eq 2 ]]
grep -F 'cannot distinguish absent canonical object from a failed read' "$scratch/out" >/dev/null
grep -F 'rolling canonical objects back' "$scratch/out" >/dev/null
grep -Fx 'previous canonical package' "$previous" >/dev/null
unexpected="$(find "$scratch/store/olivares-packages" -type f \
	! -path "$scratch/store/olivares-packages/staging/*" ! -path "$previous" -print -quit)"
[[ -z "$unexpected" ]]
ok 'mutant ambiguous canonical read also rolls prior writes back and returns could-not-look'

immutable="$scratch/store/olivares-packages/stable/apt/pool/main/o/olivares/olivares.deb"
mkdir -p "$(dirname "$immutable")"
printf '%s\n' 'different bytes under an immutable version path' >"$immutable"
set +e
run_publish --mode promote --apply --evidence-dir "$scratch/evidence" >"$scratch/out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 1 ]]
grep -F 'immutable canonical object differs; refusing overwrite' "$scratch/out" >/dev/null
grep -Fx 'different bytes under an immutable version path' "$immutable" >/dev/null
unexpected="$(find "$scratch/store/olivares-packages" -type f \
	! -path "$scratch/store/olivares-packages/staging/*" ! -path "$previous" ! -path "$immutable" -print -quit)"
[[ -z "$unexpected" ]]
ok 'mutant immutable package collision is red and preserves the prior canonical byte'
rm -f -- "$immutable"

set +e
run_publish --mode promote --apply --evidence-dir "$scratch/evidence" >"$scratch/out" 2>&1
rc=$?
set -e
if [[ "$rc" -ne 0 ]]; then
	printf 'test-package-publish: positive promotion failed (rc=%d)\n' "$rc" >&2
	sed 's/^/  /' "$scratch/out" >&2
	exit 1
fi
[[ -f "$scratch/store/olivares-packages/stable/apt/dists/stable/InRelease" ]]
[[ -f "$scratch/store/olivares-packages/security/apk/x86_64/APKINDEX.tar.gz" ]]
content_line="$(awk -F '\t' '$2 ~ /stable\/apt\/pool/{print NR; exit}' "$scratch/wrangler.log")"
root_line="$(awk -F '\t' '$2 ~ /stable\/apt\/dists\/stable\/InRelease$/{line=NR} END{print line+0}' "$scratch/wrangler.log")"
[[ "$content_line" -gt 0 && "$root_line" -gt "$content_line" ]]
ok 'positive promotion writes content before signed discovery roots'

wrong_key_log="$scratch/wrong-key.log"
bash "$root/scripts/test-package-repositories.sh" >"$wrong_key_log"
grep -F 'mutant-wrong-signing-key' "$wrong_key_log" >/dev/null
grep -F '4/4 mutants red with positive controls' "$wrong_key_log" >/dev/null
ok 'real wrong-signing-key mutant remains red with its positive control'

printf 'test-package-publish: OK — %d controls; staging-only, partial and wrong-key mutants are red\n' "$checks"
