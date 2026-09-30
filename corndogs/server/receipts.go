package server

import (
	"context"
	"errors"
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/clustering"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/CatalystCommunity/corndogs/corndogs/server/metrics"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	zlog "github.com/rs/zerolog/log"
)

const (
	receiptPurgeInterval = time.Minute
	receiptPurgeBatch    = 1000
)

// logResiliencePolicy states the effective policy at startup. Compatibility
// mode is a supported choice, but the operator must see it.
func logResiliencePolicy() {
	// Log without a level, so the default LOGLEVEL=error still prints it.
	zlog.Log().Str("server_version", config.Version).
		Str("submission_key_policy", config.SubmissionKeyPolicy).
		Str("task_guard_policy", config.TaskGuardPolicy).
		Str("receipt_retention", config.ReceiptRetention.String()).
		Msg("resilience policy (compatibility accepts released clients without submission keys or task guards; set required after all clients use the resilience operations)")
}

// startReceiptPurge deletes expired receipts in bounded batches. Expiry is also
// checked on every read, so a late purge never extends deduplication. In a
// cluster only the leader purges; followers receive the deletions.
func startReceiptPurge(interval time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				purgeExpiredReceipts()
			}
		}
	}()
	return func() { close(done) }
}

func purgeExpiredReceipts() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		n, err := store.AppStore.PurgeExpiredReceipts(ctx, time.Now().UnixNano(), receiptPurgeBatch)
		if err != nil {
			if !errors.Is(err, clustering.ErrNotLeader) && !isRedirect(err) {
				zlog.Warn().Err(err).Msg("receipt purge failed")
			}
			return
		}
		metrics.CountPurged(n)
		if n < receiptPurgeBatch {
			return
		}
	}
}

func isRedirect(err error) bool {
	return err != nil && len(err.Error()) >= 10 && err.Error()[:10] == "not-leader"
}
