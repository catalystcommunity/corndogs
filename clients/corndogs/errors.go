package corndogs

import (
	"errors"
	"fmt"
)

// ServiceError codes of the resilience contract. See csil/corndogs.csil.
const (
	CodeInvalidArgument       uint64 = 1
	CodeSubmissionKeyRequired uint64 = 2
	CodeTaskGuardRequired     uint64 = 3
	CodeSubmissionKeyConflict uint64 = 4
	CodeRevisionConflict      uint64 = 5
	CodeOperationConflict     uint64 = 6
	CodeTaskNotFound          uint64 = 7
	CodeClaimSuperseded       uint64 = 8
	FeatureSubmissionKeys            = "submission-keys-v1"
	FeatureTaskGuards                = "task-guards-v1"
	PolicyCompatibility              = "compatibility"
	PolicyRequired                   = "required"
	MaxSubmissionKeyBytes            = 128
	MaxOperationIDBytes              = 128
)

// Error makes a ServiceError usable as a Go error. The server returns it to
// send the ServiceError arm of an operation.
func (e *ServiceError) Error() string {
	return fmt.Sprintf("corndogs service error %d: %s", e.Code, e.Message)
}

// NewServiceError returns a ServiceError with a formatted message.
func NewServiceError(code uint64, format string, args ...any) *ServiceError {
	return &ServiceError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrOutcomeUncertain marks a call whose request may have reached the server,
// but whose reply did not arrive. The operation may or may not have executed.
// A client never replays such a call unless the operation is safe to replay
// (see ReplaySafe).
var ErrOutcomeUncertain = errors.New("corndogs: outcome uncertain")

// ErrNotApplied marks a call that the server provably did not apply: the
// connection failed before the request was sent, a cluster follower redirected
// it, or the leader rejected it before execution because it had no write
// quorum. A client can retry it.
var ErrNotApplied = errors.New("corndogs: request not applied")

// ErrUnsupported marks an operation that the server does not support, for
// example a resilience operation sent to a server before 0.8.0. The server did
// not execute it. Do not retry it as a legacy operation.
var ErrUnsupported = errors.New("corndogs: operation not supported by server")

// TransportStatusError is a non-zero CSIL-RPC transport status.
type TransportStatusError struct {
	Status  int64
	Message string
}

func (e *TransportStatusError) Error() string {
	return fmt.Sprintf("transport status %d: %s", e.Status, e.Message)
}

// CSIL-RPC transport statuses that the client classifies.
const (
	statusMalformedEnvelope  = 1
	statusUnknownServiceOrOp = 2
	statusUnavailable        = 7
)

// Is makes errors.Is classify the status. Status 1 (malformed envelope) and
// status 7 (the server did not apply the request) are not applied, and status
// 2 (unknown operation) is unsupported. A not-leader redirect is not applied.
// Every other status is uncertain: the server gives no proof either way.
func (e *TransportStatusError) Is(target error) bool {
	switch target {
	case ErrUnsupported:
		return e.Status == statusUnknownServiceOrOp
	case ErrNotApplied:
		if _, ok := redirectLeader(e); ok {
			return true
		}
		return e.Status == statusMalformedEnvelope || e.Status == statusUnavailable || e.Status == statusUnknownServiceOrOp
	case ErrOutcomeUncertain:
		if _, ok := redirectLeader(e); ok {
			return false
		}
		return e.Status != statusMalformedEnvelope && e.Status != statusUnavailable && e.Status != statusUnknownServiceOrOp
	}
	return false
}

// IsUncertain reports whether err means that the operation may have executed.
func IsUncertain(err error) bool { return errors.Is(err, ErrOutcomeUncertain) }

// IsUnsupported reports whether err means that the server does not support the
// operation.
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }

// ServiceErrorCode returns the ServiceError code in err, or 0 and false when
// err is not a ServiceError from the server.
func ServiceErrorCode(err error) (uint64, bool) {
	var ce *ClientError
	if errors.As(err, &ce) && ce.Err == nil && (ce.Code != 0 || ce.Message != "") {
		return uint64(ce.Code), true
	}
	var se *ServiceError
	if errors.As(err, &se) {
		return se.Code, true
	}
	return 0, false
}

// replaySafeOps are the operations that a client may send again after an
// uncertain outcome. Reads change nothing. The keyed and guarded mutations
// carry a submission_key or operation_id, and the server returns the first
// result for a repeated request. Legacy mutations are not in this set.
var replaySafeOps = map[string]bool{
	"GetTaskStateByID":       true,
	"GetQueues":              true,
	"GetQueueTaskCounts":     true,
	"GetTaskStateCounts":     true,
	"GetQueueAndStateCounts": true,
	"GetServerInfo":          true,
	"SubmitKeyedTask":        true,
	"LookupSubmission":       true,
	"ClaimGuardedTask":       true,
	"ClaimGuardedTaskGroup":  true,
	"UpdateGuardedTask":      true,
	"CompleteGuardedTask":    true,
	"CancelGuardedTask":      true,
	"GetGuardedTask":         true,
	"LookupOperation":        true,
}

// ReplaySafe reports whether a client may send op again after an uncertain
// outcome, with the same request bytes.
func ReplaySafe(op string) bool { return replaySafeOps[op] }

// uncertain wraps err so that errors.Is(err, ErrOutcomeUncertain) is true.
func uncertain(err error) error {
	return &ClientError{Err: fmt.Errorf("%w: %w", ErrOutcomeUncertain, err)}
}

// notApplied wraps err so that errors.Is(err, ErrNotApplied) is true.
func notApplied(err error) error { return &ClientError{Err: fmt.Errorf("%w: %w", ErrNotApplied, err)} }

// Unwrap exposes the transport cause, so errors.Is sees ErrOutcomeUncertain,
// ErrNotApplied, and ErrUnsupported.
func (e *ClientError) Unwrap() error { return e.Err }
