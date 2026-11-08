// Package conn opens goDTA's connections: profile selection (sqlq profiles or
// GODTA_* variables), DSN with goDTA's own application name, secret redaction,
// and the identity of the instance with the supported-version rule.
package conn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	_ "github.com/microsoft/go-mssqldb"         // registers "sqlserver"
	_ "github.com/microsoft/go-mssqldb/azuread" // registers "azuresql"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

// AppName identifies goDTA connections in sys.dm_exec_sessions, distinct from
// sqlq's own (spec section 6).
const AppName = "db-ai-toolkit/godta"

// PasswordEnv is the variable a GODTA_* profile reads its SQL password from.
// The password never goes into the profile itself: sqlq refuses inline ones.
const PasswordEnv = "GODTA_PASSWORD"

// ErrUsage marks a selection error the user must fix on the command line
// (exit code 1), as opposed to a connection failure.
var ErrUsage = errors.New("usage")

// ProfileFromEnv builds a profile from the GODTA_* variables. ok is false when
// GODTA_SERVER is empty: there is then no direct connection to build.
func ProfileFromEnv(getenv func(string) string) (p sqlq.Profile, ok bool, err error) {
	server := strings.TrimSpace(getenv("GODTA_SERVER"))
	if server == "" {
		return sqlq.Profile{}, false, nil
	}
	p = sqlq.Profile{
		Name:        "GODTA_*",
		Server:      server,
		Database:    strings.TrimSpace(getenv("GODTA_DATABASE")),
		Auth:        sqlq.AuthKind(strings.TrimSpace(getenv("GODTA_AUTH"))),
		User:        strings.TrimSpace(getenv("GODTA_USER")),
		Environment: strings.TrimSpace(getenv("GODTA_ENVIRONMENT")),
		Mode:        strings.TrimSpace(getenv("GODTA_MODE")),
	}
	if p.Mode == "" {
		p.Mode = sqlq.ModeReadOnly
	}
	if p.Auth == sqlq.AuthSQL {
		p.PasswordEnv = PasswordEnv
	}
	if v := strings.TrimSpace(getenv("GODTA_TRUST_SERVER_CERTIFICATE")); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return sqlq.Profile{}, true, fmt.Errorf("GODTA_TRUST_SERVER_CERTIFICATE: %q is not a boolean", v)
		}
		p.TrustServerCertificate = b
	}
	if verr := p.Validate(); verr != nil {
		return sqlq.Profile{}, true, fmt.Errorf("GODTA_* variables: %w", verr)
	}
	return p, true, nil
}

// Select returns the named profile from profilesPath (sqlq.DefaultProfilePath
// when empty) if a name is given, otherwise the GODTA_* profile, otherwise a
// usage error.
func Select(profilesPath, name string, getenv func(string) string) (sqlq.Profile, error) {
	if name != "" {
		if profilesPath == "" {
			profilesPath = sqlq.DefaultProfilePath()
		}
		ps, err := sqlq.LoadProfiles(profilesPath)
		if err != nil {
			return sqlq.Profile{}, fmt.Errorf("%w: %w", ErrUsage, err)
		}
		p, err := ps.Get(name)
		if err != nil {
			return sqlq.Profile{}, fmt.Errorf("%w: %w", ErrUsage, err)
		}
		return p, nil
	}
	p, ok, err := ProfileFromEnv(getenv)
	if err != nil {
		return sqlq.Profile{}, fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if !ok {
		return sqlq.Profile{}, fmt.Errorf("%w: no profile named and GODTA_SERVER is not set", ErrUsage)
	}
	return p, nil
}

// dsnFor builds the profile's DSN, then rewrites its application name and,
// when database is not empty, its database.
func dsnFor(p sqlq.Profile, secret, database string) (sqlq.Driver, string, error) {
	driver, dsn, err := p.DSN(secret)
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		// The parse error quotes the URL, which holds the password.
		return "", "", fmt.Errorf("profile %q: the DSN does not parse as a URL", p.Name)
	}
	q := u.Query()
	q.Set("app name", AppName)
	if database != "" {
		q.Set("database", database)
	}
	u.RawQuery = q.Encode()
	return driver, u.String(), nil
}

// Open resolves the profile's secret once, opens a pool on database (the
// profile's own when empty) and pings it. Every error returned is redacted.
func Open(ctx context.Context, p sqlq.Profile, resolve sqlq.SecretResolver, database string) (*sql.DB, error) {
	secret, err := resolve(p)
	if err != nil {
		return nil, Redact(err, secret)
	}
	driver, dsn, err := dsnFor(p, secret, database)
	if err != nil {
		return nil, Redact(err, secret)
	}
	db, err := sql.Open(string(driver), dsn)
	if err != nil {
		return nil, Redact(err, secret)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, Redact(err, secret)
	}
	return db, nil
}

// Redact replaces every spelling of secret in the error text by ***. The
// result does not wrap err: unwrapping would hand the unredacted text back.
func Redact(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	text := err.Error()
	out := text
	for _, form := range secretForms(secret) {
		out = strings.ReplaceAll(out, form, "***")
	}
	if out == text {
		return err
	}
	return errors.New(out)
}

