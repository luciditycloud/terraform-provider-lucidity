terraform {
  required_providers {
    lucidity = {
      source = "registry.terraform.io/luciditycloud/lucidity"
    }
  }
}

provider "lucidity" {
  lucidity_dashboard_url          = "https://www.web.lucidity.dev/dashboard"
  lucidity_dashboard_account_name = "Acme Corp"

  # The refresh token itself is best supplied via refresh_token_file,
  # refresh_token_command, or the LUCIDITY_REFRESH_TOKEN environment
  # variable rather than hardcoded here — see the provider index docs.
}
