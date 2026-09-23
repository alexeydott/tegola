package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
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
)

// ErrMissingLayerName is returned when a layer config is missing the 'name' key
var ErrMissingLayerName = errors.New("mysql: layer is missing 'name'")

func init() {
	provider.Register(provider.TypeStd.Prefix()+Name, NewTileProvider, Cleanup)
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
// will try before giving up. Some datasets (e.g. MapplBase exports) store a
// small fraction of rows in wrapper formats that the active geometry_format
// cannot decode, so a single-row sample would poison provider registration.
const geomTypeSampleRows = 16

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

func configuredLayerSRID(layerConf dict.Dicter, fallback int) (int, error) {
	lsrid := fallback
	var err error
	if lsrid, err = layerConf.Int(ConfigKeySRID, &lsrid); err != nil {
		return 0, err
	}
	if err = applyLayerCRSDefn(layerConf, &lsrid); err != nil {
		return 0, err
	}
	return lsrid, nil
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
// the given query and decodes the first one that succeeds. A
// TLayerSystemInfoRec version wrapper blob (the layer self-description
// MapplBase stores as the first row of a MOS table) is parsed and returned
// via sysInfo without terminating the sampling. It returns sql.ErrNoRows
// when the query yields no rows at all, and the decode error only when
// every sampled row failed to decode.
func geomTypeFromColumn(db *sql.DB, qtext string, geometryFormat string, serverFlavor string, mosPrecision float64) (geo geom.Geometry, headerSRID uint64, sysInfo *mos.SystemInfo, err error) {
	rows, err := db.Query(sampleGeometryQuery(qtext))
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()

	var lastErr error
	var values []interface{}
	for rows.Next() {
		var geomVal interface{}
		if err := rows.Scan(&geomVal); err != nil {
			return nil, 0, nil, err
		}
		if geomVal == nil {
			continue
		}
		// a layer system info blob describes the layer rather than being a
		// geometry; parse it and keep sampling for a real feature.
		if blob, ok := geomVal.([]byte); ok && mos.IsSystemInfoBlob(blob) {
			si, perr := mos.ParseSystemInfo(blob)
			if perr != nil {
				log.Warnf("mysql provider: unable to parse layer system info blob: %v", perr)
				continue
			}
			sysInfo = &si
			continue
		}
		values = append(values, geomVal)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}

	sampleFormat := geometryFormat
	if sampleFormat == GeometryFormatAuto && sysInfo != nil {
		// MapplBase MOS tables identify themselves with a system-info row.
		// Decode the sampled values only after scanning all rows so the marker
		// is honored even when the driver does not return it first.
		sampleFormat = GeometryFormatMOS
	}

	for _, geomVal := range values {
		srid, decoded, decodeErr := decodeGeometry(geomVal, sampleFormat, serverFlavor, mos.Options{Precision: mosPrecision})
		if decodeErr != nil {
			lastErr = decodeErr
			continue
		}
		return decoded, srid, sysInfo, nil
	}
	if lastErr != nil {
		return nil, 0, nil, fmt.Errorf("error decoding sampled geometry: %v", lastErr)
	}
	return nil, 0, nil, sql.ErrNoRows
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

	// check if the user explicitly configured a provider-level SRID. When they
	// did, that value takes precedence over any SRID decoded from geometry
	// headers, which can be 0 or carry MariaDB axis-order flags.
	_, sridExplicit := config.Interface(ConfigKeySRID)
	srid := DefaultSRID
	if srid, err = config.Int(ConfigKeySRID, &srid); err != nil {
		return nil, err
	}

	// crs_defn: a full PROJ.4 definition used instead of a numeric SRID. When
	// present it wins over srid and is registered under a synthetic SRID that
	// flows through the regular reprojection path.
	crsDefnDefault := ""
	var crsDefn string
	if crsDefn, err = config.String(ConfigKeyCRSDefn, &crsDefnDefault); err != nil {
		return nil, err
	}
	if strings.TrimSpace(crsDefn) != "" {
		defnSRID, rerr := basic.RegisterProj4Defn(crsDefn)
		if rerr != nil {
			return nil, fmt.Errorf("invalid %v: %v", ConfigKeyCRSDefn, rerr)
		}
		srid = int(defnSRID)
		sridExplicit = true
		log.Infof("registered %v as synthetic srid %v", ConfigKeyCRSDefn, defnSRID)
	}

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

	// mos_precision: number of decimal digits quantized MOS blob coordinates
	// carry (e.g. 3 = metre units with millimetre precision). Only used with
	// geometry_format = "mos". The MOS format itself carries no CRS
	// information, so the coordinate units come from the layer/provider
	// srid (or crs_defn).
	_, mosPrecisionExplicit := config.Interface(ConfigKeyMOSPrecision)
	mosPrecision := mosPrecisionDefault
	if mosPrecision, err = config.Float(ConfigKeyMOSPrecision, &mosPrecision); err != nil {
		return nil, err
	}
	if err := validateMOSPrecision(mosPrecision); err != nil {
		return nil, fmt.Errorf("invalid %v: %w", ConfigKeyMOSPrecision, err)
	}
	if geometryFormat != GeometryFormatMOS {
		if mosPrecision != mosPrecisionDefault {
			log.Warnf("%v is only used with %v = %q; ignoring", ConfigKeyMOSPrecision, ConfigKeyGeometryFormat, GeometryFormatMOS)
			mosPrecision = mosPrecisionDefault
		}
	}

	// mos_units identifies the linear units used by quantized MOS
	// coordinates. Decoded coordinates are converted to metres before they
	// enter the SRID reprojection path.
	mosUnitsFactor := mosUnitsFactorDefault
	mosUnitsExplicit := false
	mosUnitsName := ""
	if mosUnitsName, err = config.String(ConfigKeyMOSUnits, &mosUnitsName); err != nil {
		return nil, err
	}
	if strings.TrimSpace(mosUnitsName) != "" {
		units, uerr := mos.ParseMapUnits(mosUnitsName)
		if uerr != nil {
			return nil, fmt.Errorf("invalid %v: %v", ConfigKeyMOSUnits, uerr)
		}
		mosUnitsFactor, uerr = units.ToMetres()
		if uerr != nil {
			return nil, fmt.Errorf("invalid %v: %v", ConfigKeyMOSUnits, uerr)
		}
		mosUnitsExplicit = true
	}
	if geometryFormat != GeometryFormatMOS && mosUnitsExplicit {
		log.Warnf("%v is only used with %v = %q; ignoring", ConfigKeyMOSUnits, ConfigKeyGeometryFormat, GeometryFormatMOS)
		mosUnitsFactor = mosUnitsFactorDefault
	}

	// register the built-in table of common projected SRIDs (UTM zones,
	// Pulkovo Gauss-Kruger) so any of them can be used as a layer srid
	// without extra configuration.
	basic.RegisterBuiltinProj4SRIDs()

	// proj4 config option: extra SRID -> PROJ.4 definitions for systems not
	// in the built-in table. Accepts either a single string with entries
	// separated by newlines or ';' ("2180=+proj=sterea ...; 2177=+proj=tmerc
	// ..."), or a TOML table ({2180 = "+proj=sterea ..."}).
	if raw, ok := config.Interface(ConfigKeyProj4); ok && raw != nil {
		defs, err := parseProj4ConfigValue(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %v: %v", ConfigKeyProj4, err)
		}
		for srid, def := range defs {
			if err := basic.RegisterProj4SRID(srid, def); err != nil {
				return nil, fmt.Errorf("invalid %v: %v", ConfigKeyProj4, err)
			}
			log.Infof("registered proj4 definition for srid %v", srid)
		}
	}

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

		// layer container. will be added to the provider after it's configured
		layer := Layer{
			name:                 layerName,
			idFieldname:          idFieldname,
			geomFieldname:        geomFieldname,
			geometryFormat:       geometryFormat,
			crsExplicit:          sridExplicit,
			mosPrecision:         mosPrecision,
			mosPrecisionExplicit: mosPrecisionExplicit,
			mosUnitsFactor:       mosUnitsFactor,
			mosUnitsExplicit:     mosUnitsExplicit,
		}
		if _, explicit := layerConf.Interface(ConfigKeySRID); explicit {
			layer.crsExplicit = true
		}
		if raw, ok := layerConf.Interface(ConfigKeyCRSDefn); ok && raw != nil {
			defn, derr := layerConf.String(ConfigKeyCRSDefn, nil)
			if derr != nil {
				return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyCRSDefn, derr)
			}
			layer.crsExplicit = layer.crsExplicit || strings.TrimSpace(defn) != ""
		}

		// layer-level mos_precision overrides the provider-level value
		if layer.mosPrecision, err = layerConf.Float(ConfigKeyMOSPrecision, &mosPrecision); err != nil {
			return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyMOSPrecision, err)
		}
		if err := validateMOSPrecision(layer.mosPrecision); err != nil {
			return nil, fmt.Errorf("for layer (%v) %v invalid %v: %w", i, layerName, ConfigKeyMOSPrecision, err)
		}
		if _, explicit := layerConf.Interface(ConfigKeyMOSPrecision); explicit {
			layer.mosPrecisionExplicit = true
		}

		// layer-level mos_units overrides the provider-level value.
		if _, explicit := layerConf.Interface(ConfigKeyMOSUnits); explicit {
			layerMosUnits, uerr := layerConf.String(ConfigKeyMOSUnits, nil)
			if uerr != nil {
				return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyMOSUnits, uerr)
			}
			units, uerr := mos.ParseMapUnits(layerMosUnits)
			if uerr != nil {
				return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyMOSUnits, uerr)
			}
			layer.mosUnitsFactor, uerr = units.ToMetres()
			if uerr != nil {
				return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyMOSUnits, uerr)
			}
			layer.mosUnitsExplicit = true
		}

		if errTable == nil { // layerConf[ConfigKeyTableName] exists
			tablename, err := layerConf.String(ConfigKeyTableName, &idFieldname)
			if err != nil {
				return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
			}

			// verify the table exists and sample its geometry to learn the
			// geometry type and SRID
			inspectionSQL := fmt.Sprintf("SELECT %v FROM %v WHERE %v IS NOT NULL LIMIT 1",
				quoteIdentifier(geomFieldname), quoteIdentifier(tablename), quoteIdentifier(geomFieldname))

			geo, headerSRID, sysInfo, err := geomTypeFromColumn(db, inspectionSQL, geometryFormat, serverFlavor, layer.mosPrecision)
			switch {
			case err == sql.ErrNoRows:
				lsrid, rerr := configuredLayerSRID(layerConf, srid)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %v", i, layerName, rerr)
				}
				layer.srid = uint64(lsrid)
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

				var lsrid int = layerSRID
				if lsrid, err = layerConf.Int(ConfigKeySRID, &lsrid); err != nil {
					return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
				}
				if err = applyLayerCRSDefn(layerConf, &lsrid); err != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyCRSDefn, err)
				}

				layer.tablename = tablename
				layer.tagFieldnames = tagFieldnames
				layer.geomType = geo
				layer.srid = uint64(lsrid)

				// apply layer self-description from a TLayerSystemInfoRec blob:
				// precision and PROJ.4 projection. Explicit config values
				// (mos_precision / mos_units / srid / crs_defn) always win.
				if err := applySystemInfo(&layer, layerConf, sysInfo, sridExplicit); err != nil {
					return nil, fmt.Errorf("layer '%v' (table %v): %v", layerName, tablename, err)
				}
			}

		} else { // layerConf[ConfigKeySQL] exists
			var customSQL string
			if customSQL, err = layerConf.String(ConfigKeySQL, &customSQL); err != nil {
				return nil, fmt.Errorf("for %v layer(%v) %v has an error: %v", i, layerName, ConfigKeySQL, err)
			}
			layer.sql = customSQL

			// if a !ZOOM! token exists, all features could be filtered out so we
			// don't have a geometry to inspect its type. Replace comparisons
			// against !ZOOM! with a permissive IN list and !BBOX! with 1=1 for
			// the inspection query, mirroring the gpkg provider.
			if customSQLNeedsDeferredInspection(customSQL) {
				lsrid, rerr := configuredLayerSRID(layerConf, srid)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %v", i, layerName, rerr)
				}
				layer.srid = uint64(lsrid)
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

			geo, headerSRID, sysInfo, err := geomTypeFromColumn(db, qtext, geometryFormat, serverFlavor, layer.mosPrecision)
			switch {
			case err == sql.ErrNoRows:
				lsrid, rerr := configuredLayerSRID(layerConf, srid)
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %v", i, layerName, rerr)
				}
				layer.srid = uint64(lsrid)
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

				var lsrid int = layerSRID
				if lsrid, err = layerConf.Int(ConfigKeySRID, &lsrid); err != nil {
					return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
				}
				if err = applyLayerCRSDefn(layerConf, &lsrid); err != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid %v: %v", i, layerName, ConfigKeyCRSDefn, err)
				}

				layer.geomType = geo
				layer.srid = uint64(lsrid)
				layer.idFieldname = idFieldname

				// apply layer self-description from a TLayerSystemInfoRec blob:
				// precision and PROJ.4 projection. Explicit config values
				// (mos_precision / mos_units / srid / crs_defn) always win.
				if err := applySystemInfo(&layer, layerConf, sysInfo, sridExplicit); err != nil {
					return nil, fmt.Errorf("layer '%v' (custom SQL): %v", layerName, err)
				}
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

