# A30: Полный typed GetFeature stream — план

## Проблема
WFS GetFeature получает `features.Feature` с GeoJSON-геометрией, снова парсит JSON,
строит GML. Все свойства — `fmt.Sprintf` из `map[string]string`. Нет typed properties,
нет schema snapshot, нет единого конвейера для JSON и GML.

## Объём
- `provider.Feature`: добавить `TagsTyped map[string]feature.TypedValue` (рядом с legacy `Tags`)
- Провайдеры (postgis, mysql, gpkg): заполнять типизированные значения из SQL-типов
  (integer → int64, float → float64, bool → bool, остальное → string)
- `feature.FeatureRecord`: уже имеет typed Properties — использовать как единый record
- WFS GML encoder: читать `TypedValue` напрямую, форматировать по типу
  (int → `%d`, float → `%g` с нужной точностью, bool → `true/false`, string → XML-escape)
- GeoJSON encoder: использовать те же typed values
- Schema snapshot: `feature.SchemaDescriptor` передаётся в encoder один раз

## Шаги
1. Добавить `TagsTyped` в `provider.Feature` (обратная совместимость: `Tags` остаётся)
2. Реализовать `typedValueFromSQL(driver.Value, colType)` helper
3. Обновить 3 провайдера для заполнения `TagsTyped`
4. Обновить WFS GML encoder: `encodeTypedValue(TypedValue) string`
5. Обновить GeoJSON encoder аналогично
6. Тесты: parity JSON vs GML (одинаковые значения), precision (float64 без потерь),
   типы (int не становится "1.0")
7. Проверка: нет PASS→FAIL в существующих тестах

## Критерий закрытия
- Ни одного `fmt.Sprintf("%v")` для свойств в GML/GeoJSON encoders
- Тест: feature с int/float/bool/string проходит через оба encoder'а
  с сохранением типов и точности
