package main

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 生成token
func GenerateToken(userID uint64, secret []byte) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   strconv.FormatUint(userID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(2 * time.Hour)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// 验证token
func ParseToken(tokenString string, secret []byte) (uint64, error) {
	claims := &jwt.RegisteredClaims{}
	//解析内容，并验证签名和相关声明
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return secret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}), //只接受我们签发时使用的 HS256 算法
		jwt.WithExpirationRequired())
	if err != nil {
		return 0, err
	}
	if !token.Valid {
		return 0, errors.New("无效的 token")
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64) //将 Subject 中的用户 ID 字符串转换回 uint64
	if err != nil || userID == 0 {
		return 0, errors.New("token中额用户ID无效")
	}
	return userID, nil
}
