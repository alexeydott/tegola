package mysql

import "testing"

func TestParseProj4ConfigValue(t *testing.T) {
	// string form: newline- and semicolon-separated entries
	defs, err := parseProj4ConfigValue("3844=+proj=aea +lat_1=46;\nEPSG:32601 = +proj=utm +zone=1")
	if err != nil {
		t.Fatalf("string form: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(defs), defs)
	}
	if defs[3844] != "+proj=aea +lat_1=46" {
		t.Errorf("entry 3844 wrong: %q", defs[3844])
	}
	if defs[32601] != "+proj=utm +zone=1" {
		t.Errorf("entry 32601 wrong: %q", defs[32601])
	}

	// table form (TOML table arrives as map[string]interface{})
	defs, err = parseProj4ConfigValue(map[string]interface{}{
		"3844":     "+proj=aea +lat_1=46",
		"EPSG:28408": "+proj=etmerc +lon_0=21",
	})
	if err != nil {
		t.Fatalf("table form: %v", err)
	}
	if len(defs) != 2 || defs[3844] != "+proj=aea +lat_1=46" || defs[28408] != "+proj=etmerc +lon_0=21" {
		t.Errorf("table form wrong: %v", defs)
	}

	// error cases
	if _, err := parseProj4ConfigValue(42); err == nil {
		t.Error("expected error for unsupported type, got nil")
	}
	if _, err := parseProj4ConfigValue("nonsense"); err == nil {
		t.Error("expected error for entry without '=', got nil")
	}
	if _, err := parseProj4ConfigValue(map[string]interface{}{"EPSG:xx": "+proj=merc"}); err == nil {
		t.Error("expected error for non-numeric srid key, got nil")
	}
	if _, err := parseProj4ConfigValue(map[string]interface{}{"3844": 42}); err == nil {
		t.Error("expected error for non-string value, got nil")
	}
}

func TestParseSRIDKey(t *testing.T) {
	for key, want := range map[string]uint64{
		"3844":       3844,
		" 28408 ":    28408,
		"EPSG:32601": 32601,
		"epsg:3857":  3857,
	} {
		got, err := parseSRIDKey(key)
		if err != nil {
			t.Errorf("parseSRIDKey(%q): %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("parseSRIDKey(%q) = %d, want %d", key, got, want)
		}
	}
	for _, key := range []string{"", "   ", "EPSG:", "abc", "12.5"} {
		if _, err := parseSRIDKey(key); err == nil {
			t.Errorf("parseSRIDKey(%q): expected error, got nil", key)
		}
	}
}
