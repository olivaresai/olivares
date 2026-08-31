# A grant with no scope is global. Adding scope/scope_ref confines the role to one
# workspace, which is how a viewer is kept from reading every workspace at once.
resource "olivares_rbac_grant" "alice_admin" {
  subject_type = "user"
  subject_ref  = "alice@example.com"
  role         = "admin"
}

resource "olivares_rbac_grant" "team_viewer" {
  subject_type = "group"
  subject_ref  = "engineering"
  role         = "viewer"
  scope        = "workspace"
  scope_ref    = "ws-prod"
}
