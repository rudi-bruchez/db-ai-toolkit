package sqlq

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseServer(t *testing.T) {
	tests := []struct {
		in       string
		host     string
		instance string
		port     string
	}{
		{"SRV01", "SRV01", "", ""},
		{"SRV01,1433", "SRV01", "", "1433"},
		{`SRV01\SQLEXPRESS`, "SRV01", "SQLEXPRESS", ""},
		{`SRV01\SQLEXPRESS,1435`, "SRV01", "SQLEXPRESS", "1435"},
		{"x.database.windows.net", "x.database.windows.net", "", ""},
		{"  SRV01 , 1433 ", "SRV01", "", "1433"},
		{"(local)", "(local)", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			host, instance, port := parseServer(tc.in)
			if host != tc.host || instance != tc.instance || port != tc.port {
				t.Errorf("parseServer(%q) = (%q, %q, %q); want (%q, %q, %q)",
					tc.in, host, instance, port, tc.host, tc.instance, tc.port)
			}
		})
	}
}

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestDSNIntegratedCarriesNoUser(t *testing.T) {
	p := Profile{Name: "p", Server: "SRV01", Database: "ERP", Auth: AuthIntegrated}
	driver, dsn, err := p.DSN("")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	if driver != DriverSQLServer {
		t.Errorf("driver = %q; want %q", driver, DriverSQLServer)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN is not a valid URL: %v", err)
	}
	if u.User != nil {
		t.Errorf("integrated auth must leave the user empty so SSPI kicks in, got %v", u.User)
	}
	if u.Host != "SRV01" {
		t.Errorf("host = %q; want SRV01", u.Host)
	}
	if got := u.Query().Get("database"); got != "ERP" {
		t.Errorf("database = %q; want ERP", got)
	}
}

func TestDSNSQLAuthReadsPasswordFromEnvironment(t *testing.T) {
	p := Profile{
		Name: "p", Server: `SRV02\SQLEXPRESS,1433`, Database: "L",
		Auth: AuthSQL, User: "svc_claude", PasswordEnv: "MSSQL_LEGACY_PWD",
	}
	driver, dsn, err := p.DSN("p@ss;word/1")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	if driver != DriverSQLServer {
		t.Errorf("driver = %q; want %q", driver, DriverSQLServer)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN is not a valid URL: %v", err)
	}
	if u.User.Username() != "svc_claude" {
		t.Errorf("user = %q; want svc_claude", u.User.Username())
	}
	pwd, _ := u.User.Password()
	if pwd != "p@ss;word/1" {
		t.Errorf("password = %q; special characters must survive URL encoding", pwd)
	}
	if u.Host != "SRV02:1433" {
		t.Errorf("host = %q; want SRV02:1433", u.Host)
	}
	if u.Path != "/SQLEXPRESS" {
		t.Errorf("path = %q; the named instance belongs in the path", u.Path)
	}
}

// DSN no longer knows where a secret came from, so naming the missing variable
// is the resolver's job (see TestEnvResolverNamesTheMissingVariable). What DSN
// still owes is a refusal: SQL authentication with an empty password must never
// produce a connection string.
func TestDSNSQLAuthRefusesAnEmptySecret(t *testing.T) {
	p := Profile{Name: "p", Server: "S", Auth: AuthSQL, User: "u", PasswordEnv: "MSSQL_ABSENT"}
	_, _, err := p.DSN("")
	if err == nil {
		t.Fatal("DSN should refuse SQL auth with no password")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error %q should say a password is missing", err)
	}
}

func TestDSNEntraSelectsAzureDriver(t *testing.T) {
	p := Profile{
		Name: "p", Server: "x.database.windows.net", Database: "D",
		Auth: AuthEntra, FedAuth: "ActiveDirectoryDefault",
	}
	driver, dsn, err := p.DSN("")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	if driver != DriverAzureAD {
		t.Errorf("driver = %q; want %q", driver, DriverAzureAD)
	}
	u, _ := url.Parse(dsn)
	if got := u.Query().Get("fedauth"); got != "ActiveDirectoryDefault" {
		t.Errorf("fedauth = %q; want ActiveDirectoryDefault", got)
	}
}

func TestDSNEntraDefaultsFedAuth(t *testing.T) {
	p := Profile{Name: "p", Server: "S", Auth: AuthEntra}
	_, dsn, err := p.DSN("")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	u, _ := url.Parse(dsn)
	if got := u.Query().Get("fedauth"); got != "ActiveDirectoryDefault" {
		t.Errorf("fedauth = %q; want the ActiveDirectoryDefault fallback", got)
	}
}

func TestDSNEncryptionDefaults(t *testing.T) {
	p := Profile{Name: "p", Server: "S", Auth: AuthIntegrated}
	_, dsn, _ := p.DSN("")
	u, _ := url.Parse(dsn)
	if got := u.Query().Get("encrypt"); got != "true" {
		t.Errorf("encrypt = %q; connections must be encrypted unless the profile says otherwise", got)
	}
	if got := u.Query().Get("trustservercertificate"); got != "false" {
		t.Errorf("trustservercertificate = %q; want false by default", got)
	}
}

func TestDSNHonoursTrustServerCertificate(t *testing.T) {
	p := Profile{Name: "p", Server: "S", Auth: AuthIntegrated, TrustServerCertificate: true}
	_, dsn, _ := p.DSN("")
	u, _ := url.Parse(dsn)
	if got := u.Query().Get("trustservercertificate"); got != "true" {
		t.Errorf("trustservercertificate = %q; want true", got)
	}
}

func TestDSNAppName(t *testing.T) {
	p := Profile{Name: "p", Server: "S", Auth: AuthIntegrated}
	_, dsn, _ := p.DSN("")
	u, _ := url.Parse(dsn)
	if got := u.Query().Get("app name"); got == "" {
		t.Error(`the connection must identify itself via "app name" so DBAs can spot it in sys.dm_exec_sessions`)
	}
}
