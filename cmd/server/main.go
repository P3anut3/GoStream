package main

import (
	"context"
	"errors"
	"gostream/internal/config"
	"gostream/internal/health"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gostream/internal/auth"
	"gostream/internal/database"
	"gostream/internal/feed"
	"gostream/internal/follow"
	"gostream/internal/httpx"
	"gostream/internal/notification"
	"gostream/internal/ratelimit"
	"gostream/internal/user"
	"gostream/internal/video"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func main() {
	// 本地可以使用 .env；没有这个文件时，允许使用进程环境变量
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("加载 .env 失败")
	}
	// 使用 目前实际保存的 YAML路径
	cfg, err := config.Load("internal/config/config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	secret := []byte(cfg.Auth.JWTSecret)

	feedCursor, err := feed.NewCursorCodec([]byte(cfg.Auth.FeedCursorSecret))
	if err != nil {
		log.Fatal("初始化 Feed 游标失败")
	}
	db, err := database.OpenGORM(cfg.Database.DSN)
	if err != nil {
		log.Fatal("连接数据库失败，请检查配置和 MySQL 服务")
	}
	pool, _ := db.DB()
	defer pool.Close()
	redisClient := openRedis(cfg.Redis)
	if redisClient != nil {
		defer redisClient.Close()
	}
	router := newRouter(db, secret, feedCursor, cfg.Storage, redisClient, cfg.Auth.SecureCookie == "true")
	server := &http.Server{Addr: cfg.HTTP.Addr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	log.Printf("GoStream Gin HTTP 服务监听 %s", cfg.HTTP.Addr)
	select {
	case err := <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP 服务停止: %v", err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("关闭 HTTP 服务失败: %v", err)
			server.Close()
		}
	}
}

// openRedis 创建可选的 Redis 客户端（缓存、限流计数和登录会话）：未配置时返回 nil，限流和会话改存本进程内存；
// 连不上只记录日志，之后各功能分别降级：缓存直接读 MySQL，限流单机计数，登录和刷新返回 503。
func openRedis(cfg config.RedisConfig) *redis.Client {
	if cfg.Addr == "" {
		log.Printf("未配置 Redis：缓存关闭，限流计数和登录会话只存在本进程内存里（API 重启后需要重新登录）")
		return nil
	}
	client := newRedisClient(cfg.Addr, cfg.Password)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Printf("Redis 暂不可用：缓存直接读 MySQL，限流改为单机计数，登录和刷新会返回 503: %v", err)
	} else {
		log.Printf("已连接 Redis %s（缓存、限流计数和登录会话）", cfg.Addr)
	}
	return client
}

// newRedisClient 的配置原则：缓存不能比直接查数据库更慢，超时要短、失败不重试，出问题就尽快降级。
func newRedisClient(addr, password string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DialTimeout:  time.Second,
		ReadTimeout:  300 * time.Millisecond,
		WriteTimeout: 300 * time.Millisecond,
		MaxRetries:   -1, // 命令失败不重试
		// 建连失败也只试一次：默认会试 5 次、每次间隔 100ms，Redis 停掉时每次缓存操作要多等约 0.4 秒。
		// 这个字段 <= 0 都按默认 5 次处理，所以要明确写 1。
		DialerRetries: 1,
	})
}

