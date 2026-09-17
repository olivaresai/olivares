#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bounded KEY=value reader for the packaged OpenRC unit. Values are never
# executed: the file is not sourced, and a raw line is never passed to export
# in double quotes (that would expand command substitution as root).
#
# This is not systemd EnvironmentFile parsing (quotes, escapes, continuations,
# last-wins). That parser is owned by the native-layouts work. Extra serve
# flags in OLIVARES_EXTRA_ARGS are returned as a string; the caller appends
# them with globbing disabled. Nested quoting is not interpreted.

# olivares_load_env FILE
# Sets olivares_extra_args. Exports other POSIX environment names for the
# daemon process. Returns 1 on a malformed line or unusable key.
olivares_load_env() {
	_olivares_cfg=$1
	olivares_extra_args=
	[ -n "$_olivares_cfg" ] || return 1
	[ -r "$_olivares_cfg" ] || return 0
	while IFS= read -r _olivares_line || [ -n "$_olivares_line" ]; do
		case "$_olivares_line" in
		"" | "#"*) continue ;;
		*=*)
			_olivares_key=${_olivares_line%%=*}
			_olivares_val=${_olivares_line#*=}
			case "$_olivares_key" in
			"" | *[!A-Za-z0-9_]* | [0-9]*) return 1 ;;
			esac
			case "$_olivares_val" in
			\"*\")
				_olivares_val=${_olivares_val#\"}
				_olivares_val=${_olivares_val%\"}
				;;
			\'*\')
				_olivares_val=${_olivares_val#\'}
				_olivares_val=${_olivares_val%\'}
				;;
			esac
			if [ "$_olivares_key" = OLIVARES_EXTRA_ARGS ]; then
				olivares_extra_args=$_olivares_val
			else
				# Parameter expansion is not re-parsed, so $() in the
				# stored value stays literal.
				export "${_olivares_key}=${_olivares_val}" || return 1
			fi
			;;
		*) return 1 ;;
		esac
	done <"$_olivares_cfg"
	return 0
}
