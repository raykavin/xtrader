package flaglist

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestList(t *testing.T) {
	require.Equal(t, []string{"BTCUSDm", "XAUUSDm"}, List("BTCUSDm;XAUUSDm"))
	require.Equal(t, []string{"BTCUSDm", "XAUUSDm"}, List(" BTCUSDm , XAUUSDm "), "the comma of before")
	require.Equal(t, []string{"a", "b", "c"}, List("a;b,c;;"), "both, the blanks dropped")
	require.Nil(t, List(""))
}

func TestSizes_DecimalCommaRefused(t *testing.T) {
	for _, value := range []string{
		"1,5", "0,05", " 0, 05 ", "1,5;2", "0.5;1,5", "1.000,5", "0.03,1", "1,2",
		"XAUUSDm=1,5", "XAUUSDm=0,05;BTCUSDm=1", "BTCUSDm=0.03,XAUUSDm=0,01",
	} {
		sizes, err := Sizes("-lot", value)
		require.ErrorContains(t, err, "-lot: ", value)
		require.ErrorContains(t, err, "is ambiguous, the comma may be a decimal one: separate the sizes with ; "+
			"and write the decimals with .", value)
		require.Nil(t, sizes, value)
	}
}

func TestSizes_Unambiguous(t *testing.T) {
	for value, want := range map[string][]string{
		"":                           nil,
		"0.05":                       {"0.05"},
		"5":                          {"5"},
		"0.03;0.01":                  {"0.03", "0.01"},
		"1;5":                        {"1", "5"},
		"0.03,0.01":                  {"0.03", "0.01"},
		" 0.03 , 0.01 ":              {"0.03", "0.01"},
		"BTCUSDm=0.03;XAUUSDm=0.01":  {"BTCUSDm=0.03", "XAUUSDm=0.01"},
		"BTCUSDm=0.03,XAUUSDm=0.01":  {"BTCUSDm=0.03", "XAUUSDm=0.01"},
		"BTCUSDm=1,XAUUSDm=2":        {"BTCUSDm=1", "XAUUSDm=2"},
		"100;1":                      {"100", "1"},
		"BTCUSDm = 1 ; XAUUSDm = 5;": {"BTCUSDm = 1", "XAUUSDm = 5"},
	} {
		sizes, err := Sizes("-lot", value)
		require.NoError(t, err, value)
		require.Equal(t, want, sizes, value)
	}
}
