package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
	conf "github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/provider/crsconfig"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider/mapplgis"
)

// ErrMissingLayerName is returned when a layer config is missing the 'name' key
var ErrMissingLayerName = errors.New("mysql: layer is missing 'name'")

func init() {
	_ = provider.Register(provider.TypeStd.Prefix()+Name, NewTileProvider, Cleanup)
}

// ProviderType is the config type name for this provider. The same driver
// and code path serves both MySQL and MariaDB servers; the flavor is
// detected at runtime from SELECT VERSION().
const ProviderType = "mysql"

// serverFlavorFromVersion parses a VERSION() string and reports whether the
// server is MariaDB ("mariadb") or MySQL ("mysql").
func serverFlavorFromVersion(v string) string {
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return GeometryFormatMariaDB
	}
	return GeometryFormatMySQL
}

// detectServerFlavor queries the server version and maps it to a flavor.
func detectServerFlavor(db *sql.DB) (string, error) {
	var version string
	if err := db.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		return "", fmt.Errorf("error querying server version: %v", err)
	}
	return serverFlavorFromVersion(version), nil
}

// geomTypeSampleRows is the number of geometry values the table inspection
// will try before giving up. Some datasets (e.g. MapplGIS exports) store a
// small fraction of rows in wrapper formats that the active geometry_format
// cannot decode, so a single-row sample would poison provider registration.
// It is shared with the other providers through the common inspection
// contract (docs/provider-contract.md).
const geomTypeSampleRows = codec.InspectionSampleLimit

// limitClauseRe matches a trailing LIMIT [offset,] n or LIMIT n OFFSET offset
// clause.
var limitClauseRe = regexp.MustCompile(`\s+limit\s+(\d+\s*,\s*\d+|\d+(?:\s+offset\s+\d+)?)\s*$`)

// customSQLNeedsDeferredInspection reports tokens whose value depends on the
// requested tile. A single 0/0/0 sample cannot establish a geometry type for
// predicates such as "tile_x = !X!", so those layers are registered with
// their configured CRS and inspected only when they are actually queried.
func customSQLNeedsDeferredInspection(sqlText string) bool {
	upper := strings.ToUpper(sqlText)
	for _, token := range []string{
		conf.XToken,
		conf.YToken,
		conf.ZToken,
		conf.ScaleDenominatorToken,
		conf.PixelWidthToken,
		conf.PixelHeightToken,
	} {
		if strings.Contains(upper, token) {
			return true
		}
	}
	return false
}

func sampleGeometryQuery(qtext string) string {
	base := strings.TrimSpace(qtext)
	// A caller may provide a complete SQL statement with a trailing
	// semicolon. Strip it before removing the caller's LIMIT clause so the
	// sampling LIMIT is never appended after a statement terminator.
	base = strings.TrimSpace(strings.TrimSuffix(base, ";"))
	for {
		m := limitClauseRe.FindStringIndex(strings.ToLower(base))
		if m == nil || m[0] == 0 {
			break
		}
		base = strings.TrimSpace(base[:m[0]])
	}
	return fmt.Sprintf("%v LIMIT %v", base, geomTypeSampleRows)
}

