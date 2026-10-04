## Verified by running

**1. Lexical parameter extraction incorrectly flags parameter assignments as local variables.**
For `bundled` and `personal` queries, the design (carried over from September) defines a local variable as any `@x` introduced by a `DECLARE`, and a parameter as what remains. Running the existing `Sanitize` and `tokens` functions on `DECLARE @local INT = @cutoff;` yields the token stream `["DECLARE", "@local", "INT", "@cutoff"]`. Because there is no AST parser to distinguish the target of the assignment from the expression, the implementation will flag both `@local` and `@cutoff` as local variables. Consequently, `sqlq` will not enforce `-param cutoff=...`, resulting in a SQL Server error (`Must declare the scalar variable "@cutoff"`) at execution, which breaks the exact mechanic it was meant to solve.

**2. Multi-line initializers without parentheses bypass catalog checks but corrupt the rewritten SQL.**
Section 9 attempts to catch multi-line initializers by checking for unbalanced parentheses ("parenthèses déséquilibrées") because replacing them up to the first `;` or newline would "break the SQL". Running a check on `tsql-scripts` and evaluating T-SQL syntax confirms that multi-line initializers can be constructed without parentheses, for example using `CASE`: `DECLARE @dop int = CASE \n WHEN ...`. This bypasses the catalog validation check. `sqlq` will replace only the first line (`DECLARE @dop int = @sqlq_dop`), leaving `WHEN ...` stranded on the next line and generating a loud syntax error upon execution.

**3. Compound assignment operators silently bypass the parameter reassignment check.**
Section 9 checks for `SET @p =`, `SELECT @p =`, or `FETCH ... INTO` to reject scripts that would silently overwrite the value passed by the agent. Running `tokens` on `SET @p += 1` confirms that the `+=` operator is completely stripped out (returning `["SET", "@p", "1"]`). Since T-SQL supports compound assignment operators (`+=`, `-=`, `*=`, `/=`, etc.), a script using them will evade the strict `=` check and silently overwrite the agent's parameter, yielding an incorrect diagnostic result without any warning.

## Concluded by reading

**4. Renaming a marker drops the execution verification (Contradiction).**
Section 8 states that renaming a marker does not cause the verification to be lost because the query name is not part of the registry key ("renommer un marqueur ne fait pas perdre la vérification d'un contenu inchangé"). However, for `tsql-scripts`, the name is defined *inside* the file on the marker line itself (`-- sqlq: name=waits-statistics`). Renaming the marker modifies the file's content, which inevitably changes its `sha256` hash. Because the catalog validates execution by strictly comparing the file's current hash against the registry, the verification will immediately drop. `TestVerifiedSurvivesRename` is therefore impossible to implement under these rules.

**5. The CLI contract for `-save-query` requires the agent to flawlessly format the parameter block.**
Section 7 states that `-save-query` writes the `Parameters:` line based entirely on what the agent provides in the `-summary "<texte>"` argument. However, Section 11 specifies a test (`TestDeclaredParametersMatchTheSQL`) to ensure the header's parameter declarations perfectly match the SQL. If the agent makes a formatting mistake, typo, or omits a parameter in its summary string, the newly saved query will be immediately marked as `rejected` and unusable. `sqlq` should automatically generate the `Parameters:` documentation line using its own lexical extraction logic instead of relying on fragile free-text input from an LLM.

## Not a problem

- **`READ UNCOMMITTED` detection:** Searching the sanitized text for `READ UNCOMMITTED` works perfectly, as string literals and comments containing false positives are already blanked out by `Sanitize`.
- **SQL Injection via `-param`:** The design explicitly uses `sql.Named` instead of string concatenation, preventing any injection even if the parameter string is maliciously crafted.
- **`USE` statements and cross-database queries:** Handled correctly. `FindContextChanges` explicitly catches `USE`, and `FindWrites` catches `EXEC`, ensuring dynamic SQL cross-database tricks cannot bypass read-only or scope limits.
- **Single-line comments in replaced `DECLARE`:** If a line contains `DECLARE @p int = 1 -- default`, replacing to the end of the line safely deletes the comment in the executed SQL without altering the file or causing a syntax error.
- **Variable reassignments via `UPDATE` or `EXEC`:** While the reassignment checks (`SET`, `SELECT`, `FETCH`) miss reassignments via `UPDATE t SET @p = ...` or `EXEC @p = ...`, this is completely safe because `UPDATE` and `EXEC` are already rejected by the primary write guard.
