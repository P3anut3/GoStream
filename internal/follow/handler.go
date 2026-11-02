package follow

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

// Follow 处理 PUT /users/:id/follow：首次关注和重复关注都返回 204
func (h *Handler) Follow(c *gin.Context) {
	followerID, followeeID, ok := h.relationIDs(c)
	if !ok {
		return
	}
	err := h.service.Follow(c.Request.Context(), followerID, followeeID)
	switch {
	case errors.Is(err, ErrCannotFollowSelf):
		c.JSON(http.StatusBadRequest, gin.H{"error": ErrCannotFollowSelf.Error()})
	case errors.Is(err, ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": ErrUserNotFound.Error()})
	case err != nil:
		log.Printf("关注失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "关注失败"})
	default:
		c.Status(http.StatusNoContent)
	}
}

// Unfollow 处理 DELETE /users/:id/follow: 本来没有关注、目标不存在也返回204.
func (h *Handler) Unfollow(c *gin.Context) {
	followerID, followeeID, ok := h.relationIDs(c)
	if !ok {
		return
	}
	if err := h.service.Unfollow(c.Request.Context(), followerID, followeeID); err != nil {
		log.Printf("取消关注失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "取消关注失败"})
		return
	}
	c.Status(http.StatusNoContent)
}

// GetStatus 处理 GET /users/:id/follow：返回当前登录用户是否关注了目标用户
func (h *Handler) GetStatus(c *gin.Context) {
	followerID, followeeID, ok := h.relationIDs(c)
	if !ok {
		return
	}
	// 同一个URL 对不同登录用户返回结果不同，禁止共享缓存
	c.Header("Cache-Control", "private, no-store")
	c.Header("Vary", "Authorization")
	following, err := h.service.IsFollowing(c.Request.Context(), followerID, followeeID)
	if errors.Is(err, ErrUserNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": ErrUserNotFound.Error()})
		return
	}
	if err != nil {
		log.Printf("查询关注状态失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询关注状态失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"following": following})
}

// relationIDs 取出操作人(只来自JWT)和目标用户(来自路径)；返回 false 时错误响应已经写好
func (h *Handler) relationIDs(c *gin.Context) (uint64, uint64, bool) {
	value, exists := c.Get("userID")
	followerID, ok := value.(uint64)
	if !exists || !ok || followerID == 0 {
		// 走到这里说明路由漏挂了 JWT 中间件， 是服务端的问题，所以返回 500 而不是 401.
		c.JSON(http.StatusInternalServerError, gin.H{"error": "请求中缺少有效的用户身份"})
		return 0, 0, false
	}
	followeeID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || followeeID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户 ID 必须是正整数"})
		return 0, 0, false
	}

	if h.service == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "关注服务未初始化"})
		return 0, 0, false
	}
	return followerID, followeeID, true
}
