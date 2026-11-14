package notification

import (
	"errors"
	"sync"
)

// MaxStreamsPerUser 是一个用户同时能开的推送连接数：每个打开的标签页一条，不能无限开。
const MaxStreamsPerUser = 5

var (
	ErrTooManyStreams = errors.New("推送连接太多，请关闭一些标签页")
	ErrHubClosed      = errors.New("服务正在关闭")
)

// Hub 记录本进程里每个用户打开的推送连接。收到"用户 X 有新通知"时，提示 X 的每一条连接。
//
// 每条连接有一个只能放 1 个提示的槽（容量为 1 的 channel），放的时候从不等待：
// 槽满说明上一个提示还没处理完，这次直接丢掉也不会漏，因为浏览器每次收到提示都会重新查未读数。
// 这样一条很慢的连接，也拖不住给所有用户转发提示的那个 goroutine。
type Hub struct {
	mu      sync.Mutex
	streams map[uint64]map[*Subscription]struct{} // 用户 ID → 这个用户的连接集合
	closed  bool
}

// Subscription 是一条推送连接在 Hub 里的登记。
type Subscription struct {
	hub    *Hub
	userID uint64
	signal chan struct{}
}

func NewHub() *Hub {
	return &Hub{streams: make(map[uint64]map[*Subscription]struct{})}
}

// Subscribe 为 userID 登记一条新连接。这个用户的连接已经有 MaxStreamsPerUser 条时返回
// ErrTooManyStreams；Hub 关闭以后返回 ErrHubClosed。
func (h *Hub) Subscribe(userID uint64) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	subs := h.streams[userID]
	if len(subs) >= MaxStreamsPerUser {
		return nil, ErrTooManyStreams
	}
	if subs == nil {
		subs = make(map[*Subscription]struct{})
		h.streams[userID] = subs
	}
	s := &Subscription{hub: h, userID: userID, signal: make(chan struct{}, 1)}
	subs[s] = struct{}{}
	return s, nil
}

// Publish 提示 userID 的每一条连接：有新通知了。
func (h *Hub) Publish(userID uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.streams[userID] {
		s.notify()
	}
}

// PublishAll 提示所有连接。Redis 重新订阅成功后调用：断开期间的提示可能丢了，让每个页面都重新查一次。
func (h *Hub) PublishAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, subs := range h.streams {
		for s := range subs {
			s.notify()
		}
	}
}

// Close 关闭所有连接的槽，每条连接看到槽被关闭就结束；之后的 Subscribe 都返回 ErrHubClosed。
// 服务关闭时调用，调用多次也没关系。
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, subs := range h.streams {
		for s := range subs {
			close(s.signal)
		}
	}
	// 清空登记：之后的 Subscription.Close 找不到自己，就不会再动已经关闭的槽。
	h.streams = nil
}

// C 是这条连接的槽：收到值表示有新通知；channel 被关闭表示服务正在关闭，连接应该结束。
func (s *Subscription) C() <-chan struct{} {
	return s.signal
}

// Close 取消这条连接的登记，连接结束时调用（用 defer）。调用多次、在 Hub 关闭之后调用都没关系。
func (s *Subscription) Close() {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.streams[s.userID]
	delete(subs, s)
	// 这个用户已经没有连接了：把整个用户删掉，否则来过的用户越多，map 越大。
	if len(subs) == 0 {
		delete(h.streams, s.userID)
	}
}

// notify 往槽里放一个提示，从不等待：槽满就什么也不做。调用方必须持有 h.mu。
func (s *Subscription) notify() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}
