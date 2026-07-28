package filestore

import (
	"bytes"
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

const migrationBatchSize = 1000

var (
	keySchemaVersion   = []byte("schema-version")
	keyMigrationCursor = []byte("task-payload-migration-cursor")
	schemaVersion      = []byte{2}
)

// migrateTaskStorage moves legacy JSON payload fields into the raw payload
// bucket and builds the deadline index. Each batch is atomic. The cursor lets a
// restart continue after the last committed task.
func migrateTaskStorage(db *bolt.DB) error {
	for {
		done := false
		err := db.Update(func(tx *bolt.Tx) error {
			meta := tx.Bucket(bucketMeta)
			if bytes.Equal(meta.Get(keySchemaVersion), schemaVersion) {
				done = true
				return nil
			}

			tasks := tx.Bucket(bucketTasks)
			payloads := tx.Bucket(bucketPayloads)
			deadlines := tx.Bucket(bucketDeadlines)
			cursor := tasks.Cursor()

			var key, value []byte
			if resume := meta.Get(keyMigrationCursor); resume != nil {
				key, value = cursor.Seek(resume)
				if bytes.Equal(key, resume) {
					key, value = cursor.Next()
				}
			} else {
				key, value = cursor.First()
			}

			processed := 0
			var last []byte
			for ; key != nil && processed < migrationBatchSize; key, value = cursor.Next() {
				var task Task
				if err := json.Unmarshal(value, &task); err != nil {
					return err
				}

				// A payload member marks the legacy record format. JSON represents
				// []byte as base64 text and nil as null.
				var legacy struct {
					Payload json.RawMessage `json:"payload"`
				}
				if err := json.Unmarshal(value, &legacy); err != nil {
					return err
				}
				if legacy.Payload != nil {
					var payload []byte
					if !bytes.Equal(legacy.Payload, []byte("null")) {
						if err := json.Unmarshal(legacy.Payload, &payload); err != nil {
							return err
						}
					}
					if payload == nil {
						payload = []byte{}
					}
					if err := payloads.Put([]byte(task.UUID), payload); err != nil {
						return err
					}
					metadata, err := json.Marshal(&task)
					if err != nil {
						return err
					}
					if err := tasks.Put(key, metadata); err != nil {
						return err
					}
				}

				if task.Timeout > 0 {
					if err := deadlines.Put(encodeDeadlineKey(&task), key); err != nil {
						return err
					}
				}
				last = bytes.Clone(key)
				processed++
			}

			if key == nil {
				if err := meta.Put(keySchemaVersion, schemaVersion); err != nil {
					return err
				}
				if err := meta.Delete(keyMigrationCursor); err != nil {
					return err
				}
				done = true
				return nil
			}
			return meta.Put(keyMigrationCursor, last)
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}
