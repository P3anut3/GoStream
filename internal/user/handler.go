package user

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"gostream/internal/auth"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/go-sql-driver/mysql"
)

/*
handler.go：收到请求后，校验参数、生成哈希、写数据库、返回响应
*/
type UserHandler struct {
	DB        *sql.DB
	JWTSecret []byte
}

// CreateUserRequest 接收客户端提交的注册信息，Password 是明文密码。
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type LoginRequest struct { // 登录请求结构体
	Username string `json:"username"`
	Password string `json:"password"`
}

// (h *UserHandler) 是接收者，让函数能够通过 h.DB 使用结构体中的数据库连接池。
func (h *UserHandler) CreateUser(ctx context.Context, c *app.RequestContext) {
	var req CreateUserRequest
	// 1. 读取请求中的 JSON
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请求体不是有效的用户JSON",
		})
		return
	}
	// 2. 检查参数
	if req.Username == "" {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "username 不能为空",
		})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "password 不能为空",
		})
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 72 {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "密码长度必须为 8～72 字节",
		})
		return
	}
	// 3. 生成密码哈希
	passwordHash, err := HashPassword(req.Password) // hash处理密码，存储的是 hash密码而不是原密码
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "处理密码失败",
		})
		return
	}
	// 4. 组装用户对象
	user := User{
		Username:     req.Username,
		PasswordHash: passwordHash,
	}
	// 5. 将用户写入数据库
	result, err := h.DB.ExecContext(ctx, "INSERT INTO users (username, password_hash) VALUES (?, ?)", user.Username, user.PasswordHash)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			c.JSON(http.StatusConflict, map[string]string{
				"error": "用户名已经存在",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "保存用户失败",
		})
		return
	}
	//6. 获取数据库生成的用户ID
	id, err := result.LastInsertId()
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "获取用户ID失败",
		})
		return
	}
	user.ID = uint64(id)
	// 7. 返回创建结果
	c.JSON(http.StatusCreated, map[string]interface{}{
		"id":       user.ID,
		"username": user.Username,
	})
}

// 登录
func (h *UserHandler) Login(ctx context.Context, c *app.RequestContext) {
	var req LoginRequest
	//绑定
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "请求体格式错误",
		})
		return
	}
	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "用户名和密码不能为空",
		})
		return
	}
	// 1、根据用户名查询用户
	var user User
	err := h.DB.QueryRowContext(ctx, "SELECT id,username,password_hash FROM users WHERE username = ?", req.Username).Scan(&user.ID, &user.Username, &user.PasswordHash) //它把查询结果按顺序填入变量：

	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "用户名或密码错误",
		})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "查询用户失败",
		})
		return
	}
	//2、查询成功，校验数据库中的哈希密码
	if err := CheckPassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "用户名或密码错误",
		})
		return
	}

	//3、 生成 token
	token, err := auth.GenerateToken(user.ID, h.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "生成登录凭证失败",
		})
		return
	}
	// 返回响应
	c.JSON(http.StatusOK, map[string]interface{}{
		"id":       user.ID,
		"username": user.Username,
		"token":    token,
	})

}

// 验证
func (h *UserHandler) Me(ctx context.Context, c *app.RequestContext) {
	//1、去除中间件验证后的用户ID
	value, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "请求中缺少已验证的用户身份信息",
		})
		return
	}
	userID, ok := value.(uint64) //类型断言
	if !ok {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "用户身份类型错误",
		})
		return
	}
	//2、根据用户ID 查询数据库
	var user User
	err := h.DB.QueryRowContext(ctx, "SELECT id,username FROM users WHERE id = ?", userID).Scan(&user.ID, &user.Username)

	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "当前用户已经不存在",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "查询用户失败",
		})
		return
	}
	// 3、返回公开的用户信息
	c.JSON(http.StatusOK, map[string]interface{}{
		"id":       userID,
		"username": user.Username,
	})
}
