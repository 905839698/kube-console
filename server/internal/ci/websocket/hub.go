package websocket

import (
	"sync"
)

// EventType WS 消息类型。
const (
	EventRunStatus = "run" // 执行状态变更（含 task 级状态）
	EventLog       = "log" // 日志行
)

// Message WS 推送消息。
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// client 是一个已连接的 WS 客户端，按 runID 订阅。
type client struct {
	runID  string
	send   chan Message
	cancel func() // 断开时从 hub 注销
}

// Hub 按 runID 分发事件给订阅者。
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*client]struct{} // runID -> clients
}

func NewHub() *Hub {
	return &Hub{clients: make(map[string]map[*client]struct{})}
}

// Subscribe 订阅某 run 的事件，返回发送通道与注销函数。
func (h *Hub) Subscribe(runID string) (<-chan Message, func()) {
	c := &client{runID: runID, send: make(chan Message, 64)}
	h.mu.Lock()
	if h.clients[runID] == nil {
		h.clients[runID] = make(map[*client]struct{})
	}
	h.clients[runID][c] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		if set, ok := h.clients[runID]; ok {
			if _, exists := set[c]; exists {
				delete(set, c)
				if len(set) == 0 {
					delete(h.clients, runID)
				}
			}
		}
		h.mu.Unlock()
	}
	c.cancel = cancel
	return c.send, cancel
}

// Publish 向订阅 runID 的所有客户端推送事件；无人订阅时丢弃（状态已落库，前端可轮询兜底）。
func (h *Hub) Publish(runID string, msg Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[runID] {
		select {
		case c.send <- msg:
		default: // 慢消费者：丢弃，避免阻塞
		}
	}
}

// RunPayload 状态事件载荷（前端直接渲染）。
type RunPayload struct {
	RunID   uint                `json:"runId"`
	Cluster string              `json:"cluster,omitempty"`
	Status  string              `json:"status"`
	Tasks   []TaskStatusPayload `json:"tasks"`
}

// TaskStatusPayload 任务级状态。
type TaskStatusPayload struct {
	NodeID     string  `json:"nodeId"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	StartedAt  *string `json:"startedAt,omitempty"`
	FinishedAt *string `json:"finishedAt,omitempty"`
}

// PublishRun 便捷方法：推送状态事件。
func (h *Hub) PublishRun(runID string, p RunPayload) {
	h.Publish(runID, Message{Type: EventRunStatus, Data: p})
}
