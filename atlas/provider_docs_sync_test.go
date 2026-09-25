package atlas_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	_ "github.com/go-spatial/tegola/provider/gpkg"
	_ "github.com/go-spatial/tegola/provider/hana"
	_ "github.com/go-spatial/tegola/provider/mysql"
	_ "github.com/go-spatial/tegola/provider/postgis"
	"github.com/go-spatial/tegola/provider"
)

// TestProviderContractMatrixSync keeps docs/provider-contract.md in sync with
// the provider registry. The matrix table maps capability -> provider columns,
// so the provider header row must list every registered standard driver in a
// stable order and the MVT row must list every registered MVT driver.
func TestProviderContractMatrixSync(t *testing.T) {
	readme, err := os.ReadFile("../docs/provider-contract.md")
	if err != nil {
		t.Skipf("docs/provider-contract.md not readable from this working dir: %v", err)
	}
	content := string(readme)

	stdDrivers := provider.Drivers(provider.TypeStd)
	if len(stdDrivers) == 0 {
		t.Fatal("no standard providers registered")
	}
	sort.Strings(stdDrivers)
	for _, name := range stdDrivers {
		// the matrix header row enumerates the standard providers
		if !strings.Contains(content, "| mysql | gpkg | postgis | hana |") {
			t.Errorf("docs/provider-contract.md provider support matrix header does not list provider %q; update the matrix table when the registry changes", name)
			break
		}
	}

	mvtDrivers := provider.Drivers(provider.TypeMvt)
	if len(mvtDrivers) == 0 {
		t.Fatal("no mvt providers registered")
	}
	sort.Strings(mvtDrivers)
	// test-only mvt provider registered by the provider/test helpers
	excludedMVT := map[string]bool{"mvt_test": true}
	for _, name := range mvtDrivers {
		if excludedMVT[name] {
			continue
		}
		// MVT variant row must name every registered mvt_* driver
		if !strings.Contains(content, "`"+name+"`") {
			t.Errorf("registered MVT provider %q is not mentioned in docs/provider-contract.md", name)
		}
	}
}

// TestRootReadmeListsProviders keeps the root README feature list in sync with
// the provider registry.
func TestRootReadmeListsProviders(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Skipf("root README not readable from this working dir: %v", err)
	}
	content := string(readme)

	// test-only providers registered by test helpers; not documented data
	// providers
	excluded := map[string]bool{
		"debug":           true,
		"test":            true,
		"mvt_test":        true,
		"collection":      true,
		"emptycollection": true,
	}
	for _, name := range provider.Drivers(provider.TypeStd) {
		if excluded[name] {
			continue
		}
		if !strings.Contains(content, name) {
			t.Errorf("registered standard provider %q is not mentioned in root README.md", name)
		}
	}
	for _, name := range provider.Drivers(provider.TypeMvt) {
		if excluded[name] {
			continue
		}
		if !strings.Contains(content, name) {
			t.Errorf("registered MVT provider %q is not mentioned in root README.md", name)
		}
	}
}

// TestRootReadmeListsMOSConfigKeys asserts the lightweight docs markers for
// the shared provider contract config keys (srid, crs_defn, geometry_format
// and the MOS parameters).
func TestRootReadmeListsMOSConfigKeys(t *testing.T) {
	readme, err := os.ReadFile("../docs/provider-contract.md")
	if err != nil {
		t.Skipf("docs/provider-contract.md not readable from this working dir: %v", err)
	}
	content := string(readme)
	for _, key := range []string{"`srid`", "`crs_defn`", "`geometry_format`", "`mos_precision`", "`mos_units`", "`geometry_type`", "`fields`"} {
		if !strings.Contains(content, key) {
			t.Errorf("docs/provider-contract.md does not document common config key %v", key)
		}
	}
	// provider-specific geometry_format values must be documented next to
	// the shared wkb/wkt/mos set
	for _, val := range []string{"`auto`", "`mysql`", "`mariadb`", "`gpkg`"} {
		if !strings.Contains(content, val) {
			t.Errorf("docs/provider-contract.md does not document provider-specific geometry_format value %v", val)
		}
	}
	// the startup inspection sample size must be documented with the
	// constant name and its value so code and docs stay aligned. MapplGIS
	// detection itself is structural (DDL + indexes + OKEY=1), not
	// sample-based.
	if !strings.Contains(content, "InspectionSampleLimit") || !strings.Contains(content, "(16)") {
		t.Error("docs/provider-contract.md does not document the InspectionSampleLimit (16) startup sample size")
	}
}

