package clustering

import (
	"hash/fnv"
	"path/filepath"
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
)

// Build assembles a Replicator from operator Settings: it opens the replication
// log under dataDir/repl, constructs the election node over the configured
// membership, and wires them to the store and transport with the resolved
// semi-sync ack quorum. seed makes the node's random bids deterministic per
// process (pass a per-node stable value, e.g. a hash of NodeID, in production).
//
// The node is marked caught-up: Build assumes the local store is a valid starting
// point (a fresh cluster, or a restart that kept its state). A node that is
// actually behind is corrected by catch-up (it pulls missing batches) or, if it
// diverged, by the rejoin rollback — so this is safe, just optimistic.
func Build(s Settings, store *filestore.BoltStore, dataDir string, tr Transport, seed int64) (*Replicator, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	log, err := filestore.OpenReplLog(filepath.Join(dataDir, "repl"), s.ReplLogChunkMB, false)
	if err != nil {
		return nil, err
	}
	node := cluster.NewNode(s.NodeID, s.Peers, s.Election, seed, store.ReplLSN())
	node.MarkCaughtUp()
	return New(s.NodeID, node, store, log, tr, s.DurabilityAckCount()), nil
}

// Setup assembles a fully wired, ready-to-run ClusteredStore from settings and the
// operator's filestore config: it opens the local bbolt store, builds the HTTP
// transport + replicator + engine, and returns a ClusteredStore whose Initialize
// starts the engine. The replication log lives under the filestore DataDir/repl.
// This is the one call the server makes to turn on clustering.
func Setup(s Settings, fsCfg filestore.Config) (*ClusteredStore, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	local := filestore.NewBoltStore(fsCfg)
	localCleanup, err := local.Initialize()
	if err != nil {
		return nil, err
	}
	tr := NewTCPTransport(s.NodeID, s.Listen, s.PeerAddr, s.RPCAdvertise)
	rep, err := Build(s, local, fsCfg.DataDir, tr, seedFromID(s.NodeID))
	if err != nil {
		tr.Close()
		localCleanup()
		return nil, err
	}
	engine := NewEngine(rep, s, 100*time.Millisecond)
	tr.Bind(engine)
	return NewClusteredStore(local, engine, tr, localCleanup), nil
}

// seedFromID derives a stable per-node RNG seed from the node id so bids differ
// across nodes but are reproducible for one node.
func seedFromID(id string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return int64(h.Sum64())
}
