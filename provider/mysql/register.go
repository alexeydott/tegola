package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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

		idFieldname := DefaultIDFieldName
		if idFieldname, err = layerConf.String(ConfigKeyGeomIDField, &idFieldname); err != nil {
			return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
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

		// The effective layer format is now known: MOS quantization settings
		// are irrelevant for explicitly raw formats. With geometry_format
		// auto they are kept, since a runtime MapplGIS LayerInfo blob can
		// still switch the layer to the MOS format.
		codec.WarnAndResetMOSParams(layerGeometryFormat, &layer.mosConfig, layerName, GeometryFormatAuto)

		if errTable == nil { // layerConf[ConfigKeyTableName] exists
			tablename, err := layerConf.String(ConfigKeyTableName, &idFieldname)
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
				layer.mapplSysInfo = mInfo.SystemInfo
				layer.geomFieldname = mapplgis.GeometryField
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

			// Raw custom-SQL contract: raw formats (wkb/wkt/mos) cannot use
			// the native-spatial !BBOX! token; reject it up front instead of
			// generating invalid per-tile SQL. The auto format is exempt:
			// runtime inspection may resolve the column to a native spatial
			// type for which !BBOX! is valid.
			if verr := codec.ValidateRawCustomSQL(layerName, layerGeometryFormat, customSQL, conf.BboxToken); verr != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, verr)
			}

			if gtypeExplicit {
				// an explicit geometry_type skips startup inspection for
				// custom SQL as well: no sampling query runs, so
				// tile-dependent SQL needs no deferred registration either.
				lcrs, rerr := crsconfig.ResolveLayer(layerConf, srid)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				layer.srid = uint64(lcrs.SRID)
				layer.crsExplicit = sridExplicit || lcrs.Explicit
				p.layers[layer.name] = layer
				continue
			}

			// if a !ZOOM! token exists, all features could be filtered out so we
			// don't have a geometry to inspect its type. Replace comparisons
			// against !ZOOM! with a permissive IN list and !BBOX! with 1=1 for
			// the inspection query, mirroring the gpkg provider.
			if customSQLNeedsDeferredInspection(customSQL) {
				layer.deferredInspection = true
				log.Warnf("layer '%v' uses tile-dependent custom SQL; deferring startup geometry inspection", layerName)
				p.layers[layer.name] = layer
				continue
			}

			allZoomsSQL := "IN (0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24)"
			tokenReplacer := strings.NewReplacer(
				">= "+conf.ZoomToken, allZoomsSQL,
				">="+conf.ZoomToken, allZoomsSQL,
				"=> "+conf.ZoomToken, allZoomsSQL,
				"=>"+conf.ZoomToken, allZoomsSQL,
				"=< "+conf.ZoomToken, allZoomsSQL,
				"=<"+conf.ZoomToken, allZoomsSQL,
				"<= "+conf.ZoomToken, allZoomsSQL,
				"<="+conf.ZoomToken, allZoomsSQL,
				"!= "+conf.ZoomToken, allZoomsSQL,
				"!="+conf.ZoomToken, allZoomsSQL,
				"= "+conf.ZoomToken, allZoomsSQL,
				"="+conf.ZoomToken, allZoomsSQL,
				"> "+conf.ZoomToken, allZoomsSQL,
				">"+conf.ZoomToken, allZoomsSQL,
				"< "+conf.ZoomToken, allZoomsSQL,
				"<"+conf.ZoomToken, allZoomsSQL,
				conf.BboxToken, "1=1",
				"!BOX!", "1=1",
				"!bbox!", "1=1",
			)

			inspectionSQL := tokenReplacer.Replace(trimTrailingSemicolon(uppercaseTokens(customSQL)))
			inspectionTile := provider.NewTile(0, 0, 0, 0, uint(srid))
			inspectionExtent, _ := inspectionTile.BufferedExtent()
			inspectionSQL = replaceTokens(inspectionSQL, &layer, inspectionTile, inspectionExtent)

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

// detectMapplGIS collects the schema metadata required by the canonical
// MapplGIS table contract (provider/mapplgis) for the given table and runs
// the one-time detection:
//   - SHOW COLUMNS supplies the DDL column list;
//   - SHOW INDEX supplies the primary key and indexed columns;
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
	defer colRows.Close()
	var meta mapplgis.TableMeta
	for colRows.Next() {
		var field, colType string
		var nullVal, keyVal, extra sql.NullString
		var def sql.NullString
		if err := colRows.Scan(&field, &colType, &nullVal, &keyVal, &def, &extra); err != nil {
			return mapplgis.Info{}, fmt.Errorf("unable to scan columns of table %v: %v", tablename, err)
		}
		meta.Columns = append(meta.Columns, field)
		if err := colRows.Err(); err != nil {
			return mapplgis.Info{}, fmt.Errorf("error iterating columns of table %v: %v", tablename, err)
		}
	}
	if err := colRows.Err(); err != nil {
		return mapplgis.Info{}, fmt.Errorf("error iterating columns of table %v: %v", tablename, err)
	}

	// SHOW INDEX column count varies by server version (MySQL 8.0 returns
	// the legacy 13 columns plus `expression` and `visible`), so scan the
	// legacy prefix and discard any trailing columns dynamically.
	idxRows, err := db.Query(fmt.Sprintf("SHOW INDEX FROM %v", quoteIdentifier(tablename)))
	if err != nil {
		return mapplgis.Info{}, fmt.Errorf("unable to list indexes of table %v: %v", tablename, err)
	}
	defer idxRows.Close()
	indexed := make(map[string]struct{})
	for idxRows.Next() {
		vals := make([]any, 15)
		for i := range vals {
			vals[i] = new(sql.NullString)
		}
		if err := idxRows.Scan(vals...); err != nil {
			return mapplgis.Info{}, fmt.Errorf("unable to scan indexes of table %v: %v", tablename, err)
		}
		keyName := vals[2].(*sql.NullString).String
		seqInIndexStr := vals[3].(*sql.NullString).String
		columnName := vals[4].(*sql.NullString).String
		seqInIndex, err := strconv.Atoi(seqInIndexStr)
		if err != nil {
			return mapplgis.Info{}, fmt.Errorf("unable to parse seq_in_index of table %v: %v", tablename, err)
		}
		if keyName == "PRIMARY" && seqInIndex == 1 {
			meta.PKColumn = columnName
		}
		indexed[columnName] = struct{}{}
	}
	if err := idxRows.Err(); err != nil {
		return mapplgis.Info{}, fmt.Errorf("error iterating indexes of table %v: %v", tablename, err)
	}
	for column := range indexed {
		meta.IndexedColumns = append(meta.IndexedColumns, column)
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
	if layer.geometryFormat == GeometryFormatAuto {
		layer.geometryFormat = GeometryFormatMOS
	}

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
