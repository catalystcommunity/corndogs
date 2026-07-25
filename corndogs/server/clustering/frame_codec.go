package clustering

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
)

// Frame wire encoding for the cluster peer transport. A frame is self-framing so a
// stream of them (over a TCP StreamCarrier) parses back unambiguously:
//
//	kind(u8) len(from) from len(to) to <kind-specific payload>
//
// Payloads: Msg = type,epoch,lsn,ackLSN,bid ; Batch = EncodeBatch ;
// CatchupReq/SnapshotReq = afterLSN ; Snapshot = lsn,len,bytes. All big-endian.

// maxDecodeLen caps a single length-prefixed field read from the wire before its
// bytes are allocated, so a corrupt or hostile header can't drive an oversized
// make() ahead of the (possibly failing) read. It matches the StreamCarrier frame
// cap (clusterMaxFrame), the largest legitimate snapshot a peer can ship.
const maxDecodeLen = clusterMaxFrame

func writeStr(w *bufio.Writer, s string) error {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(s)))
	if _, err := w.Write(l[:]); err != nil {
		return err
	}
	_, err := w.WriteString(s)
	return err
}

func readStr(r io.Reader) (string, error) {
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(l[:])
	if n == 0 {
		return "", nil
	}
	if n > maxDecodeLen {
		return "", fmt.Errorf("clustering: string length %d exceeds cap %d", n, maxDecodeLen)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func putU64(w *bufio.Writer, v uint64) error {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func readU64(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b[:]), nil
}

// EncodeFrame writes one self-framed frame to w.
func EncodeFrame(w io.Writer, f Frame) error {
	bw := bufio.NewWriter(w)
	if err := bw.WriteByte(byte(f.Kind)); err != nil {
		return err
	}
	if err := writeStr(bw, f.From); err != nil {
		return err
	}
	if err := writeStr(bw, f.To); err != nil {
		return err
	}
	switch f.Kind {
	case FrameMsg:
		if err := bw.WriteByte(byte(f.Msg.Type)); err != nil {
			return err
		}
		for _, v := range []uint64{f.Msg.Epoch, f.Msg.LSN, f.Msg.AckLSN, math.Float64bits(f.Msg.Bid)} {
			if err := putU64(bw, v); err != nil {
				return err
			}
		}
	case FrameBatch:
		if err := bw.Flush(); err != nil {
			return err
		}
		return EncodeBatchFrame(w, f.Batch)
	case FrameCatchupReq, FrameSnapshotReq:
		if err := putU64(bw, f.AfterLSN); err != nil {
			return err
		}
	case FrameSnapshot:
		if err := putU64(bw, f.SnapshotLSN); err != nil {
			return err
		}
		if err := putU64(bw, uint64(len(f.Snapshot))); err != nil {
			return err
		}
		if _, err := bw.Write(f.Snapshot); err != nil {
			return err
		}
	case FrameHello:
		if err := writeStr(bw, f.Addr); err != nil {
			return err
		}
	case FrameSubscribe:
		// no payload
	case FrameTopology:
		if err := writeStr(bw, f.Addr); err != nil {
			return err
		}
		if err := putU64(bw, f.Epoch); err != nil {
			return err
		}
	default:
		return fmt.Errorf("clustering: unknown frame kind %d", f.Kind)
	}
	return bw.Flush()
}

// EncodeBatchFrame writes a MutationBatch using the filestore codec (exported here
// so the frame codec and the log share one framing).
func EncodeBatchFrame(w io.Writer, b filestore.MutationBatch) error {
	return filestore.EncodeBatch(w, b)
}

// DecodeFrame reads one frame previously written by EncodeFrame. Returns io.EOF at
// a clean boundary.
func DecodeFrame(r io.Reader) (Frame, error) {
	var kb [1]byte
	if _, err := io.ReadFull(r, kb[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Kind: FrameKind(kb[0])}
	var err error
	if f.From, err = readStr(r); err != nil {
		return Frame{}, unexpectedEOF(err)
	}
	if f.To, err = readStr(r); err != nil {
		return Frame{}, unexpectedEOF(err)
	}
	switch f.Kind {
	case FrameMsg:
		var tb [1]byte
		if _, err := io.ReadFull(r, tb[:]); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
		f.Msg.Type = cluster.MsgType(tb[0])
		f.Msg.From, f.Msg.To = f.From, f.To
		fields := make([]uint64, 4)
		for i := range fields {
			if fields[i], err = readU64(r); err != nil {
				return Frame{}, unexpectedEOF(err)
			}
		}
		f.Msg.Epoch, f.Msg.LSN, f.Msg.AckLSN = fields[0], fields[1], fields[2]
		f.Msg.Bid = math.Float64frombits(fields[3])
	case FrameBatch:
		if f.Batch, err = filestore.DecodeBatch(r); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
	case FrameCatchupReq, FrameSnapshotReq:
		if f.AfterLSN, err = readU64(r); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
	case FrameSnapshot:
		if f.SnapshotLSN, err = readU64(r); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
		n, err := readU64(r)
		if err != nil {
			return Frame{}, unexpectedEOF(err)
		}
		if n > maxDecodeLen {
			return Frame{}, fmt.Errorf("clustering: snapshot length %d exceeds cap %d", n, maxDecodeLen)
		}
		f.Snapshot = make([]byte, n)
		if _, err := io.ReadFull(r, f.Snapshot); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
	case FrameHello:
		if f.Addr, err = readStr(r); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
	case FrameSubscribe:
		// no payload
	case FrameTopology:
		if f.Addr, err = readStr(r); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
		if f.Epoch, err = readU64(r); err != nil {
			return Frame{}, unexpectedEOF(err)
		}
	default:
		return Frame{}, fmt.Errorf("clustering: unknown frame kind %d", f.Kind)
	}
	return f, nil
}

func unexpectedEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// MarshalFrame is a convenience returning the frame's bytes (the payload the
// StreamCarrier length-prefixes and sends).
func MarshalFrame(f Frame) ([]byte, error) {
	var buf bytes.Buffer
	if err := EncodeFrame(&buf, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
