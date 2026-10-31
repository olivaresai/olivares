# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.

"""Deterministic test-name assignment for the existing CI shard readers."""

import hashlib
import re
from collections.abc import Sequence


def assign(domain: str, shards: Sequence[str], tests: Sequence[str]) -> dict[str, list[str]]:
    """Assign every test exactly once, without moving it when another test is added."""
    if not domain:
        raise ValueError("the assignment domain is empty")
    if not shards or any(not shard for shard in shards):
        raise ValueError("at least one named shard is required")
    if len(set(shards)) != len(shards):
        raise ValueError("shard names must be unique")
    if len(set(tests)) != len(tests):
        raise ValueError("the test inventory contains a duplicate row")
    if any(not test for test in tests):
        raise ValueError("the test inventory contains an empty name")

    assigned = {shard: [] for shard in shards}
    for test in tests:
        digest = hashlib.sha256(domain.encode("utf-8") + b"\0" + test.encode("utf-8")).digest()
        shard = shards[int.from_bytes(digest[:8], "big") % len(shards)]
        assigned[shard].append(test)
    return {shard: sorted(names) for shard, names in assigned.items()}


def expression(tests: Sequence[str]) -> str:
    """Select exactly these top-level names, including every subtest they own."""
    if not tests:
        return ""
    return "^(" + "|".join(re.escape(test) for test in sorted(tests)) + ")$"
