package llm

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// failingOnce answers status with body to the first request, with headers,
// then answer, and counts the requests.
func failingOnce(t *testing.T, status int, body, answer string, headers ...string) (*atomic.Int32, string) {
	t.Helper()
	var requests atomic.Int32
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			for i := 0; i+1 < len(headers); i += 2 {
				w.Header().Set(headers[i], headers[i+1])
			}
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
			return
		}
		_, _ = fmt.Fprint(w, answer)
	})
	return &requests, server.URL
}

// shortRetry sets the waits of the retry of the test.
func shortRetry(t *testing.T, wait, most, room time.Duration) {
	t.Helper()
	w, m, r := retryWait, retryMaxWait, retryRoom
	t.Cleanup(func() { retryWait, retryMaxWait, retryRoom = w, m, r })
	retryWait, retryMaxWait, retryRoom = wait, most, room
}

// M8: an answer the API didn't bill, 429 (but the quota spent), 503 or 529, is
// sent again once, after its Retry-After or a wait, within the deadline of the
// request: the decision of the candle kept.
func TestProviders_SentAgainOnceWhenNotRun(t *testing.T) {
	shortRetry(t, 20*time.Millisecond, time.Second, 100*time.Millisecond)
	for _, tp := range testProviders {
		for _, tc := range []struct {
			status  int
			body    string
			headers []string
			again   bool
		}{
			{http.StatusTooManyRequests, `{"error": {"type": "rate_limit_error", "message": "slow down"}}`,
				[]string{"Retry-After-Ms", "50"}, true},
			{http.StatusServiceUnavailable, `{"error": {"message": "unavailable"}}`, nil, true},
			{529, `{"error": {"type": "overloaded_error", "message": "Overloaded"}}`, nil, true},
			{http.StatusTooManyRequests, `{"error": {"code": "insufficient_quota", "message": "quota"}}`, nil, false},
			{http.StatusTooManyRequests, `{"error": {"message": "slow down"}}`, []string{"Retry-After", "5"}, false},
			{http.StatusInternalServerError, `{"error": {"message": "boom"}}`, nil, false},
			{http.StatusBadGateway, `{"error": {"message": "bad gateway"}}`, nil, false},
			{http.StatusBadRequest, `{"error": {"message": "bad"}}`, nil, false},
		} {
			t.Run(fmt.Sprintf("%s %d %v", tp.name, tc.status, tc.headers), func(t *testing.T) {
				requests, url := failingOnce(t, tc.status, tc.body, tp.answer(longDecision), tc.headers...)
				provider, err := tp.create(url)
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				start := time.Now()
				d, err := provider.Analyze(ctx, testInput(nil, flatActions...))
				if !tc.again {
					require.Error(t, err)
					require.EqualValues(t, 1, requests.Load(), "never sent again")
					return
				}
				require.NoError(t, err)
				require.Equal(t, ActionEnterLong, d.Action)
				require.EqualValues(t, 2, requests.Load())
				wait := 20 * time.Millisecond
				if len(tc.headers) > 0 {
					wait = 50 * time.Millisecond // its Retry-After
				}
				require.GreaterOrEqual(t, time.Since(start), wait)
			})
		}
	}
}

// M8: never sent again past the deadline of the request: the wait and
// retryRoom left before it, or else the error at once.
func TestProviders_NeverSentAgainPastTheDeadline(t *testing.T) {
	shortRetry(t, 300*time.Millisecond, time.Second, 100*time.Millisecond)
	for _, tp := range testProviders {
		t.Run(tp.name, func(t *testing.T) {
			requests, url := failingOnce(t, 529, `{"error": {"message": "Overloaded"}}`, tp.answer(longDecision))
			provider, err := tp.create(url)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err = provider.Analyze(ctx, testInput(nil, flatActions...))
			require.ErrorIs(t, err, ErrUnavailable)
			require.EqualValues(t, 1, requests.Load())
			require.Less(t, time.Since(start), 300*time.Millisecond, "the error at once")

			// sent again once at most
			requests, url = failingOnce(t, 529, `{"error": {"message": "Overloaded"}}`,
				`{"error": {"message": "Overloaded"}}`)
			provider, err = tp.create(url)
			require.NoError(t, err)
			shortRetry(t, time.Millisecond, time.Second, 0)
			_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
			require.Error(t, err)
			require.EqualValues(t, 2, requests.Load())
		})
	}
}

// M8: the tokens of an answer 2xx that can't be read whole are counted when it
// tells them, eg: a body cut short after them.
func TestUsage_CountedForUnreadableAnswers(t *testing.T) {
	for _, tc := range []struct {
		tp    testProvider
		body  string
		usage Usage
	}{
		{testProviders[0], `{"usage": {"prompt_tokens": 4800, "completion_tokens": 300, "prompt_tokens_details": ` +
			`{"cached_tokens": 1000}, "completion_tokens_details": {"reasoning_tokens": 200}}, "choices": [{"mess`,
			Usage{Input: 4800, Cached: 1000, Output: 300, Reasoning: 200}},
		{testProviders[0], `{"choices": "unexpected", "usage": {"prompt_tokens": 100, "completion_tokens": 5}}`,
			Usage{Input: 100, Output: 5}},
		{testProviders[1], `{"usage": {"input_tokens": 3000, "output_tokens": 90, "cache_read_input_tokens": 1200, ` +
			`"cache_creation_input_tokens": 500}, "content": [{"type": "tool_use", "inp`,
			Usage{Input: 4200, Cached: 1200, CacheWritten: 500, Output: 90}},
		{testProviders[1], `<html>bad gateway</html>`, Usage{}},
	} {
		t.Run(tc.tp.name+" "+tc.body[:20], func(t *testing.T) {
			provider, err := tc.tp.create(answering(t, http.StatusOK, tc.body).URL)
			require.NoError(t, err)
			_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
			require.ErrorIs(t, err, ErrInvalidResponse)
			usage, requests := provider.(interface{ TodayUsage() (Usage, int) }).TodayUsage()
			require.Equal(t, tc.usage, usage)
			if tc.usage == (Usage{}) {
				require.Zero(t, requests, "nothing told, nothing counted")
			} else {
				require.Equal(t, 1, requests)
			}
		})
	}
}
