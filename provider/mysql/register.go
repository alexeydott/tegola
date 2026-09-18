package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/go-spatial/geom"
	conf "github.com/go-spatial/tegola/config"
	"github.com/go-spatial/tegola/dict"
	"github.com/go-spatial/tegola/internal/log"
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

// geomTypeFromColumn scans the first row of the given query for a geometry
// value and decodes it to a tegola geometry type plus the SRID decoded from
// the geometry header (0 for plain WKB). It returns sql.ErrNoRows when the
// query yields no rows.
func geomTypeFromColumn(db *sql.DB, qtext string, geometryFormat string, serverFlavor string) (geom.Geometry, uint64, error) {
	var geomData []byte
	if err := db.QueryRow(qtext).Scan(&geomData); err != nil {
		return nil, 0, err
	}

	srid, geo, err := decodeGeometry(geomData, geometryFormat, serverFlavor)
	if err != nil {
		return nil, 0, fmt.Errorf("error decoding sampled geometry: %v", err)
	}
	return geo, srid, nil
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

	geometryFormat := GeometryFormatAuto
	if geometryFormat, err = config.String(ConfigKeyGeometryFormat, &geometryFormat); err != nil {
		return nil, err
	}
	switch geometryFormat {
	case GeometryFormatAuto, GeometryFormatMySQL, GeometryFormatMariaDB, GeometryFormatWKB, "":
	default:
		return nil, fmt.Errorf("invalid %v: %v (expected one of: %v, %v, %v, %v)",
			ConfigKeyGeometryFormat, geometryFormat, GeometryFormatAuto, GeometryFormatMySQL, GeometryFormatMariaDB, GeometryFormatWKB)
	}

	dsn := fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?parseTime=true&multiStatements=true",
		user, password, host, port, database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("unable to open mysql connection to %v:%v/%v: %v", host, port, database, err)
	}
	db.SetMaxOpenConns(maxConn)

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

	// names of custom-SQL layers that currently match 0 rows and were
	// therefore skipped (not registered). A handful of empty layers alongside
	// otherwise-populated ones is fine, but if EVERY configured layer comes
	// back empty that's a strong signal of a real misconfiguration, so we
	// still want a hard error in that case. Mirrors the gpkg provider.
	var emptyLayerNames []string

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
			name:          layerName,
			idFieldname:   idFieldname,
			geomFieldname: geomFieldname,
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

			geo, headerSRID, err := geomTypeFromColumn(db, inspectionSQL, geometryFormat, serverFlavor)
			switch {
			case err == sql.ErrNoRows:
				log.Warnf("layer '%v' (table %v) currently returns 0 rows; skipping registration of this layer until matching data exists", layerName, tablename)
				emptyLayerNames = append(emptyLayerNames, layerName)
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

				layer.tablename = tablename
				layer.tagFieldnames = tagFieldnames
				layer.geomType = geo
				layer.srid = uint64(lsrid)
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

			inspectionSQL := tokenReplacer.Replace(customSQL)
			inspectionTile := provider.NewTile(0, 0, 0, 0, uint(srid))
			inspectionExtent, _ := inspectionTile.BufferedExtent()
			inspectionSQL = replaceTokens(inspectionSQL, &layer, inspectionTile, inspectionExtent)

			// MySQL-derived tables require an alias
			qtext := fmt.Sprintf("SELECT %v FROM (%v) AS __tegola_inspection LIMIT 1;",
				quoteIdentifier(geomFieldname), inspectionSQL)

			log.Debugf("qtext: %v", qtext)

			geo, headerSRID, err := geomTypeFromColumn(db, qtext, geometryFormat, serverFlavor)
			switch {
			case err == sql.ErrNoRows:
				log.Warnf("layer '%v' with custom SQL currently returns 0 rows; skipping registration of this layer until matching data exists: %v", layerName, customSQL)
				emptyLayerNames = append(emptyLayerNames, layerName)
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

				layer.geomType = geo
				layer.srid = uint64(lsrid)
				layer.idFieldname = idFieldname
			}
		}

		p.layers[layer.name] = layer
	}

	// if every single configured layer came back empty, that's very unlikely
	// to be legitimate "no data yet" - it's much more likely a real
	// misconfiguration, so fail loudly instead of starting a provider that has
	// no chance of ever rendering anything. Mirrors the gpkg provider.
	if len(layers) > 0 && len(emptyLayerNames) == len(layers) {
		return nil, fmt.Errorf("mysql provider (%v:%v/%v): all %v configured layer(s) currently return 0 rows: %v; check the host, database, table names, custom SQL and any bbox/zoom filters",
			host, port, database, len(layers), strings.Join(emptyLayerNames, ", "))
	}

	// track the provider so we can clean it up later
	providers = append(providers, p)

	return &p, nil
}
