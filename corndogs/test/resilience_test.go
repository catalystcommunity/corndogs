package test

// Integration tests of the resilience contract (resilience-recovery.md, A01 to
// A11). They use the released Go client library over real CSIL-RPC TCP
// connections, so they run unchanged against every storage configuration.
//
//	CORNDOGS_TEST_ADDR   server address (default 127.0.0.1:5080)
//	CORNDOGS_TEST_SEEDS  comma-separated cluster seeds; enables cluster checks
//
// The policy tests read the server policy with GetServerInfo and check the
// rules of that policy. Run the suite once against a server in compatibility
// mode and once against a server in required mode.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
)

func testSeeds() []string {
	if s := os.Getenv("CORNDOGS_TEST_SEEDS"); s != "" {
		return strings.Split(s, ",")
	}
	return nil
}

// newLibClient returns the library client: the cluster transport when seeds
// are set, else one stream transport.
func newLibClient(t *testing.T) *api.CorndogsClient {
	t.Helper()
	if seeds := testSeeds(); len(seeds) > 0 {
		return api.NewCluster(seeds...)
	}
	return api.New(testAddr())
}

// proxyTarget returns the address that a fault proxy fronts: the server, or
// the current cluster leader. Only the leader answers LookupOperation.
func proxyTarget(t *testing.T) string {
	t.Helper()
	seeds := testSeeds()
	if len(seeds) == 0 {
		return testAddr()
	}
	for _, s := range seeds {
		one := api.NewCorndogsClient(&api.StreamTransport{Addr: s, MaxAttempts: 1})
		if _, err := one.LookupOperation(rctx(t), api.LookupOperationRequest{OperationId: "leader-probe"}); err == nil {
			return s
		}
	}
	t.Fatal("no cluster leader answered")
	return ""
}

func rctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func uniqueQueue(t *testing.T) string {
	return fmt.Sprintf("res-%s-%s", strings.ReplaceAll(t.Name(), "/", "-"), api.NewOperationID()[:8])
}

func serverInfo(t *testing.T, c *api.CorndogsClient) api.GetServerInfoResponse {
	t.Helper()
	info, err := c.RequireFeatures(rctx(t), api.FeatureSubmissionKeys, api.FeatureTaskGuards)
	require.NoError(t, err)
	return info
}

func requireCode(t *testing.T, err error, code uint64) {
	t.Helper()
	require.Error(t, err)
	got, ok := api.ServiceErrorCode(err)
	require.True(t, ok, "want ServiceError %d, got %v", code, err)
	require.Equal(t, code, got, "error: %v", err)
}

func keyed(queue, key string, payload []byte) api.SubmitKeyedTaskRequest {
	return api.SubmitKeyedTaskRequest{
		SubmissionKey: key, Guarded: true, Queue: queue,
		CurrentState: "submitted", AutoTargetState: "submitted-working",
		Timeout: -1, Payload: payload, Priority: 3,
	}
}

func claim(queue string) api.ClaimGuardedTaskRequest {
	return api.ClaimGuardedTaskRequest{OperationId: api.NewOperationID(), Queue: queue, CurrentState: "submitted"}
}

// drain claims every task in queue with guarded claims and returns their uuids.
func drain(t *testing.T, c *api.CorndogsClient, queue, state string) []string {
	t.Helper()
	var ids []string
	for i := 0; i < 1000; i++ {
		r, err := c.ClaimGuardedTask(rctx(t), api.ClaimGuardedTaskRequest{OperationId: api.NewOperationID(), Queue: queue, CurrentState: state})
		require.NoError(t, err)
		if r.Delivery == nil {
			return ids
		}
		ids = append(ids, r.Delivery.Task.Task.Uuid)
	}
	t.Fatal("drain did not finish")
	return nil
}

func TestResilienceServerInfo(t *testing.T) {
	c := newLibClient(t)
	info := serverInfo(t, c)
	t.Logf("server %s: submission keys %s, task guards %s", info.ServerVersion, info.SubmissionKeyPolicy, info.TaskGuardPolicy)
	require.Contains(t, []string{api.PolicyCompatibility, api.PolicyRequired}, info.SubmissionKeyPolicy)
	require.Contains(t, []string{api.PolicyCompatibility, api.PolicyRequired}, info.TaskGuardPolicy)
	require.GreaterOrEqual(t, info.ReceiptRetentionSeconds, int64(60))
}

