package phase2

import (
	"strings"
	"testing"
)

func TestBackupMethod(t *testing.T) {
	tests := []struct {
		name string
		bm   BackupMethod
		want string
	}{
		{
			name: "none",
			bm:   BackupNone,
			want: "none",
		},
		{
			name: "mysqldump",
			bm:   BackupMysqldump,
			want: "mysqldump",
		},
		{
			name: "xtrabackup",
			bm:   BackupXtrabackup,
			want: "xtrabackup",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.bm) != tt.want {
				t.Errorf("BackupMethod = %v, want %v", tt.bm, tt.want)
			}
		})
	}
}

func TestBuildXtrabackupConnectionArgsSocket(t *testing.T) {
	args := buildXtrabackupConnectionArgs("/var/run/mysqld/mysqld.sock", "3306", "releem", "secret")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--socket=/var/run/mysqld/mysqld.sock") {
		t.Fatalf("xtrabackup socket args missing --socket, got %q", joined)
	}
	if strings.Contains(joined, "--host=") {
		t.Fatalf("xtrabackup socket args must not include --host, got %q", joined)
	}
	if strings.Contains(joined, "--port=") {
		t.Fatalf("xtrabackup socket args must not include --port, got %q", joined)
	}
}

func TestBuildXtrabackupConnectionArgsTCP(t *testing.T) {
	args := buildXtrabackupConnectionArgs("127.0.0.1", "3307", "releem", "secret")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--host=127.0.0.1") {
		t.Fatalf("xtrabackup tcp args missing --host, got %q", joined)
	}
	if !strings.Contains(joined, "--port=3307") {
		t.Fatalf("xtrabackup tcp args missing --port, got %q", joined)
	}
	if strings.Contains(joined, "--socket=") {
		t.Fatalf("xtrabackup tcp args must not include --socket, got %q", joined)
	}
}

func TestRewriteDDLTargetTable(t *testing.T) {
	testTable := "`releem_ddl_test`.`_releem_ddl_test_prerecommend_config_1`"

	tests := []struct {
		name    string
		sql     string
		want    string
		wantErr bool
	}{
		{
			name: "create index with backtick-qualified table and column list",
			sql:  "CREATE INDEX `idx_sid` ON `releemdb`.`prerecommend_config`(`sid`) ALGORITHM=INPLACE LOCK=NONE",
			want: "CREATE INDEX `idx_sid` ON " + testTable + "(`sid`) ALGORITHM=INPLACE LOCK=NONE",
		},
		{
			name: "create index with unqualified table and column list",
			sql:  "CREATE INDEX idx_sid ON prerecommend_config(sid)",
			want: "CREATE INDEX idx_sid ON " + testTable + "(sid)",
		},
		{
			name: "alter table",
			sql:  "ALTER TABLE `releemdb`.`prerecommend_config` ADD INDEX `idx_sid` (`sid`)",
			want: "ALTER TABLE " + testTable + " ADD INDEX `idx_sid` (`sid`)",
		},
		{
			name:    "unsupported ddl",
			sql:     "DROP TABLE `releemdb`.`prerecommend_config`",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rewriteDDLTargetTable(tt.sql, testTable)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("rewriteDDLTargetTable() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("rewriteDDLTargetTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildOnlineDDLSQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "create index already has online clauses",
			sql:  "CREATE INDEX `idx_sid` ON `releemdb`.`prerecommend_config`(`sid`) ALGORITHM=INPLACE LOCK=NONE",
			want: "CREATE INDEX `idx_sid` ON `releemdb`.`prerecommend_config`(`sid`) ALGORITHM=INPLACE LOCK=NONE",
		},
		{
			name: "create index adds online clauses",
			sql:  "CREATE INDEX `idx_sid` ON `releemdb`.`prerecommend_config`(`sid`)",
			want: "CREATE INDEX `idx_sid` ON `releemdb`.`prerecommend_config`(`sid`) ALGORITHM=INPLACE LOCK=NONE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildOnlineDDLSQL(tt.sql)
			if err != nil {
				t.Fatalf("buildOnlineDDLSQL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("buildOnlineDDLSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteDDLTargetTable_EndToEnd(t *testing.T) {
	ddl := "CREATE INDEX `idx_sid` ON `releemdb`.`prerecommend_config`(`sid`)"
	finalSQL, err := buildOnlineDDLSQL(ddl)
	if err != nil {
		t.Fatalf("buildOnlineDDLSQL() error = %v", err)
	}

	testTable := "`releem_ddl_test`.`_releem_ddl_test_prerecommend_config_1`"
	testSQL, err := rewriteDDLTargetTable(finalSQL, testTable)
	if err != nil {
		t.Fatalf("rewriteDDLTargetTable() error = %v", err)
	}

	if !strings.Contains(testSQL, testTable) {
		t.Fatalf("test SQL should target test table, got %q", testSQL)
	}
	if strings.Contains(testSQL, "`releemdb`.`prerecommend_config`") {
		t.Fatalf("test SQL should not reference source table, got %q", testSQL)
	}
}
