package filestore

import (
	"bytes"
	"context"
	"encoding/json"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Buckets of the resilience contract. All of them replicate with the tasks.
var (
	bucketSubmissions   = []byte("submissions")    // queue<sep>key -> json(submissionRecord)
	bucketOperations    = []byte("operations")     // operation id -> json(operationRecord)
	bucketReceiptExpiry = []byte("receipt-expiry") // expiresAt(8)+kind(1)+id -> nil
)

// Receipt kinds in the expiry index.
const (
	receiptSubmission byte = 's'
	receiptOperation  byte = 'o'
)

// submissionRecord is the durable receipt of a keyed submission.
type submissionRecord struct {
	Queue      string `json:"queue"`
	Key        string `json:"key"`
	Digest     []byte `json:"digest"`
	TaskUUID   string `json:"task_uuid"`
	AcceptedAt int64  `json:"accepted_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Guarded    bool   `json:"guarded"`
}

// operationRecord is the durable receipt of a guarded mutation. Result holds
// the task as the operation left it, so a replay returns the same result even
// after later changes.
type operationRecord struct {
	OperationID string       `json:"operation_id"`
	Op          string       `json:"op"`
	Digest      []byte       `json:"digest"`
	TaskUUID    string       `json:"task_uuid"`
	Queue       string       `json:"queue"`
	At          int64        `json:"at"`
	ExpiresAt   int64        `json:"expires_at"`
	Result      *resultState `json:"result"`
}

// resultState is the task state after a guarded mutation.
type resultState struct {
	Task     Task `json:"task"`
	Terminal bool `json:"terminal,omitempty"`
}

func (r *resultState) view() *api.GuardedTask {
	g := toGuardedTask(&r.Task)
	g.Terminal = r.Terminal
	return g
}

func (r *submissionRecord) receipt() *api.SubmissionReceipt {
	return &api.SubmissionReceipt{
		Queue:         r.Queue,
		SubmissionKey: r.Key,
		TaskUuid:      r.TaskUUID,
		AcceptedAt:    r.AcceptedAt,
		ExpiresAt:     r.ExpiresAt,
		Guarded:       r.Guarded,
	}
}

func (r *operationRecord) receipt() *api.OperationReceipt {
	out := &api.OperationReceipt{
		OperationId: r.OperationID,
		Op:          r.Op,
		TaskUuid:    r.TaskUUID,
		Queue:       r.Queue,
		At:          r.At,
		ExpiresAt:   r.ExpiresAt,
	}
	if r.Result != nil {
		out.ResultRevision = r.Result.Task.Revision
		out.ResultState = r.Result.Task.CurrentState
	}
	return out
}

func submissionKey(queue, key string) []byte {
	k := make([]byte, 0, len(queue)+1+len(key))
	k = append(k, queue...)
	k = append(k, sep)
	return append(k, key...)
}

func expiryKey(expiresAt int64, kind byte, id []byte) []byte {
	t := encodeTimeAsc(expiresAt)
	k := make([]byte, 0, 9+len(id))
	k = append(k, t[:]...)
	k = append(k, kind)
	return append(k, id...)
}

// putBytes writes one key and records it for replication.
func (s *BoltStore) putBytes(tx *bolt.Tx, bucket, key, val []byte) error {
	if err := tx.Bucket(bucket).Put(key, val); err != nil {
		return err
	}
	if s.cap != nil {
		s.cap.put(bucket, key, val)
	}
	return nil
}

// deleteBytes deletes one key and records it for replication.
func (s *BoltStore) deleteBytes(tx *bolt.Tx, bucket, key []byte) error {
	if err := tx.Bucket(bucket).Delete(key); err != nil {
		return err
	}
	if s.cap != nil {
		s.cap.del(bucket, key)
	}
	return nil
}

// loadSubmission returns the unexpired record for (queue, key), or nil.
func loadSubmission(tx *bolt.Tx, queue, key string, now int64) (*submissionRecord, error) {
	v := tx.Bucket(bucketSubmissions).Get(submissionKey(queue, key))
	if v == nil {
		return nil, nil
	}
	var r submissionRecord
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, err
	}
	if r.ExpiresAt <= now {
		return nil, nil
	}
	return &r, nil
}

// loadOperation returns the unexpired record for an operation id, or nil.
func loadOperation(tx *bolt.Tx, id string, now int64) (*operationRecord, error) {
	v := tx.Bucket(bucketOperations).Get([]byte(id))
	if v == nil {
		return nil, nil
	}
	var r operationRecord
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, err
	}
	if r.ExpiresAt <= now {
		return nil, nil
	}
	return &r, nil
}

func (s *BoltStore) putSubmission(tx *bolt.Tx, r *submissionRecord) error {
	val, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key := submissionKey(r.Queue, r.Key)
	if err := s.putBytes(tx, bucketSubmissions, key, val); err != nil {
		return err
	}
	return s.putBytes(tx, bucketReceiptExpiry, expiryKey(r.ExpiresAt, receiptSubmission, key), []byte{})
}

func (s *BoltStore) putOperation(tx *bolt.Tx, r *operationRecord) error {
	val, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.putBytes(tx, bucketOperations, []byte(r.OperationID), val); err != nil {
		return err
	}
	return s.putBytes(tx, bucketReceiptExpiry, expiryKey(r.ExpiresAt, receiptOperation, []byte(r.OperationID)), []byte{})
}

// loadArchived returns the archive record for id, or nil.
func loadArchived(tx *bolt.Tx, id string) (*ArchivedTask, error) {
	v := tx.Bucket(bucketArchived).Get([]byte(id))
	if v == nil {
		return nil, nil
	}
	var a ArchivedTask
	if err := json.Unmarshal(v, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// currentView returns the live or archived guarded view of id, or nil.
func currentView(tx *bolt.Tx, id string) (*api.GuardedTask, error) {
	t, err := loadByUUID(tx, id)
	if err != nil {
		return nil, err
	}
	if t != nil {
		return toGuardedTask(t), nil
	}
	a, err := loadArchived(tx, id)
	if err != nil || a == nil {
		return nil, err
	}
	return archivedToGuardedTask(a), nil
}

func (s *BoltStore) SubmitKeyedTask(ctx context.Context, req *api.SubmitKeyedTaskRequest, ro store.ReceiptOptions) (*api.SubmitKeyedTaskResponse, error) {
	var out *api.SubmitKeyedTaskResponse
	err := s.write(func(tx *bolt.Tx) error {
		now := nowNano()
		prev, err := loadSubmission(tx, req.Queue, req.SubmissionKey, now)
		if err != nil {
			return err
		}
		if prev != nil {
			if !bytes.Equal(prev.Digest, ro.Digest) {
				return store.SubmissionKeyConflict(req.Queue, req.SubmissionKey)
			}
			view, err := currentView(tx, prev.TaskUUID)
			if err != nil {
				return err
			}
			out = &api.SubmitKeyedTaskResponse{Receipt: *prev.receipt(), Replayed: true, Task: view}
			return nil
		}
		id, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		t := &Task{
			UUID:            id.String(),
			Queue:           req.Queue,
			CurrentState:    req.CurrentState,
			AutoTargetState: req.AutoTargetState,
			SubmitTime:      now,
			UpdateTime:      now,
			Timeout:         req.Timeout,
			Priority:        req.Priority,
			Revision:        1,
			Guarded:         req.Guarded,
		}
		rec := &submissionRecord{
			Queue:      req.Queue,
			Key:        req.SubmissionKey,
			Digest:     ro.Digest,
			TaskUUID:   t.UUID,
			AcceptedAt: now,
			ExpiresAt:  now + ro.Retention.Nanoseconds(),
			Guarded:    req.Guarded,
		}
		if err := s.putPayload(tx, t.UUID, req.Payload); err != nil {
			return err
		}
		if err := s.putTask(tx, t); err != nil {
			return err
		}
		if err := s.putSubmission(tx, rec); err != nil {
			return err
		}
		s.audit.Record(AuditEvent{Op: "submit", UUID: t.UUID, Queue: t.Queue, ToState: t.CurrentState, Priority: t.Priority})
		out = &api.SubmitKeyedTaskResponse{Receipt: *rec.receipt(), Task: toGuardedTask(t)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *BoltStore) LookupSubmission(ctx context.Context, req *api.LookupSubmissionRequest) (*api.LookupSubmissionResponse, error) {
	out := &api.LookupSubmissionResponse{}
	err := s.db.View(func(tx *bolt.Tx) error {
		rec, err := loadSubmission(tx, req.Queue, req.SubmissionKey, nowNano())
		if err != nil || rec == nil {
			return err
		}
		out.Receipt = rec.receipt()
		out.Task, err = currentView(tx, rec.TaskUUID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// replayOperation checks the receipt of opID inside a write transaction. It
// returns the record when the same request already ran, nil when the operation
// is new, or a conflict when the id was used for a different request.
func replayOperation(tx *bolt.Tx, opID, op string, digest []byte, now int64) (*operationRecord, error) {
	rec, err := loadOperation(tx, opID, now)
	if err != nil || rec == nil {
		return nil, err
	}
	if rec.Op != op || !bytes.Equal(rec.Digest, digest) {
		return nil, store.OperationConflict(opID, rec.Op)
	}
	return rec, nil
}

func (s *BoltStore) ClaimGuardedTask(ctx context.Context, req *api.ClaimGuardedTaskGroupRequest, ro store.ReceiptOptions) (*api.ClaimGuardedTaskGroupResponse, error) {
	out := &api.ClaimGuardedTaskGroupResponse{}
	claim := &api.GetNextTaskRequest{
		CurrentState:            req.CurrentState,
		OverrideTimeout:         req.OverrideTimeout,
		OverrideCurrentState:    req.OverrideCurrentState,
		OverrideAutoTargetState: req.OverrideAutoTargetState,
	}
	err := s.write(func(tx *bolt.Tx) error {
		now := nowNano()
		rec, err := replayOperation(tx, req.OperationId, store.OpClaim, ro.Digest, now)
		if err != nil {
			return err
		}
		if rec != nil {
			// The claim ran before. Return it again only while it still holds
			// the task: the revision changes on any later mutation.
			t, err := loadByUUID(tx, rec.TaskUUID)
			if err != nil {
				return err
			}
			if t == nil || t.Revision != rec.Result.Task.Revision {
				return store.ClaimSuperseded(req.OperationId, rec.TaskUUID)
			}
			payload, err := loadPayload(tx, t.UUID)
			if err != nil {
				return err
			}
			out.Delivery = &api.GuardedDelivery{Task: *toGuardedTask(t), Payload: payload}
			out.Replayed = true
			return nil
		}
		best, err := bestHead(tx, req.Queues, req.CurrentState, bucketTasks, bucketGuarded)
		if err != nil || best == nil {
			return err
		}
		payload, err := loadPayload(tx, best.UUID)
		if err != nil {
			return err
		}
		if err := s.deleteTask(tx, best); err != nil {
			return err
		}
		applyGetNext(best, claim, now)
		best.Revision++
		if err := s.putTask(tx, best); err != nil {
			return err
		}
		if err := s.putOperation(tx, &operationRecord{
			OperationID: req.OperationId,
			Op:          store.OpClaim,
			Digest:      ro.Digest,
			TaskUUID:    best.UUID,
			Queue:       best.Queue,
			At:          now,
			ExpiresAt:   now + ro.Retention.Nanoseconds(),
			Result:      &resultState{Task: *best},
		}); err != nil {
			return err
		}
		out.Delivery = &api.GuardedDelivery{Task: *toGuardedTask(best), Payload: payload}
		s.audit.Record(AuditEvent{Op: "claim", UUID: best.UUID, Queue: best.Queue, FromState: req.CurrentState, ToState: best.CurrentState})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// bestHead returns the next task to claim across queues and buckets, in the
// dequeue order (priority desc, update_time asc, uuid asc), or nil.
func bestHead(tx *bolt.Tx, queues []string, state string, buckets ...[]byte) (*Task, error) {
	var best *Task
	for _, name := range buckets {
		c := tx.Bucket(name).Cursor()
		for _, queue := range queues {
			prefix := taskPrefix(queue, state)
			k, v := c.Seek(prefix)
			if k == nil || !bytes.HasPrefix(k, prefix) {
				continue
			}
			var t Task
			if err := json.Unmarshal(v, &t); err != nil {
				return nil, err
			}
			if best == nil || betterCandidate(&t, best) {
				cand := t
				best = &cand
			}
		}
	}
	return best, nil
}

// checkGuard loads the live task for a guarded mutation and checks the queue,
// revision, and expected state. It changes nothing.
func checkGuard(tx *bolt.Tx, uuid, queue string, expectedRevision int64, expectedState *string) (*Task, error) {
	t, err := loadByUUID(tx, uuid)
	if err != nil {
		return nil, err
	}
	if t == nil {
		a, err := loadArchived(tx, uuid)
		if err != nil {
			return nil, err
		}
		if a == nil {
			return nil, store.TaskNotFound(uuid)
		}
		return nil, store.RevisionConflict(uuid, "task is %s at revision %d", a.CurrentState, a.Revision)
	}
	if t.Queue != queue {
		return nil, store.RevisionConflict(uuid, "task is in queue %q, not %q", t.Queue, queue)
	}
	if t.Revision != expectedRevision {
		return nil, store.RevisionConflict(uuid, "revision is %d, expected %d", t.Revision, expectedRevision)
	}
	if expectedState != nil && t.CurrentState != *expectedState {
		return nil, store.RevisionConflict(uuid, "state is %q, expected %q", t.CurrentState, *expectedState)
	}
	return t, nil
}

func (s *BoltStore) UpdateGuardedTask(ctx context.Context, req *api.UpdateGuardedTaskRequest, ro store.ReceiptOptions) (*api.UpdateGuardedTaskResponse, error) {
	var out *api.UpdateGuardedTaskResponse
	err := s.write(func(tx *bolt.Tx) error {
		now := nowNano()
		rec, err := replayOperation(tx, req.OperationId, store.OpUpdate, ro.Digest, now)
		if err != nil {
			return err
		}
		if rec != nil {
			out = &api.UpdateGuardedTaskResponse{Task: *rec.Result.view(), Replayed: true}
			return nil
		}
		t, err := checkGuard(tx, req.Uuid, req.Queue, req.ExpectedRevision, req.ExpectedState)
		if err != nil {
			return err
		}
		if err := s.deleteTask(tx, t); err != nil {
			return err
		}
		from := t.CurrentState
		t.CurrentState = req.NewState
		t.AutoTargetState = req.AutoTargetState
		t.Timeout = req.Timeout
		if req.Priority != nil {
			t.Priority = *req.Priority
		}
		if req.Payload != nil {
			if err := s.putPayload(tx, t.UUID, *req.Payload); err != nil {
				return err
			}
		}
		t.UpdateTime = now
		t.Revision++
		if err := s.putTask(tx, t); err != nil {
			return err
		}
		if err := s.putOperation(tx, &operationRecord{
			OperationID: req.OperationId,
			Op:          store.OpUpdate,
			Digest:      ro.Digest,
			TaskUUID:    t.UUID,
			Queue:       t.Queue,
			At:          now,
			ExpiresAt:   now + ro.Retention.Nanoseconds(),
			Result:      &resultState{Task: *t},
		}); err != nil {
			return err
		}
		s.audit.Record(AuditEvent{Op: "update", UUID: t.UUID, Queue: t.Queue, FromState: from, ToState: t.CurrentState, Priority: t.Priority})
		out = &api.UpdateGuardedTaskResponse{Task: *toGuardedTask(t)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *BoltStore) FinishGuardedTask(ctx context.Context, req *api.CompleteGuardedTaskRequest, op string, ro store.ReceiptOptions) (*api.CompleteGuardedTaskResponse, error) {
	terminal := store.TerminalState(op)
	var out *api.CompleteGuardedTaskResponse
	err := s.write(func(tx *bolt.Tx) error {
		now := nowNano()
		rec, err := replayOperation(tx, req.OperationId, op, ro.Digest, now)
		if err != nil {
			return err
		}
		if rec != nil {
			out = &api.CompleteGuardedTaskResponse{Task: *rec.Result.view(), Replayed: true}
			return nil
		}
		t, err := checkGuard(tx, req.Uuid, req.Queue, req.ExpectedRevision, req.ExpectedState)
		if err != nil {
			return err
		}
		a, err := s.archiveTask(tx, t, terminal, op)
		if err != nil {
			return err
		}
		result := &resultState{Task: Task{
			UUID: a.UUID, Queue: a.Queue, CurrentState: a.CurrentState, AutoTargetState: a.AutoTargetState,
			SubmitTime: a.SubmitTime, UpdateTime: a.UpdateTime, Revision: a.Revision, Guarded: a.Guarded,
		}, Terminal: true}
		if err := s.putOperation(tx, &operationRecord{
			OperationID: req.OperationId,
			Op:          op,
			Digest:      ro.Digest,
			TaskUUID:    a.UUID,
			Queue:       a.Queue,
			At:          now,
			ExpiresAt:   now + ro.Retention.Nanoseconds(),
			Result:      result,
		}); err != nil {
			return err
		}
		out = &api.CompleteGuardedTaskResponse{Task: *archivedToGuardedTask(a)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *BoltStore) GetGuardedTask(ctx context.Context, req *api.GetGuardedTaskRequest) (*api.GetGuardedTaskResponse, error) {
	out := &api.GetGuardedTaskResponse{}
	err := s.db.View(func(tx *bolt.Tx) error {
		view, err := currentView(tx, req.Uuid)
		if err != nil || view == nil {
			return err
		}
		if req.Queue != "" && view.Task.Queue != req.Queue {
			return nil
		}
		out.Task = view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *BoltStore) LookupOperation(ctx context.Context, req *api.LookupOperationRequest) (*api.LookupOperationResponse, error) {
	out := &api.LookupOperationResponse{}
	err := s.db.View(func(tx *bolt.Tx) error {
		rec, err := loadOperation(tx, req.OperationId, nowNano())
		if err != nil || rec == nil {
			return err
		}
		out.Receipt = rec.receipt()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PurgeExpiredReceipts deletes at most limit receipts that expired before now.
// A record that a later request replaced has a later expiry, so the old index
// entry does not delete it.
func (s *BoltStore) PurgeExpiredReceipts(ctx context.Context, now int64, limit int) (int, error) {
	purged := 0
	err := s.write(func(tx *bolt.Tx) error {
		purged = 0
		type entry struct {
			index, id []byte
			kind      byte
			expiresAt int64
		}
		var expired []entry
		c := tx.Bucket(bucketReceiptExpiry).Cursor()
		for k, _ := c.First(); k != nil && len(expired) < limit; k, _ = c.Next() {
			if len(k) < 9 {
				continue
			}
			at := decodeTimeAsc(k[:8])
			if at > now {
				break
			}
			expired = append(expired, entry{index: bytes.Clone(k), id: bytes.Clone(k[9:]), kind: k[8], expiresAt: at})
		}
		for _, e := range expired {
			bucket := bucketOperations
			if e.kind == receiptSubmission {
				bucket = bucketSubmissions
			}
			if v := tx.Bucket(bucket).Get(e.id); v != nil && recordExpiresAt(v) == e.expiresAt {
				if err := s.deleteBytes(tx, bucket, e.id); err != nil {
					return err
				}
				purged++
			}
			if err := s.deleteBytes(tx, bucketReceiptExpiry, e.index); err != nil {
				return err
			}
		}
		return nil
	})
	return purged, err
}

// recordExpiresAt reads expires_at from a stored receipt, or 0.
func recordExpiresAt(v []byte) int64 {
	var r struct {
		ExpiresAt int64 `json:"expires_at"`
	}
	if json.Unmarshal(v, &r) != nil {
		return 0
	}
	return r.ExpiresAt
}

func errLegacyOnGuarded(op, id string) error {
	return store.LegacyOnGuarded(op, id)
}

func legacyOpName(op string) string {
	if op == "cancel" {
		return "CancelTask"
	}
	return "CompleteTask"
}
