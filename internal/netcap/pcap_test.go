package netcap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestPCAPReaderReadsPackets(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.LittleEndian, false, 128, LinkTypeEthernet)
	writePCAPPacket(t, &b, binary.LittleEndian, 10, 25, []byte{1, 2, 3, 4}, 60)

	r, err := NewPCAPReader(&b)
	if err != nil {
		t.Fatalf("NewPCAPReader: %v", err)
	}
	if r.LinkType != LinkTypeEthernet || r.SnapLen != 128 || r.Nanosecond {
		t.Fatalf("reader metadata = %+v", r)
	}
	pkt, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if pkt.TimestampSec != 10 || pkt.TimestampFrac != 25 || pkt.CapturedLen != 4 || pkt.OriginalLen != 60 {
		t.Fatalf("packet metadata = %+v", pkt)
	}
	if !bytes.Equal(pkt.Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("packet data = %v", pkt.Data)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next EOF = %v, want io.EOF", err)
	}
}

func TestPCAPReaderReadsBigEndianNanosecondPCAP(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.BigEndian, true, 96, LinkTypeRaw)

	r, err := NewPCAPReader(&b)
	if err != nil {
		t.Fatalf("NewPCAPReader: %v", err)
	}
	if !r.Nanosecond || r.LinkType != LinkTypeRaw || r.SnapLen != 96 {
		t.Fatalf("reader metadata = %+v", r)
	}
}

func TestPCAPReaderRejectsInvalidInput(t *testing.T) {
	if _, err := NewPCAPReader(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("expected short header error")
	}
	var badMagic bytes.Buffer
	badMagic.Write(bytes.Repeat([]byte{0}, 24))
	if _, err := NewPCAPReader(&badMagic); err == nil {
		t.Fatal("expected bad magic error")
	}
	var badVersion bytes.Buffer
	writePCAPHeader(t, &badVersion, binary.LittleEndian, false, 128, LinkTypeEthernet)
	raw := badVersion.Bytes()
	raw[4] = 3
	if _, err := NewPCAPReader(bytes.NewReader(raw)); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestPCAPReaderRejectsPacketLongerThanSnapLen(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.LittleEndian, false, 2, LinkTypeEthernet)
	writePCAPPacket(t, &b, binary.LittleEndian, 1, 2, []byte{1, 2, 3}, 3)

	r, err := NewPCAPReader(&b)
	if err != nil {
		t.Fatalf("NewPCAPReader: %v", err)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("expected snaplen error")
	}
}

func TestPCAPReaderRejectsTruncatedPacketHeader(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.LittleEndian, false, 128, LinkTypeEthernet)
	b.Write([]byte{1, 2, 3})

	r, err := NewPCAPReader(&b)
	if err != nil {
		t.Fatalf("NewPCAPReader: %v", err)
	}
	if _, err := r.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("Next = %v, want non-EOF truncated header error", err)
	}
}

func TestPCAPReaderRejectsOversizedPacketAllocation(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.LittleEndian, false, 0, LinkTypeEthernet)
	write32(t, &b, binary.LittleEndian, 1)
	write32(t, &b, binary.LittleEndian, 2)
	write32(t, &b, binary.LittleEndian, DefaultSnapLen+1)
	write32(t, &b, binary.LittleEndian, DefaultSnapLen+1)

	r, err := NewPCAPReader(&b)
	if err != nil {
		t.Fatalf("NewPCAPReader: %v", err)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("expected oversized packet error")
	}
}

func writePCAPHeader(t *testing.T, b *bytes.Buffer, order binary.ByteOrder, nanos bool, snapLen, linkType uint32) {
	t.Helper()
	switch {
	case order == binary.LittleEndian && nanos:
		b.Write([]byte{0x4d, 0x3c, 0xb2, 0xa1})
	case order == binary.LittleEndian:
		b.Write([]byte{0xd4, 0xc3, 0xb2, 0xa1})
	case nanos:
		b.Write([]byte{0xa1, 0xb2, 0x3c, 0x4d})
	default:
		b.Write([]byte{0xa1, 0xb2, 0xc3, 0xd4})
	}
	write16(t, b, order, 2)
	write16(t, b, order, 4)
	write32(t, b, order, 0)
	write32(t, b, order, 0)
	write32(t, b, order, snapLen)
	write32(t, b, order, linkType)
}

func writePCAPPacket(t *testing.T, b *bytes.Buffer, order binary.ByteOrder, sec, frac uint32, data []byte, origLen uint32) {
	t.Helper()
	write32(t, b, order, sec)
	write32(t, b, order, frac)
	write32(t, b, order, uint32(len(data)))
	write32(t, b, order, origLen)
	b.Write(data)
}

func write16(t *testing.T, b *bytes.Buffer, order binary.ByteOrder, n uint16) {
	t.Helper()
	var tmp [2]byte
	order.PutUint16(tmp[:], n)
	b.Write(tmp[:])
}

func write32(t *testing.T, b *bytes.Buffer, order binary.ByteOrder, n uint32) {
	t.Helper()
	var tmp [4]byte
	order.PutUint32(tmp[:], n)
	b.Write(tmp[:])
}