// geomTypeFromColumn samples up to geomTypeSampleRows geometry values from
// the given query and decodes the first one that succeeds. It returns
// sql.ErrNoRows when the query yields no rows at all, and the decode error
// only when every sampled row failed to decode.
func geomTypeFromColumn(db *sql.DB, qtext string, geometryFormat string, serverFlavor string, mosCfg codec.MOSConfig) (geo geom.Geometry, headerSRID uint64, err error) {
	rows, err := db.Query(sampleGeometryQuery(qtext))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var lastErr error
	var values []interface{}
	for rows.Next() {
		var geomVal interface{}
		if err := rows.Scan(&geomVal); err != nil {
			return nil, 0, err
		}
		if geomVal == nil {
			continue
		}
		// A MapplGIS LayerInfo blob describes the layer rather than being a
		// geometry; detection is handled by the one-time provider/mapplgis
		// contract, so sampling just skips such values.
		if blob, ok := geomVal.([]byte); ok && mos.IsSystemInfoBlob(blob) {
			continue
		}
		values = append(values, geomVal)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	for _, geomVal := range values {
		srid, decoded, decodeErr := decodeGeometry(geomVal, geometryFormat, serverFlavor, mosCfg)
		if decodeErr != nil {
			lastErr = decodeErr
			continue
		}
		return decoded, srid, nil
	}
	if lastErr != nil {
		return nil, 0, fmt.Errorf("error decoding sampled geometry: %v", lastErr)
	}
	return nil, 0, sql.ErrNoRows
}

// sridConsistencySQL builds the distinct-SRID probe (audit N8): the smallest
// result set that proves a geometry column mixes SRIDs. NULL geometries are
// excluded so an untyped NULL row cannot masquerade as a second SRID.
func sridConsistencySQL(tablename, geomFieldname string) string {
	geomIdent := quoteIdentifier(geomFieldname)
	return fmt.Sprintf("SELECT DISTINCT ST_SRID(%v) FROM %v WHERE %v IS NOT NULL LIMIT 2",
		geomIdent, quoteIdentifier(tablename), geomIdent)
}

// shouldProbeTableSRIDs reports whether the distinct-SRID probe (audit N8)
// applies to a table layer. The probe uses ST_SRID, which only speaks to
// native geometry columns, so raw wkb/wkt/mos columns are excluded outright.
// Explicit native formats (mysql/mariadb) assert a native column and are
// always probed; the auto format is probed only once a sampled row decoded a
// native header with a non-zero SRID, since auto may legitimately fall back
// to plain WKB/WKT blobs that ST_SRID cannot read.
func shouldProbeTableSRIDs(geometryFormat string, headerSRID uint64) bool {
	switch geometryFormat {
	case GeometryFormatMySQL, GeometryFormatMariaDB:
		return true
	case GeometryFormatAuto, "":
		return headerSRID > 0
	}
	return false
}

// checkTableSRIDs verifies that a table's geometry column exposes a single
// SRID (audit N8). Without an explicit srid/crs_defn, the layer SRID is
// derived from sampled geometry headers; in a table that mixes SRIDs that
// silently becomes one arbitrary row's SRID and the !BBOX! filter breaks for
// the remaining rows. More than one distinct SRID fails registration with a
// controlled error advising an explicit srid/crs_defn, mirroring the PostGIS
// Find_SRID mixed-SRID failure documented in docs/crs.md.
func checkTableSRIDs(db *sql.DB, tablename, geomFieldname, layerName string) error {
	rows, err := db.Query(sridConsistencySQL(tablename, geomFieldname))
	if err != nil {
		return fmt.Errorf("layer '%v' (table %v): cannot determine the geometry column SRIDs: %w", layerName, tablename, err)
	}
	defer func() { _ = rows.Close() }()

	var distinct int
	for rows.Next() {
		var srid sql.NullInt64
		if err := rows.Scan(&srid); err != nil {
			return fmt.Errorf("layer '%v' (table %v): cannot scan the geometry column SRIDs: %w", layerName, tablename, err)
		}
		distinct++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("layer '%v' (table %v): cannot read the geometry column SRIDs: %w", layerName, tablename, err)
	}
	if distinct > 1 {
		return fmt.Errorf("layer '%v' (table %v): geometry column %v mixes multiple SRIDs; "+
			"set an explicit %v or %v to declare the layer CRS",
			layerName, tablename, geomFieldname, ConfigKeySRID, ConfigKeyCRSDefn)
	}
	return nil
}

func NewTileProvider(config dict.Dicter, maps []provider.Map) (provider.Tiler, error) {

	log.Debugf("config: %v", config)

	host, err := config.String(ConfigKeyHost, nil)
	if err != nil {
		return nil, fmt.Errorf("mysql provider requires %v", ConfigKeyHost)
	}

	port := DefaultPort
	if port, err = config.Int(ConfigKeyPort, &port); err != nil {
		return nil, err
	}

	database, err := config.String(ConfigKeyDatabase, nil)
	if err != nil {
		return nil, fmt.Errorf("mysql provider requires %v", ConfigKeyDatabase)
	}

	user, err := config.String(ConfigKeyUser, nil)
	if err != nil {
		return nil, fmt.Errorf("mysql provider requires %v", ConfigKeyUser)
	}

	password, err := config.String(ConfigKeyPassword, nil)
	if err != nil {
		return nil, fmt.Errorf("mysql provider requires %v", ConfigKeyPassword)
	}

	maxConn := 100
	if maxConn, err = config.Int(ConfigKeyMaxConn, &maxConn); err != nil {
		return nil, err
	}

	// provider-level srid/crs_defn via the shared CRS contract. An explicit
	// value takes precedence over any SRID decoded from geometry headers,
	// which can be 0 or carry MariaDB axis-order flags.
	pcrs, perr := crsconfig.ResolveProvider(config, DefaultSRID)
	if perr != nil {
		return nil, perr
	}
	srid := pcrs.SRID
	sridExplicit := pcrs.Explicit

	geometryFormat := GeometryFormatAuto
	if geometryFormat, err = config.String(ConfigKeyGeometryFormat, &geometryFormat); err != nil {
		return nil, err
	}
	switch geometryFormat {
	case GeometryFormatAuto, GeometryFormatMySQL, GeometryFormatMariaDB, GeometryFormatWKB, GeometryFormatWKT, GeometryFormatMOS, "":
	default:
		return nil, fmt.Errorf("invalid %v: %v (expected one of: %v, %v, %v, %v, %v, %v)",
			ConfigKeyGeometryFormat, geometryFormat, GeometryFormatAuto, GeometryFormatMySQL, GeometryFormatMariaDB, GeometryFormatWKB, GeometryFormatWKT, GeometryFormatMOS)
	}

	// mos_precision/mos_units: number of decimal digits quantized MOS blob
	// coordinates carry (e.g. 3 = metre units with millimetre precision) and
	// the linear unit of the dequantized coordinates. Only used with
	// geometry_format = "mos". The MOS format itself carries no CRS
	// information, so the coordinate units come from the layer/provider
	// srid (or crs_defn). Resolution (layer > provider, explicit flags,
	// validation) lives in the shared geometrycodec contract.
	mosCfg, err := codec.ResolveMOSConfig(config, nil, "")
	if err != nil {
		return nil, err
	}
	// NOTE: mos_precision/mos_units are NOT rejected here even when the
	// provider-level geometry_format is not "mos": they are provider-level
	// defaults for layers that may still select the MOS format via their own
	// layer-level geometry_format. Relevance (warn) is decided per layer,
	// after the effective layer format is known.

	// register the built-in table of common projected SRIDs (UTM zones,
	// Pulkovo Gauss-Kruger) so any of them can be used as a layer srid
	// without extra configuration. Additional CRS definitions are supplied
	// per provider/layer via crs_defn, matching the other standard providers.
	basic.RegisterBuiltinProj4SRIDs()

	dsn := fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?parseTime=true&multiStatements=true",
		user, password, host, port, database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("unable to open mysql connection to %v:%v/%v: %v", host, port, database, err)
	}
	keepDB := false
	defer func() {
		if !keepDB {
			_ = db.Close()
		}
	}()
	db.SetMaxOpenConns(maxConn)
	maxIdle := maxConn
	if maxIdle <= 0 {
		maxIdle = 2
	}
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(mysqlConnMaxIdleTime)
	db.SetConnMaxLifetime(mysqlConnMaxLifetime)

	// detect whether the server is MySQL or MariaDB. This drives the "auto"
	// geometry format: the two servers store geometry columns in different
	// native internal layouts. If detection fails (e.g. the server is
	// unreachable), fall back to MySQL layout without failing provider start -
	// decodeGeometry's WKB fallback keeps decoding working for either flavor.
	serverFlavor := GeometryFormatMySQL
	if flavor, err := detectServerFlavor(db); err != nil {
		log.Warnf("unable to detect server flavor, defaulting to MySQL geometry layout: %v", err)
	} else {
		serverFlavor = flavor
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("unable to connect to mysql server %v:%v/%v: %v", host, port, database, err)
	}

	p := Provider{
		Host:           host,
		Port:           port,
		Database:       database,
		layers:         make(map[string]Layer),
		db:             db,
		srid:           uint64(srid),
		geometryFormat: geometryFormat,
		serverFlavor:   serverFlavor,
	}

	layers, err := config.MapSlice(ConfigKeyLayers)
	if err != nil {
		return nil, err
	}

	lyrsSeen := make(map[string]int)
	for i, layerConf := range layers {

		layerName, err := layerConf.String(ConfigKeyLayerName, nil)
		if err != nil {
			return nil, fmt.Errorf("for layer (%v) we got the following error trying to get the layer's name field: %v", i, err)
		}
		if layerName == "" {
			return nil, ErrMissingLayerName
		}

		// check if we have already seen this layer
		if j, ok := lyrsSeen[layerName]; ok {
			return nil, fmt.Errorf("layer name (%v) is duplicated in both layer %v and layer %v", layerName, i, j)
		}
		lyrsSeen[layerName] = i

		// ensure only one of sql or tablename exist
		_, errTable := layerConf.String(ConfigKeyTableName, nil)
		if _, ok := errTable.(dict.ErrKeyRequired); errTable != nil && !ok {
			return nil, errTable
		}
		_, errSQL := layerConf.String(ConfigKeySQL, nil)
		if _, ok := errSQL.(dict.ErrKeyRequired); errSQL != nil && !ok {
			return nil, errSQL
		}
		// err != nil <-> key != exists
		if errTable != nil && errSQL != nil {
			return nil, errors.New("'tablename' or 'sql' is required for a feature's config")
		}
		// err == nil <-> key == exists
		if errTable == nil && errSQL == nil {
			return nil, errors.New("'tablename' or 'sql' is required for a feature's config")
		}

		// A02: track whether id_fieldname was explicitly configured. A
		// detected MapplGIS table overrides the default ID field with the
		// contract primary key (OKEY); an explicit value is honored.
		const idFieldDefault = "\x00tegola:default"
		var idFieldname string
		idFieldname = idFieldDefault
		if idFieldname, err = layerConf.String(ConfigKeyGeomIDField, &idFieldname); err != nil {
			return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
		}
		idFieldExplicit := idFieldname != idFieldDefault
		if !idFieldExplicit {
			idFieldname = DefaultIDFieldName
		}

		tagFieldnames, err := layerConf.StringSlice(ConfigKeyFields)
		if err != nil { // empty slices are okay
			return nil, fmt.Errorf("for layer (%v) %v, %q field had the following error: %v", i, layerName, ConfigKeyFields, err)
		}

		geomFieldname := DefaultGeomFieldName
		if geomFieldname, err = layerConf.String(ConfigKeyGeomField, &geomFieldname); err != nil {
			return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
		}

		// layer-level srid/crs_defn via the shared CRS contract. The
		// provider-level value is the fallback until source metadata
		// (header SRID / system info) is inspected in the branches below.
		lcrs, cerr := crsconfig.ResolveLayer(layerConf, srid)
		if cerr != nil {
			return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, cerr)
		}

		// layer container. will be added to the provider after it's configured
		layer := Layer{
			name:           layerName,
			idFieldname:    idFieldname,
			geomFieldname:  geomFieldname,
			srid:           uint64(lcrs.SRID),
			geometryFormat: geometryFormat,
			crsExplicit:    sridExplicit || lcrs.Explicit,
			mosConfig:      mosCfg,
		}

		// layer-level geometry_format overrides the provider-level value.
		// Validated against the full MySQL value set with the layer name in
		// the error context.
		layerGeometryFormat, gerr := codec.ResolveLayerGeometryFormat(geometryFormat, layerConf, layerName, mysqlGeometryFormats)
		if gerr != nil {
			return nil, gerr
		}
		layer.geometryFormat = layerGeometryFormat

		// common geometry_type key: an explicit value fixes the layer type
		// before any data is read and skips startup type inspection.
		explicitGeomType, gtypeExplicit, terr := codec.ResolveGeometryType(layerConf, layerName)
		if terr != nil {
			return nil, terr
		}
		if gtypeExplicit {
			layer.geomType = explicitGeomType
			layer.geomTypeExplicit = true
		}

		// layer-level mos_precision/mos_units override the provider-level
		// values atomically (value + explicit flag) through the shared
		// resolution contract.
		layerMosCfg, lerr := codec.ResolveMOSConfig(nil, layerConf, "")
		if lerr != nil {
			return nil, fmt.Errorf("for layer (%v) %v %v", i, layerName, lerr)
		}
		layer.mosConfig = codec.MergeMOSConfig(layer.mosConfig, layerMosCfg)

		// Bounds-backed MOS SQL: resolve the bounds field names with the
		// common layer > provider > defaults precedence. Used by the
		// !BBOX! predicate for bounds-backed MOS custom SQL and excluded
		// from feature tags.
		layer.bboxFields, err = codec.ResolveBBoxFields(config, layerConf, layerName)
		if err != nil {
			return nil, err
		}

		// The effective layer format is now known: MOS quantization settings
		// are irrelevant for explicitly raw formats. With geometry_format
		// auto they are kept, since a runtime MapplGIS LayerInfo blob can
		// still switch the layer to the MOS format.
		codec.WarnAndResetMOSParams(layerGeometryFormat, &layer.mosConfig, layerName, GeometryFormatAuto)

		if errTable == nil { // layerConf[ConfigKeyTableName] exists
			// the tablename lookup takes no default: the key is known to
			// exist here, and the old &idFieldname argument was a
			// copy-paste that made an unrelated field the fallback value
			// (audit N9).
			tablename, err := layerConf.String(ConfigKeyTableName, nil)
			if err != nil {
				return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
			}

			// one-time MapplGIS table detection (provider/mapplgis contract):
			// runs for every tablename layer, including ones with an explicit
			// geometry_type, since detection and declared geometry types are
			// orthogonal concerns. Tile requests never repeat this discovery.
			mInfo, derr := detectMapplGIS(db, tablename)
			if derr != nil {
				return nil, fmt.Errorf("layer '%v' (table %v): %v", layerName, tablename, derr)
			}
			if mInfo.IsMapplGIS {
				layer.isMapplGIS = true
				layer.mapplSource = codec.MapplGISTableCanonical
				layer.mapplSysInfo = mInfo.SystemInfo
				layer.geomFieldname = mapplgis.GeometryField
				// A02: the MapplGIS contract fixes the ID field to the
				// table's primary key column; only an explicit id_fieldname
				// keeps a custom value.
				if !idFieldExplicit {
					idFieldname = mapplgis.PrimaryKey
				}
			}

			if gtypeExplicit {
				// an explicit geometry_type skips startup inspection: the
				// declared type wins over any sampled inference. The layer
				// CRS contract still applies: a layer-level srid/crs_defn
				// overrides the provider-level default even though no
				// geometry header is decoded. Table identity (tablename/
				// fields) must still be recorded so TileFeatures takes the
				// table path instead of falling through to an empty custom
				// SQL. A detected MapplGIS table still applies its layer
				// self-description (format, precision, projection) behind
				// explicit-config precedence.
				if mInfo.IsMapplGIS {
					if err := applySystemInfo(&layer, &layer.mapplSysInfo, sridExplicit || lcrs.Explicit); err != nil {
						return nil, fmt.Errorf("layer '%v' (table %v): %v", layerName, tablename, err)
					}
				}
				lcrs, rerr := crsconfig.ResolveLayer(layerConf, srid)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				layer.srid = uint64(lcrs.SRID)
				layer.crsExplicit = sridExplicit || lcrs.Explicit
				layer.tablename = tablename
				layer.tagFieldnames = tagFieldnames
				layer.idFieldname = idFieldname
				p.layers[layer.name] = layer
				continue
			}

			// a detected MapplGIS table skips startup geometry sampling: its
			// geometry column and layer self-description are already known.
			if mInfo.IsMapplGIS {
				if err := applySystemInfo(&layer, &layer.mapplSysInfo, sridExplicit || lcrs.Explicit); err != nil {
					return nil, fmt.Errorf("layer '%v' (table %v): %v", layerName, tablename, err)
				}
				layer.tablename = tablename
				layer.tagFieldnames = tagFieldnames
				layer.idFieldname = idFieldname
				p.layers[layer.name] = layer
				continue
			}

			// verify the table exists and sample its geometry to learn the
			// geometry type and SRID
			inspectionSQL := fmt.Sprintf("SELECT %v FROM %v WHERE %v IS NOT NULL LIMIT 1",
				quoteIdentifier(geomFieldname), quoteIdentifier(tablename), quoteIdentifier(geomFieldname))

			geo, headerSRID, err := geomTypeFromColumn(db, inspectionSQL, layerGeometryFormat, serverFlavor, layer.mosConfig)
			switch {
			case err == sql.ErrNoRows:
				layer.deferredInspection = true
				log.Warnf("layer '%v' (table %v) currently returns 0 rows; registering it without an inferred geometry type", layerName, tablename)
				layer.tablename = tablename
				layer.tagFieldnames = tagFieldnames
				p.layers[layer.name] = layer
				continue

			case err != nil:
				return nil, fmt.Errorf("layer '%v' problem inspecting table %v: %v", layerName, tablename, err)

			default:
				// an explicit provider-level or layer-level srid always wins over
				// the value decoded from the geometry header.
				layerSRID := srid
				if !sridExplicit && headerSRID > 0 {
					layerSRID = int(headerSRID)
				}

				lcrs, rerr := crsconfig.ResolveLayer(layerConf, layerSRID)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				lsrid := lcrs.SRID
				layer.crsExplicit = sridExplicit || lcrs.Explicit

				// audit N8: when the layer SRID is auto-derived, one sampled
				// row must not silently represent a table that mixes SRIDs.
				// An explicit srid/crs_defn (provider or layer level) already
				// declares the CRS and skips the probe.
				if !layer.crsExplicit && shouldProbeTableSRIDs(layerGeometryFormat, headerSRID) {
					if perr := checkTableSRIDs(db, tablename, geomFieldname, layerName); perr != nil {
						return nil, perr
					}
				}

				layer.tablename = tablename
				layer.tagFieldnames = tagFieldnames
				layer.geomType = geo
				layer.srid = uint64(lsrid)

				// MapplGIS layer self-description already applied above via
				// the one-time provider/mapplgis detection; startup sampling
				// carries no system-info contract anymore.
			}

		} else { // layerConf[ConfigKeySQL] exists
			var customSQL string
			if customSQL, err = layerConf.String(ConfigKeySQL, &customSQL); err != nil {
				return nil, fmt.Errorf("for %v layer(%v) %v has an error: %v", i, layerName, ConfigKeySQL, err)
			}
			layer.sql = customSQL

			// Raw custom-SQL contract: wkb/wkt cannot use the native-spatial
			// !BBOX! token; reject it up front instead of generating invalid
			// per-tile SQL. Bounds-backed MOS custom SQL is permitted — and
			// required to carry !BBOX! — because the token expands into the
			// configured bounds-fields predicate. The auto format is exempt
			// from both rules: runtime inspection may resolve the column to
			// a native spatial type for which !BBOX! is valid.
			if verr := codec.ValidateRawCustomSQL(layerName, layerGeometryFormat, customSQL, conf.BboxToken, "!BOX!"); verr != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, verr)
			}
			if rerr := codec.RequireBBoxCustomSQL(layerName, layerGeometryFormat == codec.FormatMOS, customSQL, conf.BboxToken, "!BOX!"); rerr != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, rerr)
			}

			// Shared probe preparation: the probe always executes the SQL
			// without a spatial filter (!BBOX!/!BOX! -> 1=1) and with
			// permissive position/zoom placeholders, applied in the ONE
			// documented order of codec.PrepareProbeSQL.
			var probeGeomType string
			if layer.geomType != nil {
				probeGeomType = codec.GeomTypeName(layer.geomType)
			}
			inspectionSQL := codec.PrepareProbeSQL(customSQL, layer.geomFieldname, layer.idFieldname, probeGeomType)
			probeSQL := codec.WrapProbeSQL(inspectionSQL)

			// Bounds-backed storage-format probe. It runs for explicit MOS
			// and for inference (auto), INCLUDING tile-dependent SQL and
			// layers with an explicit geometry_type: the structural
			// result-column contract is always validated at registration
			// and never skipped (the >=3-sample-row evidence bar is
			// inference-only).
			if layerGeometryFormat == codec.FormatMOS || layerGeometryFormat == GeometryFormatAuto || layerGeometryFormat == "" {
				columns, contract, perr := probeMOSCustomSQLContract(db, &layer, probeSQL, layerGeometryFormat, serverFlavor)
				strict := layerGeometryFormat == codec.FormatMOS
				switch {
				case perr != nil && strict:
					return nil, fmt.Errorf("layer '%v' problem probing bounds-backed MOS custom SQL: %v", layerName, perr)

				case perr != nil:
					log.Warnf("layer '%v': custom SQL storage-format probe failed; format not detected: %v", layerName, perr)

				default:
					// >=3 decodable MOS rows (positive MOS signature only)
					// is the sql-sample evidence bar; the structural
					// contract is fail-closed for explicit MOS and for
					// inference with MOS evidence (A02/A05).
					mosEvidence := contract.ValidMOSRows >= codec.MinValidMOSRows
					if strict || mosEvidence {
						if cerr := codec.ValidateBoundsSQLContract(layerName, customSQL, layer.geomFieldname, contract); cerr != nil {
							return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, cerr)
						}
						// persist the ACTUAL result-column names (A09):
						// runtime predicates quote identifiers
						// case-sensitively.
						layer.bboxFields = contract.BoundsFields
						if contract.GeometryField != "" {
							layer.geomFieldname = contract.GeometryField
						}
						if mosEvidence {
							layer.isMapplGIS = true
							layer.mapplSource = codec.MapplGISSQLSample
							if !strict {
								// inference resolves the effective format
								// to MOS (A04/A07)
								layer.geometryFormat = codec.FormatMOS
								layerGeometryFormat = codec.FormatMOS
							}
							log.Infof("layer '%v': bounds-backed MOS custom SQL contract detected (source %v, %v valid MOS sample rows)", layerName, layer.mapplSource, contract.ValidMOSRows)
						} else if !gtypeExplicit {
							log.Warnf("layer '%v': bounds-backed MOS custom SQL sample carried %v decodable MOS rows (need %v for sql-sample tagging)", layerName, contract.ValidMOSRows, codec.MinValidMOSRows)
						}
						// MOS blobs carry no CRS metadata: the source CRS
						// must be configured explicitly (A11).
						if merr := codec.ValidateMOSSQLExplicitConfig(layerName, layer.crsExplicit); merr != nil {
							return nil, fmt.Errorf("layer '%v': %v", layerName, merr)
						}
					} else {
						// inference without usable MOS evidence ends
						// "not detected"; the 3-row sample is best-effort.
						log.Warnf("layer '%v': custom SQL storage format not detected (sample columns: %v; %v decodable MOS sample rows); registering with format %q", layerName, strings.Join(columns, ", "), contract.ValidMOSRows, layerGeometryFormat)
					}
				}
			}

			// warn-only 1.3 policy: the scale/pixel tokens are computed as
			// Web Mercator metres regardless of the layer CRS.
			codec.WarnNonMetricScaleTokens(layerName, customSQL, uint32(layer.srid), config, layerConf)

			// An explicit geometry_type skips only geometry-class
			// inference and the >=3-sample-row evidence bar, never the
			// structural validation above (A03). Empty result sets are
			// allowed for explicitly-typed layers.
			if gtypeExplicit {
				p.layers[layer.name] = layer
				continue
			}

			// Tile-dependent SQL may filter out everything at the sample
			// tile, so defer geometry-type inference; the structural
			// result-column check above has already run (A08).
			if customSQLNeedsDeferredInspection(customSQL) {
				layer.deferredInspection = true
				log.Warnf("layer '%v' uses tile-dependent custom SQL; deferring startup geometry inspection", layerName)
				p.layers[layer.name] = layer
				continue
			}

			// MySQL-derived tables require an alias
			qtext := fmt.Sprintf("SELECT %v FROM (%v) AS __tegola_inspection LIMIT 1;",
				quoteIdentifier(geomFieldname), inspectionSQL)

			log.Debugf("qtext: %v", qtext)

			geo, headerSRID, err := geomTypeFromColumn(db, qtext, layerGeometryFormat, serverFlavor, layer.mosConfig)
			switch {
			case err == sql.ErrNoRows:
				layer.deferredInspection = true
				log.Warnf("layer '%v' with custom SQL currently returns 0 rows; registering it without an inferred geometry type: %v", layerName, customSQL)
				p.layers[layer.name] = layer
				continue

			case err != nil:
				return nil, fmt.Errorf("layer '%v' problem executing custom SQL: %v", layerName, err)

			default:
				// an explicit provider-level or layer-level srid always wins over
				// the value decoded from the geometry header.
				layerSRID := srid
				if !sridExplicit && headerSRID > 0 {
					layerSRID = int(headerSRID)
				}

				lcrs, rerr := crsconfig.ResolveLayer(layerConf, layerSRID)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				lsrid := lcrs.SRID
				layer.crsExplicit = sridExplicit || lcrs.Explicit

				layer.geomType = geo
				layer.srid = uint64(lsrid)
				layer.idFieldname = idFieldname

				// Custom SQL layers never auto-detect as MapplGIS tables
				// (canonical one-time detection contract): no system-info
				// blob is parsed here, and tile requests never apply runtime
				// detection either. Configure these layers explicitly.
			}
		}

		p.layers[layer.name] = layer
	}

	// track the provider so we can clean it up later
	providersMu.Lock()
	providers = append(providers, p)
	providersMu.Unlock()
	keepDB = true

	return &p, nil
}

