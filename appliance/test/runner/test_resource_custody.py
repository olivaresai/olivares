# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
import importlib
from pathlib import Path
import tempfile
import unittest


class ResourceCustody(unittest.TestCase):
    def helper(self):
        return importlib.import_module("owned_resources")

    def test_foreign_loop_is_never_detached_or_unlinked(self):
        mod = self.helper()
        with tempfile.TemporaryDirectory() as tmp:
            backing = Path(tmp)/"probe.img"; backing.write_bytes(b"owned")
            commands = []
            def run(argv, timeout=10):
                commands.append(argv)
                if "losetup" in argv and "--json" in argv:
                    return mod.Result(0, '{"loopdevices":[{"name":"/dev/loop7","back-file":"/foreign"}]}')
                return mod.Result(0, "")
            result = mod.release_loop(run, "/dev/loop7", backing, Path(tmp)/"mount")
            self.assertFalse(result)
            self.assertTrue(backing.exists())
            self.assertFalse(any("--detach" in argv or "umount" in argv for argv in commands))

    def test_detach_failure_preserves_backing(self):
        mod = self.helper()
        with tempfile.TemporaryDirectory() as tmp:
            backing = Path(tmp)/"probe.img"; backing.write_bytes(b"owned")
            def run(argv, timeout=10):
                if "losetup" in argv and "--json" in argv:
                    import json
                    return mod.Result(0,json.dumps({"loopdevices":[{"name":"/dev/loop7","back-file":str(backing)}]}))
                if "findmnt" in argv: return mod.Result(0, "{\"filesystems\":[]}")
                return mod.Result(1, "refused")
            self.assertFalse(mod.release_loop(run,"/dev/loop7",backing,Path(tmp)/"mount"))
            self.assertTrue(backing.exists())

    def test_image_remove_failure_never_claims_cleanup(self):
        mod = self.helper()
        calls=[]
        def run(argv, timeout=10):
            calls.append(argv)
            if "inspect" in argv: return mod.Result(0,'[{"Id":"sha256:'+'a'*64+'","Config":{"Labels":{"org.olivares.appliance.attempt":"attempt"}}}]')
            return mod.Result(1,"denied")
        self.assertFalse(mod.release_image(run,"sha256:"+"a"*64,"attempt"))
        self.assertTrue(any("rm" in x for x in calls))

    def test_cli_timeout_is_not_daemon_cleanup_proof(self):
        mod=self.helper()
        self.assertFalse(mod.build_terminal(mod.Result(124,"",timed_out=True)))
        self.assertTrue(mod.build_terminal(mod.Result(0,"")))

    def test_successful_loop_release_observes_absence_before_unlink(self):
        mod=self.helper()
        import json
        with tempfile.TemporaryDirectory() as tmp:
            backing=Path(tmp)/"probe.img";backing.write_bytes(b"owned")
            detached=False
            def run(argv, timeout=10):
                nonlocal detached
                if "losetup" in argv and "--json" in argv:
                    devices=[] if detached else [{"name":"/dev/loop7","back-file":str(backing)}]
                    return mod.Result(0,json.dumps({"loopdevices":devices}))
                if "findmnt" in argv:return mod.Result(0,'{"filesystems":[]}')
                if "--detach" in argv:detached=True
                return mod.Result(0,"")
            self.assertTrue(mod.release_loop(run,"/dev/loop7",backing,Path(tmp)/"mount"))
            self.assertFalse(backing.exists())
