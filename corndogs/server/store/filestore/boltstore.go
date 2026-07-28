package filestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Bucket names for the bbolt backend.
var (
	bucketTasks     = []byte("tasks")     // ordered key -> json(Task metadata)
	bucketByUUID    = []byte("uuid")      // uuid -> ordered key
	bucketPayloads  = []byte("payloads")  // uuid -> opaque payload bytes
	bucketDeadlines = []byte("deadlines") // deadline+uuid -> ordered task key
	bucketArchived  = []byte("archived")  // uuid -> json(ArchivedTask)
	bucketMeta      = []byte("meta")      // filestore schema metadata
)

// BoltStore implements store.Store on top of go.etcd.io/bbolt.
//
// The live tasks bucket is keyed by an ordered composite key
// (queue, state, priority desc, update_time asc, uuid) so that GetNextTask is a
// single cursor Seek to the (queue, state) prefix followed by an in-place
// mutate, all inside one write transaction. bbolt allows many concurrent
// readers with a single writer, so claims serialize correctly with no
// SKIP-LOCKED machinery and no optimistic-retry loops.
type BoltStore struct {
	db        *bolt.DB
	audit     *AuditLog
	cfg       Config
	stop      chan struct{} // stops the interval-sync ticker
	committer *committer    // non-nil only in group-commit mode

	// Replication (Tier-1 clustering, leader side). When replSink is non-nil the
	// store captures the physical mutations of each committed direct write and
	// publishes them as one LSN-stamped MutationBatch. writeMu serializes the
	// capture-enabled write path so mutation capture and LSN assignment stay in
	// commit order (bbolt already single-writes; this extends the critical section
	// to cover capture). When replSink is nil there is zero overhead and the path
	// is byte-for-byte the original single-node behavior.
	writeMu  sync.Mutex
	cap      *captureBuf
	replSink func(MutationBatch)
	replLSN  uint64
}

// EnableReplication turns on leader-side mutation capture. sink receives one
// batch per committed write, in LSN order starting at startLSN+1. It must be
// called before serving writes.
//
// Enabling capture selects the direct write path (writeCapturing) over the group
// committer, by design: sink runs synchronously on the goroutine that performed the
// write, immediately after the commit, so LSN assignment and publication stay in
// commit order. For a clustered leader that goroutine is the single engine goroutine
// that owns the (not-thread-safe) Replicator — moving publication onto the
// committer's own goroutine would both race that Replicator and reorder LSNs. Group
// commit would not add throughput here anyway: clustered writes are already
// serialized one-at-a-time through the engine, so a batch is only ever one write.
func (s *BoltStore) EnableReplication(startLSN uint64, sink func(MutationBatch)) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.cap = &captureBuf{}
	s.replLSN = startLSN
	s.replSink = sink
}

// SnapshotTo writes a consistent bbolt snapshot of the whole store to w and
// returns the LSN that snapshot reflects. A follower bootstrapping (or catching up
// from far behind) restores this snapshot, then applies replicated batches with
// LSN > the returned value. Taken under the write lock so the snapshot bytes and
// the returned LSN correspond exactly — no write can interleave.
func (s *BoltStore) SnapshotTo(w io.Writer) (uint64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	lsn := s.replLSN
	err := s.db.View(func(tx *bolt.Tx) error {
		_, e := tx.WriteTo(w)
		return e
	})
	return lsn, err
}

