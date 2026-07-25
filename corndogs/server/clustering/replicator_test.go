package clustering

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func ctxBg() context.Context { return context.Background() }

// cluster harness: real BoltStores + cluster.Nodes wired through an in-memory,
// fault-injectable frame network. This exercises the ACTUAL storage + replication
// integration deterministically, no sockets.
type replNet struct {
	t        *testing.T
	ids      []string
	reps     map[string]*Replicator
	stores   map[string]*filestore.BoltStore
	part     map[string]int
	crashed  map[string]bool
	latency  int64
	inflight []timedFrame
	now      int64
}

type timedFrame struct {
	at int64
	f  Frame
}

func (n *replNet) send(f Frame) {
	n.inflight = append(n.inflight, timedFrame{at: n.now + n.latency, f: f})
}

func newReplNet(t *testing.T, size, ackCount int) *replNet {
	t.Helper()
	cfg := cluster.DefaultConfig()
	require.NoError(t, cfg.Validate())
	n := &replNet{
		t: t, reps: map[string]*Replicator{}, stores: map[string]*filestore.BoltStore{},
		part: map[string]int{}, crashed: map[string]bool{}, latency: 1,
	}
	dir := t.TempDir()
	for i := 0; i < size; i++ {
		id := fmt.Sprintf("n%d", i+1)
		n.ids = append(n.ids, id)
	}
	sort.Strings(n.ids)
	for i, id := range n.ids {
		st := filestore.NewBoltStore(filestore.Config{Backend: "bolt", DataDir: filepath.Join(dir, id), AuditDir: filepath.Join(dir, id), Sync: filestore.SyncNever})
		cleanup, err := st.Initialize()
		require.NoError(t, err)
		t.Cleanup(cleanup)
		log, err := filestore.OpenReplLog(filepath.Join(dir, id, "repl"), 0, false)
		require.NoError(t, err)
		t.Cleanup(func() { _ = log.Close() })
		node := cluster.NewNode(id, n.ids, cfg, int64(1000+i), 0)
		node.MarkCaughtUp()
		n.stores[id] = st
		n.reps[id] = New(id, node, st, log, transportFunc(n.send), ackCount)
		n.part[id] = 0
	}
	return n
}

type transportFunc func(Frame)

func (f transportFunc) Send(fr Frame) { f(fr) }

func (n *replNet) connected(a, b string) bool { return n.part[a] == n.part[b] }

func (n *replNet) step() {
	n.now++
	var due, rest []timedFrame
	for _, tf := range n.inflight {
		if tf.at <= n.now {
			due = append(due, tf)
		} else {
			rest = append(rest, tf)
		}
	}
	n.inflight = rest
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].f.To != due[j].f.To {
			return due[i].f.To < due[j].f.To
		}
		return due[i].f.From < due[j].f.From
	})
	for _, tf := range due {
		to := tf.f.To
		if n.crashed[to] || !n.connected(tf.f.From, to) {
			continue
		}
		n.reps[to].Recv(n.now, tf.f)
	}
	for _, id := range n.ids {
		if n.crashed[id] {
			continue
		}
		n.reps[id].Tick(n.now)
	}
}

func (n *replNet) run(ticks int) {
	for i := 0; i < ticks; i++ {
		n.step()
	}
}

func (n *replNet) leader() string {
	for _, id := range n.ids {
		if !n.crashed[id] && n.reps[id].IsLeader() {
			return id
		}
	}
	return ""
}

func (n *replNet) requireLeader(maxTicks int) string {
	n.t.Helper()
	for i := 0; i < maxTicks; i++ {
		n.step()
		if l := n.leader(); l != "" {
			return l
		}
	}
	n.t.Fatalf("no leader after %d ticks", maxTicks)
	return ""
}

// submit performs a client write on the leader's store (which captures + ships a
// batch) and returns the batch's LSN.
func (n *replNet) submit(leader, payload string) uint64 {
	n.t.Helper()
	_, err := n.stores[leader].SubmitTask(ctxBg(), &api.SubmitTaskRequest{
		Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte(payload),
	})
	require.NoError(n.t, err)
	return n.stores[leader].ReplLSN()
}

func (n *replNet) requireCommitted(leader string, lsn uint64, maxTicks int) {
	n.t.Helper()
	for i := 0; i < maxTicks; i++ {
		n.step()
		if n.reps[leader].Committed(lsn) {
			return
		}
	}
	n.t.Fatalf("write LSN %d not committed within %d ticks (committed=%d)", lsn, maxTicks, n.reps[leader].CommittedLSN())
}

