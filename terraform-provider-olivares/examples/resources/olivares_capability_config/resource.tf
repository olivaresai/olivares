# Configuration of an MCP server connection. Secrets are referenced by locator
# only (never cleartext); the engine rejects an endpoint or ref that embeds a
# credential.
resource "olivares_capability_config" "github_mcp" {
  server_ref = "github-mcp"
  transport  = "http"
  endpoint   = "https://mcp.internal/github"
  scope      = "team:platform"

  secret_refs = [
    {
      name     = "token"
      ref_kind = "vault"
      ref      = "secret/data/mcp/github#token"
      hint     = "ghp_…last4"
    }
  ]
}
