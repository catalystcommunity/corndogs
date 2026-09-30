package clustering

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/stretchr/testify/require"
)

// restartCluster runs real nodes over localhost TCP on fixed data directories, so
// a test can stop every node and start it again on its existing storage.
type restartCluster struct {
	t     *testing.T
	dir   string
	ids   []string
	addr  map[string]string
	nodes map[string]*liveNode
	stops map[string]func()
}

func newRestartCluster(t *testing.T, basePort int) *restartCluster {
	c := &restartCluster{
		t: t, dir: t.TempDir(), ids: []string{"n1", "n2", "n3"},
		addr:  map[string]string{},
		nodes: map[string]*liveNode{}, stops: map[string]func(){},
	}
	for i, id := range c.ids {
		c.addr[id] = fmt.Sprintf("127.0.0.1:%d", basePort+i)
	}
	t.Cleanup(func() {
		for _, id := range c.ids {
			c.kill(id)
		}
	})
	return c
}

// start opens the node's existing storage (or creates it) and joins the cluster.
func (c *restartCluster) start(id string) {
	t := c.t
	st := filestore.NewBoltStore(filestore.Config{Backend: "bolt", DataDir: filepath.Join(c.dir, id), AuditDir: filepath.Join(c.dir, id), Sync: filestore.SyncAlways})
	closeStore, err := st.Initialize()
	require.NoError(t, err)
	s := Settings{
		Enabled: true, NodeID: id, Peers: c.ids, PeerAddr: c.addr, AckCount: -1,
		Listen: c.addr[id], RPCAdvertise: "rpc-" + id, Election: mustDefaultElection(),
	}
	tr := NewTCPTransport(id, s.Listen, c.addr, s.RPCAdvertise)
	rep, err := Build(s, st, filepath.Join(c.dir, id), tr, seedFromID(id))
	require.NoError(t, err)
	eng := NewEngine(rep, s, 10*time.Millisecond)
	tr.Bind(eng)
	go eng.Run()
	require.Eventually(t, func() bool { return tr.Start() == nil }, 5*time.Second, 50*time.Millisecond, "listen %s", id)
	c.nodes[id] = &liveNode{id: id, store: st, engine: eng, tr: tr}
	// The replication log is not closed: like a killed process, the node leaves
	// only what already reached the operating system.
	c.stops[id] = func() {
		eng.Stop()
		tr.Close()
		closeStore()
	}
}

// kill stops the node without a graceful replication shutdown.
func (c *restartCluster) kill(id string) {
	if stop := c.stops[id]; stop != nil {
		stop()
		delete(c.stops, id)
		delete(c.nodes, id)
	}
}

func (c *restartCluster) running() []string {
	var out []string
	for _, id := range c.ids {
		if c.nodes[id] != nil {
			out = append(out, id)
		}
	}
	return out
}

func (c *restartCluster) leader() string {
	return waitTCPLeader(c.t, c.nodes, c.running(), 10*time.Second)
}

func (c *restartCluster) submit(queue, payload string) (api.Task, error) {
	id := c.leader()
	var out *api.SubmitTaskResponse
	err := c.nodes[id].engine.Propose(func() (e error) {
		out, e = c.nodes[id].store.SubmitTask(ctxBg(), &api.SubmitTaskRequest{
			Queue: queue, CurrentState: "ready", AutoTargetState: "working", Timeout: -1, Payload: []byte(payload), Priority: 7,
		})
		return
	})
	if err != nil {
		return api.Task{}, err
	}
	return *out.Task, nil
}

func (c *restartCluster) claim(queue string) (*api.TaskDelivery, error) {
	id := c.leader()
	var out *api.GetNextTaskResponse
	err := c.nodes[id].engine.Propose(func() (e error) {
		out, e = c.nodes[id].store.GetNextTask(ctxBg(), &api.GetNextTaskRequest{
			Queue: queue, CurrentState: "ready", OverrideCurrentState: "working", OverrideAutoTargetState: "ready", OverrideTimeout: 2,
		})
		return
	})
	if err != nil {
		return nil, err
	}
	return out.Delivery, nil
}

func (c *restartCluster) state(node, uuid string) *api.Task {
	resp, err := c.nodes[node].store.MustGetTaskStateByID(ctxBg(), &api.GetTaskStateByIDRequest{Uuid: uuid})
	require.NoError(c.t, err)
	return resp.Task
}

