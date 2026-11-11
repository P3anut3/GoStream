package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/redis/go-redis/v9"
)

// SessionTTL 是一次登录最多能闲置多久：每次刷新重新计时，7 天不用就要重新登录。
const SessionTTL = 7 * 24 * time.Hour

var (
	ErrSessionNotFound     = errors.New("登录会话不存在或已过期")
	ErrInvalidRefreshToken = errors.New("refresh token 无效")
	// ErrRefreshTokenReused 表示已经换掉的旧 refresh token 又被拿来用了：很可能被偷了，整个会话已删除。
	ErrRefreshTokenReused = errors.New("refresh token 被重复使用，会话已注销")
)

// SessionStore 保存登录会话；RedisSessionStore 和 MemorySessionStore 是它的两种实现。
type SessionStore interface {
	// Create 为用户新建会话，返回会话 ID 和交给浏览器的 refresh token。
	Create(ctx context.Context, userID uint64) (sessionID, refreshToken string, err error)
	// Rotate 校验 refresh token 并换一个新的，旧的立即作废。
	Rotate(ctx context.Context, refreshToken string) (userID uint64, sessionID, newRefreshToken string, err error)
	// Exists 判断会话还在不在；中间件对每个请求调用。
	Exists(ctx context.Context, sessionID string) (bool, error)
	// Delete 删除会话（退出登录）；这个会话签发过的 access token 随之全部失效。
	Delete(ctx context.Context, sessionID string) error
}

// newSessionID 用 16 字节随机数生成会话 ID（32 个十六进制字符）。
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成会话 ID 失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// newRefreshSecret 生成 32 字节随机密钥；refresh token 的格式是"会话ID.密钥"。
func newRefreshSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成 refresh token 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret 会话里只存密钥的 SHA-256：存储泄露时，拿到的哈希也没法直接用来刷新。
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// splitRefreshToken 把"会话ID.密钥"拆开并检查格式。
func splitRefreshToken(token string) (sessionID, secret string, err error) {
	sessionID, secret, found := strings.Cut(token, ".")
	if !found || len(sessionID) != 32 || secret == "" {
		return "", "", ErrInvalidRefreshToken
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		return "", "", ErrInvalidRefreshToken
	}
	return sessionID, secret, nil
}

// SessionIDFromRefreshToken 从 refresh token 里取出会话 ID，退出登录时用。
func SessionIDFromRefreshToken(token string) (string, bool) {
	sessionID, _, err := splitRefreshToken(token)
	return sessionID, err == nil
}

// RedisSessionStore 把会话存在 Redis 哈希里，所有 API 实例共用；Compose 里的 Redis 开了 AOF，重启后会话还在。
type RedisSessionStore struct {
	Client *redis.Client
}

func sessionKey(sessionID string) string { return "gostream:auth:v1:session:" + sessionID }

func (s *RedisSessionStore) Create(ctx context.Context, userID uint64) (string, string, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return "", "", err
	}
	secret, err := newRefreshSecret()
	if err != nil {
		return "", "", err
	}
	key := sessionKey(sessionID)
	// 写入和设置过期放进同一个 MULTI：不会留下永不过期的会话。
	_, err = s.Client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, "user", userID, "refresh", hashSecret(secret), "previous", "")
		pipe.Expire(ctx, key, SessionTTL)
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("创建登录会话失败: %w", err)
	}
	return sessionID, sessionID + "." + secret, nil
}

// rotateScript 在 Redis 里一次完成"比对、换新"。整段 Lua 脚本原子执行，中间插不进别的命令，
// 两个同时到达的刷新不会都成功。返回 {"rotated", 用户ID}、{"reused"}、{"invalid"} 或 {"missing"}。
var rotateScript = redis.NewScript(`
local session = redis.call('HMGET', KEYS[1], 'user', 'refresh', 'previous')
if not session[1] then
  return {'missing'}
end
if session[2] == ARGV[1] then
  redis.call('HSET', KEYS[1], 'refresh', ARGV[2], 'previous', ARGV[1])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return {'rotated', session[1]}
end
if session[3] == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return {'reused'}
end
return {'invalid'}
`)

