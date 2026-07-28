package postgresstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/logging"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/postgresstore/models"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var taskMetadataColumns = []string{
	"uuid",
	"queue",
	"current_state",
	"auto_target_state",
	"submit_time",
	"update_time",
	"timeout",
	"priority",
}

// global db
var DB *gorm.DB

var DatabaseName = config.GetEnvOrDefault("DATABASE_NAME", "localcorndogsdev")
var DatabaseHost = config.GetEnvOrDefault("DATABASE_HOST", "corndogs-postgresql")
var DatabaseUser = config.GetEnvOrDefault("DATABASE_USER", "postgres")
var DatabasePassword = config.GetEnvOrDefault("DATABASE_PASSWORD", "localcorndogsdevpass")
var DatabasePort = config.GetEnvOrDefault("DATABASE_PORT", "5432")
var DatabaseSSLMode = config.GetEnvOrDefault("DATABASE_SSL_MODE", "disable")
var MaxIdleConns = config.GetEnvAsIntOrDefault("DATABASE_MAX_IDLE_CONNS", "1")
var MaxOpenConns = config.GetEnvAsIntOrDefault("DATABASE_MAX_OPEN_CONNS", "10")
var ConnMaxLifetime = time.Duration(config.GetEnvAsIntOrDefault("DATABASE_CONN_MAX_LIFETIME_SECONDS", "3600")) * time.Second

// sql files embedded at compile time, used by goose
//
//go:embed migrations/*.sql
var embedMigrations embed.FS

type PostgresStore struct{}

func (s PostgresStore) Initialize() (func(), error) {
	var err error
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s", DatabaseHost, DatabaseUser, DatabasePassword, DatabaseName, DatabasePort, DatabaseSSLMode)
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logging.NewGormLogger()})
	if err != nil {
		return nil, err
	}
	sqlDb, err := DB.DB()
	if err != nil {
		panic(err)
	}
	sqlDb.SetMaxIdleConns(MaxIdleConns)
	sqlDb.SetMaxOpenConns(MaxOpenConns)
	sqlDb.SetConnMaxLifetime(ConnMaxLifetime)
	// configure database connection settings
	fmt.Printf("Connected to %q", DB.Name())
	goose.SetBaseFS(embedMigrations)

	err = goose.Up(sqlDb, "migrations")
	if err != nil {
		return nil, err
	}
	return func() { sqlDb.Close() }, nil
}

func (s PostgresStore) SubmitTask(ctx context.Context, req *api.SubmitTaskRequest) (*api.SubmitTaskResponse, error) {
	taskProto := &api.Task{}
	newUuid, _ := uuid.NewRandom()

	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{
			UUID:            newUuid.String(),
			Queue:           req.Queue,
			CurrentState:    req.CurrentState,
			AutoTargetState: req.AutoTargetState,
			Timeout:         req.Timeout,
			Priority:        req.Priority,
			Payload:         req.Payload,
		}
		result := tx.Create(&model)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		taskProto = taskToAPI(&model)
		return nil
	})

	return &api.SubmitTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) MustGetTaskStateByID(ctx context.Context, req *api.GetTaskStateByIDRequest) (*api.GetTaskStateByIDResponse, error) {
	taskProto := &api.Task{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{UUID: req.Uuid}
		result := tx.Select(taskMetadataColumns).First(&model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				archived_model := models.ArchivedTask{UUID: req.Uuid}
				archived_result := tx.First(&archived_model)
				if archived_result.Error != nil {
					if errors.Is(archived_result.Error, gorm.ErrRecordNotFound) {
						// not found return nil
						taskProto = nil
						return nil
					} else {
						log.Err(result.Error)
						return archived_result.Error
					}
				}
				taskProto = archivedTaskToAPI(&archived_model)
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		taskProto = taskToAPI(&model)
		return nil
	},
	)
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.GetTaskStateByIDResponse{Task: taskProto}, err
}

