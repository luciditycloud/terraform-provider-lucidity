# terraform-provider-lucidity

Terraform provider for Lucidity (cloud storage optimization platform). Goal: publish
to the public Terraform Registry as a community provider, built to Partner-tier
quality. All design decisions below are FINAL from the planning phase — do not
revisit them without asking the maintainer.

## Project facts

- **Language/stack:** Go + Terraform Plugin Framework (NOT legacy SDKv2).
- **License:** MPL-2.0.
- **Publishing:** community provider from the maintainer's personal GitHub repo.
  Releases signed with a dedicated GPG key held by the maintainer.
- **API docs:** `docs/api/` contains the three Lucidity API reference PDFs
  (auth refresh, getting started, Public Tenant API). Treat these as the source
  of truth for endpoints, fields, and error tables.

## Product sections

Lucidity's own product is organized into functional sections; this
provider's phases map onto them, and future phases will keep following this
list rather than being organized purely around Terraform resource
boundaries. QA test logs are organized the same way — see
[docs/qa-testing/README.md](docs/qa-testing/README.md).

| Section | Provider phase | Status |
|---|---|---|
| Authentication management | Phase 1 | Shipped |
| Account management | Phase 2 | Shipped |
| User management | — | Not yet implemented |
| Permission validation (cloud IAM vs. Lucidity requirements) | — | Not yet implemented |
| Agent install and status validation | — | Not yet implemented |
| Buffer policy management | — | Not yet implemented |
| Temporary buffer management | — | Not yet implemented |
| Onboarding and Disk management | — | Not yet implemented |

## Current scope — PHASE 1 (auth) + PHASE 2 (tenant resource), both shipped

**Update (2026-09-07):** Phase 2 is now implemented — see the "PHASE 2"
section below for what shipped and what's still open (mainly: no live test
of a full onboard→update→deboard cycle against a real AWS account yet).
Phase 1 definition of done:
- `terraform plan` with an empty config + provider block succeeds against sandbox.
  **Caveat (discovered 2026-08-21):** Terraform prunes a provider from the
  plan graph when nothing references it, so with zero resources/data sources
  this never actually calls `ValidateProviderConfig`/`ConfigureProvider` —
  it would "succeed" even with broken auth wiring. Real coverage of
  `ConfigValidators`/`Configure` comes from driving the `tfprotov6.ProviderServer`
  RPCs directly in `internal/provider/provider_test.go`, not from this check.
  This gap closes naturally once Phase 2 adds a resource/data source.
- Gated smoke test (`TF_ACC=1`) performs one authenticated `GET /external/client/api/v1/tenants`.
- CI green (build + unit tests on PR).

### Phase 1 deliverables

1. Repo scaffold: Go module, Plugin Framework wiring, MPL-2.0 LICENSE,
   GitHub Actions CI, GoReleaser config (GPG signing; include RELEASING.md
   with dedicated-key generation instructions).
2. `internal/client/auth.go` — token manager:
   - Exchanges long-lived refresh token via `POST /external/api/v1/auth/user-token/refresh`.
   - Access tokens expire in 15 min (`client.AccessTokenTTL`) → proactive
     refresh at the 12-minute mark by default (`client.DefaultProactiveRefreshAge`),
     overridable via the provider's `proactive_refresh_buffer_minutes`
     attribute (see Provider config block below).
   - Mutex-protected single-flight refresh (Terraform runs concurrent ops).
   - On 401: force one refresh + one retry; if still 401, surface error. This
     retry is independent of the 5xx-backoff budget below — see bug note.
   - The refresh call itself retries 5xx with the same backoff policy as
     every other endpoint (fixed 2026-09-03 — previously `doRefresh` didn't
     retry at all, inconsistent with the policy below).
   - Access tokens live in memory only. Never persisted, never logged.
   - **Round 2 fixes (2026-09-05):**
     - `TokenManager` gained a `DebugLog` hook (wired from `Client.logDebug`
       in `NewClient`), logging `status`/`attempt` per refresh call —
       previously refresh-token exchanges produced zero `TF_LOG=DEBUG`
       output at all, unlike every other API call.
     - The single-flight refresh's initiating call now runs under
       `context.WithoutCancel(ctx)`. Previously it used whichever caller's
       context happened to start the refresh; that caller cancelling its own
       context (e.g. Terraform tearing down one operation) aborted the
       shared refresh for every other goroutine waiting on it too. Safe
       because `NewClient`'s `http.Client` already enforces its own 30s
       `Timeout` independent of context.
     - `ForceRefresh(ctx, failedToken)` now takes the token that actually
       got the 401 and skips the refetch if the cache no longer holds it
       (another goroutine already refreshed first) — returns that fresher
       token directly instead of an avoidable extra refresh call.
