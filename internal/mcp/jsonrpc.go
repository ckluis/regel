package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"regel.dev/regel/internal/admission"
	"regel.dev/regel/internal/pgwire"
)

// jsonrpc.go is the owned, minimal JSON-RPC 2.0 (zero third-party deps): the wire
// types, the method dispatch, a newline-delimited stdio server, and an HTTP door —
// both feeding the SAME Dispatch, so stdio and HTTP are two mouths on one gate.

const jsonRPCVersion = "2.0"

// rpcRequest is one JSON-RPC request (or notification, when id is absent).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one JSON-RPC response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	skip    bool            // internal: a notification produces no reply
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC + MCP error codes used on this plane.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeUnauthorized   = -32000 // agent-plane: missing/revoked key (rotation)
)

func okResp(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result}
}
func errResp(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// Dispatch routes one request to its handler. It is the single entry both doors
// call. A notification (absent id) whose handler has no reply yields skip=true.
func (s *Server) Dispatch(ctx context.Context, sess *Session, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return okResp(req.ID, s.initializeResult())
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{skip: true}
	case "ping":
		return okResp(req.ID, map[string]any{})
	case "tools/list":
		return okResp(req.ID, map[string]any{"tools": toolSpecs()})
	case "resources/list":
		return okResp(req.ID, map[string]any{"resources": resourceSpecs()})
	case "prompts/list":
		return okResp(req.ID, map[string]any{"prompts": promptSpecs()})
	case "tools/call":
		return s.handleToolCall(ctx, sess, req)
	case "resources/read":
		return s.handleResourceRead(ctx, sess, req)
	case "prompts/get":
		return s.handlePromptGet(ctx, sess, req)
	default:
		return errResp(req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

// initializeResult advertises the server's protocol + capabilities.
func (s *Server) initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"serverInfo":      map[string]any{"name": "regel", "version": "stage-c"},
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
	}
}

// withConn acquires a pooled connection and resolves the caller principal for a
// request that needs one, returning an error response on auth failure (rotation).
func (s *Server) withConn(ctx context.Context, sess *Session, id json.RawMessage,
	fn func(conn *pgwire.Conn, p admission.Principal) rpcResponse) rpcResponse {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return errResp(id, codeInternalError, err.Error())
	}
	defer s.pool.Release(conn)
	p, ok, err := s.resolvePrincipal(ctx, conn, sess)
	if err != nil {
		return errResp(id, codeInternalError, err.Error())
	}
	if !ok {
		return errResp(id, codeUnauthorized, "unauthorized: unknown or revoked API key")
	}
	return fn(conn, p)
}

// --- stdio + HTTP doors ------------------------------------------------------

// ServeStdio runs the newline-delimited JSON-RPC loop over r/w until EOF. One JSON
// object per line in, one response object per line out (notifications produce no
// line). No daemon is left running: the loop returns when r closes.
func (s *Server) ServeStdio(ctx context.Context, sess *Session, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if e := enc.Encode(errResp(nil, codeParseError, "parse error: "+err.Error())); e != nil {
				return e
			}
			continue
		}
		resp := s.Dispatch(ctx, sess, req)
		if resp.skip {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// HTTPHandler is the HTTP door: POST one JSON-RPC request, get one response. The
// API key rides the X-Regel-Key header. Reuses Dispatch verbatim.
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, 400, errResp(nil, codeParseError, err.Error()))
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, 400, errResp(nil, codeParseError, err.Error()))
			return
		}
		sess := &Session{APIKey: r.Header.Get("X-Regel-Key")}
		resp := s.Dispatch(r.Context(), sess, req)
		if resp.skip {
			w.WriteHeader(204)
			return
		}
		writeJSON(w, 200, resp)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}
