package filestore

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestLegacyPayloadMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corndogs.bolt")
	db, err := bolt.Open(path, 0o600, nil)
	require.NoError(t, err)

	task := Task{
		UUID:            "legacy-task",
		Queue:           "q",
		CurrentState:    "submitted",
		AutoTargetState: "working",
		SubmitTime:      100,
		UpdateTime:      200,
		Timeout:         30,
		Priority:        4,
	}
	payload := []byte{0x00, 0x01, 0xfe, 0xff}
	legacyValue, err := json.Marshal(struct {
		Task
		Payload []byte `json:"payload"`
	}{
		Task:    task,
		Payload: payload,
	})
	require.NoError(t, err)
	taskKey := encodeTaskKey(&task)

	err = db.Update(func(tx *bolt.Tx) error {
		tasks, err := tx.CreateBucket(bucketTasks)
		if err != nil {
			return err
		}
		byUUID, err := tx.CreateBucket(bucketByUUID)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucket(bucketArchived); err != nil {
			return err
		}
		if err := tasks.Put(taskKey, legacyValue); err != nil {
			return err
		}
		return byUUID.Put([]byte(task.UUID), taskKey)
	})
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store := NewBoltStore(Config{
		DataDir:      dir,
		AuditDir:     dir,
		AuditEnabled: false,
		Sync:         SyncNever,
	})
	cleanup, err := store.Initialize()
	require.NoError(t, err)
	defer cleanup()

	err = store.db.View(func(tx *bolt.Tx) error {
		metadata := tx.Bucket(bucketTasks).Get(taskKey)
		require.NotContains(t, string(metadata), `"payload"`)
		require.Equal(t, payload, tx.Bucket(bucketPayloads).Get([]byte(task.UUID)))
		require.Equal(t, taskKey, tx.Bucket(bucketDeadlines).Get(encodeDeadlineKey(&task)))
		require.Equal(t, schemaVersion, tx.Bucket(bucketMeta).Get(keySchemaVersion))
		return nil
	})
	require.NoError(t, err)

	claimed, err := store.GetNextTask(ctx(), &api.GetNextTaskRequest{
		Queue:        task.Queue,
		CurrentState: task.CurrentState,
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Delivery)
	require.True(t, bytes.Equal(payload, claimed.Delivery.Payload))
}
