package store

import (
	"fmt"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// Operation names in receipts and request digests.
const (
	OpClaim    = "claim"
	OpUpdate   = "update"
	OpComplete = "complete"
	OpCancel   = "cancel"
)

// TerminalState returns the archive state of a finishing operation.
func TerminalState(op string) string {
	if op == OpCancel {
		return "canceled"
	}
	return "completed"
}

// ReceiptOptions go with each keyed or guarded mutation. Digest identifies the
// request as the caller sent it, before defaults. Retention is how long the
// receipt stays valid.
type ReceiptOptions struct {
	Digest    []byte
	Retention time.Duration
}

// The errors below are ServiceErrors: the server sends them as the
// ServiceError arm, which every generated client decodes.

func InvalidArgument(format string, args ...any) error {
	return api.NewServiceError(api.CodeInvalidArgument, format, args...)
}

func SubmissionKeyRequired(op string) error {
	return api.NewServiceError(api.CodeSubmissionKeyRequired,
		"%s is rejected: this server requires submission keys (CORNDOGS_SUBMISSION_KEY_POLICY=required). Use SubmitKeyedTask with a submission_key; the task was not created", op)
}

func TaskGuardRequired(op string) error {
	return api.NewServiceError(api.CodeTaskGuardRequired,
		"%s is rejected: this server requires task guards (CORNDOGS_TASK_GUARD_POLICY=required). Use the guarded operation with expected_revision and operation_id; nothing was changed", op)
}

func LegacyOnGuarded(op, id string) error {
	return api.NewServiceError(api.CodeTaskGuardRequired,
		"%s is rejected: task %s is guarded. Use the guarded operation with expected_revision and operation_id; nothing was changed", op, id)
}

func SubmissionKeyConflict(queue, key string) error {
	return api.NewServiceError(api.CodeSubmissionKeyConflict,
		"submission key %q in queue %q was accepted for a different request; nothing was created", key, queue)
}

func RevisionConflict(id, format string, args ...any) error {
	return api.NewServiceError(api.CodeRevisionConflict,
		"task %s: %s; nothing was changed", id, fmt.Sprintf(format, args...))
}

func OperationConflict(opID, prevOp string) error {
	return api.NewServiceError(api.CodeOperationConflict,
		"operation_id %q was used for a different %s request; nothing was changed", opID, prevOp)
}

func TaskNotFound(id string) error {
	return api.NewServiceError(api.CodeTaskNotFound, "task %s was not found", id)
}

func ClaimSuperseded(opID, id string) error {
	return api.NewServiceError(api.CodeClaimSuperseded,
		"claim %q of task %s no longer holds the task; it changed after the claim", opID, id)
}