// A01: the same keyed submission, sent sequentially and concurrently, creates
// one task.
func TestResilienceKeyedSubmitOnce(t *testing.T) {
	c := newLibClient(t)
	q := uniqueQueue(t)
	key := api.NewSubmissionKey()
	first, err := c.SubmitKeyedTask(rctx(t), keyed(q, key, []byte("p1")))
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.NotNil(t, first.Task)
	require.True(t, first.Task.Guarded)
	require.Equal(t, int64(1), first.Task.Revision)

	again, err := c.SubmitKeyedTask(rctx(t), keyed(q, key, []byte("p1")))
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, first.Receipt, again.Receipt)

	key2 := api.NewSubmissionKey()
	var wg sync.WaitGroup
	ids := make([]string, 16)
	errs := make([]error, 16)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cl := newLibClient(t)
			// CORNDOGS_TEST_ALT_ADDR sends half of the requests to a second
			// server that shares the same database.
			if alt := os.Getenv("CORNDOGS_TEST_ALT_ADDR"); alt != "" && i%2 == 1 {
				cl = api.New(alt)
			}
			r, err := cl.SubmitKeyedTask(rctx(t), keyed(q, key2, []byte("p2")))
			errs[i] = err
			if err == nil {
				ids[i] = r.Receipt.TaskUuid
			}
		}(i)
	}
	wg.Wait()
	for i := range ids {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i])
	}
	require.Len(t, drain(t, c, q, "submitted"), 2)
}

// A02: the same key with a different request is a conflict and changes
// nothing.
func TestResilienceKeyConflict(t *testing.T) {
	c := newLibClient(t)
	q := uniqueQueue(t)
	key := api.NewSubmissionKey()
	_, err := c.SubmitKeyedTask(rctx(t), keyed(q, key, []byte("a")))
	require.NoError(t, err)
	for _, change := range []func(*api.SubmitKeyedTaskRequest){
		func(r *api.SubmitKeyedTaskRequest) { r.Payload = []byte("b") },
		func(r *api.SubmitKeyedTaskRequest) { r.Priority = 4 },
		func(r *api.SubmitKeyedTaskRequest) { r.Timeout = 0 },
		func(r *api.SubmitKeyedTaskRequest) { r.CurrentState = "" },
		func(r *api.SubmitKeyedTaskRequest) { r.Payload = []byte{} },
	} {
		req := keyed(q, key, []byte("a"))
		change(&req)
		_, err := c.SubmitKeyedTask(rctx(t), req)
		requireCode(t, err, api.CodeSubmissionKeyConflict)
	}
	require.Len(t, drain(t, c, q, "submitted"), 1)
}

// A03: a keyed reply lost after commit. The transport replays the keyed
// request and returns the original identity; an explicit retry does too.
func TestResilienceKeyedLostReply(t *testing.T) {
	c := newLibClient(t)
	p := startFaultProxy(t, proxyTarget(t))
	q := uniqueQueue(t)

	auto := api.New(p.Addr())
	p.DropNextReply()
	r, err := auto.SubmitKeyedTask(rctx(t), keyed(q, "auto-key", []byte("x")))
	require.NoError(t, err)
	require.Equal(t, 1, p.Dropped())
	require.True(t, r.Replayed, "the transport replay must return the stored receipt")

	once := api.NewCorndogsClient(&api.StreamTransport{Addr: p.Addr(), MaxAttempts: 1})
	p.DropNextReply()
	_, err = once.SubmitKeyedTask(rctx(t), keyed(q, "manual-key", []byte("y")))
	require.True(t, api.IsUncertain(err), "got %v", err)
	r2, err := once.SubmitKeyedTask(rctx(t), keyed(q, "manual-key", []byte("y")))
	require.NoError(t, err)
	require.True(t, r2.Replayed)

	look, err := c.LookupSubmission(rctx(t), api.LookupSubmissionRequest{Queue: q, SubmissionKey: "manual-key"})
	require.NoError(t, err)
	require.Equal(t, r2.Receipt.TaskUuid, look.Receipt.TaskUuid)

	// The cluster-aware transport replays the keyed request too.
	cluster := api.NewCluster(p.Addr())
	p.DropNextReply()
	r3, err := cluster.SubmitKeyedTask(rctx(t), keyed(q, "cluster-key", []byte("z")))
	require.NoError(t, err)
	require.True(t, r3.Replayed)
	require.Equal(t, 3, p.Dropped())
	require.Len(t, drain(t, c, q, "submitted"), 3)
}

