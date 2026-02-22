package postgres

import (
	"context"
	"time"

	errors "github.com/Laisky/errors/v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
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

// BuildDSN builds a PostgreSQL DSN for shared database clients.
func BuildDSN(dialInfo DialInfo) string {
	return "host=" + dialInfo.Addr + " user=" + dialInfo.User + " password=" + dialInfo.Pwd + " dbname=" + dialInfo.DBName + " port=5432 sslmode=disable TimeZone=UTC"
}

// NewDB create a new postgres db
func NewDB(ctx context.Context, dialInfo DialInfo) (*DB, error) {
	dsn := BuildDSN(dialInfo)

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: newTruncatingParamsLogger(gormLogger.Default.LogMode(gormLogger.Warn)),
	})
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
