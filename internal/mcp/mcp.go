// Package mcp implements bounded MCP (Model Context Protocol) clients for
// stdio and Streamable HTTP servers. Server output and metadata are always
// treated as untrusted input.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// ProtocolVersion is the newest MCP revision implemented by this client.
	ProtocolVersion = "2025-06-18"

	maxRequestBytes     = 256 << 10
	maxResponseBytes    = 1 << 20
	maxToolOutputBytes  = 64 << 10
	maxTools            = 128
	maxToolPages        = 32
	maxToolNameBytes    = 128
	maxDescriptionBytes = 4 << 10
	maxCursorBytes      = 4 << 10
	defaultConnectTime  = 8 * time.Second
	defaultDiscoverTime = 8 * time.Second
	defaultCallTime     = 60 * time.Second
)

// Version is set by the command at startup and defaults to an honest local
// build marker for library users.
var Version = "dev"

var supportedProtocolVersions = map[string]bool{
	ProtocolVersion: true,
	"2025-03-26":    true,
	"2024-11-05":    true,
}

// Server describes one configured MCP server (from maestrorc `mcp add`).
type Server struct {
	Name    string
	Type    string // stdio | http | sse (legacy HTTP+SSE compatibility)
	URL     string
	Command string   // stdio only; parsed without a shell
	Headers []string // "K V" pairs
	Token   string   // bearer token (memory only, never persisted by this package)
	WorkDir string   // stdio process working directory
}

// Snapshot is a race-safe, secret-free view of a client.
type Snapshot struct {
	Name            string
	Type            string
	Status          string // disconnected | connecting | connected | error
	Error           string
	ToolCount       int
	ProtocolVersion string
}

// Tool is one validated MCP tool exposed by a server. Annotations are
// intentionally ignored: the protocol specifies that they are untrusted
// hints and they must never weaken Maestro's permission policy.
type Tool struct {
	Name         string
	Title        string
	Description  string
	InputSchema  map[string]any
	OutputSchema map[string]any

	inputValidator  *compiledSchema
	outputValidator *compiledSchema
}

