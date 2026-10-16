package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestToken(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")

	token, err := GenerateToken(123, secret)
	if err != nil {
		t.Fatalf("生成 token 失败：%v", err)
	}

	t.Run("正确密钥验证成功", func(t *testing.T) {
		userID, err := ParseToken(token, secret)
		if err != nil {
			t.Fatalf("验证 token 失败：%v", err)
		}

		if userID != 123 {
			t.Errorf("期望用户 ID 为 123，实际为 %d", userID)
		}
	})

	t.Run("错误密钥验证失败", func(t *testing.T) {
		wrongSecret := []byte("another-test-secret-key-at-least-32-bytes")

		if _, err := ParseToken(token, wrongSecret); err == nil {
			t.Error("使用错误密钥不应该验证成功")
		}
	})
}
func TestExpiredToken(t *testing.T) {
	secret := []byte("test-only-secret-key-at-least-32-bytes")

	// 主动构造一个已经过期的 token
	claims := jwt.RegisteredClaims{
		Subject:   "123",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("生成测试 token 失败：%v", err)
	}

	// 签名正确，但已经过期，仍然必须拒绝
	_, err = ParseToken(tokenString, secret)
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("期望过期错误，实际得到：%v", err)
	}
}
