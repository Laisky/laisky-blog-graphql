package postgres

import (
	"context"
	"database/sql"
	"time"

	errors "github.com/Laisky/errors/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// DB postgres db
type DB struct {
	DB *sql.DB
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

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if err = db.PingContext(ctx); err != nil {
		return nil, errors.Wrap(err, "ping postgres")
	}

	// config db
	db.SetMaxIdleConns(6)
	db.SetMaxOpenConns(50)
	db.SetConnMaxLifetime(time.Hour)

	return &DB{DB: db}, nil
}
