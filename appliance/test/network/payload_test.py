# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The guest harness sends only keys the guard's Go types decode; the driver refuses unknown fields.

No guest is started: the request builders of guest-cases.py run against a recording stub.
"""
import importlib.util
import pathlib
import re
import unittest

HERE = pathlib.Path(__file__).resolve().parent
NETGUARD = HERE.parent.parent / "layer" / "netguard"
DRIVER = HERE / "driver" / "main.go"


def struct_body(text, name):
    match = re.search(r"^type " + name + r" struct \{\n(.*?)^\}", text, re.S | re.M)
    return match.group(1) if match else None


def go_json_keys(name):
    """The JSON keys of a struct declared in the netguard package's non-test sources."""
    for source in sorted(NETGUARD.glob("*.go")):
        if source.name.endswith("_test.go"):
            continue
        body = struct_body(source.read_text(), name)
        if body is None:
            continue
        keys = set()
        for line in body.splitlines():
            tag = re.search(r'`json:"([^",]*)', line)
            if tag and tag.group(1) not in ("", "-"):
                keys.add(tag.group(1))
        if not keys:
            raise AssertionError(name + " declares no JSON keys")
        return keys
    raise AssertionError("netguard declares no struct " + name)


def driver_request_fields():
    """The untagged field names of the driver's request document (matched without case)."""
    body = struct_body(DRIVER.read_text(), "request")
    if body is None:
        raise AssertionError("the driver declares no request struct")
    fields = set()
    for line in body.splitlines():
        match = re.match(r"\s*((?:\w+\s*,\s*)*\w+)\s+\S", line.split("//")[0])
        if match:
            fields.update(part.strip().lower() for part in match.group(1).split(","))
    return fields


def load_guest_cases():
    spec = importlib.util.spec_from_file_location("guest_cases", HERE / "guest-cases.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Recorder:
    """Stands in for the guest: records every request document and answers minimally."""

    def __init__(self, cases):
        self.guest = cases.Guest.__new__(cases.Guest)
        self.sent = []
        profile = {"uuid": "11111111-1111-1111-1111-111111111111", "ipv4": {"method": "manual", "addresses": ["10.0.3.10/24"],
                   "gateway": "", "never_default": True}, "ipv6": {"method": "auto", "gateway": "", "never_default": False}}
        self.guest.observe = lambda: {"Profile": profile}
        self.guest.request = self.request

    def request(self, value, allow_error=False):
        self.sent.append(value)
        return {"status": {"state": "awaiting_confirmation"}, "digest": "b" * 64, "token": "c" * 32}, "wire-stub.json"


class HarnessPayloadKeys(unittest.TestCase):
    def setUp(self):
        self.cases = load_guest_cases()
        self.confirmation = go_json_keys("Confirmation")
        self.change = go_json_keys("Change")
        self.settings = go_json_keys("IPSettings")
        self.top = driver_request_fields()

    def check_top(self, value):
        for key in value:
            self.assertIn(key.lower(), self.top, "driver request has no field " + key)

    def test_confirm_sends_only_confirmation_keys(self):
        recorder = Recorder(self.cases)
        change = {"operation_id": "a" * 32}
        answer = {"digest": "b" * 64, "token": "c" * 32}
        variants = [{}]
        source = (HERE / "guest-cases.py").read_text()
        for literal in re.findall(r"\{'(\w+)':", source[source.index("def main"):]):
            if literal in ("token", "digest", "operation_id", "probe_witness", "confirmation_class", "class"):
                variants.append({literal: "0"})
        for updates in variants:
            recorder.guest.confirm(change, answer, **updates)
        self.assertEqual(len(recorder.sent), len(variants))
        for value in recorder.sent:
            self.check_top(value)
            unknown = set(value["Confirmation"]) - self.confirmation
            self.assertEqual(unknown, set(), "confirm sends keys the guard refuses: " + ", ".join(sorted(unknown)))
            self.assertIn("confirmation_class", value["Confirmation"])

    def test_apply_sends_only_change_and_settings_keys(self):
        recorder = Recorder(self.cases)
        recorder.guest.apply(20, dns=["10.0.3.3"])
        recorder.guest.apply(ipv6={"method": "manual", "addresses": ["fd00:33::20/64"], "gateway": "", "routes": [], "dns": [],
                                   "search": [], "never_default": True})
        for value in recorder.sent:
            self.check_top(value)
            self.assertEqual(set(value["Change"]) - self.change, set())
            for family in ("ipv4", "ipv6"):
                if family in value["Change"]:
                    self.assertEqual(set(value["Change"][family]) - self.settings, set())

    def test_keys_come_from_the_go_tags(self):
        self.assertEqual(self.confirmation, {"operation_id", "digest", "token", "confirmation_class", "probe_witness"})
        self.assertIn("confirmation", self.top)


if __name__ == "__main__":
    unittest.main()
