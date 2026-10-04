# A03: Committed concurrency tests — план

## Проблема
In-transaction CAS (SELECT FOR UPDATE) реализован для MySQL/PostGIS/GPKG,
но нет committed тестов, доказывающих, что concurrent writes с If-Match
корректно дают 412, а не lost update.

## Шаги
1. Тест: два concurrent UPDATE с одинаковым If-Match → один 200, второй 412
   - Использовать SQLite (GPKG) in-memory для детерминизма
   - Барьер: обе транзакции читают revision, затем пишут
2. Тест: If-Match со stale revision → 412 Precondition Failed
3. Тест: If-Match с актуальным revision → 200 и revision инкрементирован
4. Тест: без If-Match → last-write-wins (200)
5. Для MySQL/PostGIS: SQL-уровень тест с двумя соединениями (skip если нет БД)

## Критерий закрытия
- `go test ./provider/gpkg/ -run TestConcurrentCAS` PASS
- Тесты закоммичены, детерминированы (без sleep-хаков, через каналы/барьеры)
