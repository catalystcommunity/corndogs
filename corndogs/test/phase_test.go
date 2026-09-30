package test

// Two-phase recovery checks. A script runs the "write" phase, kills the
// servers (SIGKILL), restarts them on the same storage, and runs the
// "verify" phase. The phases share a JSON state file.
//
//	RESILIENCE_PHASE  write | verify | legacy-write | legacy-verify
//	RESILIENCE_STATE  path of the state file
//
// write/verify (A06, A13): keyed submissions, a held guarded claim, and
// terminal tasks keep their receipts and identities across the restart.
// legacy-write/legacy-verify (A12, A15): a 0.7.6 server writes legacy data;
// the new server reads and serves it after its storage migration.

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
)

type phaseState struct {
	Queue      string            `json:"queue"`
	Keys       map[string]string `json:"keys"`     // key -> task uuid
	Payloads   map[string][]byte `json:"payloads"` // task uuid -> payload
	ClaimOp    string            `json:"claim_op"`
	ClaimUUID  string            `json:"claim_uuid"`
	ClaimRev   int64             `json:"claim_rev"`
	DoneKey    string            `json:"done_key"`
	DoneOp     string            `json:"done_op"`
	LegacyIDs  []string          `json:"legacy_ids"`
	LegacyWait string            `json:"legacy_wait"`
}

func phase(t *testing.T, want ...string) (string, string) {
	p, path := os.Getenv("RESILIENCE_PHASE"), os.Getenv("RESILIENCE_STATE")
	for _, w := range want {
		if p == w && path != "" {
			return p, path
		}
	}
	t.Skip("set RESILIENCE_PHASE and RESILIENCE_STATE")
	return "", ""
}

func saveState(t *testing.T, path string, st *phaseState) {
	b, err := json.Marshal(st)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

func loadState(t *testing.T, path string) *phaseState {
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var st phaseState
	require.NoError(t, json.Unmarshal(b, &st))
	return &st
}

func TestResiliencePhase(t *testing.T) {
	p, path := phase(t, "write", "verify")
	c := newLibClient(t)
	if p == "write" {
		st := &phaseState{Queue: uniqueQueue(t), Keys: map[string]string{}, Payloads: map[string][]byte{}}
		for i := 0; i < 24; i++ {
			key := fmt.Sprintf("key-%02d", i)
			payload := []byte(fmt.Sprintf("payload-%02d-%x", i, i*7919))
			r, err := c.SubmitKeyedTask(rctx(t), keyed(st.Queue, key, payload))
			require.NoError(t, err)
			st.Keys[key] = r.Receipt.TaskUuid
			st.Payloads[r.Receipt.TaskUuid] = payload
		}
		// One held claim and one completed task.
		st.ClaimOp = api.NewOperationID()
		cl, err := c.ClaimGuardedTask(rctx(t), api.ClaimGuardedTaskRequest{OperationId: st.ClaimOp, Queue: st.Queue, CurrentState: "submitted"})
		require.NoError(t, err)
		st.ClaimUUID, st.ClaimRev = cl.Delivery.Task.Task.Uuid, cl.Delivery.Task.Revision
		done, err := c.ClaimGuardedTask(rctx(t), claim(st.Queue))
		require.NoError(t, err)
		st.DoneOp = api.NewOperationID()
		_, err = c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: st.DoneOp, Uuid: done.Delivery.Task.Task.Uuid, Queue: st.Queue, ExpectedRevision: done.Delivery.Task.Revision})
		require.NoError(t, err)
		for k, id := range st.Keys {
			if id == done.Delivery.Task.Task.Uuid {
				st.DoneKey = k
			}
		}
		saveState(t, path, st)
		return
	}

	st := loadState(t, path)
	for key, id := range st.Keys {
		r, err := c.SubmitKeyedTask(rctx(t), keyed(st.Queue, key, st.Payloads[id]))
		require.NoError(t, err, key)
		require.True(t, r.Replayed, key)
		require.Equal(t, id, r.Receipt.TaskUuid, key)
	}
	replay, err := c.ClaimGuardedTask(rctx(t), api.ClaimGuardedTaskRequest{OperationId: st.ClaimOp, Queue: st.Queue, CurrentState: "submitted"})
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, st.ClaimUUID, replay.Delivery.Task.Task.Uuid)
	require.Equal(t, st.ClaimRev, replay.Delivery.Task.Revision)
	require.Equal(t, st.Payloads[st.ClaimUUID], replay.Delivery.Payload)
	op, err := c.LookupOperation(rctx(t), api.LookupOperationRequest{OperationId: st.DoneOp})
	require.NoError(t, err)
	require.Equal(t, "completed", op.Receipt.ResultState)
	look, err := c.LookupSubmission(rctx(t), api.LookupSubmissionRequest{Queue: st.Queue, SubmissionKey: st.DoneKey})
	require.NoError(t, err)
	require.True(t, look.Task.Terminal)

	// Claim every retained ready task and compare exact payloads.
	claimed := 0
	for {
		r, err := c.ClaimGuardedTask(rctx(t), claim(st.Queue))
		require.NoError(t, err)
		if r.Delivery == nil {
			break
		}
		require.Equal(t, st.Payloads[r.Delivery.Task.Task.Uuid], r.Delivery.Payload)
		claimed++
	}
	require.Equal(t, 22, claimed)
}