3. `internal/client/client.go` — HTTP client:
   - Required headers on EVERY call: `X-Authtype: lucidity_access_token`,
     `Authorization: <raw token>` (NO "Bearer " prefix — enforce in one place),
     `Content-Type: application/json`.
   - Parse standard envelope `{success, data, error{code,message}, requestId}`.
     All error diagnostics MUST include error.code, error.message, requestId.
   - Retry: 500s with exponential backoff; NEVER auto-retry 400s.
     **Exception (2026-10-10): the onboard POST is never auto-retried on a
     5xx** (`Client.DoNoRetry5xx`) — a 5xx doesn't prove the tenant wasn't
     created, and a blind retry after a successful create surfaces a masking
     409 (seen in the NYL-Prod incident). `client.IsOutcomeUnknown(ctx, err)`
     is true for 5xx and transport failures (`client.TransportError`:
     timeouts/connection errors) unless the caller's ctx is done; Create()
     then re-lists (`recoverUnknownOnboard`): ACTIVE → adopt + sync display
     name/external ID via PATCH (+ warning); INACTIVE → "already exists and is
     in inactive state, contact support"; not found → error with recommended
     actions. Deliberate deviation from the API doc's "5xx are safe to resend".
     **CONFLICT confirmed (2026-09-06): "Already a tenant exists with the
     provided details."** Not transient — never retry it (already the
     behavior, unchanged). This is a real, actionable business-logic state
     Phase 2's `lucidity_tenant` Create() needs to handle explicitly (likely
     the same pattern as the existing INACTIVE-entry pre-check below: a
     clear error naming the conflicting account rather than a generic
     APIError). Other still-unmapped codes continue to render generically
     but completely.
   - **Bug fixed 2026-09-03:** the 5xx-backoff retries and the one sanctioned
     401-forced-refresh retry used to share one bounded attempt counter. If 3
     straight 500s consumed all-but-one attempt and the last attempt came
     back as a first-time 401, the forced refresh happened but the retry
     using the fresh token never did — fell through to a generic error
     instead of succeeding. Fixed by giving the 401 retry its own budget,
     independent of the 5xx counter (see
     `TestClient_401OnFinalRetryAttemptStillRetriesWithFreshToken`).
   - Client-side concurrency semaphore, default 10, from provider config.
     `max_parallel_requests` is validated (`AtLeast(1)`) — a 0/negative value
     is a config-time error, not a silent fallback to the default. Semaphore
     acquisition respects context cancellation (fixed 2026-09-05 — previously
     `c.sem <- struct{}{}` blocked unconditionally even with an
     already-cancelled/timed-out `ctx`).
   - Secret scrubbing: refresh + access tokens redacted from ALL logging
     including TF_LOG=DEBUG. Add a test that greps captured debug output
     for token material.
4. Provider config block:
   ```hcl
   provider "lucidity" {
     refresh_token         = "…"  # Sensitive, discouraged (ends up in .tf/state)
     refresh_token_file    = "…"  # Path to a file containing only the token
     refresh_token_command = "…"  # Shell command; trimmed stdout is used as the token
     lucidity_dashboard_url   = "…"  # REQUIRED, no default — must be one of the 5 known values below
     max_parallel_requests = 10   # optional, must be >= 1
     proactive_refresh_buffer_minutes = 3  # optional, 1-14, default 3 (renew at the 12-min mark)
     lucidity_dashboard_account_name = "…"  # REQUIRED (locked 2026-09-06, renamed 2026-09-07) — see below
   }
   ```
   **Token-source precedence (locked 2026-08-20):** exactly one of
   `refresh_token` / `refresh_token_file` / `refresh_token_command` may be set.
   A `ConfigValidator` MUST hard-error at validate time if more than one is
   set, naming all attributes that were set. If none of the three are set,
   fall back to the `LUCIDITY_REFRESH_TOKEN` env var. If nothing resolves a
   token at all, error naming all three attributes and the env var.

   - `refresh_token_file`: read the file, trim trailing whitespace/newline.
     Missing/unreadable file → error naming the path (never its contents).
   - `refresh_token_command`: run via `sh -c` (unix) / `cmd /C` (windows) so
     pipelines and env expansion work as users expect (mirrors AWS CLI's
     `credential_process` / kubectl exec-auth pattern). Trim trailing
     whitespace/newline from stdout. Enforce a 30s timeout. Non-zero exit →
     surface stderr in the diagnostic. Never log stdout (it's the secret) —
     same scrubbing rule as access/refresh tokens elsewhere.
   - `lucidity_dashboard_url` (locked 2026-09-03, renamed 2026-09-07 from
     `dashboard_login_url` for clarity — the "lucidity_" prefix matches
     `lucidity_dashboard_account_name` and makes it unambiguous this is a
     Lucidity-specific setting, not a generic one): **required, no default.**
     Replaces the old optional/free-form `base_url` entirely — the maintainer
     chose a closed, validated set over a string the user could mistype into
     a working-looking but wrong host. A `stringvalidator.OneOf` rejects
     anything outside the table below at validate time, before Configure
     ever runs; a missing value is also a validate-time error (Required, no
     default). The Dashboard Login URL → API Base URL mapping is defined in
     the provider itself (`internal/provider/deployment.go`), not read from
     user input:

     | Dashboard Login URL | API Base URL |
     |---|---|
     | `https://www.web.lucidity.dev/dashboard` | `https://dash-back.lucidity.dev` |
     | `https://web-azurepls.lucidity.cloud/dashboard` | `https://dashboard-azurepls.lucidity.cloud` |
     | `https://app.lucidity.cloud` | `https://app.lucidity.cloud` |
     | `https://in.app.lucidity.cloud` | `https://in.app.lucidity.cloud` |
     | `https://eu.app.lucidity.cloud` | `https://eu.app.lucidity.cloud` |

     Deliberate accepted trade-off: no free-form override attribute remains.
     A customer on a deployment not yet in this table can't configure the
     provider until a new release adds it — the maintainer chose closed
     validation over that escape hatch.
   - `proactive_refresh_buffer_minutes` (added 2026-09-03): optional Int64,
     `int64validator.Between(1, 14)`. How many minutes before the 15-minute
     access-token expiry (`client.AccessTokenTTL`) to proactively renew it.
     Unset → `client.DefaultProactiveRefreshAge` (3-minute buffer, i.e. renew
     at the 12-minute mark) — today's exact default behavior, unchanged.
     Deliberately made user-configurable per the maintainer's explicit call,
     overriding the initial recommendation to keep it an internal-only
     constant (the margin is a client-side implementation detail, not
     deployment-specific like `lucidity_dashboard_url`) — kept here for the
     record in case it's revisited.
   - `lucidity_dashboard_account_name` (locked 2026-09-06, renamed
     2026-09-07 from `account_name` for clarity — the old name was
     ambiguous against cloud/tenant account concepts): **required.** The
     maintainer wants `Configure()` to validate that the refresh token
     actually belongs to the declared account — specifically to catch
     "wrong refresh token from the wrong account" misconfiguration before
     any tenant operation runs. **Blocked on a real gap, not yet
     implementable:** live-testing on 2026-09-06 checked response headers
     and bodies across refresh/list/onboard calls and found no field or
     endpoint anywhere that identifies which dashboard account a token
     belongs to. The `lucidity_dashboard_account_name` attribute itself
     (required, plain string) can and does go in now; the actual
     cross-validation logic cannot be written until either Lucidity exposes
     such an endpoint or an alternative signal turns up. Track this as the
     top open item below, not a silently-dropped requirement.

   Rationale: rather than the provider baking in bespoke Vault/AWS-SM/
   Azure-KV/GCP-SM client integrations (real maintenance surface for a
   community provider), `refresh_token_command` is one generic escape hatch —
   docs show recipes like `vault kv get -field=token secret/lucidity` per
   backend. Fetching a secret via a Terraform *data source* and feeding it
   into `refresh_token` still works, but the docs must call out that the
   fetched value then lands in state as that data source's attribute (only
   as safe as your state encryption).
