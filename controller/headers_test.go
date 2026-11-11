package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inlineScript is the inline script of a page.
var inlineScript = regexp.MustCompile(`<script>([^<]*)</script>`)

// hashOf returns the source of a Content-Security-Policy of script.
func hashOf(script string) string {
	sum := sha256.Sum256([]byte(script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// Every answer of the dashboard, its pages, its files, its API and its
// redirects, tells the browser to run its own scripts only, never to show it
// in a frame nor to sniff its types, and to send its address to itself only.
func TestDashboard_SecurityHeaders(t *testing.T) {
	d, err := NewDashboard(nil, WithUsers(newFakeUsers(testUsername, testPassword)), WithUIDir(localBundle(t)))
	require.NoError(t, err)
	d.auth.failDelay = 0
	srv := httptest.NewServer(d.Handler(context.Background()))
	t.Cleanup(srv.Close)

	check := func(resp *http.Response, what string) string {
		t.Helper()
		assert.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"), what)
		assert.Equal(t, []string{"nosniff"}, resp.Header.Values("X-Content-Type-Options"), what)
		assert.Equal(t, "same-origin", resp.Header.Get("Referrer-Policy"), what)
		policies := resp.Header.Values("Content-Security-Policy")
		require.Len(t, policies, 1, what)
		for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'",
			"base-uri 'none'", "style-src 'self'", "connect-src 'self'", "font-src 'self'", "img-src 'self'"} {
			assert.Contains(t, policies[0], directive, what)
		}
		assert.NotContains(t, policies[0], "unsafe", what)
		return policies[0]
	}

	resp, body := do(t, http.MethodGet, srv.URL+"/login", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	policy := check(resp, "the index page")
	scripts := inlineScript.FindAllStringSubmatch(body, -1)
	require.Len(t, scripts, 1, body)
	assert.Equal(t, `window.__XTRADER__ = { version: "local", api: "/api" };`, scripts[0][1])
	assert.Contains(t, policy, "script-src 'self' "+hashOf(scripts[0][1])+";", "the inline script by its hash")

	resp, _ = do(t, http.MethodGet, srv.URL+"/", "", nil)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, check(resp, "a redirect to the login"), "script-src 'self';", "no inline script")
	resp, _ = do(t, http.MethodGet, srv.URL+"/ui/local/app.js", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	check(resp, "a file of the bundle")
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	check(resp, "the API")
	resp, _ = login(t, srv, testUsername, testPassword)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	check(resp, "the login")
	resp, _ = do(t, http.MethodGet, srv.URL+"/settings", "", sessionOf(resp))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	check(resp, "a page of a session")
}

// The settings of the web app in the index page are a script of their own,
// never broken out of, whatever the version of the bundle.
func TestIndexPage_ScriptOfItsVersion(t *testing.T) {
	for _, version := range []string{"v1.4.0", `v1"</script><script>alert(1)</script>`, "v1 x"} {
		page, policy, err := indexPage(version)
		require.NoError(t, err)
		scripts := inlineScript.FindAllStringSubmatch(string(page), -1)
		require.Len(t, scripts, 1, version)
		assert.NotContains(t, string(page), "<script>alert", version)
		assert.Contains(t, policy, hashOf(scripts[0][1]), version)
	}
}

// The files of the bundle are served, never a listing of its directories nor
// its hidden files.
func TestDashboard_BundleWithoutListings(t *testing.T) {
	d := newTestDashboard(t)
	dir := localBundle(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assets", "font.woff2"), []byte("font"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, completeMarker), []byte("v1.0.0\n"), 0o644))
	srv := httptest.NewServer(asAdmin(d.router(bundle{dir: dir, version: "v1.0.0"}, nil)))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/ui/v1.0.0/", "/ui/v1.0.0/assets/", "/ui/v1.0.0/assets", "/ui/v1.0.0/.complete",
		"/ui/v1.0.0/assets/../.complete"} {
		resp, body := do(t, http.MethodGet, srv.URL+path, "", nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		assert.NotContains(t, body, "font.woff2", path)
		assert.NotContains(t, body, "app.js", path)
		assert.NotContains(t, body, "v1.0.0", path)
	}
	resp, body := get(t, srv.URL+"/ui/v1.0.0/assets/font.woff2")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "font", body)
	resp, _ = get(t, srv.URL+"/ui/v1.0.0/app.css")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css"))
}
