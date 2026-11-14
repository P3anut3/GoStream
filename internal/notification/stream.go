package notification

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// SessionChecker 回答"这个登录会话还在不在"；auth 包的会话存储实现了它。
type SessionChecker interface {
	Exists(ctx context.Context, sessionID string) (bool, error)
}

// StreamHandler 处理 GET /notifications/stream：一条不结束的响应（SSE），有新通知时写一条提示。
// 提示里没有通知内容，浏览器收到后自己去查未读数和列表。
type StreamHandler struct {
	hub          *Hub
	sessions     SessionChecker // 心跳时复查会话；nil 表示不复查
	heartbeat    time.Duration  // 多久写一次心跳
	writeTimeout time.Duration  // 每次写最多等多久
}

func NewStreamHandler(hub *Hub, sessions SessionChecker) *StreamHandler {
	return &StreamHandler{hub: hub, sessions: sessions, heartbeat: 25 * time.Second, writeTimeout: 10 * time.Second}
}

// Stream 先登记连接、写响应头，然后一直等，直到浏览器断开、token 过期、会话被删除或服务关闭。
func (h *StreamHandler) Stream(c *gin.Context) {
	userID, ok := currentUser(c)
	if !ok {
		return
	}
	// 中间件放进来的 token 过期时间：连接不能比 token 活得更久。
	expiresAt := c.GetTime("tokenExpiresAt")
	if expiresAt.IsZero() {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "请求中缺少 token 的过期时间"})
		return
	}
	sub, err := h.hub.Subscribe(userID)
	switch {
	case errors.Is(err, ErrTooManyStreams):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
		return
	case err != nil:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	defer sub.Close()

	noStore(c)
	c.Header("Content-Type", "text/event-stream")
	c.Header("X-Accel-Buffering", "no") // 放在 nginx 后面时，让它别把内容攒着不发
	c.Status(http.StatusOK)

	rc := http.NewResponseController(c.Writer)
	// write 写一段文字并立刻发出去；返回 false 表示连接已经不能用了。
	write := func(text string) bool {
		// 每次写之前设写超时：浏览器不读了，写会在 10 秒后失败，而不是永远卡住。
		// 测试用的 ResponseRecorder 不支持写超时，这时只是少了这层保护。
		if err := rc.SetWriteDeadline(time.Now().Add(h.writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := io.WriteString(c.Writer, text); err != nil {
			return false
		}
		// 不 Flush 的话，内容会留在缓冲区里，浏览器收不到。
		return rc.Flush() == nil
	}
	// 冒号开头的行是注释，浏览器会跳过；先写一行，让浏览器马上拿到响应头。
	if !write(": connected\n\n") {
		return
	}

	ctx := c.Request.Context()
	sessionID := c.GetString("sessionID")
	heartbeat := time.NewTicker(h.heartbeat)
	defer heartbeat.Stop()
	expiry := time.NewTimer(time.Until(expiresAt))
	defer expiry.Stop()
	for {
		select {
		case <-ctx.Done(): // 浏览器断开了
			return
		case <-expiry.C: // token 到期：浏览器会用新 token 重连
			return
		case _, open := <-sub.C():
			if !open { // 槽被关闭：服务正在关闭
				return
			}
			if !write("event: notification\ndata: {}\n\n") {
				return
			}
		case <-heartbeat.C:
			if h.sessionEnded(ctx, sessionID) {
				return
			}
			if !write(": ping\n\n") {
				return
			}
		}
	}
}

// sessionEnded 复查会话：确定已经删除（退出登录、检测到 refresh token 被盗用）才返回 true。
// 查不了（Redis 故障）时按还在处理，和中间件一样；连接最晚在 token 过期时结束。
func (h *StreamHandler) sessionEnded(ctx context.Context, sessionID string) bool {
	if h.sessions == nil {
		return false
	}
	exists, err := h.sessions.Exists(ctx, sessionID)
	if err != nil {
		log.Printf("复查登录会话失败，推送连接继续: %v", err)
		return false
	}
	return !exists
}
