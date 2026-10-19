// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/model"
)

// An image a person attaches to a message (#2158). The engine keeps it with the
// session, under the tenant and outside every folder the session works in:
//
//	<attachment root>/<tenant>/<run_ref>/<id>.<ext>
//
// The path is the whole access rule. The routes below are run routes, so the run is
// authorized (and concealed as a 404 across tenants) before a path is built, and the
// tenant and run_ref in it come from that authorized run, never from the request.
//
// The tool receives the bytes natively: Claude Code as an image content block in the
// stream-json user message, Codex as an image input of its turn. A tool with neither
// refuses the attachment with its own name; nothing is dropped silently.
const (
	// maxAttachmentBytes is the largest image the tools take. Claude's API refuses an
	// image whose base64 form is over 5 MiB, so the decoded limit is 3/4 of that.
	maxAttachmentBytes = 5 << 20 / 4 * 3
	// maxAttachmentBody is the JSON upload: the base64 form of the largest image
	// plus its envelope.
	maxAttachmentBody = 5<<20 + 4<<10
	// maxTurnAttachments bounds one message, so one stdin line stays far below the
	// tool's own request limit.
	maxTurnAttachments = 4
	attachmentIDPrefix = "att_"
	// A session keeps at most this many images, and this many bytes of them. Images
	// stay until the session is deleted, so a bound keeps one writer from filling the
	// data directory; a person at a console reaches neither.
	maxStoredAttachments = 32
	maxStoredBytes       = 64 << 20
	// attachmentUploadSlots bounds the uploads in flight on a node: each holds about
	// three copies of its image while it is decoded.
	attachmentUploadSlots = 4
)

var attachmentUploads = make(chan struct{}, attachmentUploadSlots)

// attachmentExt is every image type the engine accepts and the file extension that
// records it. The type is read back from the extension, so no sidecar file exists.
var attachmentExt = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// turnImage is one stored image on its way into a message.
type turnImage struct {
	ID        string
	MediaType string
	Data      []byte
}

func (i turnImage) ref() attachmentRef { return attachmentRef{ID: i.ID, MediaType: i.MediaType} }

// attachmentRef is what a transcript needs to show the image again.
type attachmentRef struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
}

type attachmentUpload struct {
	MediaType string `json:"media_type"`
	// Data is the image, base64 encoded.
	Data string `json:"data"`
}

type attachmentDTO struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	SizeBytes int    `json:"size_bytes"`
}

// WithAttachmentRoot sets where attached images are kept. Unset, attaching is refused
// with a 503: the alternative would be a guessed directory next to the session's work.
func WithAttachmentRoot(dir string) Option {
	return func(m *Module) {
		if filepath.IsAbs(dir) {
			m.rt.attachmentRoot = filepath.Clean(dir)
		}
	}
}

func (m *Module) attachmentRunDir(tenant model.TenantID, runRef string) (string, error) {
	root := m.rt.attachmentRoot
	if root == "" {
		return "", &runErr{http.StatusServiceUnavailable, "attaching images is not available on this node"}
	}
	for _, part := range []string{tenant.String(), runRef} {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part || strings.ContainsAny(part, "\\\x00") {
			return "", notFoundErr()
		}
	}
	return filepath.Join(root, tenant.String(), runRef), nil
}

// toolDisplayName is the name a person knows a tool by.
func toolDisplayName(driver string) string {
	switch driver {
	case "", providerDriverClaude:
		return "Claude Code"
	case providerDriverCodex:
		return "Codex"
	case providerDriverGrok:
		return "Grok Build"
	}
	if name := nativeACPToolName(driver); name != "" {
		return name
	}
	return driver
}

// imageRefusal says why this run's tool cannot take an image, "" when it can. The
// sentence names the tool: a person who attached a screenshot learns it did not
// arrive, and from which tool.
func imageRefusal(rec model.Record) string {
	driver := rec.String(colRunProfileDriver)
	switch driver {
	case "", providerDriverClaude:
		if Transport(rec.String(colTransport)) != TransportStreamJSON {
			return "Claude Code does not take images in this kind of session"
		}
		return ""
	case providerDriverCodex:
		return ""
	}
	return toolDisplayName(driver) + " does not take images"
}

