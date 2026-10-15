# Official Ark protocol module

This package is S1-A of the Ark gateway plan. It has no database tables,
migrations, ledger writes, task queue, credential persistence, or router changes.
It does not enable a production model or establish that an account has rights to
use any model. All non-historical test responses and capability flags are
simulated fixtures, not official account acceptance results.

## Integration API

```go
request, err := ark.Decode(reader, ark.DefaultMaxRequestBytes)
normalized, err := ark.Normalize(request, capability, verifiedBindings)
result := client.Submit(ctx, normalized)
transition := ark.ApplyObservation(currentStatus, result)
```

- `Decode` rejects duplicate keys at any depth, case-variant/unknown fields,
  request nulls, malformed types and trailing JSON. Request maximum is 10 MiB.
- `Normalize` requires explicit `Enabled`, `Verified`, version, official model
  mapping, duration/default/expiry limits, FPS, and allowed content counts.
  Media, frames, Draft, edit/extend, audio and last-frame support are separately
  gated; unset limits/flags do not silently enable a feature. It rejects old
  prompt parameter directives including normalized Unicode forms.
- `Bindings` must come from gateway checks of organization, resource readiness,
  provider account/region/project, licence, expiry, original Draft model and
  source-video billing context. A public `asset_`/`task_` ID alone is not access
  permission. `HasInputVideo` and `SourceDraftID` let billing recover the parent
  Draft input context without counting the Draft video as a new input video.
- `NormalizedRequest.Body` is official JSON; `Platform` holds max-charge/price
  version options that must never be sent to Ark. `Parameters` also includes
  Draft-inherited context which is deliberately absent from the outbound JSON.
- `Client.BuildSubmit/BuildQuery/BuildDelete` build official HTTPS inference
  requests. Base URLs must be an `ark.*.volces.com` host under `/api/v3`, without
  query, fragment or URL credentials. The orchestration layer still verifies
  that the configured host/region matches the pinned provider scope.
- `Client.Submit/Query/Delete` return `Result` without automatic application
  retry. Redirect following and replayable POST bodies are disabled. Production
  callers must supply the platform's guarded HTTP client/transport; custom
  middleware must not retry a non-idempotent request behind this package.
- `ParseResponse(action, response, maximum)` owns/closes the response body and
  captures at most 1 MiB. `Result.Body`, artifact URLs and original upstream IDs
  are internal evidence for Invocation Store, never a customer response or
  ordinary log entry. `Error` contains only safe synthesized text.
- `ApplyObservation` proposes a state change, preserving unknown and terminal
  states. `CancellationDecision` handles pre-dispatch local cancellation,
  intent-only cancellation while submission is unresolved and queued DELETE.
  Neither API authorizes charging or releasing money.

HTTP status and outcome are distinct. A successful DELETE is only acknowledged:
200 followed by 404 can mean that an already successful generation was deleted.
Only independently persisted cancellation/non-billing evidence allows the
gateway to release funds. Never retry unresolved create operations because an
outbox lease expired.

`Usage.CompletionTokens` and `TotalTokens` are pointers. Missing/null are absent;
explicit zero remains zero and still needs billing-rule validation. Negative,
fractional, string or overflowing counts invalidate the observation. Unknown
upstream status is preserved in `RawStatus` with empty normalized `Status` and
cannot become `running` automatically. `NeedsReview` and `UsagePending` are
signals for the coordinator, not financial decisions.

## Orchestration responsibilities

Persist normalized encrypted execution input and intent before dispatch; record
the returned receipt before applying a transition. Bind response identity and
provider scope, use CAS/fencing, and combine state, usage evidence, ledger,
quota, events and outbox in the intended transaction. Preserve legacy behavior
for other providers through explicit gateway wiring rather than changing the
existing generic task adaptor here.

Media URL syntax validation is not a downloader SSRF defense. Any gateway media
inspection/transfer must use DNS/IP/redirect checks, byte/time limits and pinned
metadata. Runtime rate limits, idempotency, monetary limits and business licence
authorization belong to the gateway, not this protocol package.

## Validation and provenance

The three successful historical fixtures are copied from
`internal/task/volcengine_fixture_test.go`, captured on 2026-07-05 for
`doubao-seedance-2-0-260128`; signed artifact credentials are redacted. They do
not establish current 2.5 capability or authorize any paid API call. New edge
cases deliberately use simulated responses. Tests cover strict decoding,
capability/asset/Draft gates, missing versus zero usage, bounded/partial
responses, identity mismatch, lost POST replies, redirects and cancellation
races. No live API is called.

No schema changes are requested by this module. Public DTOs and persisted
models remain the coordinator's responsibility.