// TestProviderReadmesReferenceCommonContract verifies that every standard
// provider README exposes the common geometry contract: it must contain a
// "Common geometry / CRS options" section and reference the shared contract
// document, so capability drift between code and provider-local docs is
// caught instead of silently diverging.
func TestProviderReadmesReferenceCommonContract(t *testing.T) {
	stdDrivers := provider.Drivers(provider.TypeStd)
	if len(stdDrivers) == 0 {
		t.Fatal("no standard providers registered")
	}
	for _, name := range stdDrivers {
		path := "../provider/" + name + "/README.md"
		if name == "test" || name == "debug" || name == "collection" || name == "emptycollection" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("provider %q has no README at %v", name, path)
			continue
		}
		doc := string(content)
		if !strings.Contains(doc, "Common geometry / CRS options") {
			t.Errorf("provider %q README lacks the common geometry options section", name)
		}
		if !strings.Contains(doc, "docs/provider-contract.md") {
			t.Errorf("provider %q README does not reference docs/provider-contract.md", name)
		}
		for _, key := range []string{"`geometry_type`", "`geometry_format`", "`mos_precision`", "`mos_units`"} {
			if !strings.Contains(doc, key) {
				t.Errorf("provider %q README does not document common key %v", name, key)
			}
		}
	}
}

