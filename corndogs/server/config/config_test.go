package config

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMaxPayloadBytes(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		valid bool
	}{
		{name: "minimum", value: 1, valid: true},
		{name: "default", value: DefaultMaxPayloadBytes, valid: true},
		{name: "hard maximum", value: HardMaxPayloadBytes, valid: true},
		{name: "zero", value: 0, valid: false},
		{name: "above hard maximum", value: HardMaxPayloadBytes + 1, valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMaxPayloadBytes(strconv.FormatInt(test.value, 10))
			if !test.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.value, got)
		})
	}

	_, err := parseMaxPayloadBytes("not-a-number")
	require.Error(t, err)
}
