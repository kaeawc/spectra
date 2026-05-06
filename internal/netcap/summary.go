package netcap

import (
	"bytes"
	"io"
	"net/netip"
	"strings"

	"github.com/kaeawc/spectra/internal/netproto"
)

const DefaultSummaryEventLimit = 100

const maxTCPReassemblyBytes = 256 * 1024
const tcpFlagSYN = 0x02

// PCAPSummary is a bounded metadata-only summary of a classic pcap stream.
type PCAPSummary struct {
	LinkType      uint32            `json:"link_type"`
	Packets       int               `json:"packets"`
	DecodedFlows  int               `json:"decoded_flows"`
	DecodeErrors  int               `json:"decode_errors,omitempty"`
	DNS           []DNSFlowSummary  `json:"dns,omitempty"`
	TLS           []TLSFlowSummary  `json:"tls,omitempty"`
	HTTP          []HTTPFlowSummary `json:"http,omitempty"`
	EventsDropped int               `json:"events_dropped,omitempty"`
}

// FlowSummary identifies the flow that carried a parsed protocol event.
type FlowSummary struct {
	NetworkProto   string `json:"network_proto"`
	TransportProto string `json:"transport_proto"`
	SrcAddr        string `json:"src_addr"`
	SrcPort        uint16 `json:"src_port"`
	DstAddr        string `json:"dst_addr"`
	DstPort        uint16 `json:"dst_port"`
}

// DNSFlowSummary is a parsed DNS message with its carrying flow.
type DNSFlowSummary struct {
	Flow    FlowSummary         `json:"flow"`
	Message netproto.DNSMessage `json:"message"`
}

// TLSFlowSummary is a parsed TLS ClientHello with its carrying flow.
type TLSFlowSummary struct {
	Flow        FlowSummary             `json:"flow"`
	ClientHello netproto.TLSClientHello `json:"client_hello"`
}

// HTTPFlowSummary is a parsed HTTP/1.x header block with its carrying flow.
type HTTPFlowSummary struct {
	Flow    FlowSummary          `json:"flow"`
	Message netproto.HTTPMessage `json:"message"`
}

// SummarizePCAP extracts bounded DNS, TLS ClientHello, and HTTP/1.x metadata
// from a classic pcap stream. Payload bodies are never retained.
func SummarizePCAP(r io.Reader, eventLimit int) (PCAPSummary, error) {
	reader, err := NewPCAPReader(r)
	if err != nil {
		return PCAPSummary{}, err
	}
	if eventLimit <= 0 {
		eventLimit = DefaultSummaryEventLimit
	}
	summary := PCAPSummary{LinkType: reader.LinkType}
	tcpStreams := newTCPSummaryStreams()
	for {
		pkt, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				return summary, nil
			}
			return PCAPSummary{}, err
		}
		summary.Packets++
		flow, err := DecodeFlowPacket(reader.LinkType, pkt.Data)
		if err != nil {
			summary.DecodeErrors++
			continue
		}
		summary.DecodedFlows++
		summarizeFlow(&summary, flow, eventLimit, tcpStreams)
	}
}

func summarizeFlow(summary *PCAPSummary, flow FlowPacket, eventLimit int, tcpStreams *tcpSummaryStreams) {
	if len(flow.Payload) == 0 {
		return
	}
	if isDNSFlow(flow) {
		msg, err := netproto.ParseDNSMessage(flow.Payload)
		if err == nil {
			appendDNSSummary(summary, flow, msg, eventLimit)
		}
		return
	}
	if flow.TransportProto != "tcp" {
		return
	}
	stream, ok := tcpStreams.add(flow)
	if !ok {
		return
	}
	if !stream.parsedTLS && isTLSClientHelloPayload(stream.data) {
		hello, err := netproto.ParseTLSClientHello(stream.data)
		if err == nil {
			stream.parsedTLS = true
			appendTLSSummary(summary, flow, hello, eventLimit)
		}
		return
	}
	if !stream.parsedHTTP && isHTTP1Payload(stream.data) && hasHTTPHeaderTerminator(stream.data) {
		msg, err := netproto.ParseHTTP1Headers(stream.data)
		if err == nil {
			stream.parsedHTTP = true
			appendHTTPSummary(summary, flow, msg, eventLimit)
		}
	}
}

