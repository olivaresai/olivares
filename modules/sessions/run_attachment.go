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
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/model"
	conversion "github.com/olivaresai/olivares/core/runtime/documents"
)

// An image a person attaches to a message. The engine keeps it with the
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
	// maxTurnAttachments is the most images one message carries: one message reaches
	// the model in one request, which takes 100 images on a model with a 200k-token
	// context window and 600 on the others
	// (https://platform.claude.com/docs/en/build-with-claude/vision#request-limits).
	// Their bytes, with the text, are bounded by toolRequestMaxBytes.
	maxTurnAttachments = 100
	attachmentIDPrefix = "att_"
	// attachmentUploadSlots bounds the uploads in flight on a node: each holds about
	// three copies of its image while it is decoded.
	attachmentUploadSlots = 4
)

var attachmentUploads = make(chan struct{}, attachmentUploadSlots)

// attachmentExt is every image type the engine accepts and the file extension that
// records it. The type is read back from the extension, so no sidecar file exists.
var attachmentExt = map[string]string{
	"application/pdf": "pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/json":     "json",
	"application/xml":      "xml",
	"text/csv":             "csv",
	"text/html":            "html",
	"application/epub+zip": "epub",
	"application/zip":      "zip",
	"image/png":            "png",
	"image/jpeg":           "jpg",
	"image/webp":           "webp",
	"image/gif":            "gif",
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
	return runDirUnder(root, tenant, runRef)
}

// runDirUnder is <root>/<tenant>/<run_ref>, the place a run keeps what the engine holds for
// it outside its folder. The tenant and the run come from the authorized run, never from
// the request; a segment that could leave the root is the same 404 as an unknown run.
func runDirUnder(root string, tenant model.TenantID, runRef string) (string, error) {
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
		writeJSON(w, http.StatusUnsupportedMediaType, errorBody("attach a supported image, PDF, Office document, HTML, CSV, JSON, XML, EPUB or ZIP"))
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
	var markdown []byte
	if m.supportsDocument("document." + ext) {
		markdown, err = m.documentMarkdown(r.Context(), "document."+ext, data)
		if errors.Is(err, conversion.ErrBusy) {
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusTooManyRequests, errorBody(err.Error()))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorBody(err.Error()))
			return
		}
	} else if why := imageRefusal(rec); why != "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody(why))
		return
	}
	// The declared type is a claim; the bytes decide. A file renamed to .png is not one.
	if conversion.IsImage("document."+ext) && http.DetectContentType(data) != body.MediaType {
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
	if err := makeRoom(dir, int64(len(data)+len(markdown))); err != nil {
		m.warnf("session attachments not removed", "run_ref", ref, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("the image could not be stored"))
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
	if markdown != nil {
		if err := writeAttachment(filepath.Join(dir, id+"."+ext+".md"), markdown); err != nil {
			_ = os.Remove(filepath.Join(dir, id+"."+ext))
			writeJSON(w, http.StatusInternalServerError, errorBody("the document Markdown could not be stored"))
			return
		}
	}
	writeJSON(w, http.StatusCreated, attachmentDTO{ID: id, MediaType: body.MediaType, SizeBytes: len(data)})
}

// makeRoom removes a session's oldest images until one of incoming bytes fits with the
// rest in one model request (toolRequestMaxBytes). Every request resends the whole
// conversation (https://platform.claude.com/docs/en/build-with-claude/vision), so an
// image older than a request's worth of newer ones no longer reaches the model with
// them; the console shows a removed image as unavailable. The bound keeps one writer
// from filling the data directory. A directory not yet made holds none.
func makeRoom(dir string, incoming int64) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type stored struct {
		name string
		size int64
		at   time.Time
	}
	var images []stored
	total := incoming
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.Type().IsRegular() {
			// Document originals and derivatives stay until the session is removed.
			if ext := strings.ToLower(filepath.Ext(e.Name())); ext == ".png" || ext == ".jpg" || ext == ".webp" || ext == ".gif" {
				images = append(images, stored{e.Name(), info.Size(), info.ModTime()})
			}
			total += info.Size()
		}
	}
	slices.SortFunc(images, func(a, b stored) int { return a.at.Compare(b.at) })
	for _, img := range images {
		if total <= toolRequestMaxBytes {
			break
		}
		if err := os.Remove(filepath.Join(dir, img.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		total -= img.size
	}
	if total > toolRequestMaxBytes {
		return errors.New("session document storage is full; remove the session or use its folder")
	}
	return nil
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

// loadTurnImages resolves the ids of one message to stored images, in order. The
// message travels as one model request: its images as base64 and its text (textBytes)
// fit in toolRequestMaxBytes.
func (m *Module) loadTurnImages(tenant model.TenantID, runRef string, ids []string, textBytes int) ([]turnImage, error) {
	if len(ids) > maxTurnAttachments {
		return nil, badRequest(fmt.Sprintf("attach at most %d images to one message", maxTurnAttachments))
	}
	seen := map[string]bool{}
	images := make([]turnImage, 0, len(ids))
	size := textBytes
	for _, id := range ids {
		if seen[id] {
			return nil, badRequest("an image is attached twice")
		}
		seen[id] = true
		img, err := m.readAttachment(tenant, runRef, id)
		if err != nil {
			return nil, err
		}
		if size += base64.StdEncoding.EncodedLen(len(img.Data)); size > toolRequestMaxBytes {
			return nil, &runErr{http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"the images and text of this message are over the %d MB one request to the model takes", toolRequestMaxBytes/1_000_000)}
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
		resp["201"] = oas.Obj("description", "the file is stored with the session; documents reach the agent as Markdown",
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
			"description", "The file, base64 encoded. At most 3.75 MiB decoded."),
	), "media_type", "data")
}

func sessionsAttachmentMediaTypes() []string {
	types := make([]string, 0, len(attachmentExt))
	for media := range attachmentExt {
		types = append(types, media)
	}
	slices.Sort(types)
	return types
}
