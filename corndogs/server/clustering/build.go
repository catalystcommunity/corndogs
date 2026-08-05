package clustering

import (
	"hash/fnv"
	"path/filepath"
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
)

// Build marks the local node as caught up. Replication catch-up or rollback
// corrects its state after it connects to the leader.
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

// Setup creates a clustered file store. Its replication log is in DataDir/repl.
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

func seedFromID(id string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return int64(h.Sum64())
}
