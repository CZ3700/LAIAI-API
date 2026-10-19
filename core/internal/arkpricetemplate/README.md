# Versioned Seedance pricing templates

`0120_video_pricing_templates.sql` creates the template library.
`0121_seedance_pricing_template_20261019.sql` installs the user's approved
`seedance-20261019-v1` price snapshot: standard 2.0 (8 conditions), 2.5 (6),
Fast (4), Mini (4). Seedance 1.x and unverified task modes are excluded.

The readable `data/seedance-20261019-v1.json` and the SQL contain the same
canonical documents. Tests pin each SHA-256 and verify exact parity. The SQL
must not contain `;` or `--` inside JSON strings because the migration runner
uses a simple statement splitter. The seed is a separate transactional DML
migration. Normal migration replay uses `schema_migrations` and makes no
changes. A conflicting manually inserted row fails instead of being updated.

Each document has fixed USD retail, CNY supplier cost, the platform hold-risk
multiplier, original FX metadata, source price revision, exact upstream model,
and reviewed capability/minimum evidence versions. The source FX explains the
snapshot, but applying the template must not change the target account FX or
recalculate its retail rates. Risk multiplier 4 is the existing platform hold
policy, not an official supplier price or a retail markup.

The library is independent of channel/account/organization/credential/budget
data. Installing it does not publish executable prices, enable a model, modify
existing live rules, or reset any consumed/reserved amounts. `Load` and `List`
only read installed database data and fail closed for absent schema, bad raw
hashes, noncanonical JSON, wrong exact model identity, or changed official
evidence. There is no startup or read-time seeding fallback.

Administrators can preview a template against a bound model/channel. Filling
missing conditions preserves complete existing rows, including disabled rows.
Replacement changes only template-covered prices and preserves existing enable
flags and all hidden modes. Newly filled conditions start disabled and require
an explicit enable decision. Application to a draft is not publication. The
ordinary prepare/validate/publication path checks the template again and stores
its reference in the publication snapshot and audit.

First-time configuration publication can use `pricing_template` instead of a
hand-written `prices` list. Account scope, target channel, FX and supplier budgets
still come from explicit target-environment configuration. Minimum rules are
imported transactionally from the matching reviewed profile, with the current
administrator as reviewer. No local IDs or test budgets belong in the template.

Future retail changes require a new template version and a new data migration.
Do not edit old migration files or mutate a published template in place.
