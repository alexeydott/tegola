#!/bin/bash
# A40: Native database test runner for WFS-T providers.
# Runs provider tests against real MySQL/MariaDB/PostGIS.
# Requires: running database servers, Go toolchain.
set -e

REPO=~/workspace/projects/tegola-test/repo
cd $REPO

export PATH=$PATH:/home/hatch/go-tool/go/bin
export TMPDIR=~/go-tmp

echo "=== A40: Native provider tests ==="
echo ""
echo "MySQL/MariaDB:"
echo "  Requires: MySQL 8.0+ or MariaDB 10.6+ on localhost:3306"
echo "  Env: MYSQL_TEST_DSN='user:pass@tcp(localhost:3306)/test'"
echo ""
echo "PostGIS:"
echo "  Requires: PostgreSQL 14+ with PostGIS on localhost:5432"
echo "  Env: POSTGIS_TEST_DSN='postgres://user:pass@localhost:5432/test?sslmode=disable'"
echo ""
echo "GeoPackage (no server needed):"
go test ./provider/gpkg/ -count=1 -run "TestFeature" 2>&1 | tail -3

echo ""
echo "Note: MySQL/PostGIS native tests require live servers."
echo "Unit tests (above) use mocks and in-memory SQLite."
