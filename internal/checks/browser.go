//go:build !lite

package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func init() {
	registerType(TypeInfo{
		Type: "real-browser", Category: "web", Needs: "chromium",
		Fields: []Field{
			{Name: "url", Kind: "text", Required: true, URL: true},
			{Name: "accepted_status", Kind: "text", Default: "200-399"},
			{Name: "wait_selector", Kind: "text", Advanced: true},
			fInsecure,
		},
		target: targetURL("url"),
		validate: func(p params) error {
			_, _, err := statusRange(p["accepted_status"])
			return err
		},
	}, checkBrowser)
}

// findChromium is replaced in tests.
var findChromium = func() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "headless-shell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			path := filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe")
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return ""
}

// chromiumNoSandbox says whether Chromium runs without its sandbox, the one
// barrier between a hostile page and the probe host. Only on request
// (UF_BROWSER_NO_SANDBOX=1, e.g. in a container with capabilities dropped)
// or as root, where Chromium refuses to start sandboxed.
func chromiumNoSandbox() bool {
	return os.Getenv("UF_BROWSER_NO_SANDBOX") == "1" || os.Geteuid() == 0
}

// checkBrowser loads the page in headless Chromium and reads the status of the
// main document. Chromium dials by itself: behind an upstream proxy it would
// report on the wrong network, so that is unavailable rather than a lie.
func checkBrowser(ctx context.Context, s *Spec, env *Env) Result {
	if env.Via != nil {
		return fail("unavailable", errors.New("a real browser cannot go through this probe's upstream proxy"))
	}
	path := findChromium()
	if path == "" {
		return fail("unavailable", errors.New("no Chromium on this probe, install chromium or use the image that has it"))
	}
	p := paramsOf(s)
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path), chromedp.Flag("disable-dev-shm-usage", true))
	noSandbox := chromiumNoSandbox()
	if noSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	if p.bool("insecure") {
		opts = append(opts, chromedp.IgnoreCertErrors)
	}
	actx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	bctx, cancelBrowser := chromedp.NewContext(actx)
	defer cancelBrowser()

	if err := chromedp.Do(bctx); err != nil {
		if ctx.Err() != nil {
			return fail("timeout", errors.New("Chromium did not start in time"))
		}
		if !noSandbox {
			// Containers with capabilities dropped cannot give Chromium its sandbox.
			return fail("unavailable", fmt.Errorf("Chromium does not start on this probe with its sandbox "+
				"(set UF_BROWSER_NO_SANDBOX=1 where it cannot have one): %w", err))
		}
		return fail("unavailable", fmt.Errorf("Chromium does not start on this probe: %w", err))
	}
	// An error status without a body fails the navigation itself, so the code
	// has to be picked up from the event, not from the navigation's result.
	var docStatus atomic.Int64
	if err := chromedp.Listen(bctx, func(ev any) {
		if e, ok := ev.(*network.EventResponseReceived); ok && e.Type == network.ResourceTypeDocument {
			docStatus.Store(e.Response.Status)
		}
	}); err != nil {
		return fail("unavailable", fmt.Errorf("Chromium does not start on this probe: %w", err))
	}
	start := time.Now()
	_, err := chromedp.RunResponse(bctx, chromedp.Navigate(p["url"]))
	r := Result{LatencyMS: ms(time.Since(start))}
	code := int(docStatus.Load())
	if err != nil && !(code != 0 && strings.Contains(err.Error(), "ERR_HTTP_RESPONSE_CODE_FAILURE")) {
		r.Status, r.Reason, r.Error = Down, browserReason(ctx, err), err.Error()
		return r
	}
	if code == 0 {
		r.Status, r.Reason, r.Error = Down, "bad_response", "the browser saw no response for the page"
		return r
	}
	r.Info = map[string]string{"status_code": strconv.Itoa(code)}
	lo, hi, _ := statusRange(p["accepted_status"])
	if code < lo || code > hi {
		r.Status, r.Reason, r.Error = Down, "http_status", fmt.Sprintf("HTTP %d", code)
		return r
	}
	if sel := p["wait_selector"]; sel != "" {
		if err := chromedp.Do(bctx, chromedp.WaitVisible(chromedp.CSS(sel))); err != nil {
			if ctx.Err() != nil {
				r.Status, r.Reason, r.Error = Down, "not_found", "page loaded, but "+sel+" did not appear in time"
			} else {
				r.Status, r.Reason, r.Error = Down, "error", err.Error()
			}
		}
	}
	return r
}

func browserReason(ctx context.Context, err error) string {
	s := err.Error()
	switch {
	case ctx.Err() != nil || strings.Contains(s, "ERR_TIMED_OUT") || strings.Contains(s, "ERR_CONNECTION_TIMED_OUT"):
		return "timeout"
	case strings.Contains(s, "ERR_NAME_NOT_RESOLVED"):
		return "dns"
	case strings.Contains(s, "ERR_CONNECTION_REFUSED"):
		return "refused"
	case strings.Contains(s, "ERR_CERT") || strings.Contains(s, "ERR_SSL"):
		return "tls"
	case strings.Contains(s, "ERR_CONNECTION_RESET") || strings.Contains(s, "ERR_CONNECTION_CLOSED") || strings.Contains(s, "ERR_EMPTY_RESPONSE"):
		return "reset"
	case strings.Contains(s, "ERR_ADDRESS_UNREACHABLE") || strings.Contains(s, "ERR_NETWORK_UNREACHABLE"):
		return "unreachable"
	}
	return "error"
}
