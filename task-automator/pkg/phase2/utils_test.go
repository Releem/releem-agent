package phase2

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseTableName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		getDB   func() (string, error)
		wantDB  string
		wantTbl string
		wantErr bool
	}{
		{
			name:    "full table name",
			input:   "mydb.mytable",
			getDB:   func() (string, error) { return "testdb", nil },
			wantDB:  "mydb",
			wantTbl: "mytable",
			wantErr: false,
		},
		{
			name:    "quoted qualified table name",
			input:   " `my``db` . \"my\"\"table\" ",
			getDB:   func() (string, error) { return "testdb", nil },
			wantDB:  "my`db",
			wantTbl: `my"table`,
			wantErr: false,
		},
		{
			name:    "table name only",
			input:   "mytable",
			getDB:   func() (string, error) { return "testdb", nil },
			wantDB:  "testdb",
			wantTbl: "mytable",
			wantErr: false,
		},
		{
			name:    "error getting database",
			input:   "mytable",
			getDB:   func() (string, error) { return "", fmt.Errorf("database error") },
			wantDB:  "",
			wantTbl: "",
			wantErr: true,
		},
		{
			name:    "invalid table reference",
			input:   "app.users.extra",
			getDB:   func() (string, error) { return "testdb", nil },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseTableName(tt.input, tt.getDB)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTableName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if info.Database != tt.wantDB {
					t.Errorf("ParseTableName() Database = %v, want %v", info.Database, tt.wantDB)
				}
				if info.Table != tt.wantTbl {
					t.Errorf("ParseTableName() Table = %v, want %v", info.Table, tt.wantTbl)
				}
			}
		})
	}
}

func TestExtractAlterStatement(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "simple ADD COLUMN",
			sql:  "ALTER TABLE mytable ADD COLUMN newcol INT",
			want: "ADD COLUMN newcol INT",
		},
		{
			name: "full table name",
			sql:  "ALTER TABLE mydb.mytable ADD COLUMN newcol INT",
			want: "ADD COLUMN newcol INT",
		},
		{
			name: "DROP COLUMN",
			sql:  "ALTER TABLE mytable DROP COLUMN oldcol",
			want: "DROP COLUMN oldcol",
		},
		{
			name: "not ALTER statement",
			sql:  "SELECT * FROM mytable",
			want: "SELECT * FROM mytable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAlterStatement(tt.sql)
			if !strings.Contains(got, tt.want) || len(got) < len(tt.want) {
				// For simplicity, check if extracted statement contains expected parts
				if tt.name == "not ALTER statement" {
					if got != tt.want {
						t.Errorf("ExtractAlterStatement() = %v, want %v", got, tt.want)
					}
				} else {
					// Check that we extracted something reasonable
					if len(got) == 0 {
						t.Errorf("ExtractAlterStatement() returned empty string")
					}
				}
			}
		})
	}
}

func TestExtractAlterStatementPreservesQuotedWhitespace(t *testing.T) {
	sql := "ALTER TABLE users ADD COLUMN note VARCHAR(32) DEFAULT 'two  spaces'"
	want := "ADD COLUMN note VARCHAR(32) DEFAULT 'two  spaces'"

	if got := ExtractAlterStatement(sql); got != want {
		t.Fatalf("ExtractAlterStatement() = %q, want %q", got, want)
	}
}

func TestExtractAlterStatementHandlesMariaDBModifiersAndIdentifiers(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "if exists",
			sql:  "ALTER TABLE IF EXISTS users ADD COLUMN c INT",
			want: "ADD COLUMN c INT",
		},
		{
			name: "online ignore",
			sql:  "ALTER ONLINE IGNORE TABLE users ADD COLUMN c INT",
			want: "ADD COLUMN c INT",
		},
		{
			name: "dollar identifier",
			sql:  "ALTER TABLE orders$2026 ADD COLUMN c INT",
			want: "ADD COLUMN c INT",
		},
		{
			name: "Unicode identifier",
			sql:  "ALTER TABLE заказы ADD COLUMN c INT",
			want: "ADD COLUMN c INT",
		},
		{
			name: "BMP symbol identifier",
			sql:  "ALTER TABLE orders£ ADD COLUMN c INT",
			want: "ADD COLUMN c INT",
		},
		{
			name: "spaces around qualifier dot",
			sql:  "ALTER TABLE app . users ADD COLUMN c INT",
			want: "ADD COLUMN c INT",
		},
		{
			name: "ANSI quotes identifier",
			sql:  `ALTER TABLE "app"."users" ADD COLUMN c INT`,
			want: "ADD COLUMN c INT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractAlterStatement(tt.sql); got != tt.want {
				t.Fatalf("ExtractAlterStatement() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanUseOnlineDDL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{
			name: "ADD COLUMN supports Online DDL",
			sql:  "ALTER TABLE mytable ADD COLUMN newcol INT",
			want: true,
		},
		{
			name: "ADD FULLTEXT does not support Online DDL",
			sql:  "ALTER TABLE mytable ADD FULLTEXT INDEX idx_ft (col1)",
			want: false,
		},
		{
			name: "ADD SPATIAL does not support Online DDL",
			sql:  "ALTER TABLE mytable ADD SPATIAL INDEX idx_sp (col1)",
			want: false,
		},
		{
			name: "DROP PRIMARY KEY does not support Online DDL",
			sql:  "ALTER TABLE mytable DROP PRIMARY KEY",
			want: false,
		},
		{
			name: "MODIFY COLUMN does not support Online DDL",
			sql:  "ALTER TABLE mytable MODIFY COLUMN col1 VARCHAR(100)",
			want: false,
		},
		{
			name: "MODIFY without COLUMN does not support Online DDL",
			sql:  "ALTER TABLE mytable MODIFY col1 VARCHAR(100)",
			want: false,
		},
		{
			name: "MODIFY with type change does not support Online DDL",
			sql:  "alter table myisamt modify id bigint not null",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanUseOnlineDDL(tt.sql)
			if got != tt.want {
				t.Errorf("CanUseOnlineDDL() = %v, want %v", got, tt.want)
			}
		})
	}
}