func isDNSFlow(flow FlowPacket) bool {
	return flow.TransportProto == "udp" && (flow.SrcPort == 53 || flow.DstPort == 53)
}

func isTLSClientHelloPayload(payload []byte) bool {
	return len(payload) >= 6 && payload[0] == 22 && payload[5] == 1
}

func isHTTP1Payload(payload []byte) bool {
	if bytes.HasPrefix(payload, []byte("HTTP/")) {
		return true
	}
	methodEnd := bytes.IndexByte(payload, ' ')
	if methodEnd <= 0 {
		return false
	}
	switch strings.ToUpper(string(payload[:methodEnd])) {
	case "CONNECT", "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "TRACE":
		return true
	default:
		return false
	}
}

func hasHTTPHeaderTerminator(payload []byte) bool {
	return bytes.Contains(payload, []byte("\r\n\r\n")) || bytes.Contains(payload, []byte("\n\n"))
}

type tcpFlowKey struct {
	srcAddr netip.Addr
	dstAddr netip.Addr
	srcPort uint16
	dstPort uint16
}

type tcpSummaryStreams struct {
	streams map[tcpFlowKey]*tcpSummaryStream
}

type tcpSummaryStream struct {
	data       []byte
	nextSeq    uint32
	parsedTLS  bool
	parsedHTTP bool
}

func newTCPSummaryStreams() *tcpSummaryStreams {
	return &tcpSummaryStreams{streams: make(map[tcpFlowKey]*tcpSummaryStream)}
}

func (s *tcpSummaryStreams) add(flow FlowPacket) (*tcpSummaryStream, bool) {
	key := tcpFlowKey{
		srcAddr: flow.SrcAddr,
		dstAddr: flow.DstAddr,
		srcPort: flow.SrcPort,
		dstPort: flow.DstPort,
	}
	dataSeq := flow.TCPSeq
	if flow.TCPFlags&tcpFlagSYN != 0 {
		dataSeq++
	}
	stream := s.streams[key]
	if stream == nil {
		stream = &tcpSummaryStream{nextSeq: dataSeq}
		s.streams[key] = stream
	}
	payload := flow.Payload
	if dataSeq < stream.nextSeq {
		overlap := int(stream.nextSeq - dataSeq)
		if overlap >= len(payload) {
			return stream, true
		}
		payload = payload[overlap:]
	} else if dataSeq > stream.nextSeq {
		return stream, false
	}
	if len(stream.data) >= maxTCPReassemblyBytes {
		return stream, true
	}
	remaining := maxTCPReassemblyBytes - len(stream.data)
	if len(payload) > remaining {
		payload = payload[:remaining]
	}
	stream.data = append(stream.data, payload...)
	stream.nextSeq += uint32(len(payload))
	return stream, true
}

func appendDNSSummary(summary *PCAPSummary, flow FlowPacket, msg netproto.DNSMessage, limit int) {
	if !summaryHasRoom(summary, limit) {
		summary.EventsDropped++
		return
	}
	summary.DNS = append(summary.DNS, DNSFlowSummary{Flow: newFlowSummary(flow), Message: msg})
}

func appendTLSSummary(summary *PCAPSummary, flow FlowPacket, hello netproto.TLSClientHello, limit int) {
	if !summaryHasRoom(summary, limit) {
		summary.EventsDropped++
		return
	}
	summary.TLS = append(summary.TLS, TLSFlowSummary{Flow: newFlowSummary(flow), ClientHello: hello})
}

func appendHTTPSummary(summary *PCAPSummary, flow FlowPacket, msg netproto.HTTPMessage, limit int) {
	if !summaryHasRoom(summary, limit) {
		summary.EventsDropped++
		return
	}
	summary.HTTP = append(summary.HTTP, HTTPFlowSummary{Flow: newFlowSummary(flow), Message: msg})
}

func summaryHasRoom(summary *PCAPSummary, limit int) bool {
	return len(summary.DNS)+len(summary.TLS)+len(summary.HTTP) < limit
}

func newFlowSummary(flow FlowPacket) FlowSummary {
	return FlowSummary{
		NetworkProto:   flow.NetworkProto,
		TransportProto: flow.TransportProto,
		SrcAddr:        flow.SrcAddr.String(),
		SrcPort:        flow.SrcPort,
		DstAddr:        flow.DstAddr.String(),
		DstPort:        flow.DstPort,
	}
}
