package user

type User struct { //表示准备存储的用户	PasswordHash：密码哈希
	ID           uint64
	Username     string
	PasswordHash string
}
