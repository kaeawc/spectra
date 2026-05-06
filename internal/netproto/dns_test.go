package netproto

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestParseDNSQuery(t *testing.T) {
	packet := makeDNSQuery(t, 0x1234, "api.example.com", 1)

	msg, err := ParseDNSMessage(packet)
	if err != nil {
		t.Fatalf("ParseDNSMessage: %v", err)
	}
	if msg.ID != 0x1234 || msg.Response || msg.OpCode != 0 || msg.RCode != 0 {
		t.Fatalf("header = %+v", msg)
	}
	if len(msg.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(msg.Questions))
	}
	q := msg.Questions[0]
	if q.Name != "api.example.com" || q.Type != "A" || q.Class != "IN" {
		t.Fatalf("question = %+v", q)
	}
}

func TestParseDNSResponseWithCompressedQuestion(t *testing.T) {
	packet := makeDNSResponseWithCompressedQuestion(t)

	msg, err := ParseDNSMessage(packet)
	if err != nil {
		t.Fatalf("ParseDNSMessage: %v", err)
	}
	if !msg.Response || msg.RCode != 3 || !msg.Truncated || msg.Answers != 2 {
		t.Fatalf("response header = %+v", msg)
	}
	if len(msg.Questions) != 2 {
		t.Fatalf("questions = %+v", msg.Questions)
	}
	if msg.Questions[0].Name != "example.com" || msg.Questions[1].Name != "www.example.com" {
		t.Fatalf("question names = %+v", msg.Questions)
	}
	if msg.Questions[1].Type != "HTTPS" {
		t.Fatalf("question type = %q, want HTTPS", msg.Questions[1].Type)
	}
}

func TestParseDNSRejectsMalformedPackets(t *testing.T) {
	if _, err := ParseDNSMessage([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected short packet error")
	}

	truncated := makeDNSQuery(t, 1, "example.com", 1)
	truncated = truncated[:len(truncated)-2]
	if _, err := ParseDNSMessage(truncated); err == nil {
		t.Fatal("expected truncated question error")
	}

	loop := makeDNSQuery(t, 1, "example.com", 1)
	loop[12] = 0xc0
	loop[13] = 12
	if _, err := ParseDNSMessage(loop); err == nil {
		t.Fatal("expected compression loop error")
	}
}

func makeDNSQuery(t *testing.T, id uint16, name string, qType uint16) []byte {
	t.Helper()
	var b bytes.Buffer
	write16(&b, id)
	write16(&b, 0x0100) // recursion desired
	write16(&b, 1)      // qdcount
	write16(&b, 0)      // ancount
	write16(&b, 0)      // nscount
	write16(&b, 0)      // arcount
	writeDNSName(t, &b, name)
	write16(&b, qType)
	write16(&b, 1)
	return b.Bytes()
}

func makeDNSResponseWithCompressedQuestion(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	write16(&b, 0x9999)
	write16(&b, 0x8383) // response, truncated, rcode=3
	write16(&b, 2)
	write16(&b, 2)
	write16(&b, 0)
	write16(&b, 0)
	writeDNSName(t, &b, "example.com")
	write16(&b, 1)
	write16(&b, 1)
	b.WriteByte(3)
	b.WriteString("www")
	b.Write([]byte{0xc0, 12}) // pointer to example.com above
	write16(&b, 65)
	write16(&b, 1)
	return b.Bytes()
}

func writeDNSName(t *testing.T, b *bytes.Buffer, name string) {
	t.Helper()
	start := 0
	for i := 0; i <= len(name); i++ {
		if i != len(name) && name[i] != '.' {
			continue
		}
		label := name[start:i]
		if len(label) > 63 {
			t.Fatalf("label too long: %q", label)
		}
		b.WriteByte(byte(len(label)))
		b.WriteString(label)
		start = i + 1
	}
	b.WriteByte(0)
}

func write16(b *bytes.Buffer, n uint16) {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], n)
	b.Write(tmp[:])
}
