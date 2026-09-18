// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WHAT THE ENGINE LEAVES BEHIND SO A SECOND PROCESS CAN ANSWER "WHERE DO I GO?".
//
// 2026-09-17, after `docker compose up --wait` on a server: the container is
// healthy, `docker exec … bash` fails because the image is distroless, and nothing
// tells the operator how to continue. The banner DID say where to go — into a log
// they were not reading, minutes earlier, possibly rotated away.
//
// So the engine writes what the banner computed into its own data directory, and
// `olivares first-boot` reads it. The data directory is the one thing a second
// process in that container is guaranteed to share with the first: it is the
// volume. No socket, no API call, no credential — a second process that could
// authenticate to the API would not need this file.
//
// WHAT IS IN IT AND WHAT IS NOT. Addresses, the bind, the transport and the
// paragraph the banner printed. NOT the setup token: the token is stored as a
// SHA-256 and the plaintext is unrecoverable by design (core/secure/setup.go),
// and writing it here to make one command prettier would undo that design. What
// first-boot does about that is its own decision, stated in cmd_firstboot.go.

// consoleStateFile is the file name inside the data directory. It sits beside
// setup.token, tls.crt and the store.
const consoleStateFile = "console.json"

// consoleStateVersion is the schema version. A reader that does not recognize the
// version says so instead of guessing: this file crosses binary versions inside a
// long-lived volume, which is exactly where a silently-misread field hurts.
const consoleStateVersion = 1

// consoleState is one boot's answer to "which addresses does this engine answer
// at". Every field is derived from what the banner already printed, so the two
// cannot disagree.
type consoleState struct {
	Version int `json:"version"`
	// BootedAt is when this file was written, so a reader can tell a live boot
	// from the residue of one that ended a week ago. It is informational: this
	// file does not prove a process is running, and first-boot says so.
	BootedAt time.Time `json:"booted_at"`
	// Browse is the address the banner printed: the declared one when the
	// operator declared one, otherwise the bind made openable.
	Browse string `json:"browse"`
	// Declared is true when Browse came from --public-url / OLIVARES_PUBLIC_URL.
	Declared bool `json:"declared"`
	// Addresses is every address the bind answers at, in the banner's order. It
	// is empty for a bind that is already an address.
	Addresses []string `json:"addresses,omitempty"`
	// Listen and GRPCListen are the binds as the operator spelled them.
	Listen     string `json:"listen"`
	GRPCListen string `json:"grpc_listen"`
	// Container records whether the writing process could SEE that it was in a
	// container. False means "could not tell", never "was not".
	Container bool `json:"container"`
	// Insecure records that this engine served plain HTTP.
	Insecure bool `json:"insecure"`
	// Advice is the banner's address paragraphs WITHOUT the address list, kept
	// verbatim so first-boot repeats the product's own words rather than
	// inventing a second set that can drift from them. The list is omitted
	// because first-boot prints the addresses itself, in its own layout.
	Advice string `json:"advice,omitempty"`
}

// newConsoleState builds the record from the resolved address. It takes the
// consoleAddress AFTER withPlan, because the advice is only complete then.
func newConsoleState(addr consoleAddress, listen, grpcListen string, insecure bool) consoleState {
	state := consoleState{
		Version:    consoleStateVersion,
		BootedAt:   time.Now().UTC(),
		Browse:     addr.URL(),
		Declared:   addr.Declared,
		Listen:     listen,
		GRPCListen: grpcListen,
		Container:  addr.Container,
		Insecure:   insecure,
		Advice:     addr.AdviceAfterAddresses,
	}
	for _, a := range addr.Reachable {
		state.Addresses = append(state.Addresses, a.Origin)
	}
	return state
}

// writeConsoleState persists the record atomically: a temporary file in the same
// directory, then a rename. A reader that opens the file mid-write would
// otherwise see a truncated JSON document and report the engine as unreadable.
//
// Mode 0600. The file carries no secret, but its directory holds the signing keys
// and the store, and a first-boot convenience is not a reason to widen anything.
func writeConsoleState(dataDir string, state consoleState) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode console state: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(dataDir, consoleStateFile+".*")
	if err != nil {
		return fmt.Errorf("create console state: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set console state mode: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write console state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close console state: %w", err)
	}
	if err := os.Rename(name, filepath.Join(dataDir, consoleStateFile)); err != nil {
		return fmt.Errorf("publish console state: %w", err)
	}
	return nil
}

// readConsoleState loads the record. A missing file is reported as such, because
// "no engine has ever started here" and "the engine wrote something unreadable"
// are different things to tell an operator.
func readConsoleState(dataDir string) (consoleState, error) {
	var state consoleState
	body, err := os.ReadFile(filepath.Join(dataDir, consoleStateFile))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return state, fmt.Errorf("the console state in %s is not readable JSON: %w", dataDir, err)
	}
	if state.Version != consoleStateVersion {
		return state, fmt.Errorf("the console state in %s was written with schema version %d and this binary reads version %d; start the engine once with this binary to refresh it",
			dataDir, state.Version, consoleStateVersion)
	}
	return state, nil
}
