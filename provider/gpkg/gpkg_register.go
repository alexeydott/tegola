//go:build cgo
// +build cgo

package gpkg

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	conf "github.com/go-spatial/tegola/config"
	_ "github.com/mattn/go-sqlite3"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/internal/log"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/provider/crsconfig"
)

var colFinder *regexp.Regexp

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

// configuredLayerSRID and applyLayerCRSDefn were replaced by the shared
// provider/crsconfig resolver, which implements the same provider/layer
// crs_defn > srid > fallback precedence together with explicit-flag
// tracking for every standard provider.

func init() {
	provider.Register(provider.TypeStd.Prefix()+Name, NewTileProvider, Cleanup)
	colFinder = regexp.MustCompile(`^(([a-zA-Z_][a-zA-Z0-9_]*)|"([^"]+)")\s`)
}

// Metadata for feature tables in gpkg database
type featureTableDetails struct {
	colNames      []string
	idFieldname   string
	geomFieldname string
	geomType      geom.Geometry
	srid          uint64
	bbox          *geom.Extent
}

// Creates a config instance of the type NewTileProvider() requires including all available feature
//
//	tables in the gpkg at 'gpkgPath'.
func AutoConfig(gpkgPath string) (map[string]interface{}, error) {
	// Get all feature tables
	db, err := sql.Open("sqlite3", gpkgPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ftMetaData, err := featureTableMetaData(db)
	if err != nil {
		return nil, err
	}

	// Handle table config creation in consistent order to facilitate testing
	tnames := make([]string, len(ftMetaData))
	i := 0
	for tname := range ftMetaData {
		tnames[i] = tname
		i++
	}
	sort.Strings(tnames)

	conf := make(map[string]interface{})
	conf["name"] = "autoconfd_gpkg"
	conf["type"] = provider.TypeStd.Prefix() + Name
	conf["filepath"] = gpkgPath
	conf["layers"] = make([]map[string]interface{}, len(tnames))
	for i, tablename := range tnames {
		// Use all columns besides the primary key (id) and geometry columns in "fields"
		propFields := make([]string, 0, len(ftMetaData[tablename].colNames))
		for _, colName := range ftMetaData[tablename].colNames {
			if colName != ftMetaData[tablename].idFieldname && colName != ftMetaData[tablename].geomFieldname {
				propFields = append(propFields, colName)
			}
		}

		lconf := make(map[string]interface{})
		lconf["name"] = tablename
		lconf["tablename"] = tablename
		lconf["id_fieldname"] = ftMetaData[tablename].idFieldname
		lconf["fields"] = propFields
		conf["layers"].([]map[string]interface{})[i] = lconf
	}

	return conf, nil
}

// extractColsAndPKFromSQL extracts all column names and the primary key colum
// from an SQL definition string.
func extractColsAndPKFromSQL(sql string) ([]string, string) {
	defs := extractColDefsFromSQL(sql)

	var pkCol string
	colNames := make([]string, 0, len(defs))

	// match unquoted (`column_name`) or quoted (`"column name"`) indentifiers
	for _, def := range defs {
		matches := colFinder.FindStringSubmatch(def)
		if matches == nil {
			continue
		}
		colName := matches[2] + matches[3] // either from unquoted, or quoted submatch
		colNames = append(colNames, colName)

		if strings.Contains(strings.ToLower(def), "primary key") {
			pkCol = colName
		}
	}
	// Sort colNames for consistent output to facilitate testing
	sort.Strings(colNames)

	return colNames, pkCol
}

// extractColDefsFromSQL extracts all column definitions an SQL definition string.
func extractColDefsFromSQL(sql string) []string {
	// Simple parser for SQL definitions. Skips everything before the first
	// parentheses, splits definitions at comma, but ignores commas between
	// subsequent parentheses.

	// Does not handle comments or quoted commas.

	var defs []string
	var col bytes.Buffer
	p := 0 // count number of open parentheses

	for _, r := range sql {

		if r == ')' && p == 1 {
			// closing outer brace of column definitions
			defs = append(defs, strings.TrimSpace(col.String()))
			col.Reset()
			break
		}
		if r == ',' && p == 1 {
			// next definition
			defs = append(defs, strings.TrimSpace(col.String()))
			col.Reset()
			continue
		}

		col.WriteRune(r)
		if r == '(' {
			if p == 0 {
				// start of column definitions, ignore CREATE TABLE ...
				col.Reset()
			}
			p++
		}
		if r == ')' {
			p--
		}
	}
	return defs
}

// Collect meta data about all feature tables in opened gpkg.
func featureTableMetaData(gpkg *sql.DB) (map[string]featureTableDetails, error) {
	// this query is used to read the metadata from the gpkg_contents, gpkg_geometry_columns, and
	// sqlite_master tables for tables that store geographic features.
	qtext := `
		SELECT
			c.table_name, c.min_x, c.min_y, c.max_x, c.max_y, c.srs_id, gc.column_name, gc.geometry_type_name, sm.sql
		FROM
			gpkg_contents c JOIN gpkg_geometry_columns gc ON c.table_name == gc.table_name JOIN sqlite_master sm ON c.table_name = sm.tbl_name
		WHERE
			c.data_type = 'features' AND sm.type = 'table';`

	rows, err := gpkg.Query(qtext)
	if err != nil {
		if isMissingGpkgMetadataErr(err) {
			// No GeoPackage metadata tables: only raw-format layers can be
			// served from this file.
			return make(map[string]featureTableDetails), nil
		}
		log.Errorf("error during query: %v - %v", qtext, err)
		return nil, err
	}
	defer rows.Close()

	// container for tracking metadata for each table with a geometry
	geomTableDetails := make(map[string]featureTableDetails)

	// iterate each row extracting meta data about each table
	for rows.Next() {
		var tablename, geomCol, geomType, tableSql sql.NullString
		var minX, minY, maxX, maxY sql.NullFloat64
		var srid sql.NullInt64

		if err = rows.Scan(&tablename, &minX, &minY, &maxX, &maxY, &srid, &geomCol, &geomType, &tableSql); err != nil {
			return nil, err
		}
		if !tableSql.Valid {
			return nil, fmt.Errorf("invalid sql for table '%v'", tablename)
		}

		// map the returned geom type to a tegola geom type
		tg, err := geomNameToGeom(geomType.String)
		if err != nil {
			log.Errorf("error mapping geom type (%v): %v", geomType, err)
			return nil, err
		}

		bbox := geom.NewExtent(
			[2]float64{minX.Float64, minY.Float64},
			[2]float64{maxX.Float64, maxY.Float64},
		)

		var sridVal uint64
		if srid.Valid && srid.Int64 > 0 {
			sridVal = uint64(srid.Int64)
		}

		colNames, pkCol := extractColsAndPKFromSQL(tableSql.String)

		geomTableDetails[tablename.String] = featureTableDetails{
			colNames:      colNames,
			idFieldname:   pkCol,
			geomFieldname: geomCol.String,
			geomType:      tg,
			srid:          sridVal,
			// the extent of the layer's features
			//bbox: geom.BoundingBox{minX.Float64, minY.Float64, maxX.Float64, maxY.Float64},
			bbox: bbox,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return geomTableDetails, nil
}

// isMissingGpkgMetadataErr reports whether the error indicates that the
// GeoPackage metadata tables (gpkg_contents / gpkg_geometry_columns) do not
// exist, in which case only raw-format (wkb/wkt/mos) layers can be served.
func isMissingGpkgMetadataErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table") &&
		(strings.Contains(msg, "gpkg_contents") || strings.Contains(msg, "gpkg_geometry_columns"))
}

// sqliteTableSQL returns the CREATE TABLE statement of a plain SQLite table
// from sqlite_master, used to inspect raw-format tables that are not
// registered in gpkg_geometry_columns.
func sqliteTableSQL(db *sql.DB, tablename string) (string, error) {
	var tableSQL sql.NullString
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND tbl_name = ?;`,
		tablename,
	).Scan(&tableSQL)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("table %q does not exist", tablename)
	}
	if err != nil {
		return "", fmt.Errorf("table %q lookup: %v", tablename, err)
	}
	if !tableSQL.Valid {
		return "", fmt.Errorf("invalid sql for table %q", tablename)
	}
	return tableSQL.String, nil
}

// sampleRawTableLayer samples a raw-format (wkb/wkt/mos) table's geometry
// column to infer the layer's geometry type and apply a MOS system-info blob
// when one is stored alongside the geometries. Only rows the in-memory tile
// filter cannot pre-reject are needed, so a small LIMIT window suffices; a
// table that currently holds no decodable geometry registers without an
// inferred type, matching the custom-SQL path.
func sampleRawTableLayer(db *sql.DB, layer *Layer) error {
	qtext := fmt.Sprintf("SELECT `%v` FROM `%v` WHERE `%v` IS NOT NULL LIMIT 10;",
		layer.geomFieldname, layer.tablename, layer.geomFieldname)

	rows, err := db.Query(qtext)
	if err != nil {
		return fmt.Errorf("table %q sample query: %v", layer.tablename, err)
	}
	defer rows.Close()

	for rows.Next() {
		var value interface{}
		if err := rows.Scan(&value); err != nil {
			return fmt.Errorf("table %q sample scan: %v", layer.tablename, err)
		}

		if layer.geometryFormat == codec.FormatMOS && codec.IsSystemInfoValue(value) {
			sysInfo, serr := codec.ParseSystemInfoValue(value)
			if serr != nil {
				return fmt.Errorf("table %q parse MOS system info: %v", layer.tablename, serr)
			}
			if aerr := layer.mosConfig.ApplySystemInfo(&sysInfo); aerr != nil {
				return fmt.Errorf("table %q apply MOS system info: %v", layer.tablename, aerr)
			}
			if srid, applied, aerr := crsconfig.ApplySystemInfoCRS(int(layer.srid), layer.crsExplicit, sysInfo.Projection); aerr != nil {
				return fmt.Errorf("table %q apply MOS projection: %v", layer.tablename, aerr)
			} else if applied {
				layer.srid = uint64(srid)
				layer.crsExplicit = true
			}
			continue
		}

		_, geo, derr := decodeGeometryValue(value, layer.geometryFormat, layer.mosConfig)
		if derr != nil {
			return fmt.Errorf("table %q decode %v geometry: %v", layer.tablename, layer.geometryFormat, derr)
		}
		if geo != nil {
			layer.geomType = geo
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("table %q sample rows: %v", layer.tablename, err)
	}
	return nil
}

func NewTileProvider(config dict.Dicter, maps []provider.Map) (provider.Tiler, error) {

	log.Debugf("config: %v", config)

	filepath, err := config.String(ConfigKeyFilePath, nil)
	if err != nil {
		return nil, err
	}
	if filepath == "" {
		return nil, ErrInvalidFilePath{filepath}
	}

	// check the file exists
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		return nil, ErrInvalidFilePath{filepath}
	}

	db, err := sql.Open("sqlite3", filepath)
	if err != nil {
		return nil, err
	}
	keepDB := false
	defer func() {
		if !keepDB {
			_ = db.Close()
		}
	}()

	// The GeoPackage metadata tables (gpkg_contents / gpkg_geometry_columns)
	// are only required for gpkg-format layers; raw-format (wkb/wkt/mos)
	// layers read plain SQLite tables, so an empty metadata map is tolerated
	// when those tables are absent.
	geomTableDetails, err := featureTableMetaData(db)
	if err != nil && !isMissingGpkgMetadataErr(err) {
		return nil, err
	}

	// provider-level srid/crs_defn via the shared CRS contract. An explicit
	// value must take precedence over any SRID inferred from the GPKG itself
	// (gpkg_contents.srs_id or the per-row WKB header), since that inferred
	// data is not always reliable (e.g. GPKGs produced by third-party
	// conversion tools such as DWG exporters commonly leave those fields at 0
	// or set them to a non-standard code).
	pcrs, perr := crsconfig.ResolveProvider(config, DefaultSRID)
	if perr != nil {
		return nil, perr
	}
	srid := pcrs.SRID
	providerSRIDExplicit := pcrs.Explicit

	// Register the same common projected SRIDs supported by the other SQL
	// providers. GeoPackages frequently carry UTM or Gauss-Kruger metadata;
	// without this registration a valid numeric layer SRID cannot be used for
	// source-CRS bbox conversion or feature reprojection.
	basic.RegisterBuiltinProj4SRIDs()

	// provider-level geometry format / MOS settings, shared by all layers
	// that do not override them (same contract as the MySQL provider).
	providerGeometryFormat := ""
	if v, ok := config.Interface(codec.ConfigKeyGeometryFormat); ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, fmt.Errorf("invalid %v: expected string, got %T", codec.ConfigKeyGeometryFormat, v)
		}
		providerGeometryFormat, err = resolveGeometryFormat(strings.TrimSpace(s))
		if err != nil {
			return nil, err
		}
	}
	providerMOSCfg, err := codec.ResolveMOSConfig(config, nil, "")
	if err != nil {
		return nil, err
	}
	// A non-mos provider-level format must not carry leftover MOS settings;
	// mirror the MySQL provider's warn + reset semantics.
	if providerGeometryFormat != "" && providerGeometryFormat != GeometryFormatMOS &&
		(providerMOSCfg.PrecisionSet || providerMOSCfg.UnitsSet) {
		log.Warnf("%v / %v only apply when %v = %q; ignoring provider-level values",
			codec.ConfigKeyMOSPrecision, codec.ConfigKeyMOSUnits,
			codec.ConfigKeyGeometryFormat, providerGeometryFormat)
		if providerMOSCfg.UnitsSet {
			providerMOSCfg.UnitFactor = codec.MOSUnitsFactorDefault
		}
		providerMOSCfg.Precision = codec.DefaultMOSPrecisionForUnits(providerMOSCfg.UnitFactor)
	}

	p := Provider{
		Filepath: filepath,
		layers:   make(map[string]Layer),
		db:       db,
		srid:     uint64(srid),
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
		idFieldname, err = layerConf.String(ConfigKeyGeomIDField, &idFieldname)
		if err != nil {
			return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
		}

		geomFieldname := DefaultGeomFieldName
		geomFieldname, err = layerConf.String(ConfigKeyGeomField, &geomFieldname)
		if err != nil {
			return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
		}

		tagFieldnames, err := layerConf.StringSlice(ConfigKeyFields)
		if err != nil { // empty slices are okay
			return nil, fmt.Errorf("for layer (%v) %v, %q field had the following error: %v", i, layerName, ConfigKeyFields, err)
		}

		// layer container. will be added to the provider after it's configured
		layer := Layer{
			name:          layerName,
			idFieldname:   idFieldname,
			geomFieldname: geomFieldname,
			geometryFormat: providerGeometryFormat,
			mosConfig:      providerMOSCfg,
		}

		// layer-level geometry format / MOS overrides merge on top of the
		// provider-level values.
		layerMOSCfg, err := codec.ResolveMOSConfig(nil, layerConf, layerName)
		if err != nil {
			return nil, err
		}
		layerGeometryFormat := ""
		if v, ok := layerConf.Interface(codec.ConfigKeyGeometryFormat); ok {
			s, isStr := v.(string)
			if !isStr {
				return nil, fmt.Errorf("for layer (%v) invalid %v: expected string, got %T", i, codec.ConfigKeyGeometryFormat, v)
			}
			layerGeometryFormat, err = resolveGeometryFormat(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("for layer (%v) %v", i, err)
			}
		}
		if layerGeometryFormat != "" {
			layer.geometryFormat = layerGeometryFormat
		}
		if layerMOSCfg.PrecisionSet {
			layer.mosConfig.Precision = layerMOSCfg.Precision
			layer.mosConfig.PrecisionSet = true
		}
		if layerMOSCfg.UnitsSet {
			layer.mosConfig.UnitFactor = layerMOSCfg.UnitFactor
			layer.mosConfig.UnitsSet = true
		}
		if layer.geometryFormat != "" && layer.geometryFormat != GeometryFormatMOS &&
			(layerMOSCfg.PrecisionSet || layerMOSCfg.UnitsSet) {
			log.Warnf("layer (%v): %v / %v only apply when %v = %q; ignoring values",
				layerName, codec.ConfigKeyMOSPrecision, codec.ConfigKeyMOSUnits,
				codec.ConfigKeyGeometryFormat, layer.geometryFormat)
			if layerMOSCfg.UnitsSet {
				layer.mosConfig.UnitFactor = codec.MOSUnitsFactorDefault
			}
			layer.mosConfig.Precision = codec.DefaultMOSPrecisionForUnits(layer.mosConfig.UnitFactor)
		}

		if errTable == nil { // layerConf[ConfigKeyTableName] exists
			tablename, err := layerConf.String(ConfigKeyTableName, &idFieldname)
			if err != nil {
				return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
			}

			layer.tablename = tablename
			layer.tagFieldnames = tagFieldnames
			layer.idFieldname = idFieldname

			if codec.IsRawFormat(layer.geometryFormat) {
				// Raw geometry formats (wkb/wkt/mos) are read from plain
				// SQLite tables that need not be registered in
				// gpkg_geometry_columns; the GeoPackage metadata is neither
				// required nor consulted and there is no RTree index, so
				// TileFeatures filters in memory.
				tableSQL, terr := sqliteTableSQL(db, tablename)
				if terr != nil {
					return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, terr)
				}
				colNames, pkCol := extractColsAndPKFromSQL(tableSQL)
				colSet := make(map[string]struct{}, len(colNames))
				for _, c := range colNames {
					colSet[c] = struct{}{}
				}
				if _, ok := colSet[layer.geomFieldname]; !ok {
					return nil, fmt.Errorf("for layer (%v) %v: table %q has no geometry column %q", i, layerName, tablename, layer.geomFieldname)
				}
				if _, ok := colSet[layer.idFieldname]; !ok {
					if pkCol == "" {
						return nil, fmt.Errorf("for layer (%v) %v: table %q has no id column %q", i, layerName, tablename, layer.idFieldname)
					}
					log.Warnf("layer (%v): table %q has no column %q; using primary key %q as id field",
						layerName, tablename, layer.idFieldname, pkCol)
					layer.idFieldname = pkCol
				}

				// Raw tables carry no SRID metadata, so the SRID comes from
				// explicit config or the provider default. A MOS system-info
				// blob (sampled below) may still register a projection.
				lcrs, rerr := crsconfig.ResolveLayer(layerConf, int(p.srid))
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				layer.srid = uint64(lcrs.SRID)
				layer.crsExplicit = providerSRIDExplicit || lcrs.Explicit

				if gerr := sampleRawTableLayer(db, &layer); gerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, gerr)
				}
			} else {
				d, ok := geomTableDetails[tablename]
				if !ok {
					return nil, fmt.Errorf("table %q does not exist", tablename)
				}

				// an explicit provider-level srid always wins over the value inferred from
				// gpkg_contents.srs_id; the inferred value is only used as a fallback when
				// the user did not configure anything explicitly.
				layerSRID := p.srid
				if !providerSRIDExplicit && d.srid > 0 {
					layerSRID = d.srid
				}

				lcrs, rerr := crsconfig.ResolveLayer(layerConf, int(layerSRID))
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}

				layer.geomFieldname = d.geomFieldname
				layer.geomType = d.geomType
				layer.srid = uint64(lcrs.SRID)
				layer.bbox = *d.bbox
				layer.crsExplicit = providerSRIDExplicit || lcrs.Explicit
			}

		} else { // layerConf[ConfigKeySQL] exists
			var customSQL string
			customSQL, err = layerConf.String(ConfigKeySQL, &customSQL)
			if err != nil {
				return nil, fmt.Errorf("for %v layer(%v) %v has an error: %v", i, layerName, ConfigKeySQL, err)
			}
			layer.sql = customSQL

			if customSQLNeedsDeferredInspection(customSQL) {
				lcrs, rerr := crsconfig.ResolveLayer(layerConf, int(p.srid))
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				layer.srid = uint64(lcrs.SRID)
				layer.crsExplicit = providerSRIDExplicit || lcrs.Explicit
				log.Warnf("layer '%v' uses tile-dependent custom SQL; deferring startup geometry inspection", layerName)
				p.layers[layer.name] = layer
				continue
			}

			// if a !ZOOM! token exists, all features could be filtered out so we don't have a geometry to inspect it's type.
			// TODO(arolek): implement an SQL parser or figure out a different approach. this is brittle but I can't figure out a better
			// solution without using an SQL parser on custom SQL statements
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
			inspectionTile := provider.NewTile(0, 0, 0, 0, uint(p.srid))
			inspectionExtent, _ := inspectionTile.BufferedExtent()
			inspectionSQL = replaceTokens(inspectionSQL, &layer, inspectionTile, inspectionExtent)

			// Get geometry type & srid from geometry of first row. For raw
			// formats several rows are inspected because a MOS system-info
			// blob may precede the first decodable geometry.
			qtext := fmt.Sprintf("SELECT %[1]v FROM (%v) WHERE %[1]v IS NOT NULL LIMIT 10;", layer.geomFieldname, inspectionSQL)

			log.Debugf("qtext: %v", qtext)

			inspectRows, qerr := db.Query(qtext)
			if qerr != nil {
				return nil, fmt.Errorf("layer '%v' problem executing custom SQL: %v", layerName, qerr)
			}

			var firstGeom geom.Geometry
			var firstHeader *BinaryHeader
			for inspectRows.Next() {
				var geomData interface{}
				if serr := inspectRows.Scan(&geomData); serr != nil {
					inspectRows.Close()
					return nil, fmt.Errorf("layer '%v' problem reading custom SQL row: %v", layerName, serr)
				}

				if codec.IsRawFormat(layer.geometryFormat) {
					// MOS system-info rows are metadata, never features:
					// apply them and continue to the next row.
					if layer.geometryFormat == codec.FormatMOS && codec.IsSystemInfoValue(geomData) {
						sysInfo, serr := codec.ParseSystemInfoValue(geomData)
						if serr != nil {
							inspectRows.Close()
							return nil, fmt.Errorf("layer '%v' parse MOS system info: %v", layerName, serr)
						}
						if aerr := layer.mosConfig.ApplySystemInfo(&sysInfo); aerr != nil {
							inspectRows.Close()
							return nil, fmt.Errorf("layer '%v' apply MOS system info: %v", layerName, aerr)
						}
						continue
					}
					_, geo, derr := decodeGeometryValue(geomData, layer.geometryFormat, layer.mosConfig)
					if derr != nil {
						inspectRows.Close()
						return nil, fmt.Errorf("layer '%v' decode %v geometry: %v", layerName, layer.geometryFormat, derr)
					}
					if geo != nil {
						firstGeom = geo
						break
					}
					continue
				}

				geomDataBytes, ok := geomData.([]byte)
				if !ok {
					inspectRows.Close()
					return nil, errors.New("unexpected column type for geom field. expected blob")
				}
				h, geo, derr := decodeGeometry(geomDataBytes)
				if derr != nil {
					inspectRows.Close()
					return nil, derr
				}
				firstGeom = geo
				firstHeader = h
				break
			}
			if rerr := inspectRows.Err(); rerr != nil {
				inspectRows.Close()
				return nil, fmt.Errorf("layer '%v' problem reading custom SQL rows: %v", layerName, rerr)
			}
			inspectRows.Close()

			switch {
			case firstGeom == nil:
				// The layer's custom SQL currently returns no decodable
				// rows. Keep a placeholder layer so map registration can
				// succeed; a later tile request can still execute the SQL
				// when data appears.
				lcrs, rerr := crsconfig.ResolveLayer(layerConf, int(p.srid))
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}
				layer.srid = uint64(lcrs.SRID)
				log.Warnf("layer '%v' with custom SQL currently returns 0 rows; registering it without an inferred geometry type: %v", layerName, customSQL)
				p.layers[layer.name] = layer
				continue

			default:
				// as above: an explicit provider-level srid always wins over the value
				// decoded from the sampled row's WKB header, which is frequently 0 or
				// otherwise unreliable for GPKGs produced by third-party tooling.
				layerSRID := p.srid
				if !providerSRIDExplicit && firstHeader != nil && firstHeader.SRSId() > 0 {
					layerSRID = uint64(firstHeader.SRSId())
				}

				lcrs, rerr := crsconfig.ResolveLayer(layerConf, int(layerSRID))
				if rerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
				}

				layer.geomType = firstGeom
				layer.srid = uint64(lcrs.SRID)
				layer.crsExplicit = providerSRIDExplicit || lcrs.Explicit
				// keep the configured (or default) id/geometry field names set
				// at layer creation; only fill in the inferred geometry type.
			}
		}

		p.layers[layer.name] = layer
	}

	// track the provider so we can clean it up later
	providersMu.Lock()
	providers = append(providers, p)
	providersMu.Unlock()
	keepDB = true

	return &p, err
}

// reference to all instantiated providers
var providers []Provider
var providersMu sync.Mutex

// Cleanup will close all database connections and destroy all previously instantiated Provider instances
func Cleanup() {
	providersMu.Lock()
	defer providersMu.Unlock()

	if len(providers) > 0 {
		log.Infof("cleaning up gpkg providers")
	}

	for i := range providers {
		if err := providers[i].Close(); err != nil {
			log.Errorf("err closing connection: %v", err)
		}
	}

	providers = make([]Provider, 0)
}
