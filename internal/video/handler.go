package video

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
)

type Handler struct { // 持有数据库连接池，后续提供处理方法
	DB             *sql.DB
	UploadDir      string
	CoverDir       string
	CommentService *CommentService
	LikeService    *LikeService
}

type PublishRequest struct { // 接收客户端提交的发布信息
	Title    string `json:"title"`
	PlayURL  string `json:"play_url"`
	CoverURL string `json:"cover_url"`
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

// 查看公共视频列表
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
		`SELECT id,author_id,title,play_url,cover_url,created_at FROM videos ORDER BY id DESC LIMIT ? OFFSET ?`, pageSize, offset,
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

// 查看自己的视频列表
func (h *Handler) ListMine(ctx context.Context, c *app.RequestContext) {
	// 1、获取用户ID
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || !ok || userID == 0 {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户信息",
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
	rows, err := h.DB.QueryContext(ctx,
		`SELECT id,author_id,title,play_url,cover_url,created_at FROM videos WHERE author_id= ? ORDER BY id DESC LIMIT ? OFFSET ?`, userID, pageSize, offset,
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

// 创建视频作品记录。
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
	//验证是否使用当前系统的上传视频地址
	savedName, vaild := strings.CutPrefix(req.PlayURL, "/media/videos/")
	if !vaild || savedName == "" || strings.ContainsAny(savedName, `/\?#%`) {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请使用本站上传接口返回的视频地址",
		})
		return
	}
	// tx表示本次事务。后面的数据库操作必须通过它执行，才能属于同一个事务。不会只创建了视频记录，却没更新上传状态；也不会只更新上传状态，却没创建视频记录。
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("开启发布事务失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "发布视频失败",
		})
		return
	}
	defer tx.Rollback()

	// 验证上传用户是否是当前用户
	var videoUploadID, coverUploadID uint64
	var videoStatus, coverStatus string
	err = tx.QueryRowContext(
		ctx,
		`SELECT id, status FROM uploads
	 WHERE saved_name = ? AND user_id = ? AND file_type = ?
	 FOR UPDATE`,
		savedName, authorID, "video",
	).Scan(&videoUploadID, &videoStatus)
	// 查不到属于自己的上传记录”应返回 403
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusForbidden, map[string]string{
			"error": "上传文件不存在或不属于当前用户",
		})
		return
	}
	if err != nil {
		log.Printf("查询上传文件归属失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "检查上传文件归属失败",
		})
		return
	}
	if videoStatus != "pending" && videoStatus != "published" {
		c.JSON(http.StatusConflict, map[string]string{
			"error": "视频文件正在清理或不可使用，请重新上传",
		})
		return
	}

	// 默认封面属于系统资源；用户上传的封面需要验证上传者及用途。
	if req.CoverURL != "/cover.svg" {
		coverName, valid := strings.CutPrefix(req.CoverURL, "/media/covers/")
		if !valid || coverName == "" || strings.ContainsAny(coverName, `/\?#%`) {
			c.JSON(http.StatusBadRequest, map[string]string{
				"error": "请使用默认封面或本站上传接口返回的封面地址",
			})
			return
		}
		err = tx.QueryRowContext(ctx,
			`SELECT id, status FROM uploads
	 WHERE saved_name = ? AND user_id = ? AND file_type = ?
	 FOR UPDATE`,
			coverName, authorID, "cover",
		).Scan(&coverUploadID, &coverStatus)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusForbidden, map[string]string{
				"error": "封面不存在、不属于当前用户或文件类型不正确",
			})
			return
		}
		if err != nil {
			log.Printf("查询封面归属失败: %v", err)
			c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "检查封面归属失败",
			})
			return
		}
		if coverStatus != "pending" && coverStatus != "published" {
			c.JSON(http.StatusConflict, map[string]string{
				"error": "封面文件正在清理或不可使用，请重新上传",
			})
			return
		}
	}

	// 4、保存视频元数据
	result, err := tx.ExecContext(
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
	_, err = tx.ExecContext(ctx,
		`UPDATE uploads SET status = 'published' WHERE id = ?`,
		videoUploadID,
	)
	if err != nil {
		log.Printf("更新视频上传状态失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "发布视频失败",
		})
		return
	}

	if coverUploadID != 0 {
		_, err = tx.ExecContext(ctx,
			`UPDATE uploads SET status = 'published' WHERE id = ?`,
			coverUploadID,
		)
		if err != nil {
			log.Printf("更新封面上传状态失败: %v", err)
			c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "发布视频失败",
			})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("提交发布事务失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "发布视频失败",
		})
		return
	}

	c.JSON(http.StatusCreated, map[string]interface{}{
		"id":        id,
		"author_id": authorID,
		"title":     req.Title,
	})
}

// 负责传视频文件
func (h *Handler) Upload(ctx context.Context, c *app.RequestContext) {
	//验证上传用户身份
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || userID == 0 || !ok {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请通过file 字段上传文件",
		})
		return
	}
	const maxFileSize = 100 * 1024 * 1024
	if file.Size == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "不能上传空文件",
		})
		return
	}
	if file.Size > maxFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, map[string]string{
			"error": "文件大小不能超过100MiB",
		})
		return
	}
	// 检查文件名后缀
	if !strings.EqualFold(filepath.Ext(file.Filename), ".mp4") {
		c.JSON(http.StatusUnsupportedMediaType, map[string]string{
			"error": "目前只支持MP4文件",
		})
		return
	}
	// 打开上传内容, 读取文件头
	src, err := file.Open()
	if err != nil {
		log.Printf("上传打开文件失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "上传读取文件失败",
		})
		return
	}
	defer src.Close()
	header := make([]byte, 512)
	n, err := io.ReadFull(src, header) //尝试读取 512 字节
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		log.Printf("读取文件头失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "读取上传文件失败",
		})
		return
	}
	// 只检查实际读到的字节
	contentType := http.DetectContentType(header[:n]) //根据内容初步识别类型
	if contentType != "video/mp4" {
		c.JSON(http.StatusUnsupportedMediaType, map[string]string{
			"error": "文件内容未识别为MP4",
		})
		return
	}

	// 文件头检查已经读取了一部分内容，保存前回到开头
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		log.Printf("重置文件读取位置失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "读取上传文件失败",
		})
		return
	}
	savedName, err := saveVideo(src, h.UploadDir)
	if err != nil {
		log.Printf("保存上传文件失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "保存上传文件失败",
		})
		return
	}
	// 视频保存成功，写入上传记录
	_, err = h.DB.ExecContext(
		ctx,
		`INSERT INTO uploads (user_id, saved_name, file_type) VALUES (?, ?, ?)`,
		userID, savedName, "video",
	)
	if err != nil {
		log.Printf("保存上传记录失败: %v", err)
		//数据库写入失败，尝试清除本次刚保存的视频
		path := filepath.Join(h.UploadDir, savedName)
		if removeErr := os.Remove(path); removeErr != nil {
			log.Printf("清理上传视频失败: %v", removeErr)
		}
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "视频上传记录 保存失败",
		})
		return
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"filename":   file.Filename,
		"saved_name": savedName,
		"size":       file.Size,
		"play_url":   "/media/videos/" + savedName,
	})
}
