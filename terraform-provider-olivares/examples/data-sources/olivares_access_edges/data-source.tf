# Read the R/RW access map, and the reconciled permitted-vs-observed drift.
data "olivares_access_edges" "map" {
  include_drift = true
}

output "unexpected_accesses" {
  value = [for d in data.olivares_access_edges.map.drift : d if d.kind == "unexpected_access"]
}
