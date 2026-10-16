package main

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func OpenDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn) //创建数据库连接池对象，不代表连接已经验证成功
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second) //实际检查数据库是否可连接；这里最多等待 3 秒。
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
