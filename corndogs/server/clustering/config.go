package clustering

import (
	"fmt"
	"strings"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
)

// Settings is the operator-facing cluster configuration, parsed from
// CORNDOGS_CLUSTER_* env. When Enabled is false the server runs the ordinary
// single-node file/postgres backend and none of this applies.
type Settings struct {
	Enabled bool

	// NodeID is this member's stable id; Peers is the full membership (including
	// this node), the seed list every member shares. PeerAddr maps each id to its
	// cluster TCP address (host:port for the StreamCarrier peer transport), parsed
	// from PEERS entries of the form id=host:port.
	NodeID   string
	Peers    []string
	PeerAddr map[string]string

	// RPCAdvertise is this node's client-facing CSIL-RPC address as a dialable
	// host:port (e.g. host:5080) — NOT a URL. The client transport dials it directly
	// over TCP (net.Dial "tcp"), and it is advertised to peers + watching clients so
	// the current leader's address can be pushed to clients over the topology stream.
	RPCAdvertise string

	// AckCount is the semi-sync durability quorum: a write is acked to the client
	// once this many followers have applied it. For split-brain safety it must be a
	// write-majority, ⌊N/2⌋ (docs §6); DurabilityAckCount() computes that default
	// when AckCount is left at -1.
	AckCount int

	// Async, when true, sets AckCount to 0 (leader acks immediately, ships in
	// background) — available but split-brain-divergent. Off by default.
	Async bool

	// Listen is the TCP StreamCarrier address this node serves peer + watch traffic
	// on (server↔server replication and the client topology push).
	Listen string

	// ReplLogChunkMB rolls the replication log into segments of this size (0 = one
	// growing segment).
	ReplLogChunkMB int

	// Election timings; zero fields fall back to cluster.DefaultConfig().
	Election cluster.Config
}

// LoadSettings reads CORNDOGS_CLUSTER_* env into Settings.
func LoadSettings() Settings {
	s := Settings{
		Enabled:        config.GetEnvAsBoolOrDefault("CORNDOGS_CLUSTER_ENABLED", "false"),
		NodeID:         config.GetEnvOrDefault("CORNDOGS_CLUSTER_NODE_ID", ""),
		Async:          config.GetEnvAsBoolOrDefault("CORNDOGS_CLUSTER_ASYNC", "false"),
		Listen:         config.GetEnvOrDefault("CORNDOGS_CLUSTER_LISTEN", "0.0.0.0:5090"),
		RPCAdvertise:   config.GetEnvOrDefault("CORNDOGS_CLUSTER_RPC_ADVERTISE", ""),
		ReplLogChunkMB: config.GetEnvAsIntOrDefault("CORNDOGS_CLUSTER_REPLLOG_CHUNK_MB", "64"),
		AckCount:       config.GetEnvAsIntOrDefault("CORNDOGS_CLUSTER_ACK_COUNT", "-1"), // -1 => write-majority default
	}
	// PEERS is a comma-separated membership list; each entry is "id" or "id=addr"
	// (addr = host:port for the peer StreamCarrier TCP transport). The id-only form
	// is for tests / single-process use.
	s.PeerAddr = map[string]string{}
	if peers := config.GetEnvOrDefault("CORNDOGS_CLUSTER_PEERS", ""); peers != "" {
		for _, p := range strings.Split(peers, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			id, addr, hasAddr := strings.Cut(p, "=")
			id = strings.TrimSpace(id)
			s.Peers = append(s.Peers, id)
			if hasAddr {
				s.PeerAddr[id] = strings.TrimSpace(addr)
			}
		}
	}
	s.Election = cluster.DefaultConfig()
	if v := config.GetEnvAsIntOrDefault("CORNDOGS_CLUSTER_HEARTBEAT_TICKS", "0"); v > 0 {
		s.Election.HeartbeatInterval = int64(v)
	}
	if v := config.GetEnvAsIntOrDefault("CORNDOGS_CLUSTER_FAILURE_TICKS", "0"); v > 0 {
		s.Election.FailureTimeout = int64(v)
	}
	return s
}

// DurabilityAckCount resolves the effective ack quorum: 0 if Async; the configured
// AckCount if set explicitly (>= 0); otherwise the write-majority default ⌊N/2⌋
// that makes the cluster split-brain-safe.
func (s Settings) DurabilityAckCount() int {
	if s.Async {
		return 0
	}
	if s.AckCount >= 0 {
		return s.AckCount
	}
	return len(s.Peers) / 2
}

// Validate checks the settings are usable for a running cluster.
func (s Settings) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.NodeID == "" {
		return fmt.Errorf("cluster: CORNDOGS_CLUSTER_NODE_ID is required when clustering is enabled")
	}
	if len(s.Peers) < 1 {
		return fmt.Errorf("cluster: CORNDOGS_CLUSTER_PEERS must list the membership")
	}
	found := false
	for _, p := range s.Peers {
		if p == s.NodeID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("cluster: NODE_ID %q must appear in PEERS %v", s.NodeID, s.Peers)
	}
	if ack := s.DurabilityAckCount(); ack > len(s.Peers)-1 {
		return fmt.Errorf("cluster: ack_count %d exceeds follower count %d", ack, len(s.Peers)-1)
	}
	return s.Election.Validate()
}