// R3: a legacy mutation whose reply is lost returns an uncertain outcome and
// is not replayed. A04: keyless identical submissions stay distinct tasks.
func TestResilienceLegacyLostReplyIsUncertain(t *testing.T) {
	c := newLibClient(t)
	if serverInfo(t, c).SubmissionKeyPolicy == api.PolicyRequired || serverInfo(t, c).TaskGuardPolicy == api.PolicyRequired {
		t.Skip("legacy submissions are rejected in required mode")
	}
	p := startFaultProxy(t, proxyTarget(t))
	q := uniqueQueue(t)
	legacy := api.New(p.Addr())
	sub := api.SubmitTaskRequest{Queue: q, CurrentState: "submitted", Timeout: -1, Payload: []byte("same")}
	p.DropNextReply()
	_, err := legacy.SubmitTask(rctx(t), sub)
	require.True(t, api.IsUncertain(err), "got %v", err)
	_, err = legacy.SubmitTask(rctx(t), sub)
	require.NoError(t, err)
	// The application retry above created a second task; the transport did not.
	require.Len(t, drain(t, c, q, "submitted"), 2)

	p.DropNextReply()
	_, err = legacy.GetNextTask(rctx(t), api.GetNextTaskRequest{Queue: q, CurrentState: "submitted-working"})
	require.True(t, api.IsUncertain(err), "a lost legacy claim reply must not claim another task; got %v", err)

	// The Opal case: one application call through the cluster-aware transport
	// must not create two tasks.
	cluster := api.NewCluster(p.Addr())
	p.DropNextReply()
	_, err = cluster.SubmitTask(rctx(t), api.SubmitTaskRequest{Queue: q, CurrentState: "cluster", Timeout: -1, Payload: []byte("c")})
	require.True(t, api.IsUncertain(err), "got %v", err)
	require.Len(t, drain(t, c, q, "cluster"), 1)
}

// A05: a server in required mode rejects keyless submission before storage.
func TestResilienceRequiredModeRejects(t *testing.T) {
	c := newLibClient(t)
	info := serverInfo(t, c)
	q := uniqueQueue(t)
	if info.SubmissionKeyPolicy == api.PolicyRequired {
		_, err := c.SubmitTask(rctx(t), api.SubmitTaskRequest{Queue: q, Timeout: -1, Payload: []byte("x")})
		requireCode(t, err, api.CodeSubmissionKeyRequired)
	}
	if info.TaskGuardPolicy == api.PolicyRequired {
		_, err := c.GetNextTask(rctx(t), api.GetNextTaskRequest{Queue: q})
		requireCode(t, err, api.CodeTaskGuardRequired)
		req := keyed(q, api.NewSubmissionKey(), []byte("x"))
		req.Guarded = false
		_, err = c.SubmitKeyedTask(rctx(t), req)
		requireCode(t, err, api.CodeTaskGuardRequired)
	}
	if info.SubmissionKeyPolicy != api.PolicyRequired && info.TaskGuardPolicy != api.PolicyRequired {
		t.Skip("server is in compatibility mode")
	}
	counts, err := c.GetTaskStateCounts(rctx(t), api.GetTaskStateCountsRequest{Queue: q})
	require.NoError(t, err)
	require.Zero(t, counts.Count)
}

