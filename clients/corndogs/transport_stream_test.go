package corndogs

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeRPCServer is a minimal CSIL-RPC-over-TCP server for exercising the client
// StreamTransport without the real corndogs server. It echoes each request's
// payload back under variant "Echo" (or "$pong" for a $ping), and — to prove
// id-multiplexing — replies OUT OF ORDER: earlier requests are delayed longer, so
// responses return in reverse, and the transport must still route each to its own
// caller by correlation id.
type fakeRPCServer struct {
	ln   net.Listener
	reqN int64
	mu   sync.Mutex
}

func startFakeRPCServer(t *testing.T) *fakeRPCServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeRPCServer{ln: ln}
	go s.acceptLoop()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeRPCServer) addr() string { return s.ln.Addr().String() }

func (s *fakeRPCServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeRPCServer) handle(conn net.Conn) {
	defer conn.Close()
	var writeMu sync.Mutex
	seq := 0
	for {
		frame, err := readFrame(conn, maxFrame)
		if err != nil || frame == nil {
			return
		}
		val, derr := cborDecode(frame)
		if derr != nil {
			return
		}
		var id uint64
		if idv, ok := cborMapGet(val, "id"); ok {
			if i, e := cborAsI64(idv); e == nil {
				id = uint64(i)
			}
		}
		op := ""
		if ov, ok := cborMapGet(val, "op"); ok {
			op, _ = cborAsText(ov)
		}
		var payload []byte
		if pv, ok := cborMapGet(val, "payload"); ok {
			if tag, ok := pv.(cborTag); ok {
				pv = tag.inner
			}
			payload, _ = cborAsBytes(pv)
		}
		variant := "Echo"
		if op == opPing {
			variant, payload = "$pong", nil
		}
		// Delay earlier requests more, so replies come back reversed.
		delay := time.Duration(20-seq) * time.Millisecond
		if delay < 0 {
			delay = 0
		}
		seq++
		go func(id uint64, variant string, payload []byte, delay time.Duration) {
			time.Sleep(delay)
			resp := cborEncode(cborMap{
				{key: cborText("v"), val: cborUint(1)},
				{key: cborText("id"), val: cborUint(id)},
				{key: cborText("status"), val: cborUint(0)},
				{key: cborText("variant"), val: cborText(variant)},
				{key: cborText("payload"), val: cborTag{num: tagEncodedCBOR, inner: cborBytes(payload)}},
			})
			writeMu.Lock()
			_ = writeFrame(conn, resp, maxFrame)
			writeMu.Unlock()
		}(id, variant, payload, delay)
	}
}

// TestStreamTransportMultiplexing proves many concurrent calls on ONE connection,
// with out-of-order server replies, are each routed back to the right caller by id.
func TestStreamTransportMultiplexing(t *testing.T) {
	s := startFakeRPCServer(t)
	tr := &StreamTransport{Addr: s.addr()}
	defer tr.Close()

	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := []byte(fmt.Sprintf("payload-%d", i))
			got, err := tr.Call(context.Background(), "CorndogsService", "SubmitTask", want)
			if err != nil {
				errs <- fmt.Errorf("call %d: %w", i, err)
				return
			}
			if string(got) != string(want) {
				errs <- fmt.Errorf("call %d: got %q want %q (id mismatch!)", i, got, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// TestStreamTransportPingAndHeartbeat exercises the control-plane ping plus the
// sync and async heartbeat starts.
func TestStreamTransportPingAndHeartbeat(t *testing.T) {
	s := startFakeRPCServer(t)
	tr := &StreamTransport{Addr: s.addr()}
	defer tr.Close()

	// Single ping.
	if err := tr.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Async start: runs in the background until stop.
	stop := tr.StartHeartbeat(10 * time.Millisecond)
	time.Sleep(60 * time.Millisecond) // let a few beats fire
	stop()

	// Sync start: blocks until ctx cancel, returning (it pinged along the way).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := tr.RunHeartbeat(ctx, 10*time.Millisecond); err == nil {
		t.Fatal("RunHeartbeat should return non-nil when ctx expires")
	}
}

// TestStreamTransportReconnect proves the transport re-dials after the connection
// drops (server closed), so a later call succeeds.
func TestStreamTransportReconnect(t *testing.T) {
	s := startFakeRPCServer(t)
	tr := &StreamTransport{Addr: s.addr()}
	defer tr.Close()

	if _, err := tr.Call(context.Background(), "CorndogsService", "SubmitTask", []byte("a")); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Force the current connection down; the next call must re-dial.
	tr.mu.Lock()
	conn := tr.conn
	tr.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	// Give the read loop a moment to notice and tear down.
	time.Sleep(20 * time.Millisecond)
	if _, err := tr.Call(context.Background(), "CorndogsService", "SubmitTask", []byte("b")); err != nil {
		t.Fatalf("call after reconnect: %v", err)
	}
}
