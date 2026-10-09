# Live QA test log — Account management

**Section:** Account management (Phase 2) — the `lucidity_tenant` resource
and `lucidity_tenants` data source: onboarding, modifying, and deboarding a
cloud account as a Lucidity tenant, plus import and cross-referencing
against the data source. This log does **not** cover the authentication
layer itself (token refresh, credential sourcing) — that's the
Authentication management section, tracked separately in
[authentication-management.md](authentication-management.md). See
[README.md](README.md) for the full list of provider sections, shipped and
planned.

Tracks execution of the live test matrix against real AWS accounts and a
real Lucidity account. See [README.md](README.md) for how this fits with
`CHANGELOG.md`.

**Status legend:** `Not run` / `Pass` / `Fail` / `Blocked` (needs something
before it can run, noted in Notes).

**Prerequisites — done as of 2026-09-10:**
- ~~A live `LUCIDITY_REFRESH_TOKEN`~~ — have one, for the `Lucidity_Business`
  account (`dash-back.lucidity.dev`).
- ~~Local Terraform pointed at a locally-built provider binary via
  `dev_overrides`~~ — working (no release had been cut yet at the time of
  this run).
- ~~`LucidityRole`/`LucidityPolicy` on `testaccount1`~~ — added.
- **New:** the account pool grew to 6: pool1–pool5 plus `testaccount1`.
  pool4/pool5 sit under two new AWS Organizations OUs (`Lucidity-QA-Alpha`,
  `Lucidity-QA-Beta`) rather than directly under root — see
  `aws-terraform-account-creation`'s `lucidity_test_accounts.tf`.

## Execution order

Run top-to-bottom within each phase; phases are ordered by dependency, not
by the section numbers below (e.g. an update test needs something already
onboarded).

1. ~~**Phase 0 — setup**~~ Done — see Prerequisites above.
2. **Phase 1 — concurrency first:** 4.1, onboarding pool1/pool2/pool3
   together via `concurrency/main.tf`'s own state — do this *before*
   anything else touches those accounts, since it's a separate state and
   would otherwise hit the ACTIVE-duplicate conflict against an
   already-onboarded pool1/pool2.
3. **Phase 2 — import pool1 into the main config:** 2.1, via
   `terraform import` — continues the sequence on pool1 from the main
   `lucidity_tenant.tf` config rather than re-onboarding it.
4. **Phase 3 — remaining onboard tests not requiring a fresh account:**
   1.1.3–1.1.15, 1.1.17 (against testaccount1/spare accounts as needed;
   1.1.1/1.1.2 are effectively covered by how 4.1 onboarded pool1/pool2).
5. **Phase 4 — modify:** 1.2.1–1.2.17, run against pool1 (imported in
   Phase 2).
6. **Phase 5 — data source:** 3.1–3.3.
7. **Phase 6 — duplicate-onboard conflict:** 1.1.16, against pool1 while
   still ACTIVE.
8. **Phase 7 — deboard and everything downstream:** 1.3.1–1.3.9 (pool3 is
   the account earmarked to actually end up permanently INACTIVE).
9. **Phase 8 — remaining import tests:** 2.2–2.9.

## 1. Basic functionality tests

### 1.1 Onboard account tests