// RestoreSnapshot replaces this store's entire local state with the bbolt snapshot
// read from r, and sets the local LSN to lsn. This is the **rejoin rollback**: a
// node that led a losing partition applied writes locally that were never
// cluster-committed (never reached an ack quorum); rather than trying to un-apply
// individual mutations (bbolt has no undo), it discards its diverged database
// wholesale and re-bootstraps from the winning leader's snapshot, then tails the
// stream from lsn. The swap is atomic (write temp, fsync, close, rename, reopen)
// so a crash mid-restore leaves either the old or the new database, never a torn
// one.
func (s *BoltStore) RestoreSnapshot(r io.Reader, lsn uint64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	dbPath := filepath.Join(s.cfg.DataDir, "corndogs.bolt")
	tmpPath := dbPath + ".restore"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	// Validate the snapshot opens as bbolt before we throw away the live db.
	check, err := bolt.Open(tmpPath, 0o600, bolt.DefaultOptions)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("restore: snapshot is not a valid bolt db: %w", err)
	}
	_ = check.Close()

	opts := *bolt.DefaultOptions
	opts.NoSync = s.cfg.Sync == SyncInterval || s.cfg.Sync == SyncNever

	if err := s.db.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dbPath); err != nil {
		// The rename failed, so the original db is still at dbPath. Reopen it so we
		// don't leave the store holding a closed handle (which would brick the node
		// on every subsequent op); surface the rename error to the caller.
		if db, oerr := bolt.Open(dbPath, 0o600, &opts); oerr == nil {
			s.db = db
		}
		return err
	}
	// The snapshot (validated above) is now at dbPath; reopen it as the live db.
	db, err := bolt.Open(dbPath, 0o600, &opts)
	if err != nil {
		return fmt.Errorf("restore: reopen after swap failed: %w", err)
	}
	s.db = db
	s.replLSN = lsn
	return nil
}

// DB exposes the underlying bbolt handle for snapshot/verification use by the
// clustering layer. Callers must not mutate it directly — writes go through the
// store so they are captured for replication.
func (s *BoltStore) DB() *bolt.DB { return s.db }

// ReplLSN returns the store's current replication LSN (the last captured batch on
// a leader, or the last applied batch on a follower).
func (s *BoltStore) ReplLSN() uint64 {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.replLSN
}

// ApplyReplicated applies one replicated batch on a follower and advances the
// local LSN. Batches must arrive gap-free (b.LSN == ReplLSN()+1); the caller
// (the replication coordinator) requests catch-up on a gap.
func (s *BoltStore) ApplyReplicated(b MutationBatch) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if b.LSN != s.replLSN+1 {
		return fmt.Errorf("filestore: non-contiguous replicated batch LSN %d (have %d)", b.LSN, s.replLSN)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error { return applyBatch(tx, b) }); err != nil {
		return err
	}
	s.replLSN = b.LSN
	return nil
}

// write routes a write transaction through the group committer when enabled,
// otherwise commits it directly. Either way it returns only after the write is
// committed (and, except in never/interval modes, durably fsync'd).
func (s *BoltStore) write(fn func(tx *bolt.Tx) error) error {
	if s.replSink != nil {
		// Capture-enabled (clustered leader) writes take the direct path so the batch
		// is published synchronously on this goroutine — see EnableReplication.
		return s.writeCapturing(fn)
	}
	if s.committer != nil {
		return s.committer.submit(fn)
	}
	return s.db.Update(fn)
}

