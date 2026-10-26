# Changelog

All notable changes to this project are documented here. All entries below
are authored by **Devaansh Goenka**, the maintainer of this provider.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and
this project intends to follow [Semantic Versioning](https://semver.org/)
once it reaches a stable release cadence.

## [Unreleased]

_Author: Swapnesh Kumar._

### Added

- `lucidity_temp_buffer` resource: requests a temporary storage buffer on a
  Lucidity-managed instance (AWS, Azure, GCP) and waits until it is ACTIVE
  (polls every 30s, up to 30 minutes); destroy revokes it. Nothing is
  updatable in place. A buffer that ends FAILED or times out is kept in
  state as tainted so it can still be revoked. Import by buffer ID.
- `lucidity_temp_buffer` data source: reads a buffer's status by buffer ID.
- A `request_id` is always sent with the buffer request (generated as
  `terraform-<uuid>` if not set), so a 5xx is retried with the same ID —
  Lucidity documents it as the deduplication key.

### Changed

- Responses without the `{success,data,error,requestId}` envelope are now
  decoded directly, and flat `{code,message,details}` error bodies are
  parsed (code, message and details shown) instead of reported as
  `UNKNOWN`. The temporary buffer and refresh-token docs specify these
  shapes; enveloped responses behave as before.

## [0.1.7] - 2026-10-10

_Author: Devaansh Goenka._

### Added

- `terraform import` now recovers `aws_iam_external_id`: Lucidity's List
  API returns `externalId`, and `Read` copies it into state (older backends
  that omit the key, or a null value, leave state untouched, so there is no
  perpetual diff). A remote value that differs from state surfaces as drift.
- `aws_iam_external_id` is now updatable in place: `Update` sends
  `externalId` in the PATCH. Rotate the IAM trust policy first — Lucidity
  re-validates by assuming the role.
- After `terraform import`, `lucidity_product_list` and `aws_org_root_id`
  (not returned by List yet) are assumed to match config: empty state no
  longer forces a replace (`lucidity_product_list`) or errors
  (`aws_org_root_id`); the config value is simply recorded.
- When an onboard call ends in a 5xx, timeout or connection error, the
  provider re-lists instead of retrying: ACTIVE tenant → adopted and synced
  to config via the update API (with a warning); INACTIVE → error telling the
  user to contact support; not found → error with recommended actions.
- `lucidity_dashboard_display_name` now rejects a blank value client-side
  at plan time, instead of only surfacing a confusing generic "Nothing to
  update" error from Lucidity's API (Update silently omits an
  explicitly-blank value from its request body).
- A "Pending improvements" section in `README.md` tracking JIRA tickets
  filed against gaps in Lucidity's own API found during live QA testing
  (external ID rotation/verification, `aws_org_root_id` and
  `lucidity_product_list` update/read support, List API returning
  provider-auth fields, read-your-writes consistency, account-identity in
  auth responses, distinct onboarding error codes, update-time IAM
  validation).
- Client-side AWS account ID format validation: `cloud_provider_account_id`
  now requires exactly 12 digits when `cloud_provider = "AWS"`, checked in
  `ValidateConfig`. Lucidity's own API applies no format check here
  either, so a malformed value was previously only caught much later as
  the same ambiguous cloud-account-validation failure as a
  wrong-but-well-formed account number.

### Changed

- `ListTenants` now errors with `TruncatedListError` when `meta.totalCount`
  exceeds the number of tenants returned (a paginated/truncated list), instead
  of silently treating a missing tenant as "not found" — which could remove a
  real tenant from state or let a duplicate onboard through. The count is only
  compared against the same response (it includes INACTIVE tenants, so it
  never matches local state); a missing, zero or smaller count is ignored.

- The onboard POST is no longer auto-retried on 5xx (`Client.DoNoRetry5xx`);
  all other calls keep the existing backoff retry. Transport failures are now
  a typed `client.TransportError`, and `client.IsOutcomeUnknown` classifies
  5xx/timeouts/connection errors.