// probeMOSCustomSQLContract runs the pre-wrapped probe SQL (token-expanded
// by codec.PrepareProbeSQL — no spatial filter — and wrapped by
// codec.WrapProbeSQL) and runs the common InspectSQLGeometryContract probe
// over the real scanned row values: bounds columns presence plus at least
// MinValidMOSRows decodable MOS geometries with coordinates. It returns the
// sample column names for diagnostics. SystemInfo rows are skipped, never
// applied: SQL-sample detection carries no projection contract.
func probeMOSCustomSQLContract(db *sql.DB, layer *Layer, probeSQL string, geometryFormat string, serverFlavor string) ([]string, codec.SQLGeometryContract, error) {
	rows, err := db.Query(probeSQL)
	if err != nil {
		return nil, codec.SQLGeometryContract{}, err
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, codec.SQLGeometryContract{}, err
	}

	// A01 fix: really scan the row values — placeholder pointers without
	// rows.Scan never carry data into the shared probe. next() feeds the
	// inspector with the actual scanned values.
	next := func() ([]interface{}, bool, error) {
		if !rows.Next() {
			return nil, false, rows.Err()
		}
		dest := make([]interface{}, len(columns))
		for i := range dest {
			dest[i] = new(interface{})
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, false, err
		}
		vals := make([]interface{}, len(dest))
		for i := range dest {
			vals[i] = *(dest[i].(*interface{}))
		}
		return vals, true, nil
	}

	// Explicit MOS counts only positive-signature MOS rows; inference
	// accepts native/WKB/WKT rows first and falls back to MOS only for
	// blobs with a positive MOS signature (7.1.3). SystemInfo rows are
	// skipped, never applied: SQL-sample detection carries no projection
	// contract.
	var decode codec.RowDecode
	if geometryFormat == GeometryFormatMOS {
		decode = codec.MOSRowDecode(layer.mosConfig)
	} else {
		decode = codec.AutoRowDecode(func(value interface{}) (geom.Geometry, error) {
			if value == nil {
				return nil, fmt.Errorf("nil geometry value")
			}
			_, g, err := decodeGeometry(value, geometryFormat, serverFlavor, layer.mosConfig)
			return g, err
		}, layer.mosConfig)
	}

	contract, err := codec.InspectSQLGeometryContract(next, columns, layer.geomFieldname, layer.bboxFields, decode)
	if err != nil {
		return columns, codec.SQLGeometryContract{}, err
	}
	return columns, contract, nil
}

