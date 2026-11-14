package auth

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// SessionChecker 是中间件需要的能力：会话还在不在。SessionStore 实现了它。
type SessionChecker interface {
	Exists(ctx context.Context, sessionID string) (bool, error)
}

// OptionalJWTAuth 允许不带凭证的游客；提供凭证时必须验证，不能静默降为游客。
func OptionalJWTAuth(secret []byte, sessions SessionChecker) gin.HandlerFunc {
	verify := JWTAuth(secret, sessions)
	return func(c *gin.Context) {
		_, supplied := c.Request.Header["Authorization"]
		if !supplied {
			c.Next()
			return
		}
		verify(c)
	}
}

// 中间件：先验签名和过期时间，再确认 token 所属的会话还在（退出登录后立刻失效）。
// sessions 为 nil 时只验签名，只在不关心会话的测试里这样用。
func JWTAuth(secret []byte, sessions SessionChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 请求头格式： Authorization: Bearer<token>
		auth := string(c.GetHeader("Authorization"))
		parts := strings.Fields(auth)

		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "请提供 Bearer token",
			})
			c.Abort()
			return
		}
		userID, claims, err := parseClaims(parts[1], secret)
		if err != nil {
			c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "登录凭证无效或已过期",
			})
			c.Abort()
			return
		}
		sessionID := claims.SessionID
		if sessions != nil {
			if sessionID == "" {
				// 改版前签发的 token 没有会话 ID，没法撤销，不再接受。
				c.AbortWithStatusJSON(http.StatusUnauthorized, map[string]string{"error": "登录已失效，请重新登录"})
				return
			}
			exists, err := sessions.Exists(c.Request.Context(), sessionID)
			switch {
			case err != nil:
				// 查不了会话（Redis 故障）：只按签名放行。最坏情况是已退出的 token 还能用，但最多 15 分钟。
				log.Printf("检查登录会话失败，本次只按签名放行: %v", err)
			case !exists:
				// 会话已删除（退出登录或检测到 refresh token 被盗用）：这个 token 立刻失效。
				c.AbortWithStatusJSON(http.StatusUnauthorized, map[string]string{"error": "登录已失效，请重新登录"})
				return
			}
		}
		// 将验证后的身份放入本次请求上下文
		c.Set("userID", userID)
		// 推送这种长连接只在建立时验一次 token，所以还要知道会话 ID 和过期时间：
		// 心跳时复查会话，到了过期时间就结束连接。
		c.Set("sessionID", sessionID)
		c.Set("tokenExpiresAt", claims.ExpiresAt.Time)
		//继续执行后续处理函数
		c.Next()

	}
}
