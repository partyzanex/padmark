# TODO.md — полный перенос находок из REVIEW.md и PRODUCT.md

> Каждая находка из обоих документов — отдельный пункт ниже, в том же порядке, что и в
> источнике. Крупные находки разбиты на под-пункты (что именно сделать). Статус ревью
> (CONFIRMED / REFUTED / PARTIAL) сохранён как есть — REFUTED-пункты оставлены для полноты
> картины, но помечены как не требующие действий.

---

## Часть 1. Находки из REVIEW.md (11 пунктов ревью)

### 1. Анонимные мутации при `--enable-accounts` — REFUTED, действий не требует
- [ ] Ревью утверждало: POST /notes, PUT/DELETE /notes/{id} с корректным edit_code,
      GET /edit/{id} должны отдавать 401 ещё в middleware при включённых аккаунтах
- [x] Проверено по коду `internal/adapters/http/middleware_auth.go:191-225` (`isPublicRoute`) —
      это осознанное поведение, зафиксированное комментарием в коде: анонимные мутации через
      edit_code разрешены намеренно («Restoring the documented edit_code workflow»)
- [ ] Действие: не требуется. Если поведение всё же должно измениться — это отдельное
      продуктовое решение, не баг

### 2. TOTP-ключ через HKDF-SHA256 вместо argon2id — DONE
- [x] Файл: `internal/infra/crypto/password.go:65-73`, функция `DeriveUserKey`
- [x] Проблема: `hkdf.New(sha256.New, password, kdfSalt, ...)` — пароль используется напрямую
      как входной материал для HKDF; пароли пользователей для входа хешируются argon2id, но
      ключ шифрования TOTP-секрета — нет
- [x] Риск: при утечке `users` таблицы — оффлайн-брутфорс пароля на скорости SHA-256 →
      расшифровка TOTP-секрета
- [x] Действие: `DeriveUserKey` переведён на `argon2.IDKey` (коммит `1bc2c01`)
- [x] Продумать миграцию уже зашифрованных TOTP-секретов существующих пользователей:
      `crypto.Sealer` (`internal/infra/crypto/sealer.go`) вводит версионированный конверт
      `v1$<memory>$<time>$<threads>$<base64 ciphertext>`. `Open` читает cost из самого конверта,
      поэтому смена `--argon2-memory`/`-time`/`-threads` больше не делает секреты нечитаемыми
      (раньше это залочило бы всех пользователей разом). Два legacy-формата без версии —
      argon2id и HKDF-SHA256 — открываются и помечаются `stale`; успешный вход перезаписывает
      секрет в текущем формате (`SessionManager.resealTOTPSecret` → `UserStore.UpdateTOTPSecret`,
      fail-soft: сбой записи не ломает вход)
- [x] Добавить тест на корректность миграции/совместимости со старыми данными:
      `internal/infra/crypto/sealer_test.go` (round-trip, смена cost, оба legacy-формата,
      битые конверты), `internal/usecases/auth/manager_test.go` (миграция на входе, отсутствие
      лишней записи, fail-soft, миграция только после проверки TOTP),
      `tests/auth/flow_test.go` (end-to-end на реальном репозитории)

### 3. CSRF-секрет не конфигурируется, рандом на процесс — DONE
- [x] Файл: `internal/infra/server/server.go:76-87` (сборка `RouterOptions`),
      `internal/adapters/http/router.go:171-179` (генерация случайного секрета)
- [x] `CSRFSecret` не заполняется нигде — не пробрасывается из флагов, `flags.go` вообще не
      содержит ссылок на CSRF/secret
- [x] Следствие: рестарт процесса → все формы 403 до следующей загрузки; больше одной реплики
      сервера → формы CSRF-403 всегда. Подтверждено тестом
      `TestCSRF_NoSecret_TokenRejectedByOtherInstance`
- [x] Действие: флаг `--csrf-secret` + env `PADMARK_CSRF_SECRET` (`flags.go:38,70,274`),
      `parseCSRFSecret` требует минимум 32 байта — короткое значение это ошибка старта, а не
      слабый HMAC-ключ
- [x] Прокинуть значение в `RouterOptions.CSRFSecret` в `server.go` (`buildRouter`)
- [x] Решить поведение при отсутствии секрета: выбран задокументированный fallback (рандом на
      процесс) + `WARN` в логах при старте. Fail-hard отвергнут: одиночный инстанс должен
      работать из коробки без управления секретами, а цена fallback — только необходимость
      перезагрузить открытую форму после рестарта
