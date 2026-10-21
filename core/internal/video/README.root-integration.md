# Ark assets: root integration

`ConfigureAssets(AssetIntegrationOptions{PlatformCallbackURL: configURL})` installs
`Service.Assets` with complete DB-backed scope, media and callback resolvers.
No configured rows means no capability. The main service registers
`Assets.RegisterRoutes(videoGroup)`, runs `Assets.ProcessOnce(ctx)`, exposes
`Assets.HandlePortraitCallback` at the fixed unauthenticated callback path, and
falls back to `Assets.GetOperation` when a video operation is not found.
The callback verifies its one-use HMAC state internally. The platform callback
URL must be HTTPS, have exactly `/v1/video/portrait-callback` and have no query.

## Seedance generated-result delivery (2026-10-21)

Official Seedance results use direct upstream HTTPS URLs. The gateway does not
download, store, copy or proxy generated video or last-frame bytes. It does not
require an artifact downloader, artifact root/temp/host configuration or a shared
artifact volume. Existing historical files are not automatically removed or read
to serve this path. This change does not alter trusted input-media storage,
asset-management verification or other providers' delivery contracts below.

Authenticated task reads return `content_url` as the video URL and preserve
`content.video_url` / `content.last_frame_url` when available. `artifact_status`
is retained for compatibility: `ready` means a result URL was obtained, not that
the platform hosts a permanent object. The upstream controls signature validity.
`GET /v1/video/generations/{id}/content` remains an authenticated JSON endpoint
returning `{ "url": "https://…", "kind": "video", "source": "upstream" }`;
`kind=last_frame` selects the optional last-frame link. It returns neither a
binary stream nor a redirect. Clients use the URL directly as the media source
or download destination and must not send a Gateway Bearer token to that host.

Invocation Store retains a separate, minimal encrypted `delivery` capsule with
validated result URLs only. Each capsule lives for at most 24 hours and expires
no later than task creation plus the task's recorded `RetentionDays`. This is
separate from the execution-request capsule's 24-hour lifetime after acceptance.
It contains no media bytes, complete request/response payloads
or provider credentials. Signed query parameters remain intact for delivery;
known provider credentials, non-HTTPS URLs, userinfo and unsafe destinations are
rejected. Tenant/purpose binding and audited decryption are mandatory. Ordinary
state and receipt records contain references, never the signed URL. This purpose
is independent of optional original-payload history and must not become a way
to retain that history when it is disabled.

Task lists only read cached delivery capsules. Single-task detail and `/content`
may recover missing or expired links, including historical tasks, from the pinned
provider account through a rate-limited, audited query with task identity checks.
Within the task's recorded retention boundary, a new query may establish a new
capsule; reading an existing capsule never extends its expiry. A deletion barrier must
prevent both reading and backfilling a deleted task. Recovery neither creates a
generation nor changes billing, and never reads or migrates old video files.
Missing or expired links are reported as unavailable; no automatic signature
renewal or permanent download availability is promised.

## Administrator functions

All functions check the caller-provided **already authenticated** `*identity.User`
has a nonzero ID, active status and `superadmin` platform role. A request body
must never supply that actor. These functions do not establish authentication.
The root server owns the superadmin HTTP routes; return only success/error, never
serialize input objects containing credentials or URLs.

- `RegisterAssetScope(ctx, actor, AssetScopeRegistration) error`: immutable
  `(provider_scope_id, version)` plus credentials and explicit capabilities.
  Provider account/region/project are copied from `video_provider_scopes`; no
  customer account/project override exists. Credentials are encrypted with a
  purpose/scope/version/account/region/project envelope.
- `SetAssetScopeEnabled(ctx, actor, scopeID, version, enabled) error`.
- `SetOrgAssetRoute(ctx, actor, AssetOrgRoute) error`: explicitly grants an
  organization the scope version. `default_route` selects the scope used to
  create new asset groups. Multiple scopes may be granted to an organization.
  Changing the version does not silently rebind existing groups or operations.
- `RegisterAssetMedia(ctx, actor, AssetMediaRegistration) error`: the administrator
  attests an immutable, safely fetched object and supplies SHA-256, object ref,
  bytes, type, measured dimensions/duration/FPS, evidence ref and expiry. This
  function is **not** a downloader, virus scanner or proof of object ownership.
  The existing trusted workflow must verify the bytes before calling it. Client
  URL must later match the registered string exactly. Both it and the fetch URL
  exist only inside an authenticated Keyring envelope; lookup uses tenant/domain
  separated HMAC. Maximum validity is one hour from verified_at, matching S2's
  media freshness bound. Renew with a new immutable registration ID after fresh
  verification. Imported Ark assets retain the original metadata provenance.
- `RegisterAssetCallback(ctx, actor, AssetCallbackRegistration) error`: immutable
  tenant-bound HTTPS destination, evidence reference, maximum one-year expiry.
- `RevokeAssetMedia(ctx, actor, org, id) error` and `RevokeAssetCallback(...)`:
  mark revoked, cryptographically erase the registry ciphertext and audit.

Inputs and their exact JSON tags are declared in `assets.go`. Registry rows are
internal models. Reads of URLs append an audit event before returning any URL;
an audit write/commit failure fails closed. Admin mutations and audit are atomic.
Normal URL reads cannot cross organizations or substitute a different ciphertext.
Previously accepted immutable source bodies are retained under execution consent;
revoking a registry entry blocks future acceptance, not an already accepted send.

