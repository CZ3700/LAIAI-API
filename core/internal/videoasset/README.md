# Ark asset and portrait service

`NewService(db, keyring, evidenceStore, Options)` creates an independent service.
`RegisterRoutes(group)` expects `/v1/video` and authenticated Gin context keys
`video_key` (`*identity.APIKey`) and `video_request_id` (string). Parent middleware
owns RequestStart/Finish and authentication; these handlers also require
`video:write` for mutation and `video:read` or write for reads. Root operation GET
can fall back to `GetOperation(ctx, orgID, operationID)` for group deletion.
Run `ProcessOnce(ctx)` periodically; it processes at most 16 pending operations
and creates bounded query intents. No background goroutine starts implicitly.

Options.ResolveScope resolves trusted account/region/project/credential versions;
an empty requested Scope selects the route, while a populated Scope must resolve
exactly. Capabilities default disabled. Explicit execution retention consent is
mandatory. Media upload also requires Options.VerifyMedia, which must safely
fetch and verify an immutable object including digest/type/size/duration/shape,
and return its approved immutable FetchURL; this URL is used for provider import
and is retained only in encrypted execution evidence, never metadata;
the package checks the documented bounds. ConfigureConsole can supply the built-in immutable MediaStore with DNS/IP pinning, HTTPS-only fetch, no redirects, byte limits, external malware scan and bounded ffprobe.
Options.ResolveCallback must return an exact pre-registered HTTPS target.
Options.ResolveCallbackByID resolves that registered target on return;
PlatformCallbackURL is a fixed platform HTTPS URL with path
`/v1/video/portrait-callback` and no query. Mount HandlePortraitCallback at that
path outside Gateway authentication. The official CallbackURL contains signed
state bound to org/session/target/expiry; only its HMAC is persisted, and the
state is assembled at dispatch outside execution/history payload storage.
The callback consumes this state once, audits, and redirects solely to the
registered target; it never accepts resultCode/GroupId as proof of success.
HMACKeys/ActiveHMACKey are independent domain-separated idempotency/cursor keys;
retain old versions while records/cursors remain live. No real accounts or
credentials are configured by this package or its tests.

`ResolveBindingsTx(tx, orgID, Scope, taskID, []ReferenceInput)` validates ownership,
account/project/version, ready status, metadata version and current portrait
license, then persists task references in the same caller transaction. It returns
internal Bindings for the Ark adapter. The root adapter must still enforce model
and role compatibility, and use the same immutable media when computing bounds.
`ReleaseBindingsTx(tx, orgID, taskID)` releases deletion barriers only after the
caller has authoritative proof the task is terminal; it retains historical refs.
Both resource mutation and task binding serialize through a per-org lock; root
must use the same lock order before quota/ledger locks.

The public DTOs expose platform IDs only. Names are limited to 64 Unicode characters and sent to the official API. Group
descriptions are limited to 300 characters. H5 group names are applied only after
the official group ID is known; unknown updates are not automatically resent. CreateAsset returns
processing until a scoped GetAsset says Active; HTTP 200 with Status Failed is
failed. A status query never incurs another resource creation.

Each operation is a transactional intent/outbox row with encrypted execution
payload, HMAC idempotency, fixed route version, safe events and a fenced claim.
Request bodies, URLs, provider errors and H5 credentials are not metadata.
Mutating requests are sent once; a timeout, unexpected response, missing ID or
expired lease becomes unknown and is never automatically resubmitted. A late
authoritative response may resolve the same fence, but another fence cannot
overwrite it. Optional redacted response capture and HTTP facts commit with the
resource transition. A DB failure after a provider acceptance remains uncertain
and requires reconciliation; it must never be interpreted as safe to create again.
The service does not promise automatic discovery of unknown creates without an
official ID. Read-only query intents can be recreated after a bounded delay.

Virtual empty-group deletion requires both the explicit deletion capability and
exclusive project write control, because official deletion cascades without an
atomic empty-group precondition. Local and fresh official emptiness checks are
mandatory. Nonempty/cascade and real-person group deletion are rejected.
Individual DeleteAsset is enabled separately and rejects live task references. Deletion
sets a barrier before external dispatch; unknown outcomes keep that barrier.
Deletion confirmation preserves evidence and task references. A definite
rejection restores the prior group and permits a new explicit delete request.

Group DTOs expose `deletion_mode` (`official`, `local_failed`, or `blocked`), a
safe `deletion_block_reason`, and the latest `delete_operation`, including its
platform `request_id`. Rejected deletion receipts remain visible after reopening
the page. Reusing an idempotency key returns its receipt; a new explicit request
may retry a definite rejection. Pending or unknown operations are not replayed.

