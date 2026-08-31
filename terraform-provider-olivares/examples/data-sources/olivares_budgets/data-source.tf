data "olivares_budgets" "all" {}

output "total_monthly_cap_usd" {
  value = sum([for b in data.olivares_budgets.all.budgets : b.limit_micro_usd / 1000000 if b.period == "monthly"])
}