// A06: a retry after completion or cancellation returns the retained identity
// and does not make the task ready again.
func TestResilienceKeyAfterTerminal(t *testing.T) {
	c := newLibClient(t)
	q := uniqueQueue(t)
	for _, op := range []string{"complete", "cancel"} {
		key := api.NewSubmissionKey()
		s, err := c.SubmitKeyedTask(rctx(t), keyed(q, key, []byte(op)))
		require.NoError(t, err)
		cl, err := c.ClaimGuardedTask(rctx(t), claim(q))
		require.NoError(t, err)
		require.NotNil(t, cl.Delivery)
		require.Equal(t, s.Receipt.TaskUuid, cl.Delivery.Task.Task.Uuid)
		fin := api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: s.Receipt.TaskUuid, Queue: q, ExpectedRevision: cl.Delivery.Task.Revision}
		if op == "complete" {
			_, err = c.CompleteGuardedTask(rctx(t), fin)
		} else {
			_, err = c.CancelGuardedTask(rctx(t), api.CancelGuardedTaskRequest(fin))
		}
		require.NoError(t, err)

		again, err := c.SubmitKeyedTask(rctx(t), keyed(q, key, []byte(op)))
		require.NoError(t, err)
		require.True(t, again.Replayed)
		require.Equal(t, s.Receipt, again.Receipt)
		require.NotNil(t, again.Task)
		require.True(t, again.Task.Terminal)
		look, err := c.LookupSubmission(rctx(t), api.LookupSubmissionRequest{Queue: q, SubmissionKey: key})
		require.NoError(t, err)
		require.Equal(t, s.Receipt, *look.Receipt)
		require.True(t, look.Task.Terminal)
	}
	require.Empty(t, drain(t, c, q, "submitted"))
}

// A07: the key namespace is (queue, key).
func TestResilienceKeyScope(t *testing.T) {
	c := newLibClient(t)
	q1, q2 := uniqueQueue(t), uniqueQueue(t)
	key := api.NewSubmissionKey()
	a, err := c.SubmitKeyedTask(rctx(t), keyed(q1, key, []byte("x")))
	require.NoError(t, err)
	b, err := c.SubmitKeyedTask(rctx(t), keyed(q2, key, []byte("x")))
	require.NoError(t, err)
	require.NotEqual(t, a.Receipt.TaskUuid, b.Receipt.TaskUuid)
	_, err = c.SubmitKeyedTask(rctx(t), keyed(q1, "", []byte("x")))
	requireCode(t, err, api.CodeInvalidArgument)
	_, err = c.SubmitKeyedTask(rctx(t), keyed("", key, []byte("x")))
	requireCode(t, err, api.CodeInvalidArgument)
	_, err = c.SubmitKeyedTask(rctx(t), keyed(q1, strings.Repeat("k", api.MaxSubmissionKeyBytes+1), []byte("x")))
	requireCode(t, err, api.CodeInvalidArgument)
	missing, err := c.LookupSubmission(rctx(t), api.LookupSubmissionRequest{Queue: q1, SubmissionKey: "never-used"})
	require.NoError(t, err)
	require.Nil(t, missing.Receipt)
}

