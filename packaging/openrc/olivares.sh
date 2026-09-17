#!/sbin/openrc-run
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# shellcheck disable=SC2034 # openrc-run consumes name/command/command_args/command_user.
#
# Packaged OpenRC unit for the .apk. It is not enabled or started by the
# package hooks. Extra flags from OLIVARES_EXTRA_ARGS are appended with
# globbing disabled and split on IFS; this is not a general quoting parser.

name="Olivares AI"
description="Self-hosted AI governance control plane"

if [ -r /usr/lib/olivares/openrc-load-env.sh ]; then
	# shellcheck disable=SC1091
	. /usr/lib/olivares/openrc-load-env.sh
else
	olivares_load_env() { return 1; }
fi

olivares_config="/etc/olivares/olivares.env"
command="/usr/bin/olivares"
command_args_base="serve --data-dir=/var/lib/olivares --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 --checkpoint-interval=1h"
command_args="$command_args_base"
command_user="olivares:olivares"
command_background=true
pidfile="/run/olivares.pid"
# start-stop-daemon opens output_log after switching to command_user.
# /var/lib/olivares/olivares.log was EACCES on Alpine 3.22 OpenRC 0.62.6 even
# after that file was olivares:olivares 0640 in a 0750 data dir, and start
# still exited 0 while status was crashed (32). Piping the real product to
# `logger -t olivares` then SIGPIPE-crashed serve when syslogd was not
# accepting the boot chatter (empty-main stub wrote nothing, so it survived).
# /var/log/olivares.log is created 0640 olivares:olivares by postinstall and
# start_pre; the first-boot token is in that file.
output_log="/var/log/olivares.log"
error_log="/var/log/olivares.log"

depend() {
	# Loopback-only listeners do not require a configured uplink. `need net`
	# refuses to start when the networking service cannot run (measured on
	# Alpine 3.22 OpenRC 0.62.6 with no NIC: rc-service start exits 1).
	use net
	after firewall
}

start_pre() {
	command_args="$command_args_base"
	olivares_extra_args=
	olivares_load_env "$olivares_config" || return 1
	set -f
	# Intentional IFS split of extra flags; nested quotes are not interpreted.
	command_args="$command_args_base $olivares_extra_args"
	set +f
	# Loopback-only listeners still need lo. `use net` does not start
	# networking, and `need net` refuses when there is no uplink (Alpine
	# 3.22 OpenRC 0.62.6, -nic none): wget to 127.0.0.1 then fails with
	# "Network unreachable".
	if command -v ip >/dev/null 2>&1; then
		ip link set lo up 2>/dev/null || true
	elif command -v ifconfig >/dev/null 2>&1; then
		ifconfig lo up 2>/dev/null || true
	fi
	mkdir -p /var/lib/olivares
	chown olivares:olivares /var/lib/olivares
	chmod 0750 /var/lib/olivares
	if [ ! -e /var/log/olivares.log ]; then
		: >/var/log/olivares.log
	fi
	chown olivares:olivares /var/log/olivares.log 2>/dev/null || true
	chmod 0640 /var/log/olivares.log
	return 0
}
