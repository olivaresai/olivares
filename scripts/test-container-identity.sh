#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# The container's identity is "olivares": its hostname and its default user (uid/gid 65532).
# The base image's own `node` user must not survive, and every documented `docker run` and the
# Compose service name the hostname. Static: no docker, no network.
set -euo pipefail
cd "$(dirname -- "${BASH_SOURCE[0]}")/.."

fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }

grep -Eq '^[[:space:]]+hostname:[[:space:]]*olivares[[:space:]]*$' deploy/compose/docker-compose.yml \
	|| bad "deploy/compose/docker-compose.yml: the olivares service sets no hostname: olivares"

for f in Dockerfile Dockerfile.release; do
	grep -Eq 'groupadd --gid 65532 olivares' "$f" || bad "$f: gid 65532 is not named olivares"
	grep -Eq 'useradd --uid 65532 --gid 65532 .* olivares( \\)?$' "$f" || bad "$f: uid 65532 is not named olivares"
	grep -Eq 'userdel --remove node' "$f" || bad "$f: the base image's node user is not removed"
	if grep -Eq '\bnonroot\b' "$f"; then bad "$f: still names the user nonroot"; fi
done

# Every documented `docker run ... --name olivares` block (up to its `serve` line) has --hostname olivares.
while IFS= read -r f; do
	awk -v f="$f" '
		/docker run .*--name olivares([[:space:]]|$)/ { inrun = 1; seen = 0 }
		inrun && /--hostname olivares([[:space:]]|$)/ { seen = 1 }
		inrun && /serve/ { if (!seen) { print "FAIL: " f ":" NR ": docker run without --hostname olivares" > "/dev/stderr"; bad = 1 } inrun = 0 }
		END { exit bad }' "$f" || fail=1
done < <(git grep -l -I -E 'docker run .*--name olivares([[:space:]]|$)' -- ':!appliance' ':!design' ':!assessments' ':!ESTADO-PROYECTO.md' ':!docs/superpowers')

if [ "$fail" = 0 ]; then echo "container identity: ok"; fi
exit "$fail"
