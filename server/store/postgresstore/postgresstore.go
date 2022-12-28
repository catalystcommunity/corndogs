package postgresstore

import (
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/TnLCommunity/corndogs/server/config"
	"github.com/TnLCommunity/corndogs/server/conversions"
	"github.com/TnLCommunity/corndogs/server/store/postgresstore/models"
	corndogsv1alpha1 "github.com/TnLCommunity/protos-corndogs/gen/proto/go/corndogs/v1alpha1"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
//go:embed migrations/*.sql
var embedMigrations embed.FS

type PostgresStore struct{}

func (s PostgresStore) Initialize() (func(), error) {
	var err error
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s", DatabaseHost, DatabaseUser, DatabasePassword, DatabaseName, DatabasePort, DatabaseSSLMode)
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
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

func (s PostgresStore) SubmitTask(req *corndogsv1alpha1.SubmitTaskRequest) (*corndogsv1alpha1.SubmitTaskResponse, error) {
	taskProto := &corndogsv1alpha1.Task{}
	newUuid, _ := uuid.NewRandom()

	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{
			UUID:            newUuid.String(),
			Queue:           req.Queue,
			CurrentState:    req.CurrentState,
			AutoTargetState: req.AutoTargetState,
			Timeout:         req.Timeout,
			Priority:        req.Priority,
			Payload:         req.Payload,
		}
		result := DB.Create(&model)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		// marshall result to response
		return conversions.StructToProto(model, taskProto)
	})

	return &corndogsv1alpha1.SubmitTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) MustGetTaskStateByID(req *corndogsv1alpha1.GetTaskStateByIDRequest) *corndogsv1alpha1.GetTaskStateByIDResponse {
	taskProto := &corndogsv1alpha1.Task{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{UUID: req.Uuid}
		result := DB.First(&model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				archived_model := models.ArchivedTask{UUID: req.Uuid}
				archived_result := DB.First(&archived_model)
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
				return conversions.StructToProto(archived_model, taskProto)
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		// marshall result to response
		return conversions.StructToProto(model, taskProto)
	},
	)
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &corndogsv1alpha1.GetTaskStateByIDResponse{Task: taskProto}
}

func (s PostgresStore) GetNextTask(req *corndogsv1alpha1.GetNextTaskRequest) (*corndogsv1alpha1.GetNextTaskResponse, error) {
	// TODO: This may be something that can be simplified, determine that and do so or explain why it can't
	taskProto := &corndogsv1alpha1.Task{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		var nextUuid string
		result := DB.Raw(
			`UPDATE tasks SET current_state = current_state || ?
				 WHERE uuid = (
					 SELECT uuid FROM tasks
					 WHERE queue = ? AND current_state = ?
                     ORDER BY priority DESC, update_time DESC
					 FOR UPDATE SKIP LOCKED
					 LIMIT 1)
				 RETURNING uuid`,
			config.DefaultWorkingSuffix,
			req.Queue,
			req.CurrentState,
		)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) || result.RowsAffected == 0 {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		result.Scan(&nextUuid)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) || result.RowsAffected == 0 {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		if nextUuid == "" {
			// not found return nil
			taskProto = nil
			return nil
		}
		model.UUID = nextUuid
		result = DB.First(&model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) || result.RowsAffected == 0 {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		// swap states so if a timeout occurs we set them back to what they were
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

		result = DB.Save(model)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) || result.RowsAffected == 0 {
				// not found return nil
				taskProto = nil
				return nil
			} else {
				log.Err(result.Error)
				return result.Error
			}
		}
		// marshall result to response
		return conversions.StructToProto(model, taskProto)
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &corndogsv1alpha1.GetNextTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) UpdateTask(req *corndogsv1alpha1.UpdateTaskRequest) (*corndogsv1alpha1.UpdateTaskResponse, error) {
	taskProto := &corndogsv1alpha1.Task{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{
			UUID:         req.Uuid,
			Queue:        req.Queue,
			CurrentState: req.CurrentState,
		}
		result := DB.First(&model)
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
		if len(req.Payload) > 0 {
			model.Payload = req.Payload
		}
		result = DB.Save(&model)
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
		// marshall result to response
		return conversions.StructToProto(model, taskProto)
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}

	return &corndogsv1alpha1.UpdateTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) CompleteTask(req *corndogsv1alpha1.CompleteTaskRequest) (*corndogsv1alpha1.CompleteTaskResponse, error) {
	taskProto := &corndogsv1alpha1.Task{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{
			UUID:         req.Uuid,
			Queue:        req.Queue,
			CurrentState: req.CurrentState,
		}
		result := DB.First(&model)
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
		result = DB.Create(&archiveModel)
		if result.Error != nil {
			return result.Error
		}
		result = DB.Delete(&model)
		if result.Error != nil {
			return result.Error
		}
		// marshall result to response
		return conversions.StructToProto(archiveModel, taskProto)
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &corndogsv1alpha1.CompleteTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) CancelTask(req *corndogsv1alpha1.CancelTaskRequest) (*corndogsv1alpha1.CancelTaskResponse, error) {
	taskProto := &corndogsv1alpha1.Task{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{
			UUID:         req.Uuid,
			Queue:        req.Queue,
			CurrentState: req.CurrentState,
		}
		result := DB.First(&model)
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
		result = DB.Create(&archiveModel)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		result = DB.Delete(&model)
		if result.Error != nil {
			log.Err(result.Error)
			return result.Error
		}
		// marshall result to response
		return conversions.StructToProto(archiveModel, taskProto)
	})
	if err != nil {
		log.Err(err)
		panic(err)
	}
	return &corndogsv1alpha1.CancelTaskResponse{Task: taskProto}, err
}

func (s PostgresStore) CleanUpTimedOut(req *corndogsv1alpha1.CleanUpTimedOutRequest) (*corndogsv1alpha1.CleanUpTimedOutResponse, error) {
	var count int64 = 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		model := models.Task{}
		result := DB.Model(model).Where(`
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
	return &corndogsv1alpha1.CleanUpTimedOutResponse{TimedOut: count}, err
}