- [x] Обновить README с описанием флага: строка в таблице Server + абзац под таблицей;
      `.env.example` — блок `PADMARK_CSRF_SECRET`
- [x] Тест на переживание рестарта / согласованность между инстансами:
      `internal/infra/server/csrf_test.go` (юнит на `parseCSRFSecret`) +
      `internal/adapters/http/web_auth_test.go` —
      `TestCSRF_SharedSecret_TokenSurvivesInstanceChange` (токен, выданный одним роутером,
      принимается другим с тем же секретом) и `TestCSRF_NoSecret_TokenRejectedByOtherInstance`
      (без секрета — 403 у соседнего инстанса, свой токен свой инстанс принимает)

### 4. `--disable-api` якобы внутри auth-мидлвари — REFUTED, действий не требует
- [ ] Ревью утверждало: `--disable-api` стоит внутри auth-мидлвари, то есть срабатывает после
      проверки авторизации
- [x] Проверено по коду `router.go:198-216` — порядок сборки стека: `withAPIDisabled` оборачивает
      `mux` раньше `newAuthMiddleware`, то есть в терминах выполнения запроса DisableAPI
      срабатывает **до** auth (Go-стек middleware строится снаружи-внутрь)
- [x] Комментарий в коде (`router.go:102-104`, «before auth or any handler runs») соответствует
      реальному порядку
- [ ] Действие: не требуется. Утверждение ревью о «врут в 4 местах» — ложно, проверять не нужно

### 5. clientIP берёт первый элемент X-Forwarded-For — DONE (исходный диагноз неверен)
- [x] Файл: `internal/adapters/http/middleware_ratelimit.go` (`clientIP`, `clientIPFromForwarded`,
      `isTrustedProxy`)
- [x] ~~Код берёт первый элемент через `strings.Cut(forwarded, ",")`~~ — **неверно**:
      `strings.Cut` в коде нет, `clientIPFromForwarded` идёт по списку справа налево
      (`slices.Backward`), пропускает записи доверенных прокси и обрывает обход на невалидной
      записи. Покрыто `TestClientIP_TrustedProxy_SpoofedLeftmostXFFIgnored` и
      `..._PlantedTrustedEntryDoesNotUnmaskAttackerValue`
- [x] ~~При слишком широком CIDR возможен XFF-спуфинг~~ — **неверно**: при `0.0.0.0/0` все
      записи XFF считаются прокси-хопами, обход не находит клиентскую и откатывается на
      `RemoteAddr` (`TestClientIP_WideOpenProxies_XForwardedForStillFallsBackToRemoteAddr`)
- [x] **Реальный вектор — `X-Real-IP`**: у него нет цепочки, значение берётся как есть, как
      только `RemoteAddr` признан доверенным. При широком CIDR доверенный — любой клиент, и он
      выбирает себе ведро сам. Подтверждено `TestClientIP_WideOpenProxies_XRealIPIsSpoofable` и
      end-to-end `TestWithTOTPRateLimit_WideOpenProxies_SpoofedXRealIPDefeatsLimit` (30 попыток
      TOTP-логина подряд, лимит 10/мин не срабатывает)
- [x] Верно: XFF/X-Real-IP читаются только после `isTrustedProxy`; пустой `--trusted-proxies` →
      заголовки игнорируются; TOTP-лимит идёт через тот же `clientIP`
- [x] Не замечено в ревью — обратная misconfiguration: прокси есть, флаг пуст → все клиенты в
      одном ведре, один шумный клиент выбивает TOTP-логин остальным
      (`TestWithTOTPRateLimit_ProxyPresentButUntrusted_ClientsShareOneBucket`)
- [x] Действие: warning при незаданном `--trusted-proxies` — реализован не по адресу
      прослушивания (прямое выставление наружу легитимно, был бы постоянный ложный сигнал), а по
      факту прихода forwarding-заголовка: `warnUnconfiguredProxy` пишет один раз за процесс
      (`sync.Once`), тесты `TestWarnUnconfiguredProxy_*`, включая guard на тишину без заголовка
