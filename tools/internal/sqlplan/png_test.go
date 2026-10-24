package sqlplan

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real plan, drawn by a real browser: skipped where none is installed
func TestScreenshot(t *testing.T) {
	browser, err := FindBrowser()
	if err != nil {
		t.Skip(err)
	}
	const realPlan = `<?xml version="1.0" encoding="utf-16"?><ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.6" Build="16.0.4265.3"><BatchSequence><Batch><Statements><StmtSimple StatementText="SELECT * FROM T" StatementId="1" StatementCompId="1" StatementType="SELECT" StatementSubTreeCost="0.0032831"><QueryPlan><RelOp NodeId="0" PhysicalOp="Table Scan" LogicalOp="Table Scan" EstimateRows="1" EstimatedTotalSubtreeCost="0.0032831"><OutputList/><TableScan Ordered="0"><Object Database="[D]" Schema="[dbo]" Table="[T]"/></TableScan></RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`
	dir := t.TempDir()
	for _, c := range []struct {
		node, wantErr string
	}{{"", ""}, {"0", ""}, {"7", "No operator with NodeId 7"}} {
		page, err := Page("t", []Plan{{Name: "p", XML: realPlan}}, Options{Theme: "light", NodeID: c.node, ForImage: true})
		if err != nil {
			t.Fatal(err)
		}
		html := filepath.Join(dir, "p.html")
		os.WriteFile(html, []byte(page), 0o644)
		out := filepath.Join(dir, "p.png")
		err = Screenshot(browser, html, out)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("node %s: got %v, want %q", c.node, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("node %q: %v", c.node, err)
		}
		b, _ := os.ReadFile(out)
		img, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		// One operator: a small image, not the browser's default window
		if img.Width < 100 || img.Width > 800 || img.Height < 100 || img.Height > 600 {
			t.Errorf("node %q: image %dx%d, not cut to the plan", c.node, img.Width, img.Height)
		}
	}
}
