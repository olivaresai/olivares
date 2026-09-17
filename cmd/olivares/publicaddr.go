// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/webaddr"
)

// THE ONE FACT THIS FILE OWNS: the address a browser reaches this console at.
//
// Until now the product had no way to be told it. The panels printed the BIND
// spelling — the flagship Compose install prints ":8443", so the first-boot
// banner read "Console: https://:8443", an address no browser can open, and seven
// tutorials told the reader to copy it out of the logs. The WebAuthn relying
// party, meanwhile, came from whatever Host header arrived, unless BOTH
// environment keys were set, in which case they were used without a single
// predicate ever being applied to them.
//
// One declared fact, parsed once, decides both: what the panel prints and what
// the relying party is. The parsing and the classification live in core/webaddr
// so the engine, the panel and the console cannot disagree about them.
//
// PRECEDENCE, and it is deliberately boring:
//
//	--public-url        explicit flag, INCLUDING an explicit empty value
//	OLIVARES_PUBLIC_URL environment, when the flag was not given
//	(neither)           no declared address; today's behavior, unchanged
//
// "Including an explicit empty value" is the half that is easy to get wrong.
// `--public-url=` on a host whose environment file sets OLIVARES_PUBLIC_URL must
// CLEAR it, not be ignored as if it were absent — so presence is carried
// separately from value, and a caller passes the flag's Changed bit rather than
// its emptiness.
//
// There is no stored schema and no hot reload: a change takes a restart, and the
// flag's help says so.

// publicURLEnv is the environment spelling of --public-url.
const publicURLEnv = "OLIVARES_PUBLIC_URL"

// publicAddrSource records WHERE the declared address came from. It exists for
// the startup log line and for the tests that prove explicit clearing works; it
// is never sent to a client.
type publicAddrSource uint8

const (
	// publicAddrUnset: no address was declared by flag or environment.
	publicAddrUnset publicAddrSource = iota
	// publicAddrFlag: --public-url was given, possibly with an empty value.
	publicAddrFlag
	// publicAddrEnv: OLIVARES_PUBLIC_URL supplied it.
	publicAddrEnv
)

func (s publicAddrSource) String() string {
	switch s {
	case publicAddrFlag:
		return "--public-url"
	case publicAddrEnv:
		return publicURLEnv
	default:
		return "unset"
	}
}

// resolvePublicAddr applies the precedence above and parses the winning value
// exactly once.
//
// flagSet is the flag's PRESENCE, not its emptiness: that is what makes
// `--public-url=` clear an environment setting instead of falling through to it.
func resolvePublicAddr(flagValue string, flagSet bool, getenv func(string) string) (webaddr.Address, publicAddrSource, error) {
	if flagSet {
		// The flag was GIVEN, so it decides — and an empty value decides too: this
		// instance declares no browser address, whatever the environment says. That
		// is the only way to clear a value baked into a systemd environment file
		// without editing the file.
		addr, err := webaddr.Parse("--public-url", flagValue)
		if err != nil {
			return webaddr.Address{}, publicAddrFlag, err
		}
		return addr, publicAddrFlag, nil
	}
	raw := getenv(publicURLEnv)
	if strings.TrimSpace(raw) == "" {
		return webaddr.Address{}, publicAddrUnset, nil
	}
	addr, err := webaddr.Parse(publicURLEnv, raw)
	if err != nil {
		return webaddr.Address{}, publicAddrEnv, err
	}
	return addr, publicAddrEnv, nil
}

// webAuthnPlan is what this instance will do about passkeys, decided once at boot
// and carried to the API construction.
type webAuthnPlan struct {
	// RP is the relying party to pin. Zero means "derive it per request", which is
	// the historical single-node default and stays the behavior when nothing at
	// all is configured.
	RP auth.WebAuthnRP
	// Unusable states that an address WAS declared and cannot be a relying party.
	// It is not a fallback to per-request derivation: see resolveWebAuthnRP.
	Unusable bool
	// Source names the input that decided this, for the startup log line.
	Source string
}

