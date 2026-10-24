// Command sqlplan draws SQL Server execution plans in the browser: it reads
// .sqlplan files, or the JSON of `sqlq -plan` on stdin, writes a standalone HTML
// page and opens it with the default browser.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkg/browser"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlplan"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sqlplan:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("sqlplan", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `usage: sqlplan [-o page.html] [-theme auto|light|dark] [-node id] [-no-open] [file.sqlplan ...]
       sqlplan -png out.png [-theme light|dark] [-node id] [file.sqlplan]

Draws execution plans in the default browser. With no file, or "-", reads one plan
from stdin: showplan XML, or the JSON printed by "sqlq -plan". -png writes an image
of one plan instead, through Chrome, Chromium or Edge run headless (SQLPLAN_BROWSER
names another one), for a document that cannot run JavaScript, such as a PDF.

  sqlplan plan.sqlplan
  sqlq -profile lab -query "SELECT ..." -plan | sqlplan
  sqlplan -png lookup.png -node 5 plan.sqlplan     # operator 5 and what it reads`)
		fs.PrintDefaults()
	}
	out := fs.String("o", "", "write the page to this file (default: a new temporary file)")
	theme := fs.String("theme", "", "light, dark, or auto to follow the system (default auto, light with -png)")
	noOpen := fs.Bool("no-open", false, "write the page without opening the browser")
	png := fs.String("png", "", "write an image of the plan to this file instead of opening a page")
	node := fs.String("node", "", "draw only the operator of this NodeId and the operators under it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	names := fs.Args()
	if len(names) == 0 {
		names = []string{"-"}
	}
	var plans []sqlplan.Plan
	for _, name := range names {
		var data []byte
		var err error
		if name == "-" {
			data, err = io.ReadAll(stdin)
			name = "stdin"
		} else {
			data, err = os.ReadFile(name)
		}
		if err != nil {
			return err
		}
		xml, err := sqlplan.Decode(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		plans = append(plans, sqlplan.Plan{Name: filepath.Base(name), XML: xml})
	}

	if *png != "" {
		if len(plans) != 1 {
			return fmt.Errorf("-png draws one plan, got %d", len(plans))
		}
		if *theme == "" {
			*theme = "light"
		}
		return writePNG(plans[0], *theme, *node, *png, stdout)
	}
	if *theme == "" {
		*theme = "auto"
	}

	title := plans[0].Name
	if len(plans) > 1 {
		title = fmt.Sprintf("%d execution plans", len(plans))
	}
	page, err := sqlplan.Page(title, plans, sqlplan.Options{Theme: *theme, NodeID: *node})
	if err != nil {
		return err
	}

	path := *out
	if path == "" {
		// Left in place: the browser reads it after this process has exited
		f, err := os.CreateTemp("", "sqlplan-*.html")
		if err != nil {
			return err
		}
		path = f.Name()
		f.Close()
	}
	if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	if *noOpen {
		return nil
	}
	browser.Stdout = io.Discard
	if err := browser.OpenFile(path); err != nil {
		return fmt.Errorf("page written to %s, but the browser did not open: %w", path, err)
	}
	return nil
}

func writePNG(plan sqlplan.Plan, theme, node, out string, stdout io.Writer) error {
	browserPath, err := sqlplan.FindBrowser()
	if err != nil {
		return err
	}
	page, err := sqlplan.Page(plan.Name, []sqlplan.Plan{plan}, sqlplan.Options{Theme: theme, NodeID: node, ForImage: true})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "sqlplan-*.html")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(page)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := sqlplan.Screenshot(browserPath, f.Name(), out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, out)
	return nil
}
