# Fan critical findings out to the on-call destination, deduped within 5 minutes.
resource "olivares_notification_route" "crit_to_pager" {
  name         = "crit-to-pager"
  destination  = "pagerduty-oncall"
  match_types  = ["finding"]
  min_severity = "critical"

  dedup_window_seconds = 300
  priority             = 10
}
