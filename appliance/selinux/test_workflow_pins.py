# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The hosted SELinux workflow builds against the image recipe's pins: the Fedora 44 container digest and the signed
selinux-policy build. recipe-pins.json records the recipe lock's entries; the workflow must equal them, and when the
recipe's lock is in the tree, recipe-pins.json must equal the lock. Its per-domain sesearch listing names every
domain the module declares and requires the one type transition into the CLI's domain.

Run: python3 -m unittest discover -s appliance/selinux -p 'test_*.py'
"""
import json
import os
import re
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
WORKFLOW = os.path.join(ROOT, ".github", "workflows", "appliance-selinux.yml")
PINS = os.path.join(HERE, "recipe-pins.json")
sys.path.insert(0, HERE)

import generate  # noqa: E402


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def pin_mismatches(workflow, pins):
    """Returns what in the workflow's text differs from the recipe pins: the container image, the Koji build
    directory of selinux-policy, and the sha256 line of selinux-policy-targeted."""
    out = []
    image = pins["base_image"]
    want_image = f"{image['registry']}:{image['reference'].split(':', 1)[1]}@{image['manifest_digest_amd64']}"
    images = re.findall(r"^\s*image:\s*(\S+)\s*$", workflow, re.M)
    if images != [want_image]:
        out.append(f"container image {images} is not the recipe's {want_image}")
    targeted = pins["selinux-policy-targeted"]
    koji_dir, rpm = targeted["koji_signed"].rsplit("/", 1)
    koji = re.findall(r"^\s*SELINUX_POLICY_KOJI:\s*(\S+)\s*$", workflow, re.M)
    if koji != [koji_dir]:
        out.append(f"SELINUX_POLICY_KOJI {koji} is not the recipe's build directory {koji_dir}")
    lines = re.findall(r"^\s*([0-9a-f]{64}) (\S+\.rpm)\s*$", workflow, re.M)
    sums = {name: sha for sha, name in lines}
    if sums.get(rpm) != targeted["sha256"]:
        out.append(f"the sha256 line of {rpm} is {sums.get(rpm)}, the recipe's is {targeted['sha256']}")
    build = "-".join(targeted["nevra"].split("-")[-2:])  # version-release.arch, e.g. 44.10-1.fc44.noarch
    for name in sums:
        if not name.endswith(f"-{build}.rpm"):
            out.append(f"{name} is not of the recipe's selinux-policy build {build}")
    return out


class WorkflowPins(unittest.TestCase):
    def setUp(self):
        self.pins = json.loads(read(PINS))
        self.workflow = read(WORKFLOW)

    def test_the_workflow_uses_the_recipe_pins(self):
        self.assertEqual(pin_mismatches(self.workflow, self.pins), [])

    def test_another_container_digest_is_a_mismatch(self):
        other = self.workflow.replace(self.pins["base_image"]["manifest_digest_amd64"], "sha256:" + "0" * 64)
        self.assertEqual(len(pin_mismatches(other, self.pins)), 1)

    def test_another_selinux_policy_sha256_is_a_mismatch(self):
        other = self.workflow.replace(self.pins["selinux-policy-targeted"]["sha256"], "f" * 64)
        self.assertEqual(len(pin_mismatches(other, self.pins)), 1)

    def test_the_pins_equal_the_recipe_lock_in_the_tree(self):
        lock = os.path.join(ROOT, self.pins["lock"])
        if not os.path.exists(lock):
            # Before the recipe's Fedora toolchain is in this tree, recipe-pins.json is the only record of its lock,
            # and nothing else in the tree may pin a Fedora builder.
            self.assertFalse(os.path.exists(os.path.dirname(lock)), "a Fedora toolchain directory without its lock")
            return
        recorded = json.loads(read(lock))
        self.assertEqual({k: recorded["base_image"][k] for k in self.pins["base_image"]}, self.pins["base_image"])
        entry = recorded["image_packages"]["selinux-policy-targeted"]
        self.assertEqual({k: entry[k] for k in self.pins["selinux-policy-targeted"]},
                         self.pins["selinux-policy-targeted"])


class WorkflowDomains(unittest.TestCase):
    def test_the_per_domain_listing_names_every_domain_of_the_module(self):
        rows = generate.read_table(os.path.join(HERE, "access.tsv"), generate.ACCESS_COLUMNS)
        domains = {r["source"] for r in rows if r["class"] == "file" and "entrypoint" in r["perms"].split()}
        loop = re.search(r"for domain in ([^;]+); do", read(WORKFLOW).replace("\\\n", " "))
        self.assertIsNotNone(loop)
        self.assertEqual(set(loop.group(1).split()), domains)


    def test_the_policy_step_shows_the_one_transition_into_the_cli(self):
        workflow = read(WORKFLOW)
        self.assertIn('sesearch -T -s unconfined_t -t olivares_cli_exec_t -c process "$policy"', workflow)
        self.assertIn("test \"$(grep -c . \"$WORK/cli-entry.txt\")\" -eq 1", workflow)
        self.assertIn("grep -qxE '\\s*type_transition unconfined_t olivares_cli_exec_t:process olivares_cli_t;\\s*' "
                      "\"$WORK/cli-entry.txt\"", workflow)


if __name__ == "__main__":
    unittest.main()
