package utils

import (
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/lib/pq"
)

func TestPostgresqlConnectionStringQuotesAllValues(t *testing.T) {
	configuration := &config.Config{
		PgHost:     "tenant host",
		PgPort:     "5433",
		PgUser:     "role\\name",
		PgPassword: "pa'ss\\word",
		PgSslMode:  true,
	}

	for _, database := range []string{"tenant db", "tenant'db", `tenant\db`} {
		dsn := postgresqlConnectionString(configuration, database)
		parsed, err := pq.NewConfig(dsn)
		if err != nil {
			t.Fatalf("PostgreSQL DSN for %q should parse: %v (%s)", database, err, dsn)
		}
		if parsed.Database != database {
			t.Fatalf("parsed database = %q, want %q", parsed.Database, database)
		}
		if parsed.Host != configuration.PgHost || parsed.Port != 5433 || parsed.User != configuration.PgUser || parsed.Password != configuration.PgPassword {
			t.Fatalf("PostgreSQL DSN changed connection values: %#v", parsed)
		}
		if parsed.SSLMode != pq.SSLModeRequire {
			t.Fatalf("parsed sslmode = %q, want require", parsed.SSLMode)
		}
	}
}
