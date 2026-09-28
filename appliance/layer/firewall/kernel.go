// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
)

// The fixed argument vectors of nft: the ruleset travels on standard input, never as a path,
// and the read-back lists the one table as JSON.
var (
	LoadArgs = []string{"-f", "-"}
	ListArgs = []string{"-j", "list", "table", "inet", "olivares"}
)

const (
	// nftProgram is nft by absolute path.
	nftProgram = "/usr/sbin/nft"
	// maxListing bounds nft's listing of the table.
	maxListing = 1 << 20
	// maxLoadOutput bounds what nft prints while loading.
	maxLoadOutput = 64 * 1024
)

// NFT is the kernel as nft reaches it over netlink.
type NFT struct {
	// Program is nft's absolute path; nftProgram when empty.
	Program string
}

func (n NFT) program() string {
	if n.Program == "" {
		return nftProgram
	}
	return n.Program
}

// Load loads ruleset as one nft transaction.
func (n NFT) Load(ctx context.Context, ruleset []byte) error {
	_, err := runBounded(ctx, n.program(), LoadArgs, ruleset, maxLoadOutput)
	return err
}

// Table lists the table inet olivares and reads it.
func (n NFT) Table(ctx context.Context) (Table, error) {
	out, err := runBounded(ctx, n.program(), ListArgs, nil, maxListing)
	if err != nil {
		return Table{}, err
	}
	return ParseTable(out)
}

// Exec runs a program by absolute path with fixed arguments, no shell and an empty environment,
// and returns at most 256 KiB of its standard output. It is the owner's Runner.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runBounded(ctx, name, args, nil, 256*1024)
}

// runBounded runs program with args, stdin on its standard input, no shell and an empty
// environment, discards its standard error and returns at most limit bytes of its standard
// output: more is an error, and the program is killed.
func runBounded(ctx context.Context, program string, args []string, stdin []byte, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = []string{}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if len(out) > limit {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case len(out) > limit:
		return nil, errors.New("the program's output exceeds its bound")
	case readErr != nil:
		return nil, readErr
	case waitErr != nil:
		return nil, waitErr
	}
	return out, nil
}

// ParseTable reads nft's JSON listing of the table inet olivares: the table's comment, the
// default policies of its input and forward base chains and each chain's rules. A listing
// without exactly that one table and its two base chains, each on its own hook, is refused.
func ParseTable(data []byte) (Table, error) {
	var listing struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&listing); err != nil {
		return Table{}, errors.New("not an nft listing")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Table{}, errors.New("not one nft listing")
	}
	var table Table
	tables := 0
	hooks := map[string]string{}
	for _, object := range listing.Nftables {
		if raw, ok := object["table"]; ok {
			var t struct{ Family, Name, Comment string }
			if json.Unmarshal(raw, &t) != nil {
				return Table{}, errors.New("a table the listing cannot describe")
			}
			if t.Family == "inet" && t.Name == "olivares" {
				tables++
				table.Comment = t.Comment
			}
		}
		if raw, ok := object["chain"]; ok {
			var c struct{ Family, Table, Name, Hook, Policy string }
			if json.Unmarshal(raw, &c) != nil {
				return Table{}, errors.New("a chain the listing cannot describe")
			}
			if c.Family != "inet" || c.Table != "olivares" {
				continue
			}
			switch c.Name {
			case "input":
				hooks[c.Name], table.InputPolicy = c.Hook, c.Policy
			case "forward":
				hooks[c.Name], table.ForwardPolicy = c.Hook, c.Policy
			}
		}
		if raw, ok := object["rule"]; ok {
			var r struct{ Family, Table, Chain string }
			if json.Unmarshal(raw, &r) != nil {
				return Table{}, errors.New("a rule the listing cannot describe")
			}
			if r.Family != "inet" || r.Table != "olivares" {
				continue
			}
			switch r.Chain {
			case "input":
				table.InputRules++
			case "forward":
				table.ForwardRules++
			}
		}
	}
	if tables != 1 || hooks["input"] != "input" || hooks["forward"] != "forward" {
		return Table{}, errors.New("the listing does not hold the one table inet olivares with its input and forward base chains")
	}
	return table, nil
}
