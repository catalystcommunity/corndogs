package implementations

import (
	"crypto/sha256"
	"encoding/binary"
)

// digestVersion names the request digest format. The format is written here by
// hand, not taken from the CBOR codec, so a codec change cannot change the
// digest of an accepted request. Change the version when the format changes.
const digestVersion = "corndogs-request-digest-v1"

// digest builds a SHA-256 over typed, length-prefixed fields in a fixed order.
// Absent optional fields and present empty fields have different encodings.
type digest struct{ buf []byte }

func newDigest(op string) *digest {
	d := &digest{}
	d.text(digestVersion)
	d.text(op)
	return d
}

func (d *digest) tag(t byte) { d.buf = append(d.buf, t) }

func (d *digest) text(s string) *digest {
	d.tag('s')
	d.buf = binary.BigEndian.AppendUint64(d.buf, uint64(len(s)))
	d.buf = append(d.buf, s...)
	return d
}

func (d *digest) bytes(b []byte) *digest {
	d.tag('b')
	d.buf = binary.BigEndian.AppendUint64(d.buf, uint64(len(b)))
	d.buf = append(d.buf, b...)
	return d
}

func (d *digest) int(v int64) *digest {
	d.tag('i')
	d.buf = binary.BigEndian.AppendUint64(d.buf, uint64(v))
	return d
}

func (d *digest) bool(v bool) *digest {
	d.tag('t')
	if v {
		d.buf = append(d.buf, 1)
	} else {
		d.buf = append(d.buf, 0)
	}
	return d
}

func (d *digest) optText(s *string) *digest {
	if s == nil {
		d.tag('n')
		return d
	}
	return d.text(*s)
}

func (d *digest) optBytes(b *[]byte) *digest {
	if b == nil {
		d.tag('n')
		return d
	}
	return d.bytes(*b)
}

func (d *digest) optInt(v *int64) *digest {
	if v == nil {
		d.tag('n')
		return d
	}
	return d.int(*v)
}

func (d *digest) list(items []string) *digest {
	d.tag('l')
	d.buf = binary.BigEndian.AppendUint64(d.buf, uint64(len(items)))
	for _, s := range items {
		d.text(s)
	}
	return d
}

func (d *digest) sum() []byte {
	h := sha256.Sum256(d.buf)
	return h[:]
}
