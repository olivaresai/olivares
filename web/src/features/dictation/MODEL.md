# Dictation build input

`model/` contains the pinned files from
[onnx-community/whisper-tiny at ff4177021cc41f7db950912b73ea4fdf7d01d8e7](https://huggingface.co/onnx-community/whisper-tiny/tree/ff4177021cc41f7db950912b73ea4fdf7d01d8e7).
`tokenizer.json`, `config.json` and `generation_config.json` are stored losslessly
as `.gz` files (the JSON pair so that the console formatter never reads upstream
bytes); the other files are stored unmodified. `whisper-model.json` records each
expanded file's SHA-256 and size, plus `gzipSha256` for each stored archive. The
Vite build verifies the stored bytes, bounds decompression to the pinned size,
verifies the expanded bytes and emits them at `assets/voice/<revision>/<file>`
without network access. A missing or changed file fails the build; restore the
pinned bytes instead of downloading a replacement during a build.

The model derives from OpenAI Whisper, whose code and weights are released under
[MIT](https://github.com/openai/whisper/blob/main/LICENSE), recorded in
`REUSE.toml`. Its license ships at `web/public/licenses/whisper/Whisper-MIT.txt`.
To update it, replace the revision, files and pins together, keep the upstream
license, and run the dictation build regression tests and an offline console
build. Use deterministic gzip (for example, `gzip -n`) for the archives and update their
compressed hashes too. Do not format or normalize the expanded upstream bytes.
