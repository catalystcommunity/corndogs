package filestore

import api "github.com/CatalystCommunity/corndogs/clients/corndogs"

// toAPITask converts internal metadata to the generated CSIL Task.
func toAPITask(t *Task) *api.Task {
	return &api.Task{
		Uuid:            t.UUID,
		Queue:           t.Queue,
		CurrentState:    t.CurrentState,
		AutoTargetState: t.AutoTargetState,
		SubmitTime:      t.SubmitTime,
		UpdateTime:      t.UpdateTime,
		Timeout:         t.Timeout,
		Priority:        t.Priority,
	}
}

// toAPIDelivery combines task metadata with the opaque payload for a claim
// response. No other response returns payload bytes.
func toAPIDelivery(t *Task, payload []byte) *api.TaskDelivery {
	return &api.TaskDelivery{Task: *toAPITask(t), Payload: payload}
}

// archivedToAPITask converts an archived record to API task metadata.
func archivedToAPITask(a *ArchivedTask) *api.Task {
	return &api.Task{
		Uuid:            a.UUID,
		Queue:           a.Queue,
		CurrentState:    a.CurrentState,
		AutoTargetState: a.AutoTargetState,
		SubmitTime:      a.SubmitTime,
		UpdateTime:      a.UpdateTime,
	}
}

// toGuardedTask converts live metadata to the guarded view.
func toGuardedTask(t *Task) *api.GuardedTask {
	return &api.GuardedTask{Task: *toAPITask(t), Guarded: t.Guarded, Revision: t.Revision}
}

// archivedToGuardedTask converts an archive record to the guarded view.
func archivedToGuardedTask(a *ArchivedTask) *api.GuardedTask {
	return &api.GuardedTask{Task: *archivedToAPITask(a), Guarded: a.Guarded, Revision: a.Revision, Terminal: true}
}
