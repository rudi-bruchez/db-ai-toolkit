package sqlq

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// SecretResolver produces the password a profile will authenticate with.
//
// It is a function rather than a lookup so that the two ways of naming a secret
// - an environment variable and the DPAPI credential store - stay behind one
// call, and so that tests can inject a synthetic one.
type SecretResolver func(Profile) (string, error)

// EnvResolver resolves profiles that name an environment variable, and nothing
// else. Passing os.Getenv gives the behaviour sqlq had before DPAPI existed.
func EnvResolver(getenv func(string) string) SecretResolver {
	r := &Resolver{Getenv: getenv}
	return r.Resolve
}

// BoundTo records the destination a credential was imported for. Neither field
// is secret; together they are what stops a password being sent somewhere it
// does not belong.
type BoundTo struct {
	Server string `json:"server"`
	Login  string `json:"login"`
}

// Credential is one entry of the credential store: an encrypted blob and the
// destination it belongs to.
type Credential struct {
	Blob    string  `json:"blob"`
	BoundTo BoundTo `json:"boundTo"`
}

// CredentialStore is the file Import-RegisteredServerCredentials.ps1 writes.
// Only the resolver reads it, and only for the id of the profile in hand.
type CredentialStore struct {
	GeneratedAt string                `json:"generatedAt"`
	Source      *SourceStamp          `json:"source"`
	Credentials map[string]Credential `json:"credentials"`
}

// DefaultCredentialPath is where the import script writes the credential store.
// LOCALAPPDATA, never APPDATA: the latter roams, and a roaming profile would
// put these blobs on a network share.
func DefaultCredentialPath() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "db-ai-toolkit", "credentials.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "credentials.json"
	}
	return filepath.Join(home, ".local", "share", "db-ai-toolkit", "credentials.json")
}

// Resolver turns a profile into a password.
//
// Unprotect and Warn are injected so that the DPAPI path can be driven on any
// platform in tests, and so that warnings can be captured instead of printed.
type Resolver struct {
	Getenv          func(string) string
	CredentialsPath string
	Unprotect       func([]byte) ([]byte, error)
	Warn            func(string)

	store  *CredentialStore
	loaded bool
}

// NewResolver builds the resolver sqlq uses in production: environment
// variables, plus the DPAPI credential store, with warnings on stderr.
func NewResolver(getenv func(string) string, credentialsPath string, warn func(string)) *Resolver {
	return &Resolver{
		Getenv:          getenv,
		CredentialsPath: credentialsPath,
		Unprotect:       dpapiUnprotect,
		Warn:            warn,
	}
}

func (r *Resolver) warn(format string, args ...any) {
	if r.Warn != nil {
		r.Warn(fmt.Sprintf(format, args...))
	}
}

// Resolve returns the profile's password, or "" when the profile carries no
// secret at all (integrated authentication, and the Entra flows that do not
// name an identity).
//
// Every caller must use this one result for both the connection string and the
// error redaction. Two independent lookups would mean two DPAPI decryptions and
// two chances for them to disagree - and a redaction pass holding a different
// string from the one in the DSN redacts nothing.
func (r *Resolver) Resolve(p Profile) (string, error) {
	// The auth mode decides, not the presence of a field. An integrated profile
	// needs no secret, and an Entra flow that names no identity carries none
	// either. Resolving regardless would make a leftover "passwordEnv" on a
	// working integrated profile fail the connection - it used to be harmless,
	// and nothing about integrated auth changed.
	if !p.usesSecret() {
		return "", nil
	}
	switch {
	case strings.TrimSpace(p.PasswordEnv) != "":
		if r.Getenv == nil {
			return "", fmt.Errorf("profile %q: no environment available to read %s from",
				p.Name, p.PasswordEnv)
		}
		secret := r.Getenv(p.PasswordEnv)
		if secret == "" {
			return "", fmt.Errorf(
				"profile %q: environment variable %s is empty or unset; it must hold the password",
				p.Name, p.PasswordEnv)
		}
		return secret, nil

	case strings.TrimSpace(p.PasswordDpapi) != "":
		return r.resolveDpapi(p)

	default:
		return "", nil
	}
}

func (r *Resolver) resolveDpapi(p Profile) (string, error) {
	store, err := r.loadStore()
	if err != nil {
		return "", err
	}

	entry, ok := store.Credentials[p.PasswordDpapi]
	if !ok {
		return "", fmt.Errorf(
			"profile %q: no credential %q in %s. Run Import-RegisteredServerCredentials.ps1.",
			p.Name, p.PasswordDpapi, r.credentialsPath())
	}

	// The refusal, before any warning. A registration whose server or login was
	// edited in SSMS keeps its id, so the profile is regenerated pointing
	// somewhere new while the stored credential still belongs to the old
	// destination. Sending it anyway hands one account's password to a third
	// party, and the 18456 that comes back looks exactly like a stale password.
	if err := checkBinding(p, entry.BoundTo); err != nil {
		return "", err
	}

	r.warnIfStale(p, store)

	blob, err := base64.StdEncoding.DecodeString(entry.Blob)
	if err != nil {
		return "", fmt.Errorf("profile %q: credential %q is not valid base64: %w",
			p.Name, p.PasswordDpapi, err)
	}

	unprotect := r.Unprotect
	if unprotect == nil {
		unprotect = dpapiUnprotect
	}
	clear, err := unprotect(blob)
	if err != nil {
		return "", fmt.Errorf(`profile %q: cannot decrypt credential %q (%w).
The blob was encrypted under a different Windows account or on a different machine, and this
one does not hold the key. Re-running the import cannot fix it: the import copies the blob
unchanged, so it would copy the same undecryptable bytes. Open SSMS on this machine, under
this account, re-enter the password and save - SSMS re-encrypts it with the local key - then
run Import-RegisteredServerCredentials.ps1.`, p.Name, p.PasswordDpapi, err)
	}
	defer zero(clear)

	secret, err := decodeUTF16LE(clear)
	if err != nil {
		return "", fmt.Errorf("profile %q: credential %q did not decrypt to text: %w",
			p.Name, p.PasswordDpapi, err)
	}
	if secret == "" {
		return "", fmt.Errorf("profile %q: credential %q decrypted to an empty password",
			p.Name, p.PasswordDpapi)
	}
	return secret, nil
}

