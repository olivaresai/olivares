// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/olivaresai/olivares/core/secret"
)

// Redacted returns the snapshot as a read shows it: every environment value, URL
// userinfo and query values, and argument values under a credential name become
// secret.Redacted. The shape is unchanged. A write that sends a marker back where
// the read put it keeps the stored value (keepRedacted).
func (s MCPGatewaySnapshot) Redacted() MCPGatewaySnapshot {
	s.Servers = slices.Clone(s.Servers)
	for i := range s.Servers {
		in := &s.Servers[i].MCPGatewayServerInput
		in.URL, in.Command, in.Args = secret.RedactValue(in.URL), secret.RedactValue(in.Command), secret.RedactArgs(in.Args)
		if in.Env != nil {
			env := make(map[string]string, len(in.Env))
			for name := range in.Env {
				env[name] = secret.Redacted
			}
			in.Env = env
		}
	}
	return s
}

var mcpCommandEnvPrefix = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// refuseNamedCredentials is the save-time check of what a roster may not gain: a
// value under a credential name or URL userinfo anywhere in the url, command,
// arguments or env, or a command that sets its own environment. It runs on writes
// only: a server saved before it still loads, and its values read redacted.
func refuseNamedCredentials(in MCPGatewayServerInput) error {
	refuse := func(where string) error {
		return fmt.Errorf("%w: %s holds a credential; store it as a secret and use a secret reference (env_secret_refs or credential_ref, store:mcp/<name>) instead", ErrMCPGatewayInvalid, where)
	}
	if secret.ContainsNamedCredential(in.URL) {
		return refuse("the url")
	}
	if secret.ContainsNamedCredential(in.Command) {
		return refuse("the command")
	}
	if mcpCommandEnvPrefix.MatchString(in.Command) {
		return fmt.Errorf("%w: the command sets environment variables; put public values in env and credentials in env_secret_refs as a secret reference", ErrMCPGatewayInvalid)
	}
	if i := secret.CredentialArgument(in.Args); i >= 0 {
		return refuse(fmt.Sprintf("argument %d", i+1))
	}
	for _, name := range slices.Sorted(maps.Keys(in.Env)) {
		value := in.Env[name]
		if value != secret.Redacted && secret.IsCredentialName(name) || secret.ContainsNamedCredential(value) {
			if !mcpEnvName.MatchString(name) {
				name = "an entry"
			}
			return refuse("env " + name)
		}
	}
	return nil
}

// keepRedacted puts back each stored value a write returned as its redacted read,
// and refuses a marker with no stored value reading that way behind it, so the
// marker is never saved as a value. old is the zero input on create.
func keepRedacted(in *MCPGatewayServerInput, old MCPGatewayServerInput) error {
	refuse := func(where string) error {
		return fmt.Errorf("%w: %s holds %s but no stored value reads that way; send the value itself", ErrMCPGatewayInvalid, where, secret.Redacted)
	}
	var ok bool
	if in.URL, ok = secret.KeepRedacted(in.URL, old.URL, secret.RedactValue(old.URL)); !ok {
		return refuse("the url")
	}
	if in.Command, ok = secret.KeepRedacted(in.Command, old.Command, secret.RedactValue(old.Command)); !ok {
		return refuse("the command")
	}
	if in.Args != nil {
		args, read := slices.Clone(in.Args), secret.RedactArgs(old.Args)
		for i := range args {
			stored, storedRead := "", ""
			if i < len(old.Args) {
				stored, storedRead = old.Args[i], read[i]
			}
			if args[i], ok = secret.KeepRedacted(args[i], stored, storedRead); !ok {
				return refuse(fmt.Sprintf("argument %d", i+1))
			}
		}
		in.Args = args
	}
	if in.Env != nil {
		env := maps.Clone(in.Env)
		for name, value := range env {
			stored, has := old.Env[name]
			storedRead := ""
			if has {
				storedRead = secret.Redacted
			}
			if env[name], ok = secret.KeepRedacted(value, stored, storedRead); !ok {
				return refuse("env " + name)
			}
		}
		in.Env = env
	}
	return nil
}
