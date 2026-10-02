//go:build cgo

package gpkg

import (
	"database/sql"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
)

const featureSQLiteDriver = "tegola-gpkg-feature-sqlite3"
const featureUTF8Function = "tegola_feature_valid_utf8"
const maxFilterSourceTextBytes = 1024 * 1024

func init() {
	sql.Register(featureSQLiteDriver, &sqlite3.SQLiteDriver{ConnectHook: func(connection *sqlite3.SQLiteConn) error {
		return connection.RegisterFunc(featureUTF8Function, validFilterUTF8, true)
	}})
}

// SQL domain guards establish TEXT and a byte cap before this callback. The
// vendored generic converter copies at most that many UTF-8 bytes with GoStringN.
// UTF-16 databases are not admitted to the optional filter profile.
func validFilterUTF8(value any) bool {
	text, ok := value.(string)
	return ok && len(text) <= maxFilterSourceTextBytes && utf8.ValidString(text)
}
