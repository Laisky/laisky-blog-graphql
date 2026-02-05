package postgres

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DB postgres db
type DB struct {
	*gorm.DB
}

// DialInfo postgres dial info
type DialInfo struct {
	Addr,
	DBName,
	User,
	Pwd string
}

// NewDB create a new postgres db
func NewDB(ctx context.Context, dialInfo DialInfo) (*DB, error) {
	dsn := "host=" + dialInfo.Addr + " user=" + dialInfo.User + " password=" + dialInfo.Pwd + " dbname=" + dialInfo.DBName + " port=5432 sslmode=disable TimeZone=UTC"

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// config db
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.Wrap(err, "get db")
	}
	sqlDB.SetMaxIdleConns(6)
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetConnMaxLifetime(time.Hour)

	return &DB{DB: db}, nil
}
