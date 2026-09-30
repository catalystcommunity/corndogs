package implementations

import (
	"context"
	"sort"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	uuidpkg "github.com/google/uuid"
)

// admitLegacy applies the policy to a legacy mutation. It returns an error
// before the store is called, so a rejected request changes nothing.
func admitLegacy(op string, isSubmit bool) error {
	if isSubmit && config.KeysRequired() {
		metrics.CountRejection(op, "submission_key_required")
		return store.SubmissionKeyRequired(op)
	}
	if config.GuardsRequired() {
		metrics.CountRejection(op, "task_guard_required")
		return store.TaskGuardRequired(op)
	}
	metrics.CountLegacy(op)
	return nil
}

func receiptOptions(d *digest) store.ReceiptOptions {
	return store.ReceiptOptions{Digest: d.sum(), Retention: config.ReceiptRetention}
}

func validateID(field, v string, max int) error {
	if v == "" {
		return store.InvalidArgument("%s must not be empty", field)
	}
	if len(v) > max {
		return store.InvalidArgument("%s has %d bytes; the maximum is %d", field, len(v), max)
	}
	return nil
}

func (s *V1Alpha1Server) GetServerInfo(ctx context.Context, req api.GetServerInfoRequest) (api.GetServerInfoResponse, error) {
	return api.GetServerInfoResponse{
		ServerVersion:           config.Version,
		Features:                []string{api.FeatureSubmissionKeys, api.FeatureTaskGuards},
		SubmissionKeyPolicy:     config.SubmissionKeyPolicy,
		TaskGuardPolicy:         config.TaskGuardPolicy,
		ReceiptRetentionSeconds: int64(config.ReceiptRetention.Seconds()),
	}, nil
}

func (s *V1Alpha1Server) SubmitKeyedTask(ctx context.Context, req api.SubmitKeyedTaskRequest) (api.SubmitKeyedTaskResponse, error) {
	if err := validateID("submission_key", req.SubmissionKey, api.MaxSubmissionKeyBytes); err != nil {
		return api.SubmitKeyedTaskResponse{}, err
	}
	// The key namespace is (queue, key). An empty queue would take the
	// configured default, and a later change of that default would move the
	// key to another namespace, so the queue must be explicit.
	if req.Queue == "" {
		return api.SubmitKeyedTaskResponse{}, store.InvalidArgument("queue must not be empty for SubmitKeyedTask")
	}
	if !req.Guarded && config.GuardsRequired() {
		metrics.CountRejection("SubmitKeyedTask", "task_guard_required")
		return api.SubmitKeyedTaskResponse{}, api.NewServiceError(api.CodeTaskGuardRequired,
			"SubmitKeyedTask with guarded=false is rejected: this server requires task guards (CORNDOGS_TASK_GUARD_POLICY=required); the task was not created")
	}
	if err := validatePayload(req.Payload); err != nil {
		return api.SubmitKeyedTaskResponse{}, err
	}
	// The digest covers the request as sent, before defaults, so a change of
	// a default cannot reinterpret an accepted key.
	d := newDigest("SubmitKeyedTask").
		bool(req.Guarded).text(req.Queue).text(req.CurrentState).text(req.AutoTargetState).
		int(req.Timeout).bytes(req.Payload).int(req.Priority)
	applySubmitDefaults(&req.CurrentState, &req.AutoTargetState, &req.Timeout)
	resp, err := store.AppStore.SubmitKeyedTask(ctx, &req, receiptOptions(d))
	if err != nil || resp == nil {
		return api.SubmitKeyedTaskResponse{}, err
	}
	if resp.Replayed {
		metrics.CountReplay("SubmitKeyedTask")
	} else if config.PrometheusEnabled {
		metrics.TasksTotal.Inc()
	}
	return *resp, nil
}

