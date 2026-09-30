package implementations

import (
	"context"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

func (s *V1Alpha1Server) UpdateTask(ctx context.Context, req api.UpdateTaskRequest) (api.UpdateTaskResponse, error) {
	if err := admitLegacy("UpdateTask", false); err != nil {
		return api.UpdateTaskResponse{}, err
	}
	if req.Payload != nil {
		if err := validatePayload(*req.Payload); err != nil {
			return api.UpdateTaskResponse{}, err
		}
	}
	if req.CurrentState == "" {
		req.CurrentState = config.DefaultStartingState
	}
	applyUpdateDefaults(&req.NewState, &req.AutoTargetState)
	resp, err := store.AppStore.UpdateTask(ctx, &req)
	if resp == nil {
		return api.UpdateTaskResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) CompleteTask(ctx context.Context, req api.CompleteTaskRequest) (api.CompleteTaskResponse, error) {
	if err := admitLegacy("CompleteTask", false); err != nil {
		return api.CompleteTaskResponse{}, err
	}
	resp, err := store.AppStore.CompleteTask(ctx, &req)
	if config.PrometheusEnabled && err == nil {
		metrics.CompletedTasksTotal.Inc()
	}
	if resp == nil {
		return api.CompleteTaskResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) CancelTask(ctx context.Context, req api.CancelTaskRequest) (api.CancelTaskResponse, error) {
	if err := admitLegacy("CancelTask", false); err != nil {
		return api.CancelTaskResponse{}, err
	}
	resp, err := store.AppStore.CancelTask(ctx, &req)
	if config.PrometheusEnabled && err == nil {
		metrics.CanceledTasksTotal.Inc()
	}
	if resp == nil {
		return api.CancelTaskResponse{}, err
	}
	return *resp, err
}

func (s *V1Alpha1Server) CleanUpTimedOut(ctx context.Context, req api.CleanUpTimedOutRequest) (api.CleanUpTimedOutResponse, error) {
	resp, err := store.AppStore.CleanUpTimedOut(ctx, &req)
	if config.PrometheusEnabled && err == nil {
		metrics.TimedOutTasksTotal.Inc()
	}
	if resp == nil {
		return api.CleanUpTimedOutResponse{}, err
	}
	return *resp, err
}

// applyUpdateDefaults fills empty update fields. Legacy and guarded updates
// share it.
func applyUpdateDefaults(newState, autoTargetState *string) {
	if *newState == "" {
		*newState = "updated"
	}
	if *autoTargetState == "" {
		*autoTargetState = *newState + config.DefaultWorkingSuffix
	}
}
