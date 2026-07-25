package clustering

import (
	"bytes"
	"io"
	"testing"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
	"github.com/stretchr/testify/require"
)

// TestFrameCodecRoundTrip: every frame kind survives encode→decode, and a stream
// of frames parses back in order ending in a clean io.EOF.
func TestFrameCodecRoundTrip(t *testing.T) {
	frames := []Frame{
		{Kind: FrameMsg, From: "n1", To: "n2", Msg: cluster.Message{Type: cluster.MsgHeartbeat, Epoch: 7, LSN: 42, Bid: 3.5}},
		{Kind: FrameMsg, From: "n2", To: "n1", Msg: cluster.Message{Type: cluster.MsgHeartbeatAck, Epoch: 7, AckLSN: 40}},
		{Kind: FrameMsg, From: "n3", To: "n1", Msg: cluster.Message{Type: cluster.MsgBid, Epoch: 8, Bid: 9.99}},
		{Kind: FrameBatch, From: "n1", To: "n2", Batch: filestore.MutationBatch{LSN: 42, Mutations: []filestore.Mutation{
			{Bucket: []byte("tasks"), Key: []byte("k"), Value: []byte("v")},
			{Bucket: []byte("uuid"), Key: []byte("u"), Delete: true},
		}}},
		{Kind: FrameCatchupReq, From: "n2", To: "n1", AfterLSN: 10},
		{Kind: FrameSnapshotReq, From: "n2", To: "n1", AfterLSN: 0},
		{Kind: FrameSnapshot, From: "n1", To: "n2", SnapshotLSN: 100, Snapshot: []byte("snapshot-bytes-here")},
	}
	var buf bytes.Buffer
	for _, f := range frames {
		require.NoError(t, EncodeFrame(&buf, f))
	}
	for i, want := range frames {
		got, err := DecodeFrame(&buf)
		require.NoError(t, err, "frame %d", i)
		require.Equal(t, want.Kind, got.Kind, "frame %d kind", i)
		require.Equal(t, want.From, got.From)
		require.Equal(t, want.To, got.To)
		switch want.Kind {
		case FrameMsg:
			require.Equal(t, want.Msg.Type, got.Msg.Type)
			require.Equal(t, want.Msg.Epoch, got.Msg.Epoch)
			require.Equal(t, want.Msg.LSN, got.Msg.LSN)
			require.Equal(t, want.Msg.AckLSN, got.Msg.AckLSN)
			require.InDelta(t, want.Msg.Bid, got.Msg.Bid, 0)
		case FrameBatch:
			require.Equal(t, want.Batch.LSN, got.Batch.LSN)
			require.Equal(t, len(want.Batch.Mutations), len(got.Batch.Mutations))
		case FrameCatchupReq, FrameSnapshotReq:
			require.Equal(t, want.AfterLSN, got.AfterLSN)
		case FrameSnapshot:
			require.Equal(t, want.SnapshotLSN, got.SnapshotLSN)
			require.Equal(t, want.Snapshot, got.Snapshot)
		}
	}
	_, err := DecodeFrame(&buf)
	require.ErrorIs(t, err, io.EOF)
}
