# Workspaces are the unit that scoped RBAC grants, budgets and deployments attach to.
resource "olivares_workspace" "dev" {
  name        = "development"
  description = "Development workspace for experimentation"
}
