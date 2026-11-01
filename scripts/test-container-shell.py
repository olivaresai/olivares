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
                install = " ".join(re.findall(r"apt-get install (.*?)&&", runtime, re.S))
                for package in ("vim", "less", "procps", "curl", "jq", "iproute2", "tar", "gzip",
                                "gh", "openssh-client", "netcat-openbsd"):
                    self.assertIn(package, install.split())
                # A confined session's ssh reaches a declared host through the session
                # proxy's CONNECT tunnel (modules/sessions/egress), and only then.
                self.assertIn('Match exec "printenv HTTPS_PROXY | grep -q ."', runtime)
                self.assertIn('ProxyCommand nc -X connect -x "$(echo "$HTTPS_PROXY" | cut -d/ -f3)" %h %p', runtime)
                # The main file: ssh refuses an included file that the session's user namespace
                # shows as owned by nobody.
                self.assertIn(">> /etc/ssh/ssh_config\n", runtime)
                # ssh percent-expands a ProxyCommand and refuses a token it does not know, so
                # the only percent signs are its own %h and %p.
                command = re.search(r"ProxyCommand (.*?)'", runtime)[1]
                self.assertEqual(re.findall(r"%.", command), ["%h", "%p"])
                self.assertNotIn("ssh_config.d", runtime)
                self.assertIn("COPY packaging/container/bashrc.sh /etc/olivares-bashrc", runtime)
                self.assertIn("1i . /etc/olivares-bashrc", runtime)
                self.assertIn("USER 65532:65532", runtime)
                self.assertIn("ENV HOME=/var/lib/olivares/home", runtime)

    def test_gh_comes_from_githubs_signed_repository(self):
        # Debian trixie's gh 2.46.0 is built with go1.23.4 and carries its Go advisories.
        keyring = "/usr/share/keyrings/githubcli-archive-keyring.gpg"
        for name in ("Dockerfile", "Dockerfile.release"):
            with self.subTest(dockerfile=name):
                # One logical line per shell command: the continuation backslashes joined.
                runtime = re.sub(r"\s*\\\n\s*", " ", (ROOT / name).read_text().rsplit("\nFROM ", 1)[1])
                installs = re.findall(r"apt-get install (.*?)&&", runtime, re.S)
                self.assertEqual(len(installs), 2, "Debian packages first, then gh from GitHub's repository")
                self.assertNotIn("gh", installs[0].split())
                self.assertEqual(installs[1].split(), ["-y", "--no-install-recommends", "gh"])
                github = runtime.split(installs[0], 1)[1].split(installs[1], 1)[0]
                # Between the two installs: the keyring checked by content, then the source naming it.
                self.assertIn("https://cli.github.com/packages/githubcli-archive-keyring.gpg", github)
                self.assertIn(f'"$(sha256sum {keyring})" = '
                              f'"6084d5d7bd8e288441e0e94fc6275570895da18e6751f70f057485dc2d1a811b  {keyring}"', github)
                self.assertIn("'URIs: https://cli.github.com/packages'", github)
                self.assertIn(f"'Signed-By: {keyring}'", github)
                self.assertNotIn("trusted=yes", runtime)
                # Without the source, apt would fall back to Debian's gh silently.
                self.assertIn("dpkg-query -W -f='${Version}' gh", runtime)
                self.assertIn('dpkg --compare-versions "$v" ge 2.103.3', runtime)

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
