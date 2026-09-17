# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# shellcheck shell=bash
#
# aws-estate-compiled-modcache.sh — prepare the pinned go.mod/go.sum graph of
# the selected cloud/control-plane copy so compiled estate witnesses can run
# GOWORK=off GOPROXY=off. Sourced; not executed.
#
# ⛔ POR QUÉ EXISTE. `scripts/test-aws-estate.sh` copies the real
# `cloud/control-plane` module and runs four compiled router/server
# witnesses with `GOWORK=off GOPROXY=off`. That offline step never proved
# the selected module's pinned graph was in the module cache. An empty
# cache fails at OpenTelemetry imports with `[setup failed]` and
# `module lookup disabled by GOPROXY=off`. That is fixture setup, not the
# expected mutant 404. Warm caches conceal the gap.
#
# Preparation is script-local and shared by every caller of the compiled
# fixture. It never writes into the caller's GOMODCACHE. It does not unset
# GOPROXY=off to hide a missing graph. If the graph cannot be made available
# offline, the caller must fail as NOT MEASURED and must not credit a
# witness as passed.

# aws_estate_goproxy_forbids_download
# True when the caller set GOPROXY so that no proxy or "direct" remains.
# Unset GOPROXY is Go's default (network allowed) and is not a forbid.
# GOPROXY=off,direct still has a usable fallback and is not a forbid.
aws_estate_goproxy_forbids_download() {
	# Distinguish unset from empty: empty is not a usable proxy either, but
	# it is not the documented "off" policy. Only "off" with no usable
	# token forbids download.
	[ -n "${GOPROXY+x}" ] || return 1
	local _rest="$GOPROXY" _tok _saw_off=0 _saw_usable=0
	while [ -n "$_rest" ]; do
		_tok="${_rest%%,*}"
		if [ "$_tok" = "$_rest" ]; then
			_rest=""
		else
			_rest="${_rest#*,}"
		fi
		case "$_tok" in
		"") ;;
		off) _saw_off=1 ;;
		*) _saw_usable=1 ;;
		esac
	done
	[ "$_saw_off" = 1 ] && [ "$_saw_usable" = 0 ]
}

# _aws_estate_go_in_module <moddir> <gomodcache> <goproxy|inherit> <go-args...>
# goproxy "inherit" leaves the caller's GOPROXY alone (Go default if unset).
# "off" and file:// URLs override. Does not write outside gomodcache.
_aws_estate_go_in_module() {
	local _moddir="$1" _cache="$2" _proxy="$3"
	shift 3
	[ -d "$_moddir" ] || return 1
	[ -n "$_cache" ] || return 1
	mkdir -p "$_cache" || return 1
	(
		cd "$_moddir" || exit 1
		export GOWORK=off
		export GOTOOLCHAIN=local
		export GO111MODULE=on
		export GOMODCACHE="$_cache"
		export GOFLAGS="-mod=readonly"
		if [ "$_proxy" != "inherit" ]; then
			export GOPROXY="$_proxy"
		fi
		if [ -n "${GOSUMDB+x}" ]; then
			:
		elif [ "$_proxy" = "off" ] || [ "${_proxy#file://}" != "$_proxy" ]; then
			# Offline prove and file:// copies must not consult sum.golang.org.
			# go.sum already pins the selected graph.
			export GOSUMDB=off
		fi
		go "$@"
	)
}

# _aws_estate_modcache_offline_ok <moddir> <gomodcache>
# The pinned graph is available with GOPROXY=off in that cache.
_aws_estate_modcache_offline_ok() {
	_aws_estate_go_in_module "$1" "$2" "off" mod download
}

# aws_estate_prepare_compiled_modcache <moddir> [dest-gomodcache]
#
# Populate dest with the module's pinned go.mod/go.sum graph, then prove it
# serves `go mod download` with GOPROXY=off. Prints dest on stdout.
# Returns 0 if prepared; 2 if NOT MEASURED (stderr names the reason).
#
# Order:
#   1. dest already proven offline
#   2. copy from the caller's GOMODCACHE via file:// (no public network)
#   3. if the caller forbade download: stop, NOT MEASURED
#   4. go mod download into dest with the caller's GOPROXY
#   5. prove dest offline, or NOT MEASURED
aws_estate_prepare_compiled_modcache() {
	local _moddir="$1" _dest="${2:-}" _inherited="" _dl="" _dl_abs=""
	if [ -z "$_moddir" ] || [ ! -f "$_moddir/go.mod" ] || [ ! -f "$_moddir/go.sum" ]; then
		echo "compiled witnesses NOT MEASURED: selected module has no pinned go.mod/go.sum" >&2
		return 2
	fi
	if ! command -v go >/dev/null 2>&1; then
		echo "compiled witnesses NOT MEASURED: no Go toolchain to prepare the pinned module graph" >&2
		return 2
	fi
	if [ -z "$_dest" ]; then
		_dest="$(mktemp -d "${TMPDIR:-/workspace/.olivares-tmptest}/aws-estate-modcache.XXXXXX")" || {
			echo "compiled witnesses NOT MEASURED: cannot create an owned module cache" >&2
			return 2
		}
	fi
	if ! mkdir -p "$_dest"; then
		echo "compiled witnesses NOT MEASURED: cannot write owned module cache $_dest" >&2
		return 2
	fi

	if _aws_estate_modcache_offline_ok "$_moddir" "$_dest"; then
		printf '%s\n' "$_dest"
		return 0
	fi

	_inherited="$(go env GOMODCACHE 2>/dev/null || true)"
	if [ -n "$_inherited" ] && [ "$_inherited" != "$_dest" ]; then
		_dl="$_inherited/cache/download"
		if [ -d "$_dl" ]; then
			_dl_abs="$(readlink -f "$_dl" 2>/dev/null || true)"
			if [ -n "$_dl_abs" ]; then
				# Local file proxy: reuse zips already on disk without writing
				# them back into the caller's cache and without a public fetch.
				if _aws_estate_go_in_module "$_moddir" "$_dest" "file://${_dl_abs}" mod download \
					&& _aws_estate_modcache_offline_ok "$_moddir" "$_dest"; then
					printf '%s\n' "$_dest"
					return 0
				fi
			fi
		fi
	fi

	if aws_estate_goproxy_forbids_download; then
		echo "compiled witnesses NOT MEASURED: GOPROXY=${GOPROXY-} and the pinned module graph is not available offline" >&2
		return 2
	fi

	if ! _aws_estate_go_in_module "$_moddir" "$_dest" "inherit" mod download; then
		echo "compiled witnesses NOT MEASURED: go mod download of the pinned go.mod/go.sum graph failed" >&2
		return 2
	fi
	if ! _aws_estate_modcache_offline_ok "$_moddir" "$_dest"; then
		echo "compiled witnesses NOT MEASURED: pinned graph downloaded but GOPROXY=off cannot use it" >&2
		return 2
	fi
	printf '%s\n' "$_dest"
	return 0
}
