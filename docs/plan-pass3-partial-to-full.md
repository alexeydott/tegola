# План закрытия: Pass 3 — частичные R-пункты → полное закрытие

## A01: MySQL DDL из транзакции (P0) — R09 частично
**Осталось**: migration/version/engine admission, fresh/upgrade rollback.
**План**:
1. Версионированная миграция `tegola_schema_version` уже есть (R09).
   Добавить: `migrate up/down`, проверку engine=InnoDB при старте,
   отказ при version mismatch (не silent create).
2. `BeginFeatureTx` НЕ делает DDL вообще — только проверяет версию.
   Отсутствующая таблица → `MutationErrSchemaNotMigrated` (не auto-create).
3. Тесты: fresh install, upgrade v1→v2, downgrade, wrong engine.

## A02: Физический binding (P0) — R08 частично
**Осталось**: первый writer разрешает имена всех слоёв; security context.
**План**:
1. `WriteBinding` struct: `{Domain, Schema, Table, PKColumn, GeomColumn,
   SchemaFingerprint, Principal}` — immutable после admission.
2. `ExecuteAll` проверяет binding каждого mutation против admission.
3. Тесты: два провайдера на одну БД → разные bindings; смена principal → отказ.

## A04: Авторизация (P0) — R02 частично
**Осталось**: row/field/post-image policy.
**План**:
1. `Policy` interface: `CheckRow(collection, id, principal)`,
   `CheckField(collection, field, principal)`, `CheckPostImage(...)`.
2. Production: deny-by-default; dev: allow-all (явный `auth_mode: dev`).
3. Тесты: матрица principal × operation × field.

## A05: PUT semantics (P0) — R10 частично (только документировано)
**Осталось**: complete replacement, NULL/default/system fields.
**План**:
1. PUT = полная замена: отсутствующие nullable → NULL,
   отсутствующие с default → default, system/generated → игнорировать + warning.
2. `Replace` в провайдерах уже UPDATE (не DELETE+INSERT).
3. Тесты: PUT с отсутствующими полями → проверка итогового состояния.

## A06: GML парсер (P0) — R05 частично
**Осталось**: inherited XYZ, полная структура, WFS→DB e2e.
**План**:
1. GML corpus тесты с `want` (сейчас только наличие ошибки проверяется).
2. srsDimension=3 → явный отказ (уже есть, покрыть тестами).
3. E2E: GML → Parse → WKB → INSERT → SELECT → сравнить.

## A09: Lock обходы (P0) — R06 частично
**Осталось**: физические aliases, atomic guard.
**План**:
1. Lock key = physical (schema.table.id), не protocol typeName.
2. `Check` + `DML` в одной транзакции (SELECT FOR UPDATE на lock row).
3. Тесты: PUT с lock на alias → отказ; конкурентный захват.

## A10: Persistent locks (P0) — R06 частично
**Осталось**: default memory, fail-open errors, owner.
**План**:
1. Config: `wfs.lock_store: memory|postgres|mysql`.
2. Все SQL ошибки → fail-closed (уже частично, довести).
3. Owner: `principal` в lease; чужой lock → 409.
4. Тесты: restart → lease persists; permission denied → fail-closed.

## A11: Locking lifecycle (P0) — R07 частично
**Осталось**: полный lifecycle, versioned parsing.
**План**:
1. `ReleaseAction` (ALL/SOME), `GetFeatureWithLock`.
2. Renewal: `LockFeature` с существующим lockId продлевает.
3. Namespace-aware парсинг для 1.1/2.0.
4. Тесты: полный цикл acquire → use → release.

## A14: MySQL geometry (P0) — R03 частично
**Осталось**: WKT как SQL, %q/ANSI_QUOTES.
**План**:
1. Аудит всех SQL construction в mysql провайдере на `%q`/конкатенацию.
2. Все значения через bind parameters.
3. Тесты: ANSI_QUOTES mode, спецсимволы в WKT.

## A15: Affected count (P0) — R04 частично
**Осталось**: фиктивный успех, no-op Replace=404.
**План**:
1. `existsInTx` уже есть. Довести: no-op UPDATE (те же значения) → success,
   не 404. MySQL `CLIENT_FOUND_ROWS` задокументировать.
2. Тесты: UPDATE без изменений → 200; DELETE несуществующего → 404.

## A19: GML validation (P1) — R05 частично
**Осталось**: cardinality/closure/dimension admission.
**План**:
1. Polygon: минимум 4 точки, замкнутость; LineString: минимум 2 точки.
2. Уже отклоняется srsDimension!=2 — покрыть тестами все пути.
3. Тесты: незамкнутый ring → 400; 1 точка в LineString → 400.

## A26: FES фильтры (P0) — R11 частично
**Осталось**: FE/FES не реализован.
**План** (честная граница):
1. Поддержать `PropertyIsEqualTo` + `And` (наиболее частый кейс).
2. Остальное (`Or`/`Not`/`BBOX`/spatial) → явный 400
   `UnsupportedFilter`, не silent ignore.
3. Тесты: EqualTo работает; BBOX → 400 с понятным сообщением.

## A34: Audit/outbox (P0) — R12 частично
**Осталось**: before/after ревизии, payload, delivery.
**План**:
1. Audit пишет `old_revision`/`new_revision` корректно (R12 частично сделал).
2. Outbox payload: `{collection, feature_id, op, revision, tx_id}`
   вместо `{}`.
3. In-memory dispatcher: claim → deliver → ack (для тестов).
4. Тесты: payload содержит все поля; dispatcher доставляет.
