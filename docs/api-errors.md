# API errors

Ordinary REST failures use the `Error` schema in `/openapi.json` and
`/openapi.beta.json`:

```json
{"error":{"code":"not_found","message":"not found"}}
```

Both `code` and `message` are strings. Use the HTTP status and `error.code` to
classify a failure; `message` is human-readable. Beta handlers that previously
provided only a message use `module_error` until they define a more specific
code. Existing specific codes retain their meaning.

An error may include additional fields. Git publication keeps `intent_id` at the
top level so callers can inspect an unresolved operation before deciding what to
do next. Workflow graph validation keeps `error.step_ref`; policy failures keep
their policy references. Clients should retain such fields and tolerate new ones.

Some operations return a structured result on a non-success HTTP status, such as
a raced session control request or a rejected publication intent. Preserve that
result and consult the operation's response schema. An HTTP status alone does
not determine the response body. SCIM and OAuth retain their protocol-specific
error formats.

The beta migration is recorded under Changed in [CHANGELOG.md](../CHANGELOG.md),
which names the operations that answered a string-valued `error` in 26.10.1<!-- release-fixed -->.
Clients reading older servers can accept both a string-valued `error` and the
object envelope.
Stable routes keep their existing bodies.

Handler authors use `api.WriteJSON` for encoding and `api.ErrorBody` for an error
envelope. Supply only a safe client-facing message; the constructor does not
sanitize raw internal errors. Keep route-specific cache headers, media types and
structured results at the handler boundary.

For JSON request decode failures, handlers retain their HTTP status, error code,
and envelope. The message names the offending JSON field when the decoder can
identify it, such as `invalid JSON body: unknown field "request.action"` or
`invalid JSON body: invalid value for field "request.principal"`. It does not
echo field values or Go type names. Syntax errors, empty required bodies, size
limits and trailing documents retain the handler's existing generic message.
Custom unknown-field errors may have only a leaf name. Custom type errors
retain the generic message because their offsets may be relative to one value.

Handler authors should pass decoder failures through
`api.RequestBodyErrorMessage(err, existingMessage)`; it appends only classified
field diagnostics and keeps the supplied fallback for other failures.
