# Pinned Chat execution profile example

Set `OLIVARES_MODEL_GATEWAY_PROFILES_CONFIG` to a local JSON file. Loading this
file validates references and metadata only. It does not resolve the credential
or contact the endpoint.

```json
{
  "schema_version": "olivares.model-gateway-profiles.v1",
  "profiles": [
    {
      "ref": "primary-chat",
      "revision": "sha256:a61fa36590fe4cec852c4587908433e27e5cd36c251f41c0c1266f04d99ee29b",
      "tenant_ref": "11111111-1111-4111-8111-111111111111",
      "action": "text.generate",
      "protocol": "chat-completions.text.v1",
      "adapter_id": "olivares.modelprovider.chat-text",
      "adapter_version": "1",
      "provider_ref": "operator-gateway",
      "model_ref": "chat-model-v1",
      "endpoint": "https://gateway.example.invalid/v1/chat/completions",
      "surface": "direct",
      "inference_geo": "us",
      "credential_audience": "https://gateway.example.invalid",
      "auth_scheme": "bearer",
      "credential_ref": "env:OLIVARES_CHAT_TOKEN",
      "allow_http": false,
      "max_request_bytes": 1048576,
      "max_response_bytes": 8388608,
      "timeout_ms": 30000
    }
  ]
}
```

The revision is SHA-256 over the ASCII domain
`olivares.model-gateway-profile.revision.v1`, one zero byte, and every profile
member except `revision` in the order below. Each member is framed as its ASCII
name, a zero byte, the decimal UTF-8 byte length, a zero byte, and the value.
Booleans are lowercase and integers are decimal. This Python standard-library
snippet calculates the example revision:

```python
import hashlib
profile = {
    "ref": "primary-chat",
    "tenant_ref": "11111111-1111-4111-8111-111111111111",
    "action": "text.generate",
    "protocol": "chat-completions.text.v1",
    "adapter_id": "olivares.modelprovider.chat-text",
    "adapter_version": "1",
    "provider_ref": "operator-gateway",
    "model_ref": "chat-model-v1",
    "endpoint": "https://gateway.example.invalid/v1/chat/completions",
    "surface": "direct",
    "inference_geo": "us",
    "credential_audience": "https://gateway.example.invalid",
    "auth_scheme": "bearer",
    "credential_ref": "env:OLIVARES_CHAT_TOKEN",
    "allow_http": False,
    "max_request_bytes": 1048576,
    "max_response_bytes": 8388608,
    "timeout_ms": 30000,
}
digest = hashlib.sha256(b"olivares.model-gateway-profile.revision.v1\0")
for name, value in profile.items():
    if isinstance(value, bool):
        value = "true" if value else "false"
    else:
        value = str(value)
    raw = value.encode("utf-8")
    digest.update(name.encode("ascii") + b"\0")
    digest.update(str(len(raw)).encode("ascii") + b"\0" + raw)
print("sha256:" + digest.hexdigest())
```

The matching routing policy stores both
`"execution_profile_ref":"primary-chat"` and the exact
`execution_profile_revision`. Changing the destination, credential reference,
audience, auth, surface, geography, model, adapter, or any bound requires a new
revision. The audience field binds configuration to an authority; it does not
claim that an external issuer has restricted the bearer token to that audience.
