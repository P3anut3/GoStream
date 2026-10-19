package main

import (
	"gostream/internal/video"
	"log"
	"os"

	"gostream/internal/auth"
	"gostream/internal/database"
	"gostream/internal/user"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/joho/godotenv"
)

/*
main.go：注册路由，指定请求交给谁处理
*/
func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("加载 .env 失败，请检查文件是否存在")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if len(jwtSecret) < 32 {
		log.Fatal("请在 .env 中配置至少 32 字节的随机 JWT_SECRET")
	}

	dsn := os.Getenv("GOSTREAM_DSN")
	if dsn == "" {
		log.Fatal("请设置 GOSTREAM_DSN 环境变量")
	}
	db, err := database.OpenDB(dsn)
	if err != nil {
		log.Fatalf("连接数据库失败:%v ", err)
	}
	log.Println("数据库连接成功")
	defer db.Close()

	h := server.Default(
		server.WithHostPorts("127.0.0.1:8080"),
		server.WithMaxRequestBodySize(101*1024*1024), // 限制请求体大小上限
	)
	userHandler := &user.UserHandler{DB: db, JWTSecret: []byte(jwtSecret)}
	videoHandler := &video.Handler{DB: db, UploadDir: "uploads/videos", CoverDir: "uploads/covers"}
	h.POST("/users", userHandler.CreateUser)
	h.POST("/login", userHandler.Login)
	h.GET("/me", auth.JWTAuth(userHandler.JWTSecret), userHandler.Me)

	h.POST("/videos", auth.JWTAuth(userHandler.JWTSecret), videoHandler.Publish)
	h.GET("/videos", videoHandler.List)
	h.GET("/videos/:id", videoHandler.GetByID)
	h.DELETE("/videos/:id", auth.JWTAuth(userHandler.JWTSecret), videoHandler.Delete)
	h.POST("/uploads/videos", auth.JWTAuth(userHandler.JWTSecret), videoHandler.Upload)
	h.POST("/uploads/covers", auth.JWTAuth(userHandler.JWTSecret), videoHandler.UploadCover)

	h.StaticFile("/", "./web/index.html")
	h.StaticFile("/app.js", "./web/app.js")
	h.StaticFile("/style.css", "./web/style.css")
	h.StaticFile("/cover.svg", "./web/cover.svg")
	h.StaticFS("/media/videos", &app.FS{ //静态文件访问路由 会使用 Hertz 提供的文件处理功能，注册 GET、HEAD 路由。你不需要自己写“打开文件、读取内容、返回响应”的处理函数。
		Root:               videoHandler.UploadDir,        // 从哪个磁盘目录读文件
		PathRewrite:        app.NewPathSlashesStripper(2), //查找文件前，怎样转换请求路径。这里2表示去掉路径中的前面两个部分也就是/media/videos/abc.mp4 里面的 media和 videos，最终结合root去找真实数据
		GenerateIndexPages: false,                         //是否自动展示目录文件列表。
		AcceptByteRange:    true,                          //是否支持读取文件一部分
	})
	h.GET("/me/videos", auth.JWTAuth(userHandler.JWTSecret), videoHandler.ListMine) //查看视频列表
	h.StaticFS("/media/covers", &app.FS{
		Root:               videoHandler.CoverDir,
		PathRewrite:        app.NewPathSlashesStripper(2),
		GenerateIndexPages: false,
	})
	h.PUT("/videos/:id/like", auth.JWTAuth(userHandler.JWTSecret), videoHandler.Like)               // 点赞
	h.GET("/videos/:id/like", auth.JWTAuth(userHandler.JWTSecret), videoHandler.GetLikeStatus)      // 查看视频点赞数
	h.DELETE("/videos/:id/like", auth.JWTAuth(userHandler.JWTSecret), videoHandler.UnLike)          // 取消点赞
	h.POST("/videos/:id/comments", auth.JWTAuth(userHandler.JWTSecret), videoHandler.CreateComment) // 添加评论
	h.GET("/videos/:id/comments", videoHandler.ListComments)
	h.DELETE("/comments/:id", auth.JWTAuth(userHandler.JWTSecret), videoHandler.DeleteComment)

	h.Spin()
}
