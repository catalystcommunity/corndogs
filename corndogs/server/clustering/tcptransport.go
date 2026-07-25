package clustering

import (
	"bytes"
	"net"
	"sync"
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/csilrpc"
)

// TCPTransport carries cluster traffic over persistent TCP connections framed with
// the canonical CSIL StreamCarrier (4-byte length prefix) — the same framing
// CSIL-RPC uses, no HTTP. Each node dials every peer (send-only outbound
// connections, auto-reconnecting) and accepts inbound connections it reads frames
// from. Topology is delivered to clients by real **server push**: a client opens a
// connection, sends FrameSubscribe, and the node pushes FrameTopology on it
// immediately and on every leadership change — no long-poll.
type TCPTransport struct {
	self      string
	peerAddr  map[string]string // peer id -> cluster TCP address
	rpcAddr   string            // this node's advertised client RPC URL
	listenOn  string
	maxFrame  int
	engine    *Engine
	rpcAddrMu sync.RWMutex
	rpcAddrs  map[string]string // learned id -> client RPC URL (from FrameHello)

	ln       net.Listener
	outMu    sync.Mutex
	out      map[string]chan Frame // per-peer send queue
	subsMu   sync.Mutex
	subs     map[net.Conn]*csilrpc.StreamCarrier // topology watcher connections
	pushMu   sync.Mutex                          // serializes topology writes to subscriber carriers
	stop     chan struct{}
	stopOnce sync.Once
	dialWG   sync.WaitGroup
}

const clusterMaxFrame = 256 << 20 // snapshots can be large

// keepAlivePeriod probes idle TCP connections so a silently-dropped link (a NAT or
// firewall reaping an idle follower↔follower connection) is refreshed and, if truly
// dead, detected — well before the next election needs it. The application-level
// election heartbeat only flows leader→followers, so follower↔follower links would
// otherwise sit idle; this keeps every peer connection alive.
const keepAlivePeriod = 15 * time.Second

// enableKeepAlive turns on TCP keepalive with an explicit period (the OS default is
// often hours). Safe on any net.Conn; a no-op if it is not a *net.TCPConn.
func enableKeepAlive(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(keepAlivePeriod)
	}
}

// NewTCPTransport builds the transport. Call Start (after the engine exists) to
// begin listening + dialing.
func NewTCPTransport(self, listenOn string, peerAddr map[string]string, rpcAddr string) *TCPTransport {
	return &TCPTransport{
		self:     self,
		peerAddr: peerAddr,
		rpcAddr:  rpcAddr,
		listenOn: listenOn,
		maxFrame: clusterMaxFrame,
		rpcAddrs: map[string]string{},
		out:      map[string]chan Frame{},
		subs:     map[net.Conn]*csilrpc.StreamCarrier{},
		stop:     make(chan struct{}),
	}
}

// Bind attaches the engine (whose Deliver/Topology/WatchTopology the transport
// uses). Must be called before Start.
func (t *TCPTransport) Bind(e *Engine) { t.engine = e }

// Start begins listening and dialing peers, and starts the topology-push pump.
func (t *TCPTransport) Start() error {
	ln, err := net.Listen("tcp", t.listenOn)
	if err != nil {
		return err
	}
	t.ln = ln
	go t.acceptLoop()
	// Populate all per-peer send queues under the lock before starting senders, so a
	// concurrent Send() (from the already-running engine goroutine) never races the
	// map writes.
	t.outMu.Lock()
	for id, addr := range t.peerAddr {
		if id == t.self || addr == "" {
			continue
		}
		t.out[id] = make(chan Frame, 2048)
	}
	t.outMu.Unlock()
	for id, addr := range t.peerAddr {
		if id == t.self || addr == "" {
			continue
		}
		t.outMu.Lock()
		q := t.out[id]
		t.outMu.Unlock()
		t.dialWG.Add(1)
		go t.peerSender(id, addr, q)
	}
	go t.pushLoop()
	return nil
}

// Send implements Transport: enqueue a frame to the target peer's send queue.
// Non-blocking; drops if the queue is full (protocol tolerates loss).
func (t *TCPTransport) Send(f Frame) {
	t.outMu.Lock()
	q := t.out[f.To]
	t.outMu.Unlock()
	if q == nil {
		return
	}
	select {
	case q <- f:
	default:
	}
}

// peerSender maintains one auto-reconnecting outbound connection to a peer and
// drains its send queue onto it, prefixing a FrameHello so the peer learns our RPC
// address.
func (t *TCPTransport) peerSender(id, addr string, q chan Frame) {
	defer t.dialWG.Done()
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			if t.sleep(500 * time.Millisecond) {
				return
			}
			continue
		}
		enableKeepAlive(conn)
		carrier := csilrpc.NewStreamCarrierWithMaxFrame(conn, t.maxFrame)
		// Advertise who we are + our RPC URL first.
		if t.sendFrame(carrier, Frame{Kind: FrameHello, From: t.self, To: id, Addr: t.rpcAddr}) != nil {
			conn.Close()
			if t.sleep(500 * time.Millisecond) {
				return
			}
			continue
		}
		if t.drainTo(carrier, q) {
			conn.Close()
			return // stopped
		}
		conn.Close() // connection broke; loop reconnects
	}
}

