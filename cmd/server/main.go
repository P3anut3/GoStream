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
	router := newRouter(db, secret, feedCursor, cfg.Storage, redisClient)
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

// openRedis 创建可选的缓存客户端：未配置时返回 nil；连不上只记录日志，请求会直接读 MySQL。
func openRedis(cfg config.RedisConfig) *redis.Client {
	if cfg.Addr == "" {
		log.Printf("未配置 Redis，视频缓存已关闭")
		return nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		// 缓存不能比直接查数据库更慢：超时要短，失败不重试，尽快降级。
		DialTimeout:  time.Second,
		ReadTimeout:  300 * time.Millisecond,
		WriteTimeout: 300 * time.Millisecond,
		MaxRetries:   -1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Printf("Redis 暂不可用，视频详情将直接查询数据库: %v", err)
	} else {
		log.Printf("已连接 Redis 缓存 %s", cfg.Addr)
	}
	return client
}

func newRouter(db *gorm.DB, secret []byte, feedCursor *feed.CursorCodec, storage config.StorageConfig, redisClient *redis.Client) *gin.Engine {
	router := gin.Default()
	router.SetTrustedProxies(nil)
	router.MaxMultipartMemory = 8 * 1024 * 1024
	router.Use(httpx.BodyLimit(101 * 1024 * 1024))

	userRepository := &user.GORMRepository{DB: db}
	userService := user.NewUserService(userRepository)
	users := &user.UserHandler{
		JWTSecret: secret,
		Service:   userService,
	}
	// 没有 Redis 时必须传"无类型的 nil"接口；把 nil 指针装进接口会让 cache != nil 成立。
	var videoCache video.VideoCache
	if redisClient != nil {
		videoCache = &video.RedisVideoCache{Client: redisClient, TTL: 10 * time.Minute, Jitter: time.Minute}
	}
	videos := &video.Handler{DB: db, UploadDir: storage.VideoDir, CoverDir: storage.CoverDir,
		CommentService: video.NewCommentService(&video.SQLCommentRepository{DB: db}),
		LikeService:    video.NewLikeService(&video.SQLLikeRepository{DB: db}),
		VideoService:   video.NewVideoService(&video.GORMVideoRepository{DB: db}, videoCache),
	}
	feedRepository := &feed.GORMRepository{DB: db}
	feedService := feed.NewService(feedRepository, feedCursor)
	feedHandler := feed.NewHandler(feedService)
	followRepository := &follow.GORMRepository{DB: db}
	followService := follow.NewService(followRepository)
	followHandler := follow.NewHandler(followService)
	jwt := auth.JWTAuth(secret)
	router.GET("/healthz", health.Live)
	router.GET("/readyz", health.Ready(db))
	router.GET("/feed", auth.OptionalJWTAuth(secret), feedHandler.ListLatest)
	router.GET("/feed/following", jwt, feedHandler.ListFollowing)
	router.POST("/users", users.CreateUser)
	router.POST("/login", users.Login)
	router.GET("/me", jwt, users.Me)
	router.POST("/videos", jwt, videos.Publish)
	router.GET("/videos", videos.List)
	router.GET("/videos/:id", videos.GetByID)
	router.GET("/me/videos", jwt, videos.ListMine)
	router.DELETE("/videos/:id", jwt, videos.Delete)
	router.POST("/uploads/videos", jwt, videos.Upload)
	router.POST("/uploads/covers", jwt, videos.UploadCover)
	router.PUT("/videos/:id/like", jwt, videos.Like)
	router.GET("/videos/:id/like", jwt, videos.GetLikeStatus)
	router.DELETE("/videos/:id/like", jwt, videos.UnLike)
	router.POST("/videos/:id/comments", jwt, videos.CreateComment)
	router.GET("/videos/:id/comments", videos.ListComments)
	router.DELETE("/comments/:id", jwt, videos.DeleteComment)
	router.PUT("/users/:id/follow", jwt, followHandler.Follow)
	router.GET("/users/:id/follow", jwt, followHandler.GetStatus)
	router.DELETE("/users/:id/follow", jwt, followHandler.Unfollow)
	httpx.RegisterWeb(router, "web/dist")
	httpx.RegisterMedia(router, "/media/videos", videos.UploadDir)
	httpx.RegisterMedia(router, "/media/covers", videos.CoverDir)
	return router
}
