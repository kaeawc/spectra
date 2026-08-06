package netcap

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

const (
	etherTypeIPv4  = 0x0800
	etherTypeIPv6  = 0x86dd
	loopFamilyIPv4 = 2
	ipProtoTCP     = 6
	ipProtoUDP     = 17

	ipv6HeaderLen     = 40
	ipProtoIPv6Frag   = 44
	maxIPv6ExtHeaders = 8

	ipv6ExtHopByHop = 0
	ipv6ExtRouting  = 43
	ipv6ExtDestOpts = 60
)

// loopFamiliesIPv6 are the AF_INET6 values seen in DLT_NULL/DLT_LOOP link
// headers across platforms (macOS 30, FreeBSD 28, Linux 10).
var loopFamiliesIPv6 = [...]uint32{30, 28, 10}

// FlowPacket is a decoded IPv4/IPv6 TCP/UDP packet and its transport payload.
type FlowPacket struct {
	NetworkProto   string
	TransportProto string
	SrcAddr        netip.Addr
	DstAddr        netip.Addr
	SrcPort        uint16
	DstPort        uint16
	TCPSeq         uint32
	TCPAck         uint32
	TCPFlags       uint8
	Payload        []byte
}

// DecodeFlowPacket decodes a packet from a supported pcap link type.
func DecodeFlowPacket(linkType uint32, data []byte) (FlowPacket, error) {
	switch linkType {
	case LinkTypeEthernet:
		if len(data) < 14 {
			return FlowPacket{}, fmt.Errorf("ethernet packet too short")
		}
		switch binary.BigEndian.Uint16(data[12:14]) {
		case etherTypeIPv4:
			return decodeIPv4Flow(data[14:])
		case etherTypeIPv6:
			return decodeIPv6Flow(data[14:])
		default:
			return FlowPacket{}, fmt.Errorf("unsupported ethernet ethertype")
		}
	case LinkTypeRaw:
		return decodeRawIPFlow(data)
	case LinkTypeNull, LinkTypeLoop:
		return decodeLoopbackFlow(data)
	case LinkTypeLinuxSLL:
		return decodeLinuxSLLFlow(data)
	default:
		return FlowPacket{}, fmt.Errorf("unsupported link type %d", linkType)
	}
}

func decodeLinuxSLLFlow(data []byte) (FlowPacket, error) {
	if len(data) < 16 {
		return FlowPacket{}, fmt.Errorf("linux sll packet too short")
	}
	switch binary.BigEndian.Uint16(data[14:16]) {
	case etherTypeIPv4:
		return decodeIPv4Flow(data[16:])
	case etherTypeIPv6:
		return decodeIPv6Flow(data[16:])
	default:
		return FlowPacket{}, fmt.Errorf("unsupported linux sll protocol")
	}
}

func decodeLoopbackFlow(data []byte) (FlowPacket, error) {
	if len(data) < 4 {
		return FlowPacket{}, fmt.Errorf("loopback packet too short")
	}
	familyBE := binary.BigEndian.Uint32(data[:4])
	familyLE := binary.LittleEndian.Uint32(data[:4])
	if familyBE == loopFamilyIPv4 || familyLE == loopFamilyIPv4 {
		return decodeIPv4Flow(data[4:])
	}
	if isIPv6LoopFamily(familyBE) || isIPv6LoopFamily(familyLE) {
		return decodeIPv6Flow(data[4:])
	}
	return FlowPacket{}, fmt.Errorf("unsupported loopback family")
}

func isIPv6LoopFamily(family uint32) bool {
	for _, f := range loopFamiliesIPv6 {
		if family == f {
			return true
		}
	}
	return false
}

// decodeRawIPFlow dispatches a raw IP packet (no link header) by IP version.
func decodeRawIPFlow(data []byte) (FlowPacket, error) {
	if len(data) < 1 {
		return FlowPacket{}, fmt.Errorf("empty ip packet")
	}
	switch data[0] >> 4 {
	case 4:
		return decodeIPv4Flow(data)
	case 6:
		return decodeIPv6Flow(data)
	default:
		return FlowPacket{}, fmt.Errorf("unsupported ip version %d", data[0]>>4)
	}
}