License review remains `Assets.VerifyLicenseTx` / `Assets.RevokeLicenseTx`, with
trusted reviewer evidence. The callback never grants a material license.

## Creation and Draft behavior

`plan` verifies registered media and asset references, same organization,
provider account/region/project and material license. `ValidateBindingsTx` takes the same tenant/group/license locks
as binding but inserts no task reference. After the task row exists,
`persistTaskResourcesTx` calls `ResolveBindingsTx` and compares the entire binding
snapshot, in the financial creation transaction. Any failure rolls back the task,
hold, evidence staging, parameter snapshot and resource references together.
`afterAssetsTerminalTx` delegates to `Assets.ReleaseBindingsTx`; root's terminal
worker may call the latter directly. Historical reference rows remain available.
Before the first provider POST, the root worker calls `ValidateTaskAssetsTx`
after locking the task and before ledger/quota locks. It rechecks current route,
scope capabilities, metadata version and current material licenses. Reject only
before sending; it must never reclassify an ambiguous/already-sent operation.

`video_task_parameters` stores only a typed effective-parameter whitelist, input
video duration, and routing/model provenance. Draft conversion requires a parent
owned by the same organization, successfully completed as a Draft, not hidden or
being deleted, the same model, exact upstream model and capability version, and the same provider
scope/account/region/project. Parent asset references are inherited and currently
re-authorized, including fresh real-person material licenses. The child gets its
own active references. No parent request or response body is read. Tasks predating
this metadata table fail closed for Draft conversion; no history parsing backfill.

The public task-creation contract continues to accept raw HTTPS image/audio URLs
and passes them to Ark without a platform fetch. Unregistered video URLs require
an explicit, verified `MaxInputVideoDurationMS` that represents the official
model's **aggregate total input-video duration upper bound**. When any video input
is unregistered, the entire aggregate freezes against that bound, not a sum of
customer assertions. We claim no immutability for raw external URLs; the actual
outbound request and frozen bound are retained. Registered media, asset references
and inherited Draft context provide trusted durations for more precise freezing.
The configured maximum also bounds those measured durations when present.
By contrast, the **CreateAsset material-management endpoint always requires the
immutable media registry** and never falls back to an unverified customer URL.

## Migration DDL (root assigns migration numbers)

No ORM AutoMigrate is used in production. Apply all five tables before this code.
Existing `video_asset_events` and `video_asset_tenant_locks` are reused.

```sql
CREATE TABLE video_asset_scope_configs (
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 version BIGINT UNSIGNED NOT NULL,
 account_ref VARCHAR(64) NOT NULL,
 region VARCHAR(32) NOT NULL,
 project_name VARCHAR(64) NOT NULL,
 capabilities_json TEXT NOT NULL,
 credential_cipher MEDIUMTEXT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 created_by BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(provider_scope_id,version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE video_asset_org_routes (
 org_id BIGINT UNSIGNED NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 scope_version BIGINT UNSIGNED NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 default_route BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(org_id,provider_scope_id),
 KEY idx_video_asset_org_route_default(org_id,enabled,default_route)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE video_asset_media_registry (
 id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 url_hmac CHAR(64) NOT NULL,
 media_type VARCHAR(16) NOT NULL,
 immutable_object_ref VARCHAR(64) NOT NULL,
 object_sha256 CHAR(64) NOT NULL,
 mime VARCHAR(64) NOT NULL,
 byte_size BIGINT NOT NULL,
 duration_ms BIGINT NOT NULL,
 width INT NOT NULL,
 height INT NOT NULL,
 fps_num INT NOT NULL,
 fps_den INT NOT NULL,
 version BIGINT UNSIGNED NOT NULL,
 verified_at DATETIME(3) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 evidence_ref VARCHAR(64) NOT NULL,
 value_cipher MEDIUMTEXT NOT NULL,
 revoked_at DATETIME(3) NULL,
 created_by BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_asset_media_lookup(org_id,url_hmac,media_type,expires_at),
 KEY idx_video_asset_media_expiry(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE video_asset_callback_registry (
 id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 url_hmac CHAR(64) NOT NULL,
 value_cipher TEXT NOT NULL,
 evidence_ref VARCHAR(64) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 revoked_at DATETIME(3) NULL,
 created_by BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_asset_callback_lookup(org_id,url_hmac,expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE video_task_parameters (
 task_id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 schema_version BIGINT UNSIGNED NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 provider_scope_version BIGINT UNSIGNED NOT NULL,
 account_ref VARCHAR(64) NOT NULL,
 region VARCHAR(32) NOT NULL,
 project_name VARCHAR(64) NOT NULL,
 public_model VARCHAR(64) NOT NULL,
 upstream_model VARCHAR(128) NOT NULL,
 capability_version VARCHAR(128) NOT NULL,
 parameters_json TEXT NOT NULL,
 has_input_video BOOLEAN NOT NULL,
 input_duration_ms BIGINT NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_task_parameters_org(org_id,task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
```

The per-org asset lock serializes route default changes. Only application admin
functions may write routes/config/registries. The HMAC secret is version-bound
like video idempotency: do not replace it in place; rotate through an explicit
registry/idempotency migration. Keyring encryption keys support ordinary rotation
while retaining old keys. Scope configuration version changes require an explicit
resource migration; they never silently change the Ark project of existing work.
