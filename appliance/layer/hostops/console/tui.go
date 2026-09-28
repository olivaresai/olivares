// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
)

var categories = []string{"Status", "Network", "Services", "Packages", "Updates", "Storage", "System", "License and modules", "Apps", "Tools and accounts", "Support"}

// TUI is a renderer and navigation frame over one Session. It never submits an
// effect, and refresh reads the same operation API as the command-line renderer.
type TUI struct {
	session        Session
	page, category int
	descriptors    []hostops.Descriptor
	tasks          []hostops.Descriptor
	descriptor     hostops.Descriptor
	plan           hostops.Plan
	view           hostops.View
	message        string
}

func NewTUI(session Session) *TUI {
	ui := &TUI{session: session}
	descriptors, err := session.Descriptors("tui")
	if err != nil {
		ui.message = "consumer_unavailable"
		return ui
	}
	// The same validation that protects web and CLI also protects terminal lists.
	catalog, err := hostops.NewCatalog(descriptors)
	if err != nil {
		ui.message = "input_refused"
		return ui
	}
	ui.descriptors = catalog.Descriptors()
	return ui
}

// Handle advances one explicit user choice. Confirm stays closed until the
// owning authority and each module's mutating consumer are installed.
func (u *TUI) Handle(command string) (bool, error) {
	fields := strings.Fields(command)
	if len(fields) == 1 && fields[0] == "q" {
		return true, nil
	}
	if len(fields) == 2 && fields[0] == "op" && validID(fields[1]) {
		return false, u.readOperation(fields[1])
	}
	if command == "refresh" && u.page == 7 {
		return false, u.readOperation(u.view.Record.OperationID)
	}
	if command == "back" {
		if u.page == 7 {
			u.page = 0
		} else if u.page > 0 {
			u.page--
		}
		return false, nil
	}
	if command == "confirm" && u.page == 6 {
		u.message = "act_not_adopted"
		return false, &localclient.Refused{Code: u.message}
	}
	if command == "next" {
		switch u.page {
		case 2:
			u.page = 3
		case 3:
			plan, err := u.session.Plan(u.descriptor.Module, u.descriptor.Verb, "tui", json.RawMessage(`{}`))
			if err != nil {
				u.message = "consumer_unavailable"
				return false, err
			}
			u.plan = plan
			u.page = 4
		case 4:
			u.page = 5
		case 5:
			u.page = 6
		default:
			return false, errors.New("input_refused")
		}
		u.message = ""
		return false, nil
	}
	n, err := strconv.Atoi(command)
	if err == nil && n > 0 {
		if u.page == 0 && n <= len(categories) {
			u.category = n - 1
			u.tasks = nil
			for _, d := range u.descriptors {
				if d.Category == categories[u.category] {
					u.tasks = append(u.tasks, d)
				}
			}
			u.page = 1
			return false, nil
		}
		if u.page == 1 && n <= len(u.tasks) {
			u.descriptor = u.tasks[n-1]
			u.page = 2
			return false, nil
		}
	}
	u.message = "input_refused"
	return false, errors.New(u.message)
}
func (u *TUI) readOperation(id string) error {
	view, _, err := u.session.Operation(id, "tui")
	if err != nil {
		u.message = "consumer_unavailable"
		return err
	}
	u.view = view
	u.page = 7
	u.message = ""
	return nil
}

// Frame is exactly 80 columns by 24 rows, with no colors or escape sequences.
// Host values are rendered as printable ASCII so terminal width is predictable.
func (u *TUI) Frame() string {
	title := "Categories"
	var lines []string
	switch u.page {
	case 0:
		for i, c := range categories {
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, c))
		}
	case 1:
		title = "Tasks - " + categories[u.category]
		for i, d := range u.tasks {
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, d.Title))
		}
		if len(lines) == 0 {
			lines = []string{"No tasks are installed in this category."}
		}
	case 2:
		title = "Detail"
		lines = []string{u.descriptor.Title, u.descriptor.Consequence, "Command: " + u.descriptor.EquivalentCommand, "Confirmation: " + u.descriptor.Confirmation}
		lines = append(lines, u.descriptor.Preconditions...)
	case 3:
		title = "Inputs"
		lines = []string{u.descriptor.Title, "The installed read-only task has no input fields.", "Next reads its plan; no host setting changes."}
	case 4:
		title = "Plan"
		lines = []string{u.plan.Descriptor.Title, u.plan.Descriptor.Consequence, "Command: " + u.plan.Descriptor.EquivalentCommand, "Changes: disabled (" + u.plan.Permissions.Code + ")"}
	case 5:
		title = "Summary"
		lines = []string{u.plan.Descriptor.Title, u.plan.Descriptor.Consequence, "Confirmation: " + u.plan.Descriptor.Confirmation, "Command: " + u.plan.Descriptor.EquivalentCommand}
	case 6:
		title = "Confirmation"
		lines = []string{"Changes are disabled: " + u.plan.Permissions.Code, "Reads and plans remain available.", "A terminal login alone does not authorize an operation."}
	case 7:
		title = "Operation"
		r := u.view.Record
		lines = []string{"ID: " + r.OperationID, "Task: " + r.TaskID, "Target: " + r.Target, "State: " + r.State, "Mode: " + r.Mode, "Changes: disabled (" + u.view.Permissions.Code + ")", r.LogRef}
		lines = append(lines, r.Postconditions...)
	}
	rows := make([]string, 24)
	rows[0] = "+" + strings.Repeat("-", 78) + "+"
	rows[1] = row("Appliance Console - " + title)
	rows[2] = row("")
	for i := 3; i < 22; i++ {
		text := ""
		if i-3 < len(lines) {
			text = lines[i-3]
		}
		rows[i] = row(text)
	}
	if u.message != "" {
		rows[21] = row(u.message)
	}
	rows[22] = row("number | next | back | op <id> | refresh | confirm | q")
	rows[23] = "+" + strings.Repeat("-", 78) + "+"
	return strings.Join(rows, "\n") + "\n"
}
func row(text string) string {
	text = strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, text)
	if len(text) > 76 {
		text = text[:73] + "..."
	}
	return "| " + text + strings.Repeat(" ", 76-len(text)) + " |"
}

func runTUI(ctx context.Context, session Session, input io.Reader, out, diagnostic io.Writer) int {
	ui := NewTUI(session)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 256), 1024)
	for {
		if _, err := io.WriteString(out, ui.Frame()); err != nil {
			return 2
		}
		if ctx.Err() != nil {
			return 2
		}
		if !scanner.Scan() {
			if scanner.Err() != nil {
				return reportError(diagnostic, scanner.Err(), false)
			}
			return 0
		}
		quit, _ := ui.Handle(scanner.Text())
		if quit {
			return 0
		}
	}
}
