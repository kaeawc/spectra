package netproto

import (
	"encoding/binary"
	"fmt"
	"strings"
)

const dnsHeaderLen = 12

// DNSMessage is a compact summary of one DNS datagram.
type DNSMessage struct {
	ID        uint16        `json:"id"`
	Response  bool          `json:"response"`
	OpCode    int           `json:"op_code,omitempty"`
	RCode     int           `json:"rcode,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	Questions []DNSQuestion `json:"questions,omitempty"`
	Answers   int           `json:"answers,omitempty"`
}

// DNSQuestion identifies one DNS question.
type DNSQuestion struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Class string `json:"class"`
}

// ParseDNSMessage parses DNS message metadata from a UDP or TCP DNS payload.
// For TCP DNS, pass the DNS message without the two-byte length prefix.
func ParseDNSMessage(packet []byte) (DNSMessage, error) {
	if len(packet) < dnsHeaderLen {
		return DNSMessage{}, fmt.Errorf("dns message too short")
	}
	flags := binary.BigEndian.Uint16(packet[2:4])
	qdCount := int(binary.BigEndian.Uint16(packet[4:6]))
	anCount := int(binary.BigEndian.Uint16(packet[6:8]))
	msg := DNSMessage{
		ID:        binary.BigEndian.Uint16(packet[:2]),
		Response:  flags&0x8000 != 0,
		OpCode:    int((flags >> 11) & 0x0f),
		RCode:     int(flags & 0x0f),
		Truncated: flags&0x0200 != 0,
		Answers:   anCount,
	}

	pos := dnsHeaderLen
	for i := 0; i < qdCount; i++ {
		name, next, err := parseDNSName(packet, pos, 0)
		if err != nil {
			return DNSMessage{}, err
		}
		pos = next
		if len(packet[pos:]) < 4 {
			return DNSMessage{}, fmt.Errorf("dns question exceeds buffer")
		}
		qType := binary.BigEndian.Uint16(packet[pos : pos+2])
		qClass := binary.BigEndian.Uint16(packet[pos+2 : pos+4])
		pos += 4
		msg.Questions = append(msg.Questions, DNSQuestion{
			Name:  name,
			Type:  dnsType(qType),
			Class: dnsClass(qClass),
		})
	}
	return msg, nil
}

func parseDNSName(packet []byte, pos, depth int) (string, int, error) {
	if depth > 8 {
		return "", 0, fmt.Errorf("dns name compression loop")
	}
	var labels []string
	next := pos
	for {
		if next >= len(packet) {
			return "", 0, fmt.Errorf("dns name exceeds buffer")
		}
		l := packet[next]
		next++
		switch {
		case l == 0:
			if len(labels) == 0 {
				return ".", next, nil
			}
			return strings.Join(labels, "."), next, nil
		case l&0xc0 == 0xc0:
			if next >= len(packet) {
				return "", 0, fmt.Errorf("dns compression pointer exceeds buffer")
			}
			ptr := int(l&0x3f)<<8 | int(packet[next])
			next++
			name, _, err := parseDNSName(packet, ptr, depth+1)
			if err != nil {
				return "", 0, err
			}
			labels = append(labels, name)
			return strings.Join(labels, "."), next, nil
		case l&0xc0 != 0:
			return "", 0, fmt.Errorf("unsupported dns label type")
		default:
			labelLen := int(l)
			if labelLen == 0 || len(packet[next:]) < labelLen {
				return "", 0, fmt.Errorf("dns label exceeds buffer")
			}
			labels = append(labels, string(packet[next:next+labelLen]))
			next += labelLen
		}
	}
}

func dnsType(t uint16) string {
	switch t {
	case 1:
		return "A"
	case 2:
		return "NS"
	case 5:
		return "CNAME"
	case 15:
		return "MX"
	case 16:
		return "TXT"
	case 28:
		return "AAAA"
	case 33:
		return "SRV"
	case 65:
		return "HTTPS"
	default:
		return fmt.Sprintf("TYPE%d", t)
	}
}

func dnsClass(c uint16) string {
	switch c {
	case 1:
		return "IN"
	default:
		return fmt.Sprintf("CLASS%d", c)
	}
}
