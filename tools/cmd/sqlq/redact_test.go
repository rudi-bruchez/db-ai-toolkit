package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// utf16le encodes text the way DPAPI hands it back, since SSMS stores the
// password as UTF-16 before encrypting it.
func utf16le(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func writeCredentials(t *testing.T, id, server, login string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	body := fmt.Sprintf(`{
	  "managedBy": "registered-servers",
	  "source": { "path": "C:\\nowhere\\RegSrvr17.xml", "sha256": "abc" },
	  "credentials": {
	    %q: { "blob": %q, "boundTo": { "server": %q, "login": %q } }
	  }
	}`, id, base64.StdEncoding.EncodeToString([]byte("not a real blob")), server, login)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSecretIsRedactedForEverySecretSource is the test the design asks for by
// name, and it exists because of a specific near miss.
//
// The redaction below is a last line of defence: a driver error quoting the
// connection string would otherwise print the password on stdout, and from
// there into an agent transcript. It was fed by a helper that only knew how to
// read an environment variable, so adding a second way to name a password would
// have returned "" for every profile using it - and redact("") is a no-op.
// Nothing would have broken, nothing would have been logged; the defence would
// simply have stopped defending, for exactly the profiles this project
// generates.
//
// So this asserts the whole chain per secret source: the resolver yields the
// secret, the DSN really carries it, and the error text comes back without it.
func TestSecretIsRedactedForEverySecretSource(t *testing.T) {
	const secret = "Synthetic!Passw0rd-42"
	const id = "site-prd/example"

	envProfile := sqlq.Profile{
		Name: "by-env", Server: "SRV01", Database: "ERP",
		Auth: sqlq.AuthSQL, User: "svc", PasswordEnv: "SQLQ_TEST_PWD",
	}
	dpapiProfile := sqlq.Profile{
		Name: "by-dpapi", Server: "SRV01", Database: "ERP",
		Auth: sqlq.AuthSQL, User: "svc", PasswordDpapi: id,
	}

	envResolver := sqlq.EnvResolver(func(k string) string {
		if k == "SQLQ_TEST_PWD" {
			return secret
		}
		return ""
	})
	dpapiResolver := (&sqlq.Resolver{
		CredentialsPath: writeCredentials(t, id, "SRV01", "svc"),
		Unprotect:       func([]byte) ([]byte, error) { return utf16le(secret), nil },
	}).Resolve

	cases := []struct {
		name    string
		profile sqlq.Profile
		resolve sqlq.SecretResolver
	}{
		{"passwordEnv", envProfile, envResolver},
		{"passwordDpapi", dpapiProfile, dpapiResolver},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := tc.resolve(tc.profile)
			if err != nil {
				t.Fatalf("resolving the secret: %v", err)
			}
			// The heart of it: an empty secret here means redaction is inert.
			if resolved != secret {
				t.Fatalf("resolved %q; want the secret. An empty or wrong value "+
					"leaves every error text below unredacted", resolved)
			}

			_, dsn, err := tc.profile.DSN(resolved)
			if err != nil {
				t.Fatalf("DSN: %v", err)
			}
			if !strings.Contains(dsn, "Synthetic") {
				t.Fatalf("the DSN does not carry the secret, so this test proves nothing")
			}

			// A driver error that quotes the connection string, which is the
			// case the redaction exists for.
			driverErr := fmt.Errorf("mssql: unable to open tcp connection with %s", dsn)
			structured := sqlError(driverErr, resolved)
			if strings.Contains(structured.Message, secret) {
				t.Errorf("the secret survived redaction in %q", structured.Message)
			}
			if !strings.Contains(structured.Message, "[redacted]") {
				t.Errorf("expected a redaction marker in %q", structured.Message)
			}
		})
	}
}

// A profile that carries no secret at all must not trip the redaction into
// replacing empty strings everywhere.
func TestNoSecretLeavesTextAlone(t *testing.T) {
	const text = "mssql: server does not exist"
	if got := redact(text, ""); got != text {
		t.Errorf("redact with no secret = %q; want it unchanged", got)
	}
}

// 18456 is the one error where the obvious reaction is the harmful one, so the
// advice has to travel with the error rather than live in a document.
func TestLoginFailedCarriesDoNotRetry(t *testing.T) {
	advised := adviseOn(&sqlq.SQLError{Number: errLoginFailed, Message: "Login failed for user 'dba'."})
	if !strings.Contains(advised.Message, "Do not retry") {
		t.Errorf("18456 must say not to retry, got %q", advised.Message)
	}
	if !strings.Contains(advised.Message, "SSMS") {
		t.Errorf("18456 should say where to fix the password, got %q", advised.Message)
	}

	other := adviseOn(&sqlq.SQLError{Number: 208, Message: "Invalid object name."})
	if strings.Contains(other.Message, "Do not retry") {
		t.Errorf("only 18456 gets the lockout advice, got %q", other.Message)
	}
}
