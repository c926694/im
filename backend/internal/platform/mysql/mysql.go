package mysql

import (
	"database/sql"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func Open(dsn string) (*sqlx.DB, error) {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	config.ParseTime = true
	config.Timeout = 3 * time.Second
	config.Loc = time.UTC
	config.MultiStatements = true
	if config.Params == nil {
		config.Params = make(map[string]string)
	}
	config.Params["time_zone"] = "'+00:00'"
	connector, err := mysqldriver.NewConnector(config)
	if err != nil {
		return nil, err
	}
	db := sqlx.NewDb(sql.OpenDB(connector), "mysql")
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(16)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}
