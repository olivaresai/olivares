# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Count connector capabilities separately from contract and helper libraries."""
import json
import os
from pathlib import Path

CONTRACT_LIB_DIRS = frozenset({
    "contentsource", "datasourceacl", "identitysource", "internal", "modelprovider",
    "modelrouter", "redact", "secretref", "shared", "siemsink", "threatfeed",
    "vectorindex", "voice",
})
NON_INTEGRATION_DIRS = CONTRACT_LIB_DIRS | {"interop"}  # conformance matrix, not a connector


def walk_error(error: OSError) -> None:
    raise error


def has_go(directory: Path) -> bool:
    for _, directories, files in os.walk(directory, onerror=walk_error):
        directories[:] = [name for name in directories if name not in ("node_modules", "testdata")]
        if any(name.endswith(".go") and not name.endswith("_test.go") for name in files):
            return True
    return False


def connector_census(root: str = "connectors") -> dict[str, int]:
    directories = {path.name: path for path in Path(root).iterdir()
                   if path.is_dir() and path.name != "node_modules"}
    if not directories:
        raise ValueError(f"{root} has no connector directories")
    go_dirs = {name for name, path in directories.items() if has_go(path)}
    return {
        "dirs": len(directories),
        "go": len(go_dirs),
        "nongo": len(directories) - len(go_dirs),
        "libraries": len(directories.keys() & CONTRACT_LIB_DIRS),
        "integrations": len(go_dirs - NON_INTEGRATION_DIRS),
    }


if __name__ == "__main__":
    print(json.dumps(connector_census()))
