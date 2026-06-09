package implementations

import (
	"context"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

func (s *V1Alpha1Server) SubmitTask(ctx context.Context, req api.SubmitTaskRequest) (api.SubmitTaskResponse, error) {
	if req.Queue == "" {
		req.Queue = config.DefaultQueue
	}
	if req.CurrentState == "" {
		req.CurrentState = config.DefaultStartingState
	}
	if req.AutoTargetState == "" {
		req.AutoTargetState = req.CurrentState + config.DefaultWorkingSuffix
	}
	// A 0 timeout means "use the default"; callers who genuinely want 0 send a
	// negative value (otherwise invalid), which we clamp back to 0 here.
	if req.Timeout == 0 {
		req.Timeout = config.DefaultTimeout
	}
	if req.Timeout < 0 {
		req.Timeout = 0
	}
	resp, err := store.AppStore.SubmitTask(ctx, &req)
	if config.PrometheusEnabled && err == nil {
		metrics.TasksTotal.Inc()
	}
	if resp == nil {
		return api.SubmitTaskResponse{}, err
	}
	return *resp, err
}
