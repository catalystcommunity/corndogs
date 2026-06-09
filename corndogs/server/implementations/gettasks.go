package implementations

import (
	"context"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

func (s *V1Alpha1Server) GetTaskStateByID(ctx context.Context, req api.GetTaskStateByIDRequest) (api.GetTaskStateByIDResponse, error) {
	resp, err := store.AppStore.MustGetTaskStateByID(ctx, &req)
	if resp == nil {
		return api.GetTaskStateByIDResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) GetNextTask(ctx context.Context, req api.GetNextTaskRequest) (api.GetNextTaskResponse, error) {
	if req.Queue == "" {
		req.Queue = config.DefaultQueue
	}
	if req.CurrentState == "" {
		req.CurrentState = config.DefaultStartingState
	}
	resp, err := store.AppStore.GetNextTask(ctx, &req)
	if resp == nil {
		return api.GetNextTaskResponse{}, err
	}
	return *resp, err
}
