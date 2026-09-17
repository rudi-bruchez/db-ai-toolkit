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

// SourceStamp identifies the exact state of the file a generated profile was
// built from. The hash is the part that matters: comparing paths cannot tell
// "the file changed" from "a different file was picked", and neither can a
// timestamp.
type SourceStamp struct {
	Path          string `json:"path"`
	LastWriteTime string `json:"lastWriteTime"`
	SizeBytes     int64  `json:"sizeBytes"`
	SHA256        string `json:"sha256"`
}

// Profile describes one instance sqlq may connect to.
type Profile struct {
	Name     string   `json:"-"`
	Server   string   `json:"server"`
	Database string   `json:"database"`
	Auth     AuthKind `json:"auth"`
	Mode     string   `json:"mode"`

	// SQL authentication. The password itself is never stored here. Exactly one
	// of these two names where to find it: PasswordEnv an environment variable,
	// PasswordDpapi a key into the DPAPI-encrypted credential store written by
	// registered-servers.
	User          string `json:"user"`
	PasswordEnv   string `json:"passwordEnv"`
	PasswordDpapi string `json:"passwordDpapi"`

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

	// Provenance, for profiles a tool generated rather than a human wrote.
	// ManagedBy says who owns the entry: the generator replaces and deletes its
	// own entries and never touches anyone else's. Source says which state of
	// which file it came from, which is a different question and needs a
	// different field - one name for both breaks the ownership test as soon as
	// a provenance object lands where a string was expected.
	//
	// Both must survive a round trip through json.Unmarshal. Dropping them here
	// would make the next rewrite strip the marker from every generated entry,
	// turning them all into hand-written ones at once.
	ManagedBy   string       `json:"managedBy,omitempty"`
	Source      *SourceStamp `json:"source,omitempty"`
	Environment string       `json:"environment,omitempty"`

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
	case AuthIntegrated:
		// Integrated authentication uses no password at all. A profile naming
		// one is a contradiction, not a harmless leftover: somebody believed a
		// credential was in play here, and silently ignoring the field would
		// leave them believing it.
		if p.namesSecret() {
			return fmt.Errorf(
				`auth %q uses no password; remove "passwordEnv"/"passwordDpapi"`, AuthIntegrated)
		}
	case AuthEntra:
		// Only the flows that carry an identity need a secret, and those are
		// the ones that name a user.
		if strings.TrimSpace(p.User) == "" {
			if p.namesSecret() {
				return fmt.Errorf(
					`auth %q names a password but no "user"; the flows that carry a secret `+
						`(ActiveDirectoryPassword, ActiveDirectoryServicePrincipal) need both`,
					AuthEntra)
			}
		} else if err := p.validateSecretSource(false); err != nil {
			return err
		}
	case AuthSQL:
		if strings.TrimSpace(p.User) == "" {
			return fmt.Errorf(`"user" is required for auth %q`, AuthSQL)
		}
		if err := p.validateSecretSource(true); err != nil {
			return err
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

// namesSecret reports whether the profile points at a password by either name.
func (p Profile) namesSecret() bool {
	return strings.TrimSpace(p.PasswordEnv) != "" || strings.TrimSpace(p.PasswordDpapi) != ""
}

// usesSecret reports whether this authentication mode actually sends a
// password. It is the auth mode that decides, never the presence of a field:
// resolving a secret a mode does not use turns a stray key into a failed
// connection, and on the DPAPI path into a pointless decryption.
func (p Profile) usesSecret() bool {
	switch p.Auth {
	case AuthSQL:
		return true
	case AuthEntra:
		return strings.TrimSpace(p.User) != "" && p.namesSecret()
	default:
		return false
	}
}

// validateSecretSource enforces exactly one way of naming the password, or at
// most one when required is false.
//
// Note what is deliberately NOT checked here: whether this platform can
// actually use passwordDpapi. LoadProfiles validates every profile in the file
// before any of them is selected, so refusing DPAPI at validation time would
// make one Windows-only entry render the whole file unusable on Linux - even
// -list-profiles would fail instead of listing. The refusal belongs at the
// moment the chosen profile's secret is resolved.
func (p Profile) validateSecretSource(required bool) error {
	env := strings.TrimSpace(p.PasswordEnv) != ""
	dpapi := strings.TrimSpace(p.PasswordDpapi) != ""
	switch {
	case env && dpapi:
		return fmt.Errorf(`"passwordEnv" and "passwordDpapi" are mutually exclusive; keep one`)
	case !env && !dpapi && required:
		return fmt.Errorf(`auth %q needs either "passwordEnv" or "passwordDpapi"`, p.Auth)
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

// Get returns one profile by name.
//
// A miss reports how many profiles exist and the nearest few names, rather than
// listing them all. The full list is the estate map - host names, database
// names, the group taxonomy an organisation uses internally - and printing it
// on a typo publishes the lot into an agent transcript. -list-profiles remains
// the way to see what is available, and it no longer carries the servers.
func (ps Profiles) Get(name string) (Profile, error) {
	p, ok := ps[name]
	if !ok {
		hint := ""
		if near := ps.closest(name, 3); len(near) > 0 {
			hint = fmt.Sprintf("; did you mean %s", strings.Join(near, ", "))
		}
		return Profile{}, fmt.Errorf("unknown profile %q (%d defined%s)", name, len(ps), hint)
	}
	p.Name = name
	return p, nil
}

// closest returns up to n profile names nearest to want, by edit distance,
// keeping only names that are actually close enough to be a typo.
func (ps Profiles) closest(want string, n int) []string {
	type scored struct {
		name string
		d    int
	}
	limit := len(want)/2 + 2
	var candidates []scored
	for _, name := range ps.Names() {
		d := editDistance(strings.ToLower(want), strings.ToLower(name))
		if d <= limit {
			candidates = append(candidates, scored{name, d})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].d != candidates[j].d {
			return candidates[i].d < candidates[j].d
		}
		return candidates[i].name < candidates[j].name
	})
	out := make([]string, 0, n)
	for _, c := range candidates {
		if len(out) == n {
			break
		}
		out = append(out, c.name)
	}
	return out
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
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