A virtual group with no provider ID may be soft-deleted locally only when its
creation was definitely rejected, its original operation matches the same
organization/resource/account/region/project/version, review state is clear,
and there are no assets (including historical assets), live references, or
pending/unknown operations. Cleanup and its audit event share the tenant-locked
transaction, preserve history, and never resolve credentials or call Ark.

Official `DeleteAssetGroup` HTTP 404 is not deletion proof. Only the exact
`NotFound.group_id` code with verified RequestId/Action/Version/Service/Region
permits a same-scope `GetAssetGroup` query with the original `Id` and `ProjectName`.
Only a second fully verified exact not-found response confirms absence. A
present group, permission failure, generic 404, malformed response, or timeout
leaves the deletion unknown and its barrier intact. No automatic retry is added.
The original DELETE response retains its real HTTP status. The empty-group
precheck, DELETE, and follow-up query have distinct Invocation Store correlations;
their safe events link to redacted encrypted responses when history is enabled,
or capture-disabled metadata when disabled. Raw provider error text is never
exposed through operation DTOs.

H5 session creation is independent of business portrait permission. BytedToken
and H5Link are stored only in a separate encrypted short-lived secret table;
tenant/session/scope/expiry are checked inside the decrypted sealed structure.
They are stripped from Invocation Store bodies. Pending H5 URL reads require the
creating key or original session user, a committed audit event and an unexpired session; lists contain no
URL. This is repeatable URL retrieval, while official authentication itself is
single use. Callback parameters never grant identity or license: polling verifies
GetVisualValidateResult and maps its group transactionally. The session expiry
uses the dispatch start plus 30 minutes, conservatively before official issuance.
Success destroys credentials and records session succeeded with license unknown.

`VerifyLicenseTx` accepts only a trusted reviewed backoffice evidence object;
there is no customer endpoint for it. `RevokeLicenseTx` records trusted revocation.
Valid dates, purpose, evidence source and fresh re-verification are all required
for uploads/task binding. A successful H5 session remains succeeded after the
business license expires or is revoked. GetAssetGroup does not establish current
license validity. A creating key rotation needs an explicit audited reassignment
workflow before the new key may retrieve a pending H5 URL; it does not bypass the
key ownership gate automatically.

## Official protocol evidence

Checked 2026-10-16. The client implements the signed POST management API, service
`ark`, region `cn-beijing`, version `2024-01-01`, not inference Bearer auth.
It fixes the official hostname, disables redirects and automatic replay, bounds
timeouts/responses, and never exposes raw transport errors.

