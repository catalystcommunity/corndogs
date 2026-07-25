package clustering

import (
	"path/filepath"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/stretchr/testify/require"
)

// liveNode is one cluster member running a real TCP StreamCarrier transport +
// engine, for an end-to-end networked test over real sockets.
type liveNode struct {
	id     string
	store  *filestore.BoltStore
	engine *Engine
	tr     *TCPTransport
}

// TestLiveClusterOverTCP stands up a real 3-node cluster on localhost TCP (the
// CSIL StreamCarrier peer transport, no HTTP) with real tickers, and checks the
// whole stack end-to-end: a leader is elected, a write on the leader commits to the
// semi-sync quorum and replicates to followers, a write on a follower is refused
// with ErrNotLeader, and killing the leader fails over to a survivor that accepts
// writes.
func TestLiveClusterOverTCP(t *testing.T) {
	if testing.Short() {
		t.Skip("live networked cluster test; skipped in -short")
	}
	dir := t.TempDir()
	ids := []string{"n1", "n2", "n3"}
	// Cluster TCP addresses on distinct localhost ports.
	addr := map[string]string{"n1": "127.0.0.1:57091", "n2": "127.0.0.1:57092", "n3": "127.0.0.1:57093"}
	nodes := map[string]*liveNode{}

	for i, id := range ids {
		st := filestore.NewBoltStore(filestore.Config{Backend: "bolt", DataDir: filepath.Join(dir, id), AuditDir: filepath.Join(dir, id), Sync: filestore.SyncNever})
		cleanup, err := st.Initialize()
		require.NoError(t, err)
		t.Cleanup(cleanup)

		s := Settings{
			Enabled: true, NodeID: id, Peers: ids, PeerAddr: addr, AckCount: -1,
			Listen: addr[id], RPCAdvertise: "http://rpc/" + id, Election: mustDefaultElection(),
		}
		tr := NewTCPTransport(id, s.Listen, addr, s.RPCAdvertise)
		rep, err := Build(s, st, filepath.Join(dir, id), tr, int64(100+i))
		require.NoError(t, err)
		// Fast tick so the test runs quickly (10ms/tick → heartbeat 100ms, failover ~200ms).
		eng := NewEngine(rep, s, 10*time.Millisecond)
		tr.Bind(eng)
		go eng.Run()
		require.NoError(t, tr.Start())
		t.Cleanup(func() { eng.Stop(); tr.Close() })
		nodes[id] = &liveNode{id: id, store: st, engine: eng, tr: tr}
	}

	// 1) A leader is elected.
	leaderID := waitTCPLeader(t, nodes, ids, 5*time.Second)
	t.Logf("leader elected: %s", leaderID)

	// 2) A write on the leader commits (semi-sync) and replicates.
	require.NoError(t, nodes[leaderID].engine.Propose(func() error {
		_, e := nodes[leaderID].store.SubmitTask(ctxBg(), &api.SubmitTaskRequest{
			Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("hello"),
		})
		return e
	}), "leader write should commit to quorum")

	require.Eventually(t, func() bool {
		for _, id := range ids {
			if nodes[id].store.ReplLSN() < 1 {
				return false
			}
		}
		return true
	}, 3*time.Second, 20*time.Millisecond, "write should replicate to all followers over TCP")

	// 3) A write on a follower is refused.
	var follower string
	for _, id := range ids {
		if id != leaderID {
			follower = id
			break
		}
	}
	require.ErrorIs(t, nodes[follower].engine.Propose(func() error { return nil }), ErrNotLeader, "follower must refuse writes")

	// 4) Kill the leader → a survivor takes over and accepts writes.
	nodes[leaderID].engine.Stop()
	nodes[leaderID].tr.Close()

	newLeader := ""
	require.Eventually(t, func() bool {
		for _, id := range ids {
			if id == leaderID {
				continue
			}
			if nodes[id].engine.IsLeader() {
				newLeader = id
				return true
			}
		}
		return false
	}, 6*time.Second, 20*time.Millisecond, "cluster should fail over to a new leader")
	t.Logf("failed over to: %s", newLeader)

	require.NoError(t, nodes[newLeader].engine.Propose(func() error {
		_, e := nodes[newLeader].store.SubmitTask(ctxBg(), &api.SubmitTaskRequest{
			Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("after-failover"),
		})
		return e
	}), "new leader should accept writes")
}

func waitTCPLeader(t *testing.T, nodes map[string]*liveNode, ids []string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		leaders := 0
		var who string
		for _, id := range ids {
			if nodes[id].engine.IsLeader() {
				leaders++
				who = id
			}
		}
		if leaders == 1 {
			return who
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no single leader elected in time")
	return ""
}
