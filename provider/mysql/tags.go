package mysql

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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
	t := strings.ToUpper(dbTypeName)

	// integers, including unsigned variants
	switch {
	case strings.Contains(t, "INT"): // TINYINT, SMALLINT, MEDIUMINT, INT, INTEGER, BIGINT (incl. UNSIGNED)
		return typeCategoryInt
	case strings.Contains(t, "BOOL"): // BOOL / BOOLEAN alias of TINYINT(1)
		return typeCategoryInt
	case strings.Contains(t, "BIT"):
		return typeCategoryInt
	case strings.Contains(t, "SERIAL"): // alias for BIGINT UNSIGNED
		return typeCategoryInt
	case strings.Contains(t, "FIXED"): // DECIMAL alias
		return typeCategoryFloat
	case strings.Contains(t, "DEC"): // DECIMAL
		return typeCategoryFloat
	case strings.Contains(t, "NUMERIC"):
		return typeCategoryFloat
	case strings.Contains(t, "REAL"),
		strings.Contains(t, "FLOAT"),
		strings.Contains(t, "DOUBLE"):
		return typeCategoryFloat
	case strings.Contains(t, "YEAR"),
		strings.Contains(t, "DATE"),
		strings.Contains(t, "TIME"):
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
		// unsigned values above math.MaxInt64 still fit an int64 tag when
		// they fit uint64; loss beyond that is acceptable for MVT tags
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return int64(u), nil
		}
		return nil, fmt.Errorf("cannot parse %q as int", s)
	case typeCategoryFloat:
		return strconv.ParseFloat(s, 64)
	case typeCategoryTime:
		// normalize to the same string form the direct scan path produces
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
