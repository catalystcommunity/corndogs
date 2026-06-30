package filestore

import (
	"bytes"
	"encoding/binary"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// sep separates the variable-length string components of a bbolt task key.
// Queue and state names are simple identifiers in practice and never contain
// NUL, so this keeps the (queue, state) byte prefix scannable.
const sep = 0x00

// encodePriorityDesc maps an int64 priority to 8 bytes that sort so that the
// HIGHEST priority compares smallest (i.e. comes first in ascending iteration).
func encodePriorityDesc(p int64) [8]byte {
	// order-preserving int64->uint64, then invert for descending.
	u := uint64(p) ^ (1 << 63)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], ^u)
	return b
}

// encodeTimeAsc maps an int64 (unix nanos) to 8 bytes that sort ascending
// (oldest first).
func encodeTimeAsc(t int64) [8]byte {
	u := uint64(t) ^ (1 << 63)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], u)
	return b
}

// taskPrefix is the byte prefix shared by every task in a (queue, state) group.
// Seeking to it and iterating yields tasks in (priority desc, update_time asc)
// order — exactly the postgres ORDER BY.
func taskPrefix(queue, state string) []byte {
	var buf bytes.Buffer
	buf.WriteString(queue)
	buf.WriteByte(sep)
	buf.WriteString(state)
	buf.WriteByte(sep)
	return buf.Bytes()
}

// encodeTaskKey builds the full ordered primary key for a task.
func encodeTaskKey(t *Task) []byte {
	prio := encodePriorityDesc(t.Priority)
	tm := encodeTimeAsc(t.UpdateTime)
	buf := bytes.NewBuffer(taskPrefix(t.Queue, t.CurrentState))
	buf.Write(prio[:])
	buf.Write(tm[:])
	buf.WriteString(t.UUID)
	return buf.Bytes()
}

// applyGetNext mutates a freshly-claimed task per the GetNextTask contract,
// shared by both backends so the semantics stay identical.
//
// The postgres backend swaps current_state and auto_target_state (so a later
// timeout reverts the task), then applies any overrides. The transient
// "-working" suffix it appends in SQL never survives the swap, so it is omitted
// here. update_time is refreshed to now.
func applyGetNext(t *Task, req *api.GetNextTaskRequest, now int64) {
	prevCurrent := t.CurrentState
	t.CurrentState = t.AutoTargetState
	t.AutoTargetState = prevCurrent

	if req.OverrideCurrentState != "" {
		t.CurrentState = req.OverrideCurrentState
	}
	if req.OverrideAutoTargetState != "" {
		t.AutoTargetState = req.OverrideAutoTargetState
	}
	if req.OverrideTimeout < 0 {
		t.Timeout = 0
	} else if req.OverrideTimeout != 0 {
		t.Timeout = req.OverrideTimeout
	}
	t.UpdateTime = now
}

// timedOut reports whether a task has exceeded its timeout as of atTime,
// matching the postgres CleanUpTimedOut predicate:
//
//	timeout > 0 AND (update_time + timeout*1e9) < atTime
func timedOut(t *Task, atTime int64) bool {
	const nanosPerSecond = 1_000_000_000
	return t.Timeout > 0 && (t.UpdateTime+t.Timeout*nanosPerSecond) < atTime
}
