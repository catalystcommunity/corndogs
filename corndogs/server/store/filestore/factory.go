package filestore

import (
	"fmt"

	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
)

// Compile-time proof the backend satisfies the store interface.
var _ store.Store = (*BoltStore)(nil)

// New returns the file-backed store. bbolt is the only backend; the Backend
// field is retained for forward compatibility and must be empty or "bolt".
func New(cfg Config) (store.Store, error) {
	switch cfg.Backend {
	case "bolt", "":
		return NewBoltStore(cfg), nil
	default:
		return nil, fmt.Errorf("unknown filestore backend %q (only \"bolt\" is supported)", cfg.Backend)
	}
}