// CallToolResult is the bounded, validated result of tools/call.
type CallToolResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent map[string]any   `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

// ModelOutput returns a JSON representation suitable for a tool-result
// message. The transport limit is larger than this model-facing limit so a
// remote server cannot flood the next provider turn.
func (r CallToolResult) ModelOutput() (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", errors.New("encode MCP tool result")
	}
	if len(data) > maxToolOutputBytes {
		return "", fmt.Errorf("MCP tool output exceeds %d bytes", maxToolOutputBytes)
	}
	return string(data), nil
}

// ToolExecutionError is an MCP isError=true result. It is distinct from a
// protocol/transport failure so the native loop can surface actionable tool
// feedback to the model as a failed tool call.
type ToolExecutionError struct{ Message string }

func (e *ToolExecutionError) Error() string { return e.Message }

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type wireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Method string `json:"method,omitempty"`
}

// Client is one MCP server connection.
type Client struct {
	Server Server

	// Status and Err are kept for source compatibility. Concurrent code must
	// use Snapshot; all internal mutations are synchronized.
	Status string
	Err    string

	mu              sync.Mutex
	stdioMu         sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	scanner         *bufio.Scanner
	processDone     chan struct{}
	nextID          int64
	protocolVersion string
	sessionID       string
	toolsCapable    bool
	toolsDirty      bool
	toolsRevision   uint64
	tools           map[string]Tool
	httpClient      *http.Client
	disabledErr     error
}

// New builds a client (not yet connected).
func New(s Server) *Client {
	return &Client{Server: s, Status: "disconnected", tools: map[string]Tool{}, httpClient: newHTTPClient()}
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		// MCP owns its connection pool. Closing an integration must not retain
		// sockets or disturb provider traffic using the process-global transport.
		transport = base.Clone()
	}
	return &http.Client{
		Transport: transport,
		Timeout:   defaultCallTime,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Redirects can forward bearer/custom headers to a different endpoint.
			return http.ErrUseLastResponse
		},
	}
}

// Snapshot returns a synchronized view without command, URL, headers, token,
// session ID, or any other potentially sensitive configuration.
func (c *Client) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{
		Name: sanitizeText(c.Server.Name, 128), Type: sanitizeText(c.Server.Type, 32), Status: c.Status, Error: c.Err,
		ToolCount: len(c.tools), ProtocolVersion: c.protocolVersion,
	}
}

func (c *Client) setStatus(status string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Status = status
	if err == nil {
		c.Err = ""
	} else {
		c.Err = c.redactText(err.Error(), 512)
	}
}

// Fail disconnects a client after an unsafe discovery/transport condition and
// retains only a sanitized diagnostic for status surfaces.
func (c *Client) Fail(err error) {
	_ = c.Close()
	c.setStatus("error", err)
}

// Connect establishes and initializes the configured transport. A missing
// caller deadline receives a short discovery deadline; a dead MCP server can
// never hold the native agent loop indefinitely.
func (c *Client) Connect(ctx context.Context) error {
	ctx, cancel := withDefaultTimeout(ctx, defaultConnectTime)
	defer cancel()
	c.mu.Lock()
	switch c.Status {
	case "connected":
		c.mu.Unlock()
		return nil
	case "connecting":
		c.mu.Unlock()
		return errors.New("MCP connection is already in progress")
	}
	c.Status, c.Err = "connecting", ""
	disabledErr := c.disabledErr
	c.mu.Unlock()
	if disabledErr != nil {
		c.setStatus("error", disabledErr)
		return disabledErr
	}

	var err error
	switch c.Server.Type {
	case "stdio":
		err = c.connectStdio(ctx)
	case "http", "sse":
		err = c.connectHTTP(ctx)
	default:
		err = fmt.Errorf("mcp %s: unsupported transport %q", safeName(c.Server.Name), sanitizeText(c.Server.Type, 32))
	}
	if err != nil {
		// Initialization can allocate an HTTP session or start a stdio process
		// before a later negotiation step fails. Tear it down immediately instead
		// of retaining resources until a future reconnect/application shutdown.
		_ = c.Close()
		c.setStatus("error", err)
		return err
	}
	if err := c.markConnected(); err != nil {
		_ = c.Close()
		c.setStatus("error", err)
		return err
	}
	return nil
}

func (c *Client) markConnected() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Server.Type == "stdio" && c.liveStdioCommandLocked() == nil {
		err := errors.New("MCP stdio process exited during initialization")
		c.Status = "error"
		c.Err = c.redactText(err.Error(), 512)
		return err
	}
	c.Status, c.Err = "connected", ""
	return nil
}

func (c *Client) connectStdio(ctx context.Context) error {
	argv, err := splitCommand(c.Server.Command)
	if err != nil {
		return fmt.Errorf("mcp %s: invalid stdio command", safeName(c.Server.Name))
	}
	workDir, err := canonicalWorkDir(c.Server.WorkDir)
	if err != nil {
		return fmt.Errorf("mcp %s: invalid working directory", safeName(c.Server.Name))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Stderr = io.Discard
	configureProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp %s: prepare stdio transport", safeName(c.Server.Name))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp %s: prepare stdio transport", safeName(c.Server.Name))
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp %s: start stdio server", safeName(c.Server.Name))
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxResponseBytes)
	done := make(chan struct{})
	c.mu.Lock()
	c.cmd, c.stdin, c.scanner, c.processDone = cmd, stdin, scanner, done
	c.sessionID, c.protocolVersion, c.toolsCapable, c.toolsDirty, c.toolsRevision = "", "", false, false, 0
	c.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(done)
		c.mu.Lock()
		if c.cmd == cmd {
			if c.Status != "disconnected" {
				c.Status = "error"
				c.Err = "MCP stdio process exited"
			}
			// Never retain a reaped PID: a later Close must not signal an
			// unrelated process that reused the numeric identifier.
			c.cmd, c.stdin, c.scanner, c.processDone = nil, nil, nil, nil
		}
		c.mu.Unlock()
	}()

	if err := c.initialize(ctx); err != nil {
		c.stopStdio(cmd)
		return err
	}
	if err := c.sendNotification(ctx, "notifications/initialized", nil); err != nil {
		c.stopStdio(cmd)
		return fmt.Errorf("mcp %s: initialized notification failed", safeName(c.Server.Name))
	}
	c.mu.Lock()
	live := c.liveStdioCommandLocked() == cmd
	c.mu.Unlock()
	if !live {
		return fmt.Errorf("mcp %s: stdio process exited during initialization", safeName(c.Server.Name))
	}
	return nil
}

func (c *Client) connectHTTP(ctx context.Context) error {
	if err := validateEndpoint(c.Server.URL); err != nil {
		return fmt.Errorf("mcp %s: invalid HTTP endpoint", safeName(c.Server.Name))
	}
	if err := validateHeaders(c.Server.Headers); err != nil {
		return fmt.Errorf("mcp %s: invalid HTTP headers", safeName(c.Server.Name))
	}
	c.mu.Lock()
	c.sessionID, c.protocolVersion, c.toolsCapable, c.toolsDirty, c.toolsRevision = "", "", false, false, 0
	c.mu.Unlock()
	if err := c.initialize(ctx); err != nil {
		return err
	}
	if err := c.sendNotification(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("mcp %s: initialized notification failed", safeName(c.Server.Name))
	}
	return nil
}

func (c *Client) initialize(ctx context.Context) error {
	raw, err := c.callRaw(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "maestro", "version": Version},
	})
	if err != nil {
		return fmt.Errorf("mcp %s: initialize failed: %w", safeName(c.Server.Name), err)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := decodeStrict(raw, &result); err != nil {
		return fmt.Errorf("mcp %s: invalid initialize result", safeName(c.Server.Name))
	}
	if !supportedProtocolVersions[result.ProtocolVersion] {
		return fmt.Errorf("mcp %s: unsupported negotiated protocol version", safeName(c.Server.Name))
	}
	if (c.Server.Type == "http" || c.Server.Type == "sse") && result.ProtocolVersion == "2024-11-05" {
		return fmt.Errorf("mcp %s: negotiated protocol predates Streamable HTTP", safeName(c.Server.Name))
	}
	c.mu.Lock()
	c.protocolVersion = result.ProtocolVersion
	c.toolsCapable = result.Capabilities.Tools != nil
	c.mu.Unlock()
	return nil
}

// Close tears down the client. HTTP session deletion is best effort and its
// response is deliberately discarded.
func (c *Client) Close() error {
	// Kill before waiting for the serialized call lock. Otherwise Close could
	// wait behind a blocked scanner while the scanner waits for Close to kill
	// its process.
	c.mu.Lock()
	activeCmd := c.liveStdioCommandLocked()
	c.mu.Unlock()
	killProcessTree(activeCmd)
	c.stdioMu.Lock()
	defer c.stdioMu.Unlock()

	c.mu.Lock()
	cmd, stdin, done := c.cmd, c.stdin, c.processDone
	typeName, endpoint := c.Server.Type, c.Server.URL
	sessionID, protocol := c.sessionID, c.protocolVersion
	c.cmd, c.stdin, c.scanner, c.processDone = nil, nil, nil, nil
	c.sessionID, c.protocolVersion, c.toolsCapable, c.toolsDirty, c.toolsRevision = "", "", false, false, 0
	c.tools = map[string]Tool{}
	c.Status, c.Err = "disconnected", ""
	c.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		killProcessTree(cmd)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	if (typeName == "http" || typeName == "sse") && sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
		if err == nil {
			req.Header.Set("Mcp-Session-Id", sessionID)
			if protocol != "" {
				req.Header.Set("MCP-Protocol-Version", protocol)
			}
			c.applyAuthHeaders(req)
			if resp, doErr := c.httpClient.Do(req); doErr == nil {
				_ = resp.Body.Close()
			}
		}
	}
	c.httpClient.CloseIdleConnections()
	return nil
}

func (c *Client) stopStdio(cmd *exec.Cmd) {
	c.mu.Lock()
	if c.cmd == cmd && c.liveStdioCommandLocked() == cmd && cmd.Process != nil {
		killProcessTree(cmd)
	}
	c.mu.Unlock()
}

func (c *Client) liveStdioCommandLocked() *exec.Cmd {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	if c.processDone != nil {
		select {
		case <-c.processDone:
			return nil
		default:
		}
	}
	return c.cmd
}

func (c *Client) sendNotification(ctx context.Context, method string, params any) error {
	if c.Server.Type == "http" || c.Server.Type == "sse" {
		return c.httpNotification(ctx, method, params)
	}
	c.stdioMu.Lock()
	defer c.stdioMu.Unlock()
	data, err := marshalFrame(request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.writeStdio(ctx, data)
}

func (c *Client) callRaw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var (
		raw json.RawMessage
		err error
	)
	if c.Server.Type == "http" || c.Server.Type == "sse" {
		raw, err = c.httpCall(ctx, method, params)
	} else {
		raw, err = c.stdioCall(ctx, method, params)
	}
	if err != nil {
		return nil, errors.New(c.redactText(err.Error(), 512))
	}
	return raw, nil
}

func (c *Client) nextRequest(method string, params any) (request, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	if c.nextID <= 0 {
		return request{}, errors.New("MCP request ID exhausted")
	}
	return request{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params}, nil
}

func (c *Client) stdioCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := withDefaultTimeout(ctx, defaultCallTime)
	defer cancel()
	c.stdioMu.Lock()
	defer c.stdioMu.Unlock()
	req, err := c.nextRequest(method, params)
	if err != nil {
		return nil, err
	}
	data, err := marshalFrame(req)
	if err != nil {
		return nil, err
	}
	if err := c.writeStdio(ctx, data); err != nil {
		return nil, err
	}
	for notifications := 0; notifications < 128; notifications++ {
		line, err := c.scanStdio(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := parseWireResponse(line)
		if err != nil {
			c.failStdio(errors.New("invalid JSON-RPC frame"))
			return nil, errors.New("invalid JSON-RPC frame from MCP server")
		}
		if len(resp.ID) == 0 {
			if resp.Method == "" {
				c.failStdio(errors.New("invalid JSON-RPC message"))
				return nil, errors.New("invalid JSON-RPC message from MCP server")
			}
			if resp.Method == "notifications/tools/list_changed" {
				c.markToolsDirty()
			}
			continue
		}
		if !responseIDMatches(resp.ID, req.ID) {
			c.failStdio(errors.New("unexpected JSON-RPC response ID"))
			return nil, errors.New("unexpected JSON-RPC response ID from MCP server")
		}
		return responseResult(resp)
	}
	c.failStdio(errors.New("notification flood"))
	return nil, errors.New("too many MCP notifications before response")
}

func (c *Client) writeStdio(ctx context.Context, data []byte) error {
	c.mu.Lock()
	stdin, cmd := c.stdin, c.cmd
	c.mu.Unlock()
	if stdin == nil || cmd == nil {
		return errors.New("MCP stdio transport is not connected")
	}
	done := make(chan error, 1)
	go func() {
		_, err := stdin.Write(append(data, '\n'))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			c.failStdio(errors.New("stdio write failed"))
			return errors.New("MCP stdio write failed")
		}
		return nil
	case <-ctx.Done():
		c.failStdio(ctx.Err())
		return ctx.Err()
	}
}

func (c *Client) scanStdio(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	scanner := c.scanner
	c.mu.Unlock()
	if scanner == nil {
		return nil, errors.New("MCP stdio transport is not connected")
	}
	type scanResult struct {
		line []byte
		ok   bool
		err  error
	}
	done := make(chan scanResult, 1)
	go func() {
		ok := scanner.Scan()
		done <- scanResult{line: append([]byte(nil), scanner.Bytes()...), ok: ok, err: scanner.Err()}
	}()
	select {
	case result := <-done:
		if !result.ok {
			c.failStdio(errors.New("stdio stream closed"))
			if result.err != nil {
				return nil, errors.New("MCP stdio response exceeded the frame limit or was unreadable")
			}
			return nil, errors.New("MCP stdio stream closed before response")
		}
		if len(result.line) == 0 || len(result.line) > maxResponseBytes || !utf8.Valid(result.line) {
			c.failStdio(errors.New("invalid stdio frame"))
			return nil, errors.New("invalid MCP stdio response frame")
		}
		return result.line, nil
	case <-ctx.Done():
		c.failStdio(ctx.Err())
		return nil, ctx.Err()
	}
}

func (c *Client) failStdio(err error) {
	c.mu.Lock()
	if cmd := c.liveStdioCommandLocked(); cmd != nil {
		killProcessTree(cmd)
	}
	c.Status = "error"
	c.Err = sanitizeText(err.Error(), 256)
	c.mu.Unlock()
}

func (c *Client) markToolsDirty() {
	c.mu.Lock()
	c.toolsDirty = true
	c.toolsRevision++
	c.mu.Unlock()
}

func (c *Client) httpCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := withDefaultTimeout(ctx, defaultCallTime)
	defer cancel()
	req, err := c.nextRequest(method, params)
	if err != nil {
		return nil, err
	}
	data, err := marshalFrame(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpPost(ctx, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	wire, err := c.readHTTPCallResponse(ctx, resp, req.ID)
	if err != nil {
		return nil, err
	}
	return responseResult(wire)
}

func (c *Client) httpNotification(ctx context.Context, method string, params any) error {
	ctx, cancel := withDefaultTimeout(ctx, defaultDiscoverTime)
	defer cancel()
	data, err := marshalFrame(request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	resp, err := c.httpPost(ctx, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		return errors.New("MCP HTTP notification was not accepted")
	}
	return nil
}

func (c *Client) httpPost(ctx context.Context, data []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server.URL, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("create MCP HTTP request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sessionID, protocol := c.sessionID, c.protocolVersion
	c.mu.Unlock()
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protocol != "" {
		req.Header.Set("MCP-Protocol-Version", protocol)
	}
	c.applyAuthHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("MCP HTTP request failed")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && sessionID != "" {
			c.mu.Lock()
			c.sessionID = ""
			c.Status = "error"
			c.Err = "MCP HTTP session expired; reconnect required"
			c.mu.Unlock()
			return nil, errors.New("MCP HTTP session expired; reconnect required")
		}
		return nil, fmt.Errorf("MCP HTTP endpoint returned status %d", resp.StatusCode)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		if !validSessionID(sid) {
			_ = resp.Body.Close()
			return nil, errors.New("MCP HTTP endpoint returned an invalid session ID")
		}
		c.mu.Lock()
		if c.sessionID == "" {
			c.sessionID = sid
		} else if c.sessionID != sid {
			c.mu.Unlock()
			_ = resp.Body.Close()
			return nil, errors.New("MCP HTTP session ID changed unexpectedly")
		}
		c.mu.Unlock()
	}
	return resp, nil
}

func (c *Client) applyAuthHeaders(req *http.Request) {
	if c.Server.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Server.Token)
	}
	for _, raw := range c.Server.Headers {
		key, value, ok := strings.Cut(raw, " ")
		if ok {
			req.Header.Set(textproto.CanonicalMIMEHeaderKey(key), value)
		}
	}
}

func (c *Client) readHTTPCallResponse(ctx context.Context, resp *http.Response, id int64) (wireResponse, error) {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "application/json"):
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return wireResponse{}, errors.New("read MCP HTTP response")
		}
		if len(body) > maxResponseBytes {
			return wireResponse{}, fmt.Errorf("MCP HTTP response exceeds %d bytes", maxResponseBytes)
		}
		wire, err := parseWireResponse(bytes.TrimSpace(body))
		if err != nil || !responseIDMatches(wire.ID, id) {
			return wireResponse{}, errors.New("invalid MCP JSON-RPC response")
		}
		return wire, nil
	case strings.Contains(contentType, "text/event-stream"):
		return c.readSSEResponse(ctx, resp.Body, id)
	default:
		return wireResponse{}, errors.New("MCP HTTP response has an unsupported content type")
	}
}

func (c *Client) readSSEResponse(ctx context.Context, body io.Reader, id int64) (wireResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxResponseBytes)
	var event strings.Builder
	total, notifications := 0, 0
	consume := func() (wireResponse, bool, error) {
		if event.Len() == 0 {
			return wireResponse{}, false, nil
		}
		payload := strings.TrimSuffix(event.String(), "\n")
		event.Reset()
		wire, err := parseWireResponse([]byte(payload))
		if err != nil {
			return wireResponse{}, false, errors.New("invalid JSON-RPC event in MCP SSE response")
		}
		if len(wire.ID) == 0 {
			notifications++
			if notifications > 128 {
				return wireResponse{}, false, errors.New("too many MCP SSE notifications before response")
			}
			if wire.Method == "notifications/tools/list_changed" {
				c.markToolsDirty()
			}
			return wireResponse{}, false, nil
		}
		if !responseIDMatches(wire.ID, id) {
			return wireResponse{}, false, errors.New("unexpected JSON-RPC response ID in MCP SSE response")
		}
		return wire, true, nil
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return wireResponse{}, err
		}
		line := scanner.Text()
		total += len(line) + 1
		if total > maxResponseBytes {
			return wireResponse{}, fmt.Errorf("MCP SSE response exceeds %d bytes", maxResponseBytes)
		}
		if line == "" {
			if wire, done, err := consume(); err != nil || done {
				return wire, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			part = strings.TrimPrefix(part, " ")
			event.WriteString(part)
			event.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return wireResponse{}, ctx.Err()
		}
		return wireResponse{}, errors.New("read MCP SSE response")
	}
	if wire, done, err := consume(); err != nil || done {
		return wire, err
	}
	return wireResponse{}, errors.New("MCP SSE stream closed before the expected response")
}

// ListTools returns all validated pages from tools/list and atomically
// publishes the catalog used to validate later tools/call requests.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := withDefaultTimeout(ctx, defaultDiscoverTime)
	defer cancel()
	c.mu.Lock()
	capable := c.toolsCapable
	startRevision := c.toolsRevision
	c.mu.Unlock()
	if !capable {
		return nil, errors.New("MCP server did not declare the tools capability")
	}

	seenNames := map[string]bool{}
	seenCursors := map[string]bool{}
	var out []Tool
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.callRaw(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := decodeStrict(raw, &parsed); err != nil || parsed.Tools == nil {
			return nil, errors.New("MCP tools/list returned an invalid result")
		}
		for _, rawTool := range parsed.Tools {
			tool, err := decodeTool(rawTool)
			if err != nil {
				return nil, err
			}
			if seenNames[tool.Name] {
				return nil, fmt.Errorf("MCP tools/list returned duplicate tool %q", safeName(tool.Name))
			}
			seenNames[tool.Name] = true
			out = append(out, tool)
			if len(out) > maxTools {
				return nil, fmt.Errorf("MCP server exposes more than %d tools", maxTools)
			}
		}
		if parsed.NextCursor == "" {
			break
		}
		if len(parsed.NextCursor) > maxCursorBytes || seenCursors[parsed.NextCursor] {
			return nil, errors.New("MCP tools/list returned an invalid pagination cursor")
		}
		seenCursors[parsed.NextCursor] = true
		cursor = parsed.NextCursor
		if page == maxToolPages-1 {
			return nil, fmt.Errorf("MCP tools/list exceeds %d pages", maxToolPages)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	catalog := make(map[string]Tool, len(out))
	for _, tool := range out {
		catalog[tool.Name] = tool
	}
	c.mu.Lock()
	c.tools = catalog
	c.toolsDirty = c.toolsRevision != startRevision
	c.mu.Unlock()
	return append([]Tool(nil), out...), nil
}

func decodeTool(raw json.RawMessage) (Tool, error) {
	var wire struct {
		Name         string         `json:"name"`
		Title        string         `json:"title"`
		Description  string         `json:"description"`
		InputSchema  map[string]any `json:"inputSchema"`
		OutputSchema map[string]any `json:"outputSchema"`
	}
	if err := decodeStrict(raw, &wire); err != nil {
		return Tool{}, errors.New("MCP tools/list returned an invalid tool definition")
	}
	if !validRemoteToolName(wire.Name) {
		return Tool{}, errors.New("MCP tools/list returned an invalid tool name")
	}
	input, err := compileToolSchema("input", wire.InputSchema)
	if err != nil {
		return Tool{}, fmt.Errorf("MCP tool %q: %w", safeName(wire.Name), err)
	}
	var output *compiledSchema
	if wire.OutputSchema != nil {
		output, err = compileToolSchema("output", wire.OutputSchema)
		if err != nil {
			return Tool{}, fmt.Errorf("MCP tool %q: %w", safeName(wire.Name), err)
		}
	}
	return Tool{
		Name: wire.Name, Title: sanitizeText(wire.Title, 256),
		Description: sanitizeText(wire.Description, maxDescriptionBytes),
		InputSchema: input.wire, OutputSchema: cloneSchema(output),
		inputValidator: input, outputValidator: output,
	}, nil
}

func cloneSchema(s *compiledSchema) map[string]any {
	if s == nil {
		return nil
	}
	return cloneMap(s.wire)
}

// CallTool validates the exact discovered schema before sending any bytes.
// It also validates structuredContent against outputSchema when declared.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	ctx, cancel := withDefaultTimeout(ctx, defaultCallTime)
	defer cancel()
	c.mu.Lock()
	tool, ok := c.tools[name]
	dirty := c.toolsDirty
	c.mu.Unlock()
	if dirty {
		return CallToolResult{}, errors.New("MCP tool catalog changed; reconnect required")
	}
	if !ok {
		return CallToolResult{}, fmt.Errorf("MCP tool %q was not discovered", safeName(name))
	}
	if args == nil {
		args = map[string]any{}
	}
	argBytes, err := json.Marshal(args)
	if err != nil || len(argBytes) > maxRequestBytes/2 {
		return CallToolResult{}, fmt.Errorf("MCP tool %q arguments are not bounded JSON", safeName(name))
	}
	if err := tool.inputValidator.validate(args); err != nil {
		return CallToolResult{}, fmt.Errorf("MCP tool %q arguments: %s", safeName(name), err)
	}
	raw, err := c.callRaw(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallToolResult{}, err
	}
	var result CallToolResult
	if err := decodeStrict(raw, &result); err != nil || result.Content == nil {
		return CallToolResult{}, fmt.Errorf("MCP tool %q returned an invalid result", safeName(name))
	}
	if _, err := result.ModelOutput(); err != nil {
		return CallToolResult{}, err
	}
	if result.IsError {
		return CallToolResult{}, &ToolExecutionError{Message: toolErrorMessage(name, result.Content)}
	}
	if tool.outputValidator != nil {
		if result.StructuredContent == nil {
			return CallToolResult{}, fmt.Errorf("MCP tool %q omitted required structuredContent", safeName(name))
		}
		if err := tool.outputValidator.validate(result.StructuredContent); err != nil {
			return CallToolResult{}, fmt.Errorf("MCP tool %q output: %s", safeName(name), err)
		}
	}
	return result, nil
}

func toolErrorMessage(name string, content []map[string]any) string {
	var parts []string
	for _, block := range content {
		if block["type"] != "text" {
			continue
		}
		if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, sanitizeText(text, 2<<10))
		}
		if len(parts) == 2 {
			break
		}
	}
	detail := strings.Join(parts, " · ")
	if detail == "" {
		detail = "server reported an execution error"
	}
	return fmt.Sprintf("MCP tool %q failed: %s", safeName(name), detail)
}

func parseWireResponse(data []byte) (wireResponse, error) {
	var resp wireResponse
	if err := decodeStrict(data, &resp); err != nil || resp.JSONRPC != "2.0" {
		return wireResponse{}, errors.New("invalid JSON-RPC response")
	}
	if len(resp.ID) == 0 && resp.Method == "" {
		return wireResponse{}, errors.New("JSON-RPC message has neither id nor method")
	}
	if len(resp.ID) > 0 && resp.Method != "" {
		return wireResponse{}, errors.New("JSON-RPC message mixes response and request fields")
	}
	if len(resp.ID) > 0 && resp.Error == nil && len(resp.Result) == 0 {
		return wireResponse{}, errors.New("JSON-RPC response has neither result nor error")
	}
	return resp, nil
}

func responseResult(resp wireResponse) (json.RawMessage, error) {
	if resp.Error != nil {
		return nil, fmt.Errorf("MCP protocol error %d: %s", resp.Error.Code, sanitizeText(resp.Error.Message, 512))
	}
	return append(json.RawMessage(nil), resp.Result...), nil
}

func responseIDMatches(raw json.RawMessage, id int64) bool {
	var got int64
	return json.Unmarshal(raw, &got) == nil && got == id
}

func marshalFrame(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, errors.New("MCP request is not JSON")
	}
	if len(data) > maxRequestBytes {
		return nil, fmt.Errorf("MCP request exceeds %d bytes", maxRequestBytes)
	}
	return data, nil
}

func decodeStrict(data []byte, dst any) error {
	if len(data) == 0 || len(data) > maxResponseBytes || !utf8.Valid(data) {
		return errors.New("invalid JSON payload")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid endpoint")
	}
	return nil
}

func validateHeaders(headers []string) error {
	for _, raw := range headers {
		key, value, ok := strings.Cut(raw, " ")
		if !ok || key == "" || value == "" || !validHeaderName(key) || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid header")
		}
		switch strings.ToLower(key) {
		case "host", "content-length", "content-type", "accept", "mcp-session-id", "mcp-protocol-version":
			return errors.New("reserved header")
		}
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func validSessionID(s string) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validRemoteToolName(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > maxToolNameBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func canonicalWorkDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("working directory cannot be resolved")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func splitCommand(command string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			args = append(args, word.String())
			word.Reset()
			started = false
		}
	}
	for _, r := range command {
		if r == 0 {
			return nil, errors.New("NUL in command")
		}
		if escaped {
			word.WriteRune(r)
			started, escaped = true, false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		switch r {
		case '\'', '"':
			quote, started = r, true
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unfinished escape or quote")
	}
	flush()
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("empty command")
	}
	return args, nil
}

func withDefaultTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

func safeName(s string) string {
	s = sanitizeText(s, 128)
	if strings.TrimSpace(s) == "" {
		return "unnamed"
	}
	return s
}

func sanitizeText(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "�") {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) && !unicode.In(r, unicode.Cf) {
			b.WriteRune(r)
		}
		if b.Len() >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func (c *Client) redactText(value string, max int) string {
	// Status/errors are presentation surfaces. Treat every configured transport
	// value as sensitive, not only the bearer token: malicious servers know
	// their endpoint, argv and headers and can reflect fragments deliberately.
	secrets := []string{c.Server.Token, c.Server.URL, c.Server.Command, c.Server.WorkDir}
	if endpoint, err := url.Parse(c.Server.URL); err == nil {
		secrets = append(secrets, endpoint.Host, endpoint.RawQuery)
		if endpoint.Path != "" && endpoint.Path != "/" {
			secrets = append(secrets, endpoint.Path)
		}
		for _, values := range endpoint.Query() {
			secrets = append(secrets, values...)
		}
	}
	if argv, err := splitCommand(c.Server.Command); err == nil {
		secrets = append(secrets, argv...)
	}
	for _, header := range c.Server.Headers {
		if _, headerValue, ok := strings.Cut(header, " "); ok {
			secrets = append(secrets, headerValue)
			secrets = append(secrets, strings.Fields(headerValue)...)
		}
	}
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return sanitizeText(value, max)
}

// Registry manages configured servers.
type Registry struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func NewRegistry() *Registry { return &Registry{clients: map[string]*Client{}} }

// Add registers a server. Duplicate names fail closed on the first client so
// configuration order cannot silently replace a live integration.
func (r *Registry) Add(s Server) *Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.clients[s.Name]; ok {
		err := errors.New("duplicate MCP server name")
		existing.mu.Lock()
		existing.disabledErr = err
		existing.mu.Unlock()
		existing.setStatus("error", err)
		return existing
	}
	c := New(s)
	r.clients[s.Name] = c
	return c
}

func (r *Registry) Get(name string) (*Client, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[name]
	return c, ok
}

// Clients returns a deterministic snapshot of client pointers.
func (r *Registry) Clients() []*Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.clients))
	for name := range r.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*Client, 0, len(names))
	for _, name := range names {
		out = append(out, r.clients[name])
	}
	return out
}

func (r *Registry) ConnectAll(ctx context.Context) {
	for _, c := range r.Clients() {
		if err := c.Connect(ctx); err != nil {
			continue
		}
	}
}

func (r *Registry) CloseAll() {
	clients := r.Clients()
	var wg sync.WaitGroup
	wg.Add(len(clients))
	for _, client := range clients {
		client := client
		go func() {
			defer wg.Done()
			_ = client.Close()
		}()
	}
	wg.Wait()
}

// OAuth is retained as a fail-closed compatibility shim. Interactive callers
// must use OAuthWithOpen so the authorization URL is routed through their UI
// rather than corrupting a TUI by writing to process-global stdout.
func OAuth(ctx context.Context, clientID, authorizeURL, tokenURL, redirectBase string) (string, error) {
	return "", errors.New("oauth: authorization URL callback is required; use OAuthWithOpen")
}

// OAuthWithOpen performs the authorization-code flow for an HTTP server.
// Tokens are returned to the caller and are never printed or persisted by
// this package.
func OAuthWithOpen(ctx context.Context, clientID, authorizeURL, tokenURL, redirectBase string, openURL func(string)) (string, error) {
	if openURL == nil {
		return "", errors.New("oauth: authorization URL callback is required")
	}
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("oauth: generate state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			select {
			case errCh <- errors.New("oauth: state mismatch"):
			default:
			}
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			select {
			case errCh <- errors.New("oauth: authorization code is missing"):
			default:
			}
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		select {
		case codeCh <- code:
		default:
		}
		_, _ = w.Write([]byte("OK — you can close this tab."))
	})
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	redirect := redirectBase
	if redirect == "" {
		redirect = "http://" + ln.Addr().String() + "/callback"
	}
	authURL := fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&state=%s",
		authorizeURL, url.QueryEscape(clientID), url.QueryEscape(redirect), state)
	openURL(authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
	form := "grant_type=authorization_code&code=" + url.QueryEscape(code) + "&redirect_uri=" + url.QueryEscape(redirect) + "&client_id=" + url.QueryEscape(clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	httpClient := newHTTPClient()
	defer httpClient.CloseIdleConnections()
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", errors.New("oauth token request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return "", errors.New("oauth token response is invalid")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth token endpoint returned status %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := decodeStrict(body, &token); err != nil {
		return "", errors.New("oauth token response is invalid")
	}
	if token.Error != "" || token.AccessToken == "" {
		return "", errors.New("oauth token endpoint did not return an access token")
	}
	return token.AccessToken, nil
}
