# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import collector
import fixtures
import judge
from owned_resources import LABEL, Result


class CollectorContract(unittest.TestCase):
    def test_failed_accelerator_history_survives_fallback(self):
        def probe(argv, accelerator, machine, status, timeout):
            return {"performed":True,"proved":accelerator=="tcg","timed_out":accelerator=="kvm",
                    "cleaned":True,"selected":accelerator,"qmp_status":"prelaunch","qmp_running":False}
        result=collector.accelerator_trials(fixtures.lock(),probe)
        self.assertEqual(result["selected"],"tcg")
        self.assertEqual([x["selected"] for x in result["attempts"]],["kvm","tcg"])
        self.assertTrue(result["attempts"][0]["timed_out"])
        self.assertGreater(result["attempts"][0]["elapsed_ms"],0)

    def test_unreleased_accelerator_stops_fallback(self):
        calls=[]
        def probe(argv, accelerator, machine, status, timeout):
            calls.append(accelerator)
            return {"proved":False,"cleaned":False,"selected":accelerator}
        collector.accelerator_trials(fixtures.lock(),probe)
        self.assertEqual(calls,["kvm"])

    def test_current_context_positive_and_each_mismatch(self):
        receipt=fixtures.receipt()
        receipt["binding"]={"source_commit":"c"*40,"build_input_digest":"d"*64,"run_id":"11","run_attempt":"2","job":"image"}
        receipt["capacity"]["after_toolchain"]=True
        receipt["observations"]["container_toolchain"]["image_id"]="sha256:"+"e"*64
        receipt["observations"]["accelerator"]["elapsed_ms"]=1
        measured=dict(receipt["observations"]["accelerator"], requested=["kvm"])
        receipt["observations"]["accelerator"]["attempts"]=[measured]
        expected=dict(receipt["binding"], attempt_id=receipt["attempt"]["id"],lock_digest=fixtures.FIXTURE_LOCK_DIGEST,
                      architecture="x86_64", accelerator="kvm",image_id="sha256:"+"e"*64)
        self.assertEqual(judge.judge(fixtures.lock(),receipt,expected_context=expected).state,"eligible")
        for key in expected:
            wrong=dict(expected);wrong[key]="different"
            with self.subTest(key=key):
                self.assertEqual(judge.judge(fixtures.lock(),receipt,expected_context=wrong).state,"defect")

    def test_container_probe_uses_id_and_retains_only_after_observed_stop(self):
        image="sha256:"+"e"*64
        cid="f"*64
        attempt="owned"
        calls=[]
        with tempfile.TemporaryDirectory() as tmp:
            def command(argv, timeout=10):
                calls.append(argv)
                if "build" in argv:
                    Path(argv[argv.index("--iidfile")+1]).write_text(image)
                    return Result(0,"")
                if argv[1:3]==["image","inspect"]:
                    return Result(0,json.dumps([{"Id":image,"Architecture":"amd64","Config":{"Labels":{LABEL:attempt}}}]))
                if "create" in argv:return Result(0,cid)
                if "start" in argv:return Result(0,"kiwi-ng 11.0.3\n"+json.dumps({"kiwi_version":"11.0.3","dependency_check":True,"cli_help_check":True,"imports_check":True,"dpkg_inventory":["fixture"],"python_inventory":["fixture"],"build_provenance":{"lock_sha256":hashlib.sha256(json.dumps(fixtures.lock(),indent=2,sort_keys=True).encode()+b"\n").hexdigest()}}))
                if argv[1:3]==["container","inspect"]:
                    return Result(0,json.dumps([{"Id":cid,"Config":{"Labels":{LABEL:attempt}},"State":{"Running":False,"ExitCode":0}}]))
                return Result(0,"")
            result=collector.container_probe(fixtures.lock(),Path(tmp),attempt,True,command)
        self.assertTrue(result["proved"])
        self.assertTrue(result["cleaned"])
        self.assertTrue(result["image_retained"])
        self.assertFalse(result["image_removed"])
        created=next(x for x in calls if "create" in x)
        self.assertIn(image,created)
        self.assertNotIn(fixtures.lock()["invocation"]["image_tag"],created)

    def test_uncertain_create_cannot_claim_cleanup(self):
        image="sha256:"+"e"*64
        with tempfile.TemporaryDirectory() as tmp:
            def command(argv, timeout=10):
                if "build" in argv:
                    Path(argv[argv.index("--iidfile")+1]).write_text(image)
                    return Result(0,"")
                if argv[1:3]==["image","inspect"]:
                    return Result(0,json.dumps([{"Id":image,"Architecture":"amd64","Config":{"Labels":{LABEL:"owned"}}}]))
                if "create" in argv:return Result(124,"",timed_out=True)
                return Result(0,"")
            result=collector.container_probe(fixtures.lock(),Path(tmp),"owned",False,command)
        self.assertFalse(result["cleaned"])
        self.assertFalse(result["proved"])
