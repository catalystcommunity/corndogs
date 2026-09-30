package corndogs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewSubmissionKey returns a random key for one logical submission. Create it
// once, store it with the work item if the caller must survive a restart, and
// send the same key on every retry of that submission.
func NewSubmissionKey() string { return randomID() }

// NewOperationID returns a random id for one guarded mutation or claim. Send
// the same id on every retry of that operation.
func NewOperationID() string { return randomID() }

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("corndogs: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// RequireFeatures asks the server which resilience features it enforces and
// returns an error that wraps ErrUnsupported when one of features is missing.
// A server before 0.8.0 does not know GetServerInfo, so the call itself
// returns ErrUnsupported. Call it before the first keyed or guarded request
// when the caller must not continue without those guarantees.
func (c *CorndogsClient) RequireFeatures(ctx context.Context, features ...string) (GetServerInfoResponse, error) {
	info, err := c.GetServerInfo(ctx, GetServerInfoRequest{})
	if err != nil {
		return info, err
	}
	have := map[string]bool{}
	for _, f := range info.Features {
		have[f] = true
	}
	for _, f := range features {
		if !have[f] {
			return info, &ClientError{Err: fmt.Errorf("%w: feature %q (server %s)", ErrUnsupported, f, info.ServerVersion)}
		}
	}
	return info, nil
}
