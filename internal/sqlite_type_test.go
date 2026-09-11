package python

import (
	"testing"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

func TestSQLiteTypeUsesDeclaredTypeAffinity(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"BIGINT":        "int",
		"VARCHAR(255)":  "str",
		"CLOB":          "str",
		"BLOB":          "bytes",
		"DOUBLE":        "float",
		"DECIMAL(10,2)": "Any",
		"BOOLEAN":       "Any",
		"":              "Any",
	}
	for declaredType, want := range tests {
		declaredType, want := declaredType, want
		t.Run(declaredType, func(t *testing.T) {
			t.Parallel()
			column := &plugin.Column{Type: &plugin.Identifier{Name: declaredType}}
			if got := sqliteType(column); got != want {
				t.Fatalf("sqliteType(%q) = %q, want %q", declaredType, got, want)
			}
		})
	}
}

func TestDBAPISQLExpandsRepeatedParameters(t *testing.T) {
	t.Parallel()

	gotSQL, gotOrder, err := dbapiSQL(
		"SELECT ?1, '?' FROM records -- preserve ?9\nWHERE owner_id = ?2 OR backup_owner_id = ?2 AND marker = ?",
		"sqlite",
	)
	if err != nil {
		t.Fatal(err)
	}
	wantSQL := "SELECT ?, '?' FROM records -- preserve ?9\nWHERE owner_id = ? OR backup_owner_id = ? AND marker = ?"
	if gotSQL != wantSQL {
		t.Errorf("SQL = %q, want %q", gotSQL, wantSQL)
	}
	wantOrder := []int32{1, 2, 2, 3}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
	}
	for index := range wantOrder {
		if gotOrder[index] != wantOrder[index] {
			t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
		}
	}
}

func TestDBAPISQLRejectsUnsupportedEngine(t *testing.T) {
	t.Parallel()

	if _, _, err := dbapiSQL("SELECT $1", "postgresql"); err == nil {
		t.Fatal("expected PostgreSQL DB-API generation to fail")
	}
}

func TestDBAPISQLRejectsInvalidPlaceholder(t *testing.T) {
	t.Parallel()

	if _, _, err := dbapiSQL("SELECT ?0", "sqlite"); err == nil {
		t.Fatal("expected invalid SQLite placeholder to fail")
	}
}
