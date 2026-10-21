# Official generation verification profiles

`Load` is a pure, immutable evidence package. The server applies it only through
an explicit superadmin transaction with an expected configuration version. It
does not assert that every combination has been tested against a live channel.

Reviewed on 2026-10-19 against the official model pricing, model list and create
task API. `Sources` identifies their downloaded Markdown bytes by SHA-256. The
previously captured Lark tables keep their 2026-10-17 source hashes and timestamps
in `arkpricecatalog`; they are not relabeled as freshly captured. Minimum tokens
remain estimates for holds, never the actual settlement usage. The absent 2.0
FPS column is supplemented from the official model list (24fps); raw rows remain
unchanged. Profile hashes cover the merged facts and source references, and are
not labeled as raw document hashes.

Standard 2.0 has eight price conditions and 288 minimum cells; standard 2.5 has
six price conditions and 486 minimum cells. All six explicit ratios and the full
4–15/4–30 second duration domain are covered. Output dimensions are exact pairs
from the official create API table; larger non-16:9 areas are not overlooked.

The package intentionally excludes arbitrary-ratio 2.5 edit/extend/first-frame
and Draft workflows until their input-derived dimensions can be bounded safely.
For 2.5, reference-only capabilities explicitly send `omni_reference_task_type`
as `reference` only when the content contains reference images, videos or audio,
so the upstream default `auto` cannot choose an unpublished editing mode.
Text-only and first/last-frame generation omit this field; explicitly supplying
it in those scenarios is rejected locally. An image with no role remains a
first frame, not reference media. Draft conversion retains inherited context
without resending this field. Standard 2.0 first/last frame uses enumerated ratios.
Frames, flex and 1.x are not granted. Fast and Mini use independent variant
profiles rather than receiving capabilities through a standard profile.

Reviewed supplier rates (CNY / one million video tokens):

| Standard model | Resolution | No reference video | With reference video |
| --- | --- | ---: | ---: |
| 2.0 | 480p / 720p | 46 | 28 |
| 2.0 | 1080p | 51 | 31 |
| 2.0 | 4k | 26 | 16 |
| 2.5 | 480p / 720p | 70 | 42 |
| 2.5 | 1080p | 77 | 46 |
| 2.0 Fast | 480p / 720p | 37 | 22 |
| 2.0 Mini | 480p / 720p | 23 | 14 |

Fast and Mini each have four separately reviewed price conditions. `LoadTariff`
checks their own captured rows and official pricing evidence; it remains a
tariff-only API. `Load` separately provides each variant's reviewed generation
profile: 480p/720p, six explicit ratios, 4–15 seconds, 24fps and 144 minimum
cells. The bounds use the official ratio-specific pixel pairs. Each profile has
an independent minimum-rule version and evidence digest. Verification alone does
not publish prices or enable a channel; local administrative publication and
call/settlement acceptance are separate actions.

The variant applicability chain reviewed on 2026-10-19 is:

- `seedance-2-0.md` explicitly names standard, Fast and Mini as the 2.0 family
  and lists the exact `fast-260128` and `mini-260615` deployment IDs. Original
  bytes SHA-256: `1cc95eb91d8bb7f8a69965e0815ea6fff8cde90458e4d4e60d9c8b5c99e0c39e`.
- `model-pricing.md` applies reference-video minimum usage to the 2.0 family.
- The captured table `tblmNCuMjADrXtDf` is titled
  `seedance-2.0系列最低token限制（仅针对含输入视频时）`. Only its 480p and
  720p views are admitted for the variants. Their original hashes are pinned
  independently of the price examples and preserve the 2026-10-17 capture time.
- `get-video-generation-task-api.md` confirms that `completion_tokens` already
  includes the provider's minimum usage, and separately reported input tokens
  are zero. Original bytes SHA-256:
  `f7e6e434bb9cd12ba79d39b0cb2c80fbef8b56780e6efd6d95e98c4e1b9aabe8`.

The official pricing page currently gives the same `tblheIYDXXwYM43b` link for
both the 2.0 and 2.5 minimum tables. That is the captured 2.5 table, so the link
alone is not used as variant evidence. The reviewed 2.0 table identity, title,
view IDs and original hashes are retained explicitly. No missing row is
interpolated and no minimum is reverse-calculated from rounded money.

Fast `doubao-seedance-2-0-fast-260128` and Mini
`doubao-seedance-2-0-mini-260615` may additionally save a separate price plan in
the admin drawer. `GET/POST /api/admin/video/variant-price-plans/:channel/:model`
previews and saves the four official resolution/reference-video combinations.
Retail USD equals the official CNY price multiplied by the bound scope's existing
USD/CNY rate. The plan inherits the consistent risk multiplier from currently
active standard 2.0 rules on that channel; this platform hold policy is not an
official tariff and is not multiplied into retail prices.

The append-only plan stores its exact source, FX and risk-policy snapshots. Save
requires the complete preview context digest and an idempotency key, revalidates
the current context under locks, and commits an audit record in the same
transaction. It does not write executable pricing rules, create model
capabilities or change public model availability. The UI distinguishes a saved
plan from a current preview when the context changes. Historical price plans
remain immutable documentary snapshots; their former unverified minimum status
does not automatically change or activate a model. Publication uses the newly
reviewed variant profile and its independent minimum-rule version. The captured
26-row price examples still do not establish a complete minimum table.

Administration uses `GET/POST /api/admin/video/models/:model/verification`.
POST requires `expected_config_version` and `profile_version`. It atomically
adds the immutable minimum set, updates documented capabilities and bounds,
and appends configuration/audit records. It preserves current price versions,
retail and supplier rates, enabled flags, FX, channel, budgets and balances.
Replaying the same request is safe; stale or incompatible contexts return 409.

In the price editor, synchronize official rules, select desired cells and preview
“采用官方成本”. Adoption fills only missing supplier cost and new-cell minimum
bindings; it retains existing rates. Complete the USD retail price before
saving a new cell. The editor allows a fully configured cell to be saved while
disabled, but capability and minimum coverage are still required to enable it.
No new cell is published by rule synchronization or cost adoption.

The quote uses verified pixel pairs, 24fps and input/output duration, with the
applicable minimum estimate for reference-video holds. Actual settlement uses
the upstream `usage.completion_tokens`; it never substitutes the estimate or
adds a fabricated input-token charge. Zero separately reported input tokens
does not imply that reference-video duration is free.

Acceptance on 2026-10-19: both local models synchronized successfully; replay
returned the same configuration and a stale request was rejected. All 14
standard conditions passed prepare/validate with synthetic, unpublished prices.
Existing published rates, FX, balances and price publication history stayed
identical. Browser checks covered cost adoption, the missing-retail guard,
disabled-price preview and responsive right-side evidence drawers. No upstream
generation request or paid test was performed for this change.

Public sources:

- https://console.volcengine.com/ark/region:cn-beijing/docs/model-pricing
- https://console.volcengine.com/ark/region:cn-beijing/docs/model-list
- https://ark.volcengine.com/region:cn-beijing/docs/create-video-generation-task-api.md
- https://ark.volcengine.com/region:cn-beijing/docs/seedance-2-0.md
- https://ark.volcengine.com/region:cn-beijing/docs/get-video-generation-task-api.md
- Exact minimum-table view links and capture hashes are in `arkpricecatalog`.
