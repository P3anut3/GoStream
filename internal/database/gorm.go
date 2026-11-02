package database

import (
	"database/sql"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// WrapSQL reuses a connection pool (also used by isolated driver tests).
// Explicit business transactions stay explicit; single SQL writes need no
// additional implicit transaction. Schema changes remain SQL migrations.
func WrapSQL(pool *sql.DB) *gorm.DB {
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}
	return db
}

func OpenGORM(dsn string) (*gorm.DB, error) {
	pool, err := OpenDB(dsn)
	if err != nil {
		return nil, err
	}
	return WrapSQL(pool), nil
}