- [x] Действие: README — раздел «Getting `--trusted-proxies` wrong» с обоими направлениями
      ошибки; строка флага в таблице; `.env.example` дополнен причиной запрета `0.0.0.0/0`
- [x] Санити-проверка широкого CIDR: `overlyBroadProxyCIDRs` (`internal/infra/server/db.go`),
      WARN при старте. Порог — префикс короче /8 для IPv4 и /7 для IPv6, т.е. короче самых
      широких реально используемых диапазонов (`10.0.0.0/8`, `fc00::/7`); тест
      `TestOverlyBroadProxyCIDRs` отдельно фиксирует отсутствие ложных срабатываний на приватных
      диапазонах и CDN-префиксах

### 6. TOCTOU cross-table slug между notes и links — CONFIRMED
- [ ] Файл: `internal/usecases/notes/manager.go:442-458` (`resolveUniqueSlug`) и
      `internal/usecases/links/manager.go:509`
- [ ] `notes.Manager.resolveUniqueSlug` проверяет занятость slug только через `links.Exists`;
      занятость в самой таблице `notes` полагается только на PK constraint внутри
      `storage.Create`
- [ ] Риск: параллельные `POST /notes` с одинаковым custom slug (при `--custom-slugs=true`)
      оба проходят проверку `links.Exists`, и падают друг на друга уже на уровне БД — не
      контролируемая доменная ошибка, а голая race
- [ ] Действие: добавить симметричную проверку `notes.Exists` в `resolveUniqueSlug` рядом с
      уже существующей `links.Exists`
- [ ] Действие: рассмотреть storage-level advisory lock или retry-on-conflict на уникальном
      индексе как финальный уровень защиты (проверка на уровне usecase сама по себе race не
      устраняет)
- [ ] Тест: конкурентные `POST /notes` с одинаковым custom slug — ровно один успех, остальные —
      корректная доменная ошибка (не паника, не голый 500 от constraint violation)

### 7. GET /links/{id} не отдаёт 410 для expired — CONFIRMED
- [ ] Файл: `internal/adapters/http/ogen_handler.go:181-196` (`GetLink`)
- [ ] `links.Manager.Peek` контрактно возвращает expired-ссылки как есть (документированное
      поведение самого `Peek`), но `GetLink` не проверяет `link.ExpiresAt` перед ответом и
      всегда отдаёт 200
- [ ] Расхождение: браузерный редирект `/r/{id}` (`link_handler.go`) делает эту проверку и
      корректно отдаёт 410 — API и браузерный путь ведут себя по-разному для одной и той же
      ссылки
- [ ] Действие: добавить проверку `ExpiresAt` в `GetLink` перед формированием ответа, вернуть
      410 при истёкшей ссылке
- [ ] Тест: `GET /links/{id}` для заведомо истёкшей ссылки → 410, тело ответа соответствует
      контракту ошибок

### 8. Тело ошибок ogen-декодирования не соответствует ErrorResponse — CONFIRMED
- [ ] Файл: `internal/adapters/http/ogen_errors.go:29-48` (`newMaxBodyErrorHandler`) и
      vendor `ogenerrors/handler.go:48`
- [ ] `newMaxBodyErrorHandler` корректно оборачивает 413 (`MaxBytesError`) в
      `ErrorResponse{Message: ...}`, но для остальных decode-ошибок (битый JSON, missing
      required field, неверный тип) делегирует в `ogenerrors.DefaultErrorHandler`, который
      пишет поле `error_message` — не совпадает с задокументированным `ErrorResponse{message}`
- [ ] Следствие: клиент, ожидающий контрактный `{"message": ...}`, на decode-ошибках получит
      `{"error_message": ...}`
- [ ] Действие: написать собственный `ErrorHandler`, оборачивающий все ogen decode-ошибки в
      `ErrorResponse{Message: ...}`, вместо делегирования в `DefaultErrorHandler`
- [ ] Тест: отправить битый JSON / отсутствующее обязательное поле / неверный тип на
      произвольный ogen-эндпоинт, проверить формат тела ответа

### 9. 429/500 отдаются через http.Error (text/plain) — CONFIRMED
- [ ] Файл: `internal/adapters/http/middleware_faillock.go:40` —
      `http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)`
- [ ] Файл: `internal/adapters/http/middleware_ratelimit.go:57,64,70` — три вызова `http.Error`
      для 500 (internal server error, дважды) и 429 (too many requests)
