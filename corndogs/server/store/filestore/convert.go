package filestore

import api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"

// toAPITask converts an internal Task to the generated CSIL api.Task.
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
		Payload:         t.Payload,
	}
}

// archivedToAPITask converts an archived record to api.Task. Payload is always
// nil for archived tasks (it is dropped on archive).
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
