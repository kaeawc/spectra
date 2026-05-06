package netcap

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestDecodeEthernetIPv4TCP(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\n\r\n")
	frame := ethernetFrame(ipv4Packet(6, tcpSegment(53124, 80, payload)))

	pkt, err := DecodeFlowPacket(LinkTypeEthernet, frame)
	if err != nil {
		t.Fatalf("DecodeFlowPacket: %v", err)
	}
	if pkt.NetworkProto != "ipv4" || pkt.TransportProto != "tcp" {
		t.Fatalf("protocols = %+v", pkt)
	}
	if pkt.SrcAddr.String() != "192.0.2.10" || pkt.DstAddr.String() != "198.51.100.20" {
		t.Fatalf("addresses = %s -> %s", pkt.SrcAddr, pkt.DstAddr)
	}
	if pkt.SrcPort != 53124 || pkt.DstPort != 80 || !bytes.Equal(pkt.Payload, payload) {
		t.Fatalf("transport = %+v", pkt)
	}
}

func TestDecodeRawIPv4UDP(t *testing.T) {
	payload := []byte{1, 2, 3}
	pkt, err := DecodeFlowPacket(LinkTypeRaw, ipv4Packet(17, udpDatagram(53000, 53, payload)))
	if err != nil {
		t.Fatalf("DecodeFlowPacket: %v", err)
	}
	if pkt.TransportProto != "udp" || pkt.SrcPort != 53000 || pkt.DstPort != 53 || !bytes.Equal(pkt.Payload, payload) {
		t.Fatalf("udp packet = %+v", pkt)
	}
}

func TestDecodeFlowPacketRejectsUnsupportedPackets(t *testing.T) {
	tests := []struct {
		name string
		link uint32
		data []byte
	}{
		{name: "link", link: LinkTypeLinuxSLL, data: []byte{1, 2, 3}},
		{name: "short ethernet", link: LinkTypeEthernet, data: []byte{1, 2, 3}},
		{name: "non ipv4 ethernet", link: LinkTypeEthernet, data: append(make([]byte, 12), 0x86, 0xdd)},
		{name: "short ipv4", link: LinkTypeRaw, data: []byte{0x45, 0}},
		{name: "unsupported ip proto", link: LinkTypeRaw, data: ipv4Packet(1, []byte{1, 2, 3, 4})},
		{name: "fragmented ipv4", link: LinkTypeRaw, data: fragmentedIPv4Packet()},
		{name: "short tcp", link: LinkTypeRaw, data: ipv4Packet(6, []byte{1, 2, 3})},
		{name: "short udp", link: LinkTypeRaw, data: ipv4Packet(17, []byte{1, 2, 3})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeFlowPacket(tt.link, tt.data); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func fragmentedIPv4Packet() []byte {
	packet := ipv4Packet(17, udpDatagram(1, 2, nil))
	binary.BigEndian.PutUint16(packet[6:8], 0x2000)
	return packet
}

func ethernetFrame(ip []byte) []byte {
	frame := make([]byte, 14+len(ip))
	frame[12] = 0x08
	frame[13] = 0x00
	copy(frame[14:], ip)
	return frame
}

func ipv4Packet(proto byte, body []byte) []byte {
	packet := make([]byte, 20+len(body))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = proto
	copy(packet[12:16], []byte{192, 0, 2, 10})
	copy(packet[16:20], []byte{198, 51, 100, 20})
	copy(packet[20:], body)
	return packet
}

func tcpSegment(src, dst uint16, payload []byte) []byte {
	seg := make([]byte, 20+len(payload))
	binary.BigEndian.PutUint16(seg[:2], src)
	binary.BigEndian.PutUint16(seg[2:4], dst)
	seg[12] = 0x50
	copy(seg[20:], payload)
	return seg
}

func udpDatagram(src, dst uint16, payload []byte) []byte {
	dg := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(dg[:2], src)
	binary.BigEndian.PutUint16(dg[2:4], dst)
	binary.BigEndian.PutUint16(dg[4:6], uint16(len(dg)))
	copy(dg[8:], payload)
	return dg
}
