#!/sbin/openrc-run
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# shellcheck disable=SC2034 # openrc-run consumes name/command/command_args/command_user.
#
# Signed-archive OpenRC adapter. Values from the env file are not executed:
# the file is not sourced, and a raw line is never passed to export in double
# quotes. Extra flags in OLIVARES_EXTRA_ARGS are appended with globbing
# disabled and split on IFS; nested quoting is not interpreted.

name="Olivares AI"
description="Self-hosted AI governance control plane"
olivares_config="@CONFIG@"
command="@BINARY@"
# --listen=:8443 is the dual-stack wildcard: the console accepts connections from the
# network, which is what a server is for. TLS is on with a self-signed first-boot
# certificate and there are no default credentials. To restrict the engine to this
# host, put --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 in
# OLIVARES_EXTRA_ARGS in the environment file: it is appended after these flags and
# the later flag wins.
command_args_base="serve --data-dir=@DATA_DIR@ --listen=:8443 --grpc-listen=:8444 --checkpoint-interval=1h"
command_args="$command_args_base"
command_user="olivares:olivares"
command_background=true
pidfile="/run/olivares.pid"
output_log="@DATA_DIR@/olivares.log"
error_log="@DATA_DIR@/olivares.log"

depend() {
  # The listeners bind the wildcard, which succeeds before an uplink is configured;
  # `use net` keeps the ordering without making the service fail when there is none.
  use net
  after firewall
}

start_pre() {
  command_args="$command_args_base"
  olivares_extra_args=
  if [ -r "$olivares_config" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      case "$line" in
        ""|"#"*) continue ;;
        *=*)
          key=${line%%=*}
          val=${line#*=}
          case "$key" in
            ""|*[!A-Za-z0-9_]*|[0-9]*) return 1 ;;
          esac
          case "$val" in
            \"*\") val=${val#\"}; val=${val%\"} ;;
            \'*\') val=${val#\'}; val=${val%\'} ;;
          esac
          if [ "$key" = OLIVARES_EXTRA_ARGS ]; then
            olivares_extra_args=$val
          else
            export "${key}=${val}" || return 1
          fi
          ;;
        *) return 1 ;;
      esac
    done <"$olivares_config"
  fi
  set -f
  command_args="$command_args_base $olivares_extra_args"
  set +f
  return 0
}
