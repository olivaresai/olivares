# Model access is evaluated by PRIORITY, lowest first, and the first matching rule wins.
# Keep allow rules at a lower priority than the deny rules that must override them.
resource "olivares_model_access" "allow_gpt4" {
  subject_type  = "group"
  subject_ref   = "engineering"
  model_pattern = "gpt-4*"
  effect        = "allow"
  priority      = 10
}

resource "olivares_model_access" "deny_expensive" {
  subject_type  = "group"
  subject_ref   = "interns"
  model_pattern = "claude-opus-*"
  effect        = "deny"
  priority      = 20
}
