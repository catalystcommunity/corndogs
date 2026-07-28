package postgresstore

import (
	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/postgresstore/models"
)

func taskToAPI(t *models.Task) *api.Task {
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

func archivedTaskToAPI(t *models.ArchivedTask) *api.Task {
	return &api.Task{
		Uuid:            t.UUID,
		Queue:           t.Queue,
		CurrentState:    t.CurrentState,
		AutoTargetState: t.AutoTargetState,
		SubmitTime:      t.SubmitTime,
		UpdateTime:      t.UpdateTime,
	}
}

func taskDeliveryToAPI(t *models.Task) *api.TaskDelivery {
	return &api.TaskDelivery{Task: *taskToAPI(t), Payload: t.Payload}
}
