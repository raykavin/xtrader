// Package seedtest reads the agents the seeds of the database create
// (sql/seeds/0004_agents.sql), for the tests of the agents: the strategy each
// agent of a kind trades.
package seedtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var (
	// one agent inserted alone: its name, its kind and its params
	single = regexp.MustCompile(`(?s)SELECT '([A-Za-z0-9._-]+)', '([a-z]+)', FALSE, 'live', 'localhost:\d+', ` +
		`(?:TRUE|FALSE), (?:TRUE|FALSE), (?:TRUE|FALSE), (?:TRUE|FALSE),\s*'(\{.*?\})'::jsonb`)
	// an agent of the list of the strategies, named after its strategy
	listed   = regexp.MustCompile(`\('([a-z0-9]+)', '([a-z]+)', 'localhost:\d+', \d+\)`)
	strategy = regexp.MustCompile(`"strategy": "([a-z0-9]+)"`)
)

// Strategies returns the strategies of the agents of kind the seeds of the
// repository at root create, eg: "../..", sorted, each once.
func Strategies(root, kind string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "sql", "seeds", "0004_agents.sql"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, m := range single.FindAllStringSubmatch(string(data), -1) {
		if s := strategy.FindStringSubmatch(m[3]); m[2] == kind && s != nil {
			seen[s[1]] = true
		}
	}
	for _, m := range listed.FindAllStringSubmatch(string(data), -1) {
		if m[2] == kind {
			seen[m[1]] = true
		}
	}
	list := make([]string, 0, len(seen))
	for name := range seen {
		list = append(list, name)
	}
	sort.Strings(list)
	return list, nil
}
