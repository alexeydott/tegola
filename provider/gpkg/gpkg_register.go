//go:build cgo

package gpkg

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	conf "github.com/go-spatial/tegola/config"
	_ "github.com/mattn/go-sqlite3"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/tegola/basic"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/mos"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/provider/crsconfig"
	codec "github.com/go-spatial/tegola/provider/geometrycodec"
	"github.com/go-spatial/tegola/provider/mapplgis"
)

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

func init() {
	_ = provider.Register(provider.TypeStd.Prefix()+Name, NewTileProvider, Cleanup)
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
	db, err := sql.Open("sqlite3", sqliteReadOnlyDSN(gpkgPath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
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

// tableColumnsAndPK returns the column names (sorted for consistent output)
// and the primary key columns of a table, read via PRAGMA table_info, in
// primary-key order. This replaces the previous CREATE TABLE text parser,
// which could not handle comments or quoted commas.
func tableColumnsAndPK(db *sql.DB, tablename string) ([]string, []string, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%v);", quoteIdent(tablename)))
	if err != nil {
		return nil, nil, fmt.Errorf("table %q column lookup: %v", tablename, err)
	}
	defer func() { _ = rows.Close() }()

	type columnInfo struct {
		name string
		pk   int
	}
	var cols []columnInfo
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return nil, nil, fmt.Errorf("table %q column scan: %v", tablename, err)
		}
		cols = append(cols, columnInfo{name: name, pk: pk})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("table %q column rows: %v", tablename, err)
	}
	if len(cols) == 0 {
		return nil, nil, fmt.Errorf("table %q does not exist or has no columns", tablename)
	}

	// Composite primary keys return every participating column in pk order
	// (pk is the 1-based position within the key).
	var pkColumns []string
	for pos := 1; ; pos++ {
		found := false
		for _, col := range cols {
			if col.pk == pos {
				pkColumns = append(pkColumns, col.name)
				found = true
				break
			}
		}
		if !found {
			break
		}
	}

	colNames := make([]string, 0, len(cols))
	for _, col := range cols {
		colNames = append(colNames, col.name)
	}
	sort.Strings(colNames)

	return colNames, pkColumns, nil
}

// tableIndexedColumns returns one IndexMeta per table index with its
// columns in index-position order, read via PRAGMA index_list /
// PRAGMA index_info. Names are returned in their stored case; the MapplGIS
// contract comparison is case-insensitive.
func tableIndexedColumns(db *sql.DB, tablename string) ([]mapplgis.IndexMeta, error) {
	indexRows, err := db.Query(fmt.Sprintf("PRAGMA index_list(%v);", quoteIdent(tablename)))
	if err != nil {
		return nil, fmt.Errorf("table %q index lookup: %v", tablename, err)
	}
	defer func() { _ = indexRows.Close() }()

	type indexInfo struct {
		seq     int
		name    string
		unique  int
		origin  string
		partial int
	}
	var indexes []indexInfo
	for indexRows.Next() {
		var idx indexInfo
		if err := indexRows.Scan(&idx.seq, &idx.name, &idx.unique, &idx.origin, &idx.partial); err != nil {
			return nil, fmt.Errorf("table %q index scan: %v", tablename, err)
		}
		indexes = append(indexes, idx)
	}
	if err := indexRows.Err(); err != nil {
		return nil, fmt.Errorf("table %q index rows: %v", tablename, err)
	}

	var result []mapplgis.IndexMeta
	for _, idx := range indexes {
		meta := mapplgis.IndexMeta{Name: idx.name}
		infoRows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%v);", quoteIdent(idx.name)))
		if err != nil {
			return nil, fmt.Errorf("table %q index_info %q: %v", tablename, idx.name, err)
		}
		defer func() { _ = infoRows.Close() }()

		for infoRows.Next() {
			var seqno int
			var cid, name sql.NullString
			if err := infoRows.Scan(&seqno, &cid, &name); err != nil {
				return nil, fmt.Errorf("table %q index_info %q scan: %v", tablename, idx.name, err)
			}
			if name.Valid && name.String != "" {
				meta.Columns = append(meta.Columns, name.String)
			}
		}
		if err := infoRows.Err(); err != nil {
			return nil, fmt.Errorf("table %q index_info %q rows: %v", tablename, idx.name, err)
		}
		result = append(result, meta)
	}
	return result, nil
}