// resolveWebAuthnRP decides the relying party from the two explicit environment
// keys and the declared console address.
//
//  1. Either OLIVARES_WEBAUTHN_RPID or OLIVARES_WEBAUTHN_ORIGINS non-empty
//     -> BOTH must be present and valid, or the engine refuses to start.
//     An explicit authentication configuration never falls back silently, and
//     a valid explicit pair always beats a declared address: an operator who
//     pinned a registrable parent domain meant it.
//  2. Neither set, an address declared and usable as a relying party
//     -> derive from it.
//  3. Neither set, an address declared and NOT usable (an IP, a single-label
//     name, a host that is not a valid domain)
//     -> EXPLICITLY UNAVAILABLE. The address is still printed and is still a fine
//     way to reach the console; it is simply not authority for a passkey, and
//     substituting the request's Host here would pick an authority the operator
//     never chose.
//  4. Nothing declared
//     -> per-request derivation, exactly as before.
//
// The error is a startup configuration error and carries no value: not the
// relying-party ID, not the origins. It is returned before anything mutable
// happens on the host.
func resolveWebAuthnRP(addr webaddr.Address, addrSource publicAddrSource, getenv func(string) string) (webAuthnPlan, error) {
	id := strings.TrimSpace(getenv("OLIVARES_WEBAUTHN_RPID"))
	rawOrigins := getenv("OLIVARES_WEBAUTHN_ORIGINS")
	var origins []string
	for _, o := range strings.Split(rawOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	// PRESENCE IS JUDGED ON THE RAW VALUE, not on what survives parsing. An
	// operator who wrote OLIVARES_WEBAUTHN_ORIGINS=",," declared the key and named
	// no origin; treating that as "unset" would do exactly what this whole rule
	// exists to stop — quietly deriving an authentication authority they did not
	// choose, from a typo.
	originsDeclared := strings.TrimSpace(rawOrigins) != ""
	if id != "" || originsDeclared {
		if id == "" || !originsDeclared {
			return webAuthnPlan{}, fmt.Errorf(
				"OLIVARES_WEBAUTHN_RPID and OLIVARES_WEBAUTHN_ORIGINS must be set together: "+
					"half a relying party pins nothing, and this engine will not guess the other half. "+
					"Set both, or clear both to derive the relying party from %s (or from each request). "+
					"The values are not shown: an origin can carry an internal host name and this message is written to this process's log",
				publicURLEnv)
		}
		if len(origins) == 0 {
			return webAuthnPlan{}, errors.New(
				"OLIVARES_WEBAUTHN_ORIGINS is set and names no origin: it is separators and blanks only. " +
					"Give the exact origins the console is served on, comma-separated, or clear BOTH keys. " +
					"The value is not shown: an origin can carry an internal host name and this message is written to this process's log")
		}
		rp, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
			ID: id, DisplayName: strings.TrimSpace(getenv("OLIVARES_WEBAUTHN_RP_NAME")), Origins: origins,
		})
		if err != nil {
			return webAuthnPlan{}, fmt.Errorf(
				"OLIVARES_WEBAUTHN_RPID/OLIVARES_WEBAUTHN_ORIGINS: %w. "+
					"Correct the pair, or clear BOTH keys before restarting; an explicit relying party is never replaced by a derived one", err)
		}
		return webAuthnPlan{RP: rp, Source: "OLIVARES_WEBAUTHN_RPID"}, nil
	}
	if addr.IsZero() {
		return webAuthnPlan{Source: "per-request"}, nil
	}
	// NAME THE INPUT THAT ACTUALLY SUPPLIED IT. This used to say
	// OLIVARES_PUBLIC_URL whichever way the address arrived, so the one line an
	// operator reads at boot to find out where a value came from could send them
	// to an environment file when the answer was a flag on the command line.
	source := addrSource.String()
	if rp, ok := auth.WebAuthnRPFromAddress(addr); ok {
		return webAuthnPlan{RP: rp, Source: source}, nil
	}
	return webAuthnPlan{Unusable: true, Source: source}, nil
}

// logWebAuthnPlan states the decision once, at boot, where an operator diagnosing
// a refused ceremony will find it. This is CONFIGURATION logging and is the only
// place the resolved relying-party ID is written: the client-facing 503 says
// nothing about it, and the ceremony legs keep the logging rules they had.
func (p webAuthnPlan) log(log *slog.Logger) {
	switch {
	case p.Unusable:
		log.Warn("webauthn: the declared console address cannot be a relying party, so passkey ceremonies are refused with a 503 until the console is reached by a name that can be one",
			"source", p.Source)
	case p.RP.ID != "":
		log.Info("webauthn: relying party pinned", "source", p.Source, "rp_id", p.RP.ID, "origins", len(p.RP.Origins))
	default:
		log.Info("webauthn: no relying party configured; deriving it per request from the proxy-aware external URL", "source", p.Source)
	}
}
