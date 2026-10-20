# tenzo-run-backend

API сайта tenzo.run: принимает заявки из формы связи, ведёт ИИ-консультанта в чате на сайте и в Telegram-боте
@TenzoRun_bot и присылает владельцу каждое обращение в Telegram, откуда на него можно ответить.

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
| `POST` | `/api/v1/chat` | сообщение в чат: `session`, `text`, `locale`, `page`, `website`; ответ — поток `text/event-stream` |
| `GET` | `/api/v1/chat/{id}/updates?after=N` | ответы владельца, пришедшие после N-го: `messages`, `total`, `waiting` |
| `GET` | `/healthz` | процесс жив |
| `GET` | `/readyz` | база доступна |
| `GET` | `/docs`, `/openapi.json` | документация, если включена `http.docs` |

Поле `website` — ловушка для ботов: если оно заполнено, ответ такой же успешный, но заявка не сохраняется
и не отправляется. Число заявок с одного IP ограничено `limits.leads_per_ip`, сообщений в чат —
`limits.chat_per_ip`, сверх лимита — `429` с `Retry-After`.

## Чат и бот

Поток `POST /api/v1/chat` отдаёт события `session` (id диалога, его клиент хранит и присылает дальше),
`lead` (распознан контакт или имя), `delta` (кусок ответа), в конце `done` или `error`. Ошибки до начала потока —
обычный `problem+json`: `400`, `409` (предыдущее сообщение ещё обрабатывается), `422`, `429`, `502`, `503`
(модель не настроена).

На каждое сообщение два запроса к модели: потоковый ответ по системному промпту `internal/chat/prompt.go`
(факты о услугах, ценах и условиях, правила владельца из бота, контекст страницы) и параллельное извлечение
`save_lead`: контакт, имя, суть задачи, бюджет, срок и вопрос, который консультант пообещал уточнить. Контакт
(телефон, WhatsApp, Telegram, почта) дополнительно ищется в тексте сразу.

Основная модель — OpenAI-совместимый API (`ai.base_url`, `ai.api_key`, `ai.model`). Если задан
`ai.fallback.command` (путь к Claude Code CLI) и `ai.fallback.token` (`claude setup-token`), при сбое основной
модели ответы на `ai.fallback.cooldown` переходят на запасную.

Бот работает через long polling. Владельцы — `telegram.admins` (ники) и `telegram.chat_ids` (числовые id):
достаточно написать боту `/start` с нужного аккаунта. Владелец получает каждое сообщение из чата и бота
с кнопками, отвечает свайпом или кнопкой «Ответить» — ответ появляется у клиента на сайте (через `updates`)
или в боте. Обычным сообщением владелец просит помощника поменять правила консультанта; правило применяется
после кнопки «Применить». Остальные пользователи бота разговаривают с тем же ИИ-консультантом, что и на сайте.
Заявки из формы тоже приходят владельцам бота. Диалоги без контакта удаляются через `chat.retention_days`.

## Команды

| Команда | Что делает |
| --- | --- |
| `tenzo serve -config config.yaml` | HTTP API, чат, Telegram-бот и уведомления |
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

Telegram: с `telegram.bot_token` нужен хотя бы один владелец в `telegram.admins` или `telegram.chat_ids`; без
токена уведомления и бот выключены. Уведомление о заявке уходит из очереди и повторяется при сбое. Чат
включается, когда задан `ai.api_key` или `ai.fallback.command`.

За nginx на той же машине реальный IP берётся из `X-Forwarded-For` или `X-Real-IP` только от адресов из
`http.trusted_proxies` (по умолчанию `127.0.0.1/32`). Если прокси в другой сети, добавьте её туда, иначе все
посетители попадут под один лимит.

## Устройство

```
cmd/tenzo            точка входа и CLI
internal/app         сборка зависимостей, роутер, команды, e2e-тесты
internal/config      Config, Default(), Load(), валидация
internal/lead        заявки: domain, repository, service, handler, уведомление и тесты
internal/chat        ИИ-консультант: диалоги, промпт, извлечение контакта, SSE-поток, ответы владельца
internal/admin       Telegram-бот: владельцы, уведомления, ответы клиентам, помощник для правил
internal/rules       правила консультанта от владельца
internal/shared      http, postgres, ошибки, логи, лимиты, telegram, llm
migrations           SQL-миграции goose
```