5. Unit tests against a local mock server replaying recorded envelope responses:
   token refresh, proactive renewal, single-flight, 401-retry, expired refresh
   token message, scrubbing (must cover `refresh_token_command` stdout too).
   Plus: multiple-sources-set → validator error; file read failure; command
   non-zero exit; command timeout; every `lucidity_dashboard_url` in the table
   maps to its correct `base_url`; an unrecognized or missing
   `lucidity_dashboard_url` is a validate-time error.
6. Provider index docs covering all credential-supply options: env var (CI
   default), `refresh_token_file`, `refresh_token_command` (with per-backend
   recipes for Vault/AWS SM/Azure KV/GCP SM), data-source-fed `refresh_token`
   (with state-encryption caveat), tfvars (discouraged, local only).
   Hardcoding in .tf: documented as never-do.
   **Done (2026-09-06):** all four backend recipes, both attribute- and
   data-source-fed options per backend, and the tfvars/hardcoding guidance
   are in the README's "Supplying the refresh token" section. **Done
   (2026-09-07):** the `tfplugindocs`-generated docs page too — see the Style
   section note below.

### Required error message (401 / expired refresh token)

> authentication failed (401). Check your refresh token — make sure it has not
> expired (default lifetime 30 days) and was not revoked. Generate a new token
> from the Lucidity dashboard (Users → your admin user → Generate Token).

## PHASE 2 — tenant resource (implemented 2026-09-07)

