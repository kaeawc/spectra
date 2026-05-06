package netcap

import (
	"encoding/binary"
	"fmt"
	"io"
)

const pcapHeaderLen = 24

// PCAP link-layer types Spectra currently recognizes.
const (
	LinkTypeNull     = 0
	LinkTypeEthernet = 1
	LinkTypeRaw      = 101
	LinkTypeLoop     = 108
	LinkTypeLinuxSLL = 113
)

// PCAPReader streams packets from a classic pcap file. It intentionally does
// not support pcapng yet.
type PCAPReader struct {
	r          io.Reader
	order      binary.ByteOrder
	LinkType   uint32
	SnapLen    uint32
	Nanosecond bool
}

// PCAPPacket is one captured packet record.
type PCAPPacket struct {
	TimestampSec  uint32
	TimestampFrac uint32
	CapturedLen   uint32
	OriginalLen   uint32
	Data          []byte
}

// NewPCAPReader reads and validates the pcap global header.
func NewPCAPReader(r io.Reader) (*PCAPReader, error) {
	var hdr [pcapHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("read pcap header: %w", err)
	}
	order, nanos, err := pcapByteOrder(hdr[:4])
	if err != nil {
		return nil, err
	}
	major := order.Uint16(hdr[4:6])
	if major != 2 {
		return nil, fmt.Errorf("unsupported pcap version %d.%d", major, order.Uint16(hdr[6:8]))
	}
	return &PCAPReader{
		r:          r,
		order:      order,
		SnapLen:    order.Uint32(hdr[16:20]),
		LinkType:   order.Uint32(hdr[20:24]),
		Nanosecond: nanos,
	}, nil
}

// Next returns the next packet. At EOF it returns io.EOF.
func (p *PCAPReader) Next() (PCAPPacket, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(p.r, hdr[:]); err != nil {
		if err == io.EOF {
			return PCAPPacket{}, io.EOF
		}
		return PCAPPacket{}, fmt.Errorf("read packet header: %w", err)
	}
	inclLen := p.order.Uint32(hdr[8:12])
	origLen := p.order.Uint32(hdr[12:16])
	if p.SnapLen > 0 && inclLen > p.SnapLen {
		return PCAPPacket{}, fmt.Errorf("packet length %d exceeds snaplen %d", inclLen, p.SnapLen)
	}
	if inclLen > DefaultSnapLen {
		return PCAPPacket{}, fmt.Errorf("packet length %d exceeds max supported length %d", inclLen, DefaultSnapLen)
	}
	data := make([]byte, inclLen)
	if _, err := io.ReadFull(p.r, data); err != nil {
		return PCAPPacket{}, fmt.Errorf("read packet data: %w", err)
	}
	return PCAPPacket{
		TimestampSec:  p.order.Uint32(hdr[:4]),
		TimestampFrac: p.order.Uint32(hdr[4:8]),
		CapturedLen:   inclLen,
		OriginalLen:   origLen,
		Data:          data,
	}, nil
}

func pcapByteOrder(magic []byte) (binary.ByteOrder, bool, error) {
	switch string(magic) {
	case "\xd4\xc3\xb2\xa1":
		return binary.LittleEndian, false, nil
	case "\xa1\xb2\xc3\xd4":
		return binary.BigEndian, false, nil
	case "\x4d\x3c\xb2\xa1":
		return binary.LittleEndian, true, nil
	case "\xa1\xb2\x3c\x4d":
		return binary.BigEndian, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported pcap magic 0x%x", magic)
	}
}
