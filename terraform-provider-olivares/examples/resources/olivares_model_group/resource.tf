# A named set of models that policies and budgets can refer to by group instead of
# repeating every model id at each call site.
resource "olivares_model_group" "production" {
  name        = "production-models"
  description = "Models approved for production use"
  models      = ["gpt-4o", "claude-sonnet-4-5", "claude-haiku-3-5"]
}
