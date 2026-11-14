package notification

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// List 处理 GET /notification?limit=&cursor= :当前用户的通知，新的在前面。
func (h *Handler) List(c *gin.Context) {
	userID, ok := currentUser(c)
	if !ok {
		return
	}
	var req struct {
		Limit  int    `form:"limit"`
		Cursor string `form:"cursor"`
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "查询参数格式错误"})
		return
	}
	noStore(c)
	page, err := h.service.List(c.Request.Context(), userID, req.Limit, req.Cursor)
	switch {
	case errors.Is(err, ErrInvalidLimit), errors.Is(err, ErrInvalidCursor):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		log.Printf("查询通知失败：%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询通知失败"})
	default:
		c.JSON(http.StatusOK, page)
	}
}

// UnreadCount 处理 GET /notifications/unread-count: 当前用户有几条未读通知
func (h *Handler) UnreadCount(c *gin.Context) {
	userID, ok := currentUser(c)
	if !ok {
		return
	}
	noStore(c)
	count, err := h.service.UnreadCount(c.Request.Context(), userID)
	if err != nil {
		log.Printf("查询未读数失败：%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询未读数失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// MarkRead 处理 PUT /notifications/:id/read: 第一次标记和重复标记都返回 204。
func (h *Handler) MarkRead(c *gin.Context) {
	userID, ok := currentUser(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "通知 ID 必须是正整数"})
		return
	}
	err = h.service.MarkRead(c.Request.Context(), userID, id)
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": ErrNotFound.Error()})
	case err != nil:
		log.Printf("标记已读失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "标记已读失败"})
	default:
		c.Status(http.StatusNoContent)
	}
}

// MarkAllRead 处理 PUT /notifications/read?up_to=：把 id 不大于 up_to 的未读通知都标成已读。
func (h *Handler) MarkAllRead(c *gin.Context) {
	userID, ok := currentUser(c)
	if !ok {
		return
	}
	upTo, err := strconv.ParseUint(c.Query("up_to"), 10, 64)
	if err != nil || upTo == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "up_to 必须是正整数"})
		return
	}
	if err := h.service.MarkAllRead(c.Request.Context(), userID, upTo); err != nil {
		log.Printf("全部标记已读失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "标记已读失败"})
		return
	}
	c.Status(http.StatusNoContent)
}

// currentUser 取出当前登录的用户 (只来自 JWT 中间件)； 返回 false 时错误响应已经写好
func currentUser(c *gin.Context) (uint64, bool) {
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || !ok || userID == 0 {
		// 走到这里说明路由漏挂了 JWT 中间件，是服务端的问题，所以返回 500 而不是 401
		c.JSON(http.StatusInternalServerError, gin.H{"error": "请求中缺少有效的用户身份"})
		return 0, false
	}
	return userID, true
}

// noStore: 同一个 URL，不同的人看到的内容不同，不能被共享缓存
func noStore(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Vary", "Authorization")
}
