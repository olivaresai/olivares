// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// What a department carries (#530): the standards every launch of it starts from.
//
// They are session templates assigned by source-scoping bindings (source types "instructions"
// and "template"; modules/sourcescope, standards.go) to a department of the organization tree or
// to a directory group: an assignment on a department reaches its sub-departments, one on a group
// reaches its members, and a forbid holds. This module only asks and obeys, as it does for profile
// and tool assignments.
//
//   - The custom instructions of the templates assigned as instructions are given to a Claude
//     launch, a department's before a group's and a parent department's first, ahead of anything
//     the launch's own template says. The same text is the managed instructions file of the
//     department's workstations (DepartmentInstructions), so a session and a workstation read
//     one source.
//   - The template assigned as default governs a launch that names none; the last assignment in
//     that order wins (a group's after a department's, a deeper department's after its parent's).
//     A launch that names a template keeps it: a default is a start, not a control. A resume
//     keeps the template and the instruction templates its run was launched with, whoever
//     resumes it, and a run of a Claude tool records them (standards_refs).
//
// A launch belongs to the departments of its caller: the organization-tree departments the
// caller's membership or acting agent or directory groups place them in, and the caller's own
// groups. A caller with none, or a build with no assignments wired, launches exactly as before.

// DepartmentAssignments answers which departments (workspace slugs) a caller belongs to, and
// which session templates are assigned as instructions or as the default template to the
// caller's groups and to those departments, the least specific first. A zero principal is the
// departments alone, which is how a workstation's file is rendered. IsAssigned says whether any
// department or group is given a template, and DepartmentExists whether a department is one. An
// error is a refusal: a launch is never started without standards it could not read.
type DepartmentAssignments interface {
	DepartmentsOf(ctx context.Context, tenant model.TenantID, principal auth.Principal) ([]string, error)
	AssignedTo(ctx context.Context, tenant model.TenantID, principal auth.Principal, departments []string, kind string) ([]string, error)
	IsAssigned(ctx context.Context, tenant model.TenantID, template string) (bool, error)
	DepartmentExists(ctx context.Context, tenant model.TenantID, department string) (bool, error)
}

// The assignment kinds AssignedTo is asked about; they are the source types the assignments are
// stored under.
const (
	assignedInstructions = "instructions"
	assignedTemplate     = "template"
)

// maxDepartmentInstructions bounds the composed instructions a launch passes the tool as one
// argument (a template's own are bounded by maxTemplateInstructions on top).
const maxDepartmentInstructions = 32 << 10

// launchStanding is who a launch is for as far as standards go: its caller and the departments
// the caller belongs to.
type launchStanding struct {
	principal   auth.Principal
	departments []string
}

// label names the standing in a refusal.
func (st launchStanding) label() string {
	if len(st.departments) == 0 {
		return "your groups"
	}
	return "department " + strings.Join(st.departments, ", ")
}

// askStanding is the caller's standing, asked once per launch and bound to the authenticated
// principal. Nil when no assignments are wired.
func (m *Module) askStanding(ctx context.Context, tenant model.TenantID, principal auth.Principal) func() (launchStanding, error) {
	if m.rt.Standards == nil {
		return nil
	}
	return sync.OnceValues(func() (launchStanding, error) {
		departments, err := m.rt.Standards.DepartmentsOf(ctx, tenant, principal)
		return launchStanding{principal: principal, departments: departments}, err
	})
}

