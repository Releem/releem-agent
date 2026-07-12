package phase2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/go-sql-driver/mysql"
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

func TestSharedDDLExecutionContract(t *testing.T) {
	type contractTarget struct {
		Database string `json:"database"`
		Table    string `json:"table"`
	}
	type contractCase struct {
		ID           string `json:"id"`
		SchemaName   string `json:"schema_name"`
		DDLStatement string `json:"ddl_statement"`
		Expected     struct {
			SyntaxValid bool            `json:"syntax_valid"`
			Target      *contractTarget `json:"target"`
			Flags       struct {
				OKOnlineDDL bool `json:"ok_online_ddl"`
				OKPTOSC     bool `json:"ok_pt_osc"`
			} `json:"flags"`
			RejectionReason *string `json:"rejection_reason"`
		} `json:"expected"`
	}
	var contract struct {
		Version int            `json:"contract_version"`
		Cases   []contractCase `json:"cases"`
	}

	data, err := os.ReadFile(filepath.Join("testdata", "ddl_execution_contract_v1.json"))
	if err != nil {
		t.Fatalf("read shared DDL contract: %v", err)
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("parse shared DDL contract: %v", err)
	}
	if contract.Version != 1 {
		t.Fatalf("contract version = %d, want 1", contract.Version)
	}

	executor := &Executor{}
	for _, tt := range contract.Cases {
		t.Run(tt.ID, func(t *testing.T) {
			if !tt.Expected.SyntaxValid {
				if tt.Expected.RejectionReason == nil {
					t.Fatal("syntax-invalid contract case requires a rejection reason")
				}
				return
			}
			if tt.Expected.Target == nil {
				t.Fatal("syntax-valid contract case requires a target")
			}

			target := TableInfo{
				Database: tt.Expected.Target.Database,
				Table:    tt.Expected.Target.Table,
			}
			qualifiedSQL, resolvedTarget, validationErr := executor.validateDDLTarget(
				tt.DDLStatement,
				"",
				&target,
			)
			if validationErr != nil {
				assertContractRejection(t, validationErr, tt.Expected.RejectionReason)
				return
			}
			if resolvedTarget != target {
				t.Fatalf("resolved target = %#v, want %#v", resolvedTarget, target)
			}

			_, onlineErr := buildOnlineDDLSQL(qualifiedSQL)
			_, ptoscErr := buildPTOSCAlterSQL(qualifiedSQL)
			if tt.Expected.RejectionReason != nil {
				assertContractRejection(t, onlineErr, tt.Expected.RejectionReason)
				assertContractRejection(t, ptoscErr, tt.Expected.RejectionReason)
				return
			}
			if (onlineErr == nil) != tt.Expected.Flags.OKOnlineDDL {
				t.Fatalf("Online DDL error = %v, expected flag %v", onlineErr, tt.Expected.Flags.OKOnlineDDL)
			}
			if (ptoscErr == nil) != tt.Expected.Flags.OKPTOSC {
				t.Fatalf("pt-osc error = %v, expected flag %v", ptoscErr, tt.Expected.Flags.OKPTOSC)
			}
		})
	}
}

func assertContractRejection(t *testing.T, err error, reason *string) {
	t.Helper()
	if reason == nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), *reason) {
		t.Fatalf("rejection error = %v, want reason %q", err, *reason)
	}
}

func TestPrepareBackupDirectoryBeforeUsageCheck(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "missing", "backups")
	if _, err := os.Stat(backupDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory unexpectedly exists before test: %v", err)
	}

	usage, err := prepareBackupFilesystem(backupDir, true)
	if err != nil {
		t.Fatalf("prepareBackupFilesystem() error = %v", err)
	}
	if usage == nil {
		t.Fatal("prepareBackupFilesystem() usage = nil, want filesystem usage")
	}
	info, err := os.Stat(backupDir)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("prepared path is not a directory: %s", backupDir)
	}
}

