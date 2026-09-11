package endtoend

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func python312(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the sqlite3 rowcount regression test")
	}
	if err := exec.Command(python, "-c", "import sys; assert sys.version_info >= (3, 12)").Run(); err != nil {
		t.Skip("python3.12 or newer is required for the sqlite3 rowcount regression test")
	}
	return python
}

func TestSQLiteExecRowsReturningConsumesRows(t *testing.T) {
	python := python312(t)

	script := `
import importlib.util
import sqlite3
import sys
import types
from pathlib import Path

generated = Path("testdata/sqlite_dbapi/python")
package = types.ModuleType("querytest")
package.__path__ = [str(generated)]
sys.modules["querytest"] = package

models_spec = importlib.util.spec_from_file_location("querytest.models", generated / "models.py")
models = importlib.util.module_from_spec(models_spec)
sys.modules["querytest.models"] = models
models_spec.loader.exec_module(models)

query_spec = importlib.util.spec_from_file_location("querytest.query", generated / "query.py")
query = importlib.util.module_from_spec(query_spec)
query_spec.loader.exec_module(query)

connection = sqlite3.connect(":memory:")
connection.execute("""
CREATE TABLE records (
    id INTEGER PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    note TEXT,
    payload BLOB NOT NULL,
    score REAL,
    dynamic_value NUMERIC
)
""")
connection.executemany(
    "INSERT INTO records (name, payload) VALUES (?, ?)",
    [("remove", b"a"), ("remove", b"b"), ("keep", b"c")],
)

affected = query.Querier(connection).delete_records_returning(name="remove")
assert affected == 2, affected
`
	cmd := exec.Command(python, "-c", script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated SQLite querier failed: %s\n%s", err, output)
	}
}

func TestSQLiteGoldenPassesMypyStrict(t *testing.T) {
	python := python312(t)
	if err := exec.Command(python, "-c", "import mypy").Run(); err != nil {
		t.Skip("mypy is required for the generated-code type-check regression test")
	}

	tempDir := t.TempDir()
	packageDir := filepath.Join(tempDir, "querytest")
	if err := os.Mkdir(packageDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"models.py", "query.py"} {
		source := filepath.Join("testdata", "sqlite_dbapi", "python", name)
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packageDir, name), contents, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(packageDir, "__init__.py"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(python, "-m", "mypy", "--strict", "--cache-dir", filepath.Join(tempDir, "mypy-cache"), packageDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated SQLite querier failed strict mypy: %s\n%s", err, output)
	}
}
