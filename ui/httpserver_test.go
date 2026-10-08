package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testServerLimits are the limits of the servers of the tests: the ones of
// NewHTTPServer, shorter.
var testServerLimits = serverLimits{
	ReadHeader: 300 * time.Millisecond,
	Read:       500 * time.Millisecond,
	Idle:       700 * time.Millisecond,
	MaxHeader:  defaultServerLimits.MaxHeader,
}

// serveHTTP serves handler with limits on the loopback and returns its
// address.
func serveHTTP(t *testing.T, handler http.Handler, limits serverLimits) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := newHTTPServer("", handler, limits)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

// closedWithin reports whether the server closes conn within d, reading
// whatever it answers meanwhile.
func closedWithin(conn net.Conn, d time.Duration) bool {
	_ = conn.SetReadDeadline(time.Now().Add(d))
	_, err := io.Copy(io.Discard, conn)
	var timeout net.Error
	return err == nil || !(errors.As(err, &timeout) && timeout.Timeout())
}

func TestNewHTTPServer_Limits(t *testing.T) {
	server := NewHTTPServer("localhost:1", http.NotFoundHandler())
	assert.Equal(t, "localhost:1", server.Addr)
	assert.Equal(t, 10*time.Second, server.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, server.ReadTimeout, "the body of a request")
	assert.Equal(t, 2*time.Minute, server.IdleTimeout)
	assert.Equal(t, 32<<10, server.MaxHeaderBytes)
	assert.Zero(t, server.WriteTimeout, "the events and the long polls answer for longer")
}

// A request whose body comes a byte at a time, eg: a login, is cut once the
// time to read it is over, rather than holding its connection and its handler
// forever.
func TestHTTPServer_CutsASlowBody(t *testing.T) {
	read := make(chan error, 1)
	addr := serveHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		read <- err
	}), testServerLimits)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "POST /api/auth/login HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\n"+
		"Content-Length: 4096\r\n\r\n", addr)
	require.NoError(t, err)

	start := time.Now()
	stop := make(chan struct{})
	defer close(stop)
	go func() { // a byte every 50ms: each in time for a read, never the whole body
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				if _, err := conn.Write([]byte("x")); err != nil {
					return
				}
			}
		}
	}()

	select {
	case err := <-read:
		require.Error(t, err, "the body cut")
		assert.Less(t, time.Since(start), 2*time.Second)
	case <-time.After(3 * time.Second):
		t.Fatal("the handler still reads the body")
	}
	assert.True(t, closedWithin(conn, 2*time.Second), "the connection closed")
}

// The headers have their own time, and a size: 431 above it.
func TestHTTPServer_CutsSlowOrLargeHeaders(t *testing.T) {
	addr := serveHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), testServerLimits)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "GET /api/health HTTP/1.1\r\nHost: %s\r\n", addr) // and nothing else
	require.NoError(t, err)
	assert.True(t, closedWithin(conn, 2*time.Second), "the connection closed")

	request, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/health", nil)
	require.NoError(t, err)
	request.Header.Set("X-Large", strings.Repeat("x", 64<<10))
	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusRequestHeaderFieldsTooLarge, resp.StatusCode)
}

// A connection kept alive is closed once idle for long.
func TestHTTPServer_ClosesAnIdleConnection(t *testing.T) {
	addr := serveHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), testServerLimits)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "GET /api/health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF, "closed by the server")
	assert.GreaterOrEqual(t, time.Since(start), testServerLimits.Idle/2)
}

// The answers have no limit, eg: the events of the dashboard or a long poll
// of the internal API, which run past the time to read the request, and still
// end when their client goes away.
func TestHTTPServer_LongAnswers(t *testing.T) {
	ended := make(chan error, 1)
	addr := serveHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				ended <- r.Context().Err()
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, "data: {}\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}), testServerLimits)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)
	until := time.Now().Add(3 * testServerLimits.Read)
	events := 0
	for time.Now().Before(until) {
		line, err := reader.ReadString('\n')
		require.NoError(t, err, "the events cut after %d", events)
		if strings.HasPrefix(line, "data:") {
			events++
		}
	}
	assert.Greater(t, events, 10)

	cancel()
	select {
	case err := <-ended:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("the handler didn't see its client go away")
	}
}
