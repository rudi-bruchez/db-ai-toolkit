package errorlog

import (
	"regexp"
	"strings"
	"time"
)

var (
	reHex   = regexp.MustCompile(`0x[0-9A-Fa-f]+`)
	reIP    = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reGUID  = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)
	reDigit = regexp.MustCompile(`\d+`)
)

// Event is one aggregated occurrence of a normalized ERRORLOG message: a
// count of how many kept entries shared the signature and the time span
// they cover.
type Event struct {
	First, Last time.Time
	Count       int
	Text        string
}

// signature normalizes an entry so that repeated events with varying numbers,
// hex, GUIDs and IPs collapse together. Source keeps its shape minus the spid
// number (spid51 -> spid). GUIDs are replaced first (they contain hex and
// digits), and every placeholder is digit-free so the final digit pass can
// never mangle a placeholder inserted by an earlier pass.
func signature(e Entry) string {
	s := e.Message()
	s = reGUID.ReplaceAllString(s, "guid")
	s = reHex.ReplaceAllString(s, "hexval")
	s = reIP.ReplaceAllString(s, "ipaddr")
	s = reDigit.ReplaceAllString(s, "#")
	src := reDigit.ReplaceAllString(e.Source, "")
	return src + "|" + s
}

// Aggregate collapses kept entries that share a signature into a single Event
// with a count and time span; unique entries keep their full text. Order
// follows first occurrence.
func Aggregate(kept []Entry) []Event {
	type acc struct {
		first   Entry
		count   int
		firstTs time.Time
		lastTs  time.Time
	}
	groups := map[string]*acc{}
	var order []string
	for _, e := range kept {
		key := signature(e)
		g, ok := groups[key]
		if !ok {
			g = &acc{first: e, firstTs: e.Time, lastTs: e.Time}
			groups[key] = g
			order = append(order, key)
		}
		g.count++
		if !e.Time.IsZero() {
			if g.firstTs.IsZero() || e.Time.Before(g.firstTs) {
				g.firstTs = e.Time
			}
			if e.Time.After(g.lastTs) {
				g.lastTs = e.Time
			}
		}
	}
	events := make([]Event, 0, len(order))
	for _, key := range order {
		g := groups[key]
		text := g.first.Text()
		if g.count > 1 {
			text = firstLine(g.first.Text())
		}
		events = append(events, Event{First: g.firstTs, Last: g.lastTs, Count: g.count, Text: text})
	}
	return events
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