// standingOfLaunch is the standing of the launch's caller, nil when no one is asking (an
// in-process launch) or no assignments are wired.
func (p *CreateRunParams) standingOfLaunch() (*launchStanding, error) {
	if p.asks.standing == nil {
		return nil, nil
	}
	st, err := p.asks.standing()
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// errStandardsUnreadable refuses a launch whose department standards could not be read: 503,
// whatever the cause, so a failed read is never an empty department.
func (m *Module) errStandardsUnreadable(err error) error {
	m.warnf("sessions: department standards could not be read", "err", redactErr(err))
	return &runErr{http.StatusServiceUnavailable, "department standards could not be read"}
}

// applyDepartmentDefaultTemplate names the default template of the caller's standing on a
// launch that names none and is not a resume (a resume re-reads the template its run was
// launched under). It reports whether it did, so a refusal can say where the template came from.
func (m *Module) applyDepartmentDefaultTemplate(ctx context.Context, tenant model.TenantID, p *CreateRunParams, st *launchStanding) (bool, error) {
	if st == nil || p.resuming || strings.TrimSpace(p.TemplateID) != "" {
		return false, nil
	}
	refs, err := m.rt.Standards.AssignedTo(ctx, tenant, st.principal, st.departments, assignedTemplate)
	if err != nil {
		return false, m.errStandardsUnreadable(err)
	}
	if len(refs) == 0 {
		return false, nil
	}
	p.TemplateID = refs[len(refs)-1] // the nearest assignment is the last
	return true, nil
}

// errDefaultTemplate refuses a launch under a default template it cannot honor. The default
// restricts, so the launch is refused rather than started without the restriction, and the
// refusal names whose default it is and what is wrong with it. A failure that is not about the
// template (a store that could not be read) is a 503, as everywhere else.
func (m *Module) errDefaultTemplate(st *launchStanding, err error) error {
	status, why := http.StatusUnprocessableEntity, ""
	var re *runErr
	switch {
	case errors.As(err, &re):
		status, why = re.status, re.msg
	case errors.Is(err, store.ErrNotFound):
		why = "it does not exist"
	default:
		return m.errStandardsUnreadable(err)
	}
	return &runErr{status, "the default template of " + st.label() + " cannot govern this launch (" + why +
		"); name a template or ask an administrator to assign another default"}
}

// attributedToDefault names the default template as the cause of a refusal that its terms led to
// (a tool that cannot take its instructions or its tool list), so the remedy is in the message.
func (m *Module) attributedToDefault(defaulted bool, st *launchStanding, err error) error {
	var re *runErr
	if defaulted && errors.As(err, &re) && (re.status == http.StatusBadRequest || re.status == http.StatusUnprocessableEntity) {
		return m.errDefaultTemplate(st, err)
	}
	return err
}

// applyDepartmentInstructions gives a Claude launch the instructions its standing carries, ahead
// of the launch's own, and remembers which templates they came from. A resume and a queued launch
// keep the templates their run was launched with (their text is read again), whoever resumes it:
// the standards belong to the session, not to the person who continues it. The other tools are
// not given them (Claude Code first; their managed instruction files are not verified); the run
// records no templates then.
func (m *Module) applyDepartmentInstructions(ctx context.Context, tenant model.TenantID, p *CreateRunParams, st *launchStanding) error {
	if p.ProviderHome == nil || p.ProviderHome.Driver != providerDriverClaude {
		return nil
	}
	refs, label := p.standardsRefs, "this run"
	fromRun := p.resuming || len(refs) > 0
	if !fromRun {
		if st == nil {
			return nil
		}
		var err error
		if refs, err = m.rt.Standards.AssignedTo(ctx, tenant, st.principal, st.departments, assignedInstructions); err != nil {
			return m.errStandardsUnreadable(err)
		}
		label = st.label()
	}
	text, used, err := m.composeInstructions(ctx, tenant, label, refs, fromRun)
	if err != nil {
		return err
	}
	p.standardsRefs = used
	if text == "" {
		return nil
	}
	if own := p.Instructions; own != "" {
		text += "\n" + own // the text ends with a newline: a blank line between the two
	}
	p.Instructions = text
	return nil
}

// ErrUnknownDepartment is the answer for the instructions of a department that does not exist.
var ErrUnknownDepartment = &runErr{http.StatusNotFound, "no department with that name"}

// DepartmentInstructions is the text a department carries as its instructions: the custom
// instructions of the templates assigned to it and the departments above it, parent first, each
// under a heading naming its template. It is the bytes a launch of the department's people is
// given when nothing else is assigned to them, and the bytes of the department's managed
// instructions file, so the two cannot differ. "" when the department carries none or no
// assignments are wired; ErrUnknownDepartment when it does not exist.
func (m *Module) DepartmentInstructions(ctx context.Context, tenant model.TenantID, department string) (string, error) {
	if m.rt.Standards == nil || department == "" {
		return "", nil
	}
	known, err := m.rt.Standards.DepartmentExists(ctx, tenant, department)
	if err != nil {
		return "", m.errStandardsUnreadable(err)
	}
	if !known {
		return "", ErrUnknownDepartment
	}
	st := launchStanding{departments: []string{department}}
	refs, err := m.rt.Standards.AssignedTo(ctx, tenant, st.principal, st.departments, assignedInstructions)
	if err != nil {
		return "", m.errStandardsUnreadable(err)
	}
	text, _, err := m.composeInstructions(ctx, tenant, st.label(), refs, false)
	return text, err
}

// composeInstructions is the text of the assigned instruction templates, each under a heading
// naming it, and the templates that gave some. A template with no text gives nothing. One that is
// archived or gone is a refusal, not a silent gap: it is still assigned, so the department's
// standard would be missing without a word. The exception is a run's own record (fromRun): an
// archived template there was taken off its department first (archiving is refused while it is
// assigned), so unless someone assigned it again it is no one's standard now and the run continues
// without it. The composed text is bounded as one argument.
func (m *Module) composeInstructions(ctx context.Context, tenant model.TenantID, label string, refs []string, fromRun bool) (string, []string, error) {
	var sections, used []string
	for _, ref := range refs {
		tpl, err := m.readTemplate(ctx, tenant, ref)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, &runErr{http.StatusUnprocessableEntity, "the instructions template " + ref + " assigned to " + label +
				" does not exist; unassign it or restore it"}
		}
		if err != nil {
			return "", nil, m.errStandardsUnreadable(err)
		}
		if tpl.ArchivedAt != "" {
			if fromRun {
				stillAssigned, err := m.isAssigned(ctx, tenant, model.ID(ref))
				if err != nil {
					return "", nil, err
				}
				if !stillAssigned {
					continue
				}
			}
			return "", nil, &runErr{http.StatusUnprocessableEntity, "the instructions template " + strconv.Quote(tpl.Name) + " assigned to " + label +
				" is archived; unassign it or restore it"}
		}
		if tpl.Body.Settings == nil {
			continue
		}
		text := strings.TrimSpace(tpl.Body.Settings.CustomInstructions)
		if text == "" {
			continue
		}
		if strings.ContainsRune(text, 0) || strings.ContainsRune(tpl.Name, 0) {
			return "", nil, &runErr{http.StatusUnprocessableEntity, "the instructions template " + strconv.Quote(tpl.Name) +
				" contains a NUL byte, which cannot be passed as a process argument"}
		}
		heading := strings.Join(strings.Fields(tpl.Name), " ") // one line, whatever the name holds
		sections = append(sections, "# "+heading+"\n\n"+text+"\n")
		used = append(used, ref)
	}
	if len(sections) == 0 {
		return "", nil, nil
	}
	composed := strings.Join(sections, "\n")
	if len(composed) > maxDepartmentInstructions {
		return "", nil, &runErr{http.StatusUnprocessableEntity, "the instructions of " + label + " are " +
			strconv.Itoa(len(composed)) + " bytes, over the " + strconv.Itoa(maxDepartmentInstructions) +
			"-byte budget a launch passes to the tool; shorten them"}
	}
	return composed, used, nil
}