- [ ] `http.Error` пишет `Content-Type: text/plain; charset=utf-8` + newline, спецификация
      обещает JSON `{"message"}` — несоответствие контракту
- [ ] Дополнительно: 429 не объявлен в `openapi.yaml` для `PUT`/`DELETE /notes`
- [ ] Действие: заменить все вызовы `http.Error` в этих двух файлах на общий JSON-хелпер
      (переиспользовать существующий `writeError()`, если применимо)
- [ ] Действие: добавить 429 в `openapi.yaml` для `PUT`/`DELETE /notes/{id}`
- [ ] Тест: `Content-Type: application/json` и тело `{"message": ...}` для 429 (rate limit,
      faillock) и 500 ответов

### 10. SQLite RunInTx и SQLITE_BUSY_SNAPSHOT — PARTIAL
- [ ] Файл: `internal/infra/storage/sqlite/db.go:46-74` (`withSQLitePragmas`)
- [ ] Ревью утверждало «невозвратный SQLITE_BUSY_SNAPSHOT → 500» — преувеличение:
      `busy_timeout=5000` уже стоит (5 секунд ожидания лока, не «fail immediately»), WAL-режим
      снижает конкуренцию reader/writer
- [ ] Остаточный риск: deferred BEGIN внутри bun-транзакции при высокой конкурентности
      писателей теоретически всё ещё может давать `BUSY_SNAPSHOT` в узком race-window (не
      «невозвратный сбой», а редкий кейс)
- [ ] Действие (hardening, не блокер): оценить и, при необходимости, добавить
      `_txlock=immediate` к DSN
- [ ] Если добавляется — прогнать конкурентный/нагрузочный тест с несколькими параллельными
      writer'ами до и после, чтобы подтвердить эффект

### 11. vendor не закоммичен, .dockerignore отсутствует — CONFIRMED
- [ ] `git ls-files vendor/` → 0 файлов
- [ ] `.gitignore:3` содержит `vendor/`
- [ ] `CLAUDE.md` утверждает «vendor is committed (`go mod vendor`)» — расхождение с реальным
      состоянием репозитория
- [ ] `.dockerignore` отсутствует в корне репозитория — Docker build кладёт `.env`,
      `padmark.db`, `.git`, `bin/` в слой образа
- [ ] Действие: решить — либо (а) `git add -f vendor/`, убрать `vendor/` из `.gitignore`,
      привести репозиторий в соответствие с `CLAUDE.md`; либо (б) исправить `CLAUDE.md`, явно
      описав, что vendor не коммитится и сборка требует доступа к go-proxy
  - [ ] Если выбран вариант (а): прогнать `go mod vendor`, проверить `go build ./...` офлайн
        после коммита vendor
- [ ] Действие: создать `.dockerignore`, исключить как минимум `.env`, `*.db`, `.git`, `bin/`,
      `*.log`, тестовые артефакты
  - [ ] Проверить `docker build .` — итоговый образ не должен содержать секретов и локальных БД

---

## Часть 2. Находки из REVIEW.md — что ревью НЕ проверялось до конца

- [ ] Проверить UI-часть находки №1 — утверждение «`/edit/{id}` возвращает 401 для чужих
      private-заметок» не воспроизводилось живым запросом через UI, только чтением middleware
- [ ] Перепроверить q-values в `negotiate.go` — корректность парсинга `Accept`-заголовка при
      content negotiation
- [ ] Проверить расхождение build-тегов между `tests/auth` и `tests/manager`
- [ ] Проверить `IdleTimeout` — одинаково ли задан на всех трёх HTTP-серверах в проекте

---

## Часть 3. Находки из PRODUCT.md (8 пунктов — «чего padmark пока не даёт»)

### 1. Агентного surface нет, хотя «для AI agents» заявлено в README
- [ ] В коде ноль интеграций для агентов: нет MCP-сервера, нет tool definitions, нет SDK на
      Python/JS/Go, нет `examples/`
- [ ] Всё, что доступно агенту — то же, что доступно `curl` (голый REST)
- [ ] Позиционирование в README расходится с реальностью: «built for agents» обещает готовый
      contract, а по факту — обычный API без обёртки под агентов