// handleRunAttachmentUpload stores one image with a session, to be sent with its next message.
func (m *Module) handleRunAttachmentUpload(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	dir, err := m.attachmentRunDir(mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	select {
	case attachmentUploads <- struct{}{}:
		defer func() { <-attachmentUploads }()
	default:
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusTooManyRequests, errorBody("other images are being stored; try again in a moment"))
		return
	}
	rec, err := m.loadRun(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	if why := imageRefusal(rec); why != "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody(why))
		return
	}
	var body attachmentUpload
	if err := api.DecodeRequestBody(w, r, &body, api.RequestBodySpec{MaxBytes: maxAttachmentBody}); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorBody(fmt.Sprintf("the image is over the limit of %.1f MB", megabytes(maxAttachmentBytes))))
			return
		}
		writeJSON(w, http.StatusBadRequest, errorBody(api.RequestBodyErrorMessage(err, "invalid request body")))
		return
	}
	ext, ok := attachmentExt[body.MediaType]
	if !ok {
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody("attach a PNG, JPEG, WebP or GIF image"))
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("the image is empty or not valid base64"))
		return
	}
	if len(data) > maxAttachmentBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody(fmt.Sprintf("the image is %.1f MB; the limit is %.1f MB", megabytes(len(data)), megabytes(maxAttachmentBytes))))
		return
	}
	// The declared type is a claim; the bytes decide. A file renamed to .png is not one.
	if http.DetectContentType(data) != body.MediaType {
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody("the file is not a "+strings.ToUpper(ext)+" image"))
		return
	}
	// The write is made under the run's lock, and only after the run is found again:
	// an upload cannot land in the directory of a run that is being deleted. The lock
	// is not held while the body is read, which can be slow.
	release, err := m.rt.lockRunContext(r.Context(), liveKey(mc.Tenant, ref))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	defer release()
	if _, err := m.loadRun(r.Context(), mc.Tenant, ref); err != nil {
		writeRunErr(w, err)
		return
	}
	if files, size := storedUsage(dir); files >= maxStoredAttachments || size+int64(len(data)) > maxStoredBytes {
		writeJSON(w, http.StatusInsufficientStorage, errorBody("this session holds as many images as it keeps; start a new session to attach more"))
		return
	}
	id := attachmentIDPrefix + string(model.NewID())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.warnf("session attachment directory", "run_ref", ref, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("the image could not be stored"))
		return
	}
	if err := writeAttachment(filepath.Join(dir, id+"."+ext), data); err != nil {
		m.warnf("session attachment write", "run_ref", ref, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("the image could not be stored"))
		return
	}
	writeJSON(w, http.StatusCreated, attachmentDTO{ID: id, MediaType: body.MediaType, SizeBytes: len(data)})
}

// storedUsage counts the images a session holds and their bytes. A directory not yet
// made holds none.
func storedUsage(dir string) (files int, size int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.Type().IsRegular() {
			files++
			size += info.Size()
		}
	}
	return files, size
}

