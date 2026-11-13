package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// Event 是发件箱里一条待发送的事件。
type Event struct {
	ID        uint64
	Type      string `gorm:"column:event_type"`
	Payload   []byte
	CreatedAt time.Time
}

// Publisher 把一批事件发到消息队列，返回对方确认收到的事件 ID。
// 没确认的事件不在返回值里；err 不为 nil 表示这一批里有没发出去的。
type Publisher interface {
	Publish(ctx context.Context, events []Event) (confirmed []uint64, err error)
}

// Relay 是投递员：把发件箱里还没发送的事件发出去，对方确认收到后再标记为已发送。
// 先发送、后标记：中途出错最多重复发送，不会丢事件（至少一次）。
type Relay struct {
	DB           *gorm.DB
	Publisher    Publisher
	BatchSize    int           // 每批最多领多少条
	PollInterval time.Duration // 发件箱空了以后，隔多久再查一次
	MaxBackoff   time.Duration // 连续失败时，最长等多久再试
	Retention    time.Duration // 已发送的事件保留多久
}

// NewRelay 返回默认设置的投递员：每批 100 条，每秒查一次，失败最多等 30 秒再试，已发送的事件保留 7 天。
func NewRelay(db *gorm.DB, publisher Publisher) *Relay {
	return &Relay{
		DB:           db,
		Publisher:    publisher,
		BatchSize:    100,
		PollInterval: time.Second,
		MaxBackoff:   30 * time.Second,
		Retention:    7 * 24 * time.Hour,
	}
}

// RelayOnce 领一批待发送的事件发出去，返回确认发出的条数。
// 整个过程在一个“读已提交”的事务里：领到的行被 FOR UPDATE 锁住，别的投递员用 SKIP LOCKED 跳过它们；
// 读已提交不加间隙锁，点赞、评论往发件箱插入新事件时不会被挡住。
func (r *Relay) RelayOnce(ctx context.Context) (int, error) {
	sent := 0
	var publishErr error
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []Event
		if err := tx.Raw(`SELECT id, event_type, payload, created_at FROM outbox_events
			WHERE published_at IS NULL ORDER BY id LIMIT ? FOR UPDATE SKIP LOCKED`, r.BatchSize).Scan(&events).Error; err != nil {
			return fmt.Errorf("领取待发送事件失败: %w", err)
		}
		if len(events) == 0 {
			return nil
		}
		// 先发送，确认收到以后再标记：标记之前出错，最多导致下次重复发送。
		confirmed, err := r.Publisher.Publish(ctx, events)
		publishErr = err
		if len(confirmed) == 0 {
			return nil
		}
		if err := tx.Exec(`UPDATE outbox_events SET published_at = NOW() WHERE id IN ?`, confirmed).Error; err != nil {
			return fmt.Errorf("标记已发送失败: %w", err)
		}
		sent = len(confirmed)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	if publishErr != nil {
		return sent, fmt.Errorf("发送事件失败: %w", publishErr)
	}
	return sent, nil
}

// Run 一直投递，直到 ctx 被取消（收到停止信号）才返回。
// 有失败就退避：等 1、2、4……秒再试，最长 MaxBackoff；成功一次就恢复正常节奏。每小时清理一次旧事件。
func (r *Relay) Run(ctx context.Context) {
	backoff := time.Second
	var lastCleanup time.Time
	for {
		if time.Since(lastCleanup) >= time.Hour {
			if deleted, err := r.DeletePublished(ctx); err != nil {
				log.Printf("清理已发送事件失败: %v", err)
			} else if deleted > 0 {
				log.Printf("清理了 %d 条已发送的事件", deleted)
			}
			lastCleanup = time.Now()
		}
		sent, err := r.RelayOnce(ctx)
		if ctx.Err() != nil {
			return // 收到停止信号：还没标记的事件已随事务回滚，下次启动再发
		}
		if sent > 0 {
			log.Printf("已发送 %d 条事件", sent)
		}
		wait := r.PollInterval
		switch {
		case err != nil:
			log.Printf("%v，%v 后重试", err, backoff)
			wait = backoff
			backoff = min(backoff*2, r.MaxBackoff)
		case sent == r.BatchSize:
			wait = 0 // 刚好发满一批，后面可能还有，马上再领
			backoff = time.Second
		default:
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// DeletePublished 删除发出时间早于 Retention 的事件，返回一共删了多少行。
// 每次最多删 1000 行，删到不足 1000 行为止：一条 DELETE 删太多行，会长时间锁住大量数据，事务日志也会很大。
// 时间用 MySQL 的 NOW() 计算，和标记已发送时用的是同一个时钟。
func (r *Relay) DeletePublished(ctx context.Context) (int64, error) {
	var total int64
	for {
		result := r.DB.WithContext(ctx).Exec(`DELETE FROM outbox_events
			WHERE published_at < NOW() - INTERVAL ? SECOND LIMIT 1000`, int64(r.Retention/time.Second))
		if result.Error != nil {
			return total, fmt.Errorf("清理已发送事件失败: %w", result.Error)
		}
		total += result.RowsAffected
		if result.RowsAffected < 1000 {
			return total, nil
		}
	}
}
