#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Fast shell behavior checks; actual root exec is in the image qualifier."""
import os
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]
RC = ROOT / "packaging/container/bashrc.sh"


class ContainerShellTests(unittest.TestCase):
    def test_runtime_tools_and_shell_startup(self):
        for name in ("Dockerfile", "Dockerfile.release"):
            with self.subTest(dockerfile=name):
                runtime = (ROOT / name).read_text().rsplit("\nFROM ", 1)[1]
                install = re.search(r"apt-get install (.*?)&&", runtime, re.S)[1]
                for package in ("vim", "less", "procps", "curl", "jq", "iproute2", "tar", "gzip"):
                    self.assertIn(package, install.split())
                self.assertIn("COPY packaging/container/bashrc.sh /etc/olivares-bashrc", runtime)
                self.assertIn("1i . /etc/olivares-bashrc", runtime)
                self.assertIn("USER 65532:65532", runtime)
                self.assertIn("ENV HOME=/var/lib/olivares/home", runtime)

    def shell(self, term, interactive=True):
        self.assertTrue(RC.is_file(), "container shell defaults must be shipped")
        command = 'printf "home=%s\\n" "$HOME"; alias'
        argv = ["bash", "--noprofile", "--rcfile", str(RC), "-ic", command]
        if not interactive:
            argv = ["bash", "--noprofile", "--norc", "-c", '. "$1"; printf "home=%s\\n" "$HOME"; alias', "bash", str(RC)]
        return subprocess.run(argv, env={"PATH": os.environ["PATH"], "TERM": term, "HOME": "/var/lib/olivares/home"},
                              capture_output=True, text=True)

    def test_interactive_aliases_and_home(self):
        result = self.shell("xterm")
        self.assertEqual(result.returncode, 0, result.stderr)
        expected_home = "/root" if os.geteuid() == 0 else "/var/lib/olivares/home"
        for value in (f"home={expected_home}", "alias ll='ls -alF'", "alias l='ls -CF'",
                      "alias la='ls -A'", "alias lt='ls -altrh'", "alias ls='ls --color=auto'",
                      # SLES's alias.bash and ls.bash, adopted for both images, plus vi -> vim.
                      "alias o='less'", "alias ..='cd ..'", "alias ...='cd ../..'",
                      "alias cd..='cd ..'", "alias md='mkdir -p'", "alias rd='rmdir'",
                      "alias rehash='hash -r'", "alias dir='ls -l'", "alias ls-l='ls -l'",
                      "alias grep='grep --color=auto'", "alias egrep='grep -E --color=auto'",
                      "alias fgrep='grep -F --color=auto'", "alias vi='vim'"):
            self.assertIn(value, result.stdout)

    def test_dumb_terminal_keeps_sles_aliases_but_not_ls_colour(self):
        for term in ("dumb", ""):
            with self.subTest(term=term):
                result = self.shell(term)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn("alias ls=", result.stdout)
                # alias.bash defines the grep aliases unconditionally, and AlmaLinux's
                # /etc/profile.d/colorgrep.sh defines the same three on the appliance.
                self.assertIn("alias grep='grep --color=auto'", result.stdout)
                self.assertIn("alias lt='ls -altrh'", result.stdout)
                self.assertIn("alias vi='vim'", result.stdout)

    def test_noninteractive_shell_is_unchanged(self):
        result = self.shell("xterm", interactive=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "home=/var/lib/olivares/home\n")


if __name__ == "__main__":
    unittest.main()
