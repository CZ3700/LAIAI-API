# Private video artifacts (S2-A)

This independent module is an implementation unit, not a production enablement
or official account acceptance result. Tests use simulated media, DNS, provider
responses and private blob storage. No live official download has been run.
No task state, ledger, router, dependency or migration is changed here.

## Integration

```go
repo := videoartifact.Repository{DB: db}
// Same transaction that records evidenced succeeded + trusted output metadata:
_, err := repo.RegisterTx(tx, videoartifact.Spec{
    OrgID: orgID, TaskID: taskID, Kind: videoartifact.Video,
    ExpiresAt: disclosedRetentionDeadline,
}, now)

downloader, err := videoartifact.NewDownloader(videoartifact.DownloadConfig{
    AllowedHosts: verifiedExactOfficialOutputHosts,
    MaxBytes: configuredByteLimit, Timeout: downloadTimeout,
    TempDir: privateTemporaryDirectory,
})
worker := videoartifact.Worker{Repo: repo, Resolver: resolver,
    Downloader: downloader, Blobs: privateBlobs,
    LeaseDuration: leaseTTL, TransferTimeout: transferTimeout, MaxAttempts: 3}
record, err := worker.Transfer(ctx, orgID, taskID, videoartifact.Video, false)

service := videoartifact.Service{Repo: repo, Blobs: privateBlobs}
content, err := service.Open(ctx, orgID, taskID, videoartifact.Video, now)
// Success: close content.Body, set content.Metadata.MediaType and Size,
// Cache-Control: private, no-store; stream bytes through authenticated endpoint.
```

The orchestrator must validate task ownership and succeeded evidence before
`RegisterTx`. Register last_frame only when return_last_frame was requested and
the response contains that output. `SourceResolver.ResolveSource(ctx, org, task,
kind) (string, error)` must recheck scope and return a temporary HTTPS URL; it may
perform a controlled official GET when history capture is disabled. URLs,
signature query strings and provider credentials never enter this table or safe
errors. Artifact failure does not change generation success or imply a refund.

`RegisterTx` never extends retention on repeat. `Claim` uses an atomic fence and
random lease token. Only one worker can obtain a live lease; a stale worker cannot
publish over a new attempt, task deletion or expiration. `ready` is reused without
another source resolution/download. A failed attempt or expired in-flight lease
requires explicit `retry=true`, bounded by MaxAttempts. A retry can consume an
additional official download, even if the previous attempt's final outcome is
unknown; the scheduler must account for provider download limits and source URL
expiry. Neither SQL nor blob storage can guarantee exactly-once external GET.

`TransferTimeout < LeaseDuration` is required. Each object key includes its fence
and random lease token. `BlobStore.Put` must atomically publish the complete
verified object and honor cancellation; no partial object may be readable.
Ambiguous DB completion leaves its object intact so it cannot delete a possibly
committed artifact. Unreferenced attempt objects require a private-storage
lifecycle/reconciliation job, with retention longer than active leases; do not
delete all objects merely because a lease expired. Metadata is authoritative only
after `Complete` succeeds. Implementations must not serve arbitrary blob keys.

Task soft deletion MUST call `HideTaskTx` in the same transaction: it hides both
kinds and invalidates leases. `Cleanup` is an independent retryable operation
for hidden/expired objects and never deletes financial evidence. `Open` checks
org, hidden/expired state and readiness before storage access, then rechecks the
fence and visibility after opening. Already-streaming bytes cannot be revoked.
The HTTP orchestrator must additionally verify its task visibility and current
time before streaming, and enforce its normal authentication/rate limits.

Downloads require exact approved lowercase DNS names, HTTPS port 443, no userinfo,
no fragment and no redirects. All resolved IPs are checked, then the selected IP
is pinned at dial time; private, metadata, local, CGNAT, multicast, reserved,
NAT64 and transition ranges are blocked. Proxy environment variables, cookies,
provider auth, transparent compression and reused-connection GET retries are
disabled. MIME, magic/header, content length, maximum bytes and timeout are
checked. A private temporary file prevents unbounded RAM usage and is removed
after Put or failure. Startup/crash cleanup for this private temporary directory
must be configured by deployment. MP4/MOV header validation is not a full codec
or container safety scan. Video dimensions are trusted official metadata when
available; JPEG dimensions are parsed from the downloaded header.

## Exact MySQL 8 DDL for the migration owner

The root integration owner must place this in a numbered migration and add its
schema-doctor expectations. This package intentionally does not AutoMigrate.
Use the project's standard UTC connection settings. No foreign key is proposed
because existing task storage lifecycle is controlled by orchestration.

```sql
CREATE TABLE video_artifacts (
  org_id BIGINT UNSIGNED NOT NULL,
  task_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
  kind VARCHAR(16) COLLATE utf8mb4_bin NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  object_key VARCHAR(256) NOT NULL DEFAULT '',
  sha256 CHAR(64) NOT NULL DEFAULT '',
  size BIGINT NOT NULL DEFAULT 0,
  media_type VARCHAR(64) NOT NULL DEFAULT '',
  width INT NULL,
  height INT NULL,
  fence BIGINT UNSIGNED NOT NULL DEFAULT 0,
  lease_token CHAR(32) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  lease_until DATETIME(6) NULL,
  attempts INT NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  expires_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  deleted_at DATETIME(6) NULL,
  cleaned_at DATETIME(6) NULL,
  PRIMARY KEY (org_id, task_id, kind),
  KEY ix_video_artifacts_pending (status, lease_until, updated_at),
  KEY ix_video_artifacts_expiry (expires_at, cleaned_at),
  KEY ix_video_artifacts_deletion (deleted_at, cleaned_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

Public content errors: 404 nonexistent/cross-org/hidden, 409 not ready, 410 past
retention, 503 storage unavailable. Safe fixed error codes are typed `*Error`;
callers must not infer financial outcomes from them. `Record` is internal, never
a public JSON DTO. Return only platform content URLs and safe content metadata.

Tests use handwritten SQLite DDL, with concurrent CAS coverage and simulated
transport/blob faults. MySQL locking, cloud object-store semantics, deployment
ACLs/cleanup and approved official CDN host discovery remain integration gates.
