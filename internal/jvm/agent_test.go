package jvm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

type fakeStatusProvider struct {
	status AgentStatus
}

func (f fakeStatusProvider) Status(pid int) AgentStatus {
	f.status.PID = pid
	return f.status
}

type fakeAgentTransport struct {
	gets  []string
	posts []fakePost
	err   error
}

type fakePost struct {
	path string
	body any
}

func (f *fakeAgentTransport) GetJSON(_ AgentStatus, path string, dest any) error {
	f.gets = append(f.gets, path)
	if f.err != nil {
		return f.err
	}
	switch path {
	case "/mbeans":
		*(dest.(*MBeansResult)) = MBeansResult{MBeans: []MBean{{Name: "java.lang:type=Memory"}}}
	case "/probes":
		probes := dest.(*AgentProbes)
		probes.Threads.Live = 7
	default:
		*(dest.(*MBeanAttributeValue)) = MBeanAttributeValue{
			MBean:     "java.lang:type=Memory",
			Attribute: "Verbose",
			Type:      "java.lang.Boolean",
			Value:     true,
		}
	}
	return nil
}

func (f *fakeAgentTransport) PostJSON(_ AgentStatus, path string, body any, dest any) error {
	f.posts = append(f.posts, fakePost{path: path, body: body})
	if f.err != nil {
		return f.err
	}
	*(dest.(*MBeanInvocation)) = MBeanInvocation{
		MBean:     "java.lang:type=Memory",
		Operation: "gc",
		Type:      "null",
	}
	return nil
}

func TestAgentClientUsesInjectedTransportForMBeansAndProbes(t *testing.T) {
	transport := &fakeAgentTransport{}
	client := AgentClient{
		StatusProvider: fakeStatusProvider{status: AgentStatus{Attached: true, Port: 49152, Token: "secret"}},
		Transport:      transport,
	}
	mbeans, err := client.MBeans(42)
	if err != nil {
		t.Fatalf("MBeans: %v", err)
	}
	if len(mbeans.MBeans) != 1 || mbeans.MBeans[0].Name != "java.lang:type=Memory" {
		t.Fatalf("mbeans = %#v", mbeans)
	}
	probes, err := client.Probes(42)
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	if probes.Threads.Live != 7 {
		t.Fatalf("live threads = %d, want 7", probes.Threads.Live)
	}
	if !reflect.DeepEqual(transport.gets, []string{"/mbeans", "/probes"}) {
		t.Fatalf("GET paths = %#v", transport.gets)
	}
}

func TestAgentClientReadsMBeanAttributeWithEscapedQuery(t *testing.T) {
	transport := &fakeAgentTransport{}
	client := AgentClient{
		StatusProvider: fakeStatusProvider{status: AgentStatus{Attached: true, Port: 49152, Token: "secret"}},
		Transport:      transport,
	}
	got, err := client.ReadMBeanAttribute(42, "java.lang:type=Memory", "Heap Memory Usage")
	if err != nil {
		t.Fatalf("ReadMBeanAttribute: %v", err)
	}
	if got.Attribute != "Verbose" {
		t.Fatalf("attribute result = %#v", got)
	}
	wantPath := "/mbean-attribute?name=java.lang%3Atype%3DMemory&attribute=Heap%20Memory%20Usage"
	if len(transport.gets) != 1 || transport.gets[0] != wantPath {
		t.Fatalf("GET path = %#v, want %q", transport.gets, wantPath)
	}
}

func TestAgentClientInvokesMBeanOperationWithBody(t *testing.T) {
	transport := &fakeAgentTransport{}
	client := AgentClient{
		StatusProvider: fakeStatusProvider{status: AgentStatus{Attached: true, Port: 49152, Token: "secret"}},
		Transport:      transport,
	}
	got, err := client.InvokeMBeanOperation(42, "java.lang:type=Memory", "gc")
	if err != nil {
		t.Fatalf("InvokeMBeanOperation: %v", err)
	}
	if got.Operation != "gc" {
		t.Fatalf("invocation = %#v", got)
	}
	if len(transport.posts) != 1 || transport.posts[0].path != "/mbean-operation" {
		t.Fatalf("POSTs = %#v", transport.posts)
	}
	raw, err := json.Marshal(transport.posts[0].body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"name":"java.lang:type=Memory","operation":"gc"}` {
		t.Fatalf("POST body = %s", raw)
	}
}

func TestHTTPAgentTransportRejectsDetachedStatus(t *testing.T) {
	err := HTTPAgentTransport{}.GetJSON(AgentStatus{PID: 42}, "/mbeans", &MBeansResult{})
	if err == nil {
		t.Fatal("expected detached agent error")
	}
	if got := err.Error(); got != "spectra agent is not attached to PID 42; run `spectra jvm attach 42` first" {
		t.Fatalf("error = %q", got)
	}
}

func TestAgentClientPropagatesTransportErrors(t *testing.T) {
	client := AgentClient{
		StatusProvider: fakeStatusProvider{status: AgentStatus{Attached: true, Port: 49152, Token: "secret"}},
		Transport:      &fakeAgentTransport{err: fmt.Errorf("boom")},
	}
	if _, err := client.MBeans(42); err == nil {
		t.Fatal("expected error")
	}
}
