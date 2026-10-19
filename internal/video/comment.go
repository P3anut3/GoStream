package video

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

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
	req.Content = strings.TrimSpace(req.Content)
	length := utf8.RuneCountInString(req.Content)
	if length < 1 || length > 500 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "评论内容必须为 1~500 个字符",
		})
		return
	}
	// 4、视频存在时，才插入评论
	result, err := h.DB.ExecContext(ctx,
		`INSERT INTO video_comments(video_id, user_id, content)
		SELECT id,?,? FROM videos WHERE id = ?`,
		userID, req.Content, videoID,
	)
	if err != nil {
		log.Printf("保存评论失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "保存评论失败",
		})
		return
	}

	affected, err := result.RowsAffected()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取评论保存结果失败",
		})
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "视频不存在",
		})
		return
	}
	commentID, err := result.LastInsertId()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取评论 ID 失败",
		})
		return
	}
	c.JSON(http.StatusCreated, map[string]interface{}{
		"id":       commentID,
		"video_id": videoID,
		"user_id":  userID,
		"content":  req.Content,
	})

}

// 查看评论列表
func (h *Handler) ListComments(ctx context.Context, c *app.RequestContext) {
	// 获取视频ID
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
	// 确认目标视频存在
	var foundID uint64
	err = h.DB.QueryRowContext(ctx,
		`SELECT id FROM videos WHERE id = ?`,
		videoID,
	).Scan(&foundID)

	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "视频不存在",
		})
		return
	}
	if err != nil {
		log.Printf("检查视频失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "检查视频失败",
		})
		return
	}

	rows, err := h.DB.QueryContext(ctx,
		`SELECT id, video_id, user_id, content, created_at
	 FROM video_comments
	 WHERE video_id = ?
	 ORDER BY id DESC
	 LIMIT ? OFFSET ?`,
		videoID, pageSize, offset,
	)
	if err != nil {
		log.Printf("评论列表查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "评论列表查询失败",
		})
		return
	}
	defer rows.Close()

	//将查询到的结果返回
	comments := make([]Comment, 0)
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(
			&comment.ID,
			&comment.VideoID,
			&comment.UserID,
			&comment.Content,
			&comment.CreatedAt,
		); err != nil {
			log.Printf("读取评论失败: %v", err)
			c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "读取评论失败",
			})
			return
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		log.Printf("遍历评论列表失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "查询评论列表失败",
		})
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

	// 3. 同时匹配评论 ID 和评论者 ID
	result, err := h.DB.ExecContext(ctx,
		`DELETE FROM video_comments WHERE id = ? AND user_id = ?`,
		commentID, userID,
	)
	if err != nil {
		log.Printf("删除评论失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "删除评论失败",
		})
		return
	}

	affected, err := result.RowsAffected()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取删除结果失败",
		})
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "评论不存在或不属于当前用户",
		})
		return
	}

	c.Status(http.StatusNoContent)
}
