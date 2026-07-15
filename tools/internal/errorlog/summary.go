package errorlog

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reProduct   = regexp.MustCompile(`(Microsoft SQL Server .*?\d+\.\d+\.\d+\.\d+)`)
	reEdition   = regexp.MustCompile(`(?i)((?:Standard|Enterprise|Developer|Express|Web)\b.*Edition.*)`)
	reRAM       = regexp.MustCompile(`Detected (\d+) MB of RAM`)
	reAuth      = regexp.MustCompile(`(?i)Authentication mode is (\w+)`)
	reCollation = regexp.MustCompile(`(?i)Default collation:\s*(\S+)`)
	reUTC       = regexp.MustCompile(`(?i)UTC adjustment:\s*([\d:]+)`)
	rePort      = regexp.MustCompile(`(?i)listening on \[ .* (\d{2,5})\] accept sockets`)
	reIFI       = regexp.MustCompile(`(?i)Instant File Initialization[\s\x{00a0}]*:[\s\x{00a0}]*(\S+?)[.\s]`)
	reListener  = regexp.MustCompile(`(?i)listening on virtual network name '([^']+)'`)
	reSvc       = regexp.MustCompile(`(?i)service account is '([^']+)'`)
	reLogPath   = regexp.MustCompile(`(?i)Logging SQL Server messages in file '([^']+)'`)
	reSPN       = regexp.MustCompile(`(?i)(Service Principal Name.*)$`)
)

// InstanceSummary holds instance-level facts extracted from the boot region
// of an ERRORLOG plus the overall time window covered by the parsed entries.
// Fields not present in the boot region are left as zero values.
type InstanceSummary struct {
	Product     string
	Edition     string
	RAMMB       int
	AuthMode    string
	Collation   string
	UTCAdjust   string
	IFI         string
	ServiceAcct string
	LogPath     string
	SPN         string
	TCPPorts    []string
	AGListeners []string

	StartTime time.Time
	FirstTime time.Time
	LastTime  time.Time
}

// Summarize extracts instance facts from the boot sequence and the log's time
// window. Fields not present in the boot region are left as zero values.
func Summarize(entries []Entry) InstanceSummary {
	var s InstanceSummary
	boot := BootText(entries, 400)
	first := firstMatch(reProduct, boot)
	s.Product = first
	s.Edition = firstMatch(reEdition, boot)
	if m := reRAM.FindStringSubmatch(boot); m != nil {
		s.RAMMB, _ = strconv.Atoi(m[1])
	}
	s.AuthMode = firstMatch(reAuth, boot)
	s.Collation = firstMatch(reCollation, boot)
	s.UTCAdjust = firstMatch(reUTC, boot)
	s.IFI = firstMatch(reIFI, boot)
	s.ServiceAcct = firstMatch(reSvc, boot)
	s.LogPath = firstMatch(reLogPath, boot)
	s.SPN = firstMatch(reSPN, boot)
	s.TCPPorts = uniqueMatches(rePort, boot)
	s.AGListeners = uniqueMatches(reListener, boot)

	for _, e := range entries {
		if e.Time.IsZero() {
			continue
		}
		if s.FirstTime.IsZero() {
			s.FirstTime = e.Time
			s.StartTime = e.Time
		}
		s.LastTime = e.Time
	}
	return s
}

// BootText joins the raw lines of the first entries (the boot region) so that
// facts and boot checks can be matched against a single string.
func BootText(entries []Entry, maxLines int) string {
	var b strings.Builder
	n := 0
	for _, e := range entries {
		for _, l := range e.Lines {
			b.WriteString(l)
			b.WriteByte('\n')
			n++
			if n >= maxLines {
				return b.String()
			}
		}
	}
	return b.String()
}

func firstMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func uniqueMatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// window formats the FirstTime/LastTime range for renderers.
func (s InstanceSummary) window() string {
	if s.FirstTime.IsZero() {
		return ""
	}
	const f = "2006-01-02 15:04"
	return s.FirstTime.Format(f) + " → " + s.LastTime.Format(f)
}