func TestBuildOnlineDDLTestTableNameIsBoundedAndUnique(t *testing.T) {
	longTableName := strings.Repeat("t", 64)
	first := buildOnlineDDLTestTableName("app", longTableName, 123)
	second := buildOnlineDDLTestTableName("app", longTableName, 124)

	if len(first) > 64 {
		t.Fatalf("test table name length = %d, want <= 64: %q", len(first), first)
	}
	if first == second {
		t.Fatalf("different nonces produced the same test table name: %q", first)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(first) {
		t.Fatalf("test table name contains unsupported characters: %q", first)
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

func TestBuildMysqldumpConnectionArgsSocket(t *testing.T) {
	args := buildMysqldumpConnectionArgs("/var/run/mysqld/mysqld.sock", "3306", "releem", "secret")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--socket=/var/run/mysqld/mysqld.sock") {
		t.Fatalf("mysqldump socket args missing --socket, got %q", joined)
	}
	if strings.Contains(joined, "-h ") || strings.Contains(joined, "-P ") {
		t.Fatalf("mysqldump socket args must not include TCP host or port, got %q", joined)
	}
}

func TestBuildMysqldumpConnectionArgsTCP(t *testing.T) {
	args := buildMysqldumpConnectionArgs("127.0.0.1", "3307", "releem", "secret")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-h 127.0.0.1") || !strings.Contains(joined, "-P 3307") {
		t.Fatalf("mysqldump TCP args missing host or port, got %q", joined)
	}
	if strings.Contains(joined, "--socket=") {
		t.Fatalf("mysqldump TCP args must not include socket, got %q", joined)
	}
}

func TestBuildPTOSCDSNSocket(t *testing.T) {
	dsn := buildPTOSCDSN("/var/run/mysqld/mysqld.sock", "3306", "releem", "secret", "app", "users")
	want := "S=/var/run/mysqld/mysqld.sock,u=releem,p=secret,D=app,t=users"
	if dsn != want {
		t.Fatalf("buildPTOSCDSN() = %q, want %q", dsn, want)
	}
}

func TestBuildPTOSCDSNTCP(t *testing.T) {
	dsn := buildPTOSCDSN("127.0.0.1", "3307", "releem", "secret", "app", "users")
	want := "h=127.0.0.1,P=3307,u=releem,p=secret,D=app,t=users"
	if dsn != want {
		t.Fatalf("buildPTOSCDSN() = %q, want %q", dsn, want)
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
			name: "create index replaces target rather than earlier comment text",
			sql:  "CREATE INDEX idx_sid /* prerecommend_config */ ON prerecommend_config(sid)",
			want: "CREATE INDEX idx_sid /* prerecommend_config */ ON " + testTable + "(sid)",
		},
		{
			name: "create index ignores ON target text inside quoted index name",
			sql:  "CREATE INDEX `idx ON prerecommend_config` ON prerecommend_config(sid)",
			want: "CREATE INDEX `idx ON prerecommend_config` ON " + testTable + "(sid)",
		},
		{
			name: "alter table",
			sql:  "ALTER TABLE `releemdb`.`prerecommend_config` ADD INDEX `idx_sid` (`sid`)",
			want: "ALTER TABLE " + testTable + " ADD INDEX `idx_sid` (`sid`)",
		},
		{
			name: "alter table if exists",
			sql:  "ALTER TABLE IF EXISTS prerecommend_config ADD COLUMN c INT",
			want: "ALTER TABLE IF EXISTS " + testTable + " ADD COLUMN c INT",
		},
		{
			name: "MariaDB online ignore alter table",
			sql:  "ALTER ONLINE IGNORE TABLE prerecommend_config ADD COLUMN c INT",
			want: "ALTER ONLINE IGNORE TABLE " + testTable + " ADD COLUMN c INT",
		},
		{
			name: "unquoted dollar identifier",
			sql:  "ALTER TABLE orders$2026 ADD COLUMN c INT",
			want: "ALTER TABLE " + testTable + " ADD COLUMN c INT",
		},
		{
			name: "unquoted Unicode identifier",
			sql:  "ALTER TABLE заказы ADD COLUMN c INT",
			want: "ALTER TABLE " + testTable + " ADD COLUMN c INT",
		},
		{
			name: "unquoted BMP symbol identifier",
			sql:  "ALTER TABLE orders£ ADD COLUMN c INT",
			want: "ALTER TABLE " + testTable + " ADD COLUMN c INT",
		},
		{
			name: "qualified identifier with spaces around dot",
			sql:  "ALTER TABLE app . users ADD COLUMN c INT",
			want: "ALTER TABLE " + testTable + " ADD COLUMN c INT",
		},
		{
			name: "ANSI quotes qualified identifier",
			sql:  `ALTER TABLE "app"."users" ADD COLUMN c INT`,
			want: "ALTER TABLE " + testTable + " ADD COLUMN c INT",
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
		name    string
		sql     string
		want    string
		wantErr bool
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
		{
			name: "safe clauses allow spaces around equals",
			sql:  "ALTER TABLE users ADD COLUMN c INT, ALGORITHM = INPLACE, LOCK = NONE",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM = INPLACE, LOCK = NONE",
		},
		{
			name: "online clauses are inserted before trailing line comment",
			sql:  "ALTER TABLE users ADD COLUMN c INT -- keep this comment",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE -- keep this comment",
		},
		{
			name: "form feed starts a MySQL line comment",
			sql:  "ALTER TABLE users ADD COLUMN c INT --\fkeep this comment",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE --\fkeep this comment",
		},
		{
			name: "BEL starts a MySQL line comment",
			sql:  "ALTER TABLE users ADD COLUMN c INT --\x07keep this comment",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE --\x07keep this comment",
		},
		{
			name: "terminator after trailing line comment",
			sql:  "ALTER TABLE users ADD COLUMN c INT -- keep this comment\n;",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE -- keep this comment",
		},
		{
			name: "trailing whitespace after terminator is handled",
			sql:  "ALTER TABLE users ADD COLUMN c INT;  ",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE",
		},
		{
			name: "trailing block comment after terminator is handled",
			sql:  "ALTER TABLE users ADD COLUMN c INT; /* keep this comment */",
			want: "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE /* keep this comment */",
		},
		{
			name: "check expression identifier is not treated as online clause",
			sql:  "ALTER TABLE users ADD CONSTRAINT chk CHECK (algorithm = 1)",
			want: "ALTER TABLE users ADD CONSTRAINT chk CHECK (algorithm = 1), ALGORITHM=INPLACE, LOCK=NONE",
		},
		{
			name: "quoted default and trailing comment preserve their contents",
			sql:  "ALTER TABLE users ADD COLUMN note VARCHAR(32) DEFAULT 'semi; -- text' /* ALGORITHM=COPY */",
			want: "ALTER TABLE users ADD COLUMN note VARCHAR(32) DEFAULT 'semi; -- text', ALGORITHM=INPLACE, LOCK=NONE /* ALGORITHM=COPY */",
		},
		{
			name: "unambiguous backslash literal is preserved",
			sql:  `ALTER TABLE users ADD COLUMN path VARCHAR(32) DEFAULT 'C:\tmp'`,
			want: `ALTER TABLE users ADD COLUMN path VARCHAR(32) DEFAULT 'C:\tmp', ALGORITHM=INPLACE, LOCK=NONE`,
		},
		{
			name: "MariaDB alter modifiers add clauses",
			sql:  "ALTER ONLINE IGNORE TABLE IF EXISTS users ADD COLUMN c INT",
			want: "ALTER ONLINE IGNORE TABLE IF EXISTS users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE",
		},
		{
			name: "ANSI quotes qualified target adds clauses",
			sql:  `ALTER TABLE "app"."users" ADD COLUMN c INT`,
			want: `ALTER TABLE "app"."users" ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE`,
		},
		{
			name: "create or replace unique index adds clauses",
			sql:  "CREATE OR REPLACE UNIQUE INDEX ix ON users(c)",
			want: "CREATE OR REPLACE UNIQUE INDEX ix ON users(c) ALGORITHM=INPLACE LOCK=NONE",
		},
		{
			name:    "copy algorithm without equals is rejected",
			sql:     "CREATE INDEX ix ON users(c) ALGORITHM COPY LOCK EXCLUSIVE",
			wantErr: true,
		},
		{
			name:    "blocking lock separated by control byte is rejected",
			sql:     "CREATE INDEX ix ON users(c) ALGORITHM INPLACE LOCK\x07EXCLUSIVE",
			wantErr: true,
		},
		{
			name: "MariaDB nocopy algorithm is accepted",
			sql:  "CREATE INDEX ix ON users(c) ALGORITHM NOCOPY LOCK NONE",
			want: "CREATE INDEX ix ON users(c) ALGORITHM NOCOPY LOCK NONE",
		},
		{
			name:    "copy algorithm is rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=COPY, LOCK=NONE",
			wantErr: true,
		},
		{
			name:    "exclusive lock is rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=EXCLUSIVE",
			wantErr: true,
		},
		{
			name:    "shared lock is rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=SHARED",
			wantErr: true,
		},
		{
			name:    "default algorithm is rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=DEFAULT, LOCK=NONE",
			wantErr: true,
		},
		{
			name:    "unknown assigned algorithm is rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=UNSAFE, LOCK=NONE",
			wantErr: true,
		},
		{
			name:    "duplicate algorithm forms are rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, ALGORITHM NOCOPY, LOCK=NONE",
			wantErr: true,
		},
		{
			name:    "multiple statements are rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT; ALTER TABLE audit ADD COLUMN d INT",
			wantErr: true,
		},
		{
			name:    "MySQL executable comments are rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT /*! , ALGORITHM=COPY, LOCK=EXCLUSIVE */",
			wantErr: true,
		},
		{
			name:    "MariaDB executable comments are rejected",
			sql:     "ALTER TABLE users ADD COLUMN c INT /*M! , ALGORITHM=COPY, LOCK=EXCLUSIVE */",
			wantErr: true,
		},
		{
			name:    "ambiguous backslash quote is rejected",
			sql:     "ALTER TABLE users ADD COLUMN note VARCHAR(32) DEFAULT 'x\\', ALGORITHM=COPY, LOCK=EXCLUSIVE /* ' ALGORITHM=INPLACE LOCK=NONE */",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildOnlineDDLSQL(tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildOnlineDDLSQL() error = nil, want unsafe clause rejection; SQL = %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildOnlineDDLSQL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("buildOnlineDDLSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildPTOSCAlterSQL(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		want    string
		wantErr bool
	}{
		{
			name: "alter table body",
			sql:  "ALTER TABLE app.users ADD COLUMN note VARCHAR(32) DEFAULT 'two  spaces'",
			want: "ADD COLUMN note VARCHAR(32) DEFAULT 'two  spaces'",
		},
		{
			name: "alter table strips native online clauses",
			sql:  "ALTER TABLE app.users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE",
			want: "ADD COLUMN c INT",
		},
		{
			name: "alter table preserves nested and quoted commas",
			sql:  "ALTER TABLE app.users ADD COLUMN amount DECIMAL(10,2), ADD COLUMN note VARCHAR(32) DEFAULT 'a,b', ALGORITHM=INPLACE, LOCK=NONE",
			want: "ADD COLUMN amount DECIMAL(10,2), ADD COLUMN note VARCHAR(32) DEFAULT 'a,b'",
		},
		{
			name:    "alter ignore is not rewritten with changed semantics",
			sql:     "ALTER IGNORE TABLE app.users ADD UNIQUE INDEX idx_email (email)",
			wantErr: true,
		},
		{
			name:    "alter if exists is not rewritten with changed semantics",
			sql:     "ALTER TABLE IF EXISTS app.users ADD COLUMN c INT",
			wantErr: true,
		},
		{
			name: "create index",
			sql:  "CREATE INDEX idx_email ON app.users(email)",
			want: "ADD INDEX idx_email (email)",
		},
		{
			name: "create unique index strips native online clauses",
			sql:  "CREATE UNIQUE INDEX idx_email ON app.users(email) ALGORITHM=COPY LOCK=EXCLUSIVE",
			want: "ADD UNIQUE INDEX idx_email (email)",
		},
		{
			name:    "create or replace is not rewritten with changed semantics",
			sql:     "CREATE OR REPLACE INDEX idx_email ON app.users(email)",
			wantErr: true,
		},
		{
			name:    "create index wait is not relocated",
			sql:     "CREATE INDEX idx_email ON app.users(email) WAIT 5",
			wantErr: true,
		},
		{
			name:    "create index nowait is not relocated",
			sql:     "CREATE INDEX idx_email ON app.users(email) NOWAIT",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildPTOSCAlterSQL(tt.sql)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildPTOSCAlterSQL() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildPTOSCAlterSQL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("buildPTOSCAlterSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsOnlineDDLUnsupported(t *testing.T) {
	_, clientPolicyErr := buildOnlineDDLSQL(
		"ALTER TABLE users ADD COLUMN c INT, ALGORITHM=COPY, LOCK=NONE",
	)
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "client copy policy", err: clientPolicyErr, want: true},
		{name: "mysql 1845", err: &mysql.MySQLError{Number: 1845, Message: "localized"}, want: true},
		{name: "wrapped mysql 1846", err: fmt.Errorf("preflight: %w", &mysql.MySQLError{Number: 1846, Message: "localized"}), want: true},
		{name: "mysql 1848 partition limitation", err: &mysql.MySQLError{Number: 1848, Message: "localized"}, want: true},
		{name: "mysql 1850 column type limitation", err: &mysql.MySQLError{Number: 1850, Message: "localized"}, want: true},
		{name: "mysql 1857 fulltext lock limitation", err: &mysql.MySQLError{Number: 1857, Message: "localized"}, want: true},
		{name: "mysql 1861 not null limitation", err: &mysql.MySQLError{Number: 1861, Message: "localized"}, want: true},
		{name: "mysql 3060 GIS online limitation", err: &mysql.MySQLError{Number: 3060, Message: "localized"}, want: true},
		{name: "mysql 3103 virtual column inplace limitation", err: &mysql.MySQLError{Number: 3103, Message: "localized"}, want: true},
		{name: "mysql 3178 virtual column online limitation", err: &mysql.MySQLError{Number: 3178, Message: "localized"}, want: true},
		{name: "mysql 3187 encryption inplace limitation", err: &mysql.MySQLError{Number: 3187, Message: "localized"}, want: true},
		{name: "mysql 4083 column type instant limitation", err: &mysql.MySQLError{Number: 4083, Message: "localized"}, want: true},
		{name: "mysql 4157 instant row size limitation", err: &mysql.MySQLError{Number: 4157, Message: "localized"}, want: true},
		{name: "mysql 4158 instant field count limitation", err: &mysql.MySQLError{Number: 4158, Message: "localized"}, want: true},
		{name: "outside Online DDL error range", err: &mysql.MySQLError{Number: 1859, Message: "localized"}, want: false},
		{name: "copy wording", err: errors.New("operation unsupported; try algorithm=copy"), want: true},
		{name: "lock wording", err: errors.New("LOCK=NONE is not supported for this operation"), want: true},
		{name: "unrelated error", err: errors.New("duplicate column"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOnlineDDLUnsupported(tt.err); got != tt.want {
				t.Fatalf("isOnlineDDLUnsupported(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestExecuteFallsBackFromCreateIndexPreflightOnPinnedSession(t *testing.T) {
	drv := &recordingSQLDriver{
		failQueryContains: "CREATE INDEX idx_email ON `releem_online_ddl_test`.`_releem_ddl_test_",
		failErr:           &mysql.MySQLError{Number: 1846, Message: "localized unsupported Online DDL"},
	}
	const driverName = "phase2-create-index-fallback"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var calls []externalCommandCall
	executor := &Executor{
		conn: db,
		runCommand: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, externalCommandCall{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}
	result, err := executor.Execute(ExecuteOptions{
		SQL:          "CREATE INDEX idx_email ON app.users(email)",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkOnlineDDL:  true,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks:  true,
			OnlineDDLTestSchema: "releem_online_ddl_test",
			PTOSCPath:           "pt-online-schema-change",
			MysqlHost:           "127.0.0.1",
			MysqlPort:           "3306",
			MysqlUser:           "releem",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.MethodUsed != "pt-online-schema-change" {
		t.Fatalf("Execute() method = %q, want pt-online-schema-change", result.MethodUsed)
	}
	if len(calls) != 2 {
		t.Fatalf("pt-osc calls = %d, want dry-run and execute: %#v", len(calls), calls)
	}
	for i, call := range calls {
		if call.name != "pt-online-schema-change" {
			t.Fatalf("pt-osc call %d command = %q", i, call.name)
		}
		wantMode := "--dry-run"
		if i == 1 {
			wantMode = "--execute"
		}
		if len(call.args) == 0 || call.args[0] != wantMode {
			t.Fatalf("pt-osc call %d args = %#v, want first arg %q", i, call.args, wantMode)
		}
		if got := findArgument(call.args, "--alter="); got != "--alter=ADD INDEX idx_email (email)" {
			t.Fatalf("pt-osc call %d alter = %q", i, got)
		}
	}
	if !containsString(result.Warnings, "used pt-online-schema-change") {
		t.Fatalf("Execute() warnings = %#v, want fallback warning", result.Warnings)
	}

	records := drv.snapshot()
	if len(records) != 7 {
		t.Fatalf("SQL record count = %d, want exact pinned preflight lifecycle: %#v", len(records), records)
	}
	wantQueryParts := []string{
		"SELECT @@SESSION.lock_wait_timeout",
		"SET SESSION lock_wait_timeout = 20",
		"CREATE DATABASE IF NOT EXISTS",
		"CREATE TABLE",
		"CREATE INDEX idx_email ON `releem_online_ddl_test`.",
		"DROP TABLE IF EXISTS",
		"SET SESSION lock_wait_timeout = 31536000",
	}
	for i, want := range wantQueryParts {
		if !strings.Contains(records[i].query, want) {
			t.Fatalf("SQL record %d = %q, want to contain %q", i, records[i].query, want)
		}
		if strings.Contains(records[i].query, "CREATE INDEX idx_email ON app.users") {
			t.Fatalf("production DDL ran after failed preflight: %#v", records)
		}
	}
	for i, record := range records {
		if record.connectionID != records[0].connectionID {
			t.Fatalf("SQL record %d used connection %d, want pinned connection %d", i, record.connectionID, records[0].connectionID)
		}
	}
}

func TestExecuteDoesNotFallbackWhenPTOSCDisallowed(t *testing.T) {
	commandCalls := 0
	executor := &Executor{
		runCommand: func(string, ...string) ([]byte, error) {
			commandCalls++
			return nil, nil
		},
	}
	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE app.users ADD COLUMN c INT, ALGORITHM=COPY, LOCK=EXCLUSIVE",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkOnlineDDL:  true,
		OkPTOSC:      false,
		Config: &config.Config{
			DisableSpaceChecks:  true,
			OnlineDDLTestSchema: "releem_online_ddl_test",
		},
	})
	if err == nil {
		t.Fatalf("Execute() result = %#v, want Online DDL policy error", result)
	}
	if commandCalls != 0 {
		t.Fatalf("pt-osc command calls = %d, want 0", commandCalls)
	}
}

func TestExecuteRejectsDDLTargetMismatchBeforePTOSC(t *testing.T) {
	commandCalls := 0
	backupDir := filepath.Join(t.TempDir(), "not-created")
	executor := &Executor{
		runCommand: func(string, ...string) ([]byte, error) {
			commandCalls++
			return []byte("ok"), nil
		},
	}

	result, err := executor.Execute(ExecuteOptions{
		SQL:          "CREATE INDEX idx_email ON app.users(email)",
		TableName:    "app.orders",
		BackupMethod: BackupMysqldump,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks: true,
			BackupDir:          backupDir,
			PTOSCPath:          "pt-online-schema-change",
			MysqlHost:          "127.0.0.1",
			MysqlPort:          "3306",
			MysqlUser:          "releem",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "app.users") || !strings.Contains(err.Error(), "app.orders") {
		t.Fatalf("Execute() result = %#v, error = %v, want target mismatch with both table names", result, err)
	}
	if commandCalls != 0 {
		t.Fatalf("pt-osc command calls = %d, want 0 before target validation", commandCalls)
	}
	if _, statErr := os.Stat(backupDir); !os.IsNotExist(statErr) {
		t.Fatalf("backup directory stat error = %v, want directory not created", statErr)
	}
}

func TestValidateDDLTargetAcceptsEquivalentQuotedIdentifiers(t *testing.T) {
	executor := &Executor{}
	qualifiedSQL, _, err := executor.validateDDLTarget(
		"ALTER TABLE `app`.`users` ADD COLUMN c INT",
		`"app" . "users"`,
		nil,
	)
	if err != nil {
		t.Fatalf("validateDDLTarget() error = %v", err)
	}
	if qualifiedSQL != "ALTER TABLE `app`.`users` ADD COLUMN c INT" {
		t.Fatalf("validateDDLTarget() SQL = %q", qualifiedSQL)
	}
}

func TestValidateDDLTargetUsesConfiguredSchemaForUnqualifiedDDL(t *testing.T) {
	executor := &Executor{}
	qualifiedSQL, _, err := executor.validateDDLTarget("ALTER TABLE users ADD COLUMN c INT", "app.users", nil)
	if err != nil {
		t.Fatalf("validateDDLTarget() error = %v", err)
	}
	if qualifiedSQL != "ALTER TABLE `app`.`users` ADD COLUMN c INT" {
		t.Fatalf("validateDDLTarget() SQL = %q, want qualified configured target", qualifiedSQL)
	}
}

func TestValidateDDLTargetRespectsLowerCaseTableNames(t *testing.T) {
	tests := []struct {
		name                string
		lowerCaseTableNames driver.Value
		wantErr             bool
	}{
		{name: "case sensitive", lowerCaseTableNames: int64(0), wantErr: true},
		{name: "stored lowercase", lowerCaseTableNames: int64(1)},
		{name: "case insensitive lookup", lowerCaseTableNames: int64(2)},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drv := &recordingSQLDriver{queryValue: tt.lowerCaseTableNames}
			driverName := fmt.Sprintf("phase2-lower-case-table-names-%d", i)
			sql.Register(driverName, drv)
			db, err := sql.Open(driverName, "")
			if err != nil {
				t.Fatalf("sql.Open() error = %v", err)
			}
			defer db.Close()

			executor := &Executor{conn: db}
			qualifiedSQL, _, err := executor.validateDDLTarget("ALTER TABLE App.Users ADD COLUMN c INT", "app.users", nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateDDLTarget() SQL = %q, error = %v, wantErr %v", qualifiedSQL, err, tt.wantErr)
			}
			if !tt.wantErr && qualifiedSQL != "ALTER TABLE `app`.`users` ADD COLUMN c INT" {
				t.Fatalf("validateDDLTarget() SQL = %q, want canonical configured target", qualifiedSQL)
			}
			records := drv.snapshot()
			if len(records) != 1 || records[0].query != "SELECT @@lower_case_table_names" {
				t.Fatalf("SQL records = %#v, want lower_case_table_names lookup", records)
			}
		})
	}
}

func TestExecutePTOSCAcceptsUnqualifiedDDLUsingConfiguredSchema(t *testing.T) {
	var calls []externalCommandCall
	executor := &Executor{
		runCommand: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, externalCommandCall{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}

	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE users ADD COLUMN c INT",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks: true,
			PTOSCPath:          "pt-online-schema-change",
			MysqlHost:          "127.0.0.1",
			MysqlPort:          "3306",
			MysqlUser:          "releem",
		},
	})
	if err != nil || result.MethodUsed != "pt-online-schema-change" {
		t.Fatalf("Execute() result = %#v, error = %v", result, err)
	}
	for i, call := range calls {
		if got := findArgument(call.args, "--alter="); got != "--alter=ADD COLUMN c INT" {
			t.Fatalf("pt-osc call %d alter = %q", i, got)
		}
		if len(call.args) < 2 || !strings.Contains(call.args[1], "D=app,t=users") {
			t.Fatalf("pt-osc call %d args = %#v, want configured target", i, call.args)
		}
	}
}

func TestExecutePTOSCCreateIndexUsesConfiguredSchema(t *testing.T) {
	var calls []externalCommandCall
	executor := &Executor{
		runCommand: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, externalCommandCall{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}

	result, err := executor.Execute(ExecuteOptions{
		SQL:          "CREATE INDEX idx_email ON users(email)",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks: true,
			PTOSCPath:          "pt-online-schema-change",
			MysqlHost:          "127.0.0.1",
			MysqlPort:          "3306",
			MysqlUser:          "releem",
		},
	})
	if err != nil || result.MethodUsed != "pt-online-schema-change" {
		t.Fatalf("Execute() result = %#v, error = %v", result, err)
	}
	for i, call := range calls {
		if got := findArgument(call.args, "--alter="); got != "--alter=ADD INDEX idx_email (email)" {
			t.Fatalf("pt-osc call %d alter = %q", i, got)
		}
		if len(call.args) < 2 || !strings.Contains(call.args[1], "D=app,t=users") {
			t.Fatalf("pt-osc call %d args = %#v, want configured target", i, call.args)
		}
	}
}

func TestExecuteOnlineDDLQualifiesProductionTarget(t *testing.T) {
	drv := &recordingSQLDriver{}
	const driverName = "phase2-online-ddl-qualified-production-target"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	executor := &Executor{conn: db}
	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE users ADD COLUMN c INT",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkOnlineDDL:  true,
		Config: &config.Config{
			DisableSpaceChecks:  true,
			OnlineDDLTestSchema: "releem_online_ddl_test",
		},
	})
	if err != nil || result.MethodUsed != "Online DDL" {
		t.Fatalf("Execute() result = %#v, error = %v", result, err)
	}

	wantProductionSQL := "ALTER TABLE `app`.`users` ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE"
	records := drv.snapshot()
	if !containsRecordedQuery(records, wantProductionSQL) {
		t.Fatalf("SQL records = %#v, want qualified production DDL %q", records, wantProductionSQL)
	}
	if containsRecordedQuery(records, "ALTER TABLE users ADD COLUMN c INT, ALGORITHM=INPLACE, LOCK=NONE") {
		t.Fatalf("SQL records contain unqualified production DDL: %#v", records)
	}
}

func TestExecuteRejectsMultiObjectAlterBeforeExternalCommands(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "rename table", sql: "ALTER TABLE app.users RENAME TO app.users_v2"},
		{name: "bare rename table", sql: "ALTER TABLE app.users RENAME app.users_v2"},
		{name: "exchange partition", sql: "ALTER TABLE app.users EXCHANGE PARTITION p0 WITH TABLE app.staging"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commandCalls := 0
			executor := &Executor{
				runCommand: func(string, ...string) ([]byte, error) {
					commandCalls++
					return []byte("ok"), nil
				},
			}
			result, err := executor.Execute(ExecuteOptions{
				SQL:          tt.sql,
				TableName:    "app.users",
				BackupMethod: BackupNone,
				OkPTOSC:      true,
				Config: &config.Config{
					DisableSpaceChecks: true,
					PTOSCPath:          "pt-online-schema-change",
					MysqlHost:          "127.0.0.1",
					MysqlPort:          "3306",
					MysqlUser:          "releem",
				},
			})
			if err == nil {
				t.Fatalf("Execute() result = %#v, want unsafe multi-object ALTER error", result)
			}
			if commandCalls != 0 {
				t.Fatalf("external command calls = %d, want 0", commandCalls)
			}
		})
	}
}

func TestExecutePTOSCQualifiesUnqualifiedForeignKeyReference(t *testing.T) {
	var calls []externalCommandCall
	executor := &Executor{
		runCommand: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, externalCommandCall{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}

	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE users ADD CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents(id)",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks: true,
			PTOSCPath:          "pt-online-schema-change",
			MysqlHost:          "127.0.0.1",
			MysqlPort:          "3306",
			MysqlUser:          "releem",
		},
	})
	if err != nil || result.MethodUsed != "pt-online-schema-change" {
		t.Fatalf("Execute() result = %#v, error = %v", result, err)
	}
	wantAlter := "--alter=ADD CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES `app`.`parents`(id)"
	for i, call := range calls {
		if got := findArgument(call.args, "--alter="); got != wantAlter {
			t.Fatalf("pt-osc call %d alter = %q, want %q", i, got, wantAlter)
		}
	}
}

func TestExecuteResolvesUnqualifiedConfiguredTableOnce(t *testing.T) {
	drv := &recordingSQLDriver{queryValue: "app"}
	const driverName = "phase2-resolved-configured-target"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	executor := &Executor{
		conn: db,
		runCommand: func(string, ...string) ([]byte, error) {
			return []byte("ok"), nil
		},
	}
	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE users ADD COLUMN c INT",
		TableName:    "users",
		BackupMethod: BackupNone,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks: true,
			PTOSCPath:          "pt-online-schema-change",
			MysqlHost:          "127.0.0.1",
			MysqlPort:          "3306",
			MysqlUser:          "releem",
		},
	})
	if err != nil || result.MethodUsed != "pt-online-schema-change" {
		t.Fatalf("Execute() result = %#v, error = %v", result, err)
	}

	databaseLookups := 0
	for _, record := range drv.snapshot() {
		if record.query == "SELECT DATABASE()" {
			databaseLookups++
		}
	}
	if databaseLookups != 1 {
		t.Fatalf("SELECT DATABASE() calls = %d, want resolved target reused after validation", databaseLookups)
	}
}

