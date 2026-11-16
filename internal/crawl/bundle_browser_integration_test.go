//go:build browserintegration

package crawl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/winezer0/browcrawler/internal/storage"
)

func TestBrowserBundleChunkFetchUsesSessionCookiesAndRejectsCrossOriginRedirect(t *testing.T) {
	chrome := browserIntegrationBinary()
	if chrome == "" {
		t.Skip("set BROWCRAWLER_CHROME_BIN or install Chrome/Chromium to run browser integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var pageOrigin string
	var crossOriginCookie atomic.Bool
	crossOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", pageOrigin)
		w.Header().Set("Content-Type", "application/javascript")
		if r.URL.Path == "/target.js" {
			if r.Header.Get("Cookie") != "" {
				crossOriginCookie.Store(true)
			}
			_, _ = io.WriteString(w, `fetch("/api/should-not-be-parsed");`)
			return
		}
		http.NotFound(w, r)
	}))
	defer crossOrigin.Close()

	var sessionCookieSeen atomic.Bool
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "browser-session", Path: "/", HttpOnly: true})
			_, _ = io.WriteString(w, `<!doctype html><title>fixture</title>`)
		case "/assets/chunk.js":
			if r.Header.Get("Cookie") != "session=browser-session" {
				http.Error(w, "missing browser session cookie", http.StatusUnauthorized)
				return
			}
			sessionCookieSeen.Store(true)
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = io.WriteString(w, `fetch("/api/from-browser-chunk");`)
		case "/assets/redirect.js":
			http.Redirect(w, r, crossOrigin.URL+"/target.js", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer pageServer.Close()
	pageOrigin = pageServer.URL

	process := launcher.New().Bin(chrome).Headless(true).NoSandbox(true)
	wsURL, err := process.Launch()
	if err != nil {
		t.Fatalf("launch Chrome: %v", err)
	}
	defer process.Kill()

	driver := NewRodDriver(0, 15*time.Second)
	if err := driver.Connect(ctx, wsURL); err != nil {
		t.Fatalf("connect Rod driver: %v", err)
	}
	defer driver.Close()
	if err := driver.Navigate(ctx, pageServer.URL+"/page"); err != nil {
		t.Fatalf("navigate fixture: %v", err)
	}
	entry := `/* browser integration fixture */ import("./chunk.js"); import("./redirect.js");`
	result := extractJSWithChunks(ctx, driver, []storage.JSBody{{
		URL: pageServer.URL + "/assets/app.js", Body: []byte(entry),
	}}, pageServer.URL+"/page", testBundleChunkOptions())
	if !sessionCookieSeen.Load() {
		t.Fatal("same-origin chunk request did not carry the page session cookie")
	}
	if crossOriginCookie.Load() {
		t.Fatal("same-origin credentials leaked through a cross-origin redirect")
	}
	if result.FetchedChunks != 1 || result.ChunkFetchErrors != 1 {
		t.Fatalf("unexpected browser fetch outcome: %+v", result)
	}
	for _, discovery := range result.Discoveries {
		if strings.HasSuffix(discovery.URL, "/api/from-browser-chunk") && discovery.From == pageServer.URL+"/assets/chunk.js" {
			return
		}
	}
	t.Fatalf("browser-fetched chunk API was not attributed to its response URL: %+v", result.Discoveries)
}

func browserIntegrationBinary() string {
	if configured := strings.TrimSpace(os.Getenv("BROWCRAWLER_CHROME_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured
		}
	}
	for _, name := range []string{"chrome", "chrome.exe", "chromium", "chromium-browser", "msedge"} {
		if found, err := exec.LookPath(name); err == nil {
			return found
		}
	}
	programFiles := os.Getenv("PROGRAMFILES")
	localAppData := os.Getenv("LOCALAPPDATA")
	for _, candidate := range []string{
		filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
