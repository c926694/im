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
	return sqlx.NewDb(sql.OpenDB(connector), "mysql"), nil
}
