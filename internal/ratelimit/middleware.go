package ratelimit

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// KeyFunc 从请求里取出计数用的身份；ok 为 false 表示取不到。
type KeyFunc func(c *gin.Context) (key string, ok bool)

// ByIP 按客户端 IP 计数，用于还没登录的接口（登录、注册）。
// 路由器不信任任何代理（SetTrustedProxies(nil)），客户端伪造 X-Forwarded-For 也换不了 IP。
func ByIP(c *gin.Context) (string, bool) {
	ip := c.ClientIP()
	return "ip:" + ip, ip != ""
}

// ByUser 按 JWT 中间件放进上下文的用户 ID 计数，所以必须挂在 JWT 中间件后面。
func ByUser(c *gin.Context) (string, bool) {
	value, exists := c.Get("userID")
	userID, ok := value.(uint64)
	if !exists || !ok || userID == 0 {
		return "", false
	}
	return "user:" + strconv.FormatUint(userID, 10), true
}

// Middleware 在业务处理之前限流：超过上限直接返回 429，后面的处理函数（比如用 bcrypt 校验密码）不会执行。
func Middleware(limiter Limiter, rule Rule, keyOf KeyFunc) gin.HandlerFunc {
	// 规则写错要在启动时就失败，而不是上线后悄悄不限流。
	if limiter == nil || keyOf == nil || rule.Name == "" || rule.Limit <= 0 || rule.Window <= 0 {
		panic(fmt.Sprintf("限流规则无效: %+v", rule))
	}
	return func(c *gin.Context) {
		key, ok := keyOf(c)
		if !ok {
			// 取不到身份说明路由装配有误（比如漏了 JWT 中间件），不能当成没有限制。
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "请求中缺少限流所需的身份"})
			return
		}
		decision, err := limiter.Allow(c.Request.Context(), rule, key)
		if err != nil {
			// 连单机计数都失败了（正常不会发生）：放行，限流不能把整个网站拖垮。
			log.Printf("限流失败，本次放行: %v", err)
			c.Next()
			return
		}
		if !decision.Allowed {
			seconds := int(math.Ceil(decision.RetryAfter.Seconds()))
			c.Header("Retry-After", strconv.Itoa(seconds))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": fmt.Sprintf("请求太频繁，请 %d 秒后再试", seconds)})
			return
		}
		c.Next()
	}
}
