package main

import (
	"gostream/internal/video"
	"log"
	"os"

	"gostream/internal/auth"
	"gostream/internal/database"
	"gostream/internal/user"

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
	)
	userHandler := &user.UserHandler{DB: db, JWTSecret: []byte(jwtSecret)}
	videoHandler := &video.Handler{DB: db}
	h.POST("/users", userHandler.CreateUser)
	h.POST("/login", userHandler.Login)
	h.GET("/me", auth.JWTAuth(userHandler.JWTSecret), userHandler.Me)

	h.POST("/videos", auth.JWTAuth(userHandler.JWTSecret), videoHandler.Publish)
	h.GET("/videos", videoHandler.List)
	h.GET("/videos/:id", videoHandler.GetByID)
	h.DELETE("/videos/:id", auth.JWTAuth(userHandler.JWTSecret), videoHandler.Delete)
	h.Spin()
}
