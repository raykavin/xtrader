package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
)

// The dashboard is served with a Content-Security-Policy of its own files only
// (the bundle, its fonts and images, and the API of the same origin, fetch and
// EventSource), the inline script of its index page allowed by its hash (see
// indexPage), never in a frame of another site; the styles of the web app are
// its stylesheet and the CSSOM of React, which the policy doesn't restrict.
const (
	// pagePolicy is the policy of the dashboard, the scripts of the index
	// page added to script-src.
	pagePolicy = "default-src 'self'; script-src 'self'%s; style-src 'self'; img-src 'self'; font-src 'self'; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	// errorPolicy is the policy of the page telling the bundle unavailable:
	// no script, its styles inline.
	errorPolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; base-uri 'none'; " +
		"form-action 'none'; frame-ancestors 'none'"
)

// policy returns the Content-Security-Policy of the dashboard allowing the
// inline scripts of the hashes, eg: 'sha256-...'.
func policy(hashes ...string) string {
	scripts := ""
	for _, hash := range hashes {
		scripts += " " + hash
	}
	return fmt.Sprintf(pagePolicy, scripts)
}

// securityHeaders sets the headers of the security of the dashboard on every
// answer: its Content-Security-Policy (see policy), which a page replaces with
// its own, never in a frame, never sniffed, and its address sent to itself
// only.
func securityHeaders(next http.Handler) http.Handler {
	defaultPolicy := policy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", defaultPolicy)
		header.Set("X-Frame-Options", "DENY")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// indexPage renders the index page of the bundle of version, and its
// Content-Security-Policy: its inline script, the settings of the web app,
// allowed by its hash.
func indexPage(version string) ([]byte, string, error) {
	quoted, err := json.Marshal(version) // a string of JS, without < > & in a <script>
	if err != nil {
		return nil, "", err
	}
	script := "window.__XTRADER__ = { version: " + string(quoted) + `, api: "/api" };`
	var page bytes.Buffer
	if err := indexTemplate.Execute(&page, map[string]any{"Version": version, "Config": template.JS(script)}); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256([]byte(script))
	return page.Bytes(), policy("'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"), nil
}

// bundleFiles are the files of a bundle, served without a listing of its
// directories nor its hidden files, eg: the marker of a download complete.
type bundleFiles struct{ http.FileSystem }

func (b bundleFiles) Open(name string) (http.File, error) {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return nil, os.ErrNotExist
		}
	}
	file, err := b.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || info.IsDir() {
		_ = file.Close()
		return nil, os.ErrNotExist
	}
	return file, nil
}
