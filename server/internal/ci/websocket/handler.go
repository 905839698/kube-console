package websocket

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// RunGuard 校验「当前用户能否订阅某个 run」（token 鉴权之上的 run 归属校验，
// 防止合法用户偷听其它项目的执行状态）。实现方在 handler 包（需要 DB/项目角色）。
type RunGuard interface {
	CheckRunRead(ctx context.Context, uid uint, role string, runID string) error
}

// Handler 返回 /api/v1/ws 的 gin handler。
// 查询参数：runId（订阅的执行 id）；token 走 ?token=（EventSource/WS 无法设请求头）。
// 客户端收到：{"type":"run","data":{...}} 状态事件；日志走 SSE，不走 WS。
// guard 非 nil 时，Upgrade 前校验 run 读权限。
func Handler(hub *Hub, guard RunGuard) gin.HandlerFunc {
	return func(c *gin.Context) {
		runID := c.Query("runId")
		if runID == "" {
			c.String(400, "runId required")
			return
		}
		if guard != nil {
			var (
				uid  uint
				role string
			)
			if v, ok := c.Get("uid"); ok {
				uid, _ = v.(uint)
			}
			if v, ok := c.Get("role"); ok {
				role, _ = v.(string)
			}
			if err := guard.CheckRunRead(c.Request.Context(), uid, role, runID); err != nil {
				c.String(http.StatusForbidden, "forbidden")
				return
			}
		}
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		msgs, cancel := hub.Subscribe(runID)
		defer cancel()

		// 心跳，防止代理掐断空闲连接
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()

		for {
			select {
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			case <-ping.C:
				if err := conn.WriteControl(
					websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			case <-c.Request.Context().Done():
				return
			}
		}
	}
}