func (s *RedisSessionStore) Rotate(ctx context.Context, refreshToken string) (uint64, string, string, error) {
	sessionID, secret, err := splitRefreshToken(refreshToken)
	if err != nil {
		return 0, "", "", err
	}
	next, err := newRefreshSecret()
	if err != nil {
		return 0, "", "", err
	}
	result, err := rotateScript.Run(ctx, s.Client, []string{sessionKey(sessionID)},
		hashSecret(secret), hashSecret(next), SessionTTL.Milliseconds()).StringSlice()
	if err != nil {
		return 0, "", "", fmt.Errorf("刷新登录会话失败: %w", err)
	}
	switch result[0] {
	case "rotated":
		userID, err := strconv.ParseUint(result[1], 10, 64)
		if err != nil {
			return 0, "", "", fmt.Errorf("会话里的用户 ID 无效: %w", err)
		}
		return userID, sessionID, sessionID + "." + next, nil
	case "reused":
		return 0, "", "", ErrRefreshTokenReused
	case "missing":
		return 0, "", "", ErrSessionNotFound
	default:
		return 0, "", "", ErrInvalidRefreshToken
	}
}

func (s *RedisSessionStore) Exists(ctx context.Context, sessionID string) (bool, error) {
	n, err := s.Client.Exists(ctx, sessionKey(sessionID)).Result()
	if err != nil {
		return false, fmt.Errorf("查询登录会话失败: %w", err)
	}
	return n == 1, nil
}

func (s *RedisSessionStore) Delete(ctx context.Context, sessionID string) error {
	if err := s.Client.Del(ctx, sessionKey(sessionID)).Err(); err != nil {
		return fmt.Errorf("删除登录会话失败: %w", err)
	}
	return nil
}

// MemorySessionStore 把会话存在本进程内存里（go-cache），只用于没有配置 Redis 的开发和测试：
// API 一重启所有人都要重新登录，也不能部署多个实例。
type MemorySessionStore struct {
	mu       sync.Mutex
	sessions *gocache.Cache
}

type memorySession struct {
	userID            uint64
	refresh, previous string
}

func NewMemorySessionStore() *MemorySessionStore {
	// 每个会话默认 SessionTTL 后过期；后台每小时清理一次过期的会话。
	return &MemorySessionStore{sessions: gocache.New(SessionTTL, time.Hour)}
}

func (s *MemorySessionStore) Create(_ context.Context, userID uint64) (string, string, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return "", "", err
	}
	secret, err := newRefreshSecret()
	if err != nil {
		return "", "", err
	}
	s.sessions.SetDefault(sessionID, memorySession{userID: userID, refresh: hashSecret(secret)})
	return sessionID, sessionID + "." + secret, nil
}

func (s *MemorySessionStore) Rotate(_ context.Context, refreshToken string) (uint64, string, string, error) {
	sessionID, secret, err := splitRefreshToken(refreshToken)
	if err != nil {
		return 0, "", "", err
	}
	next, err := newRefreshSecret()
	if err != nil {
		return 0, "", "", err
	}
	presented := []byte(hashSecret(secret))
	// 加锁让"比对、换新"成为一步，作用和 Redis 里的 Lua 脚本相同。
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.sessions.Get(sessionID)
	if !found {
		return 0, "", "", ErrSessionNotFound
	}
	session := value.(memorySession)
	switch {
	case subtle.ConstantTimeCompare(presented, []byte(session.refresh)) == 1:
		s.sessions.SetDefault(sessionID, memorySession{userID: session.userID, refresh: hashSecret(next), previous: string(presented)})
		return session.userID, sessionID, sessionID + "." + next, nil
	case subtle.ConstantTimeCompare(presented, []byte(session.previous)) == 1:
		s.sessions.Delete(sessionID)
		return 0, "", "", ErrRefreshTokenReused
	default:
		return 0, "", "", ErrInvalidRefreshToken
	}
}

func (s *MemorySessionStore) Exists(_ context.Context, sessionID string) (bool, error) {
	_, found := s.sessions.Get(sessionID)
	return found, nil
}

func (s *MemorySessionStore) Delete(_ context.Context, sessionID string) error {
	// 和 Rotate 用同一把锁：退出不会被同时进行的刷新"写回来"。
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions.Delete(sessionID)
	return nil
}
