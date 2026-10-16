package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// 中间件
func JWTAuth(secret []byte) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
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
		c.Next(ctx)

	}
}
