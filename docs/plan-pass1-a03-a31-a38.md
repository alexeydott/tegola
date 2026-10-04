# План закрытия: Pass 1 — A03, A31, A38

## A03: If-Match внутри native transaction (P0)

### Проблема
HTTP `checkPrecondition` читает объект ДО `Coordinator.ExecuteAll`.
Результат проверки отбрасывается; `Mutation.IfRevision` не заполняется.
UPDATE/DELETE фильтруют только по PK — race window между проверкой и записью.

### План реализации
1. **Пробросить предусловие**: `Mutation.IfRevision` уже существует — заполнить
   его в HTTP-адаптерах (`server/mutation_build.go`, `ogc/wfs/execute.go`)
   из `If-Match` заголовка.
2. **Native CAS в SQL**: в `UPDATE`/`DELETE` добавить `AND revision = ?`
   (или `AND revision_hash = ?` для hash-fallback).
   - MySQL: `UPDATE ... SET ... WHERE id=? AND revision=?`
   - PostGIS: `UPDATE ... SET ... WHERE id=$1 AND revision=$2`
   - GPKG: `UPDATE ... SET ... WHERE id=? AND revision=?`
3. **Различить исходы**: `RowsAffected==0` после CAS-failure →
   проверить существование строки:
   - существует, но revision mismatch → `MutationErrPreconditionFailed` (412)
   - не существует → `MutationErrNotFound` (404)
4. **Тесты**:
   - `TestCASConcurrentUpdate`: два клиента читают rev=5, оба пишут;
     второй получает 412.
   - `TestCASRevisionMismatch`: If-Match: "3" при текущей 5 → 412.
   - `TestCASNotFound`: If-Match на несуществующий ID → 404, не 412.
   - `TestCASHashFallback`: без revision-таблицы, ETag=hash → CAS по hash.

### Файлы
- `provider/mutation.go` (документировать IfRevision contract)
- `provider/mysql/mutation_ops.go`, `provider/postgis/mutation_ops.go`,
  `provider/gpkg/mutation_ops.go` (CAS predicate)
- `server/mutation_build.go`, `ogc/wfs/execute.go` (заполнять IfRevision)
- `server/handle_features_mutations_test.go` (новые тесты, НЕ трогая старые)

---

## A31: FID injectivity (P1)

### Проблема
`sanitizeNCName` заменяет неподходящие символы на `_`:
`a:b` и `a_b` → одинаковая строка. Коллизия.
Несколько парсеров игнорируют collection из `DecodeWFSFID`.

### План реализации
1. **Обратимое кодирование**: заменить `sanitizeNCName` на percent-encoding
   для NCName-недопустимых символов:
   - `a:b` → `a%3Ab`, `a_b` → `a_b` (различимы)
   - Декодер обращает `%XX` → исходный символ
2. **Проверка коллекции везде**: каждый ID selector проверяет,
   что collection из FID совпадает с запрашиваемой коллекцией.
   - `ogc/wfs/execute.go`: Update/Delete/Replace фильтры
   - `server/mutation_build.go`: REST пути
3. **ID 0 vs отсутствующий**: ввести `FeatureIDSet bool` или использовать
   `*uint64` в местах, где 0 — валидный ID.
4. **Тесты**:
   - `TestFIDInjective`: `a:b` и `a_b` дают разные FID; round-trip точен.
   - `TestFIDCollectionMismatch`: FID из `sites` для `roads` → 400.
   - `TestFIDZeroVsAbsent`: ID 0 обрабатывается отдельно от отсутствующего.

### Файлы
- `feature/fid.go` (новое кодирование)
- `ogc/wfs/execute.go`, `server/mutation_build.go` (проверки)
- Новые тесты в `feature/fid_test.go`

---

## A38: Incarnation (P1)

### Проблема
`PhysicalFeatureKey` включает Domain/Relation/PK без source/entity incarnation.
Нет защиты от внешних writers, пересоздания таблицы, restore из backup.

### План реализации
1. **Source incarnation**: при старте провайдера вычислить fingerprint
   схемы (хеш от table def + PK + columns). Хранить в `writeMapping`.
   - При изменении схемы между admission и tx → ошибка
     `MutationErrSchemaChanged`.
2. **Entity incarnation**: добавить колонку `tegola_incarnation` (или использовать
   существующую revision как incarnation при DELETE+recreate).
   - DELETE пишет tombstone с incarnation+1.
   - INSERT после DELETE получает новую incarnation.
3. **Связь с CAS**: `IfRevision` проверяет пару (revision, incarnation).
4. **Внешние writers**: задокументировать режимы:
   - `exclusive`: только tegola пишет (default)
   - `external`: внешний writer разрешён, tegola проверяет incarnation
     перед каждой операцией и отказывает при mismatch
5. **Тесты**:
   - `TestIncarnationSchemaChange`: ALTER TABLE между admission и tx → ошибка.
   - `TestIncarnationDeleteRecreate`: DELETE+INSERT → новая incarnation,
     старый ETag невалиден.
   - `TestIncarnationExternalWrite`: симуляция внешнего UPDATE →
     следующий tegola-write с If-Match → 412.

### Файлы
- `provider/mutation.go` (PhysicalFeatureKey + incarnation)
- `provider/postgis/mutation.go`, `provider/mysql/mutation.go`,
  `provider/gpkg/mutation.go` (schema fingerprint)
- `provider/audit/migrate.go` (incarnation column)
- Новые тесты