// writeCapturing runs one write while recording its physical mutations, then
// publishes them as a single LSN-stamped batch iff the transaction committed and
// actually changed something. The write mutex makes capture + LSN assignment
// atomic with respect to other writers.
func (s *BoltStore) writeCapturing(fn func(tx *bolt.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.cap.reset()
	if err := s.db.Update(fn); err != nil {
		s.cap.reset()
		return err
	}
	if len(s.cap.muts) > 0 {
		s.replLSN++
		batch := MutationBatch{LSN: s.replLSN, Mutations: append([]Mutation(nil), s.cap.muts...)}
		s.replSink(batch)
	}
	s.cap.reset()
	return nil
}

// NewBoltStore constructs an unopened BoltStore. Call Initialize to open it.
func NewBoltStore(cfg Config) *BoltStore { return &BoltStore{cfg: cfg} }

func (s *BoltStore) Initialize() (func(), error) {
	if err := os.MkdirAll(s.cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	opts := *bolt.DefaultOptions
	// bbolt has no built-in periodic sync; it only knows fsync-every-commit
	// (NoSync=false) or never (NoSync=true). So "always" and "group" both keep
	// the default per-commit fsync (group just commits many writes per txn),
	// while "interval" and "never" disable it — "interval" then re-adds
	// durability via a background db.Sync() ticker below.
	opts.NoSync = s.cfg.Sync == SyncInterval || s.cfg.Sync == SyncNever
	db, err := bolt.Open(filepath.Join(s.cfg.DataDir, "corndogs.bolt"), 0o600, &opts)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{
			bucketTasks,
			bucketByUUID,
			bucketPayloads,
			bucketDeadlines,
			bucketArchived,
			bucketMeta,
		} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateTaskStorage(db); err != nil {
		db.Close()
		return nil, err
	}
	audit, err := NewAuditLog(s.cfg.AuditDir, s.cfg.AuditEnabled, s.cfg.Sync, s.cfg.AuditChunkMB)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.db, s.audit = db, audit
	if s.cfg.Sync == SyncInterval {
		s.stop = make(chan struct{})
		go s.syncLoop()
	}
	if s.cfg.Sync == SyncGroup {
		s.committer = newCommitter(db, s.cfg.GroupMaxBatch, s.cfg.GroupMaxDelay)
	}
	return func() {
		if s.stop != nil {
			close(s.stop)
		}
		if s.committer != nil {
			s.committer.close()
		}
		_ = s.db.Sync()
		_ = s.audit.Close()
		_ = s.db.Close()
	}, nil
}

// syncLoop flushes the memory-mapped data to disk once a second, giving the
// "interval" durability mode bounded data loss (~1s) without an fsync on the
// hot path.
func (s *BoltStore) syncLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			_ = s.db.Sync()
		}
	}
}

// putTask writes a task and its uuid index entry within a write transaction, and
// records the two puts for replication when capture is enabled.
func (s *BoltStore) putTask(tx *bolt.Tx, t *Task) error {
	val, err := json.Marshal(t)
	if err != nil {
		return err
	}
	key := encodeTaskKey(t)
	if err := tx.Bucket(bucketTasks).Put(key, val); err != nil {
		return err
	}
	if err := tx.Bucket(bucketByUUID).Put([]byte(t.UUID), key); err != nil {
		return err
	}
	if t.Timeout > 0 {
		if err := tx.Bucket(bucketDeadlines).Put(encodeDeadlineKey(t), key); err != nil {
			return err
		}
	}
	if s.cap != nil {
		s.cap.put(bucketTasks, key, val)
		s.cap.put(bucketByUUID, []byte(t.UUID), key)
		if t.Timeout > 0 {
			s.cap.put(bucketDeadlines, encodeDeadlineKey(t), key)
		}
	}
	return nil
}

// deleteTask removes a task (by its current key) and its uuid index entry, and
// records the two deletes for replication when capture is enabled.
func (s *BoltStore) deleteTask(tx *bolt.Tx, t *Task) error {
	key := encodeTaskKey(t)
	if err := tx.Bucket(bucketTasks).Delete(key); err != nil {
		return err
	}
	if err := tx.Bucket(bucketByUUID).Delete([]byte(t.UUID)); err != nil {
		return err
	}
	if t.Timeout > 0 {
		if err := tx.Bucket(bucketDeadlines).Delete(encodeDeadlineKey(t)); err != nil {
			return err
		}
	}
	if s.cap != nil {
		s.cap.del(bucketTasks, key)
		s.cap.del(bucketByUUID, []byte(t.UUID))
		if t.Timeout > 0 {
			s.cap.del(bucketDeadlines, encodeDeadlineKey(t))
		}
	}
	return nil
}

// putPayload stores opaque bytes without JSON or base64 conversion.
func (s *BoltStore) putPayload(tx *bolt.Tx, id string, payload []byte) error {
	if payload == nil {
		payload = []byte{}
	}
	if err := tx.Bucket(bucketPayloads).Put([]byte(id), payload); err != nil {
		return err
	}
	if s.cap != nil {
		s.cap.put(bucketPayloads, []byte(id), payload)
	}
	return nil
}

func (s *BoltStore) deletePayload(tx *bolt.Tx, id string) error {
	if err := tx.Bucket(bucketPayloads).Delete([]byte(id)); err != nil {
		return err
	}
	if s.cap != nil {
		s.cap.del(bucketPayloads, []byte(id))
	}
	return nil
}

