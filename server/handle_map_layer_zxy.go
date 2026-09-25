package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/dimfeld/httptreemux"
	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/mvt"
	"github.com/go-spatial/geom/slippy"
	"github.com/go-spatial/proj"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/atlas"
	"github.com/go-spatial/tegola/cache"
	"github.com/go-spatial/tegola/internal/log"
	"github.com/go-spatial/tegola/maths"
	"github.com/go-spatial/tegola/observability"
	"github.com/go-spatial/tegola/provider"
)

var (
	webmercatorGrid = slippy.NewGrid(3857, 0)
	tileUpdateLocks = newTileUpdateCoordinator()
)

type HandleMapLayerZXY struct {
	// required
	mapName string
	// optional
	layerName string
	// zoom
	z uint
	// row
	x uint
	// column
	y uint
	// the requests extension (i.e. pbf or json)
	// defaults to "pbf"
	extension string
	// debug
	debug bool
	// the Atlas to use, nil (default) is the default atlas
	Atlas *atlas.Atlas
}

const (
	tileOperationStatus     = "status"
	tileOperationUpdate     = "update"
	tileOperationGetUpdated = "getupdated"
	metatileSize            = uint(8)
)

type tileUpdateCoordinator struct {
	mu    sync.Mutex
	locks map[string]*tileUpdateLock
}

type tileUpdateLock struct {
	mu   sync.Mutex
	refs int
}

func newTileUpdateCoordinator() *tileUpdateCoordinator {
	return &tileUpdateCoordinator{locks: make(map[string]*tileUpdateLock)}
}

