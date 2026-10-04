## Verified by running

(No defects found by verbatim execution of the plan's code blocks that weren't resolved by following the plan's structure)

## Concluded by reading

**Defect 1 (Silent SQL corruption due to unsorted offsets in `Rewrite`)**
In `Rewrite` (Task 5), parameter replacements are applied by iterating backward over the `params` array (`for i := len(params) - 1; i >= 0; i--`). The comment correctly notes this is done "so earlier offsets stay valid." However, `params` contains the overrides in the exact order they were listed in the `params=` marker (passed through `h.Marker.Params` and `AnalyseOverrides`), which is not guaranteed to match their physical declaration order in the SQL script. If the user lists parameters in the marker out of their physical order, processing them backward will shift the text at the wrong offsets and silently corrupt the SQL before it is executed against the database. `AnalyseOverrides` or `Rewrite` must sort the `OverrideParam` slice by `start` offset before modifying the string.

**Defect 2 (Instructions describe logic instead of showing the code)**
The prompt explicitly states that "a step that describes instead of showing, or says 'similar to', is a defect." Tasks 1, 9, 10, and 11 violate this rule by describing the implementation instead of providing the exact code. For example, Task 1 Step 3 instructs the agent to replace the `FindWrites`/`FindContextChanges` blocks in `main.go` with a loop on `sqlq.Refusals`, keeping the current messages and codes; Task 9 Step 3 describes the flag validation logic for `-saved`, `-query`, and `-file`; and Task 11 Step 2 tells the agent to manually review and move comments in the documentation. These descriptions force the agent to infer the code, likely leading to bugs and costly revisions.

## Not a problem

- `Refusals` in Task 1 scans the tokenized batch via `firstOf`, while `FindWrites` scans `Statements` split by `\x00` and `;`. Since both use the same tokenization logic and search linearly, they correctly identify the exact same keyword and line. Verified by running `TestRefusalsAgreeWithTheGuards`.
- `QueryParams` in Task 4 correctly ignores variables inside strings and comments, as it operates on `toks` generated from `Sanitize(sql)`, which blanks out literals. Verified by running `TestDeclaredVariableIsOnlyTheDeclareTarget`.
- `checkNotAssigned` in Task 5 uses `comparisonContext` to correctly allow `IF`, `WHILE`, `WHERE`, and `ON` comparisons while rejecting true assignments or passing variables to stored procedures. Verified by reading.
- `RecordVerified` in Task 6 replaces the registry file atomically via a temporary file and `os.Rename`. It correctly merges the map but could lose a concurrent entry update, which is explicitly accepted by the design. Verified by reading.
