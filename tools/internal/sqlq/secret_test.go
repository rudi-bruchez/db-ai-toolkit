package sqlq

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func utf16leBytes(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// credentialFile writes a store holding one credential bound to server/login.
func credentialFile(t *testing.T, id, server, login, sha string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	body := fmt.Sprintf(`{
	  "managedBy": "registered-servers",
	  "source": { "path": %q, "sha256": %q, "sizeBytes": 1, "lastWriteTime": "2026-09-17T10:00:00.0000000+02:00" },
	  "credentials": { %q: { "blob": %q, "boundTo": { "server": %q, "login": %q } } }
	}`, filepath.Join(t.TempDir(), "RegSrvr17.xml"), sha, id,
		base64.StdEncoding.EncodeToString([]byte("blob")), server, login)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testResolver(t *testing.T, path, secret string) (*Resolver, *[]string) {
	t.Helper()
	var warnings []string
	r := &Resolver{
		CredentialsPath: path,
		Unprotect:       func([]byte) ([]byte, error) { return utf16leBytes(secret), nil },
		Warn:            func(m string) { warnings = append(warnings, m) },
	}
	return r, &warnings
}

func TestResolveDpapiReturnsThePassword(t *testing.T) {
	const secret = "s3cr3t"
	r, _ := testResolver(t, credentialFile(t, "g/s", "SRV01", "svc", "aaa"), secret)
	got, err := r.Resolve(Profile{Name: "p", Server: "SRV01", Auth: AuthSQL, User: "svc", PasswordDpapi: "g/s"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != secret {
		t.Errorf("Resolve = %q; want %q", got, secret)
	}
}

// The one control that refuses rather than warns.
//
// Editing a registration's server or login in SSMS without renaming it leaves
// the id untouched, so the export regenerates a profile pointing somewhere new
// while the stored credential still belongs to the old destination. Sending it
// anyway hands one account's password to a third party - and the 18456 that
// comes back is indistinguishable from an ordinary stale password, so the
// natural reaction is to import again and send it again.
func TestResolveRefusesACredentialBoundElsewhere(t *testing.T) {
	r, _ := testResolver(t, credentialFile(t, "g/s", "SRV01", "Hedet", "aaa"), "s3cr3t")

	for _, tc := range []struct{ name, server, user string }{
		{"different server", "SRV02", "Hedet"},
		{"different login", "SRV01", "dba"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Resolve(Profile{
				Name: "p", Server: tc.server, Auth: AuthSQL, User: tc.user, PasswordDpapi: "g/s"})
			if err == nil {
				t.Fatal("a credential bound elsewhere must be refused, not sent")
			}
			for _, want := range []string{"SRV01", "Hedet", tc.server, "Import-RegisteredServerCredentials"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
		})
	}
}

// Case differences are not a rebinding: SQL Server host names and login names
// are not case-sensitive, and refusing on case would break working setups.
func TestResolveAcceptsACaseDifferentBinding(t *testing.T) {
	r, _ := testResolver(t, credentialFile(t, "g/s", "SRV01", "svc", "aaa"), "s3cr3t")
	if _, err := r.Resolve(Profile{
		Name: "p", Server: "srv01", Auth: AuthSQL, User: "SVC", PasswordDpapi: "g/s"}); err != nil {
		t.Errorf("Resolve: %v", err)
	}
}

// Profile and credentials from two different generations is a warning, not a
// refusal: the binding check above has already caught the dangerous case, and a
// password that has not changed is still valid.
func TestResolveWarnsWhenGenerationsDisagree(t *testing.T) {
	r, warnings := testResolver(t, credentialFile(t, "g/s", "SRV01", "svc", "aaa"), "s3cr3t")
	p := Profile{Name: "p", Server: "SRV01", Auth: AuthSQL, User: "svc", PasswordDpapi: "g/s",
		Source: &SourceStamp{SHA256: "bbb"}}
	if _, err := r.Resolve(p); err != nil {
		t.Fatalf("a generation mismatch must warn, not refuse: %v", err)
	}
	if len(*warnings) == 0 {
		t.Fatal("expected a warning about mismatched generations")
	}
	if !strings.Contains((*warnings)[0], "different states") {
		t.Errorf("warning %q should say the two artifacts disagree", (*warnings)[0])
	}
}

func TestResolveMissingCredentialNamesTheScript(t *testing.T) {
	r, _ := testResolver(t, credentialFile(t, "g/s", "SRV01", "svc", "aaa"), "s3cr3t")
	_, err := r.Resolve(Profile{Name: "p", Server: "SRV01", Auth: AuthSQL, User: "svc", PasswordDpapi: "other"})
	if err == nil {
		t.Fatal("an unknown credential id must fail")
	}
	if !strings.Contains(err.Error(), "Import-RegisteredServerCredentials.ps1") {
		t.Errorf("error %q should say how to fix it", err)
	}
}

// A blob encrypted under another account must not send the user round in a
// circle: re-running the import copies the same undecryptable bytes.
func TestResolveDecryptionFailureSaysReEnterInSSMSFirst(t *testing.T) {
	r, _ := testResolver(t, credentialFile(t, "g/s", "SRV01", "svc", "aaa"), "")
	r.Unprotect = func([]byte) ([]byte, error) {
		return nil, fmt.Errorf("Key not valid for use in specified state")
	}
	_, err := r.Resolve(Profile{Name: "p", Server: "SRV01", Auth: AuthSQL, User: "svc", PasswordDpapi: "g/s"})
	if err == nil {
		t.Fatal("a decryption failure must be reported")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Re-running the import cannot fix it") {
		t.Errorf("error %q must say that re-importing does not help", msg)
	}
	ssms := strings.Index(msg, "Open SSMS")
	imp := strings.Index(msg, "run Import-RegisteredServerCredentials.ps1")
	if ssms < 0 || imp < 0 || ssms > imp {
		t.Errorf("the two repair steps must appear in order (SSMS first), got %q", msg)
	}
}

func TestResolveNoSecretForIntegrated(t *testing.T) {
	r, _ := testResolver(t, credentialFile(t, "g/s", "SRV01", "svc", "aaa"), "s3cr3t")
	got, err := r.Resolve(Profile{Name: "p", Server: "SRV01", Auth: AuthIntegrated})
	if err != nil || got != "" {
		t.Errorf("Resolve = (%q, %v); integrated auth carries no secret", got, err)
	}
}

func TestDecodeUTF16LE(t *testing.T) {
	if got, err := decodeUTF16LE(utf16leBytes("pässwörd")); err != nil || got != "pässwörd" {
		t.Errorf("decodeUTF16LE = (%q, %v)", got, err)
	}
	if _, err := decodeUTF16LE([]byte{0x41}); err == nil {
		t.Error("an odd number of bytes is not UTF-16 and must be reported")
	}
}

// Validate must accept passwordDpapi everywhere, including where it cannot be
// used. LoadProfiles validates every profile before one is selected, so
// refusing here would make a single Windows-only entry render the whole file
// unusable on Linux - -list-profiles included.
func TestValidateAcceptsDpapiOnEveryPlatform(t *testing.T) {
	p := Profile{Server: "S", Auth: AuthSQL, User: "u", PasswordDpapi: "g/s"}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate() = %v; passwordDpapi must validate anywhere", err)
	}
}

func TestValidateRefusesTwoSecretSources(t *testing.T) {
	p := Profile{Server: "S", Auth: AuthSQL, User: "u", PasswordEnv: "V", PasswordDpapi: "g/s"}
	err := p.Validate()
	if err == nil {
		t.Fatal("naming a password twice must be refused")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error %q should say the two are mutually exclusive", err)
	}
}

// The ownership marker and the provenance stamp must survive a round trip. If
// json.Unmarshal dropped them, the next rewrite would strip managedBy from
// every generated entry at once, turning them all into hand-written profiles -
// untouchable, and colliding with themselves on the following export.
func TestManagedByAndSourceSurviveARoundTrip(t *testing.T) {
	body := `{ "g/s": { "server": "SRV01", "auth": "sql", "user": "svc",
	    "passwordDpapi": "g/s", "mode": "readonly", "environment": "prod",
	    "managedBy": "registered-servers",
	    "source": { "path": "C:\\x\\RegSrvr17.xml", "sha256": "abc", "sizeBytes": 42,
	                "lastWriteTime": "2026-09-17T10:00:00.0000000+02:00" } } }`
	path := filepath.Join(t.TempDir(), "mssql-profiles.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	p := profiles["g/s"]
	if p.ManagedBy != "registered-servers" {
		t.Errorf("managedBy = %q; the ownership marker was lost", p.ManagedBy)
	}
	if p.Environment != "prod" {
		t.Errorf("environment = %q; want prod", p.Environment)
	}
	if p.Source == nil || p.Source.SHA256 != "abc" || p.Source.SizeBytes != 42 {
		t.Errorf("source = %+v; the provenance stamp was lost", p.Source)
	}
}

// Naming where a secret lives is the resolver's business, so naming the
// variable that is missing is too: DSN no longer sees it.
func TestEnvResolverNamesTheMissingVariable(t *testing.T) {
	resolve := EnvResolver(func(string) string { return "" })
	_, err := resolve(Profile{Name: "p", Server: "S", Auth: AuthSQL, User: "u", PasswordEnv: "MSSQL_ABSENT"})
	if err == nil {
		t.Fatal("an unset password variable must fail")
	}
	if !strings.Contains(err.Error(), "MSSQL_ABSENT") {
		t.Errorf("error %q should name the missing variable", err)
	}
}

// A secret on a mode that sends none is a contradiction, and it used to be
// worse than useless: resolving it eagerly turned a leftover key into a failed
// connection on a profile that had worked for months.
func TestIntegratedProfileMayNotNameASecret(t *testing.T) {
	for _, p := range []Profile{
		{Server: "S", Auth: AuthIntegrated, PasswordEnv: "V"},
		{Server: "S", Auth: AuthIntegrated, PasswordDpapi: "g/s"},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v: integrated auth sends no password; naming one must be refused", p)
		}
	}
}

// Entra flows split in two: those that carry an identity need a secret, those
// that do not must not name one.
func TestEntraSecretNeedsAUser(t *testing.T) {
	if err := (Profile{Server: "S", Auth: AuthEntra, PasswordEnv: "V"}).Validate(); err == nil {
		t.Error(`an Entra profile naming a password but no "user" must be refused`)
	}
	if err := (Profile{Server: "S", Auth: AuthEntra}).Validate(); err != nil {
		t.Errorf("an Entra profile with no identity is valid: %v", err)
	}
	if err := (Profile{Server: "S", Auth: AuthEntra, User: "u", PasswordEnv: "V"}).Validate(); err != nil {
		t.Errorf("an Entra profile with an identity and a secret is valid: %v", err)
	}
}

// usesSecret is what stops a mode from decrypting something it will not send.
func TestUsesSecretFollowsTheAuthMode(t *testing.T) {
	cases := []struct {
		name string
		p    Profile
		want bool
	}{
		{"sql", Profile{Auth: AuthSQL, User: "u", PasswordEnv: "V"}, true},
		{"integrated", Profile{Auth: AuthIntegrated}, false},
		{"integrated with a stray key", Profile{Auth: AuthIntegrated, PasswordEnv: "V"}, false},
		{"entra without identity", Profile{Auth: AuthEntra}, false},
		{"entra with identity and secret", Profile{Auth: AuthEntra, User: "u", PasswordDpapi: "g/s"}, true},
		{"entra with identity, no secret", Profile{Auth: AuthEntra, User: "u"}, false},
	}
	for _, tc := range cases {
		if got := tc.p.usesSecret(); got != tc.want {
			t.Errorf("%s: usesSecret() = %v; want %v", tc.name, got, tc.want)
		}
	}
}
