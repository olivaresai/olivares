---
title: Documents
description: Local MarkItDown conversion to Markdown; originals retained, optional local OCR.
---

Local MarkItDown conversion to Markdown; originals retained, optional local OCR.


PDF, DOCX, PPTX, XLSX, HTML, CSV, JSON, XML, EPUB and ZIP documents are converted locally by Microsoft MarkItDown. Attach one in the session composer, upload one to My files, or mention a document in the session folder with `@path` (`@"path with spaces.pdf"` for spaces). The agent receives Markdown as untrusted document content. Image attachments retain their native image delivery while OCR is off.

Conversion preserves the original. A Markdown companion is created beside it; session uploads and mentions use a unique companion name so earlier files remain intact. The Documents module is enabled on new installations and can be switched off in Settings > Modules. A disabled module or missing converter refuses explicit uploads and CLI conversions. Previously converted attachments remain available.

Only binary document mentions (PDF, DOCX, PPTX, XLSX, EPUB and ZIP) expand automatically. Text formats such as JSON remain plain text. If a mention cannot be safely opened, converted or given a Markdown companion, the message is sent as written without expansion. Mentions never expand recursively inside converted content.

On the engine host, `olivares files convert report.pdf` creates `report.pdf.md` and prints its path. `--json` prints `{"path":"..."}`. An existing companion is never replaced. Exit codes are 0 for success, 1 for conversion failure and 2 for invalid arguments.

The packaged converter has no network access, plugins or provider credentials. The engine passes only the document's bounded bytes through stdin, not a user folder. Linux Landlock ABI 3 or later and the offline syscall filter are required. Conversion has a 32 MiB input/output ceiling, 30-second wall-clock deadline, 25-second CPU limit and 768 MiB address-space limit. Composer uploads retain their existing 3.75 MiB limit. Missing format dependencies, confinement failures and empty results from non-scanned formats are errors.

Server ISO and container images carry the hash-locked Python dependency closure from `modules/documents/requirements.txt`; no Python packages are installed during conversion. MarkItDown is MIT-licensed; dependency licenses travel with their installed distribution metadata.

## Scanned pages and OCR

OCR is optional and off by default. Enable Scanned documents (OCR) in Settings > Modules after an administrator sets `document_ocr` in the engine's existing `OLIVARES_SOURCES_CONFIG` JSON file. Turning OCR off restores MarkItDown conversion and prevents OCR requests. The Documents module remains the one conversion path and can be disabled in Settings > Modules. For example, merge this field into that file, using the digest of the already provisioned model:

```json
{
  "document_ocr": {
    "endpoint": "http://127.0.0.1:10000/v1",
    "model": "Unlimited-OCR-2@sha256:<64 lowercase hex digits>"
  }
}
```

Provision Unlimited-OCR 2 through the deployment's model-provisioning path before enabling this setting. Verify and pin the weights by SHA-256, then serve that immutable snapshot through SGLang using the matching digest-qualified `--served-model-name`. Conversion never installs packages or downloads weights, including at session start. The endpoint is administrator-controlled; the served alias is a declaration of that deployment's pinned snapshot, not remote attestation of its weights. Model code and weights are MIT-licensed. Follow the [upstream serving instructions](https://github.com/baidu/Unlimited-OCR) for the required SGLang wheel and GPU setup. CPU inference is for one-page tests only.

Only pages without text are sent for OCR. The offline helper detects PDF text layers and renders those pages using the already packaged PDF dependencies. Images are decoded and resized in the same confined helper. With OCR on, PDF companions retain page numbers and text pages. Without an endpoint, PDFs use the original MarkItDown output and limits; a PDF with no extracted text fails with the named error `this page has no text layer; OCR is off (Settings > Modules)` and no network request is made. Native image attachment delivery is retained until OCR is configured and the Documents module is running; with OCR configured, the agent receives the image's Markdown companion.

An endpoint must use a loopback address, `localhost`, or a literal IP assigned to this engine host. Remote OCR endpoints are refused. Proxies and redirects are disabled. URL credentials, query strings and fragments are rejected. Endpoint failures, empty responses, and oversized output refuse conversion with a named error. Input and output remain bounded at 32 MiB; rendered images are limited to 2048 pixels on the longest side.

Local CLI conversions use the same path: `olivares files convert scan.pdf --ocr-endpoint http://127.0.0.1:10000/v1 --ocr-model Unlimited-OCR-2@sha256:<digest>`. The endpoint must be local; the model flag declares the provisioned snapshot and does not verify served weights. With no flags OCR is off. Originals are preserved in either mode.
