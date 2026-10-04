# План закрытия: Pass 2 — A21, A30, A39, A40, A41, A42, A43

## A21: JSON Patch (RFC 6902) — полная реализация (P1)

### Проблема
Только Merge Patch поддерживается; JSON Patch возвращает 415.
W55 требует полной реализации.

### План реализации
1. **Парсер**: `server/jsonpatch.go`:
   - `Op` (add/remove/replace/move/copy/test), `Path` (JSON Pointer), `Value`
   - JSON Pointer unescaping: `~1` → `/`, `~0` → `~`
   - Валидация: path должен начинаться с `/`, op из enum
2. **Evaluator**: применить операции последовательно к `map[string]any`:
   - `add`: создать промежуточные объекты; `-` для append в массив
   - `remove`: удалить; ошибка если path не существует
   - `replace`: заменить; ошибка если path не существует
   - `move`: remove + add (атомарно в рамках patch)
   - `copy`: копировать значение
   - `test`: сравнить; несовпадение → 409 (не native CAS, но явная проверка)
3. **Атомарность**: весь patch применяется к копии; при любой ошибке
   исходный документ не меняется.
4. **Интеграция**:
   - `Content-Type: application/json-patch+json` → новый путь в `servePatchItem`
   - `Accept-Patch: application/merge-patch+json, application/json-patch+json`
   - Field ACL: запрещённые поля (id, geometry для некоторых ролей) → 403
5. **Тесты** (новый `server/jsonpatch_test.go`, старые не трогать):
   - Все 6 операций, вложенные path, массивы, escaping
   - Атомарность: ошибка в 3-й операции → документ неизменен
   - `test` op: совпадение → применить, несовпадение → 409

---

## A30: Typed feature stream (P1)

### Проблема
GetFeature: `features.Feature` (GeoJSON) → повторный парсинг JSON → GML.
Свойства через `fmt.Sprintf`. Двойная работа, потеря типов.

### План реализации
1. **TypedFeature struct** (`ogc/wfs/typed_feature.go`):
   ```go
   type TypedFeature struct {
       ID         uint64
       TypeName   string
       Geometry   geom.Geometry  // нативный, не JSON
       Properties map[string]TypedValue
   }
   type TypedValue struct {
       Value any           // int64, float64, string, bool, time.Time, nil
       Type  PropertyType  // из schema
   }
   ```
2. **Конвертер**: `features.Feature` → `TypedFeature` один раз,
   с типизацией по schema (не `fmt.Sprintf`).
3. **Encoders**: JSON и GML получают `TypedFeature`:
   - JSON: типы сохраняются (числа — числа, даты — ISO8601)
   - GML: форматирование по типу (decimal без потери точности)
4. **Адаптер**: старый REST read API работает через адаптер
   `TypedFeature` → `features.Feature` для обратной совместимости.
5. **Тесты**:
   - Типы сохраняются: int → int, float → float, date → ISO8601
   - NULL → отсутствует в GML, null в JSON
   - Decimal точность не теряется

---

## A39: Capability registry (P1)

### План реализации
1. **Registry** (`ogc/wfs/capabilities_registry.go`):
   ```go
   type Capability struct {
       Protocol    string   // "WFS 1.1.0", "WFS 2.0.2"
       Operation   string   // "GetFeature", "Transaction", ...
       Methods     []string // ["GET", "POST"]
       MediaTypes  []string
       InputCRS    []string
       OutputCRS   []string
       Implemented bool
       Tested      bool
   }
   ```
2. **Генерация**: Capabilities, OpenAPI, OPTIONS строятся из registry,
   а не из разрозненных строк.
3. **Тест**: registry покрывает все объявленные operations;
   `Implemented=false` → не объявляется.

---

## A40: Native test scaffolding (P1)

### План реализации
1. **Docker Compose** (`scripts/native-test/docker-compose.yml`):
   PostGIS, MySQL, MariaDB для локального прогона.
2. **Test helpers** (`provider/test/native.go`):
   - `RequirePostGIS(t)`, `RequireMySQL(t)` — skip если нет сервера
   - Fixture setup/teardown
3. **Конвертировать** существующие unit-тесты провайдеров
   в native-вариант где возможно.

---

## A41: UI editor — честная граница (P1)

### Анализ
Полноценный embedded редактор (выбор по ID, undo/redo, conflict
resolution, geometry tools) — это отдельный frontend-проект
на месяцы. В рамках backend-репозитория реализуемо:

### План реализации (backend-часть)
1. **Read-your-write**: `GET /collections/{id}/items/{fid}` возвращает
   ETag; клиент использует его для `If-Match`.
2. **Conflict response**: 412 включает `current` (текущий объект)
   для ручного merge на клиенте.
3. **Schema form**: `/collections/{id}/schema` уже есть (A17) —
   клиент строит форму из него.
4. **Документация**: `docs/wfs-client-guide.md` — как построить
   редактор на внешних клиентах (QGIS, OpenLayers) с примерами.

Полноценный embedded UI — отдельный репозиторий, не этот.

---

## A42: HANA provider matrix (P1)

### План реализации
1. **Explicit status** (`provider/hana/status.go`):
   ```go
   // Status: not implemented. Write admission denied.
   ```
2. **Admission gate**: HANA provider возвращает
   `MutationErrUnsupportedCapability` для всех write операций.
3. **Матрица** в `docs/wfs-scope-limitations.md`:
   provider × operation × status (implemented/tested/unsupported).

---

## A43: Operational acceptance (P1)

### План реализации
1. **Migration CLI** (`cmd/tegola/cmd/migrate.go`):
   `tegola migrate --config ...` — версионированные миграции,
   `tegola migrate --dry-run`, `tegola migrate --rollback`.
2. **Write-disable**: config flag `write.enabled=false` + rehearsal docs.
3. **Backup docs**: `docs/wfs-operations.md` — backup/restore,
   pool sizing, timeouts, audit retention.
4. **Health endpoint**: `/healthz/write` — проверяет revision таблицу,
   права, подключение.