// converged waits until every running node holds the same position and the same
// state for each task.
func (c *restartCluster) converged(uuids []string) {
	c.t.Helper()
	same := func() bool {
		ids := c.running()
		lsn := c.nodes[ids[0]].store.ReplLSN()
		for _, id := range ids[1:] {
			if c.nodes[id].store.ReplLSN() != lsn {
				return false
			}
		}
		for _, u := range uuids {
			want := c.state(ids[0], u)
			for _, id := range ids[1:] {
				got := c.state(id, u)
				if (want == nil) != (got == nil) || (want != nil && *want != *got) {
					return false
				}
			}
		}
		return true
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if same() {
			return
		}
	}
	diff := ""
	ids := c.running()
	for _, u := range uuids {
		for _, id := range ids {
			diff += fmt.Sprintf("%s@%s=%+v ", u[:8], id, c.state(id, u))
		}
		diff += "\n"
	}
	c.t.Fatalf("nodes did not converge: %s\n%s", c.describe(), diff)
}

func (c *restartCluster) describe() string {
	if f, err := os.Create(filepath.Join(os.TempDir(), "r4-goroutines.txt")); err == nil {
		_ = pprof.Lookup("goroutine").WriteTo(f, 1)
		f.Close()
	}
	out := ""
	for _, id := range c.running() {
		st := c.nodes[id].engine.Stats()
		out += fmt.Sprintf("[%s role=%s epoch=%d leader=%s lsn=%d pos=%+v] ", id, st.Role, st.Epoch, st.LeaderID, c.nodes[id].store.ReplLSN(), c.nodes[id].engine.rep.Node().Position())
	}
	return out
}

// TestFullClusterRestartRecovers is the regression test for the replicated-file
// full restart failure (resilience-recovery.md R4). Every node stops without a
// graceful replication shutdown, then all restart on their existing storage. The
// cluster must keep every acknowledged state and payload, claim retained work,
// fail over, rejoin a stopped node, and survive the loss of its write majority.
func TestFullClusterRestartRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("live networked cluster test; skipped in -short")
	}
	c := newRestartCluster(t, 57191)
	for _, id := range c.ids {
		c.start(id)
	}
	c.leader()

	// 1. Acknowledged work before the crash: 24 tasks, each in its own queue.
	tasks := make([]api.Task, 24)
	for i := range tasks {
		var err error
		tasks[i], err = c.submit(fmt.Sprintf("restart-%d", i), fmt.Sprintf("payload:restart-%d", i))
		require.NoError(t, err)
	}
	l := c.leader()
	require.NoError(t, c.nodes[l].engine.Propose(func() error {
		_, e := c.nodes[l].store.CompleteTask(ctxBg(), &api.CompleteTaskRequest{Uuid: tasks[0].Uuid})
		return e
	}))
	require.NoError(t, c.nodes[l].engine.Propose(func() error {
		_, e := c.nodes[l].store.CancelTask(ctxBg(), &api.CancelTaskRequest{Uuid: tasks[1].Uuid})
		return e
	}))
	d, err := c.claim("restart-2")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.NoError(t, c.nodes[l].engine.Propose(func() error {
		_, e := c.nodes[l].store.UpdateTask(ctxBg(), &api.UpdateTaskRequest{Uuid: tasks[3].Uuid, NewState: "waiting_human", AutoTargetState: "ready"})
		return e
	}))
	uuids := make([]string, len(tasks))
	for i, task := range tasks {
		uuids[i] = task.Uuid
	}
	c.converged(uuids)
	before := map[string]api.Task{}
	for _, u := range uuids {
		before[u] = *c.state(l, u)
	}

	// 2. Stop every node, then restart all of them on their existing storage.
	for _, id := range c.ids {
		c.kill(id)
	}
	for _, id := range c.ids {
		c.start(id)
	}
	l = c.leader()
	for _, id := range c.ids {
		for _, u := range uuids {
			got := c.state(id, u)
			require.NotNil(t, got, "node %s lost task %s", id, u)
			require.Equal(t, before[u], *got, "node %s task %s", id, u)
		}
	}

	// 3. Claim every retained ready task with its exact payload.
	for i := 4; i < len(tasks); i++ {
		d, err := c.claim(fmt.Sprintf("restart-%d", i))
		require.NoError(t, err, "claim retained task %d", i)
		require.NotNil(t, d, "retained task %d", i)
		require.Equal(t, tasks[i].Uuid, d.Task.Uuid)
		require.Equal(t, fmt.Sprintf("payload:restart-%d", i), string(d.Payload))
	}
	c.converged(uuids)

	// 4. Continue writing, stop the leader, and continue on the new leader.
	for i := 0; i < 5; i++ {
		task, err := c.submit("after-restart", fmt.Sprintf("after-%d", i))
		require.NoError(t, err)
		uuids = append(uuids, task.Uuid)
	}
	stopped := c.leader()
	c.kill(stopped)
	newLeader := c.leader()
	require.NotEqual(t, stopped, newLeader)
	task, err := c.submit("after-failover", "after-failover")
	require.NoError(t, err, "the new leader must commit a write")
	uuids = append(uuids, task.Uuid)
	d, err = c.claim("after-restart")
	require.NoError(t, err)
	require.Equal(t, "after-0", string(d.Payload))

	// 5. Rejoin the stopped node; it must converge to the same data.
	c.start(stopped)
	c.converged(uuids)

	// 6. Lose the write majority, then restore it. A write without a quorum has
	// an uncertain outcome; after the followers return, all nodes must agree.
	l = c.leader()
	var followers []string
	for _, id := range c.ids {
		if id != l {
			followers = append(followers, id)
		}
	}
	for _, id := range followers {
		c.kill(id)
	}
	_, err = c.submit("no-quorum", "no-quorum")
	require.Error(t, err, "a leader without a quorum must not acknowledge a write")
	for _, id := range followers {
		c.start(id)
	}
	// The leader rejects writes (without applying them) until the returning
	// followers are live again, so the caller can retry the same request.
	require.Eventually(t, func() bool {
		task, err = c.submit("quorum-restored", "quorum-restored")
		require.True(t, err == nil || errors.Is(err, ErrNoQuorum), "unexpected error: %v", err)
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "the restored quorum must accept a write")
	uuids = append(uuids, task.Uuid)
	c.converged(uuids)
	for _, id := range c.ids {
		resp, err := c.nodes[id].store.GetQueueAndStateCounts(ctxBg(), &api.GetQueueAndStateCountsRequest{})
		require.NoError(t, err)
		first, err := c.nodes[c.ids[0]].store.GetQueueAndStateCounts(ctxBg(), &api.GetQueueAndStateCountsRequest{})
		require.NoError(t, err)
		require.Equal(t, first.QueueAndStateCounts, resp.QueueAndStateCounts, "node %s counts", id)
	}
}

