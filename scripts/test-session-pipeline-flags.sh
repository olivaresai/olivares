#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

# The parser contract can be checked without starting an engine.
set -eu
bin="${1:?usage: test-session-pipeline-flags.sh BINARY}"
"$bin" session start --wait --json --repository https://example.com/repo.git --repository-ref main --cost-cap-micro-usd 1000 --timeout 1m --help >/dev/null
echo "session pipeline flags: PASS"
