# A38: Полный incarnation lifecycle — план

## Проблема
- Incarnation инкрементируется при DELETE (сделано), но нет тестов lifecycle
- Schema fingerprint только PostGIS
- Нет тестов: delete → recreate даёт новый incarnation

## Шаги
1. Schema fingerprint для MySQL и GPKG (хеш от table definition)
   - MySQL: `SHOW CREATE TABLE` → SHA256
   - GPKG/SQLite: `SELECT sql FROM sqlite_master` → SHA256
2. Lifecycle тест (SQLite in-memory):
   - Insert → revision 0.1, incarnation 0
   - Delete → incarnation 1
   - Re-insert (тот же ID) → incarnation 1, revision 0.1 (новая генерация)
   - If-Match со старым incarnation → 412
3. Concurrency тест: два concurrent delete → оба успешны, incarnation +2
4. Admission: сверка fingerprint перед операцией (все 3 провайдера)

## Критерий закрытия
- Тесты lifecycle PASS на SQLite
- Fingerprint реализован для всех 3 провайдеров
