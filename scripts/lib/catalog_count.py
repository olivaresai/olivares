# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Measure the private catalog only in an assembled Business source tree."""
import json
from pathlib import Path


def catalog_count(root=Path(".")):
    root = Path(root)
    implementation = root / "modules/compliance/frameworks.go"
    artifact = root / "compliance.catalog.json"
    if implementation.is_file():
        frameworks = json.loads(artifact.read_text())["frameworks"]
        if not isinstance(frameworks, list) or not frameworks:
            raise ValueError("private compliance catalog has no frameworks")
        ids = [framework["id"] for framework in frameworks]
        if any(not identifier for identifier in ids) or len(set(ids)) != len(ids):
            raise ValueError("private compliance catalog IDs are empty or duplicated")
        return len(frameworks)
    if artifact.exists():
        raise ValueError("private compliance catalog leaked into the Community tree")
    if not (root / "modules/compliance/views_noenterprise.go").is_file():
        raise ValueError("compliance edition boundary is missing")
    return None


if __name__ == "__main__":
    count = catalog_count()
    print(count if count is not None else "unavailable (private Business catalog)")
