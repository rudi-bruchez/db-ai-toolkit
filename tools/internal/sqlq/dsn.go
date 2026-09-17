package sqlq

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Driver names the database/sql driver to open the connection with. Both are
// registered by go-mssqldb; the Azure one lives in its azuread subpackage.
type Driver string

const (
	DriverSQLServer Driver = "sqlserver"
	DriverAzureAD   Driver = "azuresql"
)

// AppName identifies sqlq connections in sys.dm_exec_sessions, so a DBA
// looking at the instance can tell who is asking.
const AppName = "db-ai-toolkit/sqlq"

// defaultFedAuth is the Entra ID flow used when a profile does not pick one.
// ActiveDirectoryDefault walks the usual credential chain (environment,
// managed identity, Azure CLI, interactive).
const defaultFedAuth = "ActiveDirectoryDefault"

// parseServer splits a SQL Server address into host, named instance and port.
// It accepts the shapes DBAs actually type: HOST, HOST,PORT, HOST\INSTANCE and
// HOST\INSTANCE,PORT.
func parseServer(s string) (host, instance, port string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ","); i >= 0 {
		port = strings.TrimSpace(s[i+1:])
		s = s[:i]
	}
	if i := strings.Index(s, `\`); i >= 0 {
		instance = strings.TrimSpace(s[i+1:])
		s = s[:i]
	}
	return strings.TrimSpace(s), instance, port
}

// DSN builds the driver name and connection URL for a profile from an
// already-resolved secret. Pass "" for the modes that carry none.
//
// It takes the secret rather than a way of finding one on purpose: the caller
// resolves exactly once and uses that same string for both the connection and
// the redaction of any error text. A callback here would leave a second
// resolution reachable, which on the DPAPI path means a second decryption and a
// redaction pass holding a different string from the one in the DSN - and a
// redaction that holds the wrong string redacts nothing.
//
// The URL form is used rather than the key=value form because it escapes
// passwords containing ';' or '/' correctly. Never log u.String(): it carries
// the password. u.Redacted() is the only safe rendering.
func (p Profile) DSN(secret string) (Driver, string, error) {
	host, instance, port := parseServer(p.Server)
	if host == "" {
		return "", "", fmt.Errorf("profile %q: empty server", p.Name)
	}

	q := url.Values{}
	if p.Database != "" {
		q.Set("database", p.Database)
	}
	encrypt := p.Encrypt
	if encrypt == "" {
		encrypt = "true"
	}
	q.Set("encrypt", encrypt)
	q.Set("trustservercertificate", strconv.FormatBool(p.TrustServerCertificate))
	q.Set("app name", AppName)
	if p.ConnectTimeoutSec > 0 {
		q.Set("connection timeout", strconv.Itoa(p.ConnectTimeoutSec))
	}

	u := &url.URL{Scheme: "sqlserver", Host: host}
	if port != "" {
		u.Host = host + ":" + port
	}
	if instance != "" {
		u.Path = "/" + instance
	}

	driver := DriverSQLServer
	switch p.Auth {
	case AuthIntegrated:
		// No user in the URL: that is what makes go-mssqldb fall back to SSPI
		// on Windows. Away from Windows the krb5 authenticator takes over, and
		// it needs to be told where its configuration lives.
		if p.Krb5Realm != "" || p.Krb5ConfigFile != "" || p.Krb5Keytab != "" || p.Krb5CredCache != "" {
			q.Set("authenticator", "krb5")
			setIfNotEmpty(q, "krb5-realm", p.Krb5Realm)
			setIfNotEmpty(q, "krb5-configfile", p.Krb5ConfigFile)
			setIfNotEmpty(q, "krb5-keytabfile", p.Krb5Keytab)
			setIfNotEmpty(q, "krb5-credcachefile", p.Krb5CredCache)
		}
	case AuthSQL:
		if secret == "" {
			return "", "", fmt.Errorf("profile %q: no password was resolved for the SQL login", p.Name)
		}
		u.User = url.UserPassword(p.User, secret)
	case AuthEntra:
		driver = DriverAzureAD
		fedAuth := p.FedAuth
		if fedAuth == "" {
			fedAuth = defaultFedAuth
		}
		q.Set("fedauth", fedAuth)
		// Flows such as ActiveDirectoryPassword and
		// ActiveDirectoryServicePrincipal carry an identity; the rest do not.
		if p.User != "" {
			if secret != "" {
				u.User = url.UserPassword(p.User, secret)
			} else {
				u.User = url.User(p.User)
			}
		}
	default:
		return "", "", fmt.Errorf("profile %q: unsupported auth %q", p.Name, p.Auth)
	}

	u.RawQuery = q.Encode()
	return driver, u.String(), nil
}

func setIfNotEmpty(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}
