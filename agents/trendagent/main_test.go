package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
)

// The default strategy of the agent names the comment of its orders on MT5,
// and so its magic number: it is the one the controller and the store compute
// for the kind trend (agentapi.DefaultStrategy), or the agent launched by the
// controller would refuse to trade (agentapi.Config.CheckMT5Book) and the
// store would not keep it apart from the other agents.
func TestAgent_DefaultStrategyOfItsKind(t *testing.T) {
	a := agent()
	require.NotEmpty(t, a.DefaultStrategy())
	require.Equal(t, agentapi.DefaultStrategy("trend"), a.DefaultStrategy())
	require.Equal(t, "nb:"+a.DefaultStrategy(), agentapi.NewMT5Book("trend", map[string]string{}, nil).Comment)
}
