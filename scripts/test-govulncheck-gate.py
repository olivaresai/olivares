#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Exercise the real gate with bounded scanner streams, without Go builds/network."""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
OSV = json.loads((ROOT / "scripts/testdata/govulncheck-cilium-osv.json").read_text())
MODULE = "github.com/cilium/cilium"
PACKAGES = [MODULE + "/api/v1/" + name for name in ("flow", "observer", "relay")]


class Gate(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="govulncheck-gate-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "scripts").mkdir()
        (self.root / "connectors").mkdir()
        (self.root / "bin").mkdir()
        (self.root / "tmp").mkdir()
        shutil.copyfile(ROOT / "scripts/govulncheck-gate.sh", self.root / "scripts/govulncheck-gate.sh")
        self.allow = (ROOT / ".govulncheck-allow.yaml").read_text()
        (self.root / ".olivares-public-export").touch()
        self.scan = "./connectors"
        self.today = "2026-10-03"

    def executable(self, name, body):
        path = self.root / "bin" / name
        path.write_text("#!/bin/sh\n" + body + "\n")
        path.chmod(0o700)
        return path

    def finding(self, package=PACKAGES[0], **changes):
        frame = {"module": MODULE, "version": "v1.20.2", "package": package,
                 "function": "ProtoReflect"}
        frame.update(changes)
        return {"osv": "GO-2026-6596", "trace": [frame]}

    def grade(self, findings, *, osv=OSV, raw=None, tool_rc=0):
        (self.root / ".govulncheck-allow.yaml").write_text(self.allow)
        self.executable("go", "printf '%s\\n' " + "'" + json.dumps({"Use": [{"DiskPath": self.scan}]}) + "'")
        self.executable("date", "printf '%s\\n' '" + self.today + "'")
        objects = [{"config": {"scanner_version": "v1.3.0"}}, {"progress": {}}]
        if osv is not None:
            objects.append({"osv": osv})
        objects += [{"finding": f} for f in findings]
        stream = self.root / "stream.json"
        stream.write_text(raw if raw is not None else "\n".join(map(json.dumps, objects)))
        scanner = self.executable("scanner", f"cat '{stream}'\nexit {tool_rc}")
        env = {"PATH": str(self.root / "bin") + os.pathsep + os.defpath,
               "TMPDIR": str(self.root / "tmp"), "GOVULNCHECK": str(scanner)}
        result = subprocess.run(["bash", str(self.root / "scripts/govulncheck-gate.sh")],
                                env=env, capture_output=True, text=True, timeout=10)
        return result.returncode, result.stdout + result.stderr

    def assertGrade(self, expected, findings, **kwargs):
        rc, out = self.grade(findings, **kwargs)
        self.assertEqual(rc, expected, out)
        self.assertIn("vuln:gate:" if rc != 2 else "refus", out.lower())

    def test_known_generated_api_findings_pass(self):
        self.assertGrade(0, [self.finding(p) for p in PACKAGES])

    def test_gateway_ingestion_blocks(self):
        self.assertGrade(1, [self.finding(MODULE + "/operator/pkg/model/ingestion")])

    def test_one_gateway_call_among_known_calls_blocks(self):
        self.assertGrade(1, [self.finding(MODULE + "/operator/pkg/model/ingestion"), self.finding()])

    def test_gateway_frame_later_in_trace_blocks(self):
        f = self.finding()
        f["trace"].append(self.finding(MODULE + "/operator/pkg/model/ingestion")["trace"][0])
        self.assertGrade(1, [f])

    def test_different_module_blocks(self):
        self.assertGrade(1, [self.finding(module="example.com/cilium")])

    def test_changed_version_blocks(self):
        self.assertGrade(1, [self.finding(version="v1.20.3")])

    def test_new_api_package_blocks(self):
        self.assertGrade(1, [self.finding(MODULE + "/api/v1/new")])

    def test_different_scan_blocks(self):
        self.scan = "./other"
        (self.root / "other").mkdir()
        self.assertGrade(1, [self.finding()])

    def test_changed_advisory_record_blocks(self):
        osv = copy.deepcopy(OSV)
        osv["modified"] = "2026-10-04T00:00:00Z"
        self.assertGrade(1, [self.finding()], osv=osv)

    def test_missing_advisory_record_blocks(self):
        self.assertGrade(1, [self.finding()], osv=None)

    def test_incomplete_scope_refuses(self):
        self.allow = "\n".join(line for line in self.allow.splitlines() if "version:" not in line)
        self.assertGrade(2, [self.finding()])

    def test_unknown_exception_kind_refuses(self):
        self.allow = self.allow.replace("kind: false-positive", "kind: typo")
        self.assertGrade(2, [self.finding()])

    def test_expired_exception_blocks(self):
        self.today = "2026-10-11"
        self.assertGrade(1, [self.finding()])

    def test_openpgp_still_blocks(self):
        f = self.finding("golang.org/x/crypto/openpgp", module="golang.org/x/crypto")
        f["osv"] = "GO-2026-5932"
        self.assertGrade(1, [f])

    def test_failed_scanner_refuses(self):
        rc, out = self.grade([], tool_rc=1)
        self.assertEqual(rc, 2, out)
        self.assertIn("the gate certifies nothing", out)

    def test_truncated_stream_refuses(self):
        self.assertGrade(2, [], raw='{"config": {}}\n{"finding":')

    def test_existing_unscoped_dated_acceptance_keeps_working(self):
        self.allow = "allow:\n  - id: GO-2026-1234\n    expires: 2026-10-10\n"
        f = self.finding()
        f["osv"] = "GO-2026-1234"
        self.assertGrade(0, [f])


if __name__ == "__main__":
    unittest.main()
