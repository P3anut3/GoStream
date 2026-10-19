package video

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/go-sql-driver/mysql"
)

func (h *Handler) Like(ctx context.Context, c *app.RequestContext) {
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
	// 3、 视频存在时，才插入点赞记录
	result, err := h.DB.ExecContext(ctx,
		`INSERT INTO video_likes (user_id, video_id)
		SELECT ?, id FROM videos WHERE id = ?`,
		userID, videoID,
	)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			// 已经点赞，保持已点赞状态，也算成功
			c.Status(http.StatusNoContent)
			return
		}
		log.Printf("点赞失败：%v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "点赞失败",
		})
		return
	}
	// 4、没有插入记录，说明视频不存在
	affected, err := result.RowsAffected()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取点赞结果失败",
		})
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "视频不存在",
		})
		return
	}
	c.Status(http.StatusNoContent)

}

func (h *Handler) UnLike(ctx context.Context, c *app.RequestContext) {
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
	// 3、只删除当前用户对这个视频的点赞
	_, err = h.DB.ExecContext(ctx,
		`DELETE FROM video_likes WHERE user_id = ? AND video_id = ?`,
		userID, videoID)
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
func (h *Handler) GetLikeStatus(ctx context.Context, c *app.RequestContext) {
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

	var likeCount uint64
	var liked bool
	err = h.DB.QueryRowContext(ctx,
		`SELECT
		    (SELECT COUNT(*) FROM video_likes WHERE video_id = v.id),
		    EXISTS(
		        SELECT 1 FROM video_likes
		        WHERE video_id = v.id AND user_id = ?
		    )
		 FROM videos AS v
		 WHERE v.id = ?`,
		userID, videoID,
	).Scan(&likeCount, &liked)

	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "视频不存在",
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
		"like_count": likeCount,
		"liked":      liked,
	})
}
