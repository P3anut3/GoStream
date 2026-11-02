package follow

import "errors"

var (
	ErrUserNotFound     = errors.New("用户不存在")
	ErrAlreadyFollowing = errors.New("已经关注")
	ErrCannotFollowSelf = errors.New("不能关注自己")
)
