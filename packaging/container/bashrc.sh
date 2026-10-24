# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# The administrator's shell, shared by both images (dev#2650): the Docker image sources this file as
# /etc/olivares-bashrc from Debian's /etc/bash.bashrc, and the appliance installs it unchanged as
# /etc/profile.d/olivares-shell.sh, which AlmaLinux's /etc/profile sources for every login shell
# (a non-login interactive shell reaches /etc/bashrc only through a ~/.bashrc, which a fresh appliance
# root may lack, so the build measures the login shell). Change it here once; the build compares the copy.
case $- in
    *i*) ;;
    *) return ;;
esac

# docker exec -u root inherits the image's non-root HOME. Correct it before
# bash tries to read that user's private .bashrc; non-root account homes stay put.
if [ "$EUID" = 0 ]; then
    export HOME=/root
fi

# ll, l and la keep the Docker image's values since dev#2035. o, .., ..., cd.., md, rd, rehash,
# dir, ls-l and the coloured grep family are the conveniences of openSUSE/aaa_base's
# files/usr/etc/profile.d/alias.bash and ls.bash, adopted for both images; alias.bash defines
# the grep aliases unconditionally, and AlmaLinux's own /etc/profile.d/colorgrep.sh defines the
# same three, so only ls keeps this file's terminal check.
if [ "${TERM:-dumb}" != dumb ]; then
    alias ls='ls --color=auto'
else
    # AlmaLinux's stock colorls.sh sets this on a tty without looking at TERM; drop it so both
    # images behave alike on a dumb terminal.
    unalias ls 2>/dev/null || :
fi
alias grep='grep --color=auto'
alias egrep='grep -E --color=auto'
alias fgrep='grep -F --color=auto'
alias ll='ls -alF'
alias l='ls -CF'
alias la='ls -A'
alias lt='ls -altrh'
alias o='less'
alias ..='cd ..'
alias ...='cd ../..'
alias cd..='cd ..'
alias md='mkdir -p'
alias rd='rmdir'
alias rehash='hash -r'
alias dir='ls -l'
alias ls-l='ls -l'
# vim-enhanced is installed; the alias gives an interactive shell the full editor. Aliases do not expand in
# `sudo vi` or in scripts: vim-minimal's /usr/bin/vi serves those.
alias vi='vim'
