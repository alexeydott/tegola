# A43: Эксплуатационная приёмка — план

## Проблема
Нет связки durable receipt/incarnation/source epoch в data paths.
Нет процедур restore/fault/upgrade.

## Шаги (код + процедуры)
1. Код: `durable receipt` — после Commit вернуть receipt с:
   - txID, collection, featureID, revision_before, revision_after,
     incarnation, timestamp, actor
   - Сериализовать в audit log (уже есть таблица)
2. Процедуры (docs/operational.md):
   - Backup: как бэкапить tegola_revisions + tegola_audit + данные
   - Restore: порядок восстановления, проверка incarnation
   - Upgrade: запуск миграций (Migrate), проверка schema version
   - Fault: что делать при "commit unknown" (перечитать revision)
3. Тест: receipt содержит все поля после успешной транзакции

## Критерий закрытия
- `provider.CommitReceipt` расширен полями
- `docs/operational.md` с процедурами
- Тест проверяет receipt после commit