// A08: after a timeout release and a new claim, the old worker cannot update,
// complete, or cancel, also when the state names repeat.
func TestResilienceStaleWorkerRejected(t *testing.T) {
	c := newLibClient(t)
	q := uniqueQueue(t)
	s, err := c.SubmitKeyedTask(rctx(t), keyed(q, api.NewSubmissionKey(), []byte("w")))
	require.NoError(t, err)
	id := s.Receipt.TaskUuid

	claimA := claim(q)
	claimA.OverrideTimeout = 30
	a, err := c.ClaimGuardedTask(rctx(t), claimA)
	require.NoError(t, err)
	require.Equal(t, id, a.Delivery.Task.Task.Uuid)
	revA := a.Delivery.Task.Revision

	// The sweep releases A's claim; B claims the same task with the same
	// state names.
	swept, err := c.CleanUpTimedOut(rctx(t), api.CleanUpTimedOutRequest{AtTime: time.Now().Add(time.Hour).UnixNano(), Queue: q})
	require.NoError(t, err)
	require.Equal(t, int64(1), swept.TimedOut)
	b, err := c.ClaimGuardedTask(rctx(t), claim(q))
	require.NoError(t, err)
	require.Equal(t, id, b.Delivery.Task.Task.Uuid)
	require.Equal(t, a.Delivery.Task.Task.CurrentState, b.Delivery.Task.Task.CurrentState)
	require.Greater(t, b.Delivery.Task.Revision, revA)

	state := a.Delivery.Task.Task.CurrentState
	_, err = c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: id, Queue: q, ExpectedRevision: revA, ExpectedState: &state})
	requireCode(t, err, api.CodeRevisionConflict)
	_, err = c.CancelGuardedTask(rctx(t), api.CancelGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: id, Queue: q, ExpectedRevision: revA})
	requireCode(t, err, api.CodeRevisionConflict)
	_, err = c.UpdateGuardedTask(rctx(t), api.UpdateGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: id, Queue: q, ExpectedRevision: revA, NewState: "x"})
	requireCode(t, err, api.CodeRevisionConflict)

	// Wrong expected state and wrong queue with the current revision.
	wrong := "not-the-state"
	revB := b.Delivery.Task.Revision
	_, err = c.UpdateGuardedTask(rctx(t), api.UpdateGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: id, Queue: q, ExpectedRevision: revB, ExpectedState: &wrong, NewState: "x"})
	requireCode(t, err, api.CodeRevisionConflict)
	_, err = c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: id, Queue: q + "-other", ExpectedRevision: revB})
	requireCode(t, err, api.CodeRevisionConflict)

	cur, err := c.GetGuardedTask(rctx(t), api.GetGuardedTaskRequest{Uuid: id, Queue: q})
	require.NoError(t, err)
	require.Equal(t, revB, cur.Task.Revision, "rejected requests must not change the task")
	require.False(t, cur.Task.Terminal)

	_, err = c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: id, Queue: q, ExpectedRevision: revB})
	require.NoError(t, err)
}

// A09: the group claim follows the same rules, and a task that waits without a
// worker is completed with its revision, without a claim.
func TestResilienceGroupClaimAndHumanWait(t *testing.T) {
	c := newLibClient(t)
	q1, q2 := uniqueQueue(t), uniqueQueue(t)
	low, err := c.SubmitKeyedTask(rctx(t), keyed(q1, api.NewSubmissionKey(), []byte("low")))
	require.NoError(t, err)
	hi := keyed(q2, api.NewSubmissionKey(), []byte("high"))
	hi.Priority = 9
	high, err := c.SubmitKeyedTask(rctx(t), hi)
	require.NoError(t, err)

	g, err := c.ClaimGuardedTaskGroup(rctx(t), api.ClaimGuardedTaskGroupRequest{OperationId: api.NewOperationID(), Queues: []string{q1, q2}, CurrentState: "submitted"})
	require.NoError(t, err)
	require.Equal(t, high.Receipt.TaskUuid, g.Delivery.Task.Task.Uuid)
	require.Equal(t, []byte("high"), g.Delivery.Payload)

	// Park the task for a person: no worker, no timeout.
	parked, err := c.UpdateGuardedTask(rctx(t), api.UpdateGuardedTaskRequest{
		OperationId: api.NewOperationID(), Uuid: high.Receipt.TaskUuid, Queue: q2,
		ExpectedRevision: g.Delivery.Task.Revision, NewState: "awaiting-approval", AutoTargetState: "awaiting-approval", Timeout: -1,
	})
	require.NoError(t, err)
	_, err = c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: high.Receipt.TaskUuid, Queue: q2, ExpectedRevision: g.Delivery.Task.Revision})
	requireCode(t, err, api.CodeRevisionConflict)
	st := "awaiting-approval"
	done, err := c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: high.Receipt.TaskUuid, Queue: q2, ExpectedRevision: parked.Task.Revision, ExpectedState: &st})
	require.NoError(t, err)
	require.True(t, done.Task.Terminal)
	require.Equal(t, "completed", done.Task.Task.CurrentState)

	g2, err := c.ClaimGuardedTaskGroup(rctx(t), api.ClaimGuardedTaskGroupRequest{OperationId: api.NewOperationID(), Queues: []string{q2, q1}, CurrentState: "submitted"})
	require.NoError(t, err)
	require.Equal(t, low.Receipt.TaskUuid, g2.Delivery.Task.Task.Uuid)
}