// detectMapplGIS applies the canonical MapplGIS table contract to a
// tablename layer (audit A-00/A-06/A03): DDL + PK + required indexes + the
// OKEY=1 LINE blob, checked once at registration regardless of the
// configured geometry format. On a detected layer the storage format is
// authoritative MOS: unset/auto resolve to MOS and an explicit non-MOS
// format is a startup conflict error (A04). The MOS system-info is applied
// to the layer; the projection becomes the layer SRID unless the config
// was explicit.
func detectMapplGIS(db *sql.DB, tablename string, layer *Layer) (bool, error) {
	colNames, pkColumns, err := tableColumnsAndPK(db, tablename)
	if err != nil {
		return false, err
	}
	indexes, err := tableIndexedColumns(db, tablename)
	if err != nil {
		return false, err
	}

	info, err := mapplgis.Detect(mapplgis.TableMeta{
		Columns:           colNames,
		PrimaryKeyColumns: pkColumns,
		Indexes:           indexes,
	}, func() (*mos.SystemInfo, error) {
		const fetchQuery = "SELECT LINE FROM %v WHERE OKEY = 1 AND LINE IS NOT NULL LIMIT 1;"
		rows, qerr := db.Query(fmt.Sprintf(fetchQuery, quoteIdent(tablename)))
		if qerr != nil {
			return nil, qerr
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			if rerr := rows.Err(); rerr != nil {
				return nil, rerr
			}
			return nil, nil
		}
		var value interface{}
		if serr := rows.Scan(&value); serr != nil {
			return nil, serr
		}
		sysInfo, perr := codec.ParseSystemInfoValue(value)
		if perr != nil {
			return nil, perr
		}
		return &sysInfo, nil
	})
	if err != nil {
		return false, fmt.Errorf("table %q: %w", tablename, err)
	}
	if !info.IsMapplGIS {
		return false, nil
	}

	// Format authority (audit A04): a detected MapplGIS table stores MOS
	// blobs. Unset/auto resolve to MOS; an explicit non-MOS format cannot
	// decode them and fails at startup instead of serving broken tiles.
	switch layer.geometryFormat {
	case "", GeometryFormatMOS:
		layer.geometryFormat = GeometryFormatMOS
	default:
		return false, fmt.Errorf(
			"table %q is detected as a MapplGIS layer (MOS storage), but geometry_format is explicitly %q; remove the setting or use %q",
			tablename, layer.geometryFormat, GeometryFormatMOS,
		)
	}

	if aerr := layer.mosConfig.ApplySystemInfo(&info.SystemInfo); aerr != nil {
		return false, fmt.Errorf("table %q apply MOS system info: %v", tablename, aerr)
	}
	layer.mapplSysInfo = info.SystemInfo
	if srid, applied, aerr := crsconfig.ApplySystemInfoCRS(int(layer.srid), layer.crsExplicit, info.SystemInfo.Projection); aerr != nil {
		return false, fmt.Errorf("table %q apply MOS projection: %v", tablename, aerr)
	} else if applied {
		layer.srid = uint64(srid)
		layer.crsExplicit = true
	}
	return true, nil
}

// detectBoundColumns maps the raw bounds columns (case-insensitive
// minx/maxx/miny/maxy) from a table's column list to the query field order
// [minx, maxx, miny, maxy]; nil when the table does not carry them. The
// detected (actual case) names are kept for SQL quoting.
func detectBoundColumns(colNames []string) *[4]string {
	lookup := make(map[string]string, len(colNames))
	for _, name := range colNames {
		lookup[strings.ToLower(name)] = name
	}
	fields := [4]string{"minx", "maxx", "miny", "maxy"}
	for i, key := range fields {
		actual, ok := lookup[key]
		if !ok {
			return nil
		}
		fields[i] = actual
	}
	return &fields
}

