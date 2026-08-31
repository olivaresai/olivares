// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fscontent

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// fileEntry is one ingestable file discovered by the walk: enough to list it, never
// its body (the body is read lazily and confined at Fetch).
type fileEntry struct {
	rel         string // root-relative, forward-slash path — also the DocID
	title       string
	contentType string
	modTime     time.Time
	size        int64
}

// walkStats records what the walk deliberately did NOT ingest, so the connector is
// honest about coverage (no silent drops).
type walkStats struct {
	symlinks     int // symlinks are never followed
	nonRegular   int // devices/sockets/pipes
	binaries     int // skipped by text_only
	excluded     int // filtered by include/exclude globs
	richDocs     int // indexed as rich-document candidates (OOXML; extracted at Fetch)
	richTooLarge int // rich docs over the extractor input cap — skipped at walk, never read
	readErrors   int // dir/file read failures swallowed during the walk (listing is INCOMPLETE)
	budgetHit    bool
	total        int
	totalBytes   int64
}

// complete reports whether the walk produced a full listing of the tree. It is FALSE
// when the file/byte budget cut the walk short OR a directory/file read error was
// swallowed — in either case "a doc is absent from the index" does NOT prove "the doc
// was removed from the source", so orphan-based deletion must not run against it.
func (s walkStats) complete() bool {
	return !s.budgetHit && s.readErrors == 0
}

// validateGlob rejects a malformed glob pattern at Open (filepath.Match reports it).
func validateGlob(g string) error {
	if _, err := filepath.Match(g, ""); err != nil {
		return fmt.Errorf("fscontent: invalid glob %q: %w", g, err)
	}
	return nil
}

// matchAny reports whether rel matches any of the patterns (against the full relative
// path AND its basename, so both "docs/*" and "*.md" work intuitively).
func matchAny(patterns []string, rel string) bool {
	base := path.Base(rel)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

// included applies the include/exclude rules to a relative file path.
func (sc *sourceConfig) included(rel string) bool {
	if len(sc.include) > 0 && !matchAny(sc.include, rel) {
		return false
	}
	if len(sc.exclude) > 0 && matchAny(sc.exclude, rel) {
		return false
	}
	return true
}

// walk descends the tree through the confined root, returning the ingestable file
// entries and the honest skip stats. It NEVER follows a symlink, refuses anything the
// os.Root would not open (escape / traversal), and stops at the file-count and
// total-byte budgets so it cannot exhaust a large or slow (NFS) mount.
func walk(root *os.Root, sc *sourceConfig) ([]fileEntry, walkStats, error) {
	var (
		entries []fileEntry
		stats   walkStats
		queue   = []string{"."}
	)
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]

		df, err := root.Open(dir)
		if err != nil {
			// A directory that vanished mid-walk or that the root refuses (an escape)
			// is skipped, not fatal — the walk is best-effort and read-only. But a READ
			// error (a transient NFS/SMB failure) means the listing is INCOMPLETE, which
			// the caller must know before treating "absent" as "deleted" (orphan detection).
			stats.readErrors++
			continue
		}
		ents, err := df.ReadDir(-1)
		_ = df.Close()
		if err != nil {
			stats.readErrors++
			continue
		}
		for _, ent := range ents {
			rel := joinRel(dir, ent.Name())
			typ := ent.Type()
			switch {
			case typ&os.ModeSymlink != 0:
				// Never follow a symlink (the escape guarantee is the os.Root's; this
				// is the belt: we do not even attempt to resolve it).
				stats.symlinks++
			case ent.IsDir():
				if len(sc.exclude) > 0 && matchAny(sc.exclude, rel) {
					stats.excluded++
					continue
				}
				queue = append(queue, rel)
			case typ.IsRegular():
				if !sc.included(rel) {
					stats.excluded++
					continue
				}
				rich := sc.extractRichDocs && isRichDocPath(ent.Name())
				if sc.textOnly && !isTextPath(ent.Name()) && !rich {
					stats.binaries++
					continue
				}
				info, err := ent.Info()
				if err != nil {
					// A file whose metadata cannot be read is an incomplete-listing signal
					// too (it may exist but be un-indexed): count it, do not silently drop.
					stats.readErrors++
					continue
				}
				// A rich document is READ up to richDocMaxInputBytes at Fetch (the whole
				// archive), NOT sc.maxFileBytes — so it must be CHARGED at that ceiling or
				// the I/O budget (max_total_bytes) is defeated 25×. An archive larger than
				// the extractor's input cap can never be extracted (a truncated zip is
				// unparseable), so skip it at walk and never read its full bytes.
				readCap := sc.maxFileBytes
				if rich {
					if info.Size() > int64(richDocMaxInputBytes) {
						stats.richTooLarge++
						continue
					}
					readCap = richDocMaxInputBytes
				}
				readBytes := info.Size()
				if int64(readCap) < readBytes {
					readBytes = int64(readCap)
				}
				if stats.total >= sc.maxFiles || stats.totalBytes+readBytes > sc.maxTotalBytes {
					stats.budgetHit = true
					return entries, stats, nil
				}
				stats.total++
				stats.totalBytes += readBytes
				if rich {
					stats.richDocs++
				}
				entries = append(entries, fileEntry{
					rel:         rel,
					title:       ent.Name(),
					contentType: contentTypeFor(ent.Name()),
					modTime:     info.ModTime().UTC(),
					size:        info.Size(),
				})
			default:
				stats.nonRegular++
			}
		}
	}
	return entries, stats, nil
}

// joinRel joins a relative dir and a child name into a forward-slash relative path,
// dropping the leading "./" for the tree root.
func joinRel(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}

// textExtensions is the allow-list of extensions the connector treats as ingestable
// text/document content. Binary types (images, archives, executables) are excluded
// and counted. Rich formats needing extraction (PDF/DOCX) are intentionally NOT here
// — the connector reads text; extraction is a declared follow-up, not a silent skip.
var textExtensions = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".rst": true, ".adoc": true,
	".org": true, ".text": true, ".log": true, ".csv": true, ".tsv": true,
	".json": true, ".jsonl": true, ".ndjson": true, ".yaml": true, ".yml": true,
	".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	".xml": true, ".html": true, ".htm": true, ".tex": true, ".rtf": false,
	".sql": true, ".env": true,
}

func isTextPath(name string) bool {
	return textExtensions[strings.ToLower(filepath.Ext(name))]
}

// ooxmlExtensions is the set of Office Open XML document extensions the connector can
// ingest when a rich-document extractor is injected. They are indexed only in that
// case (isRichDocPath gated on sc.extractRichDocs); their bytes are extracted to text
// out-of-process at Fetch. Legacy binary Office formats (.doc/.ppt/.xls) and PDF are
// deliberately absent — they are not stdlib-extractable and remain a counted skip.
var ooxmlExtensions = map[string]bool{
	".docx": true, ".pptx": true, ".xlsx": true,
}

func isRichDocPath(name string) bool {
	return ooxmlExtensions[strings.ToLower(filepath.Ext(name))]
}

// contentTypeFor returns a MIME-ish content type by extension (no read).
func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".json", ".jsonl", ".ndjson":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".xml":
		return "text/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return "text/plain"
	}
}
