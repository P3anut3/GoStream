package user

import (
	"errors"
	"gostream/internal/auth"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

/*
handler.go：解析 HTTP 请求、调用业务层、签发登录凭证和返回响应。
*/
type UserHandler struct {
	JWTSecret    []byte
	Service      *UserService
	Sessions     auth.SessionStore // 登录会话：refresh token 的轮换和退出登录
	SecureCookie bool              // 只通过 HTTPS 发送 Cookie；上线用 HTTPS 时必须为 true
}

// RefreshCookieName 是存放 refresh token 的 Cookie。
const RefreshCookieName = "gostream_refresh"

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
	if h.Service == nil || h.Sessions == nil {
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
	// Service 确认身份后，新建一个登录会话：access token 带上会话 ID，refresh token 放进 HttpOnly Cookie
	sessionID, refreshToken, err := h.Sessions.Create(c.Request.Context(), user.ID)
	if err != nil {
		log.Printf("创建登录会话失败: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "登录服务暂时不可用，请稍后再试",
		})
		return
	}
	token, err := auth.GenerateToken(user.ID, sessionID, h.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "生成登陆凭证失败",
		})
		return
	}
	h.setRefreshCookie(c, refreshToken)
	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
		"token":    token,
	})
}

// Refresh 用 Cookie 里的 refresh token 换一对新凭证；旧的 refresh token 立即作废。
func (h *UserHandler) Refresh(c *gin.Context) {
	if h.Service == nil || h.Sessions == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "用户服务未初始化"})
		return
	}
	refreshToken, err := c.Cookie(RefreshCookieName)
	if err != nil || refreshToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期，请重新登录"})
		return
	}
	ctx := c.Request.Context()
	userID, sessionID, newRefreshToken, err := h.Sessions.Rotate(ctx, refreshToken)
	switch {
	case errors.Is(err, auth.ErrRefreshTokenReused):
		// 旧 token 被用了第二次：可能被偷了。会话已经删掉，这个账号的这次登录全部下线。
		log.Printf("用户的 refresh token 被重复使用，已注销该会话")
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期，请重新登录"})
		return
	case errors.Is(err, auth.ErrSessionNotFound), errors.Is(err, auth.ErrInvalidRefreshToken):
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已过期，请重新登录"})
		return
	case err != nil:
		// 会话存储暂时不可用：不清 Cookie，恢复后还能继续刷新。
		log.Printf("刷新登录会话失败: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "登录服务暂时不可用，请稍后再试"})
		return
	}
	// 旧 token 已经作废，新 token 要马上交给浏览器；就算后面查询失败，浏览器也能拿新 token 重试。
	h.setRefreshCookie(c, newRefreshToken)
	user, err := h.Service.GetCurrentUser(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		if err := h.Sessions.Delete(ctx, sessionID); err != nil {
			log.Printf("删除已注销用户的会话失败: %v", err)
		}
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "当前用户已经不存在"})
		return
	}
	if err != nil {
		log.Printf("刷新时查询用户失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询用户失败"})
		return
	}
	token, err := auth.GenerateToken(user.ID, sessionID, h.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成登陆凭证失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
		"token":    token,
	})
}

// Logout 删除当前会话并清掉 Cookie：这个会话签发过的 access token 立刻全部失效。
// 本机一定会退出；服务端删除失败只记日志，会话会在过期后自然消失。
func (h *UserHandler) Logout(c *gin.Context) {
	if h.Sessions == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "用户服务未初始化"})
		return
	}
	if refreshToken, err := c.Cookie(RefreshCookieName); err == nil {
		if sessionID, ok := auth.SessionIDFromRefreshToken(refreshToken); ok {
			if err := h.Sessions.Delete(c.Request.Context(), sessionID); err != nil {
				log.Printf("退出时删除登录会话失败: %v", err)
			}
		}
	}
	h.clearRefreshCookie(c)
	c.Status(http.StatusNoContent)
}

// setRefreshCookie 写入 refresh token：HttpOnly 让页面脚本读不到；SameSite=Strict 让别的网站发起的请求不带它；
// Path=/auth 让它只发给刷新和退出接口。
func (h *UserHandler) setRefreshCookie(c *gin.Context, value string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: RefreshCookieName, Value: value, Path: "/auth", MaxAge: int(auth.SessionTTL / time.Second),
		HttpOnly: true, Secure: h.SecureCookie, SameSite: http.SameSiteStrictMode,
	})
}

// clearRefreshCookie 让浏览器删掉 refresh token（MaxAge 为负数表示立即过期）。
func (h *UserHandler) clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: RefreshCookieName, Value: "", Path: "/auth", MaxAge: -1,
		HttpOnly: true, Secure: h.SecureCookie, SameSite: http.SameSiteStrictMode,
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