// TestResilienceLegacyPhase writes with legacy operations only, so the write
// phase runs against a released 0.7.6 server.
func TestResilienceLegacyPhase(t *testing.T) {
	p, path := phase(t, "legacy-write", "legacy-verify")
	c := newLibClient(t)
	if p == "legacy-write" {
		st := &phaseState{Queue: uniqueQueue(t), Payloads: map[string][]byte{}}
		for i := 0; i < 5; i++ {
			payload := []byte(fmt.Sprintf("legacy-%d", i))
			r, err := c.SubmitTask(rctx(t), api.SubmitTaskRequest{Queue: st.Queue, CurrentState: "submitted", AutoTargetState: "submitted-working", Timeout: -1, Payload: payload, Priority: int64(i)})
			require.NoError(t, err)
			st.LegacyIDs = append(st.LegacyIDs, r.Task.Uuid)
			st.Payloads[r.Task.Uuid] = payload
		}
		// A task parked for a person, and a claimed task.
		w, err := c.SubmitTask(rctx(t), api.SubmitTaskRequest{Queue: st.Queue, CurrentState: "awaiting-approval", AutoTargetState: "awaiting-approval", Timeout: -1, Payload: []byte("wait")})
		require.NoError(t, err)
		st.LegacyWait = w.Task.Uuid
		_, err = c.GetNextTask(rctx(t), api.GetNextTaskRequest{Queue: st.Queue, CurrentState: "submitted"})
		require.NoError(t, err)
		saveState(t, path, st)
		return
	}

	st := loadState(t, path)
	// Metadata is readable by the legacy read and by the guarded read.
	for _, id := range append(st.LegacyIDs, st.LegacyWait) {
		g, err := c.GetTaskStateByID(rctx(t), api.GetTaskStateByIDRequest{Uuid: id, Queue: st.Queue})
		require.NoError(t, err)
		require.NotNil(t, g.Task)
		gv, err := c.GetGuardedTask(rctx(t), api.GetGuardedTaskRequest{Uuid: id, Queue: st.Queue})
		require.NoError(t, err)
		require.False(t, gv.Task.Guarded)
	}
	// The human-wait task written by 0.7.6 is completed with its revision.
	wv, err := c.GetGuardedTask(rctx(t), api.GetGuardedTaskRequest{Uuid: st.LegacyWait, Queue: st.Queue})
	require.NoError(t, err)
	_, err = c.CompleteGuardedTask(rctx(t), api.CompleteGuardedTaskRequest{OperationId: api.NewOperationID(), Uuid: st.LegacyWait, Queue: st.Queue, ExpectedRevision: wv.Task.Revision})
	require.NoError(t, err)
	// Guarded workers claim the retained legacy tasks with their payloads.
	claimed := 0
	for {
		r, err := c.ClaimGuardedTask(rctx(t), claim(st.Queue))
		require.NoError(t, err)
		if r.Delivery == nil {
			break
		}
		require.Equal(t, st.Payloads[r.Delivery.Task.Task.Uuid], r.Delivery.Payload)
		claimed++
	}
	require.Equal(t, 4, claimed)
}