- [Official V4 signing implementation](https://github.com/volcengine/volc-sdk-golang/blob/main/base/sign.go)
- [CreateAssetGroup](https://docs.volcengine.com/docs/ark/create-asset-group-api?lang=zh)
- [CreateAsset](https://docs.volcengine.com/docs/ark/create-asset-api?lang=zh)
- [GetAsset](https://docs.volcengine.com/docs/ark/get-asset-api?lang=zh)
- [GetAssetGroup](https://docs.volcengine.com/docs/ark/get-asset-group-api?lang=zh)
- [DeleteAssetGroup](https://docs.volcengine.com/docs/ark/delete-asset-group-api?lang=zh)
- [CreateVisualValidateSession](https://docs.volcengine.com/docs/ark/h5-create-visual-validate-session-api?lang=zh)
- [GetVisualValidateResult](https://docs.volcengine.com/docs/ark/asset-group-id-get-visual-validate-result-api?lang=zh)

Fixtures are local mocks. They are not proof of live provider permissions,
model/asset compatibility or portrait compliance. Enable production capabilities
only after the deployment-specific acceptance documented by the integration.

## DDL required from the root migration owner

Use InnoDB/utf8mb4, UTC DATETIME(3), binary-collated opaque IDs, no cascading
financial/evidence deletes, and the S1 invocation tables. Production migrations
must be separate reviewed files; do not AutoMigrate. All strings below are NOT
NULL unless expressly NULL. GORM writes empty strings for unset optional values.
Provider IDs use nullable columns to allow multiple pending resources while
enforcing uniqueness once a provider identity is known.

```sql
CREATE TABLE video_asset_tenant_locks (
 org_id BIGINT UNSIGNED PRIMARY KEY, version BIGINT UNSIGNED NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_asset_groups (
 group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, kind VARCHAR(16) NOT NULL, name VARCHAR(128) NOT NULL,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 provider_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 status VARCHAR(16) NOT NULL, review_state VARCHAR(16) NOT NULL,
 portrait_id VARCHAR(64) NOT NULL, create_operation_id VARCHAR(64) NOT NULL,
 delete_operation_id VARCHAR(64) NOT NULL, row_version BIGINT UNSIGNED NOT NULL,
 deleted_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_group_org(org_id,group_id),
 UNIQUE KEY uk_video_group_provider(scope_id,provider_id),
 KEY idx_video_group_list(org_id,deleted_at,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_assets (
 asset_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 media_type VARCHAR(16) NOT NULL, name VARCHAR(64) NOT NULL, scope_id VARCHAR(64) NOT NULL,
 scope_version BIGINT UNSIGNED NOT NULL, provider_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 status VARCHAR(16) NOT NULL, review_state VARCHAR(16) NOT NULL, source_payload_id VARCHAR(64) NOT NULL,
 immutable_object_ref VARCHAR(64) NOT NULL, object_sha256 CHAR(64) NOT NULL, mime VARCHAR(64) NOT NULL,
 byte_size BIGINT NOT NULL, duration_ms BIGINT NOT NULL, width INT NOT NULL, height INT NOT NULL,
 fps_num INT NOT NULL, fps_den INT NOT NULL, metadata_version BIGINT UNSIGNED NOT NULL, verified_at DATETIME(3) NULL,
 create_operation_id VARCHAR(64) NOT NULL, safe_error_code VARCHAR(64) NOT NULL,
 row_version BIGINT UNSIGNED NOT NULL, deleted_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_org(org_id,asset_id), UNIQUE KEY uk_video_asset_provider(scope_id,provider_id),
 KEY idx_video_asset_list(org_id,group_id,deleted_at,asset_id), KEY idx_video_asset_poll(status,updated_at),
 CONSTRAINT fk_video_asset_group FOREIGN KEY(org_id,group_id) REFERENCES video_asset_groups(org_id,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_task_asset_refs (
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 asset_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 content_index INT NOT NULL, role VARCHAR(64) NOT NULL, asset_metadata_version BIGINT UNSIGNED NOT NULL,
 authorization_version BIGINT UNSIGNED NOT NULL, released_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(org_id,task_id,content_index), KEY idx_video_asset_refs(org_id,asset_id,released_at),
 KEY idx_video_group_refs(org_id,group_id,released_at),
 CONSTRAINT fk_video_ref_asset FOREIGN KEY(org_id,asset_id) REFERENCES video_assets(org_id,asset_id),
 CONSTRAINT fk_video_ref_group FOREIGN KEY(org_id,group_id) REFERENCES video_asset_groups(org_id,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_portrait_authorizations (
 session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, owner_user_id BIGINT UNSIGNED NOT NULL, created_by_key_id BIGINT UNSIGNED NOT NULL,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 session_status VARCHAR(32) NOT NULL, license_status VARCHAR(16) NOT NULL,
 create_operation_id VARCHAR(64) NOT NULL, callback_target_id VARCHAR(64) NOT NULL, secret_ref VARCHAR(64) NOT NULL,
 callback_state_hash VARCHAR(64) NOT NULL, callback_state_version VARCHAR(64) NOT NULL, callback_used_at DATETIME(3) NULL,
 session_expires_at DATETIME(3) NULL, group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
 valid_from DATETIME(3) NULL, valid_until DATETIME(3) NULL, license_scope VARCHAR(64) NOT NULL,
 license_version BIGINT UNSIGNED NOT NULL, license_evidence_ref VARCHAR(64) NOT NULL, license_source VARCHAR(32) NOT NULL,
 last_verified_at DATETIME(3) NULL, reverify_due_at DATETIME(3) NULL, revoked_at DATETIME(3) NULL,
 row_version BIGINT UNSIGNED NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_portrait_org(org_id,session_id), UNIQUE KEY uk_video_portrait_group(group_id),
 KEY idx_video_portrait_poll(session_status,session_expires_at,updated_at),
 KEY idx_video_portrait_license(license_status,reverify_due_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_portrait_secrets (
 secret_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 value_cipher TEXT NOT NULL, expires_at DATETIME(3) NOT NULL, destroyed_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_portrait_secret(org_id,session_id), KEY idx_video_portrait_secret_expiry(expires_at,destroyed_at),
 CONSTRAINT fk_video_portrait_secret FOREIGN KEY(org_id,session_id) REFERENCES video_portrait_authorizations(org_id,session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_asset_operations (
 operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, action VARCHAR(64) NOT NULL, resource_type VARCHAR(32) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 request_id VARCHAR(64) NOT NULL, canonical_path VARCHAR(200) NOT NULL,
 hmac_key_version VARCHAR(64) NOT NULL, idempotency_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 body_hash CHAR(64) NOT NULL, scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL, payload_id VARCHAR(64) NOT NULL, response_payload_id VARCHAR(64) NOT NULL,
 lease_owner VARCHAR(64) NOT NULL, lease_expires_at DATETIME(3) NULL, fence BIGINT UNSIGNED NOT NULL,
 http_status INT NOT NULL, upstream_request_id VARCHAR(128) NOT NULL, safe_error_code VARCHAR(64) NOT NULL, receipt TEXT NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, next_attempt_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_idempotency(org_id,canonical_path,idempotency_hash),
 KEY idx_video_asset_work(status,next_attempt_at), KEY idx_video_asset_lease(status,lease_expires_at),
 KEY idx_video_asset_operation_resource(org_id,resource_id,action,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE video_asset_events (
 event_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, resource_type VARCHAR(32) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 operation_id VARCHAR(64) NOT NULL, kind VARCHAR(64) NOT NULL, evidence_ref VARCHAR(64) NOT NULL, actor_id VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL, KEY idx_video_asset_events(org_id,resource_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

Validate these models against MySQL migrations plus concurrency/deadlock tests
in integration. SQLite mock tests alone cannot establish production lock behavior.


## Console integration

Call `ConfigureConsole(ConsoleOptions{CallbackURL: registeredReturnURL, Media: store})`.
The caller identity is `session_user` with actual OrgID/UserID and KeyID zero;
public API callers retain `api_key` and key scopes. Do not mint synthetic keys.
The platform H5 callback and registered return URL remain distinct.

`NewMediaStore(MediaStoreConfig{Root, PublicBaseURL, FFProbePath, SigningKey,
ScanCommand})` requires a private writable directory, public HTTPS base URL,
32-byte signing key, ffprobe and an actual malware scanner executable/arguments.
The scanner receives the temporary file path as the final argument; exit zero
means clean. No shell is invoked. Example public path is
`https://public.example/api/video/media-source`; only GET/HEAD ServeHTTP routes
may bypass session authentication, and each fetch needs a tenant-bound signature.
Raw `POST /api/video/media-uploads/:id/complete` has its own strict byte limit and
must be outside the global 1 MB JSON body limiter. All other console routes remain
inside authentication, active membership and JSON limits. Previews require a
session, organization predicate and audit event. No source URL or signing query
is returned in ordinary list metadata.

`ProcessOnce` drives durable synchronization. Same-scope active jobs are serialized
under an account gate; different scopes can coexist and share at most one page
per second per account/region. Page checkpoints are encrypted, filter-bound and
fenced. Unknown groups enter superadmin discovery only. Explicit reviewed
assignment creates an organization mapping; it never grants all of default.
Imported real groups use imported_pending/imported_verified, never fabricated
H5 success. Imported media remains needs_review until a fresh scoped GetAsset
URL has been downloaded, scanned and probed. Missing pages never cause tombstones.

Console group/assets listings use organization-bound signed cursors. The
superadmin assignment endpoint stores review evidence in Invocation Store before
assignment; named superadmin license review remains a separate audited action.
Operations retain tombstones and task/financial history after deletion.


`GET /api/video/media-uploads` exposes durable upload sessions with organization,
media type and stage filtering plus signed pagination. Verified sessions can be
registered from the console after a page refresh. These records remain separate
from official asset registration state. Fetch URL expiry is fixed at the upload
session expiry: retrying registration, including after expiry, replays the same
idempotency result; expired media cannot start a new registration. Administrative
discovery pagination binds its cursor to the exact configured scope.

## Sync recovery (2026-10-22)

Console `GET /api/video/asset-sync-jobs/latest?kind=virtual|real_person`
returns `{ "job": null }` or the latest owned job for the current organization
and exact default account/region/project/scope/version. It never returns an
admin discovery or another organization's job. All states remain readable.
The job exposes request_id, http_status, upstream_request_id, attempts,
retry_count and next_attempt_at; these are safe summaries, not raw provider data.

Official imports without local media evidence store verified_at as SQL NULL
(migration 0122). They remain visible and independently deletable when authorized,
but cannot be bound to a generation task. Migration 0123 adds safe sync summaries
and durable read-only backoff. Each List attempt retains policy-bound encrypted
history in Invocation Store, including failed responses; absent network responses
are not fabricated. Capture-disabled jobs retain metadata only. Only temporary
read failures, 429 and 5xx stop after three recorded failures per page. A worker
lost before recording its result may replay a read-only claim after lease expiry;
this is not a hard cap on network dispatches. Mutating creates and deletes keep
their existing no-replay behavior. See production runbook 6.3.
