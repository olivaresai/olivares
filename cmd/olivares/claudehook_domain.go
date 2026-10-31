// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// hookSELinuxContext reports whether SELinux is enabled for this process and, if so,
// its context (user:role:type:level). The platform files define selinuxContext;
// tests replace this variable.
var hookSELinuxContext = selinuxContext

// hookDomain is the Server's SELinux domain for the PEP hook client (appliance/selinux, HOOK-*).
const hookDomain = "olivares_hook_t"
const containerHookDomain = "olivares_container_hook_t"

var hookDomains = map[string]bool{hookDomain: true, containerHookDomain: true}

const hookDomainsText = "olivares_hook_t or olivares_container_hook_t"

// inHookDomain reports whether this process runs in the hook domain. With SELinux enabled,
// an unreadable or empty context is treated as the hook domain with an error, so both callers deny closed.
func inHookDomain() (bool, error) {
	enabled, context, err := hookSELinuxContext()
	if !enabled {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if context == "" {
		return true, fmt.Errorf("empty SELinux context")
	}
	fields := strings.SplitN(context, ":", 4)
	return len(fields) >= 3 && hookDomains[fields[2]], nil
}

// hookDomainArgs refuses, in the hook domain, every invocation but `olivares claude-hook`.
// The domain is entered by running this program from the command domain whatever its
// arguments, so a codex-hook or the confinement helper would otherwise run with the hook
// domain's network grant. main calls it before anything else runs. An unreadable context
// denies every other command too: EACCES/EPERM never means "not in the domain".
func hookDomainArgs(args []string) error {
	inDomain, err := inHookDomain()
	if len(args) > 0 && args[0] == "claude-hook" {
		return nil
	}
	if err != nil {
		return exitcode.New(exitcode.Usage, fmt.Errorf("SELinux is enabled but this process's context cannot be read (%v): every command but `olivares claude-hook` is refused before it runs (deny closed)", err))
	}
	if !inDomain {
		return nil
	}
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	return exitcode.New(exitcode.Usage, fmt.Errorf("in %s this program runs only as `olivares claude-hook`; %q is refused before it runs (deny closed)", hookDomainsText, first))
}

// hookDomainEndpoint refuses, in the hook domain only, a PEP endpoint whose host is not
// loopback. SELinux lets that domain connect to an ephemeral-range port and cannot check
// the address; in that domain only claude-hook runs (hookDomainArgs), and it checks its
// endpoint here before it dials. With SELinux enabled and the context unreadable
// it refuses too (deny closed). Without SELinux, or in any other domain, it does nothing:
// a PEP behind an ingress keeps working.
func hookDomainEndpoint(endpoint string) error {
	inDomain, err := inHookDomain()
	if err != nil {
		return fmt.Errorf("SELinux is enabled but this process's context cannot be read (%v): the hook is refused (deny closed)", err)
	}
	if !inDomain {
		return nil
	}
	host := ""
	if u, perr := url.Parse(endpoint); perr == nil {
		host = u.Hostname()
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("in %s the PEP endpoint must be on loopback (127.0.0.1, ::1 or localhost), not %q: refused without connecting (deny closed)", hookDomainsText, host)
}
