# A40: Native matrix и сильные WFS assertions — план

## Проблема
- WFS HTTP тесты проверяют подстроки вместо namespace/XSD
- CRUD тесты только на SQLite, не MySQL/PostGIS
- Нет CI evidence для audited SHA

## Шаги
1. Усилить WFS assertions:
   - Парсить XML ответ, проверять namespace (`http://www.opengis.net/wfs/2.0`)
   - Проверять `gml:id` формат (NCName)
   - Проверять структуру TransactionResponse (Inserted/Updated/Deleted)
2. Расширить `scripts/native-test.sh`:
   - Параметризовать DSN для MySQL/PostGIS/GPKG
   - Запускать CRUD матрицу на каждой доступной БД (skip если недоступна)
3. Добавить тест: WFS 2.0 LockFeature → Transaction с lockId → release

## Критерий закрытия
- Тесты парсят XML, а не ищут подстроки
- Скрипт принимает `TEST_MYSQL_DSN`, `TEST_PG_DSN` и отчитывается что пропущено
