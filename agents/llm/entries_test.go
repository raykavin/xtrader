package llm

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/service"
)

// blockingBroker is a broker whose entries are blocked or not.
type blockingBroker struct {
	service.Broker
	blocked bool
	reason  string
}

func (b blockingBroker) EntriesBlocked() (bool, string, int64) { return b.blocked, b.reason, 1 }

// An entry of the model is refused, with why, while the entries of the agent
// are blocked; a broker that doesn't tell them blocks nothing.
func TestEntriesBlockedBy(t *testing.T) {
	require.Empty(t, entriesBlockedBy(blockingBroker{}))
	require.Equal(t, "the entries of the agent are blocked: stopped by the supervisor",
		entriesBlockedBy(blockingBroker{blocked: true, reason: "stopped by the supervisor"}))
	require.Equal(t, "the entries of the agent are blocked", entriesBlockedBy(blockingBroker{blocked: true}))
	var plain service.Broker
	require.Empty(t, entriesBlockedBy(plain))
}
