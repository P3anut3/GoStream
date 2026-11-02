package user

import (
	"context"
	"errors"
	"fmt"
)

type UserRepository interface {
	CreateUser(ctx context.Context, user *User) error
	FindByUsername(ctx context.Context, username string) (User, error)
	FindByID(ctx context.Context, userID uint64) (User, error)
}

type UserService struct {
	repository UserRepository
}

func NewUserService(repository UserRepository) *UserService {
	return &UserService{
		repository: repository,
	}
}

func (s *UserService) Register(ctx context.Context, username, password string) (User, error) {
	if username == "" {
		return User{}, ErrEmptyUsername
	}
	if password == "" {
		return User{}, ErrEmptyPassword
	}
	if len(password) < 8 || len(password) > 72 {
		return User{}, ErrInvalidPasswordLength
	}
	passwordHash, err := HashPassword(password)
	if err != nil {
		return User{}, fmt.Errorf("处理密码失败: %w", err)
	}
	user := User{
		Username:     username,
		PasswordHash: passwordHash,
	}
	if err := s.repository.CreateUser(ctx, &user); err != nil {
		return User{}, err
	}

	return user, nil
}

func (s *UserService) Login(ctx context.Context, username, password string) (User, error) {
	if username == "" || password == "" {
		return User{}, ErrEmptyCredentials
	}
	user, err := s.repository.FindByUsername(ctx, username)
	if errors.Is(err, ErrUserNotFound) {
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if err := CheckPassword(user.PasswordHash, password); err != nil {
		return User{}, ErrInvalidCredentials
	}
	return user, nil
}

func (s *UserService) GetCurrentUser(ctx context.Context, userID uint64) (User, error) {
	return s.repository.FindByID(ctx, userID)
}
