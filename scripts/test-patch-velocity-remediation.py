#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
"""Keep remediation reachable after a failed scan without alerting on cancellation."""

import io
import json
import os
from pathlib import Path
import re
import tempfile
import unittest
import urllib.error
import urllib.request
from unittest import mock

import yaml


class RemediationConditionTest(unittest.TestCase):
    def setUp(self) -> None:
        root = Path(__file__).resolve().parents[1]
        workflow = yaml.safe_load((root / ".github/workflows/patch-velocity.yml").read_text())
        self.steps = workflow["jobs"]["rescan"]["steps"]
        self.step, = (step for step in self.steps
                      if step.get("name") == "open remediation issue on findings")

    def test_remediation_overrides_implicit_success_and_preserves_findings_gate(self) -> None:
        # GitHub adds success() unless a status function is present. Empty scanner
        # output after a skipped verdict must still count as an unverified scan.
        # failure() also covers Trivy SARIF upload, syft download and SBOM failures
        # after govuln/base/scanners have all reported clean results.
        condition = self.step["if"].removeprefix("${{").removesuffix("}}").strip()
        self.assertEqual(condition,
                         "!cancelled() && vars.CI_NIGHTLIES == 'true' && "
                         "(failure() || steps.govuln.outputs.found == 'true' || "
                         "steps.base.outcome != 'success' || "
                         "steps.base.outputs.drift == 'true' || "
                         "steps.scanners.outputs.found != 'false')")

    def test_issue_commands_select_repository_without_checkout(self) -> None:
        # Step-level GH_REPO applies to list, comment and both create paths even
        # when checkout failed before creating .git. Do not infer from the cwd.
        self.assertEqual(self.step["env"].get("GH_REPO"), "${{ github.repository }}")

    def test_sarif_uploads_follow_the_code_scanning_probe(self) -> None:
        # Code Security is off on this private repository, and upload-sarif then fails the run
        # (run 38479702180). Both uploads must come after the probe and read its output, so a
        # renamed output or a dropped gate cannot bring the failure back or skip uploads silently.
        names = [step.get("name") for step in self.steps]
        probe = self.steps[names.index("code scanning available?")]
        self.assertEqual(probe.get("id"), "code-scanning")
        self.assertIn('"available={\'true\' if available else \'false\'}', probe["run"])
        self.assertIn('"GITHUB_OUTPUT"', probe["run"])
        for upload in ("upload grype SARIF", "upload trivy SARIF"):
            self.assertLess(names.index("code scanning available?"), names.index(upload))
            self.assertEqual(self.steps[names.index(upload)].get("if"),
                             "steps.code-scanning.outputs.available == 'true'")

    def test_no_step_needs_the_gh_cli(self) -> None:
        # The self-hosted runner class has no gh: the probe died with "gh: command not found"
        # in run 38790970818, and the remediation step would die the same way as soon as a
        # scan has findings. Any `gh` token outside a comment fails, whatever its flags or
        # subcommand, and whether a shell or the embedded python spells it.
        gh_word = re.compile(r"(?<![\w./-])[\"']?gh[\"']?(?![\w-])")
        offenders = []
        for step in self.steps:
            code = "\n".join(line for line in step.get("run", "").splitlines()
                             if not line.lstrip().startswith("#"))
            if gh_word.search(code):
                offenders.append(step.get("name"))
        self.assertEqual(offenders, [])

    def probe_answer(self, urlopen: mock.Mock) -> tuple[int, str, str]:
        # Run the probe's own python, as bash feeds it, against a canned API answer.
        probe = self.steps[[s.get("name") for s in self.steps].index("code scanning available?")]
        source = re.search(r"python3 - <<'PY'\n(.*?)\nPY\n", probe["run"], re.S).group(1)
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "github_output"
            out.touch()
            env = {"GH_TOKEN": "t", "GH_REPO": "example/repo", "GITHUB_OUTPUT": str(out)}
            log = io.StringIO()
            code = 0
            with mock.patch.dict(os.environ, env), \
                 mock.patch.object(urllib.request, "urlopen", urlopen), \
                 mock.patch("sys.stdout", log):
                try:
                    exec(compile(source, "probe", "exec"), {})
                except SystemExit as exit_:
                    code = exit_.code or 0
            return code, out.read_text().strip(), log.getvalue()

    @staticmethod
    def refused(code: int, message: str) -> mock.Mock:
        body = io.BytesIO(json.dumps({"message": message}).encode())
        err = urllib.error.HTTPError("u", code, "x", {}, body)
        return mock.Mock(side_effect=err)

    def test_probe_skips_only_on_the_code_scanning_refusal(self) -> None:
        # Skipping uploads is allowed for exactly one answer. Any other failure must fail the
        # job, or a 401 or a 500 would silently turn the SARIF uploads off.
        code, out, _ = self.probe_answer(mock.Mock())
        self.assertEqual((code, out), (0, "available=true"))
        for message in ("Code Security must be enabled for this repository to use code scanning.",
                        "Code scanning is not enabled for this repository."):
            code, out, log = self.probe_answer(self.refused(403, message))
            self.assertEqual((code, out), (0, "available=false"), message)
            self.assertIn("::notice::", log)
        for failure in (self.refused(403, "Resource not accessible by integration"),
                        self.refused(401, "Bad credentials"),
                        self.refused(500, "Server Error"),
                        mock.Mock(side_effect=OSError("Temporary failure in name resolution"))):
            code, out, log = self.probe_answer(failure)
            self.assertEqual((code, out), (1, ""))
            self.assertIn("::error::", log)

    def test_remediation_comments_on_the_open_tracking_issue_of_an_earlier_day(self) -> None:
        # The title carries the date. A new issue per day would bury the one a human must act on.
        source = re.search(r"python3 - <<'PY'\n(.*?)\nPY\n", self.step["run"], re.S).group(1)
        env = {"GH_TOKEN": "t", "GH_REPO": "example/repo", "body": "b",
               "title": "patch-velocity: remediation needed (2026-10-15)"}
        listing = [{"number": 8, "title": "patch-velocity: remediation needed (2026-10-15)",
                    "pull_request": {}},
                   {"number": 7, "title": "patch-velocity: remediation needed (2026-10-06)"}]

        def answer(request, timeout=None):
            reply = listing if request.get_method() == "GET" else {"html_url": "https://x/1"}
            return mock.MagicMock(__enter__=lambda s: s, __exit__=lambda s, *a: None,
                                  read=lambda: json.dumps(reply).encode())

        with mock.patch.dict(os.environ, env), \
             mock.patch.object(urllib.request, "urlopen", side_effect=answer) as urlopen, \
             mock.patch("sys.stdout", io.StringIO()):
            exec(compile(source, "issue", "exec"), {})
        posts = [c.args[0] for c in urlopen.call_args_list if c.args[0].get_method() == "POST"]
        self.assertEqual([p.full_url.rsplit("/repos/", 1)[1] for p in posts],
                         ["example/repo/issues/7/comments"])


if __name__ == "__main__":
    unittest.main()
