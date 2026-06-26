package filestore

import (
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
)

// SyncMode controls how aggressively writes are flushed to disk. It is the one
// real speed/safety dial for the file backends.
type SyncMode string

const (
	// SyncAlways fsyncs on every committed write. Safest, slowest. Default.
	SyncAlways SyncMode = "always"
	// SyncInterval flushes on a timer (~1s). Fast; a crash may lose the last
	// fraction of a second of writes.
	SyncInterval SyncMode = "interval"
	// SyncNever never explicitly fsyncs; durability is left to the OS. Fastest,
	// least safe. Useful for caches/benchmarks.
	SyncNever SyncMode = "never"
	// SyncGroup acks every write only after it is fsync'd, but coalesces all
	// in-flight writes into a single fsync (group commit). Durable like
	// "always", but with throughput that scales with concurrency. (bolt only.)
	SyncGroup SyncMode = "group"
)

// Config holds the file-backend configuration, all sourced from env vars so it
// matches the existing config style.
type Config struct {
	// Backend selects the implementation. Only "bolt" (default) is supported;
	// retained for forward compatibility.
	Backend string
	// DataDir is where the live task data file lives.
	DataDir string
	// AuditDir is where the append-only audit log is written. It defaults to
	// DataDir so the audit log lives alongside the data unless an operator
	// explicitly points it at a separate volume.
	AuditDir string
	// AuditEnabled toggles the audit log entirely.
	AuditEnabled bool
	// AuditChunkMB rolls the audit log to a new segment file once the active
	// one reaches this many megabytes (0 = never roll; one growing file). The
	// size is a threshold, not a hard cap — a segment may slightly exceed it.
	AuditChunkMB int
	// Sync is the durability mode shared by the data store and the audit log.
	Sync SyncMode
	// GroupMaxBatch caps how many writes one group commit may coalesce
	// (<=0 means unbounded). Only used when Sync == SyncGroup.
	GroupMaxBatch int
	// GroupMaxDelay optionally lingers this long to widen a batch before
	// committing. 0 means commit opportunistically (whatever already queued).
	GroupMaxDelay time.Duration
}

// LoadConfig reads the file-backend configuration from the environment.
func LoadConfig() Config {
	dataDir := config.GetEnvOrDefault("CORNDOGS_FILESTORE_DIR", "./corndogs-data")
	return Config{
		Backend: config.GetEnvOrDefault("CORNDOGS_FILESTORE_BACKEND", "bolt"),
		DataDir: dataDir,
		// Default the audit dir to the data dir; only differs if explicitly set.
		AuditDir:     config.GetEnvOrDefault("CORNDOGS_FILESTORE_AUDIT_DIR", dataDir),
		AuditEnabled:  config.GetEnvAsBoolOrDefault("CORNDOGS_FILESTORE_AUDIT_ENABLED", "true"),
		AuditChunkMB:  config.GetEnvAsIntOrDefault("CORNDOGS_FILESTORE_AUDIT_CHUNK_MB", "250"),
		Sync:          SyncMode(config.GetEnvOrDefault("CORNDOGS_FILESTORE_SYNC", string(SyncGroup))),
		GroupMaxBatch: config.GetEnvAsIntOrDefault("CORNDOGS_FILESTORE_GROUP_MAX_BATCH", "0"),
		GroupMaxDelay: config.GetEnvAsDurationOrDefault("CORNDOGS_FILESTORE_GROUP_MAX_DELAY", "0s"),
	}
}
