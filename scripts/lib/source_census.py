# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Read the gofmt-shaped source factory table for both connector censuses.

Only direct package constructor returns are supported. Refuse an unfamiliar
entry rather than publish an incomplete count after a wiring refactor.
"""
import re


def source_census(source: str) -> tuple[set[str], set[str], set[str]]:
    source = re.sub(r"//[^\n]*|/\*.*?\*/", "", source, flags=re.S)
    imports = {}
    for alias, path in re.findall(
        r'(?:(\w+)\s+)?"github\.com/olivaresai/olivares/connectors/([^"\n]+)"', source
    ):
        directory = path.split("/", 1)[0]
        imports[alias or path.rsplit("/", 1)[-1].replace("-", "")] = directory

    def table(name: str) -> str:
        match = re.search(r"\bvar\s+" + name + r"\s*=\s*map[^\n{]+\{(.*?)^\}",
                          source, re.M | re.S)
        if match is None:
            raise ValueError(f"missing source table: {name}")
        return match[1]

    factories = table("inProcSourceFactories")
    entries = re.findall(
        r'"([^"\n]+)"\s*:\s*func\(\)\s+sdk\.SourceConnector\s*\{\s*'
        r'return\s+(\w+)\.\w+\(\)\s*\},', factories
    )
    kinds = {kind for kind, _ in entries}
    if not kinds or len(entries) != len(re.findall(r'"[^"\n]+"\s*:', factories)):
        raise ValueError("unresolved source constructor in inProcSourceFactories")
    directories = set()
    for _, package in entries:
        if package not in imports:
            raise ValueError(f"unresolved source constructor package: {package}")
        directories.add(imports[package])

    aliases = table("inProcSourceAliases")
    entries = re.findall(r'"([^"\n]+)"\s*:\s*"([^"\n]+)"\s*,', aliases)
    if len(entries) != len(re.findall(r'"[^"\n]+"\s*:', aliases)):
        raise ValueError("unresolved source alias in inProcSourceAliases")
    for alias, canonical in entries:
        if canonical not in kinds:
            raise ValueError(f"unknown source alias target: {alias} -> {canonical}")
    kinds.update(alias for alias, _ in entries)
    all_kinds = kinds.copy()
    for name in ("buildRosterProvider", "buildContentSource"):
        match = re.search(
            r"^func " + name + r"\([^\n]+\{\n\tswitch kind \{(.*?)^\t\}",
            source, re.M | re.S,
        )
        if match is None:
            raise ValueError(f"missing connector kind switch: {name}")
        # Only the builder's direct kind cases count; nested switches and
        # quoted operands in config/mode expressions are not connector kinds.
        all_kinds.update(
            kind for case in re.findall(
                r'^\tcase ("[^"\n]+"(?:, "[^"\n]+")*):', match[1], re.M
            ) for kind in re.findall(r'"([^"\n]+)"', case)
        )
    return kinds, directories, all_kinds
