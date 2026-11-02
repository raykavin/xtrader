package ui

import (
	"crypto/subtle"
	"mime"
	"net"
	"net/http"
	"strings"
)

// KeyHeader is the header of the requests of the controller to the API of an
// agent it launched, with the key of that start of the agent, see WithAPIKey.
const KeyHeader = "X-Xtrader-Agent-Key"

// WithAPIKey makes the API answer only the requests with key in KeyHeader: the
// key the controller gives each start of an agent it launches, so that no other
// process of the machine operates the bot. Without one, eg: an agent run on its
// own, the API answers whoever reaches its address.
func WithAPIKey(key string) Option {
	return func(c *Chart) { c.key = key }
}

// LoopbackHost reports whether host is localhost or an address of the
// loopback, 127.0.0.0/8 or ::1, where the API of an agent listens and the
// controller reaches it.
func LoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guard lets through the requests of the controller only, the client of the
// API, which never sends the headers of a browser: the requests of the web
// dashboard reach the API through its proxy, which drops them once it checked
// their session. A page of another site in a browser of the machine reaches
// localhost too, even with a form or a no-cors fetch, which need no preflight:
// its requests are refused by their Origin or Sec-Fetch-Site, and the ones
// changing something must be JSON, which a page can't send without a
// preflight. A name of another site resolved to the loopback (DNS rebinding)
// makes its pages of the same origin as the API: refused by their Host, the
// controller asking for a host of the loopback. With a key (see WithAPIKey),
// the requests without it are refused.
func (c *Chart) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !loopbackRequest(r):
			writeJSONError(w, http.StatusMisdirectedRequest,
				"A API do agente só responde no loopback: localhost, 127.0.0.0/8 ou [::1].")
		case fromBrowser(r):
			writeJSONError(w, http.StatusForbidden,
				"A API do agente só responde ao controller: abra o agente pelo painel.")
		case c.key != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(KeyHeader)), []byte(c.key)) != 1:
			writeJSONError(w, http.StatusForbidden, "A API do agente só responde ao controller que o iniciou.")
		case changes(r.Method) && !isJSON(r.Header.Get("Content-Type")):
			writeJSONError(w, http.StatusUnsupportedMediaType, "O pedido deve ser JSON: "+
				"Content-Type: application/json.")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// loopbackRequest reports whether the request asks for a host of the loopback,
// see LoopbackHost.
func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil { // without a port
		host = strings.TrimSuffix(strings.TrimPrefix(r.Host, "["), "]")
	}
	return LoopbackHost(host)
}

// fromBrowser reports whether a page of a browser sent the request: with an
// Origin, or from another site.
func fromBrowser(r *http.Request) bool {
	if len(r.Header.Values("Origin")) > 0 {
		return true
	}
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	return site == "cross-site" || site == "same-site"
}

// changes reports whether a request of method changes something.
func changes(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isJSON reports whether contentType is the one of JSON.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}
