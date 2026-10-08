package controller

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/ui"
)

// droppedHeaders are the headers of the requests of the browsers the agents
// never get, besides Sec-Fetch-* and X-Forwarded-* (see dropBrowserHeaders):
// the credentials of the dashboard, the session and its CSRF token among them,
// and where the requests come from, which the API of an agent refuses (see
// ui.WithAPIKey), checked by the dashboard already.
var droppedHeaders = []string{"Cookie", "Authorization", "Proxy-Authorization", csrfHeader, "Origin", "Referer",
	"Forwarded", "X-Real-Ip", ui.KeyHeader}

// answerHeaders are the headers of the answers of the agents the browsers get,
// canonical: the ones of their payload and of its cache. The others would act
// on the origin of the dashboard, eg: Set-Cookie, Refresh or the ones of CORS.
var answerHeaders = map[string]bool{"Content-Type": true, "Content-Length": true, "Content-Encoding": true,
	"Content-Disposition": true, "Cache-Control": true, "Expires": true, "Last-Modified": true, "Etag": true,
	"Retry-After": true, "Date": true, "Vary": true, "X-Accel-Buffering": true}

// servedTypes are the types of the payloads of the API of the agents the
// dashboard serves, besides the images of the decisions (see ui.ImageType)
// and the downloads: JSON, the events, the orders in CSV and the errors in
// text. Anything else, eg: HTML, would run on the origin of the dashboard.
var servedTypes = map[string]bool{"application/json": true, "text/event-stream": true, "text/csv": true,
	"text/plain": true}

// refusedAnswer is the error of an answer of an agent the dashboard doesn't
// serve.
type refusedAnswer struct{ what string }

func (e *refusedAnswer) Error() string { return "an answer of " + e.what }

// newAgentProxy proxies the requests to the API of an agent, their path
// relative to it, from the trade panel of the web dashboard, whatever channel
// the browser tells, with the key of the agent (see agentKeys), but none of
// the credentials and headers of the browser. A change without a payload, eg:
// start and stop, is JSON, as the agent wants. Of the answers, the headers of
// their payload only reach the browser, and only the types the API serves, the
// others a 502. The events stream is flushed as it comes, as ReverseProxy
// does with any text/event-stream.
func newAgentProxy(a Agent, api *url.URL, keys *agentKeys) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(api)
			dropBrowserHeaders(r.Out.Header)
			r.Out.Header.Set(ui.ChannelHeader, string(ui.ChannelWeb))
			keys.authorize(a.Name, r.Out.Header)
			if !safeMethod(r.Out.Method) && r.Out.ContentLength == 0 && r.Out.Header.Get("Content-Type") == "" {
				r.Out.Header.Set("Content-Type", "application/json")
			}
		},
		ModifyResponse: checkAnswer,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			var refused *refusedAnswer
			if errors.As(err, &refused) {
				log.Warnf("ui: agent %s: %v, refused", a.Name, err)
				writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("agent %s answered %s, which the dashboard "+
					"doesn't serve", a.Name, refused.what))
				return
			}
			log.Debugf("ui: agent %s: %v", a.Name, err)
			writeJSONError(w, http.StatusBadGateway, offline(a).Error())
		},
	}
}

// dropBrowserHeaders drops the headers of a request of a browser the agents
// never get, see droppedHeaders.
func dropBrowserHeaders(header http.Header) {
	for _, name := range droppedHeaders {
		header.Del(name)
	}
	for name := range header {
		if canonical := http.CanonicalHeaderKey(name); strings.HasPrefix(canonical, "Sec-Fetch-") ||
			strings.HasPrefix(canonical, "X-Forwarded-") {
			delete(header, name)
		}
	}
}

// checkAnswer keeps the headers of the payload of an answer of an agent (see
// answerHeaders) and refuses a payload of a type its API doesn't serve, or
// without a type, which the browser would sniff.
func checkAnswer(res *http.Response) error {
	for name := range res.Header {
		if !answerHeaders[http.CanonicalHeaderKey(name)] {
			delete(res.Header, name)
		}
	}
	if res.StatusCode == http.StatusSwitchingProtocols {
		return &refusedAnswer{"another protocol"}
	}
	value := res.Header.Get("Content-Type")
	if value == "" {
		if res.ContentLength == 0 {
			return nil
		}
		return &refusedAnswer{"a payload without its type"}
	}
	mediaType, _, err := mime.ParseMediaType(value)
	switch {
	case err != nil:
		return &refusedAnswer{fmt.Sprintf("a payload of the type %q", value)}
	case servedTypes[mediaType], ui.ImageType(mediaType):
		return nil
	case mediaType == "application/octet-stream" && attachment(res.Header.Get("Content-Disposition")):
		return nil // a file of a decision, see ui.ImageType
	}
	return &refusedAnswer{"a payload of the type " + mediaType}
}

// attachment reports whether the Content-Disposition of an answer makes its
// payload a download.
func attachment(disposition string) bool {
	kind, _, err := mime.ParseMediaType(disposition)
	return err == nil && kind == "attachment"
}

// agentKeys are the keys of the API of the agents the controller launched, by
// name, one for each start of an agent (see Manager.start), which the agent
// wants in ui.KeyHeader (see ui.WithAPIKey): no other process of the machine
// operates it. The agents run on their own have none.
type agentKeys struct {
	mu     sync.RWMutex
	byName map[string]string
}

func newAgentKeys() *agentKeys {
	return &agentKeys{byName: make(map[string]string)}
}

// set makes key the one of the agent of name.
func (k *agentKeys) set(name, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.byName[name] = key
}

// drop drops key, the one of a start of the agent of name, once it exited,
// unless another start has another one.
func (k *agentKeys) drop(name, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.byName[name] == key {
		delete(k.byName, name)
	}
}

// get returns the key of the agent of name, "" for none.
func (k *agentKeys) get(name string) string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.byName[name]
}

// authorize sets the key of the agent of name in header, when it has one.
func (k *agentKeys) authorize(name string, header http.Header) {
	header.Del(ui.KeyHeader)
	if key := k.get(name); key != "" {
		header.Set(ui.KeyHeader, key)
	}
}