// applySystemInfo applies a layer self-description parsed from a
// TLayerSystemInfoRec blob to the layer being registered. Only values not
// explicitly configured take effect:
//   - precision: used when neither provider- nor layer-level mos_precision
//     is set;
//   - units: used when neither provider- nor layer-level mos_units is set;
//   - projection: registered as a synthetic SRID when no srid/crs_defn is
//     set at provider or layer level (sridExplicit covers both).
//
// layerConf is checked with Interface to detect explicit layer-level keys;
// nil sysInfo (no system info blob in the table) is a no-op.
func applySystemInfo(layer *Layer, layerConf dict.Dicter, sysInfo *mos.SystemInfo, sridExplicit bool) error {
	if sysInfo == nil {
		return nil
	}
	if layer.geometryFormat == GeometryFormatAuto {
		layer.geometryFormat = GeometryFormatMOS
	}

	if err := validateMOSPrecision(float64(sysInfo.Precision)); err != nil {
		return fmt.Errorf("invalid system-info MOS precision: %w", err)
	}

	// precision: only when mos_precision is absent on both provider and
	// layer level. layer.mosPrecision currently holds the (possibly
	// defaulted) provider value; a layer-level key overrides it.
	if !layer.mosPrecisionExplicit {
		layer.mosPrecision = float64(sysInfo.Precision)
	}

	if !layer.mosUnitsExplicit && sysInfo.MapUnitsDefined {
		factor, err := sysInfo.ScaleToMetres()
		if err != nil {
			return fmt.Errorf("unable to convert MOS map units %v to metres: %v", sysInfo.MapUnits, err)
		}
		layer.mosUnitsFactor = factor
	}

	// A layer-level CRS must also suppress system-info projection. The
	// provider-level flag alone is insufficient because layer config is
	// intentionally allowed to override provider defaults.
	crsExplicit := sridExplicit
	if layerConf != nil {
		if _, ok := layerConf.Interface(ConfigKeySRID); ok {
			crsExplicit = true
		}
		layer.crsExplicit = crsExplicit
		if raw, ok := layerConf.Interface(ConfigKeyCRSDefn); ok && raw != nil {
			defn, err := layerConf.String(ConfigKeyCRSDefn, nil)
			if err != nil {
				return fmt.Errorf("invalid %v: %v", ConfigKeyCRSDefn, err)
			}
			crsExplicit = crsExplicit || strings.TrimSpace(defn) != ""
		}
	}

	// projection: register the blob's PROJ.4 definition as the layer SRID
	// when no provider- or layer-level CRS was selected.
	if sysInfo.Projection != "" && !crsExplicit {
		code, err := basic.RegisterProj4Defn(sysInfo.Projection)
		if err != nil {
			return fmt.Errorf("unable to register layer projection %q: %v", sysInfo.Projection, err)
		}
		layer.srid = code
		log.Infof("registered layer projection %q as synthetic srid %v", sysInfo.Projection, code)
	}

	if sysInfo.MapUnitsDefined {
		log.Debugf("layer %v system info: precision=%v map units=%v factor=%v projection=%q",
			layer.name, sysInfo.Precision, sysInfo.MapUnits, layer.mosUnitsFactor, sysInfo.Projection)
	} else {
		log.Debugf("layer %v system info: precision=%v projection=%q",
			layer.name, sysInfo.Precision, sysInfo.Projection)
	}
	return nil
}