// RequireAdminToChangeAssigned refuses a change to a template that a department or group is
// assigned unless the principal administers templates: what the template says, or carries (the
// skills pinned to it), is then what a whole department is given, which the permission to write
// templates was never meant to cover. The template's own edit and the skills routes that write its
// pins both ask it. nil when the change may go ahead.
func (m *Module) RequireAdminToChangeAssigned(ctx context.Context, tenant model.TenantID, p auth.Principal, id model.ID) error {
	assigned, err := m.isAssigned(ctx, tenant, id)
	if err != nil {
		return err
	}
	if !assigned {
		return nil
	}
	if m.WorkAuthorizer != nil && m.WorkAuthorizer.Authorize(ctx, auth.Request{
		Principal: p, Tenant: tenant, Permission: permTemplateAdmin, Resource: auth.ResourceFor(permTemplateAdmin),
	}).Allow {
		return nil
	}
	return forbiddenErr("this template is assigned to a department or group; changing it needs template administration")
}

// isAssigned says whether a department or group is assigned the template. With no assignments
// wired nothing is; a failed read is the 503 of every standards read, never "no".
func (m *Module) isAssigned(ctx context.Context, tenant model.TenantID, id model.ID) (bool, error) {
	if m.rt.Standards == nil {
		return false, nil
	}
	assigned, err := m.rt.Standards.IsAssigned(ctx, tenant, id.String())
	if err != nil {
		return false, m.errStandardsUnreadable(err)
	}
	return assigned, nil
}

// refuseToArchiveAssigned stops a template that a department or group is assigned from being
// archived: every launch it governs would be refused until someone unassigned it, so the archive
// says to do that first. nil when the archive may go ahead.
func (m *Module) refuseToArchiveAssigned(ctx context.Context, tenant model.TenantID, id model.ID) error {
	assigned, err := m.isAssigned(ctx, tenant, id)
	if err != nil {
		return err
	}
	if assigned {
		return conflictErr("this template is assigned to a department or group; unassign it before archiving it")
	}
	return nil
}

// encodeStandardsRefs is the run column of the templates its instructions came from; "" for none.
func encodeStandardsRefs(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	raw, _ := json.Marshal(refs)
	return string(raw)
}

// decodeStandardsRefs reads the column back. An empty one is none: the run was launched before the
// column, or with no standards. One that cannot be read is an error and never none, because the run
// was given standards that a resume would then drop without a word.
func decodeStandardsRefs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var refs []string
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil, err
	}
	return refs, nil
}
