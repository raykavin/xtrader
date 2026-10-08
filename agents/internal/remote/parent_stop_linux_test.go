package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/testdata/mocks"
)

const (
	// controllerURLEnv is the variable of the URL of the internal API the
	// fake controller hands its agent, see stopInOrder.
	controllerURLEnv = "REMOTE_TEST_CONTROLLER_URL"
	// stopResultEnv is the variable of the file the agent writes how it
	// stopped to, see stopInOrder.
	stopResultEnv = "REMOTE_TEST_STOP_RESULT"
)

// stopResult is how an agent of the tests stopped, see servingAgent.
type stopResult struct {
	Served  string `json:"served"`  // the error of Serve
	Created string `json:"created"` // of the order at the exchange as the signal came
	Refused bool   `json:"refused"` // an order asked once Serve returned
	Closed  string `json:"closed"`  // the error of Close
}

// fakeControllerProcess is a controller of the tests: it launches a serving
// agent, its output a pipe the controller reads, as the supervisor does, and
// waits for it.
func fakeControllerProcess() int {
	agent := exec.Command(os.Args[0], "-test.run=^TestParentDeath_Process$")
	agent.Env = append(os.Environ(), processEnv+"=serving-agent", agentapi.EnvToken+"="+testToken,
		agentapi.EnvURL+"="+os.Getenv(controllerURLEnv))
	// writers that aren't files: the agent writes to pipes this process reads
	agent.Stdout, agent.Stderr = struct{ io.Writer }{os.Stdout}, struct{ io.Writer }{io.Discard}
	agent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := agent.Start(); err != nil {
		return 1
	}
	fmt.Printf("program %d\n", agent.Process.Pid)
	_ = agent.Wait()
	return 0
}

// servingAgent is an agent of the controller of the tests, stopped by a
// signal: its bot has an order at the exchange as the signal comes, executed
// a moment later, and writes how it stopped to the file of stopResultEnv.
func servingAgent() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	time.AfterFunc(30*time.Second, stop) // never left running by a test failing
	file := os.Getenv(stopResultEnv)
	a, err := Connect(context.Background())
	if err != nil || a == nil {
		return 2
	}
	if err := a.Open(filepath.Dir(file)); err != nil {
		return 2
	}
	orders, err := a.Orders(context.Background())
	if err != nil {
		return 2
	}

	const pair = "BTCUSDT"
	exch := &mocks.Exchange{}
	warmup := []model.Candle{{Pair: pair, Time: time.Now().Add(-time.Minute), Open: 100, Close: 100, High: 100,
		Low: 100, Complete: true, Metadata: map[string]float64{}}}
	exch.On("CandlesByLimit", mock.Anything, pair, "1m", 1).Return(warmup, nil)
	exch.On("CandlesSubscription", mock.Anything, pair, "1m").Return(make(chan model.Candle), make(chan error))
	entered, release := make(chan struct{}), make(chan struct{})
	exch.On("CreateOrderMarket", model.SideTypeBuy, pair, 1.0).Run(func(mock.Arguments) {
		close(entered)
		<-release
	}).Return(model.Order{ExchangeID: 701, Pair: pair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 100, Quantity: 1, CreatedAt: time.Now()}, nil).Once()
	bot, err := xtrader.NewBot(context.Background(), model.Settings{Pairs: []string{pair}}, exch, idleStrategy{},
		xtrader.WithStorage(orders))
	if err != nil {
		return 2
	}
	controller := bot.Controller()

	created := make(chan error, 1)
	go func() {
		for controller.Status() != order.StatusRunning {
			time.Sleep(time.Millisecond)
		}
		go func() {
			_, err := controller.CreateOrderMarket(model.SideTypeBuy, pair, 1)
			created <- err
		}()
		<-entered
		fmt.Println("agent started")
	}()
	go func() {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // the stop logs meanwhile
		close(release)
	}()

	var result stopResult
	if err := a.Serve(ctx, nil, bot.Run); err != nil {
		result.Served = err.Error()
	}
	if err := <-created; err != nil {
		result.Created = err.Error()
	}
	_, err = controller.CreateOrderMarket(model.SideTypeSell, pair, 2)
	result.Refused = errors.Is(err, order.ErrBotStopped)
	if err := a.Close(); err != nil {
		result.Closed = err.Error()
	}
	data, _ := json.Marshal(result)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return 2
	}
	return 0
}

// B11/R5: an agent whose controller is killed gets SIGTERM and stops in order
// though its output goes nowhere anymore, the pipes of the controller closed:
// its bot stops trading, the order at the exchange recorded, then the outbox
// is sent and closed, rather than the first line it logs killing it (SIGPIPE).
func TestParentDeath_TheAgentStopsInOrderWithoutItsOutput(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.cfg.LLM, f.cfg.Prompt = nil, nil
		f.runtime = agentapi.Runtime{Version: 1}
	})
	result := filepath.Join(t.TempDir(), "stopped.json")
	controller, pid := goRun(t, "controller", true, controllerURLEnv+"="+f.srv.URL, stopResultEnv+"="+result)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	require.True(t, alive(pid))

	require.NoError(t, controller.Process.Signal(syscall.SIGKILL))
	_ = controller.Wait()
	require.Eventually(t, func() bool { return !alive(pid) }, 20*time.Second, 10*time.Millisecond,
		"the agent %d left running without its controller", pid)

	data, err := os.ReadFile(result)
	require.NoError(t, err, "the agent died before it stopped in order, eg: of SIGPIPE")
	var stopped stopResult
	require.NoError(t, json.Unmarshal(data, &stopped))
	assert.Empty(t, stopped.Served)
	assert.Empty(t, stopped.Created, "the order at the exchange as the signal came, recorded")
	assert.True(t, stopped.Refused, "an order once stopped")
	assert.Empty(t, stopped.Closed, "the outbox sent and closed")

	var keys []string
	for _, record := range f.written() {
		if record.Kind == agentapi.KindOrder {
			keys = append(keys, record.Key)
		}
	}
	require.NotEmpty(t, keys, "the order executed after the signal, recorded and sent")
	for _, key := range keys {
		_, err := strconv.ParseInt(key, 10, 64)
		assert.NoError(t, err)
	}
}
