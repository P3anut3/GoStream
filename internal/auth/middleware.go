package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// OptionalJWTAuth 允许不带凭证的游客；提供凭证时必须验证，不能静默降为游客。
func OptionalJWTAuth(secret []byte) gin.HandlerFunc {
	verify := JWTAuth(secret)
	return func(c *gin.Context) {
		_, supplied := c.Request.Header["Authorization"]
		if !supplied {
			c.Next()
			return
		}
		verify(c)
	}
}

// 中间件
func JWTAuth(secret []byte) gin.HandlerFunc {
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
		userID, err := ParseToken(parts[1], secret)
		if err != nil {
			c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "登录凭证无效或已过期",
			})
			c.Abort()
			return
		}
		// 将验证后的身份放入本次请求上下文
		c.Set("userID", userID)
		//继续执行后续处理函数
		c.Next()

	}
}
