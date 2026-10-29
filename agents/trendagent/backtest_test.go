package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/internal/backtest"
)

// A backtest of the agent as the controller runs it (-mode backtest
// -backtest-config, the binary apart) never reaches a broker, even given the
// URL of a bridge and a token: it reads its files, trades the paper wallet and
// writes its report.
func TestBacktest_NeverReachesTheBroker(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the agent")
	}
	var requests atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer bridge.Close()

	dir := t.TempDir()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var csv strings.Builder
	csv.WriteString("time,open,close,low,high,volume,spread\n")
	for i := range 6000 {
		at := start.Add(time.Duration(i) * time.Minute)
		price := 4000 + 30*math.Sin(float64(i)/180) + 10*math.Sin(float64(i)/23)
		fmt.Fprintf(&csv, "%d,%.3f,%.3f,%.3f,%.3f,1,20\n", at.Unix(), price, price+0.2, price-0.6, price+0.7)
	}
	file := filepath.Join(dir, "XAUUSDm.csv")
	require.NoError(t, os.WriteFile(file, []byte(csv.String()), 0o600))
	report := filepath.Join(dir, "report.json")
	cfg := backtest.Config{From: start.Add(48 * time.Hour).Unix(), To: start.Add(100 * time.Hour).Unix(),
		Balance: 10000, Fee: 0.0001, SpreadPoints: 20, SlippagePoints: 5, Conservative: true, GapFills: true,
		Symbols: map[string]backtest.Symbol{"XAUUSDm": {Point: 0.001, VolumeStep: 1, VolumeMin: 1}},
		Report:  report, Progress: true}
	config := filepath.Join(dir, "config.json")
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(config, data, 0o600))

	cmd := exec.Command("go", "run", ".", "-mode=backtest", "-strategy=emapullback", "-pair=XAUUSDm",
		"-timeframe=15m", "-lot=0.01", "-csv="+file, "-csv-timeframe=1m", "-backtest-config="+config,
		"-mt5-bridge="+bridge.URL, "-mt5-token=s3cret")
	cmd.Env = append(os.Environ(), "MT5_BRIDGE_TOKEN=s3cret")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), "PROGRESS")

	data, err = os.ReadFile(report)
	require.NoError(t, err)
	var got backtest.Report
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, backtest.StatusSucceeded, got.Status, got.Error)
	require.Equal(t, "USD", got.Currency)
	require.Zero(t, requests.Load(), "no request reached the bridge")
}
