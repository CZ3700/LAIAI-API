# Official video reference catalog

This package is a read-only, Git-versioned capture of the six official Feishu
tables. It has no DB, HTTP client, pricing publication, minimum-rule import or
model-enablement dependency. `GET /api/admin/video/official-price-catalog`
requires an individual active superadmin session; the emergency shared token is
insufficient. It is not exposed through customer `/v1/video/models`.

The approved price policy for this delivery is: add official reference data,
preserve every existing customer retail price, and do not publish new price or
capability combinations. Official CNY supplier rates are not USD retail prices;
no margin, FX conversion, minimum charge or default selling price is invented.
Collection and verification used public documentation and read-only fixtures.
No live generation request or paid upstream call was made by this implementation.

`data/catalog.json` names every source, including unavailable captures. A
captured table includes the exact source URL/table/view, capture UTC timestamp,
original column headings, per-column semantics and precision, exact TSV byte
SHA-256, expected row count and source notes. The TSV contains source data rows
without a synthetic header. API `raw_rows` preserves the cell strings; no JS or
Go floating-point price conversions occur. Manifest SHA-256 is computed from the
embedded manifest bytes. A hash checks fidelity, not official authenticity.
The local `.gitattributes` disables TSV newline conversion so source byte hashes
remain valid on Windows and Linux. Capture timestamps describe evidence, never
the build time.

The manifest accounts for all six observed source tables: 2.5 prices/minimum,
2.0 prices/minimum, 1.5 prices and 1.0 prices. Unavailable captures remain
explicitly `missing`; unknown view IDs are null. `complete` becomes true only
when every listed table and all its expected source views have valid captures.
This describes capture completeness, not billing coverage, model support, price
freshness or readiness for production.

Version `ark-official-2026-10-17-v1` contains 6 tables, 12 independently captured
views and 719 original rows. All view links are under the source wiki
`FXaYwxzJ5i5Zdik32ipcWzt7nxd`; the manifest records full URLs and individual UTC
times on 2026-10-17, from 13:45:19Z through 14:43:41.251Z.

| Source table | Views and original row counts | Original columns |
| --- | --- | --- |
| 2.5 prices `tblwgvQd3CUQ9K6j` | `vew7f33fzS`: 48 | 10 |
| 2.5 minimum `tblheIYDXXwYM43b` | `vewPa39Do4`: 339 | 12 |
| 2.0 prices `tblns3WjGMNbR8sL` | 2.0 `vew7f33fzS`: 52; Fast `vewAf9cSae`: 26; Mini `vewFrH7ax8`: 26 | 10 each |
| 2.0 minimum `tblmNCuMjADrXtDf` | 480p `vew2CR2JYM`: 48; 720p `vewVBehIlq`: 48; 1080p `vewQ2CK6dj`: 48; 4k `vewrMAE6IZ`: 48 | 6 / 6 / 6 / 7 |
| 1.5 prices `tblyYwswtJHVhCLb` | Pro `vewJGEMo3E`: 12 | 14 |
| 1.0 prices `tbl9E3YrLJ3n8Bk5` | Pro `vewltUCWg2`: 12; Pro Fast `vewc6F7QJj`: 12 | 9 each |

Source collection used the visible official table views and copied all rows;
view names, headers and row totals were checked against the UI. Truncated 1.5
headings were read from the field context menu without selecting an edit action.
The 1.5 silent/audio rates and four separate example amount columns remain
separate; blank Draft amounts remain blank. The 1.0 source does not establish
online/offline discounts, so the catalog does not invent those cases.

