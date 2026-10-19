#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Write the four self-contained demo tool executables into a directory.

A session child runs confined and can read only its own program, so each executable is
demo-tool.py with its tool name and conversations.json embedded.

usage: build-demo-tools.py <output directory>
"""
import json
import os
import sys

HERE = os.path.dirname(os.path.realpath(__file__))
TOOLS = ("claude", "codex", "gemini", "opencode")


def build(out_dir):
    with open(os.path.join(HERE, "demo-tool.py"), encoding="utf8") as fh:
        source = fh.read()
    with open(os.path.join(HERE, "conversations.json"), encoding="utf8") as fh:
        conversations = json.load(fh)
    os.makedirs(out_dir, exist_ok=True)
    for tool in TOOLS:
        header = "import sys\nsys.argv.insert(1, %r)\nEMBEDDED_CONVERSATIONS = %r\n" % (
            tool, conversations)
        # The shebang stays on line one; the header follows it.
        first, _, rest = source.partition("\n")
        path = os.path.join(out_dir, tool)
        with open(path, "w", encoding="utf8") as fh:
            fh.write(first + "\n" + header + rest)
        os.chmod(path, 0o755)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    build(sys.argv[1])