| ID | Test case | Status | Date | Notes / evidence |
|---|---|---|---|---|
| 1.1.1 | Onboard with `aws_org_root_id` set | Pass | 2026-09-10 | Resolves Open Q7: the field is accepted — real onboard succeeded with `aws_org_root_id` set, not rejected/ignored. |
| 1.1.2 | Onboard without `aws_org_root_id` | Pass | 2026-09-10 | Onboarded pool1 (810100779479) against `Lucidity_Business`. Needed `skip_cloud_permission_check=true` to get past a new, undocumented `500 INTERNAL_ERROR` — see 1.1.5/1.1.6. |
| 1.1.3 | Onboard with a dummy/nonexistent AWS account number | Pass | 2026-09-10 | Correctly rejected (cloud-account-reachability validation fails regardless of `skip_cloud_permission_check`). |
| 1.1.4 | Onboard with a real account number but no IAM role/policy created | Pass | 2026-09-10 | Correctly rejected — account unreachable without the role in place. |
| 1.1.5 | Onboard with real account + role created, incorrect permissions, `skip_cloud_permission_check=false` | Deferred | 2026-09-10 | Tested with *correct* permissions, not incorrect — got `500 INTERNAL_ERROR` "Tenant PermissionCheck Failed", an undocumented failure mode still unresolved as of this writing. Maintainer's call: ignore it for now and default all test cases to `skip_cloud_permission_check=true` (see `terraform testing/variables.tf`) rather than block on it. Revisit once resolved. |
| 1.1.6 | Onboard with real account + role created, incorrect permissions, `skip_cloud_permission_check=true` | Pass | 2026-09-10 | Confirmed this flag bypasses the 1.1.5 failure specifically — onboard proceeded to a real `201 Created`. Still tested against *correct* permissions, not incorrect — the "incorrect permissions" half of this case is still open. |
| 1.1.7 | Onboard with a real, correctly-permissioned role and policy (happy path) | Blocked | 2026-09-10 | Blocked by the same Open Q8 failure — a real, correctly-permissioned role hit `500 INTERNAL_ERROR` with the permission check enabled (default). Only succeeded with `skip_cloud_permission_check=true`, which isn't the intended happy path. Also surfaced and fixed a real bug: the follow-up list-and-match had no retry tolerance for a propagation delay, so this successful onboard initially came back as a Terraform-level failure with the tenant left untracked (recovered via `terraform import`; see `internal/provider/resource_tenant.go`'s `refreshFromList`). |
| 1.1.8 | Onboard with `aws_iam_role_name` pointing at a role name that doesn't exist | Pass | 2026-09-10 | Correctly rejected at the cloud-account-validation step. |
| 1.1.9 | Onboard with a mismatched `aws_iam_external_id` (doesn't match trust policy) | Pass | 2026-09-10 | Correctly rejected — `AssumeRole` fails with the wrong external ID, surfaced as the same 401 cloud-account-validation error. |
| 1.1.10 | Onboard with `aws_iam_external_id` in a non-UUID format | Pass | 2026-09-10 | No server-side format validation on the string — accepted at the request level, then failed downstream the same way a mismatched real UUID would (AssumeRole trust-policy condition doesn't match). **Update (2026-09-11):** closed client-side — the attribute now has a `stringvalidator.RegexMatches` UUID check, so a malformed value is now rejected at `terraform plan` time instead of reaching the API at all. |
| 1.1.11 | Onboard with `aws_iam_external_id` blank/whitespace | Pass | 2026-09-10 | Rejected — blank external_id fails onboard. |
| 1.1.12 | Onboard with `lucidity_dashboard_display_name` blank/empty | Pass | 2026-09-10 | Rejected with a clear `400 INVALID_REQUEST` — confirms displayName's blank check IS enforced server-side. |
| 1.1.13 | Onboard with an AWS account number in an invalid format | Pass | 2026-09-10 | No server-side format validation on `cloudProviderAccountId` either — an invalid-format string is accepted at the request level, then fails the same way any unreachable account does. **Update (2026-09-22):** closed client-side for AWS — `cloud_provider_account_id` now requires exactly 12 digits when `cloud_provider = "AWS"`, checked in `ValidateConfig`, rejected at `terraform plan` time before reaching the API at all. |
| 1.1.14 | Onboard where role/policy name casing doesn't match the real IAM resource | Pass | 2026-09-10 | Rejected — AWS IAM role/policy lookups are case-sensitive; a casing mismatch is indistinguishable from a nonexistent role at the API's error-message level. |
| 1.1.15 | Onboard an account ID belonging to an unrelated AWS Organization | Not run | | Needs an AWS account genuinely outside `aws-terraform-account-creation`'s org — not exercised; low priority given 1.1.3/1.1.4/1.1.9's reachability-check coverage already proves cross-account/wrong-account onboard attempts fail the same way. |
| 1.1.16 | Onboard the same account a second time while still ACTIVE | Pass | 2026-09-11 | Re-onboarding pool1 (810100779479, still ACTIVE) correctly hit the ACTIVE-duplicate pre-check: `"A tenant already exists for cloud account 810100779479 ... and is ACTIVE. This is a configuration error..."` — no API call made, no side effects. |
| 1.1.17 | Onboard with `cloud_provider = AZURE` or `GCP` | Pass | 2026-09-10 | Correctly rejected — Create() blocks non-AWS `cloud_provider` outright before any API call, pointing at `terraform import` instead (Azure/GCP onboarding isn't supported by Lucidity yet). |

### 1.2 Modify (update) tests

| ID | Test case | Status | Date | Notes / evidence |
|---|---|---|---|---|
| 1.2.1 | Modify role name to a dummy/nonexistent role | Pass | 2026-09-10 | **New finding:** Update's `PATCH` does NOT validate role existence/reachability — a dummy role name is accepted silently (asymmetric with onboard's strict validation). |
| 1.2.2 | Modify role name to an existing role with incorrect permissions | Deferred | | Low priority — `skip_cloud_permission_check=true` is the standing default for all test cases (Open Q8 workaround), and 1.2.1 already shows Update doesn't check reachability/permissions at all regardless. |
| 1.2.3 | Modify role name to an existing role with correct permissions | Pass | 2026-09-10 | Real role-name update against pool1, succeeded. |
| 1.2.4 | Modify policy name to a dummy/nonexistent policy | Pass | 2026-09-10 | Same finding as 1.2.1 — dummy policy name accepted silently by Update. |
| 1.2.5 | Modify policy name to an existing policy with incorrect permissions | Deferred | | Same rationale as 1.2.2. |
| 1.2.6 | Modify policy name to an existing policy with correct permissions | Pass | 2026-09-10 | Real policy-name update against pool1, succeeded. |
| 1.2.7 | Modify display name only | Pass | 2026-09-10 | Also where the stale-list-read consistency bug was found and fixed (see `refreshFromList`'s `fresh` callback in `internal/provider/resource_tenant.go`) — a successful PATCH was initially followed by a stale (pre-update) list match, causing a "Provider produced inconsistent result after apply" error. Re-verified 2026-09-11 (as part of 1.2.9/1.2.10) that the fix correctly retries instead of accepting stale data. |
| 1.2.8 | Modify display name to a blank/empty string | Pass (minor gap) | 2026-09-10 | Minor, low-priority rough edge: `DisplayName`'s `omitempty` JSON tag means a blank string is silently omitted from the PATCH body rather than sent explicitly, so the request fails with Lucidity's generic "Nothing to update" error instead of a clearer "displayName must not be blank" message. Not fixed — would need a pointer/wrapper type; assessed as disproportionate complexity for this edge case. |
| 1.2.9 | Modify role name, policy name, and display name together | Pass | 2026-09-11 | Single `PATCH` with all three fields changed at once against pool1, confirmed via the `lucidity_tenants` data source showing the new display name. Required a manual apply retry once (propagation delay past the retry window — see the note below the table). |
| 1.2.10 | Modify role name + policy name where only one new value is valid | Pass | 2026-09-11 | Valid role name + a totally bogus/nonexistent policy name in the same update — succeeded, consistent with 1.2.1/1.2.4 (Update doesn't validate either field's reachability). |
| 1.2.11 | No-op apply (no changes) | Pass | 2026-09-11 | Confirmed clean "no changes to real infrastructure" against pool1's current values. |
| 1.2.12 | Attempt to modify `aws_org_root_id` | Pass | 2026-09-11 | Plan-time `ModifyPlan` error, exactly as designed: "Changing aws_org_root_id after onboarding is not supported in this release..." |
| 1.2.13 | Attempt to modify `lucidity_product_list` | Pass (partial) | 2026-09-11 | Couldn't exercise the RequiresReplace path directly — today only `AUTOSCALER` is a valid product, so there's no second valid value to change to. Attempting an invalid second value (`"OTHER"`) is correctly rejected by the closed-set validator before any plan is even generated. The RequiresReplace behavior itself remains covered by unit/mock tests only until a second product ships. |
| 1.2.14 | Attempt to modify `aws_iam_external_id` | Pass | 2026-09-11 | Plan (never applied) against a scratch import of pool1 showed `# forces replacement`, exactly as designed. **Superseded (2026-10-10):** Lucidity now accepts `externalId` on update and returns it from List, so this field is updatable in place (no forced replace). Covered by mock-backed tests (`TestLifecycle_UpdateRotatesExternalIDInPlace`); needs a live re-run — rotate the IAM trust policy first. |
| 1.2.15 | Attempt to modify `cloud_provider_account_id` | Pass | 2026-09-11 | `# forces replacement` confirmed. |
| 1.2.16 | Attempt to modify `cloud_provider` | Pass | 2026-09-11 | `# forces replacement` confirmed. |
| 1.2.17 | Attempt to modify a tenant that is not ACTIVE | Pass | 2026-09-11 | Confirmed via pool3 mid-deboard-testing: an Update() PATCH attempt against a since-INACTIVE tenant correctly failed with `"Tenant for cloudProviderAccountId '...' is not ACTIVE; cannot update."` In normal usage (refresh enabled) this is actually unreachable — Read()'s own INACTIVE guard (1.3.6/1.3.7) fires first, before Update() ever runs; this API-level check is a defense-in-depth backstop for a narrow race window, same pattern as Create()'s pre-check + native 409 backstop. |

**Testing-methodology note (2026-09-11):** at the start of this session's testing,
pool1's Terraform state was found to have lost `aws_iam_external_id` and
`lucidity_product_list` (both write-only/unlisted — see the Import section's
"Known gap") — the same designed-for import gap already documented, just not
yet patched on pool1 the way it had been on pool3. Left unpatched, this
correctly caused any further single-field modify apply to plan a full
destroy+recreate rather than an in-place update; `account_delete_protection`'s
default-true safely blocked the destroy phase each time, so nothing was ever
actually re-onboarded or lost — but it means any 1.2.x row dated 2026-09-10
that isn't independently re-confirmed above should be read with that caveat.
Applied the same `state pull` → patch → `state push` fix used for pool3, then
re-verified 1.2.9 through 1.2.17 fresh against the corrected state (all
above, dated 2026-09-11) — those results are solid. Lesson for future runs:
patch an imported resource's write-only fields into state immediately after
import, before using it for any modify testing.

### 1.3 Account inactive/deboard test cases

| ID | Test case | Status | Date | Notes / evidence |
|---|---|---|---|---|
| 1.3.1 | Destroy with `account_delete_protection=true` (default) | Pass | 2026-09-11 | Confirmed the hard pre-flight error before any API call — see 1.2.9-era retries and the 1.3.3 troubleshooting below, where this exact guard fired repeatedly until protection was explicitly set to `false`. |
| 1.3.2 | Destroy with protection off, `destroy_behavior="forget"` | Pass | 2026-09-11 | Removed pool3 from state without deboarding — confirmed still ACTIVE on Lucidity via data source immediately after. |
| 1.3.3 | Destroy with protection off, `destroy_behavior="deboard"` | Pass | 2026-09-11 | Real, irreversible deboard of pool3 (786732396503). Confirmed via a direct second deboard call afterward returning `ALREADY_DE_BOARDED`. **Troubleshooting note:** the first attempt hit an apparent "Destroy blocked" error even with protection explicitly set false — root cause was the same import-gap state issue as the methodology note above (pool3 had just been re-imported by 1.3.2, losing `aws_iam_external_id`/`lucidity_product_list` again), forcing a replace whose destroy phase read the *old* state's `account_delete_protection=true`. Fixed via the same state-surgery technique, then the deboard completed cleanly. **New finding:** List-endpoint propagation delay after a deboard can be substantially longer than previously documented for Update (up to ~60s) — observed 45s-120+s in multiple cases this session, on both pool2 and pool3. The underlying deboard itself is immediate and real (confirmed via the repeat-call check above); only the List read model lags. This is the same already-accepted residual risk as the Create/Update propagation delay, just confirmed to be common rather than rare in this environment. |
| 1.3.4 | Deboard an already-INACTIVE tenant | Pass | 2026-09-11 | Re-imported pool3 (now INACTIVE) and ran destroy with `destroy_behavior=deboard` again — hit the designed idempotent path: `"Lucidity tenant was already deboarded"` warning, no error. |
| 1.3.5 | Attempt to re-onboard an INACTIVE account | Pass | 2026-09-11 | **Resolves Open Q6.** Attempting to onboard pool3 (786732396503) again after it went INACTIVE correctly hit the pre-check: `"cloud account ... was previously deboarded and is INACTIVE ... Contact Lucidity support..."` |
| 1.3.6 | Deboard out-of-band, then `terraform plan`/refresh | Pass | 2026-09-11 | Deboarded pool2 (217767009925) directly via the API (bypassing Terraform), then ran `terraform plan` against the still-ACTIVE-in-state resource — Read()'s INACTIVE guard fired correctly: kept in state, errored with the support-contact message and the `terraform state rm` escape hatch. |
| 1.3.7 | Attempt an update immediately after an out-of-band deboard | Pass | 2026-09-11 | Same command/evidence as 1.3.6 — Read() runs before Update() in the normal plan/apply cycle, so any attempted operation (not just a plan) hits the same INACTIVE guard first. This makes 1.2.17's API-level "not ACTIVE" check effectively unreachable except via `-refresh=false` or a narrow race window. |
| 1.3.8 | Confirm `lucidity_tenants` reflects INACTIVE after deboard | Pass | 2026-09-11 | Confirmed repeatedly throughout this session's deboard testing (pool2 and pool3), both via the provider's own data source and direct raw API calls, once List propagation caught up (see 1.3.3's finding on delay). |
| 1.3.9 | Confirm AWS-side IAM role/policy untouched after deboard | Pass | 2026-09-11 | Ran `aws-terraform-account-creation`'s own apply pipeline (via its GitHub Actions workflow) after both pool2's and pool3's deboards — `Apply complete! Resources: 0 added, 0 changed, 0 destroyed`, confirming Lucidity's deboard only touches Lucidity's own records, never AWS. |

## 2. Terraform import test cases

| ID | Test case | Status | Date | Notes / evidence |
|---|---|---|---|---|
| 2.1 | Import an existing ACTIVE tenant | Pass | 2026-09-10 | Imported pool1's tenant (onboarded out-of-band relative to the local config, per 1.1.7's aborted apply) via `terraform import lucidity_tenant.under_test AWS/810100779479`. Warning message matched design exactly. |
| 2.2 | Import with a malformed import ID | Pass | 2026-09-11 | `"AWS-810100779479"` (no slash) rejected with a clear `"Unexpected import ID format"` error naming the expected shape. |
| 2.3 | Import with the cloud provider in the wrong case | Pass (minor gap) | 2026-09-11 | `"aws/810100779479"` correctly refused (fails safe — no silent success), but with a generic `"Cannot import non-existent remote object"` message rather than one hinting at the case mismatch specifically. Minor, low-priority UX gap — parsing is case-sensitive and doesn't special-case this. |
| 2.4 | Import with extraneous whitespace in the ID string | Pass (minor gap) | 2026-09-11 | Same as 2.3 — `" AWS/810100779479 "` correctly refused, same generic message rather than a whitespace-specific hint. |
| 2.5 | Import an account/tenant combination that doesn't exist | Pass | 2026-09-11 | `"AWS/999999999999"` correctly rejected with `"Cannot import non-existent remote object"`. |
| 2.6 | Import an INACTIVE tenant | Pass | 2026-09-11 | Importing pool3 (INACTIVE) succeeded in the "keep in state" sense — the resource lands in state, then immediately errors with the same INACTIVE support-contact message and `terraform state rm` escape hatch as the 1.3.6 drift-detection path. |
| 2.7 | Import the same resource address twice | Pass | 2026-09-11 | Second import to an address already holding a resource correctly errors: `"Resource already managed by Terraform... you must first remove the existing object from the state."` |
| 2.8 | Import, then apply with mismatched `external_id`/`product_list` | Pass | 2026-09-10 | After 2.1's import, `plan` against the real original values (which import couldn't recover) correctly showed a forced replace — exactly as designed, since state had them null post-import. Did not actually apply (would attempt a real destroy — safely blocked by `account_delete_protection=true` anyway, but not worth exercising for real). **Superseded (2026-10-10):** import now recovers `aws_iam_external_id` from List, and `lucidity_product_list`/`aws_org_root_id` are assumed to match config (null state never forces a replace). Covered by mock-backed tests; needs a live re-run. |
| 2.9 | Import, then write a config missing required AWS-only fields | Pass | 2026-09-11 | Importing pool1 with a config missing `aws_iam_external_id`/`aws_iam_role_name`/`aws_iam_policy_name`/`lucidity_product_list` correctly failed `ValidateConfig` at plan time: `"Missing required fields for AWS onboarding..."`, naming exactly the missing attributes. |

## 3. Data source tests

| ID | Test case | Status | Date | Notes / evidence |
|---|---|---|---|---|
| 3.1 | `lucidity_tenants` with mixed ACTIVE/INACTIVE tenants | Pass | 2026-09-10 | Real `Lucidity_Business` account had 8 pre-existing tenants (AWS/AZURE/GCP), all INACTIVE, plus pool1 ACTIVE after 1.1.7/2.1 — all listed correctly. |
| 3.2 | `lucidity_tenants` field-by-field accuracy check | Pass | 2026-09-10 | Fields matched the real dashboard data (`cloud_entity_name`, `display_name`, `status`, provider) for all 9 tenants observed. |
| 3.3 | Cross-reference data source vs. resource in the same config | Pass | 2026-09-11 | Added a `cross_reference_check` output to `terraform testing/datasource.tf` comparing `lucidity_tenant.under_test`'s attributes against the matching entry from `data.lucidity_tenants.all` in the same apply — `tenant_id`/`status`/`cloud_entity_name` matched exactly. |

## 4. Concurrency tests

| ID | Test case | Status | Date | Notes / evidence |
|---|---|---|---|---|
| 4.1 | Onboard multiple accounts in a single `apply` | Pass | 2026-09-11 | Onboarded pool4 (348258986987) and pool5 (446476244328) concurrently via `concurrency/main.tf`'s `for_each`. Both onboard calls succeeded with no cross-account interference; the only friction was the same List-propagation delay already characterized elsewhere (this time noticeably longer — a couple of minutes rather than tens of seconds), not anything concurrency-specific. On retry, both correctly hit the ACTIVE-duplicate conflict (confirming the first attempt's onboards had genuinely gone through even though List didn't show them yet), then both `terraform import`ed cleanly once List caught up. **Follow-up (2026-09-11):** as with pool1/pool3 earlier, this import left `aws_iam_external_id`/`lucidity_product_list` unpatched in state — surfaced later while validating against the real published `v0.1.2` binary, when a plain `plan` showed both forced for replacement. Fixed with the same `state pull` → patch → `state push` technique; `concurrency/` now shows a genuine zero-diff baseline. Same testing-methodology lesson as section 1.2's note: patch an imported resource's write-only fields immediately, not just for the main config's resource. |
| 4.2 | (Stretch) Race two concurrent applies against the same new account | Not run | | Optional |
