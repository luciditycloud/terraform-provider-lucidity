# Adds 50 GiB of temporary free space to a Lucidity-managed instance, e.g.
# before a database restore. `terraform apply` requests the buffer and waits
# until it is ACTIVE; `terraform destroy` (or removing the block) revokes it.
# The buffer keeps costing money until it is revoked.
resource "lucidity_temp_buffer" "orders_db_restore" {
  instance_identifier {
    cloud_provider            = "AWS"
    cloud_provider_account_id = "123456789012"
    instance_id               = "i-0abcd1234efgh5678"
  }

  request_buffer_size_in_gib = 50
  request_id                 = "restore-orders-db-2026-10-23" # optional; generated if omitted
}

# Azure needs the resource group, and instance_id may be the VM name.
resource "lucidity_temp_buffer" "azure_vm" {
  instance_identifier {
    cloud_provider            = "AZURE"
    cloud_provider_account_id = "a1b2c3d4-5678-90ef-1234-3456abcdef01"
    instance_id               = "my-vm-instance"
    resource_group            = "my-resource-group"
  }

  request_buffer_size_in_gib = 100
}

# Only hold the buffer while a restore is planned: flip the variable to true
# before the restore and back to false afterwards to revoke it.
variable "restore_in_progress" {
  type    = bool
  default = false
}

resource "lucidity_temp_buffer" "on_demand" {
  count = var.restore_in_progress ? 1 : 0

  instance_identifier {
    cloud_provider            = "GCP"
    cloud_provider_account_id = "my-gcp-project-id"
    instance_id               = "gcp-instance-12345"
  }

  request_buffer_size_in_gib = 75
}

# Lucidity returns the buffer ID only once. Terraform keeps it in state; also
# expose it if a restore script or a later `terraform import` needs it.
output "orders_db_restore_buffer_id" {
  value = lucidity_temp_buffer.orders_db_restore.buffer_id
}
