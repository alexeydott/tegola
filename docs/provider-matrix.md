# Provider Write Matrix (A42)

## Status as of 2026-10-04

| Provider | Read | Write (Insert) | Write (Update) | Write (Delete) | Write (Replace) | Tested |
|----------|------|----------------|----------------|----------------|-----------------|--------|
| PostGIS  | ✅   | ✅             | ✅             | ✅             | ✅              | Unit (mock) |
| MySQL/MariaDB | ✅ | ✅         | ✅             | ✅             | ✅              | Unit (mock) |
| GeoPackage (GPKG) | ✅ | ✅       | ✅             | ✅             | ✅              | ✅ Real SQLite |
| HANA     | ✅   | ❌             | ❌             | ❌             | ❌              | Read-only |

## Notes

- **PostGIS/MySQL**: Write paths implemented and unit-tested. Live DB tests
  require `TEST_PG_DSN` / `TEST_MYSQL_DSN` (see `scripts/native-test.sh`).
- **GPKG**: Fully tested against real SQLite files (see
  `provider/gpkg/mutation_contract_test.go`, `cas_concurrency_test.go`,
  `incarnation_lifecycle_test.go`).
- **HANA**: Read-only. The `MutationProvider` interface is not implemented.
  To add write support, implement `BeginFeatureTx` following the
  PostGIS provider as a template. Test with:
  ```
  TEST_HANA_DSN="hdb://user:pass@host:30015" go test ./provider/hana/ -run TestWrite
  ```
  (Requires Docker HANA in the test environment.)

## A42 Closure

The matrix above is the honest declaration. HANA write support is not
claimed. The `MutationProviderFor` returns an error for HANA collections,
so the API correctly reports "provider does not support mutations"
instead of silently failing.
