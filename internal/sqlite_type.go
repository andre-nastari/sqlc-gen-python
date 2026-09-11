package python

import (
	"strings"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"
	"github.com/sqlc-dev/plugin-sdk-go/sdk"
)

// sqliteType follows SQLite's declared-type affinity rules. NUMERIC affinity
// deliberately maps to Any: without application-specific adapters SQLite may
// return int, float, str, or bytes for values in the same declared column.
func sqliteType(col *plugin.Column) string {
	declaredType := strings.ToUpper(strings.TrimSpace(sdk.DataType(col.Type)))

	switch {
	case strings.Contains(declaredType, "INT"):
		return "int"
	case strings.Contains(declaredType, "CHAR"),
		strings.Contains(declaredType, "CLOB"),
		strings.Contains(declaredType, "TEXT"):
		return "str"
	case strings.Contains(declaredType, "BLOB"):
		return "bytes"
	case strings.Contains(declaredType, "REAL"),
		strings.Contains(declaredType, "FLOA"),
		strings.Contains(declaredType, "DOUB"):
		return "float"
	case declaredType == "":
		// An undeclared column has BLOB affinity, but unlike an explicitly
		// declared BLOB it commonly carries values of any storage class.
		return "Any"
	default:
		return "Any"
	}
}