func (s *V1Alpha1Server) LookupSubmission(ctx context.Context, req api.LookupSubmissionRequest) (api.LookupSubmissionResponse, error) {
	if err := validateID("submission_key", req.SubmissionKey, api.MaxSubmissionKeyBytes); err != nil {
		return api.LookupSubmissionResponse{}, err
	}
	if req.Queue == "" {
		return api.LookupSubmissionResponse{}, store.InvalidArgument("queue must not be empty")
	}
	resp, err := store.AppStore.LookupSubmission(ctx, &req)
	if err != nil || resp == nil {
		return api.LookupSubmissionResponse{}, err
	}
	return *resp, nil
}

func (s *V1Alpha1Server) ClaimGuardedTask(ctx context.Context, req api.ClaimGuardedTaskRequest) (api.ClaimGuardedTaskResponse, error) {
	group := api.ClaimGuardedTaskGroupRequest{
		OperationId:             req.OperationId,
		Queues:                  []string{req.Queue},
		CurrentState:            req.CurrentState,
		OverrideTimeout:         req.OverrideTimeout,
		OverrideCurrentState:    req.OverrideCurrentState,
		OverrideAutoTargetState: req.OverrideAutoTargetState,
	}
	d := newDigest("ClaimGuardedTask").text(req.Queue).text(req.CurrentState).
		int(req.OverrideTimeout).text(req.OverrideCurrentState).text(req.OverrideAutoTargetState)
	if req.Queue == "" {
		group.Queues = []string{config.DefaultQueue}
	}
	resp, err := s.claimGuarded(ctx, "ClaimGuardedTask", group, d)
	return api.ClaimGuardedTaskResponse{Delivery: resp.Delivery, Replayed: resp.Replayed}, err
}

func (s *V1Alpha1Server) ClaimGuardedTaskGroup(ctx context.Context, req api.ClaimGuardedTaskGroupRequest) (api.ClaimGuardedTaskGroupResponse, error) {
	d := newDigest("ClaimGuardedTaskGroup").list(req.Queues).text(req.CurrentState).
		int(req.OverrideTimeout).text(req.OverrideCurrentState).text(req.OverrideAutoTargetState)
	if len(req.Queues) == 0 {
		req.Queues = []string{config.DefaultQueue}
	}
	// Sort a copy so the store sees each queue once; the digest keeps the
	// order as sent.
	queues := append([]string(nil), req.Queues...)
	sort.Strings(queues)
	req.Queues = compact(queues)
	return s.claimGuarded(ctx, "ClaimGuardedTaskGroup", req, d)
}

func (s *V1Alpha1Server) claimGuarded(ctx context.Context, op string, req api.ClaimGuardedTaskGroupRequest, d *digest) (api.ClaimGuardedTaskGroupResponse, error) {
	if err := validateID("operation_id", req.OperationId, api.MaxOperationIDBytes); err != nil {
		return api.ClaimGuardedTaskGroupResponse{}, err
	}
	if req.CurrentState == "" {
		req.CurrentState = config.DefaultStartingState
	}
	resp, err := store.AppStore.ClaimGuardedTask(ctx, &req, receiptOptions(d))
	if err != nil || resp == nil {
		return api.ClaimGuardedTaskGroupResponse{}, err
	}
	if resp.Replayed {
		metrics.CountReplay(op)
	}
	return *resp, nil
}

func validateGuard(opID, uuid, queue string, expectedRevision int64) error {
	if err := validateID("operation_id", opID, api.MaxOperationIDBytes); err != nil {
		return err
	}
	if _, err := uuidpkg.Parse(uuid); err != nil {
		return store.InvalidArgument("uuid %q is not a valid UUID", uuid)
	}
	if queue == "" {
		return store.InvalidArgument("queue must not be empty")
	}
	if expectedRevision < 0 {
		return store.InvalidArgument("expected_revision must not be negative")
	}
	return nil
}

