package mcp

import (
	"encoding/json"
	"fmt"
)

// jsonrpcVersion 是唯一的 JSON-RPC 版本，MCP 只使用 2.0。
const jsonrpcVersion = "2.0"

// rpcRequest 是一条 JSON-RPC 2.0 请求。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcNotification 是一条 JSON-RPC 2.0 通知（没有 id，不需要响应）。
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcMessage 是入站消息的统一视图：响应、请求与通知共用一个结构。
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError 是 JSON-RPC 错误对象；Code 使用 JSON-RPC 标准错误码或服务器自定义码。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error。
func (e *RPCError) Error() string {
	if e == nil {
		return "json-rpc error"
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// decodeID 把响应的 id 解析为内部使用的 int64。非数字 id 无法与我们的请求关联，
// 返回 false 后被 readLoop 忽略。
func decodeID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return 0, false
	}
	id, err := num.Int64()
	if err != nil {
		return 0, false
	}
	return id, true
}
