package ui

import (
	"net/http"
	"time"
)

// serverLimits bound how long an HTTP server waits for a client.
type serverLimits struct {
	ReadHeader time.Duration // the headers of a request
	Read       time.Duration // the whole request, its body included
	Idle       time.Duration // a kept-alive connection, between two requests
	MaxHeader  int           // the bytes of the headers of a request
}

// defaultServerLimits are the ones of NewHTTPServer. A request has Read to be
// sent, eg: the body of a login sent a byte at a time is cut there, but the
// answer has no limit: the events stream for hours and a long poll of the
// internal API waits up to 30s, and the read deadline doesn't cut a handler
// that runs past it, nor hides a client gone.
var defaultServerLimits = serverLimits{
	ReadHeader: 10 * time.Second,
	Read:       30 * time.Second,
	Idle:       2 * time.Minute,
	MaxHeader:  32 << 10,
}

// NewHTTPServer returns the server of handler on addr with the limits of the
// servers of the controller and of the agents: a request sent slowly or a
// connection left idle is closed rather than kept forever, see
// defaultServerLimits.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return newHTTPServer(addr, handler, defaultServerLimits)
}

func newHTTPServer(addr string, handler http.Handler, limits serverLimits) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: limits.ReadHeader,
		ReadTimeout:       limits.Read,
		IdleTimeout:       limits.Idle,
		MaxHeaderBytes:    limits.MaxHeader,
	}
}