Multi-view tables have an explicit `expected_views` list and one `captures`
entry per original view. Each entry retains its own URL, UTC time, byte hash,
row count and original TSV. The API concatenates rows for display only and
provides a derived one-based `first_row`; it never claims a fabricated combined
file was an official export. Each capture exposes its real `headers` and
`columns`. `resolution` on a capture is source-view context, not an added raw
cell. The 2.0 minimum table has six columns for 480p/720p/1080p and seven columns
for 4k, whose source includes a resolution column. `heterogeneous_views` is
derived by the loader; the UI then requires one source view at a time and uses
that view's schema. `raw_rows` still contains the original arrays in capture
order, with each capture's `first_row` identifying its range; consumers must use
the capture schema, never pad or reinterpret rows against the common schema.
Resolution selection can switch to the corresponding real source view.

Precision is `exact` (literal published value), `estimated` (official estimate),
`rounded` (displayed rounded money), or `not_applicable` (text). Semantics separate
official CNY per-million-token rates, estimated video tokens, reference video
amounts, reference second prices, minimum token estimates and minimum amounts.
The official minimum-token tables are estimates: they must remain `estimated`
when captured, and must not be passed into the stricter existing minimum-rule
publication API. Final chargeable usage is the provider's completion_tokens.
The unit/estimate interpretation was checked against the official model-pricing
documentation at https://docs.volcengine.com/docs/ark/model-pricing?lang=zh;
this documentation is separate from the preserved Feishu table captures.
Never infer minimum tokens from rounded money or identify a deployment model ID
from a series name. The 1.0/1.5 directory entries do not activate those models.

`reference_video_scope` distinguishes price rows with literal input durations
(`per_row_input_duration`), minimum references explicitly scoped to input videos
(`requires_reference_video`), and sources that do not specify that dimension
(`unspecified`). Minimum-table durations are reference conditions, never treated
as the current user's submitted duration. Missing cells remain missing; no value,
resolution, model ID, audio setting or rate is interpolated or invented.

To add a capture: obtain the complete public source; preserve the original rows
and headings; record the actual UTC capture time and all conditions/footnotes;
review every column's unit and precision; add the TSV and checksum; set the
expected row count and all expected views; change that entry to captured;
increment the catalog version. If a source view supplies context absent from its
raw columns, retain that context separately and explain it in the notes.
Do not include browser cookies, credentials, account data or signed URLs. Run
catalog tests, API authorization tests, Go vet and web build. Review the resulting
diff and commit the evidence and manifest together. Failed hash, row/column or
unit validation makes the endpoint unavailable rather than returning a partial
successful catalog. No deployment changes an existing retail price, task snapshot,
quota, capability or minimum rule.

`ReferenceCandidates` additionally projects the verified embedded evidence for
standard `seedance-2.0` and `seedance-2.5` only. Its administrative GET endpoint
is `/api/admin/video/models/:model/pricing/reference-candidates`, with the same
individual active superadmin requirement. It exposes 8 and 6 standard
generate/default/non-Draft cost cells respectively; Fast/Mini and all 1.x remain
excluded. Each cost cell has exact CNY/1M_tokens semantics and points to every
contributing original source row. Conflicting values fail closed.

Minimum candidates expand only literal paired aspect ratios and deduplicate
equal primary dimensions/values, retaining every original row reference. The
2.0 result has 288 cells from 192 rows and unknown FPS (`null`); the 2.5 result
has 486 cells from 339 rows, including 15 duplicate rows whose evidence survives.
The primary source FPS field is distinct from linked `帧率(1)`. Candidates retain
`estimated`, `hold_estimation_only`, and `requires_operator_verification`; they
are not a verified minimum-rule import or a settlement floor. No FPS, verified
actor, observation date or production version is manufactured.

The server joins current prices by model/scope/version and copies the existing
USD retail, enabled state, risk multiplier and minimum version into the response.
New cells keep empty commercial fields and remain disabled. A reference cost is
only returned as a candidate, never persisted by a GET. Unverified capability or
a missing provider/channel binding makes the candidate unavailable; a conflicting
standard upstream series or duplicate current pricing cell rejects the response.
Existing publication, version checks and evidence registration remain separate
explicit operations. This feature performs no paid upstream requests.
