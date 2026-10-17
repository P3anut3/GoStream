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

type Handler struct { // 持有数据库连接池，后续提供处理方法
	DB *sql.DB
}

type PublishRequest struct { // 接收客户端提交的发布信息
	Title    string `json:"title"`
	PlayURL  string `json:"play_url"`
	CoverURL string `json:"cover_url"`
}

// 上传视频
func (h *Handler) Publish(ctx context.Context, c *app.RequestContext) {
	// 1、取得JWT 中间件验证后的作者身份信息
	value, exists := c.Get("userID")
	authorID, ok := value.(uint64)
	if !exists || !ok || authorID == 0 {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	// 2、解析请求体
	var req PublishRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请求体格式错误",
		})
		return
	}
	//3、检查走单，去掉首位空白
	req.Title = strings.TrimSpace(req.Title)
	req.PlayURL = strings.TrimSpace(req.PlayURL)
	req.CoverURL = strings.TrimSpace(req.CoverURL)

	if req.Title == "" || req.PlayURL == "" || req.CoverURL == "" {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "标题、视频地址和封面地址不能为空",
		})
		return
	}
	// 计算Unicode字符
	if utf8.RuneCountInString(req.Title) > 100 || utf8.RuneCountInString(req.PlayURL) > 1024 || utf8.RuneCountInString(req.CoverURL) > 1024 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "标题最多·100个字符，地址最多1024个字符",
		})
		return
	}
	// 4、保存视频元数据
	result, err := h.DB.ExecContext(
		ctx,
		`INSERT INTO videos (author_id, title,play_url, cover_url) VALUES (?, ?, ?, ?)`, authorID, req.Title, req.PlayURL, req.CoverURL)
	if err != nil {
		log.Printf("保存视频失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "保存视频失败",
		})
		return
	}
	//5、取得自增 ID
	id, err := result.LastInsertId()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取视频ID失败",
		})
		return
	}
	c.JSON(http.StatusCreated, map[string]interface{}{
		"id":        id,
		"author_id": authorID,
		"title":     req.Title,
	})
}

// 查看视频
func (h *Handler) GetByID(ctx context.Context, c *app.RequestContext) {
	// 1、将路径中的字符串转化为视频ID
	id, err := strconv.ParseUint(c.Param("id"), 10, 64) //取得路径参数，类型是字符串。转化为10进制
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频ID必须是正整数",
		})
		return
	}
	// 2、查询视频
	var video Video
	err = h.DB.QueryRowContext(
		ctx,
		`SELECT id,author_id, title, play_url, cover_url ,created_at FROM videos WHERE  id =?`,
		id,
	).Scan(&video.ID, &video.AuthorID, &video.Title, &video.PlayURL, &video.CoverURL, &video.CreatedAt)
	// 3、区分 “不存在”和“查询失败”
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "视频不存在",
		})
		return
	}
	if err != nil {
		log.Printf("查询视频失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "视频查询失败",
		})
		return
	}
	// 4、返回视频信息
	c.JSON(http.StatusOK, map[string]interface{}{
		"id":         video.ID,
		"author_id":  video.AuthorID,
		"title":      video.Title,
		"play_url":   video.PlayURL,
		"cover_url":  video.CoverURL,
		"created_at": video.CreatedAt,
	})
}

// 查看视频列表
func (h *Handler) List(ctx context.Context, c *app.RequestContext) {
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
	rows, err := h.DB.QueryContext(ctx,
		`SELECT id,author_id,title,play_url,cover_url,created_at FROM videos LIMIT ? OFFSET ?`, pageSize, offset,
	)
	if err != nil {
		log.Printf("视频列表查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "视频列表查询失败",
		})
		return
	}
	defer rows.Close()

	//将查询到的结果返回
	videos := make([]Video, 0)
	for rows.Next() {
		var video Video
		if err := rows.Scan(
			&video.ID, &video.AuthorID,
			&video.Title,
			&video.PlayURL,
			&video.CoverURL,
			&video.CreatedAt); err != nil {
			log.Printf("读取视频记录失败: %v", err)
			c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "读取视频记录失败",
			})
			return
		}
		videos = append(videos, video)
	}
	if err := rows.Err(); err != nil {
		log.Printf("便利视频列表失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "查询视频列表失败",
		})
		return
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"page":      page,
		"page_size": pageSize,
		"videos":    videos,
	})
}

// 删除视频
func (h *Handler) Delete(ctx context.Context, c *app.RequestContext) {
	// 1、取得经过验证的当前用户ID
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || userID == 0 || !ok {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	// 2、检查视频ID
	vidoID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || vidoID == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频ID必须是正整数",
		})
		return
	}
	// 3、只删除当前用户拥有的视频
	result, err := h.DB.ExecContext(
		ctx,
		`DELETE FROM videos WHERE id=? AND author_id=?`, vidoID, userID,
	)
	if err != nil {
		log.Printf("删除视频失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "删除视频失败",
		})
		return
	}
	// 4、检查实际删除了多少行
	affected, err := result.RowsAffected()
	if err != nil {
		log.Printf("获取删除结果失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取删除结果失败",
		})
		return
	}
	if affected == 0 {
		c.JSON(http.StatusNotFound, map[string]string{
			"error": "视频不存在或不属于当前用户",
		})
		return
	}
	c.Status(http.StatusNoContent)
}