func (n *replNet) assertConverged(ids ...string) {
	n.t.Helper()
	if len(ids) < 2 {
		return
	}
	base := n.stores[ids[0]]
	for _, other := range ids[1:] {
		require.NoError(n.t, base.DB().View(func(btx *bolt.Tx) error {
			return n.stores[other].DB().View(func(otx *bolt.Tx) error {
				for _, bkt := range [][]byte{[]byte("tasks"), []byte("uuid"), []byte("archived")} {
					assertBucketEqual(n.t, bkt, btx.Bucket(bkt), otx.Bucket(bkt), ids[0], other)
				}
				return nil
			})
		}))
	}
}

func assertBucketEqual(t *testing.T, name []byte, a, b *bolt.Bucket, ida, idb string) {
	t.Helper()
	if a == nil && b == nil {
		return
	}
	require.NotNil(t, a)
	require.NotNil(t, b)
	ac, bc := a.Cursor(), b.Cursor()
	ak, av := ac.First()
	bk, bv := bc.First()
	for ak != nil || bk != nil {
		require.True(t, bytes.Equal(ak, bk), "%s vs %s bucket %s key mismatch %x != %x", ida, idb, name, ak, bk)
		require.True(t, bytes.Equal(av, bv), "%s vs %s bucket %s value mismatch at %x", ida, idb, name, ak)
		ak, av = ac.Next()
		bk, bv = bc.Next()
	}
}

// TestReplicationAndSemiSync: a 3-node cluster with write-majority ack_count (1)
// replicates writes to followers and commits them (semi-sync); all stores
// converge byte-for-byte.
func TestReplicationAndSemiSync(t *testing.T) {
	n := newReplNet(t, 3, 1)
	leader := n.requireLeader(60)
	var last uint64
	for i := 0; i < 30; i++ {
		last = n.submit(leader, fmt.Sprintf("w%d", i))
	}
	n.requireCommitted(leader, last, 120)
	n.run(60) // let stragglers apply
	n.assertConverged(n.ids...)
}

// TestMinorityBlocks: with write-majority ack_count, a leader partitioned away
// from its followers cannot commit new writes (the minority blocks), while the
// majority side elects a new leader whose writes DO commit. This is the split-brain
// guard from the durability requirement (docs §6).
func TestMinorityBlocks(t *testing.T) {
	n := newReplNet(t, 3, 1)
	old := n.requireLeader(60)

	// Isolate the old leader alone.
	for _, id := range n.ids {
		if id == old {
			n.part[id] = 1
		} else {
			n.part[id] = 0
		}
	}
	// The isolated old leader accepts a client write locally but must NOT be able to
	// commit it (no follower acks reachable).
	blocked := n.submit(old, "isolated-write")
	n.run(200)
	require.False(t, n.reps[old].Committed(blocked), "isolated leader must not commit without a follower quorum")

	// The majority side elects a new leader whose writes commit.
	var neu string
	for _, id := range n.ids {
		if id != old && n.reps[id].IsLeader() {
			neu = id
		}
	}
	require.NotEmpty(t, neu, "majority side should have a leader")
	lsn := n.submit(neu, "majority-write")
	n.requireCommitted(neu, lsn, 120)
}

// TestRejoinRollbackConverges: after the minority leader rejoins, it rolls back its
// uncommitted local write (via a snapshot from the winner) and the whole cluster
// converges byte-for-byte.
func TestRejoinRollbackConverges(t *testing.T) {
	n := newReplNet(t, 3, 1)
	old := n.requireLeader(60)
	for _, id := range n.ids {
		if id == old {
			n.part[id] = 1
		} else {
			n.part[id] = 0
		}
	}
	n.submit(old, "loser-only") // uncommitted divergent write on the isolated leader
	n.run(150)
	var neu string
	for _, id := range n.ids {
		if id != old && n.reps[id].IsLeader() {
			neu = id
		}
	}
	require.NotEmpty(t, neu)
	for i := 0; i < 10; i++ {
		lsn := n.submit(neu, fmt.Sprintf("win%d", i))
		n.requireCommitted(neu, lsn, 120)
	}

	// Heal. The old leader steps down to the higher epoch; it then rolls back and
	// re-syncs from the new leader.
	for _, id := range n.ids {
		n.part[id] = 0
	}
	n.run(120)                    // let the old leader hear the new leader and step down
	n.reps[old].RequestRollback() // ex-leader re-bases on the winner's snapshot
	n.run(120)

	n.assertConverged(n.ids...)
	// The rolled-back divergent write is gone everywhere.
	for _, id := range n.ids {
		require.NoError(t, n.stores[id].DB().View(func(tx *bolt.Tx) error {
			return tx.Bucket([]byte("tasks")).ForEach(func(_, v []byte) error {
				require.NotContains(t, string(v), "loser-only")
				return nil
			})
		}))
	}
}
