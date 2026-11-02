package follow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// GORMRepository 只负责读写 follows 表，并把数据库错误翻译成业务错误
type GORMRepository struct {
	DB *gorm.DB
}

// Follow 在目标用户存在时插入一条 followerID -> followeeID 的关系
func (r *GORMRepository) Follow(ctx context.Context, followerID, followeeID uint64) error {
	info := gorm.WithResult()
	result := r.DB.WithContext(ctx).Clauses(info).Exec(
		`INSERT INTO follows (follower_id, followee_id) SELECT ?, id FROM users WHERE id = ?`,
		followerID, followeeID)
	if result.Error != nil {
		var mysqlErr *mysql.MySQLError
		// 1062: 主键发生冲突，说明这条关系已经存在了
		if errors.As(result.Error, &mysqlErr) && mysqlErr.Number == 1062 {
			return ErrAlreadyFollowing
		}
		return fmt.Errorf("插入关注关系失败: %w", result.Error)
	}
	affected, err := info.Result.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取关注结果失败: %w", err)
	}
	// SELECT 没查到目标用户，就不会插入任何行
	if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// 取关
func (r *GORMRepository) Unfollow(ctx context.Context, followerID, followeeID uint64) error {
	result := r.DB.WithContext(ctx).Table("follows").Where("follower_id = ? AND followee_id = ?", followerID, followeeID).Delete(nil)
	if result.Error != nil {
		return fmt.Errorf("删除关注记录失败: %w", result.Error)
	}
	return nil
}

func (r *GORMRepository) IsFollowing(ctx context.Context, followerID, followeeID uint64) (bool, error) {
	var status bool
	err := r.DB.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM follows WHERE follower_id = ? AND followee_id = u.id)
	FROM users AS u WHERE u.id = ?`, followerID, followeeID).Row().Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrUserNotFound
	}
	if err != nil {
		return false, fmt.Errorf("查询关注状态失败: %w", err)
	}
	return status, nil
}
