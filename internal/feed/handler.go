package feed

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) ListLatest(c *gin.Context) {
	// 1、读取URL 查询参数
	var req ListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":  "INVALID_ARGUMENT",
			"error": "查询参数格式错误",
		})
		return
	}
	// 2、调用分页业务。
	var viewerID uint64
	if value, exists := c.Get("userID"); exists {
		id, ok := value.(uint64)
		if !ok || id == 0 {
			c.JSON(500, gin.H{"code": "INTERNAL_ERROR", "error": "请求身份异常"})
			return
		}
		viewerID = id
	}
	// 同一个 URL 对不同用户返回不同 liked，禁止共享 HTTP 缓存。
	c.Header("Cache-Control", "private, no-store")
	c.Header("Vary", "Authorization")
	result, err := h.service.ListLatestForViewer(
		c.Request.Context(),
		req.Limit,
		req.Cursor,
		viewerID,
	)
	//3、将业务错误转换成 HTTP响应
	if errors.Is(err, ErrInvalidLimit) {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":  "INVALID_ARGUMENT",
			"error": err.Error(),
		})
		return
	}
	if errors.Is(err, ErrInvalidCursor) {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":  "INVALID_CURSOR",
			"error": "视频流游标无效，请刷新列表",
		})
		return
	}
	if err != nil {
		log.Printf("查询视频流失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  "INTERNAL_ERROR",
			"error": "视频流查询失败",
		})
		return
	}
	//	4、返回 Service 已组装好的分页结果
	c.JSON(http.StatusOK, result)
}
