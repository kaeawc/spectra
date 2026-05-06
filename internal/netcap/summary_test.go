package netcap

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSummarizePCAPExtractsProtocolMetadata(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.LittleEndian, false, 2048, LinkTypeEthernet)
	writePCAPPacket(t, &b, binary.LittleEndian, 1, 0,
		ethernetFrame(ipv4Packet(17, udpDatagram(53123, 53, dnsQuery("example.com")))), 0)
	writePCAPPacket(t, &b, binary.LittleEndian, 2, 0,
		ethernetFrame(ipv4Packet(6, tcpSegment(53124, 443, tlsClientHello("example.com")))), 0)
	writePCAPPacket(t, &b, binary.LittleEndian, 3, 0,
		ethernetFrame(ipv4Packet(6, tcpSegment(53125, 80, []byte(
			"GET /chat HTTP/1.1\r\nHost: example.com\r\nAuthorization: secret\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\nbody")))), 0)

	summary, err := SummarizePCAP(&b, 10)
	if err != nil {
		t.Fatalf("SummarizePCAP: %v", err)
	}
	if summary.Packets != 3 || summary.DecodedFlows != 3 || summary.DecodeErrors != 0 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if len(summary.DNS) != 1 {
		t.Fatalf("dns summaries = %+v", summary.DNS)
	}
	if q := summary.DNS[0].Message.Questions[0]; q.Name != "example.com" || q.Type != "A" || q.Class != "IN" {
		t.Fatalf("dns question = %+v", q)
	}
	if len(summary.TLS) != 1 || summary.TLS[0].ClientHello.SNI != "example.com" {
		t.Fatalf("tls summaries = %+v", summary.TLS)
	}
	if len(summary.HTTP) != 1 {
		t.Fatalf("http summaries = %+v", summary.HTTP)
	}
	http := summary.HTTP[0].Message
	if !http.IsRequest || http.Method != "GET" || http.Target != "/chat" || !http.WebSocket {
		t.Fatalf("http message = %+v", http)
	}
	for _, h := range http.Headers {
		if h.Name == "Authorization" && (!h.Redacted || h.Value != "[redacted]") {
			t.Fatalf("authorization header was not redacted: %+v", h)
		}
	}
}

func TestSummarizePCAPBoundsEventsAndCountsDecodeErrors(t *testing.T) {
	var b bytes.Buffer
	writePCAPHeader(t, &b, binary.LittleEndian, false, 2048, LinkTypeEthernet)
	writePCAPPacket(t, &b, binary.LittleEndian, 1, 0,
		ethernetFrame(ipv4Packet(17, udpDatagram(53123, 53, dnsQuery("one.example")))), 0)
	writePCAPPacket(t, &b, binary.LittleEndian, 2, 0,
		ethernetFrame(ipv4Packet(17, udpDatagram(53124, 53, dnsQuery("two.example")))), 0)
	writePCAPPacket(t, &b, binary.LittleEndian, 3, 0, []byte{1, 2, 3}, 0)

	summary, err := SummarizePCAP(&b, 1)
	if err != nil {
		t.Fatalf("SummarizePCAP: %v", err)
	}
	if summary.Packets != 3 || summary.DecodedFlows != 2 || summary.DecodeErrors != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if len(summary.DNS) != 1 || summary.EventsDropped != 1 {
		t.Fatalf("bounded events = %+v", summary)
	}
}

func dnsQuery(name string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	for _, label := range bytes.Split([]byte(name), []byte(".")) {
		b.WriteByte(byte(len(label)))
		b.Write(label)
	}
	b.WriteByte(0)
	b.Write([]byte{0, 1, 0, 1})
	return b.Bytes()
}

func tlsClientHello(sni string) []byte {
	serverName := append([]byte{0, byte(len(sni) >> 8), byte(len(sni))}, []byte(sni)...)
	serverNameList := append([]byte{0, byte(len(serverName))}, serverName...)
	serverNameExt := append([]byte{0, 0, 0, byte(len(serverNameList))}, serverNameList...)

	alpnList := []byte{2, 'h', '2', 8, 'h', 't', 't', 'p', '/', '1', '.', '1'}
	alpnData := append([]byte{0, byte(len(alpnList))}, alpnList...)
	alpnExt := append([]byte{0, 16, 0, byte(len(alpnData))}, alpnData...)

	extensions := append(serverNameExt, alpnExt...)
	hello := make([]byte, 0, 128)
	hello = append(hello, 0x03, 0x03)
	hello = append(hello, bytes.Repeat([]byte{1}, 32)...)
	hello = append(hello, 0)
	hello = append(hello, 0, 2, 0x13, 0x01)
	hello = append(hello, 1, 0)
	hello = append(hello, byte(len(extensions)>>8), byte(len(extensions)))
	hello = append(hello, extensions...)

	handshake := []byte{1, byte(len(hello) >> 16), byte(len(hello) >> 8), byte(len(hello))}
	handshake = append(handshake, hello...)
	record := []byte{22, 0x03, 0x01, byte(len(handshake) >> 8), byte(len(handshake))}
	record = append(record, handshake...)
	return record
}
