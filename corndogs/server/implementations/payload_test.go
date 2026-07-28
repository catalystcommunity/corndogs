package implementations

import (
	"testing"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/stretchr/testify/require"
)

func TestValidatePayload(t *testing.T) {
	originalMax := config.MaxPayloadBytes
	config.MaxPayloadBytes = 4
	t.Cleanup(func() {
		config.MaxPayloadBytes = originalMax
	})

	require.NoError(t, validatePayload([]byte{0, 1, 2, 3}))
	require.Error(t, validatePayload([]byte{0, 1, 2, 3, 4}))
}
