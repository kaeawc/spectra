package jvm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	agentPortProperty   = "spectra.agent.port"
	agentTokenProperty  = "spectra.agent.token"
	agentSocketProperty = "spectra.agent.socket"
)

type AgentStatus struct {
	PID       int    `json:"pid"`
	Attached  bool   `json:"attached"`
	Transport string `json:"transport,omitempty"`
	Port      int    `json:"port,omitempty"`
	Socket    string `json:"socket,omitempty"`
	Token     string `json:"-"`
}

type MBeansResult struct {
	MBeans []MBean `json:"mbeans"`
}

type MBean struct {
	Name       string           `json:"name"`
	Class      string           `json:"class,omitempty"`
	Attributes []MBeanAttribute `json:"attributes,omitempty"`
	Operations []MBeanOperation `json:"operations,omitempty"`
}

type MBeanAttribute struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
}

type MBeanOperation struct {
	Name       string           `json:"name"`
	ReturnType string           `json:"return_type,omitempty"`
	Impact     int              `json:"impact,omitempty"`
	Parameters []MBeanParameter `json:"parameters,omitempty"`
}

type MBeanParameter struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

type AgentProbes struct {
	Runtime struct {
		AvailableProcessors int   `json:"available_processors"`
		FreeMemory          int64 `json:"free_memory"`
		TotalMemory         int64 `json:"total_memory"`
		MaxMemory           int64 `json:"max_memory"`
	} `json:"runtime"`
	Threads struct {
		Live int `json:"live"`
	} `json:"threads"`
	Counters  []AgentCounter  `json:"counters,omitempty"`
	Workflows []WorkflowProbe `json:"workflows,omitempty"`
}

type AgentCounter struct {
	Name      string `json:"name"`
	MBean     string `json:"mbean"`
	Attribute string `json:"attribute"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value,omitempty"`
	Error     string `json:"error,omitempty"`
}

type WorkflowProbe struct {
	Name     string         `json:"name"`
	Counters []AgentCounter `json:"counters,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type MBeanAttributeValue struct {
	MBean     string `json:"mbean"`
	Attribute string `json:"attribute"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value,omitempty"`
	Error     string `json:"error,omitempty"`
}

type MBeanInvocation struct {
	MBean     string `json:"mbean"`
	Operation string `json:"operation"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value,omitempty"`
	Error     string `json:"error,omitempty"`
}

type AgentStatusProvider interface {
	Status(pid int) AgentStatus
}

type AgentTransport interface {
	GetJSON(status AgentStatus, path string, dest any) error
	PostJSON(status AgentStatus, path string, body any, dest any) error
}

type AgentClient struct {
	StatusProvider AgentStatusProvider
	Transport      AgentTransport
}

type CmdStatusProvider struct {
	Run CmdRunner
}

func (p CmdStatusProvider) Status(pid int) AgentStatus {
	return AgentStatusForPID(pid, p.Run)
}

type staticStatusProvider struct {
	status AgentStatus
}

func (p staticStatusProvider) Status(pid int) AgentStatus {
	p.status.PID = pid
	return p.status
}

func NewAgentClient(run CmdRunner) AgentClient {
	return AgentClient{
		StatusProvider: CmdStatusProvider{Run: run},
	}
}

func NewAgentClientForStatus(status AgentStatus) AgentClient {
	return AgentClient{
		StatusProvider: staticStatusProvider{status: status},
		Transport:      transportForStatus(status),
	}
}

func (c AgentClient) MBeans(pid int) (MBeansResult, error) {
	var result MBeansResult
	if err := c.get(pid, "/mbeans", &result); err != nil {
		return result, err
	}
	return result, nil
}

func (c AgentClient) Probes(pid int) (AgentProbes, error) {
	var result AgentProbes
	if err := c.get(pid, "/probes", &result); err != nil {
		return result, err
	}
	return result, nil
}

