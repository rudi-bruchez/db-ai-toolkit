# Breaking review — `feat/query-catalog`

## Verified by running

Baseline: `cd tools && rtk go test ./... -count=1` returned `Go test: 265 passed in 4 packages` without a SQL Server. Temporary probes run with `rtk proxy go test ./cmd/sqlq ./internal/sqlq -run 'TestReview' -v -count=1` returned `PASS` for all three cases below. The probes were removed after the run.

1. **The read-only guard accepts a statement that advances a sequence.** `Refusals("SELECT NEXT VALUE FOR dbo.order_numbers AS n;")` returned an empty slice (`TestReviewSequenceWrite: PASS`); the existing suite also passed. The write keyword list ([guard.go](tools/internal/sqlq/guard.go#L16)) does not recognize `NEXT VALUE FOR`. On a profile with permission to use the sequence, this `SELECT` changes persistent sequence state without `-allow-write`. No server was available, so the increment itself is inferred from SQL Server's `NEXT VALUE FOR` semantics; only guard acceptance was run here.

2. **A saved query can write outside the personal query root.** A temporary probe made `personal/profiles/p` a symlink to another temporary directory, then called `writeSavedQuery` for `probe.sql`. It returned success and the file existed in the outside directory (`TestReviewSaveThroughProfileDirSymlink: PASS`). `ProfileDir` validates path segments but does not resolve parent symlinks ([catalog.go](tools/internal/sqlq/catalog.go#L128)); `MkdirAll` and `OpenFile` follow that symlink ([main.go](tools/cmd/sqlq/main.go#L314)). The file readback also succeeds through it, so the final assertion does not catch the escape. This breaks the save boundary when a profile directory is already a symlink.

3. **A catalogue rejection publishes SQL text.** A marked script containing `SELECT @sqlq_secret_local_path;` produced `"rejected":"identifier @sqlq_secret_local_path at line 4 uses the reserved @sqlq_ prefix"` in marshalled `LoadCatalog` output (`TestReviewRejectedReasonLeaksIdentifier: PASS`). The identifier is copied into the error in [override.go](tools/internal/sqlq/override.go#L65), then into the catalogue rejection. A path or sensitive value encoded in that identifier reaches every `-list-queries` transcript, despite the rule that rejection reasons contain no SQL excerpt.

## Concluded by reading (hypotheses)

No additional breaking hypothesis is retained. The sequence's server-side effect and the symlink scenario's impact on a user's configured path need environment-specific confirmation; the local acceptance and file escape above were observed.

## Not a problem

- `-saved` uses SQL bytes held by the catalogue loader rather than reopening the query file after listing.
- The catalogue rejects an absent bundled query directory before `-saved` or `-save-query` can rely on its name set.
- `-save-query` refuses an explicitly writing batch and a run made with `-dirty-reads`.
- Registry read and write errors shown in catalogue messages strip operating-system paths.

Defects left out: **0**.
