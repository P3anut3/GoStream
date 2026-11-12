package outbox

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// Add 往发件箱写一条待发送的事件。
// tx 必须是调用方正在用的事务：事件和业务数据一起提交、一起回滚,
// 这样就不会出现“点赞成功了，事件却丢了” 或者 “事件发了，点赞却没有成功”。
func Add(tx *gorm.DB, eventType string, payload any) error {
	data, err := json.Marshal(payload) //把 Go 的值变成 JSON 文本
	if err != nil {
		return fmt.Errorf("事件内容无法编码：%w", err)
	}
	// 用 string 传入：如果 DSN 开了 interpolateParams, []byte 会被拼成二进制字符串,
	// MySQL 的 JSON 列会拒绝(错误 3144); string 在两种设置下都没有问题
	if err := tx.Exec(`INSERT INTO outbox_events (event_type, payload) VALUES (?, ?)`, eventType, string(data)).Error; err != nil {
		return fmt.Errorf("写入发件箱失败: %w", err)
	}
	return nil
}
