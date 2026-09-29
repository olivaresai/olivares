// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestServices_AnswerCarriesEachSubcommandInItsBoundedData(t *testing.T) {
	ctx := context.Background()
	bus := newFakeBus(active("olivares.service"), active("dbus.service"))
	bus.finish = finishWith("done", inactive)
	journal := func(context.Context, []string, int64) ([]byte, error) {
		return []byte(journalLine(1790000000000000, "6", "started", nil)), nil
	}
	x := Executor{Bus: bus, Journal: journal, JobWait: time.Second}
	lines := 5
	for _, c := range []struct {
		req          Request
		result, code string
		data         string
	}{
		{Request{Op: OpList}, helperschema.ResultAnswered, "", `"units":[`},
		{Request{Op: OpStatus, Unit: "dbus.service"}, helperschema.ResultAnswered, "", `"class":"protected"`},
		{Request{Op: OpLogs, Unit: "olivares.service", Lines: &lines, OperationID: operationID}, helperschema.ResultPerformed, "", `"message":"started"`},
		{Request{Op: OpStop, Unit: "olivares.service", OperationID: operationID}, helperschema.ResultPerformed, "", `"outcome":"succeeded"`},
		{Request{Op: OpStop, Unit: "dbus.service", OperationID: operationID}, helperschema.ResultRefused, CodeUnitProtected, ""},
		{Request{Op: OpStatus, Unit: "absent.service"}, helperschema.ResultRefused, CodeInputRefused, ""},
	} {
		req := c.req
		resp := Answer(ctx, x, &req)
		if resp.Result != c.result || resp.Code != c.code || !strings.Contains(string(resp.Bundle), c.data) || len(resp.Bundle) > MaxAnswerBytes || resp.Detail == "" {
			t.Errorf("%s %s answered %+v (%s), want %s %s with %s", req.Op, req.Unit, resp, resp.Bundle, c.result, c.code, c.data)
		}
		if resp.Result == helperschema.ResultRefused && len(resp.Bundle) != 0 {
			t.Errorf("a refusal carries data: %s", resp.Bundle)
		}
	}
	// A call the manager refuses failed; nothing was queued.
	bus.queueErr = errors.New("org.freedesktop.systemd1.JobTypeNotApplicable")
	if resp := Answer(ctx, x, &Request{Op: OpRestart, Unit: "olivares.service", OperationID: operationID}); resp.Result != helperschema.ResultFailed || resp.Code != CodeEffectFailed {
		t.Errorf("a refused call answered %+v", resp)
	}
	// A job that never reports was accepted by the manager, with an unknown outcome: the
	// operation stays running.
	bus.queueErr = nil
	bus.finish = func(_, u, _ string) ([]JobRemoved, Unit) { return nil, inactive(u) }
	x.JobWait = 20 * time.Millisecond
	resp := Answer(ctx, x, &Request{Op: OpStart, Unit: "olivares.service", OperationID: operationID})
	if resp.Result != helperschema.ResultPerformed || resp.Code != CodeOutcomeUnknown || !strings.Contains(string(resp.Bundle), `"outcome":"running"`) {
		t.Errorf("an unreported job answered %+v (%s)", resp, resp.Bundle)
	}
}