// drainTo sends queued frames until the connection errors or the transport stops.
// Returns true if it stopped (vs a connection error).
func (t *TCPTransport) drainTo(carrier *csilrpc.StreamCarrier, q chan Frame) bool {
	for {
		select {
		case <-t.stop:
			return true
		case f := <-q:
			if t.sendFrame(carrier, f) != nil {
				return false
			}
		}
	}
}

func (t *TCPTransport) sendFrame(carrier *csilrpc.StreamCarrier, f Frame) error {
	b, err := MarshalFrame(f)
	if err != nil {
		return err
	}
	return carrier.SendFrame(b)
}

// acceptLoop accepts inbound connections and reads frames from each.
func (t *TCPTransport) acceptLoop() {
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			select {
			case <-t.stop:
				return
			default:
				continue
			}
		}
		go t.readConn(conn)
	}
}

// readConn reads frames from one inbound connection, dispatching control frames to
// the transport and data frames to the engine. A FrameSubscribe turns the
// connection into a topology-push channel.
func (t *TCPTransport) readConn(conn net.Conn) {
	enableKeepAlive(conn)
	carrier := csilrpc.NewStreamCarrierWithMaxFrame(conn, t.maxFrame)
	defer conn.Close()
	for {
		b, err := carrier.RecvFrame()
		if err != nil || b == nil {
			t.removeSub(conn)
			return
		}
		f, derr := DecodeFrame(bytes.NewReader(b))
		if derr != nil {
			continue
		}
		switch f.Kind {
		case FrameHello:
			t.rpcAddrMu.Lock()
			t.rpcAddrs[f.From] = f.Addr
			t.rpcAddrMu.Unlock()
		case FrameSubscribe:
			t.addSub(conn, carrier)
			t.pushOne(carrier) // immediate current view
		default:
			if t.engine != nil {
				t.engine.Deliver(f)
			}
		}
	}
}

func (t *TCPTransport) addSub(conn net.Conn, c *csilrpc.StreamCarrier) {
	t.subsMu.Lock()
	t.subs[conn] = c
	t.subsMu.Unlock()
}

func (t *TCPTransport) removeSub(conn net.Conn) {
	t.subsMu.Lock()
	delete(t.subs, conn)
	t.subsMu.Unlock()
}

// pushLoop pushes topology to all subscribers whenever leadership changes.
func (t *TCPTransport) pushLoop() {
	for {
		ch := t.engine.WatchTopology()
		select {
		case <-t.stop:
			return
		case <-ch:
			t.broadcastTopology()
		}
	}
}

func (t *TCPTransport) broadcastTopology() {
	t.subsMu.Lock()
	carriers := make([]*csilrpc.StreamCarrier, 0, len(t.subs))
	for _, c := range t.subs {
		carriers = append(carriers, c)
	}
	t.subsMu.Unlock()
	for _, c := range carriers {
		t.pushOne(c)
	}
}

// pushOne sends the current topology (leader id + RPC addr + epoch) on a carrier.
// pushMu serializes these writes because a subscriber's carrier is written from two
// goroutines — the immediate push in readConn (on FrameSubscribe) and the pushLoop
// broadcast on a leadership change — and interleaved length-prefixed frames would
// corrupt the stream.
func (t *TCPTransport) pushOne(c *csilrpc.StreamCarrier) {
	topo := t.engine.Topology()
	t.pushMu.Lock()
	_ = t.sendFrame(c, Frame{Kind: FrameTopology, From: topo.LeaderID, Addr: t.leaderRPCAddr(topo.LeaderID), Epoch: topo.Epoch})
	t.pushMu.Unlock()
}

// CurrentLeaderRPCAddr returns the current leader's advertised client RPC URL (for
// the not-leader redirect), or "" if unknown.
func (t *TCPTransport) CurrentLeaderRPCAddr() string {
	if t.engine == nil {
		return ""
	}
	return t.leaderRPCAddr(t.engine.Topology().LeaderID)
}

// leaderRPCAddr returns the leader's advertised client RPC URL: our own if we lead,
// else what we learned from that peer's FrameHello.
func (t *TCPTransport) leaderRPCAddr(leaderID string) string {
	if leaderID == t.self {
		return t.rpcAddr
	}
	t.rpcAddrMu.RLock()
	defer t.rpcAddrMu.RUnlock()
	return t.rpcAddrs[leaderID]
}

func (t *TCPTransport) sleep(d time.Duration) (stopped bool) {
	select {
	case <-t.stop:
		return true
	case <-time.After(d):
		return false
	}
}

// Close stops the transport.
func (t *TCPTransport) Close() {
	t.stopOnce.Do(func() { close(t.stop) })
	if t.ln != nil {
		t.ln.Close()
	}
}