func newRouter(db *gorm.DB, secret []byte, feedCursor *feed.CursorCodec, storage config.StorageConfig, redisClient *redis.Client, secureCookie bool) *gin.Engine {
	router := gin.Default()
	router.SetTrustedProxies(nil)
	router.MaxMultipartMemory = 8 * 1024 * 1024
	router.Use(httpx.BodyLimit(101 * 1024 * 1024))

	// 登录会话：有 Redis 时存进 Redis（所有实例共用，重启不丢）；没有 Redis 时存在本进程内存（重启后要重新登录）。
	var sessions auth.SessionStore = auth.NewMemorySessionStore()
	if redisClient != nil {
		sessions = &auth.RedisSessionStore{Client: redisClient}
	}
	userRepository := &user.GORMRepository{DB: db}
	userService := user.NewUserService(userRepository)
	users := &user.UserHandler{
		JWTSecret:    secret,
		Service:      userService,
		Sessions:     sessions,
		SecureCookie: secureCookie,
	}
	// 没有 Redis 时必须传"无类型的 nil"接口；把 nil 指针装进接口会让 cache != nil 成立。
	var videoCache video.VideoCache
	var latestTimeline feed.LatestTimeline
	if redisClient != nil {
		// 两层缓存：进程内 5 秒挡住热点，Redis 10 分钟；"不存在"标记只保留 30 秒。
		redisCache := &video.RedisVideoCache{Client: redisClient, TTL: 10 * time.Minute, Jitter: time.Minute, MissingTTL: 30 * time.Second}
		videoCache = video.NewLocalVideoCache(redisCache, 5*time.Second)
		// 首页时间线：缓存最新 500 条，1 分钟过期；作品发布、删除后整条失效。
		latestTimeline = &feed.RedisTimeline{Client: redisClient, Size: 500, TTL: time.Minute}
	}
	feedRepository := &feed.GORMRepository{DB: db}
	feedService := feed.NewService(feedRepository, feedCursor, latestTimeline)
	feedHandler := feed.NewHandler(feedService)
	videos := &video.Handler{DB: db, UploadDir: storage.VideoDir, CoverDir: storage.CoverDir,
		CommentService: video.NewCommentService(&video.SQLCommentRepository{DB: db}),
		LikeService:    video.NewLikeService(&video.SQLLikeRepository{DB: db}),
		VideoService:   video.NewVideoService(&video.GORMVideoRepository{DB: db}, videoCache),
		LatestFeed:     feedService, // 发布、删除作品后让首页时间线失效
	}
	followRepository := &follow.GORMRepository{DB: db}
	followService := follow.NewService(followRepository)
	followHandler := follow.NewHandler(followService)
	// 通知由 worker 写入，这里只读和标记已读；每个请求只能看到、改动当前登录用户自己的通知。
	notifications := notification.NewHandler(notification.NewService(&notification.GORMRepository{DB: db}))
	// 每个需要登录的请求都要验签名，并确认会话还在：退出登录后，这个会话签发的 access token 立刻失效。
	jwt := auth.JWTAuth(secret, sessions)
	// 限流：有 Redis 时所有实例共用计数，Redis 出错就降级为单机计数；没有 Redis 时只做单机计数。
	var limiter ratelimit.Limiter = ratelimit.NewLocalLimiter()
	if redisClient != nil {
		limiter = &ratelimit.FailoverLimiter{Primary: &ratelimit.RedisLimiter{Client: redisClient}, Fallback: limiter}
	}
	loginLimit := ratelimit.Middleware(limiter, ratelimit.Rule{Name: "login", Limit: 10, Window: time.Minute}, ratelimit.ByIP)
	registerLimit := ratelimit.Middleware(limiter, ratelimit.Rule{Name: "register", Limit: 10, Window: time.Hour}, ratelimit.ByIP)
	// 写操作共用一个额度，并且要排在 jwt 后面：按 jwt 放进上下文的用户 ID 计数。
	writeLimit := ratelimit.Middleware(limiter, ratelimit.Rule{Name: "write", Limit: 60, Window: time.Minute}, ratelimit.ByUser)
	// 正常每个标签页约 15 分钟刷新一次；这个上限只挡住拿 refresh token 来回猜的请求。
	refreshLimit := ratelimit.Middleware(limiter, ratelimit.Rule{Name: "refresh", Limit: 30, Window: time.Minute}, ratelimit.ByIP)
	router.GET("/healthz", health.Live)
	router.GET("/readyz", health.Ready(db))
	router.GET("/feed", auth.OptionalJWTAuth(secret, sessions), feedHandler.ListLatest)
	router.GET("/feed/following", jwt, feedHandler.ListFollowing)
	router.POST("/users", registerLimit, users.CreateUser)
	router.POST("/login", loginLimit, users.Login)
	router.POST("/auth/refresh", refreshLimit, users.Refresh)
	router.POST("/auth/logout", users.Logout)
	router.GET("/me", jwt, users.Me)
	router.POST("/videos", jwt, writeLimit, videos.Publish)
	router.GET("/videos", videos.List)
	router.GET("/videos/:id", videos.GetByID)
	router.GET("/me/videos", jwt, videos.ListMine)
	router.DELETE("/videos/:id", jwt, writeLimit, videos.Delete)
	router.POST("/uploads/videos", jwt, writeLimit, videos.Upload)
	router.POST("/uploads/covers", jwt, writeLimit, videos.UploadCover)
	router.PUT("/videos/:id/like", jwt, writeLimit, videos.Like)
	router.GET("/videos/:id/like", jwt, videos.GetLikeStatus)
	router.DELETE("/videos/:id/like", jwt, writeLimit, videos.UnLike)
	router.POST("/videos/:id/comments", jwt, writeLimit, videos.CreateComment)
	router.GET("/videos/:id/comments", videos.ListComments)
	router.DELETE("/comments/:id", jwt, writeLimit, videos.DeleteComment)
	router.PUT("/users/:id/follow", jwt, writeLimit, followHandler.Follow)
	router.GET("/users/:id/follow", jwt, followHandler.GetStatus)
	router.DELETE("/users/:id/follow", jwt, writeLimit, followHandler.Unfollow)
	router.GET("/notifications", jwt, notifications.List)
	router.GET("/notifications/unread-count", jwt, notifications.UnreadCount)
	router.PUT("/notifications/read", jwt, writeLimit, notifications.MarkAllRead)
	router.PUT("/notifications/:id/read", jwt, writeLimit, notifications.MarkRead)
	httpx.RegisterWeb(router, "web/dist")
	httpx.RegisterMedia(router, "/media/videos", videos.UploadDir)
	httpx.RegisterMedia(router, "/media/covers", videos.CoverDir)
	return router
}
