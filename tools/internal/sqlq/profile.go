package sqlq

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AuthKind is how sqlq authenticates to the instance.
type AuthKind string

const (
	// AuthIntegrated means SSPI on Windows and Kerberos elsewhere. The two are
	// not interchangeable: see the Krb5 fields.
	AuthIntegrated AuthKind = "integrated"
	AuthSQL        AuthKind = "sql"
	AuthEntra      AuthKind = "entra"
)

// Access modes. A profile with no mode is read-only: the safe default has to be
// the one you get by forgetting.
const (
	ModeReadOnly  = "readonly"
	ModeReadWrite = "readwrite"
)

// Profile describes one instance sqlq may connect to.
type Profile struct {
	Name     string   `json:"-"`
	Server   string   `json:"server"`
	Database string   `json:"database"`
	Auth     AuthKind `json:"auth"`
	Mode     string   `json:"mode"`

	// SQL authentication. The password itself is never stored here:
	// PasswordEnv names the environment variable that carries it.
	User        string `json:"user"`
	PasswordEnv string `json:"passwordEnv"`

	// InlinePassword exists only so that a profile carrying a password by
	// mistake is refused loudly instead of silently ignored.
	InlinePassword string `json:"password"`

	// Entra ID. Defaults to ActiveDirectoryDefault when empty.
	FedAuth string `json:"fedauth"`

	// Transport. Encrypt defaults to "true"; "strict" and "disable" are passed
	// through to the driver untouched.
	Encrypt                string `json:"encrypt"`
	TrustServerCertificate bool   `json:"trustServerCertificate"`
	ConnectTimeoutSec      int    `json:"connectTimeoutSec"`

	// Kerberos, for integrated authentication away from Windows.
	Krb5Realm      string `json:"krb5Realm"`
	Krb5ConfigFile string `json:"krb5ConfigFile"`
	Krb5Keytab     string `json:"krb5KeytabFile"`
	Krb5CredCache  string `json:"krb5CredCacheFile"`
}

// Profiles is the parsed profile file, keyed by profile name.
type Profiles map[string]Profile

// ReadOnly reports whether writing statements must be refused for this profile.
func (p Profile) ReadOnly() bool { return p.Mode != ModeReadWrite }

// Validate checks a profile is internally coherent, before any connection is
// attempted.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.Server) == "" {
		return fmt.Errorf(`"server" is required`)
	}
	switch p.Auth {
	case AuthIntegrated, AuthEntra:
	case AuthSQL:
		if strings.TrimSpace(p.User) == "" {
			return fmt.Errorf(`"user" is required for auth %q`, AuthSQL)
		}
		if strings.TrimSpace(p.PasswordEnv) == "" {
			return fmt.Errorf(`"passwordEnv" is required for auth %q`, AuthSQL)
		}
	case "":
		return fmt.Errorf(`"auth" is required (one of %q, %q, %q)`,
			AuthIntegrated, AuthSQL, AuthEntra)
	default:
		return fmt.Errorf(`unknown "auth" %q (want %q, %q or %q)`,
			p.Auth, AuthIntegrated, AuthSQL, AuthEntra)
	}
	switch p.Mode {
	case "", ModeReadOnly, ModeReadWrite:
	default:
		return fmt.Errorf(`unknown "mode" %q (want %q or %q)`,
			p.Mode, ModeReadOnly, ModeReadWrite)
	}
	return nil
}

// DefaultProfilePath is where sqlq looks when neither -profiles nor
// MSSQL_PROFILES says otherwise.
func DefaultProfilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "mssql-profiles.json"
	}
	return filepath.Join(home, ".config", "db-ai-toolkit", "mssql-profiles.json")
}

// LoadProfiles reads and validates the profile file at path.
func LoadProfiles(path string) (Profiles, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading profiles %s: %w", path, err)
	}
	var profiles Profiles
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, fmt.Errorf("parsing profiles %s: %w", path, err)
	}
	for name, p := range profiles {
		if p.InlinePassword != "" {
			return nil, fmt.Errorf(
				`profile %q carries an inline "password": store it in an environment variable and name that variable in "passwordEnv"`,
				name)
		}
		if p.Mode == "" {
			p.Mode = ModeReadOnly
		}
		p.Name = name
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		profiles[name] = p
	}
	return profiles, nil
}

// Get returns one profile by name, naming the alternatives when it misses.
func (ps Profiles) Get(name string) (Profile, error) {
	p, ok := ps[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q (available: %s)",
			name, strings.Join(ps.Names(), ", "))
	}
	p.Name = name
	return p, nil
}

// Names lists the profile names in a stable order.
func (ps Profiles) Names() []string {
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
