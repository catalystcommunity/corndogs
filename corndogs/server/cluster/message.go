package cluster

// MsgType enumerates the cluster-protocol messages. The election is a
// highest-random-bid scheme (no quorum voting), so the wire is tiny: heartbeats,
// their acks, and election bids.
type MsgType uint8

const (
	// MsgHeartbeat is broadcast by the leader every HeartbeatInterval. It asserts
	// leadership for an epoch and carries the leader's head AppliedLSN (the
	// replication target / in-sync yardstick) and the leader's winning Bid (used to
	// deterministically resolve the rare case of two same-epoch leaders).
	MsgHeartbeat MsgType = iota
	// MsgHeartbeatAck answers a heartbeat. AckLSN reports the follower's AppliedLSN
	// so the leader tracks replication progress and liveness (who is still in the
	// cluster) from the same message flow.
	MsgHeartbeatAck
	// MsgBid is broadcast by a node running an election. Bid is its random draw in
	// [0, BidMax); the highest bid in a round wins (ties broken by node id). Epoch
	// is the round's epoch.
	MsgBid
)

// Message is a single cluster-protocol frame. It is a value type with no pointers
// so the in-memory test network can copy it freely and the production transport
// can encode it directly.
type Message struct {
	Type  MsgType
	From  string
	To    string
	Epoch uint64

	// LSN is the leader's head AppliedLSN on MsgHeartbeat; unused elsewhere.
	LSN uint64
	// AckLSN is the follower's AppliedLSN on MsgHeartbeatAck.
	AckLSN uint64
	// Bid is the sender's election draw on MsgBid, and the leader's winning draw on
	// MsgHeartbeat (for same-epoch conflict resolution).
	Bid float64
}
