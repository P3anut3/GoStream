package user

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type GORMRepository struct {
	DB *gorm.DB
}

func (r *GORMRepository) CreateUser(ctx context.Context, user *User) error {
	err := r.DB.WithContext(ctx).Create(user).Error
	if err == nil {
		return nil
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return ErrUsernameExists
	}
	return fmt.Errorf("保存用户失败: %w", err)
}

func (r *GORMRepository) FindByUsername(ctx context.Context, username string) (User, error) {
	var user User
	//这里读取 password_hash，是为了让下一层 Service 校验密码。Repository 只负责查询，不决定能否登录，也不生成 JWT。
	err := r.DB.WithContext(ctx).Select("id", "username", "password_hash").Where("username = ?", username).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("查询用户失败: %w", err)
	}
	return user, nil
}

func (r *GORMRepository) FindByID(ctx context.Context, userID uint64) (User, error) {
	var user User
	err := r.DB.WithContext(ctx).Select("id", "username").Where("id = ?", userID).Take(&user).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("查询用户失败: %w", err)
	}
	return user, nil
}
