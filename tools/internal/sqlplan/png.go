package sqlplan

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
)

// FindBrowser returns a Chromium-based browser able to run headless: the
// SQLPLAN_BROWSER environment variable if set, else Chrome, Chromium or Edge.
func FindBrowser() (string, error) {
	if b := os.Getenv("SQLPLAN_BROWSER"); b != "" {
		return b, nil
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "msedge", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	var paths []string
	switch runtime.GOOS {
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				paths = append(paths,
					filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
	case "darwin":
		paths = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no Chrome, Chromium or Edge found: set SQLPLAN_BROWSER to its executable")
}

var titleRe = regexp.MustCompile(`<title>(size:(\d+)x(\d+)|error:[^<]*)</title>`)

// Screenshot renders a page written by Page with ForImage set into a PNG cut to
// the plan, at twice the CSS resolution so it stays sharp in a PDF. The browser
// runs twice: once to let the page measure itself, once to capture at that size.
func Screenshot(browser, pagePath, pngPath string) error {
	abs, err := filepath.Abs(pagePath)
	if err != nil {
		return err
	}
	pageURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	if runtime.GOOS == "windows" {
		pageURL = "file:///" + filepath.ToSlash(abs)
	}
	profile, err := os.MkdirTemp("", "sqlplan-browser-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	common := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir=" + profile, "--virtual-time-budget=5000"}

	dom, err := exec.Command(browser, append(common, "--dump-dom", pageURL)...).Output()
	if err != nil {
		return fmt.Errorf("%s --dump-dom: %w", filepath.Base(browser), err)
	}
	m := titleRe.FindSubmatch(dom)
	if m == nil {
		return errors.New("the page did not report its size: was it written with ForImage?")
	}
	if m[2] == nil {
		return fmt.Errorf("the plan could not be drawn: %s", m[1][len("error:"):])
	}
	w, _ := strconv.Atoi(string(m[2]))
	h, _ := strconv.Atoi(string(m[3]))

	abspng, err := filepath.Abs(pngPath)
	if err != nil {
		return err
	}
	os.Remove(abspng)
	out, err := exec.Command(browser, append(common, "--hide-scrollbars", "--force-device-scale-factor=2",
		fmt.Sprintf("--window-size=%d,%d", w, h), "--screenshot="+abspng, pageURL)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --screenshot: %w: %s", filepath.Base(browser), err, out)
	}
	if _, err := os.Stat(abspng); err != nil {
		return fmt.Errorf("the browser wrote no image: %s", out)
	}
	return nil
}
