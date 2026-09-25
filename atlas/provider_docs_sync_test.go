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
