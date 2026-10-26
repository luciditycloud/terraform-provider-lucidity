data "lucidity_temp_buffer" "restore" {
  buffer_id = "buf-abc123def456"
}

output "restore_buffer_ready" {
  value = data.lucidity_temp_buffer.restore.status == "ACTIVE"
}
