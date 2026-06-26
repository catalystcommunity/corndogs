// Package filestore provides embedded, single-process storage backends for
// corndogs as an alternative to postgres. It is intended for operators who do
// not want to run a separate database server and accept the tradeoff that the
// data file is owned by exactly one process (no horizontal scale-out / HA).
//
// The backend is BoltStore: go.etcd.io/bbolt (MIT), a memory-mapped B+tree. The
// priority dequeue falls out of an ordered composite key, so GetNextTask is a
// single cursor Seek + mutate inside one write transaction. (A buntdb prototype
// was evaluated and dropped: bbolt was ~2x faster on the dequeue hot path.)
//
// It replaces postgres' SELECT ... FOR UPDATE SKIP LOCKED with in-process
// concurrency control: a single writer (held for microseconds) plus concurrent
// readers, with group commit to amortize fsync. That is correct and fast
// precisely because everything is one process.
package filestore

// Task mirrors the active task record. The json tags match both the gorm model
// used by the postgres backend and the generated CSIL api.Task, so
// conversions.CopyStruct round-trips through any of them.
type Task struct {
	UUID            string `json:"uuid"`
	Queue           string `json:"queue"`
	CurrentState    string `json:"current_state"`
	AutoTargetState string `json:"auto_target_state"`
	SubmitTime      int64  `json:"submit_time"`
	UpdateTime      int64  `json:"update_time"`
	Timeout         int64  `json:"timeout"`
	Priority        int64  `json:"priority"`
	Payload         []byte `json:"payload"`
}

// ArchivedTask mirrors the postgres archived_tasks row: the payload is
// intentionally dropped on archive.
type ArchivedTask struct {
	UUID            string `json:"uuid"`
	Queue           string `json:"queue"`
	CurrentState    string `json:"current_state"`
	AutoTargetState string `json:"auto_target_state"`
	SubmitTime      int64  `json:"submit_time"`
	UpdateTime      int64  `json:"update_time"`
}

// toArchived converts an active task to its archived form, dropping the payload
// exactly like postgresstore.models.ConvertTaskForArchive.
func (t *Task) toArchived() ArchivedTask {
	return ArchivedTask{
		UUID:            t.UUID,
		Queue:           t.Queue,
		CurrentState:    t.CurrentState,
		AutoTargetState: t.AutoTargetState,
		SubmitTime:      t.SubmitTime,
		UpdateTime:      t.UpdateTime,
	}
}