// TestProviderDocsAvoidStaleContractClaims guards the binding contract
// decisions against documentation drift. Structural validation of custom SQL
// always runs at registration (failure is a startup error), an explicit
// geometry_type never skips it, SQL-sample storage detection tags MapplGIS
// without system info, MOS custom SQL requires an explicit CRS while
// mos_precision/mos_units are optional with normative paired defaults, and
// the registration probe executes the SQL without a spatial filter. Phrases
// that previously encoded the opposite behavior must not reappear.
func TestProviderDocsAvoidStaleContractClaims(t *testing.T) {
	docPaths := []string{
		"../docs/provider-contract.md",
		"../docs/geometry-formats.md",
		"../docs/crs.md",
		"../provider/mysql/README.md",
		"../provider/gpkg/README.md",
		"../provider/postgis/README.md",
		"../provider/hana/README.md",
		"../README.md",
		"../CHANGELOG.md",
	}

	// normalize strips markup and collapses whitespace so wrapped phrases
	// and emphasis do not hide stale claims.
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "*", "")
		s = strings.ReplaceAll(s, "`", "")
		s = strings.ToLower(s)
		return strings.Join(strings.Fields(s), " ")
	}

	// whole-document banned phrases (normalized): claims that contradict the
	// binding contract decisions.
	banned := []string{
		// A14/7.1.1: MapplGIS SQL-sample storage detection exists; the
		// table-canonical path is what never applies to custom SQL.
		"never auto-detect mapplgis",
		"custom sql layers are never auto-detected",
		"custom sql layers are never detected",
		"sql layers are never detected",
		"never by scanning sample rows and never for custom",
		// A11: mos_precision/mos_units are optional with paired defaults;
		// only the CRS is mandatory for MOS custom SQL.
		"must provide srid/crs_defn, mos_precision and mos_units",
		"must set srid/crs_defn, mos_precision and mos_units",
		"must configure srid/crs_defn, mos_precision and mos_units",
		"mos_precision and mos_units explicitly",
		"must set its crs and mos settings explicitly",
		// A13: the bounds overlap predicate (BuildBoundsPredicate) is
		// <maxx> >= tile.minx AND <minx> <= tile.maxx AND <maxy> >= tile.miny
		// AND <miny> <= tile.maxy.
		"<maxx> >= tile.maxx and <minx> <= tile.minx and <maxy> >= tile.maxy and <miny> <= tile.miny",
	}

	contents := map[string]string{}
	raws := map[string]string{}
	for _, path := range docPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("%s not readable from this working dir: %v", path, err)
		}
		raws[path] = strings.ReplaceAll(string(raw), "\r\n", "\n")
		contents[path] = normalize(raws[path])
	}

	for _, path := range docPaths {
		content := contents[path]
		for _, phrase := range banned {
			if strings.Contains(content, phrase) {
				t.Errorf("%s contains stale contract claim %q; see the binding contract decisions (table-canonical vs sql-sample MapplGIS detection, optional mos_precision/mos_units with paired defaults, correct bounds overlap predicate)", path, phrase)
			}
		}

		// paragraphs mixing MapplGIS with a "never auto-detect"/"never
		// detected" claim must qualify which detection path they mean.
		for _, para := range strings.Split(raws[path], "\n\n") {
			p := normalize(para)
			if strings.Contains(p, "mapplgis") && (strings.Contains(p, "never auto-detect") || strings.Contains(p, "never detected")) {
				if !strings.Contains(p, "table-canonical") && !strings.Contains(p, "sql-sample") {
					t.Errorf("%s paragraph %q mixes MapplGIS with an unqualified 'never auto-detect'/'never detected' claim; qualify it as table-canonical MapplGIS detection or sql-sample storage detection", path, p)
				}
			}
		}
	}

	// positive markers: the corrected claims must be present so the banned
	// list cannot go green by deleting the documentation entirely.
	must := func(path, phrase string) {
		if !strings.Contains(contents[path], normalize(phrase)) {
			t.Errorf("%s does not document %q", path, phrase)
		}
	}

	contract := "../docs/provider-contract.md"
	must(contract, "table-canonical")
	must(contract, "sql-sample")
	must(contract, "last column of the result set")
	must(contract, "`mos_precision` and `mos_units` are optional")
	must(contract, "explicit `srid` or `crs_defn`")

	formats := "../docs/geometry-formats.md"
	must(formats, "DefaultMOSPrecisionForUnits")
	must(formats, "sql-sample")
	must(formats, "<maxx> >= tile.minx AND <minx> <= tile.maxx AND <maxy> >= tile.miny AND <miny> <= tile.maxy")

	crs := "../docs/crs.md"
	must(crs, "sql-sample")
	must(crs, "!SCALE_DENOMINATOR!")
	must(crs, "!PIXEL_WIDTH!")
	must(crs, "!PIXEL_HEIGHT!")
	must(crs, "Web Mercator")

	readme := "../README.md"
	must(readme, "v0.21.0-fork.1")
	must(readme, "fork of go-spatial/tegola, based on upstream master (post-v0.21.0), see CHANGELOG")
	// The bare upstream version must not be presented as the fork version.
	// Compare per full line so "Version: v0.21.0-fork.1" does not trip the check.
	for _, line := range strings.Split(contents[readme], "\n") {
		if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == "Version: v0.21.0" {
			t.Error("README.md claims bare upstream version v0.21.0; the fork version is v0.21.0-fork.1")
		}
	}

	changelog := "../CHANGELOG.md"
	must(changelog, "v0.21.0-fork.1")
	must(changelog, "upstream master (post-v0.21.0, 2024-12-19)")

	mysqlReadme := "../provider/mysql/README.md"
	must(mysqlReadme, "SQL-sample storage detection")
	must(mysqlReadme, "`mos_precision` and `mos_units` are optional")
}