Everything below is decided. **Implemented 2026-09-07:** `lucidity_tenant`
(Create/Read/Update/Delete/Import) and `lucidity_tenants` live in
`internal/provider/resource_tenant.go` / `datasource_tenants.go`, backed by
`internal/client/tenant.go`. Verified via mock-server unit tests
(`internal/client/tenant_test.go`, `internal/provider/resource_tenant_test.go`)
and `terraform validate`/`plan` against the real compiled schema with
`docs/examples/lucidity-tenants.tf` (dev-override, no live API calls — see
Open Question #6 below for what's still untested against a real account).
Onboard endpoint: `POST /external/client/api/v1/tenants/onboard` → `201
Created`. Deboard endpoint: `PUT /external/client/api/v1/tenants/deboard` →
`200 OK`. (List and Update's paths were already recorded above.)

### `lucidity_tenant` resource

- One resource per cloud account. Onboarding (Create) is AWS-only for now
  (Azure/GCP onboarding isn't complete on Lucidity's side yet and returns
  `400 INVALID_REQUEST` — confirmed by the maintainer 2026-09-07: full
  Azure/GCP onboarding support is pending a future Lucidity release, not a
  permanent restriction). List/Deboard/Update accept all three providers
  already. See `docs/examples/lucidity-tenants.tf` for plain,
  non-abstracted example usage — one explicit resource block per account, no
  locals map/for_each (the maintainer explicitly rejected a JSON-like
  grouping structure here, twice, in favor of writing it "as per terraform").
- **Multi-cloud support (added 2026-09-07):** `cloud_provider` accepts `AWS`,
  `AZURE`, or `GCP` at the schema level. Since Create() only ever handles
  AWS, an AZURE/GCP resource can only enter Terraform via `terraform import`
  of a tenant that already exists on Lucidity some other way — Create()
  itself rejects a non-AWS `cloud_provider` outright with a clear error
  pointing at import, before making any API call. `cloud_provider_account_id`
  holds whichever identifier that provider uses: AWS account ID, Azure
  subscription ID (or name), or GCP project ID.
  - The AWS-only fields (`aws_iam_external_id`, `aws_iam_role_name`,
    `aws_iam_policy_name`, `lucidity_product_list`) are `Optional` at the
    schema level, not `Required` — a blanket `Required` would force every
    AZURE/GCP resource to fill in meaningless AWS IAM fields just to
    validate. Instead, a `ResourceWithValidateConfig.ValidateConfig`
    implementation enforces "if `cloud_provider == AWS`, these must be set"
    as a conditional invariant, checked on every plan.
  - New `azure_service_principal_id`/`azure_directory_id` fields (in
    `cloud_entity_information`, both optional, both updatable in-place) —
    match the real Update API's AZURE fields. Not usable at onboard time
    (Azure onboarding isn't supported); only meaningful for updating an
    imported AZURE tenant.
  - GCP's real Update API only accepts `displayName` — no GCP-specific auth
    fields exist to add.
- Computed attributes: `tenant_id`, `status`. New (2026-09-06):
  `cloud_entity_name` should also become computed — the provider-side
  account name, only available from List, not from onboard's response.
- Non-empty list attribute (**required for AWS only**, enforced via
  `ValidateConfig` — see above, not schema `Required`): `lucidity_product_list`
  (new field, not in earlier planning). Only `AUTOSCALER` is valid today —
  recommend validating it as a closed set the same way `lucidity_dashboard_url`
  is (`stringvalidator`-style), consistent with this project's established
  philosophy. Request field is `productList`; the onboard *response* field
  is `products` (different name) — don't conflate the two in Go struct tags.
  Not accepted by Update for any provider, and never returned by List, so it
  can't be recovered by import either (assumed to match config after import,
  see "Import").
- `aws_iam_external_id` (in `cloud_entity_information`, **required for AWS
  onboarding, `Optional` at the schema level** — enforced via `ValidateConfig`
  instead, so AZURE/GCP resources aren't forced to set it): a value the
  practitioner generates (must be a UUID — enforced by a
  `stringvalidator.RegexMatches` schema validator added 2026-09-11, since
  live testing confirmed Lucidity's own API applies no format check on this
  field server-side and a malformed value would otherwise only surface much
  later, ambiguously, as an AssumeRole trust-policy mismatch) and places it
  in the target IAM role's trust policy; Lucidity sends it
  on every `AssumeRole`. **Updated 2026-10-10: no longer write-only** —
List returns it (`externalId`, string|null) and PATCH accepts it, so it is
read on import/refresh (drift detection) and updatable in place; the old
RequiresReplace is gone. Rotation order is the user's job: update the AWS
trust policy first, then Terraform (Lucidity re-validates via AssumeRole).
Create/Update keep the planned value in state; only `Read` adopts the remote
value (see "Import").
- `aws_org_root_id` (re-added 2026-09-07, **optional**, top-level attribute —
  sibling of `lucidity_dashboard_display_name`/`lucidity_product_list`, NOT nested inside
  `cloud_entity_information`, per the maintainer's explicit placement in the
  approved reference example): the AWS Organization root/management account
  ID for the account being onboarded. **Behavior changed 2026-09-07 per the
  maintainer's explicit instruction: now sent to Lucidity on onboard when
  set**, best-effort — it remains absent from both the onboard and update
  field tables in the current Public Tenant API doc (confirmed again
  2026-09-07 against the doc directly, not just the 2026-09-06 live-test
  notes), so Lucidity may silently ignore it or, if it validates request
  bodies strictly, reject the onboard call outright. This is a deliberate,
  flagged risk, not an oversight — nothing about the current doc suggests
  Lucidity accepts an extra field named `awsRootId`. **Not used for grouping
  or resolution** — the tenant is still identified purely by `cloud_provider`
  + `cloud_provider_account_id`. Useful for audits, and for cases where a
  shared IAM role/policy is assumed across multiple member accounts under
  the same org. **Cannot be modified once set:** there is no update
  mechanism for it (documented or otherwise, and it's not returned by List
  either), so a `ModifyPlan` implementation (moved there from `Update()` on
  2026-09-07, specifically so this is caught at `terraform plan` time
  instead of only surfacing once `apply` reaches `Update()`) rejects a
  changed value with a plan-time error rather than silently dropping it or
  forcing a replace (replacing over a pure metadata field would mean an
  irreversible deboard/re-onboard cycle for no functional reason). **A
  future release of this provider may add support for modifying it in
  place**, if and when Lucidity ever documents a path for it.
- Immutable (RequiresReplace, gated by protection below): `cloud_provider`,
  `cloud_provider_account_id`, `lucidity_product_list`
  (not listed as updatable in the real Update API's field table either —
  see "Update APIs").
- **Updates go through the real `PATCH /tenants` endpoint (see "Update
  APIs" below) — not onboard re-trigger.** Onboard is create-only; it does
  not support re-triggering at all (an existing tenant, ACTIVE or INACTIVE,
  always gets `409 CONFLICT`). The old "one-change-at-a-time" rule is
  dropped: `PATCH` is a normal partial-update endpoint with no stated
  restriction on how many fields you send in one call, and its old rationale
  (re-trigger only supported one field) no longer exists. `Update()` sends
  every changed field in a single `PATCH` call.
- `lucidity_dashboard_display_name`: NEVER RequiresReplace, updatable in-place immediately —
  the update API this was waiting on has shipped, so there's no "plan-time
  ERROR until the API ships" fallback path to build anymore.

### Deboard safety (business-critical — deboarding is IRREVERSIBLE)

An INACTIVE tenant CANNOT be reactivated via API — only Lucidity support can
restore it. Deboarding an account with running services causes disruption.
Confirmed by the current doc: deboard is idempotent (`200 OK` either way),
distinguishing `DE_BOARDED` (was active) from `ALREADY_DE_BOARDED` (no-op) in
the response message. Three-tier destroy behavior:

| Config | `terraform destroy` result |
|---|---|
| `lucidity_dashboard_account_delete_protection = true` (DEFAULT) | Hard error before any API call |
| protection=false, `lucidity_account_destroy_behavior = "forget"` (default) | Remove from state only; tenant stays ACTIVE; warning emitted |
| protection=false, `lucidity_account_destroy_behavior = "deboard"` | Actual deboard call — the ONLY path to it |

Carry loud warnings in: registry docs (admonition block), attribute
descriptions, code comments above Delete(), and runtime diagnostics (even the
successful "forget" path warns). Tests must assert all three paths.

### INACTIVE handling

- Read: fetch full tenant list (no GET-by-ID exists), match on
  provider+account_id. ACTIVE → refresh state. INACTIVE (or DECOMMISSIONED —
  synonym per Lucidity, single code path) → KEEP in state, set status, emit
  ERROR with support-contact message and `terraform state rm` escape hatch.
  No match → remove from state with re-onboarding warning.
- Create: pre-check list for an INACTIVE entry with same account id → fail
  fast with:
  > cloud account <id> was previously deboarded and is INACTIVE on Lucidity.
  > Once a tenant is made inactive it cannot be made active again —
  > re-onboarding via API is not possible. Contact Lucidity support to
  > restore this account.

  **Locked 2026-09-06:** keep this pre-check even though onboard itself now
  natively returns `409 CONFLICT` for exactly this case too (distinct
  message: "An already deboarded (INACTIVE) tenant exists for
  cloudProviderAccountId '…'; re-onboarding support does not exist right
  now."). Maintainer's explicit call: the list-and-match pre-check stays as
  designed; onboard's native 409 is a defense-in-depth backstop for the race
  window between the pre-check and the actual onboard call, not a
  replacement for it. The *other* CONFLICT variant — "A tenant already
  exists for cloudProviderAccountId '…'" (tenant is ACTIVE, not INACTIVE) —
  needs the same explicit-error treatment in Create(), without the
  support-contact framing (an active duplicate isn't a Lucidity-support
  situation, it's a config error — most likely a `for_each` key collision).

### Update APIs — real design, replaces the old two-API assumption (rewritten 2026-09-06)

The actual API is a single unified endpoint, architecturally different from
what earlier planning assumed (a global by-name rename + a separate
tenant-ID-scoped one needing an "Account" param). That older description is
gone from current docs entirely — treated as superseded, not implemented.

- **Endpoint:** `PATCH /external/client/api/v1/tenants` → `200 OK`.
- Tenant identified by `cloudProvider` + `cloudProviderAccountId`, resolved
  under the caller's account from the token — same pattern as onboard/list/
  deboard. **No Account param anywhere in this API.** Tenant must be
  `ACTIVE`.
- Partial update: send only what changes. Must send `displayName` and/or at
  least one provider auth field, or the request is rejected.
- Cloud `authInfo` is **merged** — only the provider fields you send are
  overwritten; the rest of the existing `authInfo` is preserved.
- `externalId` **can be changed via update** (API change, confirmed
  2026-10-10; previously ignored forever) and is returned by List (string or
  null; key absent on older backends — `TenantListItem.ExternalIDReported`
  tells them apart). Lucidity re-validates by AssumeRole, so the AWS trust
  policy must be updated first.
- Per-provider updatable fields: **AWS** — `awsIAMRoleName`,
  `awsIAMPolicyName` (ARN rebuilt, existing `externalId` preserved);
  **AZURE** — `azureServicePrincipalId`, `azureDirectoryId` (merged into
  `authInfo`); **GCP** — `displayName` only.
- No re-trigger concept exists — see the `lucidity_tenant` resource bullets
  above.

### `skip_cloud_permission_check` attribute (optional, default false)

Doc note (maintainer's wording): "Only disable permission validation if using
a custom permission set or if your permission set is not yet up to date with
the latest Lucidity permissions. Note: this ignores permission validation
entirely — even if the account connects successfully, you may run into
permission issues later on."

**Live-tested 2026-09-06:** confirmed this only skips the *permission*
check — Lucidity still performs a baseline cloud-account-reachability
validation regardless of this flag. A synthetic/unreachable AWS account
number (tested twice, consistent) fails with `401 UNAUTHORIZED` /
"Authentication failed: the cloud account could not be validated." even
with `skipCloudPermissionCheck: true`. No orphaned tenant record is left
behind by a failed attempt. Create()'s error handling needs a case for this
401 distinctly from the "bad/expired access token" 401 — same HTTP status
and error code, different meaning, only distinguishable by message text.

### Import

`terraform import lucidity_tenant.x AWS/123456789012` (provider/account-id).
Imported resources get lucidity_dashboard_account_delete_protection=true regardless of config
until first apply.

**Updated 2026-10-10.** `aws_iam_external_id` is now returned by List, so
`Read` recovers it on import (only when List reports a non-null value for an
AWS tenant; null/absent leaves state alone) and it is updatable in place (no
RequiresReplace). `lucidity_product_list` and `aws_org_root_id` are still not
returned by List: per the maintainer, after import they are *assumed* to match
config — null state never forces a replace (`RequiresReplaceIf` on the list)
or errors (`ModifyPlan`'s org-root check); the config value is recorded on the
next apply. A value already in state still behaves as before (replace / plan
error). `aws_iam_role_name`, `aws_iam_policy_name`, and
`lucidity_dashboard_display_name` reconcile via a normal `Update()` on the
next apply. `ImportState` emits a warning describing all of this.

### Data source `lucidity_tenants`

Wraps `GET /external/client/api/v1/tenants`; response is `{tenants: [...],
meta: {totalCount}}` (object wrapper, not a bare array — deliberately
future-proofed for pagination without breaking clients, per the doc).
Results ordered ACTIVE first, then INACTIVE. Item fields: `tenantId`,
`cloudProvider`, `cloudProviderAccountId`, `cloudEntityName` (new,
provider-side account name), `displayName`, `status`. Exposes status per
tenant. Enables the "desired vs actual" output pattern (Output 3 in
planning). Live-confirmed 2026-09-06 against a real account (`LucidityPLS`)
— response shape matches this exactly.

### Live API testing notes (2026-09-10)

First live test against a real production-shaped account (`Lucidity_Business`,
`dash-back.lucidity.dev`, 8 pre-existing tenants across AWS/AZURE/GCP — all
INACTIVE) rather than a small sandbox, using real AWS infrastructure
(`aws-terraform-account-creation`'s pool1, account 810100779479, with the
real `LucidityRole`/`LucidityPolicy` from the maintainer's own IAM sample).

- `lucidity_tenants` data source: confirmed working end-to-end against a
  real, larger account — full listing, all fields accurate.
- **New, previously undocumented failure mode:** onboarding pool1 with
  `skip_cloud_permission_check = false` (default) — a real AWS account
  with the maintainer's own real `LucidityPolicy` correctly attached —
  failed with `500 INTERNAL_ERROR`: `"Tenant PermissionCheck Failed"`.
  Reproduced twice, not transient. Not in the doc's error tables at all.
  **Unresolved:** unclear whether this is a real gap in the sample policy,
  an IAM propagation-timing issue, or a genuine bug on Lucidity's side —
  needs following up with Lucidity directly.
- Confirmed `skip_cloud_permission_check = true` bypasses this specific
  failure — onboard proceeded past it to a real `201 Created`.
- **Bug found and fixed the same session:** with the permission check
  bypassed, the onboard call itself succeeded, but the provider's
  follow-up list-and-match (`refreshFromList`, shared by `Create`/`Update`)
  didn't find the new tenant on the very next `List` call — a real
  server-side propagation delay the provider had zero tolerance for. This
  turned a genuinely successful onboard into a hard Terraform error, with
  the real, now-ACTIVE tenant left completely untracked in state (recovered
  manually via `terraform import`). Fixed by giving `refreshFromList` a
  bounded retry (4 attempts, 2s apart) before giving up — see
  `internal/provider/resource_tenant.go`.
- Confirmed live, exactly as designed: importing a tenant whose
  `aws_iam_external_id`/`lucidity_product_list` can't be recovered (both
  write-only/unlisted, per the Import section above), then applying a
  config with the real original values for them, shows a forced replace —
  matching the documented import gap, not a bug.
- `aws_org_root_id` was not exercised in this pass (focus shifted to
  debugging the permission-check failure above) — Open Question #7 remains
  open.

### Live API testing notes (2026-09-11)

Continued the Phase 12 QA campaign through the remaining test matrix —
modify, deboard, import-edge-case, and data-source cross-reference cases —
against pool1/pool2/pool3. Full per-case results in
`docs/qa-testing/account-management.md` (and `docs/qa-testing/authentication-management.md`
for the auth-sanity cases); notable findings not obvious from the doc:

- **Open Question #7 resolved:** `aws_org_root_id` is accepted on a real
  onboard call (1.1.1) — Lucidity doesn't reject the undocumented field.
- **Open Question #6 resolved:** re-onboarding an INACTIVE tenant is
  blocked, both by this provider's own pre-check and by Lucidity's native
  `409 CONFLICT` (1.3.5) — matches the design in "INACTIVE handling" above.
- **New finding — Update() doesn't validate role/policy existence or
  reachability at all** (1.2.1/1.2.4/1.2.10): a completely dummy/nonexistent
  `aws_iam_role_name`/`aws_iam_policy_name` is accepted silently by the real
  `PATCH` endpoint — asymmetric with onboard's strict reachability check.
  Not a bug in this provider (nothing to validate client-side without
  duplicating Lucidity's own AssumeRole check), but worth knowing: a typo'd
  role/policy name on modify won't surface until something actually needs
  that role, not at apply time.
- **List-endpoint propagation delay is common, not rare, and applies to
  deboard too, not just onboard/update.** Confirmed via direct repeated API
  calls (bypassing this provider entirely) that a deboard's underlying
  state change is immediate and real — a second deboard call on an
  already-deboarded account correctly returns `ALREADY_DE_BOARDED`
  instantly — but the List endpoint itself lagged 45s-120+ seconds behind
  in multiple observed cases this session (both on deboard and on Update
  PATCH), regularly exceeding `refreshFromList`'s current 24s (8×3s) retry
  budget. The documented recovery (retry the apply once the propagation
  catches up) worked every time it was needed. Not widening the retry
  budget further — a bounded, occasionally-retried window is the right
  trade-off over either a much longer default cost on every apply or an
  unbounded wait.
- **Testing-methodology lesson, not a provider bug:** an imported
  resource's write-only fields (`aws_iam_external_id`, `lucidity_product_list`)
  must be patched into state (via `terraform state pull` → edit → `state
  push`) immediately after import, before using it for further modify
  testing — otherwise every subsequent apply plans a full destroy+recreate
  instead of an in-place update (exactly per the documented "Known import
  gap" behavior). `account_delete_protection`'s default-true blocked the
  destroy phase every time this was missed, so nothing was ever actually
  lost, but see `docs/qa-testing/account-management.md`'s methodology note under
  section 1.2 for the full account. **Recurred on pool4/pool5** (the
  concurrency test's own imports) and was caught the same way — this time
  surfaced while validating the real published `v0.1.2` binary against
  `concurrency/`'s existing state, not during active test execution, which
  is itself a good argument for patching write-only fields right after
  every import rather than only when about to run a specific modify test.

### Live API testing notes (2026-09-06)

Tested against `LucidityPLS` (`dashboard-azurepls.lucidity.cloud`) using two
refresh tokens confirmed to belong to the same account (identical 3-tenant
list from both — a useful confirmation, though not a general account-
identity mechanism).

- List response shape, and all 10 invalid-onboard-input scenarios (missing
  `cloudEntityInformation`; blank `cloudProvider`/`cloudProviderAccountId`/
  `displayName`/`externalId`/`awsIAMRoleName`/`awsIAMPolicyName`; non-AWS
  provider; empty/invalid `productList`) — all confirmed matching the doc's
  `400 INVALID_REQUEST` behavior (one cosmetic wording difference: the
  invalid-product-value message is `"X" is not a supported product.` rather
  than the doc's paraphrase — same code/behavior).
- A rejected onboard attempt leaves no orphaned/partial tenant record.
- **Not yet tested:** a full real onboard→update→deboard→re-onboard-conflict
  cycle. Dummy AWS account numbers can't complete it — see the
  `skip_cloud_permission_check` note above. Deferred until a real,
  disposable AWS account with an actually-assumable IAM role is available.
- **No account-identity signal found anywhere** — checked response headers
  and bodies across every call made. Directly blocks implementing the
  `lucidity_dashboard_account_name` validation from the Phase 1 provider
  config block above.

## Testing conventions

- Unit tests: mock server, recorded envelopes, cover INACTIVE / NOT_FOUND /
  permission-failure / expired-token cases. Run on every PR.
- **Lifecycle tests (added 2026-09-10):**
  `internal/provider/resource_tenant_lifecycle_test.go` drives
  `tenantResource`'s actual `Create`/`Read`/`Update`/`Delete`/`ImportState`
  Go methods directly against a stateful local mock server — bypassing
  `Configure()` entirely, since `lucidity_dashboard_url`'s closed URL set
  has no override to redirect at a mock. No live credentials needed; runs
  in CI. This is what actually closes the "CRUD business logic has zero
  test coverage" gap that the schema/validator RPC tests never covered:
  the full onboard→update→destroy-safety→re-onboard-conflict→deboard→
  re-onboard-conflict sequence, out-of-band drift detection (`Read()`
  finding a tenant gone INACTIVE without `Delete()` having run), a vanished
  tenant being removed from state, the cloud-account-validation-401
  distinguishing logic, and `ImportState` parsing (well-formed + malformed).
- Acceptance tests (TF_ACC=1): maintainer's sandbox accounts, treated as live.
  AWS first (onboarding is AWS-only per the current API); Azure/GCP schema
  support (List/Deboard/Update already accept all three providers) ships in
  Phase 2 regardless of onboard's AWS-only limitation.
- Destroy-path tests: protection-error and forget paths run freely; the
  actual-deboard test is separately tagged and run deliberately (sandbox
  tenants burn permanently on each deboard).
- **Live QA campaign (started 2026-09-10):** a ~60-case manual test matrix
  against 4 real AWS accounts (3 disposable pool accounts plus
  `testaccount1`, provisioned by `aws-terraform-account-creation`) and a
  real Lucidity account, tracked per-section in `docs/qa-testing/` (status per case,
  not duplicated here — see `docs/qa-testing/README.md`) with a
  `~/Desktop/terraform testing/` config refreshed
  for Phase 2 (`provider.tf`/`variables.tf`/`lucidity_tenant.tf`/
  `datasource.tf` plus `testvars/`, `concurrency/`, `import/` — see that
  directory's README). Blocked on: a live `LUCIDITY_REFRESH_TOKEN`, and
  adding `LucidityRole`/`LucidityPolicy` to `testaccount1` (it currently has
  neither — see `aws-terraform-account-creation`'s `lucidity_test_accounts.tf`
  for the pattern already used for the 3 pool accounts). Findings that
  resolve an open question or reveal new behavior get folded back into this
  file's Live API testing notes, same as the 2026-09-06 round.

## Open questions (do not block Phase 1)

1. ~~CONFLICT error-code semantics~~ **Resolved 2026-09-06:** "Already a
   tenant exists with the provided details." See the client.go bullet above.
2. ~~Whether update APIs' `Account` param is derivable from token~~
   **Resolved 2026-09-06:** moot — the real update API (`PATCH /tenants`)
   has no Account param at all; see "Update APIs" under Phase 2.
3. ~~Onboard re-trigger response shape for existing ACTIVE tenant~~
   **Resolved 2026-09-06:** no re-trigger exists — onboard is create-only,
   an existing tenant (ACTIVE or INACTIVE) always gets `409 CONFLICT`.
4. ~~Rate limits / parallel-onboard safety on Lucidity side~~ **Resolved
   2026-09-06:** 10 concurrent requests supported — already matches the
   existing `max_parallel_requests` default, no code change needed. Avoid
   two concurrent operations against the *same* account/tenant; Terraform's
   own per-resource-instance serialization already prevents this for normal
   `lucidity_tenant` usage.
5. **`lucidity_dashboard_account_name` validation mechanism** (new
   2026-09-06, renamed 2026-09-07): no endpoint found that identifies which
   dashboard account a token belongs to — checked response headers and
   bodies across refresh/list/onboard calls. Blocks implementing the
   cross-validation Configure() is supposed to do. Needs either a
   Lucidity-provided endpoint or an alternative signal.
6. ~~Full destructive-cycle confirmation~~ **Resolved 2026-09-11:** the full
   real onboard (1.1.2) → update (1.2.x) → deboard (1.3.3) →
   re-onboard-conflict (1.3.5) cycle has now been exercised end-to-end
   against pool1/pool3, including the idempotent re-deboard case (1.3.4)
   and out-of-band-deboard drift detection (1.3.6/1.3.7). See
   `docs/qa-testing/account-management.md` section 1.3 and the 2026-09-11 live
   testing notes above.
7. ~~`aws_org_root_id` on the real onboard payload~~ **Resolved 2026-09-10:**
   accepted — a real onboard call with `aws_org_root_id` set succeeded
   (1.1.1). Lucidity does not reject the undocumented field.
8. **`500 INTERNAL_ERROR` / "Tenant PermissionCheck Failed"** (new
   2026-09-10): onboarding a real AWS account with the maintainer's own
   real `LucidityPolicy` correctly attached, `skipCloudPermissionCheck`
   unset (defaults false), failed reproducibly with this error — not in
   any documented error table. `skipCloudPermissionCheck: true` bypasses
   it. Unclear whether this is a real gap in the sample policy, an IAM
   propagation-timing issue (the role/policy were only ~30 minutes old),
   or a genuine bug on Lucidity's side. Needs following up with Lucidity
   directly, and a retest once resolved (both to confirm the fix and to
   get a real "correct permissions, check not skipped" pass — #6's
   destructive cycle still needs this to actually complete cleanly).

## Style

- Comments explain WHY, especially around safety logic. The Delete() function
  gets the full business-critical warning block from the plan.
- Diagnostics are actionable: name the attribute/env var/next step, include
  requestId on API failures.
- Generate docs with tfplugindocs from schema descriptions + examples/.
  **Done (2026-09-07):** `examples/provider/provider.tf`,
  `examples/resources/lucidity_tenant/{resource.tf,import.sh}`, and
  `examples/data-sources/lucidity_tenants/data-source.tf` recreated per
  tfplugindocs' convention; `tfplugindocs generate --provider-name lucidity`
  run to produce `docs/index.md`, `docs/resources/tenant.md`, and
  `docs/data-sources/tenants.md`. `docs/examples/lucidity-tenants.tf` stays
  as-is — a separate, more elaborate multi-account reference config, not
  wired into tfplugindocs. Re-run `tfplugindocs generate` after any schema
  or `examples/` change; nothing currently does this automatically (no
  `go generate` directive, no CI step) — that's a reasonable follow-up if
  docs start drifting from schema in practice.
