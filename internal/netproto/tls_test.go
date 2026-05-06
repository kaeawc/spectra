package netproto

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestParseTLSClientHelloMetadata(t *testing.T) {
	record := makeClientHello(t,
		extension(0, serverName("api.example.com")),
		extension(16, alpn("h2", "http/1.1")),
		extension(43, []byte{4, 0x03, 0x04, 0x03, 0x03}),
		extension(0xfe0d, []byte{0, 1, 2, 3}),
	)

	hello, err := ParseTLSClientHello(record)
	if err != nil {
		t.Fatalf("ParseTLSClientHello: %v", err)
	}
	if hello.SNI != "api.example.com" {
		t.Fatalf("SNI = %q, want api.example.com", hello.SNI)
	}
	if hello.LegacyVersion != "TLS 1.2" {
		t.Fatalf("LegacyVersion = %q, want TLS 1.2", hello.LegacyVersion)
	}
	if !hello.ECHPresent {
		t.Fatal("ECHPresent = false, want true")
	}
	wantALPN := []string{"h2", "http/1.1"}
	if !sameStrings(hello.ALPN, wantALPN) {
		t.Fatalf("ALPN = %v, want %v", hello.ALPN, wantALPN)
	}
	wantVersions := []string{"TLS 1.3", "TLS 1.2"}
	if !sameStrings(hello.SupportedVersions, wantVersions) {
		t.Fatalf("SupportedVersions = %v, want %v", hello.SupportedVersions, wantVersions)
	}
}

func TestParseTLSClientHelloNoExtensions(t *testing.T) {
	hello, err := ParseTLSClientHello(makeClientHello(t))
	if err != nil {
		t.Fatalf("ParseTLSClientHello: %v", err)
	}
	if hello.SNI != "" || len(hello.ALPN) != 0 || len(hello.SupportedVersions) != 0 || hello.ECHPresent {
		t.Fatalf("unexpected extension metadata: %+v", hello)
	}
}

func TestParseTLSClientHelloRejectsInvalidRecord(t *testing.T) {
	if _, err := ParseTLSClientHello([]byte{23, 3, 3, 0, 0}); err == nil {
		t.Fatal("expected error for non-handshake record")
	}
	truncated := makeClientHello(t, extension(0, serverName("example.com")))
	truncated = truncated[:len(truncated)-2]
	if _, err := ParseTLSClientHello(truncated); err == nil {
		t.Fatal("expected error for truncated record")
	}
}

func makeClientHello(t *testing.T, exts ...[]byte) []byte {
	t.Helper()
	var hello bytes.Buffer
	hello.Write([]byte{0x03, 0x03})          // legacy_version
	hello.Write(bytes.Repeat([]byte{1}, 32)) // random
	hello.WriteByte(0)                       // session_id length
	writeUint16(&hello, 2)                   // cipher_suites length
	hello.Write([]byte{0x13, 0x01})          // TLS_AES_128_GCM_SHA256
	hello.WriteByte(1)                       // compression_methods length
	hello.WriteByte(0)                       // null compression
	if len(exts) > 0 {
		var all bytes.Buffer
		for _, ext := range exts {
			all.Write(ext)
		}
		writeUint16(&hello, all.Len())
		hello.Write(all.Bytes())
	}

	var handshake bytes.Buffer
	handshake.WriteByte(tlsHandshakeClient)
	writeUint24(&handshake, hello.Len())
	handshake.Write(hello.Bytes())

	var record bytes.Buffer
	record.Write([]byte{tlsRecordHandshake, 0x03, 0x03})
	writeUint16(&record, handshake.Len())
	record.Write(handshake.Bytes())
	return record.Bytes()
}

func extension(extType uint16, data []byte) []byte {
	var b bytes.Buffer
	writeUint16(&b, int(extType))
	writeUint16(&b, len(data))
	b.Write(data)
	return b.Bytes()
}

func serverName(host string) []byte {
	var names bytes.Buffer
	names.WriteByte(0)
	writeUint16(&names, len(host))
	names.WriteString(host)

	var data bytes.Buffer
	writeUint16(&data, names.Len())
	data.Write(names.Bytes())
	return data.Bytes()
}

func alpn(protocols ...string) []byte {
	var list bytes.Buffer
	for _, proto := range protocols {
		list.WriteByte(byte(len(proto)))
		list.WriteString(proto)
	}
	var data bytes.Buffer
	writeUint16(&data, list.Len())
	data.Write(list.Bytes())
	return data.Bytes()
}

func writeUint16(b *bytes.Buffer, n int) {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], uint16(n))
	b.Write(tmp[:])
}

func writeUint24(b *bytes.Buffer, n int) {
	b.WriteByte(byte(n >> 16))
	b.WriteByte(byte(n >> 8))
	b.WriteByte(byte(n))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
