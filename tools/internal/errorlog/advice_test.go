package errorlog

import (
	"strings"
	"testing"
)

func TestAdviceCountAndBootCheck(t *testing.T) {
	a, err := LoadEmbeddedAdvisories()
	if err != nil {
		t.Fatalf("LoadEmbeddedAdvisories: %v", err)
	}
	boot := "Database Instant File Initialization: désactivé. blah"
	adv := a.Evaluate(map[string]int{"backup": 105575}, boot)

	var haveBackup, haveIFI bool
	for _, x := range adv {
		if x.ID == "backup" {
			haveBackup = true
			if !strings.Contains(x.Message, "105575") {
				t.Errorf("count not interpolated: %q", x.Message)
			}
			if !strings.Contains(x.Message, "3226") {
				t.Errorf("missing TF 3226: %q", x.Message)
			}
		}
		if x.ID == "ifi-off" {
			haveIFI = true
		}
	}
	if !haveBackup || !haveIFI {
		t.Fatalf("advisories missing: backup=%v ifi=%v", haveBackup, haveIFI)
	}
}

func TestAdviceBelowThresholdSilent(t *testing.T) {
	a, _ := LoadEmbeddedAdvisories()
	if adv := a.Evaluate(map[string]int{"backup": 10}, ""); len(adv) != 0 {
		t.Fatalf("expected no advisories, got %v", adv)
	}
}
