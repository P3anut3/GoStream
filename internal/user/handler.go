package user

import (
	"errors"
	"gostream/internal/auth"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

/*
handler.go：解析 HTTP 请求、调用业务层、签发登录凭证和返回响应。
*/
type UserHandler struct {
	JWTSecret []byte
	Service   *UserService
}

// CreateUserRequest 接收客户端提交的注册信息，Password 是明文密码。
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type LoginRequest struct { // 登录请求结构体
	Username string `json:"username"`
	Password string `json:"password"`
}

// Handler 通过 Service 执行业务，不直接访问数据库。
func (h *UserHandler) CreateUser(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不是有效的用户JSON"})
		return
	}

	if h.Service == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "用户服务未初始化",
		})
		return
	}
	user, err := h.Service.Register(
		c.Request.Context(),
		req.Username,
		req.Password,
	)
	switch {
	case errors.Is(err, ErrEmptyUsername),
		errors.Is(err, ErrEmptyPassword),
		errors.Is(err, ErrInvalidPasswordLength):
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	case errors.Is(err, ErrUsernameExists):
		c.JSON(http.StatusConflict, gin.H{
			"error": ErrUsernameExists.Error(),
		})
		return
	case err != nil:
		log.Printf("注册失败：%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "注册失败",
		})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":       user.ID,
		"username": user.Username,
	})
}

// 登录
func (h *UserHandler) Login(c *gin.Context) {
	var req LoginRequest
	//绑定
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请求体格式错误",
		})
		return
	}
	if h.Service == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "用户服务未初始化",
		})
		return
	}
	user, err := h.Service.Login(c.Request.Context(), req.Username, req.Password)
	switch {
	case errors.Is(err, ErrEmptyCredentials):
		c.JSON(http.StatusBadRequest, gin.H{
			"error": ErrEmptyCredentials.Error(),
		})
		return

	case errors.Is(err, ErrInvalidCredentials):
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": ErrInvalidCredentials.Error(),
		})
		return

	case err != nil:
		log.Printf("登录失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "查询用户失败",
		})
		return
	}
	// Service 确认身份后，签发登陆凭证
	token, err := auth.GenerateToken(user.ID, h.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "生成登陆凭证失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
		"token":    token,
	})
}

// 读取信息
func (h *UserHandler) Me(c *gin.Context) {
	//已经登陆了
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !ok || userID == 0 || !exists {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "请求中缺少有效的用户身份",
		})
		return
	}
	if h.Service == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "用户服务未初始化",
		})
		return
	}
	user, err := h.Service.GetCurrentUser(c.Request.Context(), userID)
	if errors.Is(err, ErrUserNotFound) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "当前用户已经不存在",
		})
		return
	}
	if err != nil {
		log.Printf("查询当前用户失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "查询用户失败",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
	})
}
