package postgresstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/postgresstore/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errReceiptRace means that a concurrent transaction stored a receipt with the
// same key first. The caller rolls back and runs again, and then replays that
// receipt.
var errReceiptRace = errors.New("postgresstore: concurrent receipt; retry")

// maxReceiptRaceRetries bounds the reruns after errReceiptRace. One rerun is
// enough: the winning receipt is committed when the conflict is reported.
const maxReceiptRaceRetries = 3

func isServiceError(err error) bool {
	var se *api.ServiceError
	return errors.As(err, &se)
}

type submissionRow struct {
	Queue         string
	SubmissionKey string
	RequestDigest []byte
	TaskUUID      string
	AcceptedAt    int64
	ExpiresAt     int64
	Guarded       bool
}

func (r *submissionRow) receipt() *api.SubmissionReceipt {
	return &api.SubmissionReceipt{
		Queue:         r.Queue,
		SubmissionKey: r.SubmissionKey,
		TaskUuid:      r.TaskUUID,
		AcceptedAt:    r.AcceptedAt,
		ExpiresAt:     r.ExpiresAt,
		Guarded:       r.Guarded,
	}
}

// resultJSON is the task state after a guarded mutation, as stored in
// operation_receipts.result.
type resultJSON struct {
	Task     api.Task `json:"task"`
	Revision int64    `json:"revision"`
	Guarded  bool     `json:"guarded"`
	Terminal bool     `json:"terminal"`
}

func (r *resultJSON) view() *api.GuardedTask {
	return &api.GuardedTask{Task: r.Task, Guarded: r.Guarded, Revision: r.Revision, Terminal: r.Terminal}
}

type operationRow struct {
	OperationID   string
	Op            string
	RequestDigest []byte
	TaskUUID      string
	Queue         string
	At            int64
	ExpiresAt     int64
	Result        []byte
}

