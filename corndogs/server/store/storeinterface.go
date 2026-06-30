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
	UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error)
	CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error)
	CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error)
	CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error)
	// Metrics
	GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error)
	GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error)
	GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error)
	GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error)
}

func SetStore(store Store) {
	AppStore = store
}