// handleRunAttachmentGet returns an image stored with a session, as it was uploaded.
func (m *Module) handleRunAttachmentGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	img, err := m.readAttachment(mc.Tenant, chi.URLParam(r, "ref"), chi.URLParam(r, "id"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	// Served as the image it was checked to be, and never as anything a browser
	// would run: the page that shows it is the console's, not this response.
	h := w.Header()
	h.Set("Content-Type", img.MediaType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Data)
}

// readAttachment loads one stored image of a run. An id that is not one this module
// minted, or that names no file of this run, is the same 404.
func (m *Module) readAttachment(tenant model.TenantID, runRef, id string) (turnImage, error) {
	dir, err := m.attachmentRunDir(tenant, runRef)
	if err != nil {
		return turnImage{}, err
	}
	if _, err := model.ParseID(strings.TrimPrefix(id, attachmentIDPrefix)); err != nil || !strings.HasPrefix(id, attachmentIDPrefix) {
		return turnImage{}, &runErr{http.StatusNotFound, "attachment not found"}
	}
	for media, ext := range attachmentExt {
		data, err := os.ReadFile(filepath.Join(dir, id+"."+ext))
		if err == nil {
			return turnImage{ID: id, MediaType: media, Data: data}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			m.warnf("session attachment read", "run_ref", runRef, "error", err)
			return turnImage{}, &runErr{http.StatusInternalServerError, "the image could not be read"}
		}
	}
	return turnImage{}, &runErr{http.StatusNotFound, "attachment not found"}
}

// loadTurnImages resolves the ids of one message to stored images, in order.
func (m *Module) loadTurnImages(tenant model.TenantID, runRef string, ids []string) ([]turnImage, error) {
	if len(ids) > maxTurnAttachments {
		return nil, badRequest(fmt.Sprintf("attach at most %d images to one message", maxTurnAttachments))
	}
	seen := map[string]bool{}
	images := make([]turnImage, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, badRequest("an image is attached twice")
		}
		seen[id] = true
		img, err := m.readAttachment(tenant, runRef, id)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, nil
}

// removeRunAttachments deletes every image of a run, when the run itself goes.
func (m *Module) removeRunAttachments(tenant model.TenantID, runRef string) {
	dir, err := m.attachmentRunDir(tenant, runRef)
	if err != nil {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		m.warnf("session attachments not removed", "run_ref", runRef, "error", err)
	}
}

// writeAttachment stores the bytes whole or not at all: a half-written image would be
// sent to the tool as a corrupt one.
func writeAttachment(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func megabytes(n int) float64 { return float64(n) / (1 << 20) }

// sessionsRunAttachmentResponses publishes what the two attachment routes answer.
func sessionsRunAttachmentResponses(method, pattern string) (map[string]any, bool) {
	resp := api.GenericModuleResponses()
	switch method + " " + pattern {
	case http.MethodPost + " /runs/{ref}/attachments":
		delete(resp, "200")
		resp["201"] = oas.Obj("description", "the image is stored with the session; send its id with the next message",
			"content", oas.Obj("application/json", oas.Obj("schema", oas.Obj(
				"type", "object", "additionalProperties", false, "required", oas.Enum("id", "media_type", "size_bytes"),
				"properties", oas.Obj(
					"id", oas.Obj("type", "string"),
					"media_type", oas.Obj("type", "string", "enum", oas.Enum(sessionsAttachmentMediaTypes()...)),
					"size_bytes", oas.Obj("type", "integer"),
				),
			))))
		resp["413"] = oas.JSONResp("the image is over the size the tools take")
		resp["429"] = oas.JSONResp("other images are being stored on this node; retry")
		resp["507"] = oas.JSONResp("the session already holds as many images as it keeps")
		resp["415"] = oas.JSONResp("the file is not a PNG, JPEG, WebP or GIF image")
		resp["422"] = oas.JSONResp("the session's tool does not take images")
		resp["503"] = oas.JSONResp("attaching images is not available on this node")
		return resp, true
	case http.MethodGet + " /runs/{ref}/attachments/{id}":
		resp["200"] = oas.Obj("description", "the stored image, as uploaded",
			"content", oas.Obj(
				"image/png", oas.Obj("schema", oas.Obj("type", "string", "format", "binary")),
				"image/jpeg", oas.Obj("schema", oas.Obj("type", "string", "format", "binary")),
				"image/webp", oas.Obj("schema", oas.Obj("type", "string", "format", "binary")),
				"image/gif", oas.Obj("schema", oas.Obj("type", "string", "format", "binary")),
			))
		resp["503"] = oas.JSONResp("attaching images is not available on this node")
		return resp, true
	}
	return nil, false
}

// sessionsAttachmentUploadSchema is the closed body of POST /runs/{ref}/attachments.
func sessionsAttachmentUploadSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"media_type", oas.Obj("type", "string", "enum", oas.Enum(sessionsAttachmentMediaTypes()...)),
		"data", oas.Obj("type", "string", "contentEncoding", "base64",
			"description", "The image, base64 encoded. At most 3.75 MiB decoded."),
	), "media_type", "data")
}

func sessionsAttachmentMediaTypes() []string {
	return []string{"image/png", "image/jpeg", "image/webp", "image/gif"}
}