func (r *operationRow) result() (*resultJSON, error) {
	var out resultJSON
	if err := json.Unmarshal(r.Result, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *operationRow) receipt() (*api.OperationReceipt, error) {
	res, err := r.result()
	if err != nil {
		return nil, err
	}
	return &api.OperationReceipt{
		OperationId:    r.OperationID,
		Op:             r.Op,
		TaskUuid:       r.TaskUUID,
		Queue:          r.Queue,
		ResultRevision: res.Revision,
		ResultState:    res.Task.CurrentState,
		At:             r.At,
		ExpiresAt:      r.ExpiresAt,
	}, nil
}

func guardedView(m *models.Task) *api.GuardedTask {
	return &api.GuardedTask{Task: *taskToAPI(m), Guarded: m.Guarded, Revision: m.Revision}
}

func archivedGuardedView(a *models.ArchivedTask) *api.GuardedTask {
	return &api.GuardedTask{Task: *archivedTaskToAPI(a), Guarded: a.Guarded, Revision: a.Revision, Terminal: true}
}

// currentView returns the live or archived view of id, or nil.
func currentView(tx *gorm.DB, id string) (*api.GuardedTask, error) {
	var m models.Task
	res := tx.Select(taskMetadataColumns).Where("uuid = ?", id).Limit(1).Find(&m)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 1 {
		return guardedView(&m), nil
	}
	var a models.ArchivedTask
	res = tx.Where("uuid = ?", id).Limit(1).Find(&a)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return archivedGuardedView(&a), nil
}

// withReceiptRetry runs fn in a transaction and runs it again after
// errReceiptRace.
func withReceiptRetry(ctx context.Context, fn func(tx *gorm.DB) error) error {
	var err error
	for i := 0; i < maxReceiptRaceRetries; i++ {
		err = DB.WithContext(ctx).Transaction(fn)
		if !errors.Is(err, errReceiptRace) {
			return err
		}
	}
	return err
}

func (s PostgresStore) SubmitKeyedTask(ctx context.Context, req *api.SubmitKeyedTaskRequest, ro store.ReceiptOptions) (*api.SubmitKeyedTaskResponse, error) {
	var out *api.SubmitKeyedTaskResponse
	err := withReceiptRetry(ctx, func(tx *gorm.DB) error {
		now := time.Now().UnixNano()
		id, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		rec := submissionRow{
			Queue: req.Queue, SubmissionKey: req.SubmissionKey, RequestDigest: ro.Digest,
			TaskUUID: id.String(), AcceptedAt: now, ExpiresAt: now + ro.Retention.Nanoseconds(), Guarded: req.Guarded,
		}
		// Claim the key first. ON CONFLICT waits for a concurrent insert of the
		// same key to finish, then either takes over an expired record or
		// leaves the live record in place.
		res := tx.Exec(`INSERT INTO submission_receipts
			(queue, submission_key, request_digest, task_uuid, accepted_at, expires_at, guarded)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (queue, submission_key) DO UPDATE SET
				request_digest = EXCLUDED.request_digest, task_uuid = EXCLUDED.task_uuid,
				accepted_at = EXCLUDED.accepted_at, expires_at = EXCLUDED.expires_at, guarded = EXCLUDED.guarded
			WHERE submission_receipts.expires_at <= ?`,
			rec.Queue, rec.SubmissionKey, rec.RequestDigest, rec.TaskUUID, rec.AcceptedAt, rec.ExpiresAt, rec.Guarded, now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			var prev submissionRow
			q := tx.Raw(`SELECT queue, submission_key, request_digest, task_uuid::text AS task_uuid, accepted_at, expires_at, guarded
				FROM submission_receipts WHERE queue = ? AND submission_key = ?`, req.Queue, req.SubmissionKey).Scan(&prev)
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected == 0 {
				return errReceiptRace
			}
			if !bytes.Equal(prev.RequestDigest, ro.Digest) {
				return store.SubmissionKeyConflict(req.Queue, req.SubmissionKey)
			}
			view, err := currentView(tx, prev.TaskUUID)
			if err != nil {
				return err
			}
			out = &api.SubmitKeyedTaskResponse{Receipt: *prev.receipt(), Replayed: true, Task: view}
			return nil
		}
		model := models.Task{
			UUID:            rec.TaskUUID,
			Queue:           req.Queue,
			CurrentState:    req.CurrentState,
			AutoTargetState: req.AutoTargetState,
			SubmitTime:      now,
			UpdateTime:      now,
			Timeout:         req.Timeout,
			Priority:        req.Priority,
			Payload:         req.Payload,
			Revision:        1,
			Guarded:         req.Guarded,
		}
		if err := tx.Create(&model).Error; err != nil {
			return err
		}
		out = &api.SubmitKeyedTaskResponse{Receipt: *rec.receipt(), Task: guardedView(&model)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s PostgresStore) LookupSubmission(ctx context.Context, req *api.LookupSubmissionRequest) (*api.LookupSubmissionResponse, error) {
	out := &api.LookupSubmissionResponse{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rec submissionRow
		q := tx.Raw(`SELECT queue, submission_key, request_digest, task_uuid::text AS task_uuid, accepted_at, expires_at, guarded
			FROM submission_receipts WHERE queue = ? AND submission_key = ? AND expires_at > ?`,
			req.Queue, req.SubmissionKey, time.Now().UnixNano()).Scan(&rec)
		if q.Error != nil || q.RowsAffected == 0 {
			return q.Error
		}
		out.Receipt = rec.receipt()
		view, err := currentView(tx, rec.TaskUUID)
		out.Task = view
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loadOperation returns the unexpired receipt for id, or nil. It checks that
// the same request reused the id.
func loadOperation(tx *gorm.DB, id, op string, digest []byte, now int64) (*operationRow, error) {
	var rec operationRow
	q := tx.Raw(`SELECT operation_id, op, request_digest, task_uuid::text AS task_uuid, queue, at, expires_at, result
		FROM operation_receipts WHERE operation_id = ? AND expires_at > ?`, id, now).Scan(&rec)
	if q.Error != nil || q.RowsAffected == 0 {
		return nil, q.Error
	}
	if op != "" && (rec.Op != op || !bytes.Equal(rec.RequestDigest, digest)) {
		return nil, store.OperationConflict(id, rec.Op)
	}
	return &rec, nil
}

// storeOperation inserts a receipt. It returns errReceiptRace when a
// concurrent transaction stored a live receipt with the same id first.
func storeOperation(tx *gorm.DB, id, op string, ro store.ReceiptOptions, taskUUID, queue string, now int64, result *resultJSON) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	res := tx.Exec(`INSERT INTO operation_receipts
		(operation_id, op, request_digest, task_uuid, queue, at, expires_at, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (operation_id) DO UPDATE SET
			op = EXCLUDED.op, request_digest = EXCLUDED.request_digest, task_uuid = EXCLUDED.task_uuid,
			queue = EXCLUDED.queue, at = EXCLUDED.at, expires_at = EXCLUDED.expires_at, result = EXCLUDED.result
		WHERE operation_receipts.expires_at <= ?`,
		id, op, ro.Digest, taskUUID, queue, now, now+ro.Retention.Nanoseconds(), body, now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errReceiptRace
	}
	return nil
}

func (s PostgresStore) ClaimGuardedTask(ctx context.Context, req *api.ClaimGuardedTaskGroupRequest, ro store.ReceiptOptions) (*api.ClaimGuardedTaskGroupResponse, error) {
	out := &api.ClaimGuardedTaskGroupResponse{}
	err := withReceiptRetry(ctx, func(tx *gorm.DB) error {
		*out = api.ClaimGuardedTaskGroupResponse{}
		now := time.Now().UnixNano()
		rec, err := loadOperation(tx, req.OperationId, store.OpClaim, ro.Digest, now)
		if err != nil {
			return err
		}
		if rec != nil {
			res, err := rec.result()
			if err != nil {
				return err
			}
			var m models.Task
			q := tx.Where("uuid = ?", rec.TaskUUID).Limit(1).Find(&m)
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected == 0 || m.Revision != res.Revision {
				return store.ClaimSuperseded(req.OperationId, rec.TaskUUID)
			}
			out.Delivery = &api.GuardedDelivery{Task: *guardedView(&m), Payload: m.Payload}
			out.Replayed = true
			return nil
		}
		var m models.Task
		q := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("queue IN ? AND current_state = ?", req.Queues, req.CurrentState).
			Order("priority DESC, update_time ASC").
			Limit(1).
			Find(&m)
		if q.Error != nil || q.RowsAffected == 0 {
			return q.Error
		}
		m.CurrentState, m.AutoTargetState = m.AutoTargetState, req.CurrentState
		if req.OverrideCurrentState != "" {
			m.CurrentState = req.OverrideCurrentState
		}
		if req.OverrideAutoTargetState != "" {
			m.AutoTargetState = req.OverrideAutoTargetState
		}
		if req.OverrideTimeout < 0 {
			m.Timeout = 0
		} else if req.OverrideTimeout != 0 {
			m.Timeout = req.OverrideTimeout
		}
		m.UpdateTime = now
		m.Revision++
		if err := tx.Model(&models.Task{}).Where("uuid = ?", m.UUID).Updates(map[string]interface{}{
			"current_state":     m.CurrentState,
			"auto_target_state": m.AutoTargetState,
			"timeout":           m.Timeout,
			"update_time":       m.UpdateTime,
			"revision":          m.Revision,
		}).Error; err != nil {
			return err
		}
		if err := storeOperation(tx, req.OperationId, store.OpClaim, ro, m.UUID, m.Queue, now,
			&resultJSON{Task: *taskToAPI(&m), Revision: m.Revision, Guarded: m.Guarded}); err != nil {
			return err
		}
		out.Delivery = &api.GuardedDelivery{Task: *guardedView(&m), Payload: m.Payload}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lockGuarded locks the live task for a guarded mutation and checks the queue,
// revision, and expected state.
func lockGuarded(tx *gorm.DB, id, queue string, expectedRevision int64, expectedState *string) (*models.Task, error) {
	var m models.Task
	q := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(taskMetadataColumns).Where("uuid = ?", id).Limit(1).Find(&m)
	if q.Error != nil {
		return nil, q.Error
	}
	if q.RowsAffected == 0 {
		var a models.ArchivedTask
		q = tx.Where("uuid = ?", id).Limit(1).Find(&a)
		if q.Error != nil {
			return nil, q.Error
		}
		if q.RowsAffected == 0 {
			return nil, store.TaskNotFound(id)
		}
		return nil, store.RevisionConflict(id, "task is %s at revision %d", a.CurrentState, a.Revision)
	}
	if m.Queue != queue {
		return nil, store.RevisionConflict(id, "task is in queue %q, not %q", m.Queue, queue)
	}
	if m.Revision != expectedRevision {
		return nil, store.RevisionConflict(id, "revision is %d, expected %d", m.Revision, expectedRevision)
	}
	if expectedState != nil && m.CurrentState != *expectedState {
		return nil, store.RevisionConflict(id, "state is %q, expected %q", m.CurrentState, *expectedState)
	}
	return &m, nil
}

// replayLocked checks the receipt of a guarded mutation. It runs after the task
// row lock (when the task exists), so a concurrent identical request that
// committed first is visible here.
func replayLocked(tx *gorm.DB, id, op string, ro store.ReceiptOptions, now int64) (*resultJSON, error) {
	rec, err := loadOperation(tx, id, op, ro.Digest, now)
	if err != nil || rec == nil {
		return nil, err
	}
	return rec.result()
}

// lockTaskRow takes the row lock of a live task, if there is one, so that
// requests for the same task serialize before their receipts are checked.
func lockTaskRow(tx *gorm.DB, id string) error {
	return tx.Exec(`SELECT 1 FROM tasks WHERE uuid = ? FOR UPDATE`, id).Error
}

func (s PostgresStore) UpdateGuardedTask(ctx context.Context, req *api.UpdateGuardedTaskRequest, ro store.ReceiptOptions) (*api.UpdateGuardedTaskResponse, error) {
	var out *api.UpdateGuardedTaskResponse
	err := withReceiptRetry(ctx, func(tx *gorm.DB) error {
		now := time.Now().UnixNano()
		if err := lockTaskRow(tx, req.Uuid); err != nil {
			return err
		}
		prev, err := replayLocked(tx, req.OperationId, store.OpUpdate, ro, now)
		if err != nil {
			return err
		}
		if prev != nil {
			out = &api.UpdateGuardedTaskResponse{Task: *prev.view(), Replayed: true}
			return nil
		}
		m, err := lockGuarded(tx, req.Uuid, req.Queue, req.ExpectedRevision, req.ExpectedState)
		if err != nil {
			return err
		}
		m.CurrentState = req.NewState
		m.AutoTargetState = req.AutoTargetState
		m.Timeout = req.Timeout
		m.UpdateTime = now
		m.Revision++
		updates := map[string]interface{}{
			"current_state":     m.CurrentState,
			"auto_target_state": m.AutoTargetState,
			"timeout":           m.Timeout,
			"update_time":       m.UpdateTime,
			"revision":          m.Revision,
		}
		if req.Priority != nil {
			m.Priority = *req.Priority
			updates["priority"] = m.Priority
		}
		if req.Payload != nil {
			updates["payload"] = *req.Payload
		}
		if err := tx.Model(&models.Task{}).Where("uuid = ?", m.UUID).Updates(updates).Error; err != nil {
			return err
		}
		result := &resultJSON{Task: *taskToAPI(m), Revision: m.Revision, Guarded: m.Guarded}
		if err := storeOperation(tx, req.OperationId, store.OpUpdate, ro, m.UUID, m.Queue, now, result); err != nil {
			return err
		}
		out = &api.UpdateGuardedTaskResponse{Task: *result.view()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s PostgresStore) FinishGuardedTask(ctx context.Context, req *api.CompleteGuardedTaskRequest, op string, ro store.ReceiptOptions) (*api.CompleteGuardedTaskResponse, error) {
	var out *api.CompleteGuardedTaskResponse
	terminal := store.TerminalState(op)
	err := withReceiptRetry(ctx, func(tx *gorm.DB) error {
		now := time.Now().UnixNano()
		if err := lockTaskRow(tx, req.Uuid); err != nil {
			return err
		}
		prev, err := replayLocked(tx, req.OperationId, op, ro, now)
		if err != nil {
			return err
		}
		if prev != nil {
			out = &api.CompleteGuardedTaskResponse{Task: *prev.view(), Replayed: true}
			return nil
		}
		m, err := lockGuarded(tx, req.Uuid, req.Queue, req.ExpectedRevision, req.ExpectedState)
		if err != nil {
			return err
		}
		a := models.ConvertTaskForArchive(*m)
		a.CurrentState = terminal
		a.AutoTargetState = terminal
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Task{UUID: m.UUID}).Error; err != nil {
			return err
		}
		view := archivedGuardedView(&a)
		result := &resultJSON{Task: view.Task, Revision: view.Revision, Guarded: view.Guarded, Terminal: true}
		if err := storeOperation(tx, req.OperationId, op, ro, m.UUID, m.Queue, now, result); err != nil {
			return err
		}
		out = &api.CompleteGuardedTaskResponse{Task: *view}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s PostgresStore) GetGuardedTask(ctx context.Context, req *api.GetGuardedTaskRequest) (*api.GetGuardedTaskResponse, error) {
	out := &api.GetGuardedTaskResponse{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

func (s PostgresStore) LookupOperation(ctx context.Context, req *api.LookupOperationRequest) (*api.LookupOperationResponse, error) {
	out := &api.LookupOperationResponse{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rec, err := loadOperation(tx, req.OperationId, "", nil, time.Now().UnixNano())
		if err != nil || rec == nil {
			return err
		}
		out.Receipt, err = rec.receipt()
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s PostgresStore) PurgeExpiredReceipts(ctx context.Context, now int64, limit int) (int, error) {
	total := 0
	for _, q := range []string{
		`DELETE FROM submission_receipts WHERE ctid IN (SELECT ctid FROM submission_receipts WHERE expires_at <= ? LIMIT ?)`,
		`DELETE FROM operation_receipts WHERE ctid IN (SELECT ctid FROM operation_receipts WHERE expires_at <= ? LIMIT ?)`,
	} {
		res := DB.WithContext(ctx).Exec(q, now, limit)
		if res.Error != nil {
			return total, res.Error
		}
		total += int(res.RowsAffected)
	}
	return total, nil
}