- `aws_iam_external_id` no longer forces replacement when changed.
- The "Tenant already exists" errors (pre-check and native 409) no longer
  claim it is only a `for_each` collision; they explain it may be a tenant
  from an earlier failed/timed-out apply and give the `terraform import`
  command.
- A 401 on an update that changes role/policy/external ID (or on onboard
  with no cloud-account message) is reported with a checklist of every
  possible cause (token, role name, trust policy, external ID, policy,
  permissions boundary/SCP, account ID, IAM propagation) instead of a bare
  expired-token message.
- Clarified the `skip_cloud_permission_check` documentation in `README.md`
  to match the schema description: it skips the permission check only,
  never Lucidity's baseline cloud-account-reachability validation.
- The "attempting to modify an INACTIVE tenant" error now tells the user
  to leave the Terraform change pending and contact
  support@lucidity.cloud, rather than only stating that reactivation
  isn't possible via API.

## [0.1.6] - 2026-09-12

_Author: Devaansh Goenka._

### Fixed

- `promote-to-stable.yml`'s tag-push step failed with "tag 'vX.Y.Z' already
  exists" — `fetch-depth: 0` in the checkout step also fetches Devaansh's
  own existing tag of the same name (pointing at the pre-rewrite commit),
  colliding with the new local tag this workflow creates for the
  promotion commit. Fixed with `git tag -f` to overwrite the local ref;
  never touches Devaansh's real tag, since this checkout is discarded at
  the end of the job.

## [0.1.5] - 2026-09-12

_Author: Devaansh Goenka._

### Fixed

- `promote-to-stable.yml` now also drops `.vscode/` from what's pushed to
  the stable repo — it's local editor config, not something that belongs
  in the promoted tree, same reasoning as `CLAUDE.md`/`RELEASING.md`.

## [0.1.4] - 2026-09-12

_Author: Devaansh Goenka._

### Fixed

- `promote-to-stable.yml`'s push to `luciditycloud/terraform-provider-lucidity`
  was silently authenticating as the job's own `GITHUB_TOKEN` instead of the
  `LUCIDITYCLOUD_PUSH_TOKEN` PAT, failing with "Permission ... denied to
  github-actions[bot]" regardless of how the PAT was scoped.
  `actions/checkout` injects an `Authorization` header for `github.com` via
  git config that takes precedence over credentials embedded in a push
  URL, even for an unrelated repo. Fixed by clearing that header
  (`git config --local --unset-all http.https://github.com/.extraheader`)
  before pushing to the stable repo.

## [0.1.3] - 2026-09-11

_Author: Devaansh Goenka._

### Added

- A manually-triggered workflow (`promote-to-stable.yml`) that promotes a
  tagged release from this (beta) repo into
  `luciditycloud/terraform-provider-lucidity`'s `main`, rewriting the Go
  module path and provider Registry address to the `luciditycloud`
  namespace along the way, and pushes a matching tag so that repo's own
  release pipeline cuts a signed stable release.
- `release.yml` now regenerates and verifies the Registry docs before
  signing, catching stale docs at the moment a version is actually cut
  (previously only checked in CI on pull requests).

### Changed

- Rotated the release-signing GPG key; new signatures use the new key from
  this release onward.
- Clarified in `README.md` that `luciditycloud/lucidity` is the beta Registry
  namespace and `luciditycloud/lucidity` is the official-stable one.

## [0.1.2] - 2026-09-11

_Author: Devaansh Goenka._ Everything below has landed on `main` since
v0.1.1.

### Added

- `lucidity_tenant` resource: full `Create`/`Read`/`Update`/`Delete`/
  `terraform import` lifecycle for managing a Lucidity tenant (a connected
  cloud account), backed by a new `internal/client/tenant.go` covering the
  real onboard/list/update/deboard endpoints.
- `lucidity_tenants` data source, listing every tenant under the caller's
  account.
- Azure and GCP support for `lucidity_tenant` via `terraform import` +
  update — `cloud_provider` now accepts `AWS`, `AZURE`, or `GCP`. Onboarding
  a brand-new resource remains AWS-only for now, since Lucidity's Azure/GCP
  onboarding API isn't complete yet (pending a future Lucidity release, not
  a permanent restriction). Added `azure_service_principal_id` and
  `azure_directory_id` fields for updating an already-imported AZURE tenant.
