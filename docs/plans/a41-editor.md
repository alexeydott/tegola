# A41: Vue.js editor — план

## Проблема
ui/src содержит viewer (App.vue, Viewer.vue, MapControls), но нет:
schema forms, working copy, CRUD, ETag-aware editor.

## Шаги
1. `EditorPanel.vue`: боковая панель со списком коллекций (из /collections)
2. `SchemaForm.vue`: динамическая форма по queryables (тип → input)
   - integer → number, number → number step=any, boolean → checkbox,
     string → text, date → date
3. `WorkingCopy.js`: локальный Map<tempId, feature> для несохранённых правок
4. CRUD через OGC API Features Part 4:
   - POST /collections/{id}/items (create)
   - PUT /collections/{id}/items/{fid} (replace, If-Match)
   - PATCH (merge-patch или JSON-patch)
   - DELETE с If-Match
5. ETag-aware: хранить ETag из GET, отправлять If-Match, при 412 показать конфликт
6. Интеграция в App.vue: кнопка "Edit" переключает viewer/editor режим

## Критерий закрытия
- `npm run build` успешен
- Форма генерируется из реального queryables
- Create/update/delete работают против локального tegola
