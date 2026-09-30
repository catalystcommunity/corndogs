package store

import (
	"context"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

var AppStore Store

type Store interface {
	Initialize() (deferredFunc func(), err error)

	SubmitTask(ctx context.Context, req *api.SubmitTaskRequest) (*api.SubmitTaskResponse, error)
	MustGetTaskStateByID(ctx context.Context, req *api.GetTaskStateByIDRequest) (*api.GetTaskStateByIDResponse, error)
	GetNextTask(ctx context.Context, req *api.GetNextTaskRequest) (*api.GetNextTaskResponse, error)
	GetNextTaskGroup(ctx context.Context, req *api.GetNextTaskGroupRequest) (*api.GetNextTaskGroupResponse, error)
	UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error)
	CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error)
	CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error)
	CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error)
	// Metrics
	GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error)
	GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error)
	GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error)
	GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error)

	// Resilience contract (guards.go). The server layer validates and applies
	// defaults. The store checks keys, revisions, and receipts atomically with
	// the mutation, in one transaction.
	SubmitKeyedTask(ctx context.Context, req *api.SubmitKeyedTaskRequest, ro ReceiptOptions) (*api.SubmitKeyedTaskResponse, error)
	LookupSubmission(ctx context.Context, req *api.LookupSubmissionRequest) (*api.LookupSubmissionResponse, error)
	ClaimGuardedTask(ctx context.Context, req *api.ClaimGuardedTaskGroupRequest, ro ReceiptOptions) (*api.ClaimGuardedTaskGroupResponse, error)
	UpdateGuardedTask(ctx context.Context, req *api.UpdateGuardedTaskRequest, ro ReceiptOptions) (*api.UpdateGuardedTaskResponse, error)
	// FinishGuardedTask completes (op OpComplete) or cancels (op OpCancel) a task.
	FinishGuardedTask(ctx context.Context, req *api.CompleteGuardedTaskRequest, op string, ro ReceiptOptions) (*api.CompleteGuardedTaskResponse, error)
	GetGuardedTask(ctx context.Context, req *api.GetGuardedTaskRequest) (*api.GetGuardedTaskResponse, error)
	LookupOperation(ctx context.Context, req *api.LookupOperationRequest) (*api.LookupOperationResponse, error)
	// PurgeExpiredReceipts deletes at most limit receipts that expired before
	// now (Unix nanoseconds) and returns the number deleted.
	PurgeExpiredReceipts(ctx context.Context, now int64, limit int) (int, error)
}

func SetStore(store Store) {
	AppStore = store
}
