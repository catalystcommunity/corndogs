package implementations

import (
	"context"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

func (s *V1Alpha1Server) GetQueues(ctx context.Context, req api.GetQueuesRequest) (api.GetQueuesResponse, error) {
	resp, err := store.AppStore.GetQueues(ctx, &req)
	if resp == nil {
		return api.GetQueuesResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) GetQueueTaskCounts(ctx context.Context, req api.GetQueueTaskCountsRequest) (api.GetQueueTaskCountsResponse, error) {
	resp, err := store.AppStore.GetQueueTaskCounts(ctx, &req)
	if resp == nil {
		return api.GetQueueTaskCountsResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) GetTaskStateCounts(ctx context.Context, req api.GetTaskStateCountsRequest) (api.GetTaskStateCountsResponse, error) {
	resp, err := store.AppStore.GetTaskStateCounts(ctx, &req)
	if resp == nil {
		return api.GetTaskStateCountsResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) GetQueueAndStateCounts(ctx context.Context, req api.GetQueueAndStateCountsRequest) (api.GetQueueAndStateCountsResponse, error) {
	resp, err := store.AppStore.GetQueueAndStateCounts(ctx, &req)
	if resp == nil {
		return api.GetQueueAndStateCountsResponse{}, err
	}
	return *resp, err
}
