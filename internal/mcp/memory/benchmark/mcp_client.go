package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	errors "github.com/Laisky/errors/v2"
)

const mcpSessionHeader = "Mcp-Session-Id"

// MCPClient is a minimal Streamable HTTP JSON-RPC client for benchmark tool calls.
type MCPClient struct {
	endpoint        *url.URL
	authorization   string
	protocolVersion string
	httpClient      *http.Client
	requestID       atomic.Uint64
	mu              sync.Mutex
	sessionID       string
	initialized     bool
	closed          bool
}

// NewMCPClient constructs a client without opening a session.
func NewMCPClient(endpoint, authorization, protocolVersion string, httpClient *http.Client) (*MCPClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, errors.Wrap(err, "parse MCP endpoint")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("MCP endpoint must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("MCP endpoint host is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if protocolVersion == "" {
		protocolVersion = DefaultProtocolVersion
	}
	return &MCPClient{
		endpoint: parsed, authorization: strings.TrimSpace(authorization),
		protocolVersion: protocolVersion, httpClient: httpClient,
	}, nil
}

// Initialize opens the MCP session once and sends notifications/initialized.
func (c *MCPClient) Initialize(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("MCP client is closed")
	}
	if c.initialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	requestID := c.nextID()
	_, err := c.rpc(ctx, rpcRequest{
		JSONRPC: "2.0", ID: requestID, Method: "initialize",
		Params: map[string]any{
			"protocolVersion": c.protocolVersion,
			"capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "laisky-memory-bench", "version": HarnessVersion},
		},
	})
	if err != nil {
		return errors.Wrap(err, "MCP initialize")
	}
	if _, err := c.rpc(ctx, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return errors.Wrap(err, "MCP initialized notification")
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	return nil
}

// CallTool invokes one MCP tool and returns its JSON payload.
func (c *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	resultRaw, err := c.rpc(ctx, rpcRequest{
		JSONRPC: "2.0", ID: c.nextID(), Method: "tools/call",
		Params: map[string]any{"name": name, "arguments": arguments},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "MCP tools/call %s", name)
	}
	var result toolCallResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return nil, errors.Wrap(err, "decode MCP tool result")
	}
	if result.IsError {
		return nil, errors.Errorf("MCP tool %s failed: %s", name, result.text())
	}
	if len(result.StructuredContent) > 0 && string(result.StructuredContent) != "null" {
		return result.StructuredContent, nil
	}
	for _, content := range result.Content {
		if content.Type != "text" || strings.TrimSpace(content.Text) == "" {
			continue
		}
		text := strings.TrimSpace(content.Text)
		if json.Valid([]byte(text)) {
			return json.RawMessage(text), nil
		}
		fallback, marshalErr := json.Marshal(map[string]string{"text": text})
		if marshalErr != nil {
			return nil, errors.Wrap(marshalErr, "encode MCP text fallback")
		}
		return fallback, nil
	}
	return json.RawMessage(`{}`), nil
}

// Close terminates the Streamable HTTP session when the server issued one.
func (c *MCPClient) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint.String(), nil)
	if err != nil {
		return errors.Wrap(err, "construct MCP close request")
	}
	c.applyHeaders(req, sessionID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "close MCP session")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	return errors.Errorf("close MCP session: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

func (r toolCallResult) text() string {
	parts := make([]string, 0, len(r.Content))
	for _, content := range r.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "; ")
}

func (c *MCPClient) rpc(ctx context.Context, request rpcRequest) (json.RawMessage, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, errors.Wrap(err, "encode MCP request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "construct MCP request")
	}
	c.mu.Lock()
	sessionID := c.sessionID
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("MCP client is closed")
	}
	c.applyHeaders(httpRequest, sessionID)
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, errors.Wrap(err, "send MCP request")
	}
	defer func() { _ = response.Body.Close() }()
	if newSessionID := strings.TrimSpace(response.Header.Get(mcpSessionHeader)); newSessionID != "" {
		c.mu.Lock()
		c.sessionID = newSessionID
		c.mu.Unlock()
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return nil, errors.Errorf("MCP HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(errorBody)))
	}
	if response.StatusCode == http.StatusAccepted || request.ID == 0 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, nil
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64*1024*1024))
	if err != nil {
		return nil, errors.Wrap(err, "read MCP response")
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	var envelope rpcResponse
	if mediaType == "text/event-stream" || bytes.Contains(responseBody, []byte("data:")) {
		envelope, err = decodeSSEResponse(responseBody, request.ID)
	} else {
		err = json.Unmarshal(responseBody, &envelope)
	}
	if err != nil {
		return nil, errors.Wrap(err, "decode MCP JSON-RPC response")
	}
	if envelope.Error != nil {
		return nil, errors.Errorf("MCP JSON-RPC %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		return nil, errors.New("MCP JSON-RPC response has no result")
	}
	return envelope.Result, nil
}

func (c *MCPClient) applyHeaders(request *http.Request, sessionID string) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", c.protocolVersion)
	if c.authorization != "" {
		request.Header.Set("Authorization", c.authorization)
	}
	if sessionID != "" {
		request.Header.Set(mcpSessionHeader, sessionID)
	}
}

func (c *MCPClient) nextID() uint64 { return c.requestID.Add(1) }

func decodeSSEResponse(body []byte, requestID uint64) (rpcResponse, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	var fallback *rpcResponse
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var envelope rpcResponse
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			continue
		}
		fallback = &envelope
		var responseID uint64
		if json.Unmarshal(envelope.ID, &responseID) == nil && responseID == requestID {
			return envelope, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, errors.Wrap(err, "scan MCP SSE response")
	}
	if fallback != nil {
		return *fallback, nil
	}
	return rpcResponse{}, errors.New("MCP SSE response did not contain a JSON-RPC event")
}

func decodeToolPayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("tool payload is empty")
	}
	return errors.WithStack(json.Unmarshal(raw, target))
}