// TestUnknownPositionRequiresSnapshot covers a follower from a release that did
// not record its replication position: its data has tasks, but its log is empty.
// It must not start at LSN 0 (a catch-up from LSN 0 would replay old batches
// over newer data), and it must not stand for election before a snapshot.
func TestUnknownPositionRequiresSnapshot(t *testing.T) {
	dir := t.TempDir()
	st := filestore.NewBoltStore(filestore.Config{Backend: "bolt", DataDir: dir, AuditDir: dir, Sync: filestore.SyncNever})
	cleanup, err := st.Initialize()
	require.NoError(t, err)
	t.Cleanup(cleanup)
	_, err = st.SubmitTask(ctxBg(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "ready", AutoTargetState: "working", Payload: []byte("x")})
	require.NoError(t, err)

	log, err := filestore.OpenReplLog(filepath.Join(dir, "repl"), 0, false)
	require.NoError(t, err)
	node := cluster.NewNode("n1", []string{"n1", "n2"}, mustDefaultElection(), 1, 0)
	node.MarkCaughtUp()
	rep := New("n1", node, st, log, transportFunc(func(Frame) {}), 1)
	require.True(t, rep.needSnapshot)
	require.Equal(t, cluster.RoleJoining, node.Role())
	for now := int64(1); now < 200; now++ {
		rep.Tick(now)
	}
	require.NotEqual(t, cluster.RoleLeader, node.Role(), "a node with unknown data must not lead")
}

// TestRecoveredPositionPrefersNewerHistory checks the start position: a follower
// apply records its position in the database, and a leader's appends are in its
// log. The newer of the two describes the data.
func TestRecoveredPositionPrefersNewerHistory(t *testing.T) {
	dir := t.TempDir()
	st := filestore.NewBoltStore(filestore.Config{Backend: "bolt", DataDir: dir, AuditDir: dir, Sync: filestore.SyncNever})
	cleanup, err := st.Initialize()
	require.NoError(t, err)
	t.Cleanup(cleanup)
	tagA := cluster.HistoryTag(3, 1)
	tagB := cluster.HistoryTag(4, 2)
	// The log ends at (3/n1, 10); the database was later rolled back to (4/n2, 8).
	log, err := filestore.OpenReplLog(filepath.Join(dir, "repl"), 0, false)
	require.NoError(t, err)
	require.NoError(t, log.Reset(10, tagA))
	require.NoError(t, filestore.StampReplPosition(st.DB(), 8, tagB))

	node := cluster.NewNode("n1", []string{"n1", "n2"}, mustDefaultElection(), 1, 0)
	node.MarkCaughtUp()
	rep := New("n1", node, st, log, transportFunc(func(Frame) {}), 1)
	require.False(t, rep.needSnapshot)
	require.Equal(t, cluster.Position{LastEpoch: tagB, LSN: 8}, node.Position())
	require.Equal(t, uint64(8), st.ReplLSN())
	require.Equal(t, uint64(8), log.LastLSN(), "the log restarts at the recovered position")
	require.Equal(t, uint64(4), node.Epoch(), "a new election must use a higher epoch than the data")
}