- Three-tier destroy safety for `lucidity_tenant`
  (`lucidity_dashboard_account_delete_protection` +
  `lucidity_account_destroy_behavior`), since deboarding a tenant is
  irreversible via API.
- `lucidity_dashboard_account_name` provider attribute (required), recording
  which Lucidity dashboard account a refresh token is expected to belong
  to. Cross-validation against the token itself is not yet implemented —
  no Lucidity endpoint currently exposes which account a token belongs to.
- Conditional field validation via a `ValidateConfig` implementation:
  AWS-only onboarding fields (`aws_iam_external_id`, `aws_iam_role_name`,
  `aws_iam_policy_name`, `lucidity_product_list`) are only required when
  `cloud_provider` is `AWS`, so an AZURE/GCP resource (import-only) doesn't
  need to set meaningless AWS IAM fields just to validate.
- `aws_org_root_id` is re-added as an optional `lucidity_tenant` attribute
  (sent to Lucidity on onboard best-effort, since it's absent from the
  documented onboard/update request schema). Changing it after creation is
  a `terraform plan`-time error via a `ModifyPlan` implementation, instead
  of only surfacing once `apply` reaches `Update()`. A future release of
  this provider may add support for modifying it in place, if and when
  Lucidity exposes an update mechanism for it.
- Generated Registry documentation (`docs/index.md`, `docs/resources/`,
  `docs/data-sources/`) via `tfplugindocs`, and a recreated `examples/`
  directory in its expected layout.
- A CI check that regenerates the docs and fails the build if they've
  drifted from the schema or `examples/`.
- Client-side UUID format validation on `aws_iam_external_id`. Live testing
  against a real Lucidity account confirmed the onboard API applies no
  format check on this field server-side at all — a malformed value was
  previously only caught much later, and ambiguously, as an AssumeRole
  trust-policy mismatch. Now rejected at `terraform plan` time instead.

### Changed

- Renamed `dashboard_login_url` (provider) to `lucidity_dashboard_url`.
- Renamed several `lucidity_tenant` attributes for clarity — Terraform-side
  only; the underlying API request/response field names are unchanged:
  - `display_name` → `lucidity_dashboard_display_name`
  - `product_list` → `lucidity_product_list`
  - `external_id` → `aws_iam_external_id`
  - `account_delete_protection` → `lucidity_dashboard_account_delete_protection`
  - `destroy_behavior` → `lucidity_account_destroy_behavior`
  - `aws_root_id` → `aws_root_account_id` → `aws_org_root_id` (re-added as an
    optional attribute; sent to Lucidity on onboard best-effort even though
    it's absent from the documented onboard/update request schema)
- `aws_org_root_id`'s immutability check moved from `Update()` (an
  apply-time failure) to `ModifyPlan` (a plan-time failure), so an attempt
  to change it is caught by `terraform plan` rather than only once `apply`
  reaches `Update()`.

### Fixed

- A `401` response for "the cloud account could not be validated" (a
  business-logic failure distinct from a bad/expired access token, even
  though both share HTTP 401 and `error.code` `UNAUTHORIZED`) was
  previously masked behind the generic expired-refresh-token error message,
  losing the real error message and `requestId`. Now surfaced correctly.
- `refreshFromList` (the shared Create/Update post-mutation list-and-match)
  had no retry tolerance for Lucidity's List-endpoint propagation delay
  after a mutation — a genuinely successful onboard/update could come back
  as a hard Terraform error with the tenant left real but completely
  untracked in state. Fixed with a bounded retry, widened twice as live
  testing showed the propagation delay regularly running from tens of
  seconds to a couple of minutes: 4×2s → 8×3s → the current 10×15s
  (150s total).
- A stale-data acceptance bug in that same retry loop: after a successful
  `Update()` PATCH, a retry could match a List entry that was itself a
  pre-update snapshot, writing stale field values into state and triggering
  Terraform's "Provider produced inconsistent result after apply" error.
  Fixed by adding a `fresh` verification callback so `Update()` only accepts
  a list entry matching what was just requested, retrying otherwise.

