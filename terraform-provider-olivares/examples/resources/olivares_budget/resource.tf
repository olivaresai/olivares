# A global monthly spend cap that alerts at 80/90/100% (showback only).
resource "olivares_budget" "monthly" {
  name            = "monthly-global-cap"
  dimension       = "global"
  period          = "monthly"
  limit_micro_usd = 5000 * 1000000 # $5,000
  thresholds      = [0.8, 0.9, 1.0]
}

# A per-model cap that emits a hard-cap (block) signal on breach.
resource "olivares_budget" "opus" {
  name            = "opus-cap"
  dimension       = "model"
  key             = "claude-opus"
  period          = "monthly"
  limit_micro_usd = 2000 * 1000000 # $2,000
  action          = "block"
}
