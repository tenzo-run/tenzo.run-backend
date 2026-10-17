# tenzo-run-backend

API сайта tenzo.run: принимает заявки на проект из формы связи, сохраняет их в PostgreSQL и присылает
владельцу сообщение в Telegram.

## Стек

Go 1.25, `net/http` + chi, huma v2 (OpenAPI 3.1, ошибки в `application/problem+json`), PostgreSQL 18, pgx,
goose (миграции встроены в бинарник), koanf, slog.

## Запуск локально

```bash
docker compose up -d postgres
cp config.example.yaml config.yaml
make run
```

API: `http://localhost:8170/api/v1`, документация: `http://localhost:8170/docs`, схема: `/openapi.json`.
Миграции применяются при старте (`database.migrate_on_start`). Весь стек в Docker: `make up`
(Postgres на порту 5472, API на 8170).

## Маршруты

| Метод | Путь | Что делает |
| --- | --- | --- |
| `POST` | `/api/v1/leads` | заявка с сайта: `name`, `contact`, `locale`, `page`, `message`, `website`; ответ `201 {"id": "<uuid>"}` |
| `GET` | `/healthz` | процесс жив |
| `GET` | `/readyz` | база доступна |
| `GET` | `/docs`, `/openapi.json` | документация, если включена `http.docs` |

Поле `website` — ловушка для ботов: если оно заполнено, ответ такой же успешный, но заявка не сохраняется
и не отправляется. Число заявок с одного IP ограничено `limits.leads_per_ip`, сверх лимита — `429`
с `Retry-After`.

## Команды

| Команда | Что делает |
| --- | --- |
| `tenzo serve -config config.yaml` | HTTP API и отправка уведомлений в Telegram |
| `tenzo migrate up\|down\|status` | миграции базы |
| `tenzo leads -limit 20` | последние заявки таблицей: время, имя, контакт, язык, страница |

`make test` — unit-тесты, `make test-db` — все тесты, включая интеграционные на реальной базе
`tenzo_test` (адрес в `TENZO_TEST_DATABASE_URL`), `make lint` — `go vet`, `make build-linux` — бинарник
для сервера в `bin/tenzo`.

Перед первым `make test-db` создайте базу: `docker compose exec postgres createdb -U tenzo tenzo_test`.

## Настройки

Все параметры собраны в `internal/config` и описаны в `config.example.yaml`. Любой параметр переопределяется
переменной окружения с префиксом `TENZO_`, вложенность через `__`: `TENZO_DATABASE__URL`,
`TENZO_TELEGRAM__BOT_TOKEN`, списки через запятую: `TENZO_HTTP__ALLOWED_ORIGINS=https://tenzo.run,https://www.tenzo.run`.
Конфигурация полностью валидируется при старте.

Telegram: `telegram.bot_token` и `telegram.chat_id` задаются вместе; если оба пусты, уведомления выключены.
Сообщение уходит из очереди после сохранения заявки и повторяется при сбое.

За nginx на той же машине реальный IP берётся из `X-Forwarded-For` или `X-Real-IP` только от адресов из
`http.trusted_proxies` (по умолчанию `127.0.0.1/32`). Если прокси в другой сети, добавьте её туда, иначе все
посетители попадут под один лимит.

## Устройство

```
cmd/tenzo            точка входа и CLI
internal/app         сборка зависимостей, роутер, команды, e2e-тесты
internal/config      Config, Default(), Load(), валидация
internal/lead        заявки: domain, repository, service, handler, уведомление и тесты
internal/shared      http, postgres, ошибки, логи, лимиты, telegram
migrations           SQL-миграции goose
```