// GetNextTaskGroup claims the single best task across a set of queues. It is
// GetNextTask generalized from `queue = ?` to `queue IN (?)`: the ORDER BY runs
// over every candidate row in every named queue at once, so priority is
// respected across the whole group (highest priority first, then oldest), and
// FOR UPDATE SKIP LOCKED still guarantees exactly one worker claims each task.
func (s PostgresStore) GetNextTaskGroup(ctx context.Context, req *api.GetNextTaskGroupRequest) (*api.GetNextTaskGroupResponse, error) {
	var delivery *api.TaskDelivery
	if len(req.Queues) == 0 {
		return &api.GetNextTaskGroupResponse{}, nil
	}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		result := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("queue IN ? AND current_state = ?", req.Queues, req.CurrentState).
			Order("priority DESC, update_time ASC").
			Limit(1).
			Find(&model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		model.CurrentState = model.AutoTargetState
		model.AutoTargetState = req.CurrentState
		if req.OverrideCurrentState != "" {
			model.CurrentState = req.OverrideCurrentState
		}
		if req.OverrideAutoTargetState != "" {
			model.AutoTargetState = req.OverrideAutoTargetState
		}
		if req.OverrideTimeout < 0 {
			model.Timeout = 0
		} else if req.OverrideTimeout != 0 {
			model.Timeout = req.OverrideTimeout
		}
		model.UpdateTime = time.Now().UnixNano()
		result = tx.Model(&models.Task{}).Where("uuid = ?", model.UUID).Updates(map[string]interface{}{
			"current_state":     model.CurrentState,
			"auto_target_state": model.AutoTargetState,
			"timeout":           model.Timeout,
			"update_time":       model.UpdateTime,
		})
		if result.Error != nil {
			return result.Error
		}
		delivery = taskDeliveryToAPI(&model)
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.GetNextTaskGroupResponse{Delivery: delivery}, err
}

func (s PostgresStore) GetNextTask(ctx context.Context, req *api.GetNextTaskRequest) (*api.GetNextTaskResponse, error) {
	var delivery *api.TaskDelivery
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		result := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("queue = ? AND current_state = ?", req.Queue, req.CurrentState).
			Order("priority DESC, update_time ASC").
			Limit(1).
			Find(&model)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		model.CurrentState = model.AutoTargetState
		model.AutoTargetState = req.CurrentState
		if req.OverrideCurrentState != "" {
			model.CurrentState = req.OverrideCurrentState
		}
		if req.OverrideAutoTargetState != "" {
			model.AutoTargetState = req.OverrideAutoTargetState
		}
		if req.OverrideTimeout < 0 {
			model.Timeout = 0
		} else if req.OverrideTimeout != 0 {
			model.Timeout = req.OverrideTimeout
		}
		model.UpdateTime = time.Now().UnixNano()
		result = tx.Model(&models.Task{}).Where("uuid = ?", model.UUID).Updates(map[string]interface{}{
			"current_state":     model.CurrentState,
			"auto_target_state": model.AutoTargetState,
			"timeout":           model.Timeout,
			"update_time":       model.UpdateTime,
		})
		if result.Error != nil {
			return result.Error
		}
		delivery = taskDeliveryToAPI(&model)
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.GetNextTaskResponse{Delivery: delivery}, err
}

func (s PostgresStore) UpdateTask(ctx context.Context, req *api.UpdateTaskRequest) (*api.UpdateTaskResponse, error) {
	taskProto := &api.Task{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{UUID: req.Uuid}
		result := tx.Select(taskMetadataColumns).First(&model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		model.CurrentState = req.NewState
		model.AutoTargetState = req.AutoTargetState
		model.Timeout = req.Timeout
		model.Priority = req.Priority
		model.UpdateTime = time.Now().UnixNano()
		updates := map[string]interface{}{
			"current_state":     model.CurrentState,
			"auto_target_state": model.AutoTargetState,
			"timeout":           model.Timeout,
			"priority":          model.Priority,
			"update_time":       model.UpdateTime,
		}
		if req.Payload != nil {
			updates["payload"] = *req.Payload
		}
		result = tx.Model(&models.Task{}).Where("uuid = ?", model.UUID).Updates(updates)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		taskProto = taskToAPI(&model)
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}

	return &api.UpdateTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) CompleteTask(ctx context.Context, req *api.CompleteTaskRequest) (*api.CompleteTaskResponse, error) {
	taskProto := &api.Task{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Load task metadata without the payload.
		model := models.Task{UUID: req.Uuid}
		result := tx.Select(taskMetadataColumns).First(&model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		archiveModel := models.ConvertTaskForArchive(model)
		archiveModel.CurrentState = "completed"
		archiveModel.AutoTargetState = "completed"
		result = tx.Create(&archiveModel)
		if result.Error != nil {
			return result.Error
		}
		result = tx.Delete(&model)
		if result.Error != nil {
			return result.Error
		}
		taskProto = archivedTaskToAPI(&archiveModel)
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.CompleteTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) CancelTask(ctx context.Context, req *api.CancelTaskRequest) (*api.CancelTaskResponse, error) {
	taskProto := &api.Task{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Load task metadata without the payload.
		model := models.Task{UUID: req.Uuid}
		result := tx.Select(taskMetadataColumns).First(&model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		archiveModel := models.ConvertTaskForArchive(model)
		archiveModel.CurrentState = "canceled"
		archiveModel.AutoTargetState = "canceled"
		result = tx.Create(&archiveModel)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		result = tx.Delete(&model)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		taskProto = archivedTaskToAPI(&archiveModel)
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.CancelTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) CleanUpTimedOut(ctx context.Context, req *api.CleanUpTimedOutRequest) (*api.CleanUpTimedOutResponse, error) {
	var count int64 = 0
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		result := tx.Model(model).Where(`
			timeout > 0 AND
			(update_time + (timeout * ?)) < ? AND
			((? = '') OR (? <> '' AND queue = ?))
		`, time.Second.Nanoseconds(), req.AtTime, req.Queue, req.Queue, req.Queue).Updates(
			map[string]interface{}{
				"current_state":     gorm.Expr("auto_target_state"),
				"auto_target_state": gorm.Expr("current_state"),
				"timeout":           0,
			})
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		count = result.RowsAffected
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.CleanUpTimedOutResponse{TimedOut: count}, err
}

func (s PostgresStore) GetQueues(ctx context.Context, req *api.GetQueuesRequest) (*api.GetQueuesResponse, error) {
	queues := []string{}
	var count int64 = 0
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		result := tx.Model(model).Select("queue").Distinct().Find(&queues)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		result = tx.Model(model).Count(&count)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.GetQueuesResponse{Queues: queues, TotalTaskCount: count}, err
}

func (s PostgresStore) GetQueueTaskCounts(ctx context.Context, req *api.GetQueueTaskCountsRequest) (*api.GetQueueTaskCountsResponse, error) {
	queues := make(map[string]int64)
	var count int64 = 0
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		rows, err := tx.Model(model).Select("queue", "COUNT(queue)").Group("queue").Rows()
		if err != nil {
			log.Err(err)
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var value int64
			err = rows.Scan(&key, &value)
			if err != nil {
				return err
			}
			queues[key] = value
		}

		result := tx.Model(model).Count(&count)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.GetQueueTaskCountsResponse{QueueCounts: queues, TotalTaskCount: count}, err
}

func (s PostgresStore) GetTaskStateCounts(ctx context.Context, req *api.GetTaskStateCountsRequest) (*api.GetTaskStateCountsResponse, error) {
	stateCounts := make(map[string]int64)
	var count int64 = 0
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		rows, err := tx.Model(model).Select("current_state", "COUNT(current_state)").Where("queue = ?", req.Queue).Group("current_state").Rows()
		if err != nil {
			log.Err(err)
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var value int64
			err = rows.Scan(&key, &value)
			if err != nil {
				log.Err(err)
				return err
			}
			stateCounts[key] = value
		}

		result := tx.Model(model).Where("queue = ?", req.Queue).Count(&count)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &api.GetTaskStateCountsResponse{Queue: req.Queue, Count: count, StateCounts: stateCounts}, err
}

func (s PostgresStore) GetQueueAndStateCounts(ctx context.Context, req *api.GetQueueAndStateCountsRequest) (*api.GetQueueAndStateCountsResponse, error) {
	queueAndStateCounts := make(map[string]*api.QueueAndStateCounts)
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		rows, err := tx.Model(model).Select("queue", "current_state", "COUNT(current_state)").Group("queue").Group("current_state").Rows()
		if err != nil {
			log.Err(err)
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var queue string
			var state string
			var stateCount int64
			err = rows.Scan(&queue, &state, &stateCount)
			if err != nil {
				log.Err(err)
				return err
			}
			if _, ok := queueAndStateCounts[queue]; !ok {
				queueAndStateCounts[queue] = &api.QueueAndStateCounts{
					Queue:       queue,
					Count:       0,
					StateCounts: make(map[string]int64),
				}
			}
			queueAndStateCounts[queue].StateCounts[state] = stateCount
			queueAndStateCounts[queue].Count += stateCount
		}
		return nil
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	result := make(api.QueueAndStateCountsMap, len(queueAndStateCounts))
	for q, v := range queueAndStateCounts {
		result[q] = *v
	}
	return &api.GetQueueAndStateCountsResponse{QueueAndStateCounts: result}, err
}
