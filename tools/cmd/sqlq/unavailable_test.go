package main

import (
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

// The server's text for these errors is long, sometimes localised, and says
// nothing the number does not: the database cannot be queried here. Each one
// must come back as one short fixed sentence that says what to do instead.
func TestUnavailableDatabaseErrorsAreShortened(t *testing.T) {
	long := "login error: The target database, 'ERP', is participating in an availability " +
		"group and is currently not accessible for queries. Either data movement is suspended " +
		"or the availability replica is not enabled for read access. To allow read-only access " +
		"to this and other databases in the availability group, enable read access to one or " +
		"more secondary availability replicas in the group."
	for _, number := range []int32{976, 978, 942, 952, 922, 927, 4060} {
		got := sqlError(mssql.Error{Number: number, Class: 14, State: 1, Message: long}, "")
		if got.Number != number {
			t.Errorf("%d: number became %d", number, got.Number)
		}
		if got.Message != unavailableDatabase[number] {
			t.Errorf("%d: message = %q; want the fixed text", number, got.Message)
		}
		if len(got.Message) > 250 {
			t.Errorf("%d: message is %d bytes; the point is to be short", number, len(got.Message))
		}
	}
}

// The driver reports the LAST error of a login as the error (token.go,
// getError), and a database that cannot be opened is reported as 4060 then
// 18456. Read as 18456, it would tell the agent to fix a password that is
// correct, and to re-import credentials: the loop the 18456 advice exists to
// stop. The database error must win.
func TestDatabaseThatCannotOpenIsNotReportedAsBadPassword(t *testing.T) {
	cannotOpen := mssql.Error{Number: 4060, Class: 11, State: 1,
		Message: `Cannot open database "ERP" requested by the login. The login failed.`}
	loginFailed := mssql.Error{Number: errLoginFailed, Class: 14, State: 1,
		Message: "Login failed for user 'svc'."}
	last := loginFailed
	last.All = []mssql.Error{cannotOpen, loginFailed}

	got := sqlError(last, "")
	if got.Number != 4060 {
		t.Fatalf("number = %d; want 4060, the cause", got.Number)
	}
	if strings.Contains(got.Message, "SSMS") || strings.Contains(got.Message, "Import-") {
		t.Errorf("4060 must not send the agent to fix the password, got %q", got.Message)
	}
	if !strings.Contains(got.Message, "Do not retry") {
		t.Errorf("4060 must still say not to retry, got %q", got.Message)
	}
}

func TestOtherErrorsKeepTheServerText(t *testing.T) {
	const text = "Invalid object name 'dbo.Nope'."
	got := sqlError(mssql.Error{Number: 208, Class: 16, Message: text}, "")
	if got.Number != 208 || got.Message != text {
		t.Errorf("208 changed: %+v", got)
	}
}
