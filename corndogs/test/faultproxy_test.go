package test

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// faultProxy forwards CSIL-RPC frames between a client and a server. When it is
// armed, it reads the next complete response frame from the server, then closes
// the client connection without sending that frame. The server has finished
// the operation, but the caller does not receive the reply. This is the
// lost-reply fault of the Opal recovery harness.
type faultProxy struct {
	ln      net.Listener
	target  string
	armed   atomic.Int32 // number of responses to drop
	dropped atomic.Int32
	wg      sync.WaitGroup
	mu      sync.Mutex
	conns   []net.Conn
}

func startFaultProxy(t *testing.T, target string) *faultProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{ln: ln, target: target}
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			_ = c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *faultProxy) Addr() string { return p.ln.Addr().String() }

// DropNextReply makes the proxy drop the next response frame.
func (p *faultProxy) DropNextReply() { p.armed.Add(1) }

// Dropped returns the number of responses dropped so far.
func (p *faultProxy) Dropped() int { return int(p.dropped.Load()) }

func (p *faultProxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		s, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, s)
		p.mu.Unlock()
		p.wg.Add(2)
		go func() { defer p.wg.Done(); _, _ = io.Copy(s, c); _ = s.Close() }()
		go func() { defer p.wg.Done(); p.pumpReplies(s, c) }()
	}
}

func (p *faultProxy) pumpReplies(server, client net.Conn) {
	defer client.Close()
	defer server.Close()
	for {
		var prefix [4]byte
		if _, err := io.ReadFull(server, prefix[:]); err != nil {
			return
		}
		frame := make([]byte, binary.BigEndian.Uint32(prefix[:]))
		if _, err := io.ReadFull(server, frame); err != nil {
			return
		}
		if n := p.armed.Load(); n > 0 && p.armed.CompareAndSwap(n, n-1) {
			p.dropped.Add(1)
			return // the complete reply is lost; the caller sees EOF
		}
		if _, err := client.Write(prefix[:]); err != nil {
			return
		}
		if _, err := client.Write(frame); err != nil {
			return
		}
	}
}
