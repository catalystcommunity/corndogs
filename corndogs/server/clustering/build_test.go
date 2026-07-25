package clustering

import (
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/stretchr/testify/require"
)

// TestBuildSingleNode: the config→runtime path constructs a working single-node
// cluster that elects itself and commits a write (ack_count ⌊1/2⌋ = 0).
func TestBuildSingleNode(t *testing.T) {
	dir := t.TempDir()
	st := filestore.NewBoltStore(filestore.Config{Backend: "bolt", DataDir: dir, AuditDir: dir, Sync: filestore.SyncNever})
	cleanup, err := st.Initialize()
	require.NoError(t, err)
	defer cleanup()

	s := Settings{Enabled: true, NodeID: "solo", Peers: []string{"solo"}, AckCount: -1}
	s.Election = mustDefaultElection()

	var out []Frame
	rep, err := Build(s, st, dir, transportFunc(func(f Frame) { out = append(out, f) }), 1)
	require.NoError(t, err)

	// Drive ticks until it leads.
	now := int64(0)
	for i := 0; i < 30 && !rep.IsLeader(); i++ {
		now++
		rep.Tick(now)
	}
	require.True(t, rep.IsLeader(), "single node should elect itself")

	_, err = st.SubmitTask(ctxBg(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("x")})
	require.NoError(t, err)
	lsn := st.ReplLSN()
	now++
	rep.Tick(now)
	require.True(t, rep.Committed(lsn), "single-node write commits immediately (ack_count 0)")
}