func loadPayload(tx *bolt.Tx, id string) ([]byte, error) {
	payload := tx.Bucket(bucketPayloads).Get([]byte(id))
	if payload == nil {
		return nil, fmt.Errorf("filestore: payload for task %s is missing", id)
	}
	return bytes.Clone(payload), nil
}

// loadByUUID fetches live task metadata by uuid, or nil if absent.
func loadByUUID(tx *bolt.Tx, id string) (*Task, error) {
	key := tx.Bucket(bucketByUUID).Get([]byte(id))
	if key == nil {
		return nil, nil
	}
	val := tx.Bucket(bucketTasks).Get(key)
	if val == nil {
		return nil, nil
	}
	var t Task
	if err := json.Unmarshal(val, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *BoltStore) SubmitTask(ctx context.Context, req *api.SubmitTaskRequest) (*api.SubmitTaskResponse, error) {
	id, _ := uuid.NewRandom()
	now := nowNano()
	t := &Task{
		UUID:            id.String(),
		Queue:           req.Queue,
		CurrentState:    req.CurrentState,
		AutoTargetState: req.AutoTargetState,
		SubmitTime:      now,
		UpdateTime:      now,
		Timeout:         req.Timeout,
		Priority:        req.Priority,
	}
	err := s.write(func(tx *bolt.Tx) error {
		if err := s.putPayload(tx, t.UUID, req.Payload); err != nil {
			return err
		}
		return s.putTask(tx, t)
	})
	if err != nil {
		return nil, err
	}
	s.audit.Record(AuditEvent{Op: "submit", UUID: t.UUID, Queue: t.Queue, ToState: t.CurrentState, Priority: t.Priority})
	return &api.SubmitTaskResponse{Task: toAPITask(t)}, nil
}

func (s *BoltStore) MustGetTaskStateByID(ctx context.Context, req *api.GetTaskStateByIDRequest) (*api.GetTaskStateByIDResponse, error) {
	var out *api.Task
	err := s.db.View(func(tx *bolt.Tx) error {
		t, err := loadByUUID(tx, req.Uuid)
		if err != nil {
			return err
		}
		if t != nil {
			out = toAPITask(t)
			return nil
		}
		if val := tx.Bucket(bucketArchived).Get([]byte(req.Uuid)); val != nil {
			var a ArchivedTask
			if err := json.Unmarshal(val, &a); err != nil {
				return err
			}
			out = archivedToAPITask(&a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetTaskStateByIDResponse{Task: out}, nil
}

func (s *BoltStore) GetNextTask(ctx context.Context, req *api.GetNextTaskRequest) (*api.GetNextTaskResponse, error) {
	var out *api.TaskDelivery
	err := s.write(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		prefix := taskPrefix(req.Queue, req.CurrentState)
		k, v := c.Seek(prefix)
		if k == nil || !bytes.HasPrefix(k, prefix) {
			return nil // no task available
		}
		var t Task
		if err := json.Unmarshal(v, &t); err != nil {
			return err
		}
		payload, err := loadPayload(tx, t.UUID)
		if err != nil {
			return err
		}
		if err := s.deleteTask(tx, &t); err != nil {
			return err
		}
		applyGetNext(&t, req, nowNano())
		if err := s.putTask(tx, &t); err != nil {
			return err
		}
		out = toAPIDelivery(&t, payload)
		s.audit.Record(AuditEvent{Op: "claim", UUID: t.UUID, Queue: t.Queue, FromState: req.CurrentState, ToState: t.CurrentState})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetNextTaskResponse{Delivery: out}, nil
}

// GetNextTaskGroup claims the single best task across a set of queues. Each
// (queue, current_state) prefix is already stored in (priority desc,
// update_time asc, uuid) order, so the head task under each prefix is that
// queue's best candidate; comparing those heads by the same order yields the
// globally-best task across the group. It respects priority across queues just
// like the postgres backend, and the whole claim happens in one write
// transaction so two workers never claim the same task.
func (s *BoltStore) GetNextTaskGroup(ctx context.Context, req *api.GetNextTaskGroupRequest) (*api.GetNextTaskGroupResponse, error) {
	var out *api.TaskDelivery
	// applyGetNext only reads the Override* fields; reuse the single-queue path's
	// claim semantics unchanged.
	claim := &api.GetNextTaskRequest{
		CurrentState:            req.CurrentState,
		OverrideTimeout:         req.OverrideTimeout,
		OverrideCurrentState:    req.OverrideCurrentState,
		OverrideAutoTargetState: req.OverrideAutoTargetState,
	}
	err := s.write(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		var best *Task
		for _, queue := range req.Queues {
			prefix := taskPrefix(queue, req.CurrentState)
			k, v := c.Seek(prefix)
			if k == nil || !bytes.HasPrefix(k, prefix) {
				continue // no task available in this queue+state
			}
			var t Task
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			if best == nil || betterCandidate(&t, best) {
				cand := t
				best = &cand
			}
		}
		if best == nil {
			return nil // no task available in any queue
		}
		payload, err := loadPayload(tx, best.UUID)
		if err != nil {
			return err
		}
		if err := s.deleteTask(tx, best); err != nil {
			return err
		}
		applyGetNext(best, claim, nowNano())
		if err := s.putTask(tx, best); err != nil {
			return err
		}
		out = toAPIDelivery(best, payload)
		s.audit.Record(AuditEvent{Op: "claim", UUID: best.UUID, Queue: best.Queue, FromState: req.CurrentState, ToState: best.CurrentState})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetNextTaskGroupResponse{Delivery: out}, nil
}

// betterCandidate reports whether task a should be claimed before task b under
// the dequeue order (priority desc, update_time asc, uuid asc) — the same order
// encodeTaskKey imposes within a single (queue, state) prefix, applied here to
// compare heads across different queues.
func betterCandidate(a, b *Task) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if a.UpdateTime != b.UpdateTime {
		return a.UpdateTime < b.UpdateTime
	}
	return a.UUID < b.UUID
}

func (s *BoltStore) UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error) {
	var out *api.Task
	err := s.write(func(tx *bolt.Tx) error {
		t, err := loadByUUID(tx, req.Uuid)
		if err != nil || t == nil {
			return err
		}
		if err := s.deleteTask(tx, t); err != nil {
			return err
		}
		from := t.CurrentState
		t.CurrentState = req.NewState
		t.AutoTargetState = req.AutoTargetState
		t.Timeout = req.Timeout
		t.Priority = req.Priority
		if req.Payload != nil {
			if err := s.putPayload(tx, t.UUID, *req.Payload); err != nil {
				return err
			}
		}
		t.UpdateTime = nowNano()
		if err := s.putTask(tx, t); err != nil {
			return err
		}
		out = toAPITask(t)
		s.audit.Record(AuditEvent{Op: "update", UUID: t.UUID, Queue: t.Queue, FromState: from, ToState: t.CurrentState, Priority: t.Priority})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.UpdateTaskResponse{Task: out}, nil
}

// archiveAndDelete moves a live task to the archive bucket with the given
// terminal state and removes it from the live set. Shared by complete/cancel.
func (s *BoltStore) archiveAndDelete(id, terminalState, op string) (*api.Task, error) {
	var out *api.Task
	err := s.write(func(tx *bolt.Tx) error {
		t, err := loadByUUID(tx, id)
		if err != nil || t == nil {
			return err
		}
		from := t.CurrentState
		a := t.toArchived()
		a.CurrentState = terminalState
		a.AutoTargetState = terminalState
		a.UpdateTime = nowNano()
		val, err := json.Marshal(&a)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucketArchived).Put([]byte(a.UUID), val); err != nil {
			return err
		}
		if s.cap != nil {
			s.cap.put(bucketArchived, []byte(a.UUID), val)
		}
		if err := s.deleteTask(tx, t); err != nil {
			return err
		}
		if err := s.deletePayload(tx, t.UUID); err != nil {
			return err
		}
		out = archivedToAPITask(&a)
		s.audit.Record(AuditEvent{Op: op, UUID: a.UUID, Queue: a.Queue, FromState: from, ToState: terminalState})
		return nil
	})
	return out, err
}

func (s *BoltStore) CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error) {
	out, err := s.archiveAndDelete(req.Uuid, "completed", "complete")
	if err != nil {
		return nil, err
	}
	return &api.CompleteTaskResponse{Task: out}, nil
}

func (s *BoltStore) CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error) {
	out, err := s.archiveAndDelete(req.Uuid, "canceled", "cancel")
	if err != nil {
		return nil, err
	}
	return &api.CancelTaskResponse{Task: out}, nil
}

func (s *BoltStore) CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error) {
	var count int64
	err := s.write(func(tx *bolt.Tx) error {
		// Collect first because task updates also update the deadline index.
		var expired []Task
		c := tx.Bucket(bucketDeadlines).Cursor()
		for deadlineKey, taskKey := c.First(); deadlineKey != nil; deadlineKey, taskKey = c.Next() {
			if len(deadlineKey) < 8 || decodeTimeAsc(deadlineKey[:8]) >= req.AtTime {
				break
			}
			v := tx.Bucket(bucketTasks).Get(taskKey)
			if v == nil {
				continue
			}
			var t Task
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			// Ignore a stale index entry defensively. Normal writes maintain both
			// buckets in one transaction.
			if !bytes.Equal(deadlineKey, encodeDeadlineKey(&t)) || !timedOut(&t, req.AtTime) {
				continue
			}
			if req.Queue != "" && req.Queue != t.Queue {
				continue
			}
			expired = append(expired, t)
		}
		for i := range expired {
			t := expired[i]
			if err := s.deleteTask(tx, &t); err != nil {
				return err
			}
			t.CurrentState, t.AutoTargetState = t.AutoTargetState, t.CurrentState
			t.Timeout = 0
			t.UpdateTime = nowNano()
			if err := s.putTask(tx, &t); err != nil {
				return err
			}
			s.audit.Record(AuditEvent{Op: "timeout", UUID: t.UUID, Queue: t.Queue, ToState: t.CurrentState})
			count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.CleanUpTimedOutResponse{TimedOut: count}, nil
}

// --- metrics: computed by scanning keys (queue/state parsed from the key,
// avoiding a full JSON decode), mirroring postgres' GROUP BY scans. ---

func (s *BoltStore) GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error) {
	seen := map[string]struct{}{}
	queues := []string{}
	var count int64
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			count++
			q, _ := parseKeyQueueState(k)
			if _, ok := seen[q]; !ok {
				seen[q] = struct{}{}
				queues = append(queues, q)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetQueuesResponse{Queues: queues, TotalTaskCount: count}, nil
}

func (s *BoltStore) GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error) {
	counts := api.StringInt64Map{}
	var total int64
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			total++
			q, _ := parseKeyQueueState(k)
			counts[q]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetQueueTaskCountsResponse{QueueCounts: counts, TotalTaskCount: total}, nil
}

func (s *BoltStore) GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error) {
	counts := api.StringInt64Map{}
	var total int64
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		prefix := []byte(req.Queue + string(rune(sep)))
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			total++
			_, st := parseKeyQueueState(k)
			counts[st]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetTaskStateCountsResponse{Queue: req.Queue, Count: total, StateCounts: counts}, nil
}

func (s *BoltStore) GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error) {
	result := api.QueueAndStateCountsMap{}
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			q, st := parseKeyQueueState(k)
			entry, ok := result[q]
			if !ok {
				entry = api.QueueAndStateCounts{Queue: q, StateCounts: api.StringInt64Map{}}
			}
			entry.StateCounts[st]++
			entry.Count++
			result[q] = entry
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetQueueAndStateCountsResponse{QueueAndStateCounts: result}, nil
}

// parseKeyQueueState extracts the queue and state from a task key of the form
// queue<sep>state<sep>... without a full JSON decode.
func parseKeyQueueState(key []byte) (queue, state string) {
	i := bytes.IndexByte(key, sep)
	if i < 0 {
		return string(key), ""
	}
	queue = string(key[:i])
	rest := key[i+1:]
	j := bytes.IndexByte(rest, sep)
	if j < 0 {
		return queue, string(rest)
	}
	return queue, string(rest[:j])
}
