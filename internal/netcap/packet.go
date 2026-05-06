package netcap

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

const (
	etherTypeIPv4 = 0x0800
	ipProtoTCP    = 6
	ipProtoUDP    = 17
)

// FlowPacket is a decoded IPv4 TCP/UDP packet and its transport payload.
type FlowPacket struct {
	NetworkProto   string
	TransportProto string
	SrcAddr        netip.Addr
	DstAddr        netip.Addr
	SrcPort        uint16
	DstPort        uint16
	Payload        []byte
}

// DecodeFlowPacket decodes a packet from a supported pcap link type.
func DecodeFlowPacket(linkType uint32, data []byte) (FlowPacket, error) {
	switch linkType {
	case LinkTypeEthernet:
		if len(data) < 14 {
			return FlowPacket{}, fmt.Errorf("ethernet packet too short")
		}
		if binary.BigEndian.Uint16(data[12:14]) != etherTypeIPv4 {
			return FlowPacket{}, fmt.Errorf("unsupported ethernet ethertype")
		}
		return decodeIPv4Flow(data[14:])
	case LinkTypeRaw:
		return decodeIPv4Flow(data)
	default:
		return FlowPacket{}, fmt.Errorf("unsupported link type %d", linkType)
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
