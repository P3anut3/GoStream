package user

import "errors"

var (
	ErrUsernameExists        = errors.New("用户名已经存在")
	ErrEmptyUsername         = errors.New("username 不能为空")
	ErrEmptyPassword         = errors.New("password 不能为空")
	ErrInvalidPasswordLength = errors.New("密码长度必须为 8～72 字节")
	ErrUserNotFound          = errors.New("用户不存在")
	ErrEmptyCredentials      = errors.New("用户名和密码不能为空")
	ErrInvalidCredentials    = errors.New("用户名或密码错误")
)
