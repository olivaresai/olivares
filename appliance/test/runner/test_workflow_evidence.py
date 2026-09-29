# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


class WorkflowEvidence(unittest.TestCase):
    def test_actual_wrapper_finalizes_every_status_under_errexit(self):
        for code in (0, 1, 2, 143):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as tmp:
                # This calls the same wrapper as Actions under its actual shell options.
                program = ("import sys;sys.path.insert(0," + repr(str(HERE)) + ");import workflow;"
                           "sys.exit(workflow.execute(" + repr(tmp) + ",[sys.executable,'-c',"
                           + repr("import sys; print('observed'); sys.exit("+str(code)+")") + "]))")
                result = subprocess.run(["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", '"$@"',
                                         "wrapper", sys.executable, "-c", program], capture_output=True, text=True, timeout=5)
                # Exit zero without a real collector receipt must never qualify.
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(json.loads((Path(tmp)/"workflow-result.json").read_text())["collector_exit"], code)
                self.assertTrue((Path(tmp)/"receipt.json").is_file())
                self.assertIn("receipt.json", (Path(tmp)/"SHA256SUMS").read_text())
                self.assertLessEqual((Path(tmp)/"preflight.log").stat().st_size, 262144)

    def test_real_signal_leaves_incomplete_evidence(self):
        import workflow
        with tempfile.TemporaryDirectory() as tmp:
            code = workflow.execute(tmp, [sys.executable, "-c", "import os,signal;os.kill(os.getpid(),signal.SIGTERM)"])
            self.assertEqual(code, 1)
            outcome = json.loads((Path(tmp)/"workflow-result.json").read_text())
            self.assertEqual(outcome["collector_exit"], -15)
            self.assertFalse(outcome["receipt_classification_matches"])
            self.assertTrue((Path(tmp)/"SHA256SUMS").exists())

    def test_a_valid_receipt_preserves_classified_exit(self):
        import importlib
        workflow = importlib.import_module("workflow")
        for code in (0, 1, 2):
            with tempfile.TemporaryDirectory() as tmp:
                program = "import json,pathlib,sys;pathlib.Path("+repr(tmp)+",'receipt.json').write_text(json.dumps({'judgement':{'state':"+repr({0:'eligible',1:'defect',2:'unavailable'}[code])+"}}));sys.exit("+str(code)+")"
                self.assertEqual(workflow.execute(tmp,[sys.executable,"-c",program]), code)
