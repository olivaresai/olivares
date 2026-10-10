// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// ErrToolModelListUnsupported is explicit: no read-only catalog adapter is
// available for this tool. It does not mean the tool cannot run a session.
var ErrToolModelListUnsupported = errors.New("this tool has no read-only model catalog adapter")

// ReadProfileModels asks the tool in the exact active, local profile's home. It
// sends no user input, starts no provider thread and never borrows host auth.
func (m *Module) ReadProfileModels(ctx context.Context, tenant model.TenantID, ref string) ([]string, error) {
	profile, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return nil, err
	}
	if profile.AuthSource != AuthSourceAccountHome {
		return nil, ErrToolModelListUnsupported
	}
	home, err := m.snapshotForLaunch(tenant, profile, m.ExecutionEnvironmentRef())
	if err != nil {
		return nil, err
	}
	ids, err := m.readToolModels(ctx, home)
	if err != nil {
		return nil, err
	}
	after, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return nil, err
	}
	if after.Version != profile.Version || after.State != ProfileActive {
		return nil, ErrProviderRecordChanged
	}
	return ids, nil
}

// ReadOwnToolLoginModels uses the tenant's product login without creating a
// profile just to list models. The resolver and New session use this same home.
func (m *Module) ReadOwnToolLoginModels(ctx context.Context, tenant model.TenantID, driver string) ([]string, error) {
	home, err := m.OwnToolModelHome(tenant, driver)
	if err != nil {
		return nil, err
	}
	return m.readToolModels(ctx, home)
}

// OwnToolModelHome validates the product login's paths before discovery inspects
// login metadata or launches a child. Parent symlinks cannot borrow another home.
func (m *Module) OwnToolModelHome(tenant model.TenantID, driver string) (ProviderHomeSnapshot, error) {
	userHome, configHome, ok := m.OwnToolLoginHomes(tenant, driver)
	if !ok {
		return ProviderHomeSnapshot{}, ErrToolModelListUnsupported
	}
	if err := revalidateHome("user_home", userHome); err != nil {
		return ProviderHomeSnapshot{}, err
	}
	if err := revalidateHome("config_home", configHome); err != nil {
		return ProviderHomeSnapshot{}, err
	}
	if usesServerUserLogin(driver, AuthSourceAccountHome, configHome) {
		return ProviderHomeSnapshot{}, errors.New("the server user login cannot be used")
	}
	return ProviderHomeSnapshot{Driver: driver, UserHome: userHome, ConfigHome: configHome, AuthSource: AuthSourceAccountHome}, nil
}

func (m *Module) readToolModels(ctx context.Context, home ProviderHomeSnapshot) (ids []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var program string
	var args []string
	switch home.Driver {
	case providerDriverCodex:
		program = m.driverProgram(NewCodexDriver())
		args = NewCodexDriver().LaunchArgs(DriverLaunch{})
	case providerDriverClaude:
		program = m.claudeProgram()
		if err := m.requireClaudeCatalogLogin(ctx, home, program); err != nil {
			return nil, err
		}
		// SDK initialize only, without user messages. Disable configured hooks,
		// plugins and MCP: reading a model list must not execute user automation.
		args = cliruntime.ClaudeArgs(cliruntime.LaunchRequest{PermissionMode: "dontAsk", ToolSurfaceDeclared: true})
		args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "", "--no-session-persistence", "--settings", `{"disableAllHooks":true}`)
	case providerDriverGrok:
		program = m.driverProgram(NewGrokDriver())
		args = []string{"models"}
	default:
		return nil, ErrToolModelListUnsupported
	}
	spec := LaunchSpec{Program: program, Args: args, Dir: home.UserHome,
		Env:       toSessionEnv(cliruntime.HomeEnv(home.Driver, home.UserHome, home.ConfigHome)),
		Isolation: IsolationNative, WaitDelay: time.Second,
		Confinement: m.sessionConfinement("", &home, PresetNone)}
	proc, err := m.rt.Runner.Launch(ctx, spec)
	if err != nil {
		return nil, errors.New("the tool model catalog could not be started")
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if stopErr := proc.Stop(stopCtx); stopErr != nil {
			ids, err = nil, errors.New("the model catalog process could not be stopped")
		}
	}()
	if home.Driver == providerDriverCodex {
		return readCodexModelCatalog(ctx, proc)
	}
	if home.Driver == providerDriverGrok {
		return readGrokModelCatalog(ctx, proc)
	}
	return readClaudeModelCatalog(ctx, proc)
}

// Grok's native models command also prints its reference list when signed out.
// Only a confirmed account-home login can advertise that list as available.
func readGrokModelCatalog(ctx context.Context, proc Process) ([]string, error) {
	var data []byte
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case frame, ok := <-proc.Output():
			if !ok {
				code, err := proc.Wait()
				if err != nil || code != 0 {
					return nil, errors.New("the tool model catalog command failed")
				}
				return parseGrokModelCatalog(string(data))
			}
			if frame.Stream != streamStdout {
				continue
			}
			if len(data)+len(frame.Data)+1 > 1<<20 {
				return nil, errors.New("tool model list exceeded its response bound")
			}
			data = append(data, frame.Data...)
			data = append(data, '\n')
		}
	}
}

