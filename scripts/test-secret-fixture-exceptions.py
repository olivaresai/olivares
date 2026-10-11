#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise exact fixture exceptions with the real scanner in both scan modes.

Read the product's test sources so the public export needs no private Git history.
Reports stay in scratch space; diagnostics name paths and rules, never values.
"""

import base64
import collections
import hashlib
import json
import os
import re
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MARKER = "# Exact fixtures: setup, sign-in redaction, DR sealers, and plain-git transport."
END_MARKER = "# End exact fixtures."
EXPECTED = collections.Counter({
    ("cmd/olivares/cmd_setup_existing_env_test.go", "generic-api-key"): 1,
    ("cmd/olivares/internal/agenttoolsapi/signin_failure_test.go", "jwt"): 1,
    ("cmd/olivares/cmd_dr_sealer_test.go", "generic-api-key"): 4,
})
# connectors/gitpublish/plaingit_test.go is not a source here: since fcbd2917 it builds its
# fake SSH key header at run time, so the current file has no private-key capture. Its exact
# block in .gitleaks.toml stays for the history commits (78f0810e, 360a20de) that carry one.


def run(args: list[str], cwd: Path) -> subprocess.CompletedProcess:
    # Reuse the same boundary as the gate, including when this test is run
    # directly from a linked-worktree hook. The scanner invokes Git as well.
    result = subprocess.run(
        ["bash", "-c", '. "$1" || exit 2\nshift\nexec "$@"',
         "fixture-scan", str(ROOT / "scripts/lib/git-env.sh"), *args],
        cwd=cwd, capture_output=True, text=True,
    )
    if result.returncode not in (0, 1):
        raise RuntimeError(f"{args[0]} could not run (exit {result.returncode})")
    return result


def scan(work: Path, config: str, sources: dict[str, str], mode: str) -> list[dict]:
    with tempfile.TemporaryDirectory(dir=work) as scratch:
        directory = Path(scratch)
        subject = directory / "subject"
        subject.mkdir()
        cfg = directory / "gitleaks.toml"
        cfg.write_text(config)
        for name, text in sources.items():
            path = subject / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        if mode == "git":
            for args in (
                ["init", "-q"],
                ["add", "."],
                ["-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"],
            ):
                result = run(["git", *args], subject)
                if result.returncode:
                    raise RuntimeError("cannot create fixture history")
        report = directory / "report.json"
        args = ["gitleaks", mode, ".", "--no-banner", "--no-color",
                "--config", str(cfg), "--report-format", "json", "--report-path", str(report)]
        if mode == "git":
            args += ["--log-opts=HEAD"]
        result = run(args, subject)
        if re.search(r"\b(?:ERR|FTL|PNC)\b", result.stderr):
            raise RuntimeError("scanner reported an inspection error")
        findings = json.loads(report.read_text())
        if result.returncode != bool(findings):
            raise RuntimeError("scanner exit disagrees with its report")
        return findings


def check(findings: list[dict], expected: collections.Counter, label: str) -> None:
    actual = collections.Counter((f["File"], f["RuleID"]) for f in findings)
    if actual != expected:
        raise RuntimeError(f"{label}: expected {dict(expected)}, got {dict(actual)}")
    print(f"PASS {label}")


def main() -> None:
    config = Path(os.environ.get("FIXTURE_SCAN_CONFIG", ROOT / ".gitleaks.toml")).read_text()
    before, marker, after = config.partition(MARKER)
    control = before + after.partition(END_MARKER)[2] if marker else config
    sources = {path: (ROOT / path).read_text() for path, _ in EXPECTED}
    # Keep only the sealer declarations: later uses add identifier captures in
    # directory mode, while the inherited findings are the two declarations.
    sealer = "cmd/olivares/cmd_dr_sealer_test.go"
    sources[sealer] = "\n".join(line for line in sources[sealer].splitlines()
                                if line.startswith("const dr")) + "\n"
    with tempfile.TemporaryDirectory(prefix="secret-fixtures-") as scratch:
        work = Path(scratch)
        foreign = work / "foreign"
        if run(["git", "init", "-q", str(foreign)], work).returncode:
            raise RuntimeError("cannot create foreign-repository witness")
        before = {p.relative_to(foreign): p.read_bytes()
                  for p in foreign.rglob("*") if p.is_file()}
        # A standalone invocation must remain safe under hook-exported Git
        # variables. Every scratch command, including gitleaks' Git children,
        # must keep this disposable foreign repository byte-for-byte intact.
        os.environ["GIT_DIR"] = str(foreign / ".git")
        os.environ["GIT_WORK_TREE"] = str(foreign)
        # Use an ephemeral real key as the other-rule and complete-key witness.
        key = work / "probe.pem"
        result = run(["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt",
                      "rsa_keygen_bits:2048", "-out", str(key)], work)
        if result.returncode:
            raise RuntimeError("cannot generate ephemeral key witness")
        pem = key.read_text()
        for mode in ("dir", "git"):
            baseline = scan(work, control, sources, mode)
            check(baseline, EXPECTED, f"{mode}: no-exception control detects all {sum(EXPECTED.values())} captures")
            check(scan(work, config, sources, mode), collections.Counter(),
                  f"{mode}: exact fixtures are clean")
            elsewhere = {"probe/" + path: text for path, text in sources.items()}
            check(scan(work, config, elsewhere, mode),
                  collections.Counter({("probe/" + path, rule): n
                                       for (path, rule), n in EXPECTED.items()}),
                  f"{mode}: exact values on other paths remain findings")
            changed = dict(sources)
            for finding in baseline:
                path, secret = finding["File"], finding["Secret"]
                if finding["RuleID"] == "private-key":
                    changed[path] = pem
                else:
                    # Preserve the detector's shape/entropy; change one captured byte.
                    if any(tag.startswith("decoded:") for tag in finding["Tags"]):
                        continue
                    offset = len(secret) - 2 if finding["RuleID"] == "jwt" else 0
                    byte = "B" if secret[offset] != "B" else "C"
                    replacement = secret[:offset] + byte + secret[offset + 1:]
                    if path == sealer:
                        decoded = base64.b64decode(secret)
                        replacement = base64.b64encode(b"g" + decoded[1:]).decode()
                    if secret not in changed[path]:
                        raise RuntimeError("capture is absent from its source")
                    changed[path] = changed[path].replace(secret, replacement)
            check(scan(work, config, changed, mode), EXPECTED,
                  f"{mode}: changed values and a complete private key remain findings")
            probe = hashlib.sha256(b"fixture exception residual").hexdigest()
            witnesses = {path: text + f'\napi_key = "{probe}"\n' + pem
                         for path, text in sources.items()}
            check(scan(work, config, witnesses, mode),
                  collections.Counter({(path, rule): 1 for path in sources
                                       for rule in ("private-key", "generic-api-key")}),
                  f"{mode}: unrelated keys and other rules on every path remain findings")
        after = {p.relative_to(foreign): p.read_bytes()
                 for p in foreign.rglob("*") if p.is_file()}
        if after != before:
            raise RuntimeError("foreign repository was modified")
        print("PASS ambient Git variables: foreign repository is unchanged")


if __name__ == "__main__":
    main()