func (c AgentClient) ReadMBeanAttribute(pid int, mbean, attribute string) (MBeanAttributeValue, error) {
	var result MBeanAttributeValue
	path := "/mbean-attribute?name=" + urlQueryEscape(mbean) + "&attribute=" + urlQueryEscape(attribute)
	if err := c.get(pid, path, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (c AgentClient) InvokeMBeanOperation(pid int, mbean, operation string) (MBeanInvocation, error) {
	var result MBeanInvocation
	body := map[string]string{"name": mbean, "operation": operation}
	if err := c.post(pid, "/mbean-operation", body, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (c AgentClient) get(pid int, path string, dest any) error {
	status, transport := c.deps(pid)
	return transport.GetJSON(status, path, dest)
}

func (c AgentClient) post(pid int, path string, body any, dest any) error {
	status, transport := c.deps(pid)
	return transport.PostJSON(status, path, body, dest)
}

func (c AgentClient) deps(pid int) (AgentStatus, AgentTransport) {
	provider := c.StatusProvider
	if provider == nil {
		provider = CmdStatusProvider{}
	}
	status := provider.Status(pid)
	transport := c.Transport
	if transport == nil {
		transport = transportForStatus(status)
	}
	return status, transport
}

func transportForStatus(status AgentStatus) AgentTransport {
	if status.Transport == "unix" || status.Socket != "" {
		return UnixAgentTransport{Client: unixHTTPClient(status.Socket)}
	}
	return HTTPAgentTransport{Client: &http.Client{Timeout: 3 * time.Second}}
}

type HTTPAgentTransport struct {
	Client *http.Client
}

func (t HTTPAgentTransport) GetJSON(status AgentStatus, path string, dest any) error {
	return t.doJSON(http.MethodGet, status, path, nil, dest)
}

func (t HTTPAgentTransport) PostJSON(status AgentStatus, path string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode agent request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	return t.doJSON(http.MethodPost, status, path, reader, dest)
}

func (t HTTPAgentTransport) doJSON(method string, status AgentStatus, path string, body io.Reader, dest any) error {
	if !status.Attached {
		return fmt.Errorf("spectra agent is not attached to PID %d; run `spectra jvm attach %d` first", status.PID, status.PID)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", status.Port, path), body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Spectra-Agent-Token", status.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	return doAgentRequest(client, req, dest)
}

type UnixAgentTransport struct {
	Client *http.Client
}

func (t UnixAgentTransport) GetJSON(status AgentStatus, path string, dest any) error {
	return t.doJSON(http.MethodGet, status, path, nil, dest)
}

func (t UnixAgentTransport) PostJSON(status AgentStatus, path string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode agent request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	return t.doJSON(http.MethodPost, status, path, reader, dest)
}

func (t UnixAgentTransport) doJSON(method string, status AgentStatus, path string, body io.Reader, dest any) error {
	if !status.Attached {
		return fmt.Errorf("spectra agent is not attached to PID %d; run `spectra jvm attach %d` first", status.PID, status.PID)
	}
	if status.Socket == "" {
		return fmt.Errorf("spectra agent did not publish a Unix socket for PID %d", status.PID)
	}
	req, err := http.NewRequest(method, "http://spectra-agent"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Spectra-Agent-Token", status.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := t.Client
	if client == nil {
		client = unixHTTPClient(status.Socket)
	}
	return doAgentRequest(client, req, dest)
}

func unixHTTPClient(socket string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
}

func doAgentRequest(client *http.Client, req *http.Request, dest any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("agent request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read agent response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decode agent response: %w", err)
	}
	return nil
}

type AttachOptions struct {
	JarPath   string
	Transport string
	Socket    string
	Counters  []string
	Workflows []string
}

func AttachAgent(pid int, jarPath string, run CmdRunner) (AgentStatus, error) {
	return AttachAgentWithOptions(pid, AttachOptions{JarPath: jarPath}, run)
}

func AttachAgentWithOptions(pid int, opts AttachOptions, run CmdRunner) (AgentStatus, error) {
	if run == nil {
		run = DefaultRunner
	}
	jarPath := opts.JarPath
	if jarPath == "" {
		var err error
		jarPath, err = FindAgentJar()
		if err != nil {
			return AgentStatus{PID: pid}, err
		}
	}
	if _, err := os.Stat(jarPath); err != nil {
		return AgentStatus{PID: pid}, fmt.Errorf("agent jar %s: %w", jarPath, err)
	}
	cmdArgs := []string{"--add-modules", "jdk.attach", "-cp", jarPath, "com.spectra.agent.AttachMain", strconv.Itoa(pid), jarPath}
	if agentArgs := attachAgentArgs(opts); agentArgs != "" {
		cmdArgs = append(cmdArgs, agentArgs)
	}
	_, err := run("java", cmdArgs...)
	if err != nil {
		return AgentStatus{PID: pid}, fmt.Errorf("attach agent: %w", err)
	}
	status := AgentStatusFromSysProps(pid, collectAgentSysProps(pid, run))
	if !status.Attached {
		return status, fmt.Errorf("agent loaded but did not publish %s/%s", agentPortProperty, agentTokenProperty)
	}
	return status, nil
}

func attachAgentArgs(opts AttachOptions) string {
	var parts []string
	if opts.Transport != "" {
		parts = append(parts, "transport="+opts.Transport)
	}
	if opts.Socket != "" {
		parts = append(parts, "socket="+opts.Socket)
	}
	if len(opts.Counters) > 0 {
		parts = append(parts, "counters="+strings.Join(opts.Counters, "|"))
	}
	if len(opts.Workflows) > 0 {
		parts = append(parts, "workflows="+strings.Join(opts.Workflows, "|"))
	}
	return strings.Join(parts, ";")
}

func AgentStatusForPID(pid int, run CmdRunner) AgentStatus {
	if run == nil {
		run = DefaultRunner
	}
	return AgentStatusFromSysProps(pid, collectAgentSysProps(pid, run))
}

func AgentStatusFromSysProps(pid int, props map[string]string) AgentStatus {
	status := AgentStatus{PID: pid}
	port, err := strconv.Atoi(strings.TrimSpace(props[agentPortProperty]))
	if err == nil && port > 0 && props[agentTokenProperty] != "" {
		status.Attached = true
		status.Transport = "http"
		status.Port = port
		status.Token = props[agentTokenProperty]
	}
	if props[agentSocketProperty] != "" && props[agentTokenProperty] != "" {
		status.Attached = true
		status.Transport = "unix"
		status.Socket = props[agentSocketProperty]
		status.Token = props[agentTokenProperty]
	}
	return status
}

func FetchMBeans(pid int, run CmdRunner) (MBeansResult, error) {
	return NewAgentClient(run).MBeans(pid)
}

func FetchAgentProbes(pid int, run CmdRunner) (AgentProbes, error) {
	return NewAgentClient(run).Probes(pid)
}

func ReadMBeanAttribute(pid int, mbean, attribute string, run CmdRunner) (MBeanAttributeValue, error) {
	return NewAgentClient(run).ReadMBeanAttribute(pid, mbean, attribute)
}

func InvokeMBeanOperation(pid int, mbean, operation string, run CmdRunner) (MBeanInvocation, error) {
	return NewAgentClient(run).InvokeMBeanOperation(pid, mbean, operation)
}

func FindAgentJar() (string, error) {
	if env := os.Getenv("SPECTRA_AGENT_JAR"); env != "" {
		return env, nil
	}
	exe, err := os.Executable()
	if err == nil {
		for _, p := range []string{
			filepath.Join(filepath.Dir(exe), "spectra-agent.jar"),
			filepath.Join(filepath.Dir(exe), "agent", "spectra-agent.jar"),
		} {
			if _, statErr := os.Stat(p); statErr == nil {
				return p, nil
			}
		}
	}
	if _, err := os.Stat("spectra-agent.jar"); err == nil {
		return "spectra-agent.jar", nil
	}
	if _, err := os.Stat(filepath.Join("agent", "spectra-agent.jar")); err == nil {
		return filepath.Join("agent", "spectra-agent.jar"), nil
	}
	return "", fmt.Errorf("spectra-agent.jar not found; run `make agent` or set SPECTRA_AGENT_JAR")
}

func collectAgentSysProps(pid int, run CmdRunner) map[string]string {
	out, err := run("jcmd", strconv.Itoa(pid), "VM.system_properties")
	if err != nil {
		return nil
	}
	props := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == agentPortProperty || k == agentTokenProperty || k == agentSocketProperty {
			props[k] = strings.TrimSpace(v)
		}
	}
	return props
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		" ", "%20",
		"&", "%26",
		"=", "%3D",
		"?", "%3F",
		"#", "%23",
		"+", "%2B",
		",", "%2C",
		":", "%3A",
		"\"", "%22",
	)
	return r.Replace(s)
}
