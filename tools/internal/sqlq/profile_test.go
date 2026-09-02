package sqlq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleProfiles = `{
  "prod-erp":    { "server": "SRV01", "database": "ERP",
                   "auth": "integrated", "mode": "readonly" },
  "prod-azure":  { "server": "x.database.windows.net", "database": "D",
                   "auth": "entra", "fedauth": "ActiveDirectoryDefault",
                   "mode": "readonly" },
  "prod-legacy": { "server": "SRV02\\SQLEXPRESS,1433", "database": "L",
                   "auth": "sql", "user": "svc_claude",
                   "passwordEnv": "MSSQL_LEGACY_PWD", "mode": "readonly" },
  "dev-local":   { "server": "localhost", "database": "tempdb",
                   "auth": "integrated", "mode": "readwrite" }
}`

func writeProfiles(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mssql-profiles.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProfiles(t *testing.T) {
	profiles, err := LoadProfiles(writeProfiles(t, sampleProfiles))
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(profiles) != 4 {
		t.Fatalf("loaded %d profiles; want 4", len(profiles))
	}

	p, err := profiles.Get("prod-erp")
	if err != nil {
		t.Fatalf("Get(prod-erp): %v", err)
	}
	if p.Server != "SRV01" || p.Database != "ERP" || p.Auth != AuthIntegrated {
		t.Errorf("prod-erp = %+v; unexpected fields", p)
	}
	if !p.ReadOnly() {
		t.Error("prod-erp should be read-only")
	}
	if p.Name != "prod-erp" {
		t.Errorf("Name = %q; Get should stamp the profile name", p.Name)
	}
}

func TestLoadProfilesDefaultsModeToReadOnly(t *testing.T) {
	path := writeProfiles(t, `{"p": {"server": "S", "database": "D", "auth": "integrated"}}`)
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	p, _ := profiles.Get("p")
	if !p.ReadOnly() {
		t.Error("a profile with no explicit mode must default to read-only")
	}
}

func TestGetUnknownProfileListsAvailable(t *testing.T) {
	profiles, _ := LoadProfiles(writeProfiles(t, sampleProfiles))
	_, err := profiles.Get("nope")
	if err == nil {
		t.Fatal("Get(nope) should fail")
	}
	if !strings.Contains(err.Error(), "prod-erp") {
		t.Errorf("error %q should list the available profile names", err)
	}
}

func TestLoadProfilesMissingFile(t *testing.T) {
	_, err := LoadProfiles(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("LoadProfiles on a missing file should fail")
	}
	if !strings.Contains(err.Error(), "absent.json") {
		t.Errorf("error %q should name the path it tried", err)
	}
}

func TestLoadProfilesRejectsInvalidJSON(t *testing.T) {
	if _, err := LoadProfiles(writeProfiles(t, "{not json")); err == nil {
		t.Fatal("LoadProfiles should reject malformed JSON")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
		wantErr string
	}{
		{"ok integrated", Profile{Server: "S", Auth: AuthIntegrated}, ""},
		{"ok sql", Profile{Server: "S", Auth: AuthSQL, User: "u", PasswordEnv: "V"}, ""},
		{"ok entra", Profile{Server: "S", Auth: AuthEntra}, ""},
		{"missing server", Profile{Auth: AuthIntegrated}, "server"},
		{"unknown auth", Profile{Server: "S", Auth: "kerberos-ish"}, "auth"},
		{"sql without user", Profile{Server: "S", Auth: AuthSQL, PasswordEnv: "V"}, "user"},
		{"sql without passwordEnv", Profile{Server: "S", Auth: AuthSQL, User: "u"}, "passwordEnv"},
		{"unknown mode", Profile{Server: "S", Auth: AuthIntegrated, Mode: "rw"}, "mode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.profile.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v; want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("Validate() = nil; want an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("Validate() = %v; want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestProfileNeverCarriesAPassword(t *testing.T) {
	// A password in the file is a mistake we refuse loudly rather than honour:
	// the design stores only the name of the environment variable.
	path := writeProfiles(t, `{"p": {"server": "S", "auth": "sql", "user": "u", "password": "hunter2"}}`)
	_, err := LoadProfiles(path)
	if err == nil {
		t.Fatal("a profile carrying an inline password must be rejected")
	}
	if !strings.Contains(err.Error(), "passwordEnv") {
		t.Errorf("error %q should point at passwordEnv", err)
	}
}
