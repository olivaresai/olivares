#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ci-session-boundary.sh — make the runner able to give a session child its own user and
# network namespace, and print what the kernel says about it before the tests run.
#
# WHY IT EXISTS. A bound session and every session Git child start in a fresh user and
# network namespace (modules/sessions/egress.Wrap), and the reproducers for that boundary
# fail closed on a host that refuses it: SECURITY.md lists unprivileged user and network
# namespaces as a requirement. Ubuntu 23.10 and later refuse them to unprivileged
# processes through AppArmor (kernel.apparmor_restrict_unprivileged_userns=1), which is
# what a GitHub-hosted runner image ships. Fifteen tests of modules/sessions failed
# together in a hosted CI run; that this switch was the cause is an inference from the
# code, not a reading of that run, and the lines below are what the next run will show.
#
# WHAT IT DOES. On a GitHub-hosted runner that has the AppArmor switch set, it clears the
# switch for this single-use VM. Then, on every runner, it asks the kernel for a user and
# a network namespace, as the engine does, and prints the answer.
#
# A refused or unavailable probe fails the job before the session tests start. The
# engine's own tests still check its namespace setup and Landlock through confine.Probe.
# A self-hosted runner is never changed (no sudo is assumed), only measured. It changes
# no test and skips none.
set -euo pipefail

key=kernel.apparmor_restrict_unprivileged_userns
switch=/proc/sys/${key//.//}

if [ "${RUNNER_ENVIRONMENT:-}" = github-hosted ] && [ -r "$switch" ] && [ "$(cat "$switch")" != 0 ]; then
  sudo -n sysctl -w "$key=0" || echo "::warning::could not clear $key"
fi

echo "kernel: $(uname -r)"
echo "$key: $(cat "$switch" 2>/dev/null || echo 'not present')"
echo "lsm: $(cat /sys/kernel/security/lsm 2>/dev/null || echo 'unreadable')"

if ! command -v unshare > /dev/null; then
  echo "::error::unshare is not installed on this runner; install util-linux before running session tests"
  exit 1
fi
if ! answer=$(unshare --user --map-root-user --net true 2>&1); then
  answer=${answer//$'\n'/ }
  echo "::error::this runner refused an unprivileged user and network namespace (${answer}); configure the runner to allow session namespaces before running session tests"
  exit 1
fi
echo "unprivileged user and network namespaces: allowed (Landlock is checked by the tests)"
