package video

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (h *Handler) Like(c *gin.Context) {
	ctx := c.Request.Context()
	// 1、 获取当前登录用户
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || userID == 0 || !ok {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	// 2、 获取要点赞的视频 ID
	videoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || videoID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频 ID 必须是正整数",
		})
		return
	}
	// 3、调用点赞业务
	if h.LikeService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "点赞服务未初始化",
		})
		return
	}
	err = h.LikeService.LikeVideo(ctx, userID, videoID)
	if errors.Is(err, ErrLikeVideoNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": ErrLikeVideoNotFound.Error(),
		})
		return
	}
	if err != nil {
		log.Printf("点赞失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "点赞失败",
		})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) UnLike(c *gin.Context) {
	ctx := c.Request.Context()
	// 1、获取当前登录用户
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || userID == 0 || !ok {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效用户身份",
		})
		return
	}
	// 2、获取视频ID
	videoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || videoID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频 ID 必须是正整数",
		})
		return
	}
	// 3、调用取消点赞业务
	if h.LikeService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "点赞服务未初始化",
		})
		return
	}
	err = h.LikeService.UnLikeVideo(ctx, userID, videoID)
	if err != nil {
		log.Printf("取消点赞失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "取消点赞失败",
		})
		return
	}
	c.Status(http.StatusNoContent)
}

// 查看点赞数
func (h *Handler) GetLikeStatus(c *gin.Context) {
	ctx := c.Request.Context()
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || userID == 0 || !ok {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	videoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || videoID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频 ID 必须是正整数",
		})
		return
	}

	// 调用业务层查询点赞状态
	if h.LikeService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "点赞服务未初始化",
		})
		return
	}
	status, err := h.LikeService.GetLikeStatus(ctx, userID, videoID)
	if errors.Is(err, ErrLikeVideoNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": ErrLikeVideoNotFound.Error(),
		})
		return
	}
	if err != nil {
		log.Printf("查询点赞状态失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "查询点赞状态失败",
		})
		return
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"like_count": status.LikeCount,
		"liked":      status.Liked,
	})
}
