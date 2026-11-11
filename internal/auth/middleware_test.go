package auth

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	ut "gostream/internal/testutil"
)

func TestJWTAuthWithoutToken(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")
	h := gin.New()

	handlerCalled := false

	h.GET("/me", JWTAuth(secret, nil),
		func(c *gin.Context) {
			handlerCalled = true
			c.Status(http.StatusOK)
		},
	)

	// 模拟请求，没有携带 Authorization 请求头
	recorder := ut.PerformRequest(h, http.MethodGet, "/me", nil)
	response := recorder.Result()

	if response.StatusCode() != http.StatusUnauthorized {
		t.Errorf("期望状态码 401，实际为 %d", response.StatusCode())
	}

	if handlerCalled {
		t.Error("没有 token 时，不应该执行后面的业务函数")
	}
}
func TestJWTAuthWithValidToken(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")

	token, err := GenerateToken(123, "", secret)
	if err != nil {
		t.Fatalf("生成 token 失败：%v", err)
	}

	h := gin.New()
	handlerCalled := false

	h.GET("/me", JWTAuth(secret, nil),
		func(c *gin.Context) {
			handlerCalled = true

			value, exists := c.Get("userID")
			if !exists {
				t.Error("验证通过后，应保存用户 ID")
				return
			}

			userID, ok := value.(uint64)
			if !ok || userID != 123 {
				t.Errorf("期望 uint64 类型的用户 ID 123，实际为 %v", value)
			}

			c.Status(http.StatusOK)
		},
	)

	recorder := ut.PerformRequest(
		h,
		http.MethodGet,
		"/me",
		nil,
		ut.Header{
			Key:   "Authorization",
			Value: "Bearer " + token,
		},
	)

	response := recorder.Result()

	if response.StatusCode() != http.StatusOK {
		t.Errorf("期望状态码 200，实际为 %d", response.StatusCode())
	}

	if !handlerCalled {
		t.Error("有效 token 应允许执行后面的业务函数")
	}
}