func TestExecuteUsesStructuredTargetWithoutDatabaseLookup(t *testing.T) {
	var calls []externalCommandCall
	executor := &Executor{
		runCommand: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, externalCommandCall{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}
	target := TableInfo{Database: "app", Table: "users"}

	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE users ADD COLUMN c INT",
		Target:       &target,
		BackupMethod: BackupNone,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks: true,
			PTOSCPath:          "pt-online-schema-change",
			MysqlHost:          "127.0.0.1",
			MysqlPort:          "3306",
			MysqlUser:          "releem",
		},
	})
	if err != nil || result.MethodUsed != "pt-online-schema-change" {
		t.Fatalf("Execute() result = %#v, error = %v", result, err)
	}
	for i, call := range calls {
		if len(call.args) < 2 || !strings.Contains(call.args[1], "D=app,t=users") {
			t.Fatalf("pt-osc call %d args = %#v, want structured target", i, call.args)
		}
	}
}

func TestQualifyUnqualifiedReferences(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "unqualified parent",
			sql:  "ALTER TABLE `app`.`users` ADD FOREIGN KEY (parent_id) REFERENCES parents(id)",
			want: "ALTER TABLE `app`.`users` ADD FOREIGN KEY (parent_id) REFERENCES `app`.`parents`(id)",
		},
		{
			name: "keyword in quoted constraint name",
			sql:  "ALTER TABLE `app`.`users` ADD CONSTRAINT `REFERENCES parents` FOREIGN KEY (parent_id) REFERENCES parents(id)",
			want: "ALTER TABLE `app`.`users` ADD CONSTRAINT `REFERENCES parents` FOREIGN KEY (parent_id) REFERENCES `app`.`parents`(id)",
		},
		{
			name: "comment before parent",
			sql:  "ALTER TABLE `app`.`users` ADD FOREIGN KEY (parent_id) REFERENCES /* parent */ parents(id)",
			want: "ALTER TABLE `app`.`users` ADD FOREIGN KEY (parent_id) REFERENCES /* parent */ `app`.`parents`(id)",
		},
		{
			name: "qualified cross schema parent",
			sql:  "ALTER TABLE `app`.`users` ADD FOREIGN KEY (parent_id) REFERENCES shared.parents(id)",
			want: "ALTER TABLE `app`.`users` ADD FOREIGN KEY (parent_id) REFERENCES shared.parents(id)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := qualifyUnqualifiedReferences(tt.sql, "app")
			if err != nil {
				t.Fatalf("qualifyUnqualifiedReferences() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("qualifyUnqualifiedReferences() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRejectUnsafeMultiObjectAlterAllowsSingleTableRenameOperations(t *testing.T) {
	for _, ddl := range []string{
		"ALTER TABLE `app`.`users` RENAME COLUMN old_name TO new_name",
		"ALTER TABLE `app`.`users` RENAME INDEX old_idx TO new_idx",
	} {
		if err := rejectUnsafeMultiObjectAlter(ddl); err != nil {
			t.Fatalf("rejectUnsafeMultiObjectAlter(%q) error = %v", ddl, err)
		}
	}
}

func TestExecuteFallsBackForExplicitCopyWhenPTOSCAllowed(t *testing.T) {
	var calls []externalCommandCall
	executor := &Executor{
		runCommand: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, externalCommandCall{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}
	result, err := executor.Execute(ExecuteOptions{
		SQL:          "ALTER TABLE app.users ADD COLUMN c INT, ALGORITHM=COPY, LOCK=EXCLUSIVE",
		TableName:    "app.users",
		BackupMethod: BackupNone,
		OkOnlineDDL:  true,
		OkPTOSC:      true,
		Config: &config.Config{
			DisableSpaceChecks:  true,
			OnlineDDLTestSchema: "releem_online_ddl_test",
			PTOSCPath:           "pt-online-schema-change",
			MysqlHost:           "127.0.0.1",
			MysqlPort:           "3306",
			MysqlUser:           "releem",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.MethodUsed != "pt-online-schema-change" || len(calls) != 2 {
		t.Fatalf("Execute() result = %#v, calls = %#v", result, calls)
	}
	for i, call := range calls {
		if got := findArgument(call.args, "--alter="); got != "--alter=ADD COLUMN c INT" {
			t.Fatalf("pt-osc call %d alter = %q, want native Online DDL clauses removed", i, got)
		}
	}
}

type externalCommandCall struct {
	name string
	args []string
}

func findArgument(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	return ""
}

func containsString(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}

func containsRecordedQuery(records []recordedSQLQuery, query string) bool {
	for _, record := range records {
		if record.query == query {
			return true
		}
	}
	return false
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

func TestExecuteOnlineDDLUsesOneSessionAndRestoresTimeout(t *testing.T) {
	drv := &recordingSQLDriver{}
	const driverName = "phase2-session-affinity"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	ddl := "ALTER TABLE users ADD COLUMN c INT"
	executionErr, cleanupErr := executeOnlineDDLOnPinnedConn(context.Background(), db, ddl, 20)
	if executionErr != nil || cleanupErr != nil {
		t.Fatalf("executeOnlineDDLOnPinnedConn() errors = (%v, %v)", executionErr, cleanupErr)
	}

	records := drv.snapshot()
	wantQueries := []string{
		"SELECT @@SESSION.lock_wait_timeout",
		"SET SESSION lock_wait_timeout = 20",
		ddl,
		"SET SESSION lock_wait_timeout = 31536000",
	}
	if len(records) != len(wantQueries) {
		t.Fatalf("record count = %d, want %d: %#v", len(records), len(wantQueries), records)
	}
	for i, want := range wantQueries {
		if records[i].query != want {
			t.Fatalf("query[%d] = %q, want %q", i, records[i].query, want)
		}
		if records[i].connectionID != records[0].connectionID {
			t.Fatalf("query[%d] used connection %d, want connection %d", i, records[i].connectionID, records[0].connectionID)
		}
	}
}

func TestExecuteOnlineDDLRestoresTimeoutAfterDDLError(t *testing.T) {
	drv := &recordingSQLDriver{failQuery: "ALTER TABLE users ADD COLUMN c INT"}
	const driverName = "phase2-session-restore-after-error"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	executionErr, cleanupErr := executeOnlineDDLOnPinnedConn(context.Background(), db, drv.failQuery, 20)
	if executionErr == nil || !strings.Contains(executionErr.Error(), "forced query failure") {
		t.Fatalf("executeOnlineDDLOnPinnedConn() execution error = %v, want forced query failure", executionErr)
	}
	if cleanupErr != nil {
		t.Fatalf("executeOnlineDDLOnPinnedConn() cleanup error = %v, want nil", cleanupErr)
	}

	records := drv.snapshot()
	if got := records[len(records)-1].query; got != "SET SESSION lock_wait_timeout = 31536000" {
		t.Fatalf("last query = %q, want timeout restoration", got)
	}
}

func TestExecuteOnlineDDLDiscardsSessionAfterRestoreFailure(t *testing.T) {
	drv := &recordingSQLDriver{failQuery: "SET SESSION lock_wait_timeout = 31536000"}
	const driverName = "phase2-session-discard-after-restore-error"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	executionErr, cleanupErr := executeOnlineDDLOnPinnedConn(
		context.Background(), db, "ALTER TABLE users ADD COLUMN c INT", 20,
	)
	if executionErr != nil {
		t.Fatalf("executeOnlineDDLOnPinnedConn() execution error = %v, want nil after successful DDL", executionErr)
	}
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "restore session lock_wait_timeout") {
		t.Fatalf("executeOnlineDDLOnPinnedConn() cleanup error = %v, want restore failure", cleanupErr)
	}

	if _, err := db.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("db.ExecContext() after discarded session error = %v", err)
	}
	records := drv.snapshot()
	if records[len(records)-1].connectionID == records[0].connectionID {
		t.Fatalf("connection %d was reused after timeout restoration failed", records[0].connectionID)
	}
}

func TestPinnedTimeoutRestoresSessionAfterCallbackPanic(t *testing.T) {
	drv := &recordingSQLDriver{}
	const driverName = "phase2-session-restore-after-panic"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("withPinnedLockWaitTimeout() did not propagate callback panic")
			}
		}()
		_, _ = withPinnedLockWaitTimeout(context.Background(), db, 20, func(context.Context, *sql.Conn) error {
			panic("forced callback panic")
		})
	}()

	records := drv.snapshot()
	if got := records[len(records)-1].query; got != "SET SESSION lock_wait_timeout = 31536000" {
		t.Fatalf("last query after panic = %q, want timeout restoration", got)
	}
}

func TestCleanupOnlineDDLTestTableTimesOutAndDiscardsConnection(t *testing.T) {
	drv := &recordingSQLDriver{blockQueryContains: "DROP TABLE IF EXISTS"}
	const driverName = "phase2-scratch-cleanup-timeout"
	sql.Register(driverName, drv)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn() error = %v", err)
	}
	err = cleanupOnlineDDLTestTable(conn, "`scratch`.`test_table`", 10*time.Millisecond)
	_ = conn.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanupOnlineDDLTestTable() error = %v, want deadline exceeded", err)
	}

	if _, err := db.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("db.ExecContext() after discarded cleanup session error = %v", err)
	}
	records := drv.snapshot()
	if records[len(records)-1].connectionID == records[0].connectionID {
		t.Fatalf("connection %d was reused after scratch cleanup timeout", records[0].connectionID)
	}
}

