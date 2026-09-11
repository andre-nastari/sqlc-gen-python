package endtoend

import (
	"os/exec"
	"testing"
)

func TestSQLiteExecRowsReturningConsumesRows(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the sqlite3 rowcount regression test")
	}
	if err := exec.Command(python, "-c", "import sys; assert sys.version_info >= (3, 12)").Run(); err != nil {
		t.Skip("python3.12 or newer is required for the sqlite3 rowcount regression test")
	}

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
