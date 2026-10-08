package runner

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The API of an agent the controller launched answers the key of its start
// only, the one the controller sends; run on its own, it has none.
func TestDashboard_KeyOfItsAPI(t *testing.T) {
	status := func(chart *ui.Chart, key string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost:5556/api/pairs", nil)
		if key != "" {
			req.Header.Set(ui.KeyHeader, key)
		}
		w := httptest.NewRecorder()
		chart.Handler().ServeHTTP(w, req)
		return w.Code
	}

	launched, err := dashboard{addr: "localhost:5556", key: "the-key"}.chart(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, status(launched, ""))
	assert.Equal(t, http.StatusForbidden, status(launched, "another"))
	assert.Equal(t, http.StatusOK, status(launched, "the-key"))

	alone, err := dashboard{addr: "localhost:5556"}.chart(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status(alone, ""))
}
