package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessTokenTTL 是 access token 的有效期：要短，泄露了也只能使用这么久；快过期时由前端用 refresh token 换新的
const AccessTokenTTL = 15 * time.Minute

// Claims 是 access token 里的内容：用户 ID 放在标准字段 sub, 所属的登录会话放在 sid。
type Claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// 为某个登录会话生成 access token
func GenerateToken(userID uint64, sessionID string, secret []byte) (string, error) {
	now := time.Now()
	claims := Claims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatUint(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// 验证 token，返回用户 ID 和会话 ID
func ParseToken(tokenString string, secret []byte) (uint64, string, error) {
	claims := &Claims{}
	//解析内容，并验证签名和相关声明
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return secret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}), //只接受我们签发时使用的 HS256 算法
		jwt.WithExpirationRequired())
	if err != nil {
		return 0, "", err
	}
	if !token.Valid {
		return 0, "", errors.New("无效的 token")
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64) //将 Subject 中的用户 ID 字符串转换回 uint64
	if err != nil || userID == 0 {
		return 0, "", errors.New("token 中的用户 ID 无效")
	}
	return userID, claims.SessionID, nil
}