// checkBinding refuses a credential whose profile now points elsewhere.
func checkBinding(p Profile, bound BoundTo) error {
	// A missing binding is refused, not waved through. There is no store old
	// enough to lack one - the importer has written boundTo since the first
	// version that wrote a store at all - so an entry without one was written
	// by something else, and deleting two lines of JSON is otherwise all it
	// takes to turn the check below off.
	if bound.Server == "" {
		return fmt.Errorf(
			"credential %q carries no destination, so there is nothing to check profile %q against. "+
				"Re-run Import-RegisteredServerCredentials.ps1, which records one for every credential it writes.",
			p.PasswordDpapi, p.Name)
	}
	if strings.EqualFold(bound.Server, p.Server) && strings.EqualFold(bound.Login, p.User) {
		return nil
	}
	return fmt.Errorf(
		"credential %q was imported for %s/%s, but profile %q now points at %s/%s. "+
			"Re-run Import-RegisteredServerCredentials.ps1.",
		p.PasswordDpapi, bound.Server, bound.Login, p.Name, p.Server, p.User)
}

// warnIfStale reports generations that do not line up. None of these refuses:
// a password that has not changed is still perfectly valid, and denying access
// over a file hash would be out of proportion. The dangerous case - a
// credential pointed at the wrong destination - is already refused above.
func (r *Resolver) warnIfStale(p Profile, store *CredentialStore) {
	// 1. Do the profile and the credential come from the same generation? The
	//    profile's own stamp is what matters: it is the one about to be used.
	if p.Source != nil && store.Source != nil &&
		p.Source.SHA256 != "" && store.Source.SHA256 != "" &&
		p.Source.SHA256 != store.Source.SHA256 {
		r.warn("profile %q and its credential come from two different states of %s; "+
			"re-run Export-RegisteredServers.ps1 and Import-RegisteredServerCredentials.ps1",
			p.Name, store.Source.Path)
	}

	// 2. Has the source moved on since the credentials were imported?
	if store.Source == nil || store.Source.Path == "" {
		return
	}
	info, err := os.Stat(store.Source.Path)
	if err != nil {
		// Losing access to your servers because SSMS was uninstalled would be
		// absurd: the credentials already imported stay usable.
		r.warn("the registered-servers file %s is gone; credentials already imported still work",
			store.Source.Path)
		return
	}
	// Size and timestamp first: when they match there is nothing to hash, and
	// this runs on every single query.
	if info.Size() == store.Source.SizeBytes && sameInstant(info, store.Source.LastWriteTime) {
		return
	}
	sum, err := hashFile(store.Source.Path)
	if err != nil || sum == store.Source.SHA256 {
		return
	}
	r.warn("%s has changed since the credentials were imported; "+
		"re-run Import-RegisteredServerCredentials.ps1 if a password was updated in SSMS",
		store.Source.Path)
}

func (r *Resolver) credentialsPath() string {
	if r.CredentialsPath != "" {
		return r.CredentialsPath
	}
	return DefaultCredentialPath()
}

func (r *Resolver) loadStore() (*CredentialStore, error) {
	if r.loaded {
		if r.store == nil {
			return nil, fmt.Errorf("credential store %s is unavailable", r.credentialsPath())
		}
		return r.store, nil
	}
	r.loaded = true

	path := r.credentialsPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(
			"reading the credential store %s: %w. Run Import-RegisteredServerCredentials.ps1 yourself; "+
				"it is not for an agent to run.", path, err)
	}
	var store CredentialStore
	if err := json.Unmarshal(raw, &store); err != nil {
		return nil, fmt.Errorf("parsing the credential store %s: %w", path, err)
	}
	r.store = &store
	return r.store, nil
}

// decodeUTF16LE turns the bytes DPAPI returns into a string. SSMS stores the
// password as UTF-16, which is what .NET hands to ProtectedData.
func decodeUTF16LE(b []byte) (string, error) {
	if len(b)%2 != 0 {
		return "", fmt.Errorf("odd length %d, not UTF-16", len(b))
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(units)), nil
}

// zero overwrites a buffer that held plaintext. The string made from it cannot
// be wiped - Go strings are immutable and garbage-collected, which is the cost
// this design accepts - but the buffer can be, and is.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// sameInstant compares a file's modification time with the RFC 3339 timestamp
// recorded at import. A stamp that will not parse counts as different, so the
// hash decides rather than a silent pass.
func sameInstant(info os.FileInfo, recorded string) bool {
	when, err := time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return false
	}
	return info.ModTime().Equal(when)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
