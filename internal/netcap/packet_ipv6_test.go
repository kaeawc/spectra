package netcap

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// ipv6Packet builds a 40-byte IPv6 header (optionally followed by extension
// headers already prepended to body) carrying `next` as the next-header value.
func ipv6Packet(next byte, body []byte) []byte {
	packet := make([]byte, ipv6HeaderLen+len(body))
	packet[0] = 0x60 // version 6
	binary.BigEndian.PutUint16(packet[4:6], uint16(len(body)))
	packet[6] = next
	packet[7] = 64 // hop limit
	copy(packet[8:24], netip.MustParseAddr("2001:db8::1").AsSlice())
	copy(packet[24:40], netip.MustParseAddr("2001:db8::2").AsSlice())
	copy(packet[40:], body)
	return packet
}

func ethernetFrameV6(ip []byte) []byte {
	frame := make([]byte, 14+len(ip))
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv6)
	copy(frame[14:], ip)
	return frame
}

func TestDecodeEthernetIPv6TCP(t *testing.T) {
	frame := ethernetFrameV6(ipv6Packet(ipProtoTCP, tcpSegment(51000, 443, []byte("hi"))))
	flow, err := DecodeFlowPacket(LinkTypeEthernet, frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flow.NetworkProto != "ipv6" || flow.TransportProto != "tcp" {
		t.Fatalf("protos = %s/%s", flow.NetworkProto, flow.TransportProto)
	}
	if flow.SrcAddr != netip.MustParseAddr("2001:db8::1") || flow.DstAddr != netip.MustParseAddr("2001:db8::2") {
		t.Fatalf("addrs = %s -> %s", flow.SrcAddr, flow.DstAddr)
	}
	if flow.SrcPort != 51000 || flow.DstPort != 443 {
		t.Fatalf("ports = %d -> %d", flow.SrcPort, flow.DstPort)
	}
	if string(flow.Payload) != "hi" {
		t.Fatalf("payload = %q", flow.Payload)
	}
}

func TestDecodeLoopbackIPv6UDP(t *testing.T) {
	// macOS DLT_NULL loopback uses AF_INET6 = 30.
	ip := ipv6Packet(ipProtoUDP, udpDatagram(5353, 53, []byte("q")))
	packet := make([]byte, 4+len(ip))
	binary.LittleEndian.PutUint32(packet[:4], 30)
	copy(packet[4:], ip)

	flow, err := DecodeFlowPacket(LinkTypeNull, packet)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flow.NetworkProto != "ipv6" || flow.TransportProto != "udp" || flow.DstPort != 53 {
		t.Fatalf("flow = %+v", flow)
	}
}

func TestDecodeIPv6WithHopByHopExtension(t *testing.T) {
	// One 8-byte Hop-by-Hop extension header (next=TCP) before the TCP segment.
	ext := make([]byte, 8)
	ext[0] = ipProtoTCP // next header
	ext[1] = 0          // hdr ext len => (0+1)*8 = 8 bytes
	body := append(ext, tcpSegment(4000, 8080, []byte("x"))...)
	frame := ethernetFrameV6(ipv6Packet(ipv6ExtHopByHop, body))

	flow, err := DecodeFlowPacket(LinkTypeEthernet, frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flow.TransportProto != "tcp" || flow.DstPort != 8080 {
		t.Fatalf("flow = %+v", flow)
	}
}

func TestDecodeIPv6FragmentUnsupported(t *testing.T) {
	frame := ethernetFrameV6(ipv6Packet(ipProtoIPv6Frag, make([]byte, 8)))
	if _, err := DecodeFlowPacket(LinkTypeEthernet, frame); err == nil {
		t.Fatal("expected fragmented ipv6 to be unsupported")
	}
}
