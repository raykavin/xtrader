package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rowsRead returns the rows of table a query read, the ones its filters left
// out included, from the plan EXPLAIN ANALYZE answers.
func rowsRead(t *testing.T, s *Store, table, query string, args ...any) float64 {
	t.Helper()
	var plans []string
	require.NoError(t, s.db.Raw("EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&plans).Error)
	require.Len(t, plans, 1)
	var explained []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(plans[0]), &explained))
	var read func(node map[string]any) float64
	read = func(node map[string]any) float64 {
		var rows float64
		if node["Relation Name"] == table {
			number := func(key string) float64 {
				value, _ := node[key].(float64)
				return value
			}
			rows = (number("Actual Rows") + number("Rows Removed by Filter")) * number("Actual Loops")
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			rows += read(child.(map[string]any))
		}
		return rows
	}
	return read(explained[0].Plan)
}

// M7: the last decisions of each pair, read at the start of an agent, and the
// memory of its prompt read from its journal: the rows read are about the ones
// answered, never the whole history of the agent, months of decisions taken
// every few seconds.
func TestDecisionsPerPair_ReadsTheLastOnesOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	other := llmAgent(t, s, "other")

	const each = 3000
	pairs := []string{"BTCUSDm", "EURUSDm", "XAUUSDm"}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []int64{agent.ID, other.ID} {
		for p, pair := range pairs {
			// a decision every 10 s, the ones of a pair a second apart from the
			// others, two at the same time at its end (the ID their order)
			require.NoError(t, s.db.Exec(`INSERT INTO decisions (agent_id, id, pair, candle_at, decided_at, action,
					outcome, reason, notes)
				SELECT ?, to_char(n, 'FM00000') || '-' || ?::text, ?::text, ?::timestamptz + n * interval '10 seconds',
					?::timestamptz + LEAST(n, ? - 1) * interval '10 seconds' + ? * interval '1 second', 'WAIT',
					'applied', 'the reason ' || n, ?::jsonb
				FROM generate_series(1, ?) AS n`, id, pair, pair, start, start, each, p,
				`[{"kind":"text","text":"a note of the model"}]`, each).Error)
			require.NoError(t, s.db.Exec(`INSERT INTO journal_entries (agent_id, id, at, pair, input, decision, error)
				SELECT ?, to_char(n, 'FM00000') || '-' || ?::text, ?::timestamptz + n * interval '10 seconds' +
						? * interval '1 second', ?::text,
					jsonb_build_object('symbol', ?::text, 'time', ?::timestamptz + n * interval '10 seconds',
						'context', repeat('x', 2000)),
					CASE WHEN n % 7 = 0 THEN NULL ELSE jsonb_build_object('action', 'WAIT', 'confidence', 0.5,
						'reason', ' why ' || n) END,
					CASE WHEN n % 11 = 0 THEN 'timeout' ELSE '' END
				FROM generate_series(1, ?) AS n`, id, pair, start, p, pair, pair, start, each).Error)
		}
	}
	require.NoError(t, s.db.Exec("ANALYZE decisions").Error)
	require.NoError(t, s.db.Exec("ANALYZE journal_entries").Error)

	const perPair = 5
	got, err := s.DecisionsPerPair(ctx, agent.ID, perPair)
	require.NoError(t, err)
	var want []string
	for _, pair := range pairs {
		// the last two at the same time: the ID their order, newest first
		for n := each; n > each-perPair; n-- {
			want = append(want, fmt.Sprintf("%05d-%s", n, pair))
		}
	}
	slices.SortFunc(want, func(a, b string) int { // decided_at, then the ID, newest first
		at := func(id string) (int, int) {
			var n int
			var pair string
			_, _ = fmt.Sscanf(id, "%05d-%s", &n, &pair)
			return min(n, each-1), slices.Index(pairs, pair)
		}
		an, ap := at(a)
		bn, bp := at(b)
		if an != bn {
			return bn - an
		}
		if ap != bp {
			return bp - ap
		}
		if a > b {
			return -1
		}
		return 1
	})
	require.Equal(t, want, ids(got))
	require.Equal(t, "the reason 3000", got[0].Reason)

	read := rowsRead(t, s, "decisions", decisionsPerPair, sql.Named("agent", agent.ID),
		sql.Named("limit", perPair))
	require.LessOrEqual(t, read, float64(len(pairs)*(perPair+2)+1), "the rows read: the ones answered")

	remembered, err := s.JournalMemory(ctx, agent.ID, perPair)
	require.NoError(t, err)
	require.Len(t, remembered, len(pairs)*perPair)
	for i, r := range remembered {
		require.Equal(t, "WAIT", r.Action)
		require.NotEqual(t, "timeout", r.Reason)
		if i > 0 {
			require.False(t, r.At.Before(remembered[i-1].At), "oldest first")
		}
	}
	last := remembered[len(remembered)-1]
	require.Equal(t, "XAUUSDm", last.Pair)
	require.Equal(t, fmt.Sprintf("why %d", each), last.Reason, "trimmed")
	require.True(t, last.Candle.Equal(start.Add(each*10*time.Second)))
	for _, r := range remembered[len(remembered)-perPair:] {
		var n int
		_, _ = fmt.Sscanf(r.Reason, "why %d", &n)
		require.True(t, n%7 != 0 && n%11 != 0, "an entry without a decision or failed left out: %d", n)
	}

	read = rowsRead(t, s, "journal_entries", journalMemory, sql.Named("agent", agent.ID),
		sql.Named("limit", perPair))
	require.LessOrEqual(t, read, float64(len(pairs)*(perPair*2+2)+1),
		"the rows read: the last ones of each pair, the ones without a decision among them")
}
