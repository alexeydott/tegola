# A42: HANA provider matrix — план

## Проблема
HANA write path не проверен. Docker недоступен в sandbox,
но пользователь сообщает, что HANA есть в докере (в его окружении).

## Шаги
1. Проверить `provider/hana/mutation.go` существует ли
2. Написать HANA-specific CRUD тест (skip без DSN через `TEST_HANA_DSN`)
3. Документировать в `docs/plans/a42-hana.md` как запустить:
   `TEST_HANA_DSN="hdb://user:pass@host:30015" go test ./provider/hana/ -run TestWrite`
4. Если mutation.go отсутствует — реализовать базовый write path
   (по аналогии с postgis, с HANA-специфичным SQL)

## Критерий закрытия
- HANA write path либо протестирован, либо честно задокументирован
  как требующий `TEST_HANA_DSN`