func decodeIPv4Flow(data []byte) (FlowPacket, error) {
	if len(data) < 20 {
		return FlowPacket{}, fmt.Errorf("ipv4 packet too short")
	}
	version := data[0] >> 4
	if version != 4 {
		return FlowPacket{}, fmt.Errorf("unsupported ip version %d", version)
	}
	ihl := int(data[0]&0x0f) * 4
	if ihl < 20 || len(data) < ihl {
		return FlowPacket{}, fmt.Errorf("invalid ipv4 header length")
	}
	totalLen := int(binary.BigEndian.Uint16(data[2:4]))
	if totalLen < ihl || totalLen > len(data) {
		return FlowPacket{}, fmt.Errorf("invalid ipv4 total length")
	}
	frag := binary.BigEndian.Uint16(data[6:8])
	if frag&0x3fff != 0 {
		return FlowPacket{}, fmt.Errorf("fragmented ipv4 packet unsupported")
	}
	src := netip.AddrFrom4([4]byte{data[12], data[13], data[14], data[15]})
	dst := netip.AddrFrom4([4]byte{data[16], data[17], data[18], data[19]})
	body := data[ihl:totalLen]
	out := FlowPacket{NetworkProto: "ipv4", SrcAddr: src, DstAddr: dst}
	switch data[9] {
	case ipProtoTCP:
		return decodeTCPFlow(out, body)
	case ipProtoUDP:
		return decodeUDPFlow(out, body)
	default:
		return FlowPacket{}, fmt.Errorf("unsupported ip protocol %d", data[9])
	}
}

func decodeIPv6Flow(data []byte) (FlowPacket, error) {
	if len(data) < ipv6HeaderLen {
		return FlowPacket{}, fmt.Errorf("ipv6 packet too short")
	}
	if version := data[0] >> 4; version != 6 {
		return FlowPacket{}, fmt.Errorf("unsupported ip version %d", version)
	}
	// PayloadLength covers extension headers + transport. Bound the transport
	// region by it, but tolerate a zero value (jumbogram) or truncated capture
	// by falling back to the captured length.
	end := ipv6HeaderLen + int(binary.BigEndian.Uint16(data[4:6]))
	if end <= ipv6HeaderLen || end > len(data) {
		end = len(data)
	}
	var src, dst [16]byte
	copy(src[:], data[8:24])
	copy(dst[:], data[24:40])
	out := FlowPacket{
		NetworkProto: "ipv6",
		SrcAddr:      netip.AddrFrom16(src),
		DstAddr:      netip.AddrFrom16(dst),
	}

	nextHeader := data[6]
	offset := ipv6HeaderLen
	for hops := 0; hops < maxIPv6ExtHeaders; hops++ {
		switch nextHeader {
		case ipProtoTCP:
			return decodeTCPFlow(out, data[offset:end])
		case ipProtoUDP:
			return decodeUDPFlow(out, data[offset:end])
		case ipv6ExtHopByHop, ipv6ExtRouting, ipv6ExtDestOpts:
			// Option-style extension header: [next(1)][hdr_ext_len(1)][...],
			// length = (hdr_ext_len + 1) * 8 bytes.
			if offset+2 > end {
				return FlowPacket{}, fmt.Errorf("truncated ipv6 extension header")
			}
			next := data[offset]
			extLen := (int(data[offset+1]) + 1) * 8
			offset += extLen
			nextHeader = next
			if offset > end {
				return FlowPacket{}, fmt.Errorf("invalid ipv6 extension header length")
			}
		case ipProtoIPv6Frag:
			return FlowPacket{}, fmt.Errorf("fragmented ipv6 packet unsupported")
		default:
			return FlowPacket{}, fmt.Errorf("unsupported ipv6 next header %d", nextHeader)
		}
	}
	return FlowPacket{}, fmt.Errorf("too many ipv6 extension headers")
}

func decodeTCPFlow(out FlowPacket, data []byte) (FlowPacket, error) {
	if len(data) < 20 {
		return FlowPacket{}, fmt.Errorf("tcp segment too short")
	}
	headerLen := int(data[12]>>4) * 4
	if headerLen < 20 || len(data) < headerLen {
		return FlowPacket{}, fmt.Errorf("invalid tcp header length")
	}
	out.TransportProto = "tcp"
	out.SrcPort = binary.BigEndian.Uint16(data[:2])
	out.DstPort = binary.BigEndian.Uint16(data[2:4])
	out.TCPSeq = binary.BigEndian.Uint32(data[4:8])
	out.TCPAck = binary.BigEndian.Uint32(data[8:12])
	out.TCPFlags = data[13]
	out.Payload = data[headerLen:]
	return out, nil
}

func decodeUDPFlow(out FlowPacket, data []byte) (FlowPacket, error) {
	if len(data) < 8 {
		return FlowPacket{}, fmt.Errorf("udp datagram too short")
	}
	udpLen := int(binary.BigEndian.Uint16(data[4:6]))
	if udpLen < 8 || udpLen > len(data) {
		return FlowPacket{}, fmt.Errorf("invalid udp length")
	}
	out.TransportProto = "udp"
	out.SrcPort = binary.BigEndian.Uint16(data[:2])
	out.DstPort = binary.BigEndian.Uint16(data[2:4])
	out.Payload = data[8:udpLen]
	return out, nil
}
