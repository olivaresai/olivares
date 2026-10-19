# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Sourced first by Debian's /etc/bash.bashrc, before bash reads ~/.bashrc.
case $- in
    *i*) ;;
    *) return ;;
esac

# docker exec -u root inherits the image's non-root HOME. Correct it before
# bash tries to read that user's private .bashrc; non-root account homes stay put.
if [ "$EUID" = 0 ]; then
    export HOME=/root
fi

if [ "${TERM:-dumb}" != dumb ]; then
    alias ls='ls --color=auto'
fi
alias ll='ls -alF'
alias l='ls -CF'
alias la='ls -A'
alias lt='ls -altrh'