### Testing

- A ~60-case live QA test matrix run against real AWS accounts and a real
  Lucidity account, covering onboard/modify/deboard/import/data-source/
  concurrency scenarios for `lucidity_tenant`/`lucidity_tenants`. Results
  tracked per provider section in `docs/qa-testing/`.
- Mock-backed resource lifecycle tests
  (`internal/provider/resource_tenant_lifecycle_test.go`) driving
  `Create`/`Read`/`Update`/`Delete`/`ImportState` directly against a
  stateful mock server.

### Docs

- Reorganized documentation around the provider's functional sections
  (Authentication management, Account management, and more planned):
  `README.md` now has usage sections for both, plus a sections/roadmap
  table; QA test logs moved from one flat file into
  `docs/qa-testing/<section>.md`, one per section.

### Research / design record

- Reconciled the Phase 2 tenant-resource design against the real Public
  Tenant API doc and live-tested against a real Lucidity account
  (2026-09-06): confirmed `CONFLICT` error-code semantics, confirmed the
  update API is a single unified `PATCH` endpoint (not the two-API split
  originally planned), confirmed Lucidity's rate limits, confirmed onboard
  is create-only.

## [0.1.1] - 2026-09-05

_Author: Devaansh Goenka._

### Changed

- Replaced the optional, free-form `base_url` provider attribute with a
  required `dashboard_login_url` (closed set of 5 known Lucidity
  deployments, validated at plan time) — a customer on a deployment not yet
  in the table needs a provider update rather than a typo-prone free-form
  string.
- Added a configurable `proactive_refresh_buffer_minutes` provider
  attribute (default 3 minutes before the 15-minute access-token expiry).
- Moved sample/testing Terraform configuration out of the repository.

### Fixed

- Fixed a retry-budget bug: the 5xx-backoff retries and the one sanctioned
  401-forced-refresh retry used to share one bounded attempt counter. If
  three straight `500`s consumed all-but-one attempt and the final attempt
  came back as a first-time `401`, the forced refresh happened but the
  retry using the fresh token never did — it fell through to a generic
  error instead of succeeding. Gave the 401 retry its own budget,
  independent of the 5xx counter.
- The refresh-token call itself now retries `5xx` with the same backoff
  policy as every other endpoint (previously it didn't retry at all).
- The single-flight refresh's initiating call now runs under
  `context.WithoutCancel`, so one caller cancelling its own context can no
  longer abort a shared refresh that other goroutines are waiting on.
- The concurrency semaphore now respects context cancellation instead of
  blocking unconditionally on an already-cancelled/timed-out context.
- `ForceRefresh` now skips an avoidable extra refresh call when another
  goroutine has already refreshed the token first.
- Refresh-token exchanges now produce `TF_LOG=DEBUG` output, matching every
  other API call (previously silent).

## [0.1.0] - 2026-08-24

_Author: Devaansh Goenka._ Initial release: Phase 1, authentication only.

### Added

- Repo scaffold: Go module, HashiCorp Terraform Plugin Framework wiring,
  MPL-2.0 license, GitHub Actions CI, GoReleaser configuration with
  dedicated GPG-key signing.
- Token manager (`internal/client/auth.go`): exchanges a long-lived refresh
  token for a 15-minute access token, proactively renews it ahead of
  expiry, single-flight refresh under concurrent Terraform operations, and
  forces exactly one refresh-and-retry on a `401` before surfacing an
  error.
- HTTP client (`internal/client/client.go`): required auth headers on every
  call, `{success, data, error, requestId}` envelope parsing, exponential
  backoff on `5xx` (never auto-retrying `4xx`), a client-side concurrency
  semaphore, and secret scrubbing so refresh/access tokens never appear in
  logs (including at `TF_LOG=DEBUG`).
- Provider configuration: `refresh_token` / `refresh_token_file` /
  `refresh_token_command` (exactly one, or the `LUCIDITY_REFRESH_TOKEN`
  environment variable) and `max_parallel_requests`.
- Unit tests against a local mock server covering token refresh, proactive
  renewal, single-flight behavior, 401-retry, and secret scrubbing.
