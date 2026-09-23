package mysql

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// mysqlTypeCategory classifies a column's declared MySQL/MariaDB type into one
// of the value shapes tegola tags support: string, integer, float or time.
// Everything that is not recognized as numeric or temporal stays a string,
// which is the safe default for the database/sql driver: it returns TEXT and
// BLOB columns as []byte.
type mysqlTypeCategory int

const (
	typeCategoryString mysqlTypeCategory = iota
	typeCategoryInt
	typeCategoryFloat
	typeCategoryTime
)

// categoryFromDatabaseTypeName maps a DatabaseTypeName (e.g. "BIGINT",
// "UNSIGNED BIGINT", "TINYINT", "DECIMAL", "DATETIME") to a category.
// Types are matched on prefixes so size/scale suffixes need no special casing.
func categoryFromDatabaseTypeName(dbTypeName string) mysqlTypeCategory {
	t := strings.ToUpper(strings.TrimSpace(dbTypeName))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return typeCategoryString
	}
	base := fields[0]
	if base == "UNSIGNED" && len(fields) > 1 {
		base = fields[1]
	}

	switch base {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
		"BOOL", "BOOLEAN", "BIT", "SERIAL":
		return typeCategoryInt
	case "FIXED", "DECIMAL", "NUMERIC", "NEWDECIMAL", "REAL", "FLOAT", "DOUBLE":
		return typeCategoryFloat
	case "YEAR", "DATE", "DATETIME", "TIMESTAMP", "TIME":
		return typeCategoryTime
	default:
		return typeCategoryString
	}
}

// convertTagValue converts a driver-returned value to a tag value of the
// category reported by the column type. go-sql-driver/mysql returns
// non-prepared numeric columns as []byte holding the textual representation;
// without this conversion numeric tags would leak into MVT as strings.
func convertTagValue(v interface{}, cat mysqlTypeCategory) (interface{}, error) {
	if v == nil {
		return nil, nil
	}

	b, isBytes := v.([]byte)
	if !isBytes {
		// driver already produced a typed value (prepared statements, native
		// int results, float64, time.Time, string, etc.)
		return v, nil
	}

	s := string(b)
	switch cat {
	case typeCategoryInt:
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i, nil
		}
		// Preserve unsigned values above math.MaxInt64. MVT has a native
		// uint_value representation, so narrowing these to int64 would turn
		// valid identifiers into negative values.
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u, nil
		}
		return nil, fmt.Errorf("cannot parse %q as int", s)
	case typeCategoryFloat:
		return strconv.ParseFloat(s, 64)
	case typeCategoryTime:
		// Normalize common textual date/time values to the same RFC3339 form
		// used by the typed time.Time path. Keep unknown representations as
		// strings rather than dropping an otherwise valid tag.
		for _, layout := range []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
			"2006-01-02",
		} {
			if parsed, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
				return parsed.Format(time.RFC3339Nano), nil
			}
		}
		return s, nil
	default:
		return s, nil
	}
}

// tagValueFromColumn converts the raw scanned value of column i into a tag
// value using the column's declared type.
func tagValueFromColumn(ct *sql.ColumnType, v interface{}) (interface{}, error) {
	return convertTagValue(v, categoryFromDatabaseTypeName(ct.DatabaseTypeName()))
}
