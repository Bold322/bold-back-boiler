// Package testdb centralizes the integration-test database location so every
// RUN_DB_TESTS=1 test reads the same TEST_DATABASE_URL override. The default
// matches docker-compose.yml.
package testdb

import (
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Bold322/bold-back-boiler/internal/config"
)

const defaultDSN = "postgres://app:app_dev_password@localhost:15432/app?sslmode=disable"

// DSN returns TEST_DATABASE_URL or the docker-compose default.
func DSN() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return defaultDSN
}

// PostgresConfig returns DSN() decomposed into a config.PostgresConfig.
func PostgresConfig() config.PostgresConfig {
	u, err := url.Parse(DSN())
	if err != nil {
		panic("testdb: bad TEST_DATABASE_URL: " + err.Error())
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 5432
	}
	pw, _ := u.User.Password()
	return config.PostgresConfig{
		Host:     u.Hostname(),
		Port:     port,
		Database: strings.TrimPrefix(u.Path, "/"),
		User:     u.User.Username(),
		Password: pw,
		SSLMode:  u.Query().Get("sslmode"),
	}
}