// A10: legacy operations cannot claim or change a guarded task, also in
// compatibility mode. Reads still work.
func TestResilienceLegacyCannotTouchGuarded(t *testing.T) {
	c := newLibClient(t)
	if serverInfo(t, c).TaskGuardPolicy == api.PolicyRequired {
		t.Skip("legacy operations are rejected for every task in required mode (see TestResilienceRequiredModeRejects)")
	}
	q := uniqueQueue(t)
	s, err := c.SubmitKeyedTask(rctx(t), keyed(q, api.NewSubmissionKey(), []byte("g")))
	require.NoError(t, err)
	id := s.Receipt.TaskUuid

	n, err := c.GetNextTask(rctx(t), api.GetNextTaskRequest{Queue: q, CurrentState: "submitted"})
	require.NoError(t, err)
	require.Nil(t, n.Delivery, "a legacy claim must not select a guarded task")
	ng, err := c.GetNextTaskGroup(rctx(t), api.GetNextTaskGroupRequest{Queues: []string{q}, CurrentState: "submitted"})
	require.NoError(t, err)
	require.Nil(t, ng.Delivery)

	_, err = c.UpdateTask(rctx(t), api.UpdateTaskRequest{Uuid: id, Queue: q, NewState: "x"})
	requireCode(t, err, api.CodeTaskGuardRequired)
	_, err = c.CompleteTask(rctx(t), api.CompleteTaskRequest{Uuid: id, Queue: q})
	requireCode(t, err, api.CodeTaskGuardRequired)
	_, err = c.CancelTask(rctx(t), api.CancelTaskRequest{Uuid: id, Queue: q})
	requireCode(t, err, api.CodeTaskGuardRequired)

	got, err := c.GetTaskStateByID(rctx(t), api.GetTaskStateByIDRequest{Uuid: id, Queue: q})
	require.NoError(t, err)
	require.Equal(t, "submitted", got.Task.CurrentState)

	// A guarded worker can claim a legacy task; its revision still protects it.
	leg, err := c.SubmitTask(rctx(t), api.SubmitTaskRequest{Queue: q, CurrentState: "legacy", Timeout: -1, Payload: []byte("l")})
	require.NoError(t, err)
	cl, err := c.ClaimGuardedTask(rctx(t), api.ClaimGuardedTaskRequest{OperationId: api.NewOperationID(), Queue: q, CurrentState: "legacy"})
	require.NoError(t, err)
	require.Equal(t, leg.Task.Uuid, cl.Delivery.Task.Task.Uuid)
	require.False(t, cl.Delivery.Task.Guarded)
}

