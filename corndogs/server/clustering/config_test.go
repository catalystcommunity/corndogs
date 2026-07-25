package clustering

import (
	"testing"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/stretchr/testify/require"
)

func TestDurabilityAckCountDefault(t *testing.T) {
	// Write-majority default ⌊N/2⌋: 3-node → 1, 5-node → 2 (docs §6).
	require.Equal(t, 1, Settings{Peers: []string{"a", "b", "c"}, AckCount: -1}.DurabilityAckCount())
	require.Equal(t, 2, Settings{Peers: []string{"a", "b", "c", "d", "e"}, AckCount: -1}.DurabilityAckCount())
	// 2-node → ⌊2/2⌋ = 1 (leader + the one peer = a majority of 2).
	require.Equal(t, 1, Settings{Peers: []string{"a", "b"}, AckCount: -1}.DurabilityAckCount())
	// Async overrides to 0 (available, divergent).
	require.Equal(t, 0, Settings{Peers: []string{"a", "b", "c"}, AckCount: -1, Async: true}.DurabilityAckCount())
	// Explicit ack_count is honored.
	require.Equal(t, 0, Settings{Peers: []string{"a", "b", "c"}, AckCount: 0}.DurabilityAckCount())
}

func TestSettingsValidate(t *testing.T) {
	disabled := Settings{Enabled: false}
	require.NoError(t, disabled.Validate())

	ok := Settings{Enabled: true, NodeID: "b", Peers: []string{"a", "b", "c"}, AckCount: -1, Election: cluster.DefaultConfig()}
	require.NoError(t, ok.Validate())

	noID := ok
	noID.NodeID = ""
	require.Error(t, noID.Validate())

	notMember := ok
	notMember.NodeID = "z"
	require.Error(t, notMember.Validate())

	tooManyAcks := ok
	tooManyAcks.AckCount = 5 // only 2 followers exist
	require.Error(t, tooManyAcks.Validate())
}