// secretForms lists every spelling of the secret that could appear in an
// error. Copied from secretForms in tools/cmd/sqlq/main.go, which explains
// each form: the literal, the URL userinfo encoding of the DSN, the query
// encoding, the SQL literal with doubled apostrophes, and the showplan XML
// escaping of both.
func secretForms(secret string) []string {
	forms := []string{secret}
	add := func(s string) {
		if s == "" {
			return
		}
		for _, seen := range forms {
			if seen == s {
				return
			}
		}
		forms = append(forms, s)
	}
	add(strings.TrimPrefix(url.UserPassword("", secret).String(), ":"))
	add(url.QueryEscape(secret))
	doubled := strings.ReplaceAll(secret, "'", "''")
	add(doubled)
	add(xmlEscaper.Replace(secret))
	add(xmlEscaper.Replace(doubled))
	return forms
}

// xmlEscaper escapes text as SQL Server writes it into showplan XML.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

// Querier is what Identify and the phases need from a connection; *sql.DB and
// *sql.Conn both satisfy it.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Instance is the identity of the instance a connection reached.
type Instance struct {
	ServerName           string `json:"server_name"`
	MachineName          string `json:"machine_name"`
	Edition              string `json:"edition"`
	EngineEdition        int    `json:"engine_edition"`
	Major                int    `json:"major"`
	Level                string `json:"level"`
	Version              string `json:"version"`
	IsEnterpriseFeatures bool   `json:"is_enterprise_features"`
	CanCompress          bool   `json:"can_compress"`
	CPUCount             int    `json:"cpu_count"`
	MaxDOP               int    `json:"max_dop"`
	DefaultBackupPath    string `json:"default_backup_path"`
}

// identifyQuery reads the identity in one statement. SERVERPROPERTY returns
// sql_variant and ProductMajorVersion an nvarchar, value_in_use a sql_variant:
// every value is CAST to the type it is scanned into. A property the version
// does not know comes back NULL, hence the ISNULL.
const identifyQuery = `SELECT /* godta:internal */
  ISNULL(CAST(@@SERVERNAME AS nvarchar(256)), N''),
  ISNULL(CAST(SERVERPROPERTY('MachineName') AS nvarchar(256)), N''),
  ISNULL(CAST(SERVERPROPERTY('Edition') AS nvarchar(256)), N''),
  ISNULL(CAST(SERVERPROPERTY('EngineEdition') AS int), 0),
  ISNULL(CAST(CAST(SERVERPROPERTY('ProductMajorVersion') AS nvarchar(16)) AS int), 0),
  ISNULL(CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(128)), N''),
  ISNULL(CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), N''),
  ISNULL(CAST(SERVERPROPERTY('InstanceDefaultBackupPath') AS nvarchar(4000)), N''),
  (SELECT cpu_count FROM sys.dm_os_sys_info),
  ISNULL((SELECT CAST(value_in_use AS int) FROM sys.configurations
          WHERE name = N'max degree of parallelism'), 0);`

// RegistryBackupDir reads the setup's default backup directory from the
// instance registry. SERVERPROPERTY('InstanceDefaultBackupPath') is NULL
// before 2019 (measured on 2017 CU31, where this returns /var/opt/mssql/data).
// It is not part of Identify, which sends only SELECT statements.
func RegistryBackupDir(ctx context.Context, q Querier) (string, error) {
	var d sql.NullString
	if err := q.QueryRowContext(ctx, backupDirQuery).Scan(&d); err != nil {
		return "", fmt.Errorf("reading the default backup directory: %w", err)
	}
	return d.String, nil
}

const backupDirQuery = `DECLARE @d nvarchar(4000);
EXEC master.dbo.xp_instance_regread N'HKEY_LOCAL_MACHINE',
  N'Software\Microsoft\MSSQLServer\MSSQLServer', N'BackupDirectory', @d OUTPUT;
SELECT /* godta:internal */ @d;`

// Identify reads the instance identity.
func Identify(ctx context.Context, q Querier) (Instance, error) {
	var i Instance
	err := q.QueryRowContext(ctx, identifyQuery).Scan(
		&i.ServerName, &i.MachineName, &i.Edition, &i.EngineEdition, &i.Major,
		&i.Level, &i.Version, &i.DefaultBackupPath, &i.CPUCount, &i.MaxDOP)
	if err != nil {
		return Instance{}, fmt.Errorf("identifying the instance: %w", err)
	}
	deriveFeatures(&i)
	return i, nil
}

// deriveFeatures sets the flags that follow from the edition.
func deriveFeatures(i *Instance) {
	// EngineEdition 3 is Enterprise, Developer and Evaluation.
	i.IsEnterpriseFeatures = i.EngineEdition == 3
	// Standard (2) compresses backups, Web (also 2) does not, nor Express (4).
	i.CanCompress = i.EngineEdition == 3 ||
		(i.EngineEdition == 2 && !strings.HasPrefix(i.Edition, "Web"))
}

// CheckSupported applies the version rule: SQL Server 2016 SP3 and later, on
// premises or in a VM. Azure SQL Database is refused, and so is Managed
// Instance until it is measured (spec sections 4 and 15).
func CheckSupported(i Instance) error {
	switch i.EngineEdition {
	case 1, 2, 3, 4:
	case 5:
		return errors.New("azure sql database: not supported")
	case 8:
		return errors.New("managed instance: not measured")
	default:
		return fmt.Errorf("engine edition %d: not supported", i.EngineEdition)
	}
	switch {
	case i.Major < 13:
		return fmt.Errorf("SQL Server major version %d: not supported, SQL Server 2016 SP3 or later is required", i.Major)
	case i.Major == 13 && i.Level != "SP3":
		return fmt.Errorf("SQL Server 2016 %s: not supported, SP3 is required", i.Level)
	}
	return nil
}
