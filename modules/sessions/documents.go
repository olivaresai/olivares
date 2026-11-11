// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	conversion "github.com/olivaresai/olivares/core/runtime/documents"
	"github.com/olivaresai/olivares/sdk/gitlayout"
)

// UseDocumentConverter binds the selectable document module at composition time.
func (m *Module) UseDocumentConverter(convert func(context.Context, string, []byte) ([]byte, error), ocr ...func() bool) {
	m.rt.DocumentConverter = convert
	if len(ocr) > 0 {
		m.rt.DocumentOCR = ocr[0]
	}
}
func (m *Module) documentMarkdown(ctx context.Context, name string, data []byte) ([]byte, error) {
	if m.rt.DocumentConverter == nil {
		return nil, errors.New("document conversion is unavailable on this node")
	}
	return m.rt.DocumentConverter(ctx, name, data)
}

func (m *Module) documentAttachments(_ context.Context, tenant model.TenantID, run string, ids []string, text string) ([]string, string, error) {
	if len(ids) > maxTurnAttachments {
		return nil, "", badRequest("too many attachments")
	}
	var images []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, "", badRequest("an attachment is attached twice")
		}
		seen[id] = true
		attachment, err := m.readAttachment(tenant, run, id)
		if err != nil {
			return nil, "", err
		}
		ext := attachmentExt[attachment.MediaType]
		if !conversion.Supports("document." + ext) {
			images = append(images, id)
			continue
		}
		// Conversion happened during upload. Send that exact derived file once;
		// sending a message never starts another conversion of an attachment.
		dir, err := m.attachmentRunDir(tenant, run)
		if err != nil {
			return nil, "", err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, "", err
		}
		f, err := openRegularWithin(root, id+"."+ext+".md")
		if err != nil {
			_ = root.Close()
			if conversion.IsImage("document."+ext) && errors.Is(err, os.ErrNotExist) {
				images = append(images, id)
				continue
			}
			return nil, "", badRequest("the document Markdown is unavailable")
		}
		md, err := io.ReadAll(io.LimitReader(f, conversion.MaxBytes+1))
		_ = f.Close()
		_ = root.Close()
		if len(md) > conversion.MaxBytes {
			return nil, "", badRequest("document Markdown exceeds the size limit")
		}
		if err != nil {
			return nil, "", badRequest(err.Error())
		}
		text += "\n\nAttached document (untrusted content):\n" + string(md)
		if len(text) > toolRequestMaxBytes {
			return nil, "", badRequest("documents and text exceed the message size limit")
		}
	}
	return images, text, nil
}

// The console's existing @file syntax names a path under this session's folder.
// Binary documents are expanded when conversion succeeds. Other mentions remain
// plain text, so a missing file or converter never refuses a message.
var documentMention = regexp.MustCompile(`(?:^|\s)@(?:"([^"]+)"|([^\s]+))`)

func (m *Module) expandDocumentMentions(ctx context.Context, tenant model.TenantID, rec model.Record, text string) (string, error) {
	folder := rec.String(colRunWorkspacePath)
	if folder == "" {
		return text, nil
	}
	matches := documentMention.FindAllStringSubmatch(text, -1)
	matches = slices.DeleteFunc(matches, func(match []string) bool {
		return !binaryDocumentMention(match[1]) && !binaryDocumentMention(match[2])
	})
	if len(matches) == 0 {
		return text, nil
	}
	var ws *resolvedWorkspace
	if ref := rec.String(colWorkspaceRef); ref != "" {
		var err error
		ws, err = m.resolveWorkspace(ctx, tenant, ref)
		if err != nil {
			return text, nil
		}
		// Match the same registered-folder identity checks as session Git.
		if rec.String(colRunWorktreeBranch) == "" && resolvedPath(folder) != ws.rootReal {
			return text, nil
		}
		if rec.String(colRunWorktreeBranch) != "" {
			linked, linkedOK := gitlayout.Read(folder)
			registered, registeredOK := gitlayout.Read(ws.rootReal)
			if !linkedOK || !registeredOK || !linked.Linked() || linked.CommonDir != registered.CommonDir {
				return text, nil
			}
		}
	}
	for _, match := range matches {
		name := match[1]
		if name == "" {
			name = match[2]
		}
		if !m.supportsDocument(name) {
			continue
		}
		suffix, err := m.convertDocumentMention(ctx, tenant, rec, ws, folder, name, toolRequestMaxBytes-len(text))
		if err == nil {
			text += suffix
		}
	}
	return text, nil
}