// A11: lost replies of guarded claims and mutations are replayed from their
// receipts, never executed twice.
func TestResilienceGuardedLostReplies(t *testing.T) {
	c := newLibClient(t)
	p := startFaultProxy(t, proxyTarget(t))
	pc := api.New(p.Addr())
	q := uniqueQueue(t)
	for i := 0; i < 2; i++ {
		_, err := c.SubmitKeyedTask(rctx(t), keyed(q, api.NewSubmissionKey(), []byte{byte(i)}))
		require.NoError(t, err)
	}

	p.DropNextReply()
	cl, err := pc.ClaimGuardedTask(rctx(t), claim(q))
	require.NoError(t, err)
	require.True(t, cl.Replayed, "the replayed claim must return the first claim")
	// Exactly one task is claimed: the other one is still ready.
	require.Len(t, drain(t, c, q, "submitted"), 1)

	up := api.UpdateGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: cl.Delivery.Task.Task.Uuid, Queue: q,
		ExpectedRevision: cl.Delivery.Task.Revision, NewState: "step2", AutoTargetState: "step2-working", Timeout: -1}
	p.DropNextReply()
	u, err := pc.UpdateGuardedTask(rctx(t), up)
	require.NoError(t, err)
	require.True(t, u.Replayed)
	require.Equal(t, cl.Delivery.Task.Revision+1, u.Task.Revision)

	fin := api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: up.Uuid, Queue: q, ExpectedRevision: u.Task.Revision}
	p.DropNextReply()
	f, err := pc.CompleteGuardedTask(rctx(t), fin)
	require.NoError(t, err)
	require.True(t, f.Replayed)
	require.True(t, f.Task.Terminal)
	require.Equal(t, 3, p.Dropped())

	rec, err := c.LookupOperation(rctx(t), api.LookupOperationRequest{OperationId: fin.OperationId})
	require.NoError(t, err)
	require.Equal(t, "complete", rec.Receipt.Op)
	require.Equal(t, "completed", rec.Receipt.ResultState)

	// Reusing an operation_id for a different request is a conflict.
	fin.ExpectedRevision++
	_, err = c.CompleteGuardedTask(rctx(t), fin)
	requireCode(t, err, api.CodeOperationConflict)

	// A replayed claim after the task changed reports that it no longer
	// holds the task.
	s, err := c.SubmitKeyedTask(rctx(t), keyed(q, api.NewSubmissionKey(), []byte("z")))
	require.NoError(t, err)
	cr := claim(q)
	cr.OverrideTimeout = 30
	first, err := c.ClaimGuardedTask(rctx(t), cr)
	require.NoError(t, err)
	require.Equal(t, s.Receipt.TaskUuid, first.Delivery.Task.Task.Uuid)
	_, err = c.CleanUpTimedOut(rctx(t), api.CleanUpTimedOutRequest{AtTime: time.Now().Add(time.Hour).UnixNano(), Queue: q})
	require.NoError(t, err)
	_, err = c.ClaimGuardedTask(rctx(t), cr)
	requireCode(t, err, api.CodeClaimSuperseded)
}

// A06/A07 in a cluster: a follower does not answer a receipt lookup from its
// own, possibly stale, copy. It redirects to the leader.
func TestResilienceFollowerLookupRedirects(t *testing.T) {
	seeds := testSeeds()
	if len(seeds) < 2 {
		t.Skip("needs CORNDOGS_TEST_SEEDS with two or more nodes")
	}
	redirects := 0
	for _, s := range seeds {
		one := api.NewCorndogsClient(&api.StreamTransport{Addr: s, MaxAttempts: 1})
		_, err := one.LookupSubmission(rctx(t), api.LookupSubmissionRequest{Queue: "q", SubmissionKey: "k"})
		if err != nil {
			require.True(t, errors.Is(err, api.ErrNotApplied), "follower must redirect: %v", err)
			redirects++
		}
	}
	require.Equal(t, len(seeds)-1, redirects)
}

// A12, new client against a released server: the resilience operations fail
// as unsupported before any change, and the client does not fall back to a
// legacy operation. Run with CORNDOGS_TEST_OLD_SERVER=1 against 0.7.x.
func TestResilienceOldServerUnsupported(t *testing.T) {
	if os.Getenv("CORNDOGS_TEST_OLD_SERVER") == "" {
		t.Skip("set CORNDOGS_TEST_OLD_SERVER=1 and point CORNDOGS_TEST_ADDR at a 0.7.x server")
	}
	c := newLibClient(t)
	_, err := c.RequireFeatures(rctx(t), api.FeatureSubmissionKeys)
	require.True(t, api.IsUnsupported(err), "got %v", err)
	q := uniqueQueue(t)
	_, err = c.SubmitKeyedTask(rctx(t), keyed(q, api.NewSubmissionKey(), []byte("x")))
	require.True(t, api.IsUnsupported(err), "got %v", err)
	_, err = c.ClaimGuardedTask(rctx(t), claim(q))
	require.True(t, api.IsUnsupported(err), "got %v", err)
	counts, err := c.GetTaskStateCounts(rctx(t), api.GetTaskStateCountsRequest{Queue: q})
	require.NoError(t, err)
	require.Zero(t, counts.Count, "no task may be created through a fallback")
	// Legacy operations still work where the client claims support.
	_, err = c.SubmitTask(rctx(t), api.SubmitTaskRequest{Queue: q, Timeout: -1, Payload: []byte("legacy")})
	require.NoError(t, err)
}
