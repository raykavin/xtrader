// Package flaglist reads the flags of a list the agents share.
package flaglist

import (
	"fmt"
	"strings"
)

// List splits a flag of a list, its items separated by ";" (or "," as before),
// dropping the blanks.
func List(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' }) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// Sizes splits the flag name of a list of sizes as List does, refusing a comma
// that may be a decimal one: 1,5 may be 1.5 or the sizes 1 and 5, a position 5
// times another. A comma between two numbers separates them only when both have
// a decimal point, the format of before, eg: 0.03,0.01 or
// BTCUSDm=0.03,XAUUSDm=0.01.
func Sizes(name, value string) ([]string, error) {
	for _, piece := range strings.Split(value, ";") {
		fields := strings.Split(piece, ",")
		for i := 1; i < len(fields); i++ {
			left, right := strings.TrimSpace(fields[i-1]), strings.TrimSpace(fields[i])
			if endsInDigit(left) && startsWithDigit(right) &&
				(!strings.Contains(sizeOf(left), ".") || !strings.Contains(sizeOf(right), ".")) {
				return nil, fmt.Errorf("%s: %q is ambiguous, the comma may be a decimal one: separate the sizes "+
					"with ; and write the decimals with ., eg: 1.5 for one size or 1;5 for one per pair", name,
					strings.TrimSpace(piece))
			}
		}
	}
	return List(value), nil
}

// sizeOf returns the size of an item, the part after = of PAIR=SIZE.
func sizeOf(item string) string {
	if _, size, ok := strings.Cut(item, "="); ok {
		return strings.TrimSpace(size)
	}
	return item
}

func endsInDigit(s string) bool {
	return s != "" && s[len(s)-1] >= '0' && s[len(s)-1] <= '9'
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}
