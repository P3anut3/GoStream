package video

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
)

type CreateCommentRequest struct {
	Content string `json:"content"`
}

// 创建评论
func (h *Handler) CreateComment(ctx context.Context, c *app.RequestContext) {
	// 1、获取评论者身份
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || !ok || userID == 0 {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	// 2、获取目标视频ID
	videoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || videoID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频 ID 必须是正整数",
		})
		return
	}
	// 3、解析评论内容
	var req CreateCommentRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请求体不是有效的评论 JSON",
		})
		return
	}
	// 4. 把普通 Go 数据交给业务层；Handler 负责将结果映射成 HTTP 响应。
	if h.CommentService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "评论服务未初始化"})
		return
	}
	comment, err := h.CommentService.CreateComment(ctx, userID, videoID, req.Content)
	if errors.Is(err, ErrInvalidCommentContent) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": ErrInvalidCommentContent.Error()})
		return
	}
	if errors.Is(err, ErrCommentVideoNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{"error": ErrCommentVideoNotFound.Error()})
		return
	}
	if err != nil {
		log.Printf("发表评论失败: %v", err)
		message := "保存评论失败"
		var storageErr *commentStorageError
		if errors.As(err, &storageErr) {
			message = storageErr.message
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": message})
		return
	}
	c.JSON(http.StatusCreated, map[string]interface{}{
		"id":       comment.ID,
		"video_id": comment.VideoID,
		"user_id":  comment.UserID,
		"content":  comment.Content,
	})

}

// 查看评论列表
// 视频 ID、页码、每页数量的解析和检查仍属于 Handler。
func (h *Handler) ListComments(ctx context.Context, c *app.RequestContext) {
	// 1、获取视频ID
	videoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || videoID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频 ID 必须是正整数",
		})
		return
	}
	pageText := c.DefaultQuery("page", "1")
	sizeText := c.DefaultQuery("page_size", "10")

	page, err := strconv.Atoi(pageText)
	if err != nil || page < 1 || page > 10000 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "page 必须是1~10000的整数",
		})
		return
	}
	pageSize, err := strconv.Atoi(sizeText)
	if err != nil || pageSize < 1 || pageSize > 100 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "page_size 必须是1~100的整数",
		})
		return
	}
	offset := (page - 1) * pageSize
	// 2、调用业务层查询评论
	if h.CommentService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "评论服务未初始化",
		})
		return
	}
	comments, err := h.CommentService.ListComments(ctx, videoID, pageSize, offset)
	// 错误分两类， 1、ErrCommentVideoNotFound不存在。2、其他错误	比如数据库连接失败、读取结果失败
	if errors.Is(err, ErrCommentVideoNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": ErrCommentVideoNotFound.Error(),
		})
		return
	}
	if err != nil {
		log.Printf("查询评论列表失败: %v", err)
		message := "评论列表查询失败"
		var storageErr *commentStorageError
		if errors.As(err, &storageErr) {
			message = storageErr.message
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": message})
		return
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"video_id":  videoID,
		"page":      page,
		"page_size": pageSize,
		"comments":  comments,
	})
}

// 删除评论
func (h *Handler) DeleteComment(ctx context.Context, c *app.RequestContext) {
	// 1. 获取当前登录用户
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || !ok || userID == 0 {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}

	// 2. 获取要删除的评论 ID
	commentID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || commentID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "评论 ID 必须是正整数",
		})
		return
	}

	// 3. 调用业务层的删除
	if h.CommentService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "评论服务未初始化",
		})
		return
	}

	err = h.CommentService.DeleteComment(ctx, userID, commentID)
	// 4、将业务层定义的错误转化为http响应
	if errors.Is(err, ErrCommentNotFound) { // 评论未找到
		c.JSON(http.StatusNotFound, map[string]string{
			"error": ErrCommentNotFound.Error(),
		})
		return
	}
	if err != nil {
		log.Printf("删除评论失败: %v", err)
		message := "删除评论失败"
		var storageErr *commentStorageError
		if errors.As(err, &storageErr) {
			message = storageErr.message
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": message})
		return
	}
	// 5、删除成功，无需返回响应体
	c.Status(http.StatusNoContent)

}