type recordedSQLQuery struct {
	connectionID int
	query        string
}

type recordingSQLDriver struct {
	mu                 sync.Mutex
	nextID             int
	records            []recordedSQLQuery
	failQuery          string
	failQueryContains  string
	failErr            error
	blockQueryContains string
	queryValue         driver.Value
}

func (d *recordingSQLDriver) Open(string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	return &recordingSQLConn{driver: d, id: d.nextID}, nil
}

func (d *recordingSQLDriver) record(connectionID int, query string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records = append(d.records, recordedSQLQuery{connectionID: connectionID, query: query})
	if query == d.failQuery || (d.failQueryContains != "" && strings.Contains(query, d.failQueryContains)) {
		if d.failErr != nil {
			return d.failErr
		}
		return errors.New("forced query failure")
	}
	return nil
}

func (d *recordingSQLDriver) snapshot() []recordedSQLQuery {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recordedSQLQuery(nil), d.records...)
}

func (d *recordingSQLDriver) shouldBlock(query string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.blockQueryContains != "" && strings.Contains(query, d.blockQueryContains)
}

type recordingSQLConn struct {
	driver *recordingSQLDriver
	id     int
}

func (c *recordingSQLConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *recordingSQLConn) Close() error                        { return nil }
func (c *recordingSQLConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *recordingSQLConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if err := c.driver.record(c.id, query); err != nil {
		return nil, err
	}
	if c.driver.shouldBlock(query) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return driver.RowsAffected(0), nil
}

func (c *recordingSQLConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := c.driver.record(c.id, query); err != nil {
		return nil, err
	}
	value := c.driver.queryValue
	if value == nil {
		value = int64(31536000)
	}
	return &singleValueRow{value: value}, nil
}

type singleValueRow struct {
	value driver.Value
	read  bool
}

func (r *singleValueRow) Columns() []string { return []string{"value"} }
func (r *singleValueRow) Close() error      { return nil }
func (r *singleValueRow) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = r.value
	return nil
}