// matchBoundColumns maps the configured bounds fields (bbox_*_fieldname,
// layer > provider > defaults) to the table's actual column names
// (case-insensitive, preserving the column's case for SQL quoting) in the
// query field order [minx, maxx, miny, maxy]; nil unless the table carries
// all four configured columns (audit N6: explicit configuration wins over
// the legacy-name autodetection, so non-standard bounds columns still get
// a SQL filter instead of a full table scan).
func matchBoundColumns(colNames []string, fields codec.BBoxFields) *[4]string {
	lookup := make(map[string]string, len(colNames))
	for _, name := range colNames {
		lookup[strings.ToLower(name)] = name
	}
	matched := [4]string{}
	for i, key := range fields {
		actual, ok := lookup[strings.ToLower(strings.TrimSpace(key))]
		if !ok {
			return nil
		}
		matched[i] = actual
	}
	return &matched
}

// Collect meta data about all feature tables in opened gpkg.
func featureTableMetaData(gpkg *sql.DB) (map[string]featureTableDetails, error) {
	// this query is used to read the metadata from the gpkg_contents and
	// gpkg_geometry_columns tables for tables that store geographic
	// features. Column names and the primary key are read via
	// PRAGMA table_info (see tableColumnsAndPK).
	qtext := `
		SELECT
			c.table_name, c.min_x, c.min_y, c.max_x, c.max_y, c.srs_id, gc.column_name, gc.geometry_type_name
		FROM
			gpkg_contents c JOIN gpkg_geometry_columns gc ON c.table_name = gc.table_name
		WHERE
			c.data_type = 'features';`

	rows, err := gpkg.Query(qtext)
	if err != nil {
		log.Errorf("error during query: %v - %v", qtext, err)
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// container for tracking metadata for each table with a geometry
	geomTableDetails := make(map[string]featureTableDetails)

	// iterate each row extracting meta data about each table
	for rows.Next() {
		var tablename, geomCol, geomType sql.NullString
		var minX, minY, maxX, maxY sql.NullFloat64
		var srid sql.NullInt64

		if err = rows.Scan(&tablename, &minX, &minY, &maxX, &maxY, &srid, &geomCol, &geomType); err != nil {
			return nil, err
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

		colNames, pkColumns, cerr := tableColumnsAndPK(gpkg, tablename.String)
		if cerr != nil {
			// An orphaned gpkg_contents entry (table dropped or unreadable)
			// must not abort registration of every other table; skip it and
			// warn so the remaining layers are still served (R3).
			log.Warnf("table %q listed in gpkg_contents but unreadable, skipping: %v", tablename.String, cerr)
			continue
		}
		pkCol := ""
		if len(pkColumns) > 0 {
			pkCol = pkColumns[0]
		}

		geomTableDetails[tablename.String] = featureTableDetails{
			colNames:      colNames,
			idFieldname:   pkCol,
			geomFieldname: geomCol.String,
			geomType:      tg,
			srid:          sridVal,
			// the extent of the layer's features
			bbox: bbox,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return geomTableDetails, nil
}

// hasGpkgMetadataTables reports whether the file has the GeoPackage metadata
// tables (gpkg_contents / gpkg_geometry_columns) required by gpkg-format
// layers. Raw-format (wkb/wkt/mos) layers read plain SQLite tables and can
// be served without them.
func hasGpkgMetadataTables(db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('gpkg_contents', 'gpkg_geometry_columns');`,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	if count == 1 {
		// Exactly one of the two metadata tables exists: the file is
		// partially initialized or corrupted and gpkg-format layers cannot
		// be read reliably, so refuse it instead of silently mismatching
		// bounds with the RTree join (R8).
		return false, errors.New("geopackage metadata tables are partially present (one of gpkg_contents/gpkg_geometry_columns missing): file appears corrupted")
	}
	return count == 2, nil
}

// sampleRawTableLayer samples a raw-format (wkb/wkt/mos) table's geometry
// column to infer the layer's geometry type. Only rows the in-memory tile
// filter cannot pre-reject are needed, so a small LIMIT window suffices; a
// table that currently holds no decodable geometry registers without an
// inferred type, matching the custom-SQL path. The MapplGIS LayerInfo blob
// is metadata, not a feature: rows carrying it are skipped, detection runs
// once via the registration-time contract (see detectMapplGIS).
func sampleRawTableLayer(db *sql.DB, layer *Layer) error {
	qtext := fmt.Sprintf("SELECT %v FROM %v WHERE %v IS NOT NULL LIMIT %v;",
		quoteIdent(layer.geomFieldname), quoteIdent(layer.tablename), quoteIdent(layer.geomFieldname), codec.InspectionSampleLimit)

	rows, err := db.Query(qtext)
	if err != nil {
		return fmt.Errorf("table %q sample query: %v", layer.tablename, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var value interface{}
		if err := rows.Scan(&value); err != nil {
			return fmt.Errorf("table %q sample scan: %v", layer.tablename, err)
		}

		if layer.geometryFormat == codec.FormatMOS && codec.IsSystemInfoValue(value) {
			// MapplGIS LayerInfo blob: metadata, never a feature. Apply its
			// parameters once — precision/units feed the bounds quantization
			// and the CRS projection pins the layer SRID (unless the config
			// already declared one explicitly), matching detectMapplGIS.
			var info mos.SystemInfo
			info, perr := codec.ParseSystemInfoValue(value)
			if perr != nil {
				return fmt.Errorf("table %q parse system info: %v", layer.tablename, perr)
			}
			if aerr := layer.mosConfig.ApplySystemInfo(&info); aerr != nil {
				return fmt.Errorf("table %q apply system info: %v", layer.tablename, aerr)
			}
			if srid, applied, aerr := crsconfig.ApplySystemInfoCRS(int(layer.srid), layer.crsExplicit, info.Projection); aerr != nil {
				return fmt.Errorf("table %q apply system info CRS: %v", layer.tablename, aerr)
			} else if applied {
				layer.srid = uint64(srid)
				layer.crsExplicit = true
			}
			continue
		}

		if layer.geomType != nil {
			// geometry type already inferred
			continue
		}

		_, geo, derr := decodeGeometryValue(value, layer.geometryFormat, layer.mosConfig)
		if derr != nil {
			return fmt.Errorf("table %q decode %v geometry: %v", layer.tablename, layer.geometryFormat, derr)
		}
		if geo != nil {
			layer.geomType = geo
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

	db, err := sql.Open("sqlite3", sqliteReadOnlyDSN(filepath))
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
	// layers read plain SQLite tables, so an empty metadata map is used
	// when those tables are absent.
	geomTableDetails := make(map[string]featureTableDetails)
	hasMetadata, merr := hasGpkgMetadataTables(db)
	if merr != nil {
		return nil, merr
	}
	if hasMetadata {
		geomTableDetails, err = featureTableMetaData(db)
		if err != nil {
			return nil, err
		}
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
	// NOTE: mos_precision/mos_units are NOT rejected here even when the
	// provider-level geometry_format is not "mos": they are provider-level
	// defaults for layers that may still select the MOS format via their own
	// layer-level geometry_format. Relevance (warn) is decided per layer,
	// after the effective layer format is known.

	p := Provider{
		Filepath: filepath,
		layers:   make(map[string]*Layer),
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
			name:           layerName,
			idFieldname:    idFieldname,
			geomFieldname:  geomFieldname,
			geometryFormat: providerGeometryFormat,
			mosConfig:      providerMOSCfg,
		}

		// layer-level geometry format / MOS overrides merge on top of the
		// provider-level values.
		layerMOSCfg, err := codec.ResolveMOSConfig(nil, layerConf, layerName)
		if err != nil {
			return nil, err
		}
		layerGeometryFormat, gerr := codec.ResolveLayerGeometryFormat(providerGeometryFormat, layerConf, layerName, gpkgGeometryFormats)
		if gerr != nil {
			return nil, gerr
		}
		layer.geometryFormat = layerGeometryFormat
		layer.mosConfig = codec.MergeMOSConfig(providerMOSCfg, layerMOSCfg)
		if layer.geometryFormat != GeometryFormatMOS {
			codec.WarnAndResetMOSParams(layer.geometryFormat, &layer.mosConfig, layerName)
		}

		// common geometry_type key: an explicit value fixes the layer type
		// before any data is read and wins over metadata/sampled inference.
		explicitGeomType, gtypeExplicit, terr := codec.ResolveGeometryType(layerConf, layerName)
		if terr != nil {
			return nil, terr
		}
		if gtypeExplicit {
			layer.geomType = explicitGeomType
			layer.geomTypeExplicit = true
		}

		if errTable == nil { // layerConf[ConfigKeyTableName] exists
			tablename, err := layerConf.String(ConfigKeyTableName, nil)
			if err != nil {
				return nil, fmt.Errorf("for layer (%v) %v : %v", i, layerName, err)
			}

			layer.tablename = tablename
			layer.tagFieldnames = tagFieldnames
			layer.idFieldname = idFieldname

			// A03: canonical MapplGIS detection runs for every tablename
			// layer, before the native/raw branch: a MapplGIS table is not
			// registered in gpkg_geometry_columns, so a native-format layer
			// pointing at one must still be discovered here and served via
			// the MOS path. On detection the format is authoritative MOS
			// (A04) and an explicit non-MOS format is a startup error.
			isMappl, derr := detectMapplGIS(db, tablename, &layer)
			if derr != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, derr)
			}

			if codec.IsRawFormat(layer.geometryFormat) {
				// Raw geometry formats (wkb/wkt/mos) are read from plain
				// SQLite tables that need not be registered in
				// gpkg_geometry_columns; the GeoPackage metadata is neither
				// required nor consulted and there is no RTree index, so
				// TileFeatures filters in memory (or via raw bounds
				// columns when the table carries them).
				colNames, pkColumns, cerr := tableColumnsAndPK(db, tablename)
				if cerr != nil {
					return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, cerr)
				}
				colSet := make(map[string]struct{}, len(colNames))
				for _, c := range colNames {
					colSet[c] = struct{}{}
				}
				if _, ok := colSet[layer.geomFieldname]; !ok {
					return nil, fmt.Errorf("for layer (%v) %v: table %q has no geometry column %q", i, layerName, tablename, layer.geomFieldname)
				}
				if _, ok := colSet[layer.idFieldname]; !ok {
					if len(pkColumns) == 0 {
						return nil, fmt.Errorf("for layer (%v) %v: table %q has no id column %q", i, layerName, tablename, layer.idFieldname)
					}
					log.Warnf("layer (%v): table %q has no column %q; using primary key %q as id field",
						layerName, tablename, layer.idFieldname, pkColumns[0])
					layer.idFieldname = pkColumns[0]
				}
				bboxFields, berr := codec.ResolveBBoxFields(config, layerConf, layerName)
				if berr != nil {
					return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, berr)
				}
				// Explicitly configured bounds columns (bbox_*_fieldname)
				// win over the legacy-name autodetection (audit N6): when
				// the table carries all four configured columns they drive
				// the SQL bounds filter, so rawBoundsSQL can avoid a full
				// table scan for non-standard column names.
				layer.boundFieldnames = matchBoundColumns(colNames, bboxFields)
				if layer.boundFieldnames == nil {
					layer.boundFieldnames = detectBoundColumns(colNames)
				}
				if layer.boundFieldnames != nil {
					log.Debugf("layer (%v): table %q carries raw bounds columns; enabling SQL bounds filter", layerName, tablename)
				}
				// bboxFields mirrors the detected (or resolved) bounds
				// columns so tag exclusion and the predicate builder share
				// one contract.
				if layer.boundFieldnames != nil {
					layer.bboxFields = codec.BBoxFields(*layer.boundFieldnames)
				} else {
					layer.bboxFields = bboxFields
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
				if isMappl {
					layer.isMapplGIS = true
					layer.mapplSource = codec.MapplGISTableCanonical
					log.Debugf("layer '%v': table %q detected as MapplGIS", layerName, tablename)
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
				if gtypeExplicit {
					// explicit geometry_type wins over the gpkg metadata
					layer.geomType = explicitGeomType
				}
				layer.srid = uint64(lcrs.SRID)
				layer.bbox = *d.bbox
				layer.crsExplicit = providerSRIDExplicit || lcrs.Explicit

				// bounds field names backing the RTree !BBOX! predicate
				// (layer > provider > defaults).
				layer.bboxFields, err = codec.ResolveBBoxFields(config, layerConf, layerName)
				if err != nil {
					return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, err)
				}
			}

		} else { // layerConf[ConfigKeySQL] exists
			var customSQL string
			customSQL, err = layerConf.String(ConfigKeySQL, &customSQL)
			if err != nil {
				return nil, fmt.Errorf("for %v layer(%v) %v has an error: %v", i, layerName, ConfigKeySQL, err)
			}
			layer.sql = customSQL

			// Resolve the bounds field names (layer > provider > defaults)
			// backing the bounds-backed custom-SQL !BBOX! predicate.
			layer.bboxFields, err = codec.ResolveBBoxFields(config, layerConf, layerName)
			if err != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %v", i, layerName, err)
			}

			// CRS resolution up front: the probe contract needs the resolved
			// values and MOS custom SQL must configure srid/crs_defn
			// explicitly (A11). Explicitly-typed / deferred / empty-sample
			// layers keep this resolution — the same provider-SRID default
			// applies when startup probing is skipped.
			lcrs, rerr := crsconfig.ResolveLayer(layerConf, int(p.srid))
			if rerr != nil {
				return nil, fmt.Errorf("for layer (%v) %v invalid CRS: %w", i, layerName, rerr)
			}
			layer.srid = uint64(lcrs.SRID)
			layer.crsExplicit = providerSRIDExplicit || lcrs.Explicit

			// Raw custom-SQL contract: wkb/wkt cannot use the !BBOX! token;
			// bounds-backed custom SQL (explicit MOS; explicit gpkg; unset
			// format when the SQL uses the token — its !BBOX! expands into
			// the bounds-fields predicate) must carry it.
			if verr := codec.ValidateRawCustomSQL(layerName, layer.geometryFormat, customSQL, conf.BboxToken, "!BOX!"); verr != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, verr)
			}
			sqlHasBBox := codec.SQLHasBBoxToken(customSQL, conf.BboxToken, "!BOX!")
			boundsBacked := layer.geometryFormat == codec.FormatMOS || layer.geometryFormat == GeometryFormatGPKG || (layer.geometryFormat == "" && sqlHasBBox)
			if rerr := codec.RequireBBoxCustomSQL(layerName, boundsBacked, customSQL, conf.BboxToken, "!BOX!"); rerr != nil {
				return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, rerr)
			}

			// Shared probe preparation: the probe always executes the SQL
			// without a spatial filter — !BBOX!/!BOX! neutralized to 1=1 and
			// position/zoom/scale tokens to permissive values, in the ONE
			// documented order of codec.PrepareProbeSQL — wrapped with the
			// provider sample limit.
			var probeGeomType string
			if layer.geomType != nil {
				probeGeomType = codec.GeomTypeName(layer.geomType)
			}
			inspectionSQL := codec.PrepareProbeSQL(customSQL, layer.geomFieldname, layer.idFieldname, probeGeomType)
			probeSQL := codec.WrapProbeSQL(inspectionSQL)

			// Bounds-backed / storage-format probe. Runs for explicit MOS,
			// explicit gpkg and unset format (native gpkg decode; inference
			// can resolve to MOS), including tile-dependent SQL and layers
			// with an explicit geometry_type: the structural result-column
			// contract is always validated at registration and never skipped
			// (the >=3-sample-row evidence bar is inference-only).
			if layer.geometryFormat != codec.FormatWKB && layer.geometryFormat != codec.FormatWKT {
				columns, contract, perr := probeMOSCustomSQLContract(db, &layer, probeSQL)
				switch {
				case perr != nil && boundsBacked:
					// bounds-backed structural check failures are startup
					// errors (A02/A06), never warnings.
					return nil, fmt.Errorf("layer '%v' problem probing bounds-backed custom SQL: %v", layerName, perr)
				case perr != nil:
					log.Warnf("layer '%v': custom SQL storage-format probe failed; format not detected: %v", layerName, perr)
				default:
					mosEvidence := contract.ValidMOSRows >= codec.MinValidMOSRows
					if boundsBacked || mosEvidence {
						if cerr := codec.ValidateBoundsSQLContract(layerName, customSQL, layer.geomFieldname, contract); cerr != nil {
							return nil, fmt.Errorf("for layer (%v) %v: %w", i, layerName, cerr)
						}
						// persist the ACTUAL result-column names (A09): they
						// are what the runtime predicate must quote.
						layer.bboxFields = contract.BoundsFields
						if contract.GeometryField != "" {
							layer.geomFieldname = contract.GeometryField
						}
						if mosEvidence {
							// MapplGIS tagging happens only on a successful
							// MOS sample (decision 4); SystemInfo rows are
							// never applied from SQL samples.
							layer.isMapplGIS = true
							layer.mapplSource = codec.MapplGISSQLSample
							if layer.geometryFormat == "" {
								// inference resolves the unset format to MOS
								// only on positive MOS signature + >=3 rows
								// (A04/A07).
								layer.geometryFormat = codec.FormatMOS
								log.Infof("layer '%v': custom SQL switched geometry_format to %v (sql-sample detected)", layerName, codec.FormatMOS)
							}
							log.Infof("layer '%v': bounds-backed MOS custom SQL contract detected (source %v, %v valid MOS sample rows)", layerName, layer.mapplSource, contract.ValidMOSRows)
						}
						if layer.geometryFormat == codec.FormatMOS {
							if merr := codec.ValidateMOSSQLExplicitConfig(layerName, layer.crsExplicit); merr != nil {
								return nil, fmt.Errorf("layer '%v': %v", layerName, merr)
							}
						}
					} else {
						log.Warnf("layer '%v': custom SQL storage format not detected (sample columns: %v; %v decodable MOS sample rows); registering with format %q", layerName, strings.Join(columns, ", "), contract.ValidMOSRows, layer.geometryFormat)
					}
				}
			}

			// Non-metric layer CRS + scale/pixel token warning (part11 1.3,
			// code half): token values stay Web Mercator metres by design.
			codec.WarnNonMetricScaleTokens(layerName, customSQL, uint32(layer.srid), config, layerConf)

			// An explicit geometry_type skips only geometry-class inference
			// and the >=3-sample-row evidence bar, never the structural
			// validation above (A03). Empty result sets are allowed for
			// explicitly-typed layers (empty-layer policy).
			if gtypeExplicit {
				p.layers[layer.name] = &layer
				continue
			}

			// Tile-dependent SQL may filter out everything at the sample
			// tile, so defer geometry-type inference; the structural
			// result-column check above has already run (A08).
			if customSQLNeedsDeferredInspection(customSQL) {
				log.Warnf("layer '%v' uses tile-dependent custom SQL; deferring startup geometry inspection", layerName)
				p.layers[layer.name] = &layer
				continue
			}

			// Get geometry type & srid from geometry of first row. For raw
			// formats the whole sample window is scanned because MOS
			// system-info blobs may be stored at any position before or
			// after the first decodable geometry.
			qgeom := quoteIdent(layer.geomFieldname)
			qtext := fmt.Sprintf("SELECT %[1]v FROM (%[2]v) WHERE %[1]v IS NOT NULL LIMIT %[4]v;", qgeom, inspectionSQL, qgeom, codec.InspectionSampleLimit)

			firstGeom, firstHeader, sysInfoCRSApplied, ierr := inspectCustomSQLSample(db, &layer, qtext)
			if ierr != nil {
				return nil, ierr
			}

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
				p.layers[layer.name] = &layer
				continue

			default:
				// as above: an explicit provider-level srid always wins over the value
				// decoded from the sampled row's WKB header, which is frequently 0 or
				// otherwise unreliable for GPKGs produced by third-party tooling.
				if sysInfoCRSApplied {
					// the MOS system-info projection already resolved the
					// layer SRID (registered as a synthetic SRID); keep it.
					layer.geomType = firstGeom
					break
				}
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

		p.layers[layer.name] = &layer
	}

	// track the provider so we can clean it up later
	providersMu.Lock()
	providers = append(providers, &p)
	providersMu.Unlock()
	keepDB = true

	return &p, err
}

// reference to all instantiated providers
var providers []*Provider
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

	providers = make([]*Provider, 0)
}

// inspectCustomSQLSample executes an inspection query built from custom SQL
// and inspects the returned sample rows. For raw formats the whole window is
// scanned: MOS system-info blobs are recognized as metadata and skipped (no
// auto-detection for custom SQL — audit A-01), and the first decodable
// geometry infers the layer's geometry type. For native GeoPackage geometry
// the first row's binary header and geometry are returned. sysInfoCRSApplied
// is always false: kept in the signature for callers that treat the SRID as
// already resolved when a system-info projection was applied.
func inspectCustomSQLSample(db *sql.DB, layer *Layer, qtext string) (firstGeom geom.Geometry, firstHeader *BinaryHeader, sysInfoCRSApplied bool, err error) {
	layerName := layer.Name()
	log.Debugf("qtext: %v", qtext)

	inspectRows, qerr := db.Query(qtext)
	if qerr != nil {
		return nil, nil, false, fmt.Errorf("layer '%v' problem executing custom SQL: %v", layerName, qerr)
	}
	defer func() { _ = inspectRows.Close() }()

	for inspectRows.Next() {
		var geomData interface{}
		if serr := inspectRows.Scan(&geomData); serr != nil {
			return nil, nil, false, fmt.Errorf("layer '%v' problem reading custom SQL row: %v", layerName, serr)
		}

		if codec.IsRawFormat(layer.geometryFormat) {
			// The MapplGIS LayerInfo blob is metadata, never a feature.
			// Custom SQL layers are never auto-detected and never apply
			// system-info parameters from result rows (audit A-01): the
			// row is skipped so decoding continues with the explicitly
			// configured format/precision/units/CRS.
			if layer.geometryFormat == codec.FormatMOS && codec.IsSystemInfoValue(geomData) {
				continue
			}
			if firstGeom != nil {
				// geometry type already inferred
				continue
			}
			_, geo, derr := decodeGeometryValue(geomData, layer.geometryFormat, layer.mosConfig)
			if derr != nil {
				return nil, nil, false, fmt.Errorf("layer '%v' decode %v geometry: %v", layerName, layer.geometryFormat, derr)
			}
			if geo != nil {
				firstGeom = geo
			}
			continue
		}

		geomDataBytes, ok := geomData.([]byte)
		if !ok {
			return nil, nil, false, errors.New("unexpected column type for geom field. expected blob")
		}
		h, geo, derr := decodeGeometry(geomDataBytes)
		if derr != nil {
			return nil, nil, false, derr
		}
		firstHeader = h
		if geo == nil {
			// empty-geometry row (GeoPackage empty flag, audit N15):
			// keep scanning for a real sample geometry.
			continue
		}
		firstGeom = geo
		break
	}
	if rerr := inspectRows.Err(); rerr != nil {
		return nil, nil, false, fmt.Errorf("layer '%v' problem reading custom SQL rows: %v", layerName, rerr)
	}
	return firstGeom, firstHeader, sysInfoCRSApplied, nil
}

// probeMOSCustomSQLContract executes the wrapped probe SQL (built by
// codec.WrapProbeSQL over codec.PrepareProbeSQL output) and returns the
// sample result-column names together with the common
// InspectSQLGeometryContract probe: bounds columns presence plus at least
// MinValidMOSRows decodable MOS geometries with coordinates. The probe
// executes the SQL without a spatial filter and reads at most
// codec.InspectionSampleLimit rows. Real scanned row values reach the
// decode closure (A01). Native GeoPackage decode is primary for the unset/
// gpkg formats with a MOS fallback so native rows can never count as MOS
// while a positive MOS signature does (A04/7.1.3); explicit MOS decodes
// strictly as MOS. SystemInfo rows are skipped, never applied: SQL-sample
// detection carries no projection contract.
func probeMOSCustomSQLContract(db *sql.DB, layer *Layer, probeSQL string) ([]string, codec.SQLGeometryContract, error) {
	if probeSQL == "" {
		return nil, codec.SQLGeometryContract{}, fmt.Errorf("missing probing SQL")
	}

	rows, err := db.Query(probeSQL)
	if err != nil {
		return nil, codec.SQLGeometryContract{}, fmt.Errorf("error probing geometry contract for layer '%v': %w", layer.name, err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, codec.SQLGeometryContract{}, err
	}

	decode := codec.AutoRowDecode(func(value interface{}) (geom.Geometry, error) {
		if codec.IsSystemInfoValue(value) {
			// LayerInfo blob describes the layer, not a geometry: skip,
			// never decode or apply.
			return nil, fmt.Errorf("system info blob")
		}
		_, decoded, err := decodeGeometryValue(value, layer.geometryFormat, layer.mosConfig)
		return decoded, err
	}, layer.mosConfig)
	if layer.geometryFormat == codec.FormatMOS {
		decode = codec.MOSRowDecode(layer.mosConfig)
	}

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
		vals := make([]interface{}, len(columns))
		for i := range dest {
			vals[i] = *(dest[i].(*interface{}))
		}
		return vals, true, nil
	}

	contract, cerr := codec.InspectSQLGeometryContract(next, columns, layer.geomFieldname, layer.bboxFields, decode)
	if cerr != nil {
		return columns, codec.SQLGeometryContract{}, cerr
	}
	return columns, contract, nil
}
