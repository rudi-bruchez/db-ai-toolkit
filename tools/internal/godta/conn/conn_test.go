package conn

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestProfileFromEnvBuildsSQLProfile(t *testing.T) {
	env := map[string]string{
		"GODTA_SERVER":      "lab,11433",
		"GODTA_AUTH":        "sql",
		"GODTA_USER":        "sa",
		"GODTA_ENVIRONMENT": "lab",
	}
	p, ok, err := ProfileFromEnv(envOf(env))
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if p.Server != "lab,11433" || p.Auth != sqlq.AuthSQL || p.PasswordEnv != "GODTA_PASSWORD" ||
		p.Environment != "lab" || p.Mode != sqlq.ModeReadOnly || p.User != "sa" {
		t.Errorf("profile %+v", p)
	}

	env["GODTA_MODE"] = "readwrite"
	p, _, err = ProfileFromEnv(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != sqlq.ModeReadWrite {
		t.Errorf("mode %q, want %q", p.Mode, sqlq.ModeReadWrite)
	}
}

func TestProfileFromEnvAbsent(t *testing.T) {
	p, ok, err := ProfileFromEnv(envOf(map[string]string{"GODTA_USER": "sa"}))
	if ok || err != nil {
		t.Fatalf("ok %v, err %v, profile %+v", ok, err, p)
	}
}

func TestAppNameIsRewritten(t *testing.T) {
	p := sqlq.Profile{Name: "t", Server: "lab,11433", Auth: sqlq.AuthSQL, User: "sa", PasswordEnv: "X"}
	_, dsn, err := dsnFor(p, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "app+name=db-ai-toolkit%2Fgodta") || strings.Contains(dsn, "sqlq") {
		t.Errorf("dsn %s", dsn)
	}
}

func TestDatabaseIsRewritten(t *testing.T) {
	p := sqlq.Profile{Name: "t", Server: "lab,11433", Database: "master",
		Auth: sqlq.AuthSQL, User: "sa", PasswordEnv: "X"}
	_, dsn, err := dsnFor(p, "x", "godta_it_abc")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query()["database"]; len(got) != 1 || got[0] != "godta_it_abc" {
		t.Errorf("database %v in %s", got, u.Redacted())
	}
}

func TestCheckSupported(t *testing.T) {
	cases := []struct {
		name    string
		in      Instance
		wantErr string // "" means accepted
	}{
		{"2016 SP2", Instance{EngineEdition: 3, Major: 13, Level: "SP2"}, "SP3"},
		{"2016 SP3", Instance{EngineEdition: 3, Major: 13, Level: "SP3"}, ""},
		{"2017 RTM", Instance{EngineEdition: 3, Major: 14, Level: "RTM"}, ""},
		{"2014 SP3", Instance{EngineEdition: 3, Major: 12, Level: "SP3"}, "2016"},
		{"Azure SQL Database", Instance{EngineEdition: 5, Major: 16, Level: "RTM"}, "azure sql database"},
		{"Managed Instance", Instance{EngineEdition: 8, Major: 16, Level: "RTM"}, "managed instance: not measured"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckSupported(c.in)
			switch {
			case c.wantErr == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.wantErr != "" && err == nil:
				t.Errorf("accepted, want a refusal containing %q", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Errorf("error %q, want it to contain %q", err, c.wantErr)
			}
		})
	}
}

func TestRedactRemovesSecret(t *testing.T) {
	secret := "p@ss;w'rd"
	err := errors.New("login failed for sa with p@ss;w'rd in sqlserver://sa:p%40ss%3Bw%27rd@lab")
	got := Redact(err, secret).Error()
	for _, form := range []string{secret, "p%40ss%3Bw%27rd"} {
		if strings.Contains(got, form) {
			t.Errorf("%q still in %q", form, got)
		}
	}
	if !strings.Contains(got, "***") {
		t.Errorf("no *** in %q", got)
	}
}

func TestFeatureFlags(t *testing.T) {
	cases := []struct {
		edition        string
		engine         int
		enterprise, cc bool
	}{
		{"Developer Edition (64-bit)", 3, true, true},
		{"Standard Edition (64-bit)", 2, false, true},
		{"Web Edition (64-bit)", 2, false, false},
		{"Express Edition (64-bit)", 4, false, false},
	}
	for _, c := range cases {
		i := Instance{Edition: c.edition, EngineEdition: c.engine}
		deriveFeatures(&i)
		if i.IsEnterpriseFeatures != c.enterprise || i.CanCompress != c.cc {
			t.Errorf("%s: enterprise %v compress %v, want %v %v",
				c.edition, i.IsEnterpriseFeatures, i.CanCompress, c.enterprise, c.cc)
		}
	}
}

func TestSelectWithoutProfileOrServerIsUsage(t *testing.T) {
	_, err := Select("", "", envOf(nil))
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err %v, want ErrUsage", err)
	}
	p, err := Select("", "", envOf(map[string]string{
		"GODTA_SERVER": "lab", "GODTA_AUTH": "sql", "GODTA_USER": "sa"}))
	if err != nil || p.Server != "lab" {
		t.Fatalf("profile %+v, err %v", p, err)
	}
}
