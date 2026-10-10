#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# compat.sh CANDIDATE — the candidate olivares binary built from this tree keeps the published
# contracts of the current published release, and both stores upgrade from it. A release
# cut excludes its own version. Selects the baseline from RELEASE-VERSION and the checkout's
# release tag, authenticates its signed checksums with the isolated cosign
# (scripts/assert-cosign-binary.sh --isolate), captures both binaries' native contracts and
# compares them, then upgrades SQLite and PostgreSQL from the published binary to the candidate.
# Needs Go, Node with web/node_modules on NODE_PATH, the browser on PLAYWRIGHT_BROWSERS_PATH,
# USERSPACE_POSTGRES_DSN, and USERSPACE_OUT, an absolute directory: it writes baseline.json,
# published.json, candidate.json, findings.json and the sqlite/ and postgres/ results there and
# registers the published-source worktree that the workflow's always() step removes. Exits
# non-zero on any break.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: USERSPACE_OUT=DIR $0 CANDIDATE" >&2; exit 2; }
candidate="$(realpath -e "$1")"
case "${USERSPACE_OUT:-}" in
/*) ;;
*) echo "compat.sh: USERSPACE_OUT must name an absolute directory" >&2; exit 2 ;;
esac
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

version="$(awk '!/^#/ && NF { print $1 }' RELEASE-VERSION)"
baseline_args=(--candidate-version "$version")
tags_on_head="$(git tag --points-at HEAD --list "$version")"
# Git's checkout identity covers local cuts; the Actions ref covers a checkout without tags.
if [[ -n "$tags_on_head" || "${GITHUB_REF:-}" == "refs/tags/$version" ]]; then
	baseline_args+=(--release-cut)
fi
python3 scripts/userspace/resolve_baseline.py "${baseline_args[@]}" > "$USERSPACE_OUT/baseline.json"
if jq -e 'type == "null"' "$USERSPACE_OUT/baseline.json" >/dev/null; then
	# Establish the initial native contracts: 1.0 at its cut has no earlier stable release,
	# and before its publication development has none to protect. Later releases keep the
	# existing two-store checks.
	(
		export GOMAXPROCS=4 GOFLAGS=-p=2
		python3 -m unittest discover -s scripts/userspace -p 'test_*.py' -v
		go vet scripts/userspace/source.go
		bash scripts/check-migrations.sh
		python3 scripts/userspace/capture.py --root "$PWD" --binary "$candidate" \
			--output "$USERSPACE_OUT/candidate.json"
	)
	printf '{"baseline":null,"selection":"first stable release","findings":[]}\n' >"$USERSPACE_OUT/findings.json"
	echo 'Initial stable contracts captured; predecessor comparison and data upgrade do not apply.'
	exit 0
fi
tag="$(jq -r .tag_name "$USERSPACE_OUT/baseline.json")"
echo "Release version: $version; published baseline: $tag"
assets="$USERSPACE_OUT/published-assets"
mkdir -m 700 "$assets"
archive="olivares_${tag}_linux_amd64.tar.gz"
for name in "$archive" checksums.txt checksums.txt.sig checksums.txt.pem release-commit.txt; do
	curl --fail --location --retry 2 --max-time 90 \
		"https://github.com/olivaresai/olivares/releases/download/$tag/$name" -o "$assets/$name"
done
bash scripts/cosign-verified.sh verify-blob \
	--certificate "$assets/checksums.txt.pem" --signature "$assets/checksums.txt.sig" \
	--certificate-identity "https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/$tag" \
	--certificate-oidc-issuer https://token.actions.githubusercontent.com \
	--certificate-github-workflow-repository olivaresai/olivares "$assets/checksums.txt"
bash scripts/verify-archive-digest.sh "$assets/checksums.txt" "$assets/$archive" "$assets/release-commit.txt"
git fetch --no-tags https://github.com/olivaresai/olivares.git "refs/tags/$tag:refs/tags/$tag"
published_sha="$(tr -d '\r\n' < "$assets/release-commit.txt")"
[[ "$published_sha" =~ ^[0-9a-f]{40}$ ]]
test "$(git rev-parse "$tag^{commit}")" = "$published_sha"
# Runner temp cleanup can leave a registration after a cancelled job.
git worktree add --force --detach "$USERSPACE_OUT/published-source" "$published_sha"
python3 - "$assets/$archive" "$USERSPACE_OUT/published-bin" <<'PY'
import pathlib, shutil, sys, tarfile
destination = pathlib.Path(sys.argv[2])
destination.mkdir(mode=0o700)
with tarfile.open(sys.argv[1]) as bundle:
    members = [m for m in bundle.getmembers() if m.name == 'olivares']
    if len(members) != 1 or not members[0].isfile():
        raise SystemExit('published binary must be one regular archive member')
    with bundle.extractfile(members[0]) as source, (destination / 'olivares').open('xb') as target:
        shutil.copyfileobj(source, target)
(destination / 'olivares').chmod(0o755)
PY

# The check's own tests, then the native contracts of both binaries.
(
	export GOMAXPROCS=4 GOFLAGS=-p=2
	python3 -m unittest discover -s scripts/userspace -p 'test_*.py' -v
	go vet scripts/userspace/source.go
	bash scripts/check-migrations.sh
	python3 scripts/userspace/capture.py --root "$USERSPACE_OUT/published-source" \
		--binary "$USERSPACE_OUT/published-bin/olivares" --output "$USERSPACE_OUT/published.json"
	python3 scripts/userspace/capture.py --root "$PWD" \
		--binary "$candidate" --output "$USERSPACE_OUT/candidate.json"
)

rc=0
python3 scripts/userspace/check-userspace.py "$USERSPACE_OUT/published.json" "$USERSPACE_OUT/candidate.json" \
	> "$USERSPACE_OUT/findings.json" || rc=1
cat "$USERSPACE_OUT/findings.json"
for engine in sqlite postgres; do
	python3 scripts/userspace/upgrade.py --database "$engine" \
		--published "$USERSPACE_OUT/published-bin/olivares" \
		--candidate "$candidate" \
		--output "$USERSPACE_OUT/$engine" || rc=1
done
exit "$rc"
