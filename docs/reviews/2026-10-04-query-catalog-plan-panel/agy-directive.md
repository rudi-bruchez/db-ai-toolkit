## Verified by running

**Task 5 `AnalyseOverrides` misses `OUTPUT` and `EXEC OUT` (Claim 1)**
The `checkNotAssigned` function only rejects reassignments when the target parameter is followed by `=` or a compound assignment operator. It accepts `OUTPUT inserted.a INTO @p` and `EXEC sp_name @param = @p OUT`. The `OUTPUT` rewrite will fail to compile on the server (as scalar variables are invalid targets for `OUTPUT`), while `EXEC OUT` will silently overwrite the parameter value passed by the user, leading to a successful run with a different value.
Command run:
`go run test_claim1.go` (with a probe testing `OUTPUT INTO` and `EXEC OUT`)
Output:
```
[ACCEPTED] OUTPUT into @p -> Rewritten:
DECLARE @p int = @sqlq_p;
UPDATE T SET a = 2 OUTPUT inserted.a INTO @p;
SELECT @p;
[ACCEPTED] EXEC OUT -> Rewritten:
DECLARE @p int = @sqlq_p;
EXEC sp_name @param = @p OUT;
SELECT @p;
```

**Test filters match exactly the expected counts (Claim 6)**
The regexes provided in the `-run` commands for each task (`^(TestA|TestB)$` or `^(TestA|TestB)`) strictly match only the tests they are supposed to run, even after later tasks add more tests with the same prefixes.
Command run: A Python script checking the regexes against the full list of tests extracted from the plan.
Output: `MISMATCH! Over-matches: []` for all filters.

## Concluded by reading

**Possible deadlock on `MsgNotice` during `rows.NextResultSet()` (Claim 2)**
The message loop in `collect` reads from `ReturnMessage`. When the end of a result set is reached, it dequeues `MsgNextResultSet` and calls `rows.NextResultSet()`. This call blocks `collect` while `go-mssqldb` reads tokens from the network. If the server sends many `PRINT` statements before the next result set, the driver will try to enqueue `MsgNotice` into `m.queue`. Since `ReturnMessageInit` creates a channel with a capacity of 15, sending a 16th notice will block the driver. With `collect` blocked waiting for the driver to return from `NextResultSet()`, the process will deadlock.

**Untested requirement: file deletion on invalid save (Claim 7)**
Requirement §12 states that if `-save-query` writes a file that fails to parse back as a valid entry, the file must be deleted and the run must fail. While Task 10 implements this logic in `writeSavedQuery`, there is no test for it in the plan. The test `TestSaveRefusesCommentBreakingSummary` is introduced in Task 8 and only tests `SavedFileContent` in isolation. Commenting out the `os.Remove(path)` block in Task 10 would pass all tests.

**Missing test: `TestBoundQueryRefusedOnAnotherProfile` (Claim 7)**
Requirement §7 states that a query bound to a profile directory cannot run on another profile. This is correctly implemented in `missingQueryError`, but the test `TestBoundQueryRefusedOnAnotherProfile` specified in the design (§16) is completely missing from the plan's tasks.

**`run` uses `profile` instead of `p` (Claim 5)**
In Task 9, the plan modifies `run` to call `execute(profile, sqlText, named, o, resolver.Resolve)`. However, the local variable for the loaded profile is conventionally named `p` (as seen in the integration tests: `res, code := execute(p, ...)`). The name `profile` is undefined here, which will cause a compile error.

## Not a problem

- **Task 1 Refusals**: `FindWrites` and `firstOf` use the same sanitized text and identical word definitions; they always find the exact same keyword in the exact same textual order (Claim 3).
- **Task 2 error handling**: `go-mssqldb` enqueues `MsgError` and then `MsgNextResultSet` at the end of the batch, so the loop cleanly exits. Rows read before the error in the first result set are preserved because `collect` assigns `result.Rows = set.Rows` before the error breaks the loop (Claim 2).
- **Task 7 Catalogue**: `WalkDir` traverses lexically matching the expected order, Git refuses `.git` so it cannot be committed, and stripping the marker line before hashing matches the design requirement that the hash is of the "private file" content (Claim 4).
- **Task 10 `-save-query` Overwrites**: `writeSavedQuery` uses `os.O_EXCL`, so it never overwrites an existing file. It only runs if `code == exitOK`, so it never leaves a file for SQL that failed to execute (Claim 5).
