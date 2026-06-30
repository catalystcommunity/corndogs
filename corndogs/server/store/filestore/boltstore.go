package filestore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Bucket names for the bbolt backend.
var (
	bucketTasks    = []byte("tasks")    // ordered key -> json(Task)
	bucketByUUID   = []byte("uuid")     // uuid -> ordered key
	bucketArchived = []byte("archived") // uuid -> json(ArchivedTask)
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
}

// write routes a write transaction through the group committer when enabled,
// otherwise commits it directly. Either way it returns only after the write is
// committed (and, except in never/interval modes, durably fsync'd).
func (s *BoltStore) write(fn func(tx *bolt.Tx) error) error {
	if s.committer != nil {
		return s.committer.submit(fn)
	}
	return s.db.Update(fn)
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
		for _, b := range [][]byte{bucketTasks, bucketByUUID, bucketArchived} {
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

// putTask writes a task and its uuid index entry within a write transaction.
func putTask(tx *bolt.Tx, t *Task) error {
	val, err := json.Marshal(t)
	if err != nil {
		return err
	}
	key := encodeTaskKey(t)
	if err := tx.Bucket(bucketTasks).Put(key, val); err != nil {
		return err
	}
	return tx.Bucket(bucketByUUID).Put([]byte(t.UUID), key)
}

// deleteTask removes a task (by its current key) and its uuid index entry.
func deleteTask(tx *bolt.Tx, t *Task) error {
	if err := tx.Bucket(bucketTasks).Delete(encodeTaskKey(t)); err != nil {
		return err
	}
	return tx.Bucket(bucketByUUID).Delete([]byte(t.UUID))
}

// loadByUUID fetches a live task by uuid, or nil if absent.
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
		Payload:         req.Payload,
	}
	err := s.write(func(tx *bolt.Tx) error { return putTask(tx, t) })
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
	var out *api.Task
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
		if err := deleteTask(tx, &t); err != nil {
			return err
		}
		applyGetNext(&t, req, nowNano())
		if err := putTask(tx, &t); err != nil {
			return err
		}
		out = toAPITask(&t)
		s.audit.Record(AuditEvent{Op: "claim", UUID: t.UUID, Queue: t.Queue, FromState: req.CurrentState, ToState: t.CurrentState})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &api.GetNextTaskResponse{Task: out}, nil
}

func (s *BoltStore) UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error) {
	var out *api.Task
	err := s.write(func(tx *bolt.Tx) error {
		t, err := loadByUUID(tx, req.Uuid)
		if err != nil || t == nil {
			return err
		}
		if err := deleteTask(tx, t); err != nil {
			return err
		}
		from := t.CurrentState
		t.CurrentState = req.NewState
		t.AutoTargetState = req.AutoTargetState
		t.Timeout = req.Timeout
		t.Priority = req.Priority
		if len(req.Payload) > 0 {
			t.Payload = req.Payload
		}
		t.UpdateTime = nowNano()
		if err := putTask(tx, t); err != nil {
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
		if err := deleteTask(tx, t); err != nil {
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
		// Collect first; mutating the bucket during cursor iteration is unsafe.
		var expired []Task
		c := tx.Bucket(bucketTasks).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var t Task
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			if !timedOut(&t, req.AtTime) {
				continue
			}
			if req.Queue != "" && req.Queue != t.Queue {
				continue
			}
			expired = append(expired, t)
		}
		for i := range expired {
			t := expired[i]
			if err := deleteTask(tx, &t); err != nil {
				return err
			}
			t.CurrentState, t.AutoTargetState = t.AutoTargetState, t.CurrentState
			t.Timeout = 0
			t.UpdateTime = nowNano()
			if err := putTask(tx, &t); err != nil {
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
