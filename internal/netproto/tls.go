// Package netproto contains small protocol parsers used by network
// inspection. Parsers in this package summarize metadata only; they do not
// decrypt or retain payload bodies.
package netproto

import (
	"encoding/binary"
	"fmt"
)

const (
	tlsRecordHandshake = 22
	tlsHandshakeClient = 1

	tlsExtServerName     = 0
	tlsExtALPN           = 16
	tlsExtSupportedVers  = 43
	tlsExtEncryptedHello = 0xfe0d
)

// TLSClientHello is the passive metadata Spectra can extract from a TLS
// ClientHello. SNI may be empty when the client omitted it or when ECH hides it.
type TLSClientHello struct {
	SNI               string   `json:"sni,omitempty"`
	ALPN              []string `json:"alpn,omitempty"`
	LegacyVersion     string   `json:"legacy_version,omitempty"`
	SupportedVersions []string `json:"supported_versions,omitempty"`
	ECHPresent        bool     `json:"ech_present,omitempty"`
}

// ParseTLSClientHello parses one TLS record containing a ClientHello.
func ParseTLSClientHello(record []byte) (TLSClientHello, error) {
	if len(record) < 5 {
		return TLSClientHello{}, fmt.Errorf("tls record too short")
	}
	if record[0] != tlsRecordHandshake {
		return TLSClientHello{}, fmt.Errorf("tls record is not a handshake")
	}
	recordLen := int(binary.BigEndian.Uint16(record[3:5]))
	if recordLen <= 0 || len(record[5:]) < recordLen {
		return TLSClientHello{}, fmt.Errorf("tls record length exceeds buffer")
	}
	body := record[5 : 5+recordLen]
	if len(body) < 4 || body[0] != tlsHandshakeClient {
		return TLSClientHello{}, fmt.Errorf("tls handshake is not a client hello")
	}
	helloLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if helloLen <= 0 || len(body[4:]) < helloLen {
		return TLSClientHello{}, fmt.Errorf("client hello length exceeds record")
	}
	return parseClientHello(body[4 : 4+helloLen])
}

func parseClientHello(hello []byte) (TLSClientHello, error) {
	if len(hello) < 34 {
		return TLSClientHello{}, fmt.Errorf("client hello too short")
	}
	out := TLSClientHello{LegacyVersion: tlsVersion(hello[0], hello[1])}
	pos := 34 // legacy_version + random

	sessionLen, ok := readUint8(hello, &pos)
	if !ok || !skip(hello, &pos, int(sessionLen)) {
		return TLSClientHello{}, fmt.Errorf("client hello session id exceeds buffer")
	}
	cipherLen, ok := readUint16(hello, &pos)
	if !ok || cipherLen%2 != 0 || !skip(hello, &pos, int(cipherLen)) {
		return TLSClientHello{}, fmt.Errorf("client hello cipher suites exceed buffer")
	}
	compressionLen, ok := readUint8(hello, &pos)
	if !ok || !skip(hello, &pos, int(compressionLen)) {
		return TLSClientHello{}, fmt.Errorf("client hello compression methods exceed buffer")
	}
	if pos == len(hello) {
		return out, nil
	}
	extensionsLen, ok := readUint16(hello, &pos)
	if !ok || len(hello[pos:]) < int(extensionsLen) {
		return TLSClientHello{}, fmt.Errorf("client hello extensions exceed buffer")
	}
	parseTLSExtensions(hello[pos:pos+int(extensionsLen)], &out)
	return out, nil
}

func parseTLSExtensions(exts []byte, hello *TLSClientHello) {
	for pos := 0; pos+4 <= len(exts); {
		extType := binary.BigEndian.Uint16(exts[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(exts[pos+2 : pos+4]))
		pos += 4
		if len(exts[pos:]) < extLen {
			return
		}
		data := exts[pos : pos+extLen]
		pos += extLen

		switch extType {
		case tlsExtServerName:
			if sni := parseServerName(data); sni != "" {
				hello.SNI = sni
			}
		case tlsExtALPN:
			hello.ALPN = parseALPN(data)
		case tlsExtSupportedVers:
			hello.SupportedVersions = parseSupportedVersions(data)
		case tlsExtEncryptedHello:
			hello.ECHPresent = true
		}
	}
}

func parseServerName(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	if len(data[2:]) < listLen {
		return ""
	}
	for pos := 2; pos+3 <= 2+listLen; {
		nameType := data[pos]
		nameLen := int(binary.BigEndian.Uint16(data[pos+1 : pos+3]))
		pos += 3
		if len(data[pos:]) < nameLen {
			return ""
		}
		if nameType == 0 {
			return string(data[pos : pos+nameLen])
		}
		pos += nameLen
	}
	return ""
}

func parseALPN(data []byte) []string {
	if len(data) < 2 {
		return nil
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	if len(data[2:]) < listLen {
		return nil
	}
	var protocols []string
	for pos := 2; pos < 2+listLen; {
		nameLen := int(data[pos])
		pos++
		if nameLen == 0 || len(data[pos:]) < nameLen {
			return protocols
		}
		protocols = append(protocols, string(data[pos:pos+nameLen]))
		pos += nameLen
	}
	return protocols
}

func parseSupportedVersions(data []byte) []string {
	if len(data) < 1 {
		return nil
	}
	listLen := int(data[0])
	if listLen%2 != 0 || len(data[1:]) < listLen {
		return nil
	}
	versions := make([]string, 0, listLen/2)
	for pos := 1; pos < 1+listLen; pos += 2 {
		versions = append(versions, tlsVersion(data[pos], data[pos+1]))
	}
	return versions
}

func readUint8(data []byte, pos *int) (uint8, bool) {
	if *pos >= len(data) {
		return 0, false
	}
	v := data[*pos]
	*pos += 1
	return v, true
}

func readUint16(data []byte, pos *int) (uint16, bool) {
	if len(data[*pos:]) < 2 {
		return 0, false
	}
	v := binary.BigEndian.Uint16(data[*pos : *pos+2])
	*pos += 2
	return v, true
}

func skip(data []byte, pos *int, n int) bool {
	if n < 0 || len(data[*pos:]) < n {
		return false
	}
	*pos += n
	return true
}

func tlsVersion(major, minor byte) string {
	switch {
	case major == 3 && minor == 0:
		return "SSL 3.0"
	case major == 3 && minor >= 1 && minor <= 4:
		return fmt.Sprintf("TLS 1.%d", minor-1)
	default:
		return fmt.Sprintf("0x%02x%02x", major, minor)
	}
}