// showIndexRow is one parsed row of SHOW INDEX output.
type showIndexRow struct {
	keyName     string
	seqInIndex  int
	columnName  string
}

// parseShowIndexRows scans arbitrary SHOW INDEX result rows (the column set
// varies by server: MySQL 5.7 emits 13 columns, MySQL 8.0 adds Expression
// and Visible for 15, MariaDB adds others) and resolves the required
// Key_name / Seq_in_index / Column_name columns by name. It is split from
// detectMapplGIS so version-sensitive parsing can be regression tested
// without a live server (audit A08).
func parseShowIndexRows(rows *sql.Rows) ([]showIndexRow, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("reading SHOW INDEX column names: %v", err)
	}
	keyIdx, seqIdx, colIdx := -1, -1, -1
	for i, name := range columns {
		switch name {
		case "Key_name":
			keyIdx = i
		case "Seq_in_index":
			seqIdx = i
		case "Column_name":
			colIdx = i
		}
	}
	if keyIdx < 0 || seqIdx < 0 || colIdx < 0 {
		return nil, fmt.Errorf("SHOW INDEX result misses required columns (Key_name, Seq_in_index, Column_name); got %v", columns)
	}

	// scan every reported column so any server version works
	dest := make([]any, len(columns))
	for i := range dest {
		dest[i] = new(sql.NullString)
	}

	var parsed []showIndexRow
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scanning SHOW INDEX row: %v", err)
		}
		keyName := dest[keyIdx].(*sql.NullString).String
		seqStr := dest[seqIdx].(*sql.NullString).String
		colName := dest[colIdx].(*sql.NullString).String
		seq, cerr := strconv.Atoi(seqStr)
		if cerr != nil {
			return nil, fmt.Errorf("parsing Seq_in_index %q: %v", seqStr, cerr)
		}
		parsed = append(parsed, showIndexRow{keyName: keyName, seqInIndex: seq, columnName: colName})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SHOW INDEX rows: %v", err)
	}
	return parsed, nil
}