const maxMOSPrecision = 308

func validateMOSPrecision(precision float64) error {
	if math.IsNaN(precision) || math.IsInf(precision, 0) ||
		precision < 0 || math.Trunc(precision) != precision ||
		precision > maxMOSPrecision {
		return fmt.Errorf("must be a finite non-negative integer no greater than %d, got %v", maxMOSPrecision, precision)
	}
	return nil
}

// applyLayerCRSDefn resolves a layer-level crs_defn (full PROJ.4 definition)
// into the layer SRID, overriding a numeric srid when present.
func applyLayerCRSDefn(layerConf dict.Dicter, lsrid *int) error {
	defnDefault := ""
	defn, err := layerConf.String(ConfigKeyCRSDefn, &defnDefault)
	if err != nil {
		return err
	}
	if strings.TrimSpace(defn) == "" {
		return nil
	}
	code, err := basic.RegisterProj4Defn(defn)
	if err != nil {
		return err
	}
	*lsrid = int(code)
	return nil
}

// parseProj4ConfigValue parses the raw value of the proj4 config option.
// Accepted shapes:
//   - string: entries separated by newlines or ';', each "SRID=+proj=..."
//   - []map[string]interface{} / map[string]interface{} (TOML table via env.Dict):
//     keys are SRIDs, values are PROJ.4 strings
func parseProj4ConfigValue(raw interface{}) (map[uint64]string, error) {
	switch v := raw.(type) {
	case string:
		entries := strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == ';' })
		return basic.ParseProj4Config(entries)
	case map[string]interface{}:
		out := make(map[uint64]string, len(v))
		for k, defStr := range v {
			srid, err := parseSRIDKey(k)
			if err != nil {
				return nil, err
			}
			def, ok := defStr.(string)
			if !ok {
				return nil, fmt.Errorf("proj4 value for srid %v must be a string, got %T", k, defStr)
			}
			out[srid] = def
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected string or table, got %T", raw)
	}
}

// parseSRIDKey parses a proj4 table key into an EPSG code. Accepts bare
// numbers ("2180") and "EPSG:2180" / "epsg:2180" for convenience.
func parseSRIDKey(key string) (uint64, error) {
	k := strings.TrimSpace(key)
	if len(k) >= 5 && strings.EqualFold(k[:5], "epsg:") {
		k = strings.TrimSpace(k[5:])
	}
	if k == "" {
		return 0, fmt.Errorf("proj4 table has an empty srid key")
	}
	for _, r := range k {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("proj4 table key %q is not a valid srid", key)
		}
	}
	srid, err := strconv.ParseUint(k, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("proj4 table key %q is not a valid srid", key)
	}
	return srid, nil
}
