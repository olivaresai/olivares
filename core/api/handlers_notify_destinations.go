// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// Console notification-destination endpoints (notify_destinations.go): list, test,
// save and remove a Slack or Teams destination. SUPERADMIN-gated; the test send and
// every write require the AAL3 step-up. The list is a plain superadmin read.

func (s *Server) notifyDestinationsSvc(w http.ResponseWriter, r *http.Request) (NotifyDestinations, bool) {
	if s.notifyDestinations == nil {
		s.writeError(w, r, errNotifyDestinationsUnavailable)
		return nil, false
	}
	return s.notifyDestinations, true
}

func (s *Server) handleListNotifyDestinations(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	svc, ok := s.notifyDestinationsSvc(w, r)
	if !ok {
		return
	}
	list, err := svc.List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if list.Destinations == nil {
		list.Destinations = []NotifyDestination{}
	}
	if list.Kinds == nil {
		list.Kinds = []NotifyDestinationKind{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleTestNotifyDestination(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if !s.requireStepUp(w, r, mc.Principal) {
		return
	}
	svc, ok := s.notifyDestinationsSvc(w, r)
	if !ok {
		return
	}
	var in NotifyDestinationInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if err := svc.Test(r.Context(), mc.Principal, in); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePutNotifyDestination(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if !s.requireStepUp(w, r, mc.Principal) {
		return
	}
	svc, ok := s.notifyDestinationsSvc(w, r)
	if !ok {
		return
	}
	var in NotifyDestinationInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	dest, err := svc.Put(r.Context(), mc.Principal, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dest)
}

func (s *Server) handleDeleteNotifyDestination(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if !s.requireStepUp(w, r, mc.Principal) {
		return
	}
	svc, ok := s.notifyDestinationsSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if err := svc.Delete(r.Context(), mc.Principal, in.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