// detectMapplGIS collects the schema metadata required by the canonical
// MapplGIS table contract (provider/mapplgis) for the given table and runs
// the one-time detection:
//   - SHOW COLUMNS supplies the DDL column list;
//   - SHOW INDEX supplies the primary key columns and every index with its
//     ordered column list (the column set of SHOW INDEX varies by server
//     version, so the required columns are resolved by name);
//   - the OKEY = 1 row's LINE value is parsed as the layer
//     self-description blob.
//
// It runs exactly once per tablename layer at provider registration; tile
// requests never repeat this discovery.
func detectMapplGIS(db *sql.DB, tablename string) (mapplgis.Info, error) {
	// DDL columns.
	colRows, err := db.Query(fmt.Sprintf("SHOW COLUMNS FROM %v", quoteIdentifier(tablename)))
	if err != nil {
		return mapplgis.Info{}, fmt.Errorf("unable to list columns of table %v: %v", tablename, err)
	}
	defer func() { _ = colRows.Close() }()

	var meta mapplgis.TableMeta
	for colRows.Next() {
		var field, colType string
		var nullVal, keyVal, extra sql.NullString
		var def sql.NullString
		if err := colRows.Scan(&field, &colType, &nullVal, &keyVal, &def, &extra); err != nil {
			return mapplgis.Info{}, fmt.Errorf("unable to scan columns of table %v: %v", tablename, err)
		}
		meta.Columns = append(meta.Columns, field)
	}
	if err := colRows.Err(); err != nil {
		return mapplgis.Info{}, fmt.Errorf("error iterating columns of table %v: %v", tablename, err)
	}

	idxRows, err := db.Query(fmt.Sprintf("SHOW INDEX FROM %v", quoteIdentifier(tablename)))
	if err != nil {
		return mapplgis.Info{}, fmt.Errorf("unable to list indexes of table %v: %v", tablename, err)
	}
	defer func() { _ = idxRows.Close() }()

	parsed, perr := parseShowIndexRows(idxRows)
	if perr != nil {
		return mapplgis.Info{}, fmt.Errorf("table %v: %v", tablename, perr)
	}
	if err := idxRows.Err(); err != nil {
		return mapplgis.Info{}, fmt.Errorf("error iterating indexes of table %v: %v", tablename, err)
	}

	indexes := make(map[string]*mapplgis.IndexMeta)
	for _, row := range parsed {
		idx, ok := indexes[row.keyName]
		if !ok {
			idx = &mapplgis.IndexMeta{Name: row.keyName}
			indexes[row.keyName] = idx
		}
		idx.Columns = append(idx.Columns, row.columnName)
	}
	// keep PK rows ordered by Seq_in_index and store every PK column so
	// composite primary keys are represented exactly.
	var pkRows []showIndexRow
	for _, row := range parsed {
		if strings.EqualFold(row.keyName, "PRIMARY") {
			pkRows = append(pkRows, row)
		}
	}
	sort.Slice(pkRows, func(a, b int) bool { return pkRows[a].seqInIndex < pkRows[b].seqInIndex })
	for _, row := range pkRows {
		meta.PrimaryKeyColumns = append(meta.PrimaryKeyColumns, row.columnName)
	}
	for _, idx := range indexes {
		meta.Indexes = append(meta.Indexes, *idx)
	}

	// The OKEY = 1 metadata row: LINE is the required geometry column
	// literal of the MapplGIS contract; OKEY the required primary key.
	fetch := func() (*mos.SystemInfo, error) {
		var blob []byte
		err := db.QueryRow(fmt.Sprintf(
			"SELECT %v FROM %v WHERE OKEY = 1 AND %v IS NOT NULL LIMIT 1",
			quoteIdentifier(mapplgis.GeometryField), quoteIdentifier(tablename), quoteIdentifier(mapplgis.GeometryField),
		)).Scan(&blob)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
		if len(blob) == 0 {
			return nil, nil
		}
		si, perr := mos.ParseSystemInfo(blob)
		if perr != nil {
			return nil, perr
		}
		return &si, nil
	}

	return mapplgis.Detect(meta, fetch)
}

