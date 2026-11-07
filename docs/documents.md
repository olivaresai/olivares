# Documents

PDF, DOCX, PPTX, XLSX, HTML, CSV, JSON, XML, EPUB and ZIP documents are converted locally by Microsoft MarkItDown. Attach one in the session composer, upload one to My files, or mention a document in the session folder with `@path` (`@"path with spaces.pdf"` for spaces). The agent receives Markdown as untrusted document content. Image attachments retain their native image delivery.

Conversion preserves the original. A Markdown companion is created beside it; session uploads and mentions use a unique companion name so earlier files remain intact. The Documents module is enabled on new installations and can be switched off in Settings > Modules. A disabled module or missing converter refuses explicit uploads and CLI conversions. Previously converted attachments remain available.

Only binary document mentions (PDF, DOCX, PPTX, XLSX, EPUB and ZIP) expand automatically. Text formats such as JSON remain plain text. If a mention cannot be safely opened, converted or given a Markdown companion, the message is sent as written without expansion. Mentions never expand recursively inside converted content.

On the engine host, `olivares files convert report.pdf` creates `report.pdf.md` and prints its path. `--json` prints `{"path":"..."}`. An existing companion is never replaced. Exit codes are 0 for success, 1 for conversion failure and 2 for invalid arguments.

The packaged converter has no network access, plugins or provider credentials. The engine passes only the document's bounded bytes through stdin, not a user folder. Linux Landlock ABI 3 or later and the offline syscall filter are required. Conversion has a 32 MiB input/output ceiling, 30-second wall-clock deadline, 25-second CPU limit and 768 MiB address-space limit. Composer uploads retain their existing 3.75 MiB limit. Missing format dependencies, confinement failures and empty conversion results are errors.

Server ISO and container images carry the hash-locked Python dependency closure from `modules/documents/requirements.txt`; no Python packages are installed during conversion. MarkItDown is MIT-licensed; dependency licenses travel with their installed distribution metadata.
