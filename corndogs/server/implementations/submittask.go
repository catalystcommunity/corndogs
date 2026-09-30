package implementations

import (
	"context"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

func (s *V1Alpha1Server) SubmitTask(ctx context.Context, req api.SubmitTaskRequest) (api.SubmitTaskResponse, error) {
	if err := admitLegacy("SubmitTask", true); err != nil {
		return api.SubmitTaskResponse{}, err
	}
	if err := validatePayload(req.Payload); err != nil {
		return api.SubmitTaskResponse{}, err
	}
	if req.Queue == "" {
		req.Queue = config.DefaultQueue
	}
	applySubmitDefaults(&req.CurrentState, &req.AutoTargetState, &req.Timeout)
	resp, err := store.AppStore.SubmitTask(ctx, &req)
	if config.PrometheusEnabled && err == nil {
		metrics.TasksTotal.Inc()
	}
	if resp == nil {
		return api.SubmitTaskResponse{}, err
	}
	return *resp, err
}

// applySubmitDefaults fills empty submission fields. Legacy and keyed
// submissions share it.
func applySubmitDefaults(currentState, autoTargetState *string, timeout *int64) {
	if *currentState == "" {
		*currentState = config.DefaultStartingState
	}
	if *autoTargetState == "" {
		*autoTargetState = *currentState + config.DefaultWorkingSuffix
	}
	// A 0 timeout means "use the default"; callers who genuinely want 0 send a
	// negative value (otherwise invalid), which we clamp back to 0 here.
	if *timeout == 0 {
		*timeout = config.DefaultTimeout
	}
	if *timeout < 0 {
		*timeout = 0
	}
}