func parseGrokModelCatalog(data string) ([]string, error) {
	signed, listed := false, false
	ids := []string{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !signed {
			const prefix = "You are logged in with "
			host := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ".")
			if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, ".") || !validToolCatalogModelID(host) {
				return nil, errors.New("the tool account is not signed in")
			}
			signed = true
			continue
		}
		if !listed {
			if line == "Available models:" {
				listed = true
				continue
			}
			if strings.HasPrefix(line, "Default model: ") && validToolCatalogModelID(strings.TrimPrefix(line, "Default model: ")) {
				continue
			}
			return nil, errors.New("invalid tool model list")
		}
		var id string
		switch {
		case strings.HasPrefix(line, "* ") && strings.HasSuffix(line, " (default)"):
			id = strings.TrimSuffix(strings.TrimPrefix(line, "* "), " (default)")
		case strings.HasPrefix(line, "- "):
			id = strings.TrimPrefix(line, "- ")
		default:
			return nil, errors.New("invalid tool model list")
		}
		if !validToolCatalogModelID(id) || len(ids) == 1000 {
			return nil, errors.New("invalid tool model list")
		}
		ids = append(ids, id)
	}
	if !signed || !listed {
		return nil, errors.New("invalid tool model list")
	}
	return ids, nil
}

func validToolCatalogModelID(id string) bool {
	return id != "" && len(id) <= 200 && strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func readCodexModelCatalog(ctx context.Context, proc Process) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	conn := newRPCConn(proc.Send, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				conn.close(ctx.Err())
				return
			case frame, ok := <-proc.Output():
				if !ok {
					conn.close(errors.New("model catalog output ended"))
					return
				}
				if frame.Stream == streamStdout {
					conn.deliver(frame.Data)
				}
			}
		}
	}()
	defer func() { cancel(); conn.close(errors.New("model catalog closed")); <-done }()
	if _, err := conn.call(ctx, codexMethodInitialize, codexInitializeParams{ClientInfo: codexClientInfo{Name: "olivares-models", Version: "1"}, Capabilities: codexInitializeCapabilities{}}, 10*time.Second); err != nil {
		return nil, err
	}
	if err := conn.notify(ctx, codexMethodInitialized, nil); err != nil {
		return nil, err
	}
	raw, err := conn.call(ctx, codexMethodAccountRead, codexAccountReadParams{}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var account codexAccountReadResponse
	if json.Unmarshal(raw, &account) != nil || len(account.Account) == 0 || string(account.Account) == "null" {
		return nil, errors.New("the tool account is not signed in")
	}
	var out []string
	cursor := ""
	for page := 0; page < 10; page++ {
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err = conn.call(ctx, "model/list", params, 10*time.Second)
		if err != nil {
			return nil, err
		}
		var result struct {
			Data []struct {
				Model  string `json:"model"`
				Hidden bool   `json:"hidden"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Data == nil {
			return nil, errors.New("invalid tool model list")
		}
		for _, md := range result.Data {
			if !validToolCatalogModelID(md.Model) {
				return nil, errors.New("invalid tool model list")
			}
			if !md.Hidden {
				out = append(out, md.Model)
			}
		}
		if len(out) > 1000 {
			return nil, errors.New("the tool model catalog exceeded its model bound")
		}
		if result.NextCursor == nil || *result.NextCursor == "" {
			return out, nil
		}
		if *result.NextCursor == cursor {
			return nil, errors.New("the tool model cursor did not advance")
		}
		cursor = *result.NextCursor
	}
	return nil, errors.New("the tool model catalog exceeded its page bound")
}

func readClaudeModelCatalog(ctx context.Context, proc Process) ([]string, error) {
	requestID := model.NewID().String()
	request, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": requestID, "request": map[string]any{"subtype": "initialize"}})
	if err := proc.Send(ctx, request); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case frame, ok := <-proc.Output():
			if !ok {
				return nil, errors.New("tool model output ended")
			}
			if frame.Stream != streamStdout {
				continue
			}
			var response struct {
				Type     string `json:"type"`
				Response struct {
					RequestID string `json:"request_id"`
					Subtype   string `json:"subtype"`
					Response  struct {
						Models []struct {
							Value string `json:"value"`
						} `json:"models"`
					} `json:"response"`
				} `json:"response"`
			}
			if json.Unmarshal(frame.Data, &response) != nil || response.Type != "control_response" || response.Response.RequestID != requestID {
				continue
			}
			if response.Response.Subtype != "success" {
				return nil, errors.New("tool model initialization failed")
			}
			if response.Response.Response.Models == nil {
				return nil, ErrToolModelListUnsupported
			}
			var out []string
			for _, md := range response.Response.Response.Models {
				if !validToolCatalogModelID(md.Value) || len(out) == 1000 {
					return nil, errors.New("invalid tool model list")
				}
				out = append(out, md.Value)
			}
			return out, nil
		}
	}
}

func (m *Module) requireClaudeCatalogLogin(ctx context.Context, home ProviderHomeSnapshot, program string) (err error) {
	spec := LaunchSpec{Program: program, Args: []string{"auth", "status", "--json"}, Dir: home.UserHome,
		Env: toSessionEnv(cliruntime.HomeEnv(home.Driver, home.UserHome, home.ConfigHome)), Isolation: IsolationNative, WaitDelay: time.Second,
		Confinement: m.sessionConfinement("", &home, PresetNone)}
	proc, err := m.rt.Runner.Launch(ctx, spec)
	if err != nil {
		return errors.New("tool sign-in status could not be read")
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if proc.Stop(stopCtx) != nil {
			err = errors.New("the tool status process could not be stopped")
		}
	}()
	var data []byte
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-proc.Output():
			if !ok {
				code, err := proc.Wait()
				if err != nil || code != 0 {
					return errors.New("tool sign-in status could not be read")
				}
				var status struct {
					LoggedIn bool `json:"loggedIn"`
				}
				if json.Unmarshal(data, &status) != nil || !status.LoggedIn {
					return errors.New("the tool account is not signed in")
				}
				return nil
			}
			if frame.Stream == streamStdout {
				data = append(data, frame.Data...)
			}
			if len(data) > 1<<20 {
				return errors.New("tool status exceeded its response bound")
			}
		}
	}
}