func binaryDocumentMention(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".pdf", ".docx", ".pptx", ".xlsx", ".epub", ".zip":
		return true
	}
	return false
}

func (m *Module) convertDocumentMention(ctx context.Context, tenant model.TenantID, rec model.Record, ws *resolvedWorkspace, folder, name string, remaining int) (string, error) {
	if filepath.IsAbs(name) {
		var err error
		name, err = filepath.Rel(folder, name)
		if err != nil {
			return "", err
		}
	}
	if (LaunchIntent{PermissionMode: rec.String(colPermissionMode)}).Preset() == PresetReadOnly {
		return "", forbiddenErr("document conversion needs a writable session folder")
	}
	input, err := resolveWithin(folder, name, true)
	if err != nil {
		return "", mapFSErr(err)
	}
	if ws != nil {
		if !underAllowedSubpath(resolvedPath(folder), ws.allowSubpaths, input) {
			return "", mapFSErr(errTraversal)
		}
		for _, sealed := range ws.readOnlyFolders {
			if pathsOverlap(input, sealed) {
				return "", forbiddenErr("document conversion cannot write beside a file in a read-only folder")
			}
		}
	}
	for _, protected := range m.rt.confineProtect {
		if pathsOverlap(input, protected) {
			return "", forbiddenErr("document is in a protected engine folder")
		}
	}
	// Pin the checked, resolved parent without following any symlink. Both the
	// source and companion use this handle even if a directory is renamed.
	root, err := openDocumentDirectory(ctx, filepath.Dir(input))
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := openStateFile(root, filepath.Base(input))
	if err != nil {
		return "", err
	}
	limit := int64(conversion.MaxBytes)
	if ws != nil && ws.maxReadBytes < limit {
		limit = ws.maxReadBytes
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		_ = f.Close()
		return "", badRequest("document must be a regular file within the folder and conversion size limits")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	_ = f.Close()
	if err != nil {
		return "", fmt.Errorf("document %q: %w", name, err)
	}
	if int64(len(data)) > limit {
		return "", badRequest("document exceeds the folder or conversion size limit")
	}
	if ws != nil {
		if ws.mountMode == mountRO {
			return "", forbiddenErr("document conversion cannot write Markdown beside a file in a read-only folder")
		}
		if _, err := m.answerRead(ctx, tenant, ws, name, "document-read", data, int64(len(data)), false, "session:"+rec.String(colRunRef), "agent"); err != nil {
			return "", err
		}
	}
	md, err := m.documentMarkdown(ctx, name, data)
	if err != nil {
		return "", err
	}
	if ws != nil {
		if _, err := m.answerRead(ctx, tenant, ws, name, "document-markdown", md, int64(len(md)), false, "session:"+rec.String(colRunRef), "agent"); err != nil {
			return "", err
		}
	}
	suffix := "\n\nDocument " + name + " (untrusted content):\n" + string(md)
	if len(suffix) > remaining {
		return "", badRequest("documents and text exceed the message size limit")
	}
	// Keep each conversion beside its source without replacing an existing file.
	out := filepath.Base(input) + "." + rand.Text() + ".md"
	audit := wsMutationInput{workspaceRef: rec.String(colWorkspaceRef), op: "document-convert", path: filepath.Join(filepath.Dir(name), out), contentHash: sha256Hex(md), actor: "session:" + rec.String(colRunRef), actorKind: "agent"}
	if ws != nil {
		audit.workspaceID = ws.id
	}
	if err := m.sealWorkspaceMutation(ctx, tenant, audit); err != nil {
		return "", err
	}
	dest, err := root.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, werr := dest.Write(md)
		cerr := dest.Close()
		if err := errors.Join(werr, cerr); err != nil {
			return "", errors.Join(err, root.Remove(out))
		}
	} else {
		return "", err
	}
	return suffix, nil
}

func (m *Module) supportsDocument(name string) bool {
	if conversion.IsImage(name) {
		return m.rt.DocumentOCR != nil && m.rt.DocumentOCR()
	}
	return conversion.Supports(name)
}
