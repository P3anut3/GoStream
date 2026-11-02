package feed

import "errors"

var (
	ErrInvalidLimit  = errors.New("limit 不能为负数")
	ErrInvalidCursor = errors.New("无效的视频流游标")
)