func (l *tileUpdateCoordinator) acquire(key string) func() {
	l.mu.Lock()
	lock := l.locks[key]
	if lock == nil {
		lock = &tileUpdateLock{}
		l.locks[key] = lock
	}
	lock.refs++
	l.mu.Unlock()

	lock.mu.Lock()

	return func() {
		lock.mu.Unlock()
		l.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

func (l *tileUpdateCoordinator) locked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	lock := l.locks[key]
	return lock != nil && lock.refs > 0
}

// parseURI reads the request URI and extracts the various values for the request
func (req *HandleMapLayerZXY) parseURI(r *http.Request) error {
	var err error

	params := httptreemux.ContextParams(r.Context())

	// set map name
	req.mapName = params["map_name"]
	req.layerName = params["layer_name"]

	var placeholder uint64

	z := params["z"]
	placeholder, err = strconv.ParseUint(z, 10, 32)
	if err != nil || placeholder > tegola.MaxZ {
		log.Warnf("invalid Z value (%v)", z)
		return fmt.Errorf("invalid Z value (%v)", z)
	}
	req.z = uint(placeholder)

	maxXYatZ := maths.Exp2(placeholder) - 1

	x := params["x"]
	placeholder, err = strconv.ParseUint(x, 10, 32)
	if err != nil || placeholder > maxXYatZ {
		log.Warnf("invalid X value (%v)", x)
		return fmt.Errorf("invalid X value (%v)", x)
	}
	req.x = uint(placeholder)

	// trim the "y" param in the url in case it has an extension
	y := params["y"]
	yParts := strings.Split(y, ".")
	placeholder, err = strconv.ParseUint(yParts[0], 10, 32)
	if err != nil || placeholder > maxXYatZ {
		log.Warnf("invalid Y value (%v)", yParts[0])
		return fmt.Errorf("invalid Y value (%v)", yParts[0])
	}

	req.y = uint(placeholder)

	// check if we have a file extension
	if len(yParts) > 1 && yParts[len(yParts)-1] != "" {
		req.extension = yParts[len(yParts)-1]
	} else {
		req.extension = "pbf"
	}

	// Only MVT ("pbf") tile output is implemented. Other extensions (e.g. "json")
	// are not supported and are served as pbf; warn so the fallback is not silent.
	// Wiring real extension-aware output is tracked as deferred debt (UPSTREAM.md).
	if req.extension != "pbf" {
		log.Warnf("unsupported tile extension %q; serving tile as pbf (mvt)", req.extension)
	}

	// check for debug request
	if r.URL.Query().Get(QueryKeyDebug) == "true" {
		req.debug = true
	}

	return nil
}

// URI scheme: /maps/:map_name/:layer_name/:z/:x/:y?param=value
// map_name - map name in the config file
// layer_name - name of the single map layer to render
// z, x, y - tile coordinates as described in the Slippy Map Tilenames specification
//
//	z - zoom level
//	x - row
//	y - column
//
// param - configurable query parameters and their values
func (req HandleMapLayerZXY) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// parse our URI
	if err := req.parseURI(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// lookup our Map
	m, err := req.Atlas.Map(req.mapName)
	if err != nil {
		errMsg := fmt.Sprintf("map (%v) not configured. check your config file", req.mapName)
		log.Error(errMsg)
		http.Error(w, errMsg, http.StatusNotFound)
		return
	}

	// filter down the layers we need for this zoom
	m = m.FilterLayersByZoom(slippy.Zoom(req.z))
	if len(m.Layers) == 0 {
		msg := fmt.Sprintf("map (%v) has no layers, at zoom %v", req.mapName, req.z)
		log.Debug(msg)
		http.Error(w, msg, http.StatusNotFound)
		return
	}

	if req.layerName != "" {
		m = m.FilterLayersByName(req.layerName)
		if len(m.Layers) == 0 {
			msg := fmt.Sprintf("map (%v) has no layers, for LayerName %v at zoom %v", req.mapName, req.layerName, req.z)
			log.Debug(msg)
			http.Error(w, msg, http.StatusNotFound)
			return

		}
	}

	tile := slippy.Tile{Z: slippy.Zoom(req.z), X: req.x, Y: req.y}

	{
		// Check to see that the zxy is within the bounds of the map.
		// TODO(@ear7h): use a more efficient version of Intersect that doesn't
		// make a new extent
		ext3857, err := slippy.Extent(webmercatorGrid, tile)
		if err != nil {
			msg := fmt.Sprintf("map (%v -- %v) does not contains tile at %v/%v/%v. Unable to generate extent.", req.mapName, m.Bounds, req.z, req.x, req.y)
			log.Debug(msg, err)
			http.Error(w, msg, http.StatusNotFound)
			return
		}

		points4326, err := proj.Inverse(proj.WebMercator, ext3857[:])
		if err != nil {
			msg := fmt.Sprintf("Unable to convert 3857 to 4326 for map (%v -- %v) and tile %v/%v/%v -- %v.", req.mapName, m.Bounds, req.z, req.x, req.y, ext3857)
			log.Error(msg)
			http.Error(w, msg, http.StatusNotFound)
			return
		}

		ext4326 := &geom.Extent{}
		copy(ext4326[:], points4326)
		if !extentsIntersect(m.Bounds, ext4326) {
			msg := fmt.Sprintf("map (%v -- %v) does not contains tile at %v/%v/%v -- %v", req.mapName, m.Bounds, req.z, req.x, req.y, ext4326)
			log.Debug(msg)
			http.Error(w, msg, http.StatusNotFound)
			return
		}
	}

	// check for the debug query string
	if req.debug {
		m = m.AddDebugLayers()
	}

	operation, hasOperation := r.URL.Query()[QueryKeyTile]
	if hasOperation {
		if err := validateTileOperationQuery(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.debug {
			http.Error(w, "tile operations cannot be combined with debug", http.StatusBadRequest)
			return
		}

		if err := req.serveTileOperation(w, r, m, tile, operation[0]); err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return
			default:
				log.Error(err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
		return
	}

	// check for query parameters and populate param map with their values
	params, err := extractParameters(m, r)
	if err != nil {
		log.Error(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	encodeCtx := context.WithValue(r.Context(), observability.ObserveCtxKey(observability.ObserveVarMapName), m.Name)
	pbyte, err := m.Encode(encodeCtx, tile, params)

	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			log.Debugf("tile encode canceled for map %v z:%v x:%v y:%v", req.mapName, req.z, req.x, req.y)
			return
		case strings.Contains(err.Error(), "operation was canceled"):
			log.Debugf("tile encode canceled for map %v z:%v x:%v y:%v", req.mapName, req.z, req.x, req.y)
			return
		default:
			errMsg := fmt.Sprintf("error marshalling tile: %v", err)
			log.Error(errMsg)
			http.Error(w, errMsg, http.StatusInternalServerError)
			return
		}
	}

	// mimetype for mapbox vector tiles
	// https://www.iana.org/assignments/media-types/application/vnd.mapbox-vector-tile
	w.Header().Add("Content-Type", mvt.MimeType)
	w.Header().Add("Content-Length", fmt.Sprintf("%d", len(pbyte)))
	w.WriteHeader(http.StatusOK)

	_, err = w.Write(pbyte)
	if err != nil {
		log.Errorf("error writing tile z:%v, x:%v, y:%v - %v", req.z, req.x, req.y, err)
	}

	// check for tile size warnings
	if len(pbyte) > MaxTileSize {
		slog.Default().Info("tile is rather large",
			slog.String("map", req.mapName),
			slog.String("layer", req.layerName),
			slog.Uint64("z", uint64(req.z)),
			slog.Uint64("x", uint64(req.x)),
			slog.Uint64("y", uint64(req.y)),
			slog.Int("size_kb", len(pbyte)/1024),
		)
	}
}

func validateTileOperationQuery(r *http.Request) error {
	query := r.URL.Query()
	operations, ok := query[QueryKeyTile]
	if len(query) != 1 || !ok || len(operations) != 1 {
		return fmt.Errorf("%s cannot be combined with other query parameters", QueryKeyTile)
	}

	switch operations[0] {
	case tileOperationStatus, tileOperationUpdate, tileOperationGetUpdated:
		return nil
	default:
		return fmt.Errorf("invalid %s operation %q", QueryKeyTile, operations[0])
	}
}

type tileStatusResponse struct {
	Map      string  `json:"map"`
	Layer    string  `json:"layer,omitempty"`
	Z        uint    `json:"z"`
	X        uint    `json:"x"`
	Y        uint    `json:"y"`
	Cached   bool    `json:"cached"`
	Updating bool    `json:"updating"`
	Metatile [4]uint `json:"metatile"`
}

func (req HandleMapLayerZXY) serveTileOperation(w http.ResponseWriter, r *http.Request, m atlas.Map, tile slippy.Tile, operation string) error {
	cacher := req.Atlas.GetCache()
	w.Header().Set("Cache-Control", "no-store")
	key := req.tileCacheKey(tile)
	metatileKey := req.metatileLockKey(tile)

	if operation == tileOperationStatus {
		cached := false
		if cacher != nil {
			var err error
			_, cached, err = cacher.Get(r.Context(), &key)
			if err != nil {
				return fmt.Errorf("read tile status from cache: %w", err)
			}
		}

		baseX := (tile.X / metatileSize) * metatileSize
		baseY := (tile.Y / metatileSize) * metatileSize
		maxXY := uint(maths.Exp2(uint64(tile.Z)) - 1)
		endX := minUint(baseX+metatileSize-1, maxXY)
		endY := minUint(baseY+metatileSize-1, maxXY)
		status := tileStatusResponse{
			Map:      req.mapName,
			Layer:    req.layerName,
			Z:        uint(tile.Z),
			X:        tile.X,
			Y:        tile.Y,
			Cached:   cached,
			Updating: tileUpdateLocks.locked(metatileKey),
			Metatile: [4]uint{baseX, baseY, endX - baseX + 1, endY - baseY + 1},
		}

		var jsonBuffer bytes.Buffer
		if err := json.NewEncoder(&jsonBuffer).Encode(status); err != nil {
			return fmt.Errorf("encode tile status: %w", err)
		}

		var gzipBuffer bytes.Buffer
		gzipWriter := gzip.NewWriter(&gzipBuffer)
		if _, err := gzipWriter.Write(jsonBuffer.Bytes()); err != nil {
			return fmt.Errorf("compress tile status: %w", err)
		}
		if err := gzipWriter.Close(); err != nil {
			return fmt.Errorf("finish compressed tile status: %w", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(gzipBuffer.Len()))
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(gzipBuffer.Bytes())
		return err
	}

	if cacher == nil {
		return fmt.Errorf("tile operation %q requires a configured cache", operation)
	}

	params, err := extractParameters(m, r)
	if err != nil {
		return fmt.Errorf("parse tile parameters: %w", err)
	}

	unlock := tileUpdateLocks.acquire(metatileKey)
	defer unlock()

	ctx := context.WithValue(r.Context(), observability.ObserveCtxKey(observability.ObserveVarMapName), m.Name)
	maxXY := uint(maths.Exp2(uint64(tile.Z)) - 1)
	baseX := (tile.X / metatileSize) * metatileSize
	baseY := (tile.Y / metatileSize) * metatileSize
	endX := minUint(baseX+metatileSize-1, maxXY)
	endY := minUint(baseY+metatileSize-1, maxXY)

	var updated []byte
	for y := baseY; y <= endY; y++ {
		for x := baseX; x <= endX; x++ {
			if err := ctx.Err(); err != nil {
				return err
			}

			current := slippy.Tile{Z: tile.Z, X: x, Y: y}
			encoded, err := m.Encode(ctx, current, params)
			if err != nil {
				return fmt.Errorf("encode tile %d/%d/%d: %w", current.Z, current.X, current.Y, err)
			}

			currentKey := req.tileCacheKey(current)
			if err := cacher.Set(ctx, &currentKey, encoded); err != nil {
				return fmt.Errorf("cache tile %d/%d/%d: %w", current.Z, current.X, current.Y, err)
			}

			if current == tile {
				updated = encoded
			}
		}
	}

	if operation == tileOperationUpdate {
		w.Header().Del("Content-Encoding")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	w.Header().Set("Content-Type", mvt.MimeType)
	w.Header().Set("Content-Length", strconv.Itoa(len(updated)))
	w.Header().Set("Tegola-Cache", "MISS")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(updated)
	return err
}

func (req HandleMapLayerZXY) tileCacheKey(tile slippy.Tile) cache.Key {
	return cache.Key{
		MapName:   req.mapName,
		LayerName: req.layerName,
		Z:         uint(tile.Z),
		X:         tile.X,
		Y:         tile.Y,
	}
}

func (req HandleMapLayerZXY) metatileLockKey(tile slippy.Tile) string {
	return metatileLockKeyForCacheKey(&cache.Key{
		MapName:   req.mapName,
		LayerName: req.layerName,
		Z:         uint(tile.Z),
		X:         tile.X,
		Y:         tile.Y,
	})
}

func metatileLockKeyForCacheKey(key *cache.Key) string {
	baseX := (key.X / metatileSize) * metatileSize
	baseY := (key.Y / metatileSize) * metatileSize
	return fmt.Sprintf("%s/%s/%d/%d/%d", key.MapName, key.LayerName, key.Z, baseX, baseY)
}

// extentsIntersect reports whether a and b overlap. It matches the boolean
// result of geom.Extent.Intersect (edge-touching counts as no overlap) but
// without allocating a result extent. A nil extent is treated as the universe,
// matching the geom accessors.
func extentsIntersect(a, b *geom.Extent) bool {
	return !(a.MinX() >= b.MaxX() || b.MinX() >= a.MaxX() ||
		a.MinY() >= b.MaxY() || b.MinY() >= a.MaxY())
}

func minUint(a, b uint) uint {
	if a < b {
		return a
	}
	return b
}

func extractParameters(m atlas.Map, r *http.Request) (provider.Params, error) {
	var params provider.Params
	if len(m.Params) > 0 {
		params = make(provider.Params)
		err := r.ParseForm()
		if err != nil {
			return nil, err
		}

		for _, param := range m.Params {
			if r.Form.Has(param.Name) {
				val, err := param.ToValue(r.Form.Get(param.Name))
				if err != nil {
					return nil, err
				}
				params[param.Token] = val
			} else {
				p, err := param.ToDefaultValue()
				if err != nil {
					return nil, err
				}
				params[param.Token] = p
			}
		}
	}
	return params, nil
}