- [ ] Действие: реализовать MCP-сервер (`padmark-mcp`) как отдельный бинарник/модуль, либо как
      минимум добавить `/llms.txt` с описанием API для агентов
- [ ] Действие: привести формулировки README в соответствие с реальным состоянием, если
      технический surface не будет закрыт в ближайшее время

### 2. Custom slugs выключены по умолчанию — архитектурный выбор, не лень
- [ ] Файл: `internal/infra/crypto/crypto.go:99-100` — `[]byte(slug)` используется как IKM для
      HKDF-SHA256 → AES-256-GCM ключ шифрования контента
- [ ] Следствие: пользователь, выбирающий свой slug, тем самым выбирает и энтропию ключа
      шифрования — низкоэнтропийный slug (`/notes/oncall-runbook`) = угадываемый ключ =
      расшифровка контента при угадывании slug
- [ ] Поэтому `--custom-slugs` выключен по умолчанию с явным предупреждением в README — это
      обосновано, но стоит продукту человекочитаемых URL
- [ ] Действие: спроектировать схему с отдельным content-encryption key (256 бит random) на
      заметку/ссылку, хранящимся зашифрованным мастер-ключом инсталляции (envelope encryption)
- [ ] Действие: slug при этом становится чистым URL-идентификатором, не участвует в деривации
      ключа
- [ ] Действие: продумать миграцию уже созданных записей на новую схему шифрования
- [ ] После внедрения — рассмотреть включение `--custom-slugs` по умолчанию, обновить README

### 3. Нет list/search — slug единственный ключ доступа к заметке
- [ ] После создания заметки единственный способ её найти — помнить slug из ответа на создание
- [ ] `openapi.yaml` не объявляет `GET /notes`
- [ ] `notes.Manager` не имеет метода `List`/`Search`
- [ ] В БД нет FTS-индекса
- [ ] Продуктовое следствие: после ~50 заметок инсталляция превращается в неструктурированную
      свалку — убивает retention (человек забывает, что там вообще есть, и перестаёт
      пользоваться)
- [ ] Действие: добавить метод `List`/`Search` в `notes.Manager` (usecases-слой, интерфейс
      определяется рядом с потребителем)
- [ ] Действие: FTS5-индекс по title+content в SQLite-хранилище (bun уже в стеке), аналог для
      Postgres
- [ ] Действие: фильтры по privacy, created_at
- [ ] Действие: эндпоинт `GET /notes` в `openapi.yaml`, доступен только владельцу
      (сессия/API-токен)

### 4. Нет hosted demo — try-before-you-buy funnel мёртв
- [ ] README, OpenAPI `servers: [http://localhost:8080]`, Docker-артефакты — всё указывает
      только на self-host
- [ ] Нет ни `padmark.dev`, ни playground, ни «попробовать сейчас»
- [ ] Сравнение: Gist открывается в браузере за 5 секунд, padmark требует ~20 минут установки
      и Docker (или `go run`) — это убивает acquisition
- [ ] Действие: развернуть `padmark.dev` (или аналог) с rate-limit и коротким TTL (например
      1 час) на все создаваемые в песочнице данные
- [ ] Это не блокирует остальную разработку — по продуктовому приоритету стоит ниже
      технического ядра (P0–P2 сначала)

### 5. Auth setup только через web UI
- [ ] `/setup` (создание первого админа с TOTP-сканированием) — только в браузере
- [ ] `/admin` (выдача API-токенов, инвайтов, revoke пользователей) — только в браузере
- [ ] Нет `padmark-cli setup`, нет `POST /api/tokens`, нет `POST /api/invites`
- [ ] Следствие: headless agent deployments упираются в обязательный ручной шаг через браузер,
      который нельзя завернуть в CI
- [ ] Действие: `padmark-cli setup` — создание первого администратора с выводом TOTP URI в
      stdout
- [ ] Действие: `POST /api/tokens` — выдача API-токенов без похода в `/admin`
- [ ] Действие: `POST /api/invites` — выдача инвайтов без похода в `/admin`

### 6. `edit_code` показывается один раз и невосстановим
- [ ] Для анонимных заметок (дефолт при `--enable-accounts=false`) `edit_code` — единственный
      способ редактировать/удалить заметку