func (s *V1Alpha1Server) UpdateGuardedTask(ctx context.Context, req api.UpdateGuardedTaskRequest) (api.UpdateGuardedTaskResponse, error) {
	if err := validateGuard(req.OperationId, req.Uuid, req.Queue, req.ExpectedRevision); err != nil {
		return api.UpdateGuardedTaskResponse{}, err
	}
	if req.Payload != nil {
		if err := validatePayload(*req.Payload); err != nil {
			return api.UpdateGuardedTaskResponse{}, err
		}
	}
	d := newDigest("UpdateGuardedTask").text(req.Uuid).text(req.Queue).int(req.ExpectedRevision).
		optText(req.ExpectedState).text(req.NewState).text(req.AutoTargetState).int(req.Timeout).
		optBytes(req.Payload).optInt(req.Priority)
	applyUpdateDefaults(&req.NewState, &req.AutoTargetState)
	resp, err := store.AppStore.UpdateGuardedTask(ctx, &req, receiptOptions(d))
	if err != nil || resp == nil {
		return api.UpdateGuardedTaskResponse{}, err
	}
	if resp.Replayed {
		metrics.CountReplay("UpdateGuardedTask")
	}
	return *resp, nil
}

func (s *V1Alpha1Server) CompleteGuardedTask(ctx context.Context, req api.CompleteGuardedTaskRequest) (api.CompleteGuardedTaskResponse, error) {
	return s.finishGuarded(ctx, "CompleteGuardedTask", store.OpComplete, req)
}

func (s *V1Alpha1Server) CancelGuardedTask(ctx context.Context, req api.CancelGuardedTaskRequest) (api.CancelGuardedTaskResponse, error) {
	resp, err := s.finishGuarded(ctx, "CancelGuardedTask", store.OpCancel, api.CompleteGuardedTaskRequest(req))
	return api.CancelGuardedTaskResponse(resp), err
}

func (s *V1Alpha1Server) finishGuarded(ctx context.Context, name, op string, req api.CompleteGuardedTaskRequest) (api.CompleteGuardedTaskResponse, error) {
	if err := validateGuard(req.OperationId, req.Uuid, req.Queue, req.ExpectedRevision); err != nil {
		return api.CompleteGuardedTaskResponse{}, err
	}
	d := newDigest(name).text(req.Uuid).text(req.Queue).int(req.ExpectedRevision).optText(req.ExpectedState)
	resp, err := store.AppStore.FinishGuardedTask(ctx, &req, op, receiptOptions(d))
	if err != nil || resp == nil {
		return api.CompleteGuardedTaskResponse{}, err
	}
	if resp.Replayed {
		metrics.CountReplay(name)
	} else if config.PrometheusEnabled {
		if op == store.OpCancel {
			metrics.CanceledTasksTotal.Inc()
		} else {
			metrics.CompletedTasksTotal.Inc()
		}
	}
	return *resp, nil
}

func (s *V1Alpha1Server) GetGuardedTask(ctx context.Context, req api.GetGuardedTaskRequest) (api.GetGuardedTaskResponse, error) {
	if _, err := uuidpkg.Parse(req.Uuid); err != nil {
		return api.GetGuardedTaskResponse{}, store.InvalidArgument("uuid %q is not a valid UUID", req.Uuid)
	}
	resp, err := store.AppStore.GetGuardedTask(ctx, &req)
	if err != nil || resp == nil {
		return api.GetGuardedTaskResponse{}, err
	}
	return *resp, nil
}

func (s *V1Alpha1Server) LookupOperation(ctx context.Context, req api.LookupOperationRequest) (api.LookupOperationResponse, error) {
	if err := validateID("operation_id", req.OperationId, api.MaxOperationIDBytes); err != nil {
		return api.LookupOperationResponse{}, err
	}
	resp, err := store.AppStore.LookupOperation(ctx, &req)
	if err != nil || resp == nil {
		return api.LookupOperationResponse{}, err
	}
	return *resp, nil
}

func compact(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