// mapplGISFormatAuthority enforces the audit A04 contract: a successful
// MapplGIS detection is authoritative about the storage format. The
// effective geometry format becomes MOS unless the user explicitly chose a
// conflicting raw format, which is a startup error instead of a silent
// runtime decoder mismatch.
func mapplGISFormatAuthority(layerName string, format string) error {
	switch format {
	case "", GeometryFormatAuto, GeometryFormatMOS:
		return nil
	default:
		return fmt.Errorf(
			"layer '%v': table is detected as a MapplGIS layer (MOS storage), but geometry_format is explicitly %q; remove the setting or use %q",
			layerName, format, GeometryFormatMOS,
		)
	}
}

// resolveMapplGISGeometryFormat applies the audit A04 format authority: a
// detected MapplGIS table stores MOS blobs, so unset/auto formats switch to
// MOS and an explicit non-MOS format is a startup conflict error.
func resolveMapplGISGeometryFormat(layerName, format string) string {
	if err := mapplGISFormatAuthority(layerName, format); err != nil {
		// callers run mapplGISFormatAuthority first; this is a defensive
		// fallback that keeps the MOS format rather than a wrong decoder
		log.Warnf("%v", err)
	}
	return GeometryFormatMOS
}

// MapplGIS LayerInfo blob to the layer being registered. Only values not
// explicitly configured take effect:
//   - precision: used when neither provider- nor layer-level mos_precision
//     is set;
//   - units: used when neither provider- or layer-level mos_units is set;
//   - projection: registered as a synthetic SRID when no srid/crs_defn is
//     set at provider or layer level (crsExplicit covers both).
//
// crsExplicit is the combined provider- and layer-level CRS explicit flag
// from provider/crsconfig resolution. nil sysInfo (no system info blob in
// the table) is a no-op.
func applySystemInfo(layer *Layer, sysInfo *mos.SystemInfo, crsExplicit bool) error {
	if sysInfo == nil {
		return nil
	}
	// A detected MapplGIS table is authoritative about its storage format
	// (audit A04): unset/auto resolve to MOS; an explicitly configured
	// non-MOS format conflicts with the table contract and must fail at
	// startup rather than decode MOS blobs with the wrong reader.
	if err := mapplGISFormatAuthority(layer.name, layer.geometryFormat); err != nil {
		return err
	}
	layer.geometryFormat = GeometryFormatMOS

	if err := layer.mosConfig.ApplySystemInfo(sysInfo); err != nil {
		return err
	}

	layer.crsExplicit = crsExplicit

	// projection: register the blob's PROJ.4 definition as the layer SRID
	// when no provider- or layer-level CRS was selected.
	if srid, applied, err := crsconfig.ApplySystemInfoCRS(int(layer.srid), crsExplicit, sysInfo.Projection); err != nil {
		return fmt.Errorf("unable to register layer projection %q: %v", sysInfo.Projection, err)
	} else if applied {
		layer.srid = uint64(srid)
	}

	if sysInfo.MapUnitsDefined {
		log.Debugf("layer %v system info: precision=%v map units=%v factor=%v projection=%q",
			layer.name, sysInfo.Precision, sysInfo.MapUnits, layer.mosConfig.UnitFactor, sysInfo.Projection)
	} else {
		log.Debugf("layer %v system info: precision=%v projection=%q",
			layer.name, sysInfo.Precision, sysInfo.Projection)
	}
	return nil
}

// applyLayerCRSDefn was replaced by the shared provider/crsconfig resolver,
// which implements the same crs_defn > srid > fallback precedence together
// with explicit-flag tracking for every standard provider.

// parseProj4ConfigValue was removed along with the public "proj4" config
// key: custom CRS definitions are supplied via srid/crs_defn, matching the
// other standard providers.
