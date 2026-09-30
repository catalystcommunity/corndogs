// Package filestore stores task data in a local bbolt file. One process owns
// each file. The clustering package can replicate separate local files.
package filestore

// Task is the active task metadata. Payload bytes live in bucketPayloads and are
// never JSON encoded with this record.
type Task struct {
	UUID            string `json:"uuid"`
	Queue           string `json:"queue"`
	CurrentState    string `json:"current_state"`
	AutoTargetState string `json:"auto_target_state"`
	SubmitTime      int64  `json:"submit_time"`
	UpdateTime      int64  `json:"update_time"`
	Timeout         int64  `json:"timeout"`
	Priority        int64  `json:"priority"`
	// Revision changes on each mutation. Tasks written before 0.8.0 have 0.
	Revision int64 `json:"revision,omitempty"`
	// Guarded tasks live in bucketGuarded and reject legacy mutations.
	Guarded bool `json:"guarded,omitempty"`
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
	Revision        int64  `json:"revision,omitempty"`
	Guarded         bool   `json:"guarded,omitempty"`
}

// toArchived returns the archive record. Archiving is a mutation, so the
// revision increases by one.
func (t *Task) toArchived() ArchivedTask {
	return ArchivedTask{
		UUID:            t.UUID,
		Queue:           t.Queue,
		CurrentState:    t.CurrentState,
		AutoTargetState: t.AutoTargetState,
		SubmitTime:      t.SubmitTime,
		UpdateTime:      t.UpdateTime,
		Revision:        t.Revision + 1,
		Guarded:         t.Guarded,
	}
}

// taskBucket returns the bucket that holds t: guarded tasks are separate, so a
// legacy claim never sees them and needs no filter.
func taskBucket(t *Task) []byte {
	if t.Guarded {
		return bucketGuarded
	}
	return bucketTasks
}
