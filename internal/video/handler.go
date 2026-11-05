package video

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Handler struct { // 持有数据库连接池，后续提供处理方法
	DB             *gorm.DB
	UploadDir      string
	CoverDir       string
	CommentService *CommentService
	LikeService    *LikeService
	VideoService   *VideoService
}

type PublishRequest struct { // 接收客户端提交的发布信息
	Title    string `json:"title"`
	PlayURL  string `json:"play_url"`
	CoverURL string `json:"cover_url"`
}

// 查看视频
func (h *Handler) GetByID(c *gin.Context) {
	ctx := c.Request.Context()
	// 1、将路径中的字符串转化为视频ID
	id, err := strconv.ParseUint(c.Param("id"), 10, 64) //取得路径参数，类型是字符串。转化为10进制
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "视频ID必须是正整数",
		})
		return
	}
	// 2、查询视频：VideoService 先查缓存，未命中再查数据库
	if h.VideoService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "视频服务未初始化",
		})
		return
	}
	video, err := h.VideoService.GetVideo(ctx, id)
	// 3、区分 “不存在”和“查询失败”
	if errors.Is(err, ErrVideoNotFound) {
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
func (h *Handler) List(c *gin.Context) {
	ctx := c.Request.Context()
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
	videos := make([]Video, 0)
	err = h.DB.WithContext(ctx).Order("id DESC").Limit(pageSize).Offset(offset).Find(&videos).Error
	if err != nil {
		log.Printf("视频列表查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "视频列表查询失败"})
		return
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"page":      page,
		"page_size": pageSize,
		"videos":    videos,
	})
}

// 查看自己的视频列表
func (h *Handler) ListMine(c *gin.Context) {
	ctx := c.Request.Context()
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
	videos := make([]Video, 0)
	err = h.DB.WithContext(ctx).Where("author_id = ?", userID).Order("id DESC").Limit(pageSize).Offset(offset).Find(&videos).Error
	if err != nil {
		log.Printf("视频列表查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "视频列表查询失败"})
		return
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"page":      page,
		"page_size": pageSize,
		"videos":    videos,
	})
}

// 删除视频
func (h *Handler) Delete(c *gin.Context) {
	ctx := c.Request.Context()
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
	// 3、只删除当前用户拥有的视频；删除成功后 VideoService 会让缓存失效
	if h.VideoService == nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "视频服务未初始化"})
		return
	}
	err = h.VideoService.DeleteVideo(ctx, vidoID, userID)
	if errors.Is(err, ErrVideoNotFound) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "视频不存在或不属于当前用户"})
		return
	}
	if err != nil {
		log.Printf("删除视频失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "删除视频失败"})
		return
	}
	c.Status(http.StatusNoContent)
}

// 创建视频作品记录。
func (h *Handler) Publish(c *gin.Context) {
	ctx := c.Request.Context()
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
	if err := c.ShouldBindJSON(&req); err != nil {
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
	// Video metadata and upload states commit together. Locking reads
	// coordinate with ClaimExpired, which locks these same upload rows.
	tx := h.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		log.Printf("开启发布事务失败: %v", tx.Error)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "发布视频失败"})
		return
	}
	defer tx.Rollback()
	var videoUpload Upload
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").Where("saved_name = ? AND user_id = ? AND file_type = ?", savedName, authorID, "video").Take(&videoUpload).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusForbidden, map[string]string{"error": "上传文件不存在或不属于当前用户"})
		return
	}
	if err != nil {
		log.Printf("查询上传文件归属失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "检查上传文件归属失败"})
		return
	}
	if videoUpload.Status != "pending" && videoUpload.Status != "published" {
		c.JSON(http.StatusConflict, map[string]string{"error": "视频文件正在清理或不可使用，请重新上传"})
		return
	}
	var coverUpload Upload
	if req.CoverURL != "/cover.svg" {
		coverName, valid := strings.CutPrefix(req.CoverURL, "/media/covers/")
		if !valid || coverName == "" || strings.ContainsAny(coverName, `/\?#%`) {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "请使用默认封面或本站上传接口返回的封面地址"})
			return
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").Where("saved_name = ? AND user_id = ? AND file_type = ?", coverName, authorID, "cover").Take(&coverUpload).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusForbidden, map[string]string{"error": "封面不存在、不属于当前用户或文件类型不正确"})
			return
		}
		if err != nil {
			log.Printf("查询封面归属失败: %v", err)
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "检查封面归属失败"})
			return
		}
		if coverUpload.Status != "pending" && coverUpload.Status != "published" {
			c.JSON(http.StatusConflict, map[string]string{"error": "封面文件正在清理或不可使用，请重新上传"})
			return
		}
	}
	item := Video{AuthorID: authorID, Title: req.Title, PlayURL: req.PlayURL, CoverURL: req.CoverURL}
	if err := tx.Create(&item).Error; err != nil {
		log.Printf("保存视频失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "保存视频失败"})
		return
	}
	ids := []uint64{videoUpload.ID}
	if coverUpload.ID != 0 {
		ids = append(ids, coverUpload.ID)
	}
	for _, id := range ids {
		if err := tx.Model(&Upload{}).Where("id = ?", id).Update("status", "published").Error; err != nil {
			log.Printf("更新上传状态失败: %v", err)
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "发布视频失败"})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("提交发布事务失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "发布视频失败"})
		return
	}
	// 发布成功后清掉这个 ID 可能残留的"不存在"标记，让新作品立刻可见。
	if h.VideoService != nil {
		h.VideoService.Forget(ctx, item.ID)
	}
	c.JSON(http.StatusCreated, map[string]interface{}{"id": item.ID, "author_id": authorID, "title": req.Title})
}

// 负责传视频文件
func (h *Handler) Upload(c *gin.Context) {
	ctx := c.Request.Context()
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大"})
			return
		}
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
	record := Upload{UserID: userID, SavedName: savedName, FileType: "video"}
	err = h.DB.WithContext(ctx).Select("UserID", "SavedName", "FileType").Create(&record).Error
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
