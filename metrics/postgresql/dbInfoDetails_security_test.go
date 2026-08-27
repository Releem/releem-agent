package postgresql

import (
	"io"
	"testing"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	logging "github.com/google/logger"
)

func remoteSuperuserMetrics(users ...models.MetricGroupValue) *models.Metrics {
	metrics := &models.Metrics{}
	metrics.DB.Info = models.MetricGroupValue{"Users": users}
	return metrics
}

func superuser(name string) models.MetricGroupValue {
	return models.MetricGroupValue{"User": name, "rolsuper": true, "rolcanlogin": true}
}

func securityGatherer(t *testing.T) *DBInfoGatherer {
	t.Helper()
	logger := *logging.Init("pg-dbinfo-security-test", false, false, io.Discard)
	return NewDBInfoGatherer(logger, &config.Config{})
}

// TestCollectUsersSecurityCheckUsesSuppliedPgHBA covers the regression where the
// entries were written to DB.Info but read back from DB.Conf.Variables, so the
// check silently never saw them and always reported 0.
func TestCollectUsersSecurityCheckUsesSuppliedPgHBA(t *testing.T) {
	tests := []struct {
		name    string
		pgHBA   []models.MetricGroupValue
		wantHit int
	}{
		{
			name:    "no rules available means nothing can be proven",
			pgHBA:   nil,
			wantHit: 0,
		},
		{
			name: "remote rule for all users flags the superuser",
			pgHBA: []models.MetricGroupValue{
				{"type": "host", "user_name": "all", "address": "0.0.0.0/0"},
			},
			wantHit: 1,
		},
		{
			name: "remote rule naming the superuser flags it",
			pgHBA: []models.MetricGroupValue{
				{"type": "hostssl", "user_name": "releem_live,app", "address": "10.0.0.0/8"},
			},
			wantHit: 1,
		},
		{
			name: "loopback-only rule is not remote access",
			pgHBA: []models.MetricGroupValue{
				{"type": "host", "user_name": "all", "address": "127.0.0.1/32"},
			},
			wantHit: 0,
		},
		{
			name: "local socket rule is not remote access",
			pgHBA: []models.MetricGroupValue{
				{"type": "local", "user_name": "all", "address": ""},
			},
			wantHit: 0,
		},
		{
			name: "rule for a different user does not flag this one",
			pgHBA: []models.MetricGroupValue{
				{"type": "host", "user_name": "someone_else", "address": "0.0.0.0/0"},
			},
			wantHit: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := remoteSuperuserMetrics(superuser("releem_live"))

			checks := securityGatherer(t).collectUsersSecurityCheck(metrics, tt.pgHBA)

			if len(checks) != 1 {
				t.Fatalf("checks = %#v, want exactly one per user", checks)
			}
			if got := checks[0]["Remote_Conn_Superuser"]; got != tt.wantHit {
				t.Fatalf("Remote_Conn_Superuser = %#v, want %d", got, tt.wantHit)
			}
		})
	}
}

// TestCollectUsersSecurityCheckOnlyFlagsLoginSuperusers keeps the finding scoped
// to roles that can actually be used to sign in with superuser rights.
func TestCollectUsersSecurityCheckOnlyFlagsLoginSuperusers(t *testing.T) {
	openToAll := []models.MetricGroupValue{
		{"type": "host", "user_name": "all", "address": "0.0.0.0/0"},
	}
	tests := []struct {
		name    string
		user    models.MetricGroupValue
		wantHit int
	}{
		{name: "login superuser", user: superuser("releem_live"), wantHit: 1},
		{name: "superuser without login", user: models.MetricGroupValue{"User": "svc", "rolsuper": true, "rolcanlogin": false}, wantHit: 0},
		{name: "login without superuser", user: models.MetricGroupValue{"User": "app", "rolsuper": false, "rolcanlogin": true}, wantHit: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := securityGatherer(t).collectUsersSecurityCheck(remoteSuperuserMetrics(tt.user), openToAll)

			if len(checks) != 1 {
				t.Fatalf("checks = %#v, want exactly one per user", checks)
			}
			if got := checks[0]["Remote_Conn_Superuser"]; got != tt.wantHit {
				t.Fatalf("Remote_Conn_Superuser = %#v, want %d", got, tt.wantHit)
			}
		})
	}
}