- [ ] Показывается ровно один раз при создании, в БД хранится только argon2id-хеш
- [ ] Потерял код → потерял заметку безвозвратно
- [ ] Это нормальная pastebin-семантика (кто угодно с URL = хозяин), но плохо подходит для
      людей, использующих padmark как «записную книжку»
- [ ] Действие: явно зафиксировать как продуктовое решение (документировать в README как
      осознанный trade-off), либо — если решено менять — продумать альтернативный механизм
      восстановления доступа только для аутентифицированных владельцев (не для анонимных
      заметок, чтобы не сломать pastebin-модель)

### 7. `--disable-api` отвечает кириллицей «До свидания»
- [ ] Middleware отдаёт `503 {"message":"До свидания"}` на любой запрос к API при включённом
      флаге
- [ ] Интеграционные тесты не могут отличить «флаг выключен» от «сервер перегружен» без
      machine-readable `code`
- [ ] 503 не задокументирован в `openapi.yaml` ни на одном эндпоинте
- [ ] Неанглоязычный SDK-парсер увидит непонятную строку в логах/ответе
- [ ] Действие: добавить machine-readable `code` в тело ответа (например
      `"code": "api_disabled"`)
- [ ] Действие: задокументировать 503 в `openapi.yaml`
- [ ] Действие: решить — оставить текст на русском или заменить на нейтральный/английский

### 8. Нет `PUT /links/{id}` — линк immutable
- [ ] Короткая ссылка после создания неизменяема: можно только удалить и создать заново
- [ ] Это теряет slug, view-count и edit_code при любом изменении `target_url`/ttl/privacy
- [ ] Добавление `PUT /links/{id}` технически тривиально, но его нет
- [ ] Действие: добавить `PUT /links/{id}` с обновлением `target_url`/`ttl`/`privacy`/
      `direct_redirect`, по аналогии с `PUT /notes/{id}` (owner bypass edit_code, валидация)
- [ ] Действие: обновить `openapi.yaml` и HTTP-адаптер (`ogen_handler.go` + регенерация ogen)

---

## Часть 4. Из PRODUCT.md — «Куда это вести» (5 приоритетных направлений, по ROI)

- [ ] 1. Развязать slug и content-encryption key (дублирует и детализирует пункт «2» из Части 3)
- [ ] 2. `GET /notes` с FTS5 для владельца — поиск по title+content, фильтр по
      privacy/created_at (дублирует и детализирует пункт «3» из Части 3)
- [ ] 3. Hosted sandbox — `padmark.dev` с rate-limit и 1h TTL (дублирует пункт «4» из Части 3)
- [ ] 4. Реальный agent surface — `padmark-mcp` или `/llms.txt` (дублирует пункт «1» из Части 3)
- [ ] 5. Headless auth — `padmark-cli setup`, `POST /api/tokens`, `POST /api/invites`
      (дублирует пункт «5» из Части 3)

---

## Часть 5. Из PRODUCT.md — явно НЕ делать

- [ ] Не сворачивать Note и Link в единый Resource — разные lifecycle и payload
- [ ] Не добавлять новый syntax highlighting — уже работает (`goldmark-highlighting` + `chroma`)
- [ ] Не раздувать scope дальше — уже 22 server-флага, это предел

---

## Часть 6. Из PRODUCT.md — что уже хорошо и должно быть сохранено (не трогать при рефакторинге)

- [ ] Onion-архитектура в `internal/` (domain / usecases / adapters / infra), зависимости
      только внутрь — не нарушать при доработках выше
- [ ] OpenAPI spec в репозитории, ogen-генерируемые сервер и клиент как single source of truth
      для HTTP-контракта — сохранить эту практику при добавлении новых эндпоинтов (Части 2, 3)
- [ ] `sessionStorage`-only секреты, показываемые один раз с явным UI-warning — не менять
      подход при доработке `edit_code`/токенов
- [ ] `buildNamedRoutes()` = `domain.ReservedSlugPrefixes()` как single source of truth для
      first-segment route names — обязательно обновлять при любом новом первом-сегменте роуте
      (в том числе при добавлении `GET /notes` из Части 3, п.3)
- [ ] Owner-scoping строгий (никаких «admin может всё») — сохранить при доработке
      headless-auth (Часть 3, п.5) и `PUT /links/{id}` (Часть 3, п.8)
- [ ] `goldmark-highlighting` + `chroma` уже работают корректно — не трогать
