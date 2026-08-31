data "olivares_server_info" "this" {}

output "control_plane_version" {
  value = data.olivares_server_info.this.version
}
