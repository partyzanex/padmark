# TODO.md — полный перенос находок из REVIEW.md и PRODUCT.md

> Находки из обоих документов — отдельные пункты ниже с исходной нумерацией;
> снятые с плана пункты удалены. Крупные находки разбиты на под-пункты. Статусы уточнены
> по текущему рабочему дереву, включая незакоммиченные изменения, 2026-10-13.
> `[x]` — выполненное действие или завершённая проверка; `[ ]` — оставшаяся работа.
> Описания проблем и постоянные ограничения не считаются невыполненными задачами.
> Старые замеры и записи о прогонах ниже сохранены как история; результаты текущей
> проверки перечислены отдельно.

## Состояние после проверки 2026-10-13

| Раздел | Сделано | Осталось |
|---|---|---|
| Часть 1, №1 | Проверен privacy-гейт; браузер чужого владельца получает 403 | Изменение API-401 на 403 — отдельное решение |
| Часть 1, №2, №3, №5, №7–12 | Исправления найдены в коде; Docker-пункт закрыт | Ограничение legacy Argon2 cost описано в №2 |
| Часть 1, №4 | Повторно проверен порядок middleware | Исправить порядок `--disable-api` и регрессионный тест; прежний REFUTED ошибочен |
| Часть 1, №6 | Общий slug-lock внутри одного процесса | Защита между репликами с общей БД |
| Часть 2 | Все четыре проверки выполнены; q-values, `IdleTimeout` и единые build-теги реализованы | — |
| Часть 3, №1 | CLI, Go-клиент, `/llms.txt` и MCP-сервер `padmark-mcp` реализованы | Ревью формулировок README («built for agents» теперь обосновано) |
| Часть 3, №6 | Потеря `edit_code` описана; owner bypass реализован | Восстановление анонимного кода не предусмотрено осознанно |
| Часть 3, №2 | Формат `pmc2`, реестр KEK, CLI управления ключами, миграция/конвертация/ротация; проверены SQLite/Postgres | Конвертация конкретных legacy-записей требует известных slug; смена default custom-slugs — отдельное решение |
| Часть 3, №5 | Headless auth реализован: `padmark-cli setup`/`create-token`/`create-invite`, JSON-ветка `POST /setup`, `POST /api/tokens` (self-service, cap 429) и `POST /api/invites` (admin), ogen+llms.txt+README | — |
| Часть 3, №7 | Стабильный `code: api_disabled`, нейтральное сообщение, OpenAPI 503 и обработка в клиентах | Порядок middleware — Часть 1, №4 |
| Часть 3, №8 | `PUT /links/{id}`: Update use case, SQLite/Postgres, owner bypass, TTL/privacy/direct_redirect; ogen и Go-клиент | — |
| Часть 3, №4; Часть 4 | Headless auth и шифрование закрыты (см. №5 и №2) | Demo |
| Части 5–6 | Существующие свойства проверены по коду | Соблюдать ограничения при дальнейших изменениях |

Проверка Docker в этой сессии: `docker build .` успешен; в `/src` стадии `builder`
нет `.env`, `padmark.db`, `.git`, `bin/`, `test.log`, `cover.out`. Во всех трёх слоях
итогового образа из файлов только `/etc/passwd` и `/usr/local/bin/padmark-server`.

Проверки при обновлении плана:

- `CGO_ENABLED=0 go test -tags=integration -count=1 ./...` — PASS, включая SQLite,
  PostgreSQL/testcontainers и сквозные auth-тесты.
- `go test -tags=integration -race -count=1 ./...` — полный прогон не завершён:
  пять пакетов (`linkmetrics`, `server`, `storage/postgres`, `tests/auth`, `tests/manager`)
  не слинковались из-за несовместимости macOS SDK/линкера (`tapi: unknown architecture
  arm64e.x1`). Замена `CC`/`CXX` на Apple clang не помогла. Остальные тестовые пакеты
  прошли с `-race`; полный race-прогон нельзя считать подтверждённым этой проверкой.
- Отдельно воспроизведены порядок DisableAPI/auth/rate-limit и игнорирование `Accept` q-values
  (Часть 1, №4 и Часть 2). Новых постоянных тестов и исправлений кода этот аудит не добавляет.
- `git diff --check` — PASS. Прежние заявления о полном lint/vet/race-прогоне ниже — история,
  а не результаты текущего обновления плана.

---

## Часть 1. Находки из REVIEW.md (11 пунктов ревью + дополнительный №12)

### 1. Анонимные мутации при `--enable-accounts` — REFUTED, действий не требует

- Ревью утверждало: POST /notes, PUT/DELETE /notes/{id} с корректным edit_code,
      GET /edit/{id} должны отдавать 401 ещё в middleware при включённых аккаунтах
- [x] Проверено по коду `internal/adapters/http/middleware_auth.go:191-225` (`isPublicRoute`) —
      это осознанное поведение, зафиксированное комментарием в коде: анонимные мутации через
      edit_code разрешены намеренно («Restoring the documented edit_code workflow»)
- [x] UI-часть перепроверена живым сквозным запросом, а не чтением middleware
      (`TestOwnerEditFlow_EditPage_PrivacyGate` в `tests/auth/`: реальный SQLite + миграции,
      боевые менеджеры, продовый роутер за `httptest.NewServer`; редиректы не следуются, чтобы
      видеть ответ самого гейта). Матрица privacy × вызывающий:

      | privacy | аноним (браузер) | аноним (API) | чужой (браузер) | чужой (API) | владелец |
      |---|---|---|---|---|---|
      | `owner` | 303 → /login | 401 | 403 | 401 | 200 |
      | `authenticated` | 303 → /login | 401 | 200 | 200 | 200 |
      | `public` | 200 | 200 | 200 | 200 | 200 |

      Плейнтекст заметки не утекает ни в одном отказном сочетании — проверяется поиском
      уникального маркера в теле ответа, а не только по коду статуса
- [x] **Формулировка «/edit/{id} возвращает 401» неверна ровно для UI-пути.** `/edit/{id}` — это
      HTML-страница: анонимному браузеру `handlePrivateAuth` отвечает **303 на `/login?next=…`**,
      а аутентифицированному чужому владельцу — **403**; 401
      возникает только при не-HTML `Accept`. Гейт решает по `negotiate(r)`, то есть по заголовку
      `Accept` вызывающего. Чтение middleware этого показать не могло: `/edit/{id}` там
      намеренно пропускается (`isPublicRoute`), решение принимается ниже, в `EditPage`.
      В тесте отдельно закреплён реальный браузерный `Accept` (полный список Chrome), а не
      только голый `text/html`
- Действие: не требуется. Если поведение всё же должно измениться — это отдельное
      продуктовое решение, не баг
- [x] Замечено попутно и **исправлено** (UX, не безопасность): уже залогиненный пользователь,
      открывший чужую `privacy=owner` заметку, получал редирект на `/login` — тупик, повторный
      вход вернул бы тот же редирект. Теперь отказ разводится по тому, есть ли у вызывающего
      credential вообще:

      | вызывающий | ответ |
      |---|---|
      | аноним, браузер | 303 → `/login?next=…` (вход действительно даёт доступ) |
      | залогинен, браузер | **403**, страница «Not yours» |
      | не-HTML клиент | 401 (без изменений) |

      Ветка вынесена в общий `common.denyPrivate` (`handler.go`) — раньше она была скопирована в
      `NoteHandler.handlePrivateAuth` и `LinkHandler.handlePrivateAuth`, чей комментарий обещал,
      что они «mirror exactly»; теперь это обеспечено кодом, а не дисциплиной. Ссылочный путь
      `GET /r/{id}` чинится тем же изменением
- [ ] Отдельное решение: определить, менять ли 401 на 403 для аутентифицированного чужака
      в не-HTML API. Сейчас сохранён документированный в `openapi.yaml` ответ 401;
      изменение затрагивает CLI-клиентов и не входит в выполненный UI-фикс.

### 2. TOTP-ключ через HKDF-SHA256 вместо argon2id — DONE

- [x] Файл: `internal/infra/crypto/password.go:65-73`, функция `DeriveUserKey`
- [x] Исходная проблема: `hkdf.New(sha256.New, password, kdfSalt, ...)` использовал пароль
      напрямую как входной материал для HKDF; пароли для входа хешировались argon2id,
      а ключ шифрования TOTP-секрета — нет. Текущая реализация исправлена ниже.
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
- Ограничение: unversioned legacy Argon2-секрет открывается только при прежнем cost;
  автоматического определения старых параметров нет. Это закреплено в
  `TestSealer_LegacyArgon2AtOtherCostFails`; перед миграцией таких записей нельзя менять cost.

### 3. CSRF-секрет не конфигурируется, рандом на процесс — DONE

- [x] Файл: `internal/infra/server/server.go:76-87` (сборка `RouterOptions`),
      `internal/adapters/http/router.go:171-179` (генерация случайного секрета)
- [x] До исправления `CSRFSecret` не заполнялся из флагов; в `flags.go` не было настройки
      CSRF-секрета. Сейчас флаг и env подключены, см. ниже.
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

### 4. `--disable-api` внутри auth-мидлвари — CONFIRMED, TODO (прежний REFUTED ошибочен)

- [x] Повторно проверен `internal/adapters/http/router.go`, `wrapMiddleware`:
      `withAPIDisabled` оборачивает `mux` раньше `newAuthMiddleware`, поэтому на входе запроса
      **auth выполняется раньше DisableAPI**. Поздняя обёртка — внешняя, а не внутренняя.
      Общий rate-limit и выдача CSRF-cookie тоже находятся снаружи DisableAPI.
- [x] Воспроизведено через настоящий `NewRouter` и `httptest`: при `DisableAPI=true`
      запрос `GET /notes/example` с Bearer вызывает `ResolveAPIToken` до ответа 503.
      При `RateLimit=1`, `RateBurst=1` второй немедленный запрос получает 429 вместо 503.
      Обычные note/link-маршруты пропускаются auth после определения пользователя, поэтому
      без credentials часто всё равно получается 503 — это не доказательство порядка.
- [x] Существующий `TestDisableAPI` создаёт роутер без `AuthManager` и с выключенным общим
      rate-limit. Он проверяет обход бизнес-хендлеров, но не обещание «before auth».
- [ ] Действие: вынести DisableAPI перед auth и общим rate-limit, чтобы поведение совпало с
      обещанием в README, `RouterOptions.DisableAPI` и `middleware_disable_api.go`.
- [ ] Действие: добавить регрессионные проверки с настроенным `AuthManager`, Bearer/cookie
      и включённым rate-limit: 503 без lookup пользователя и вызова бизнес-хендлеров.

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

### 6. TOCTOU cross-table slug между notes и links — PARTIAL (single-process DONE)

- [x] Перепроверено дважды против кода — исходная формулировка была неточной:
      `storage.Create` в sqlite/postgres **уже** маппит PK/unique-constraint violation в
      `domain.ErrSlugConflict` (`note_repository.go` обоих бэкендов) — same-table гонка
      (notes-vs-notes, links-vs-links) была защищена и раньше, «голого 500» там нет
- [x] До исправления реальная дыра была только **cross-table**: `notes` и `links` — разные SQL-таблицы с
      независимыми PK (`migrations/*/20260327173334_init.sql`,
      `migrations/*/20260721000001_links.sql`), общего constraint между ними нет и быть не
      может — уникальность slug между note и link держалась только на двух независимых
      `Exists()`-проверках (`notes.Manager.resolveUniqueSlug` → `links.Exists`,
      `links.Manager.slugTaken` → `notes.Exists`), без блокировки между check и write
- [x] Находка подтверждена детерминированным тестом до фикса — `barrierChecker` в
      `tests/manager/suite_test.go` форсировал оба `Exists()` в одну точку (`sync.WaitGroup`
      на 2 участника), тест падал: `POST /notes` и `POST /links` с одинаковым custom slug оба
      успешны одновременно — инвариант «глобально уникальный slug» из PRODUCT.md нарушался на
      практике, не только в теории
- [x] Реализован вариант 1 (advisory lock, in-process): новый пакет `internal/infra/lock`
      (`Locker` — keyed mutex с refcounting, `Lock(ctx, key) (unlock func(), error)`)
- [x] `notes.SlugLock`/`links.SlugLock` — новые seam-интерфейсы у потребителя (по конвенции
      DIP), обе реализации структурно совпадают (`Lock(ctx, key) (func(), error)`),
      единственный конкретный тип — `*lock.Locker`
- [x] `notes.Manager.resolveUniqueSlug` и `links.Manager.resolveUniqueSlug` теперь держат
      `slugLock` от resolve до `storage.Create` включительно (у links — с учётом retry-цикла
      на случайный slug: каждая отклонённая попытка снимает свой лок перед следующей)
- [x] `server.go`: **один общий** `lock.NewLocker()` передаётся в оба конструктора
      (`notes.NewManager`, `links.NewManager`) — лок на каждый менеджер по отдельности не
      защищал бы ничего, т.к. они не контендились бы друг с другом
- [x] Ограничение задокументировано явно: `Locker` — single-process. Multi-replica деплой с
      общей БД по-прежнему не защищён (нужен Postgres advisory lock) — не входит в этот фикс
- [ ] Отдельная оставшаяся задача: обеспечить cross-table уникальность между процессами
      с общей БД (например, транзакционный Postgres advisory lock) и проверить двумя
      независимыми экземплярами менеджеров. Общий in-process `Locker` этого не проверяет.
- [x] Regression-тест переписан: `TestCrossTableSlugUniqueness_ConcurrentCreate`
      (`tests/manager/suite_test.go`) теперь гоняет 20 итераций настоящей конкурентности через
      `s.Manager`/`s.LinkManager` (без искусственного барьера — с реальным логом гонка
      закрыта структурно, не зависит от таймингов), проверяет «ровно один успех». Зелёный с
      `-race` на sqlite и postgres
- [x] Юнит-тест самого примитива: `internal/infra/lock/locker_test.go`
      (`TestLocker_SerializesSameKey`, `TestLocker_DifferentKeysDoNotBlockEachOther`)
- [x] Полный прогон: `go build ./...`, `go vet ./...` (+ `-tags=integration`),
      `go test -race -count=1 ./...` и `go test -tags=integration -race -count=1 ./tests/...`
      — всё зелёное; `golangci-lint run` без новых замечаний

### 7. GET /links/{id} не отдаёт 410 для expired — DONE

- [x] Файл: `internal/adapters/http/ogen_handler.go` (`GetLink`)
- [x] `links.Manager.Peek` контрактно возвращает expired-ссылки как есть, `GetLink` не проверял
      `ExpiresAt` и всегда отдавал 200
- [x] Расхождение шире, чем описано: `openapi.yaml` **уже документирует** `410 Link has expired`
      для `GET /links/{id}`, и `mapGetLinkError` уже умеет `StatusGone` — то есть это было
      нарушение собственного контракта, а не только несогласованность с браузерным путём.
      Уточнение: проверку делает не `link_handler.go`, а `links.Manager.Redirect`, куда
      браузерный путь заходит
- [x] Найдено попутно: старый `TestGetLink_Expired` мокал `Peek` → `domain.ErrExpired` — исход,
      исключённый контрактом `Peek`. Тест проходил, покрывая недостижимую ветку, и ровно этим
      маскировал баг
- [x] Действие: проверка добавлена после privacy-гейта (порядок как в `LinkHandler.Redirect`:
      истёкшая приватная ссылка отвечает чужому 401, а не 410 — 410 подтвердил бы факт её
      существования). Ответ строится через тот же `mapGetLinkError`, что и остальные ошибки.
      Строку не удаляем, в отличие от redirect-пути: `GET /links/{id}` задокументирован как
      метаданные без side-effects, чистка происходит при следующем `GET /r/{id}`
- [x] Условие вынесено в `domain.Link.Expired(now)` и используется обоими путями — дублирование
      inline-сравнения и было причиной расхождения
- [x] Тесты: `TestGetLink_Expired` (410 + тело `{"message":"expired"}`),
      `TestGetLink_NotYetExpired_OK` (граница с другой стороны),
      `TestGetLink_ExpiredAndPrivate_Anonymous_Unauthorized` (порядок гейтов). Проверено, что
      без фикса `TestGetLink_Expired` падает с `expected 410, actual 200`

### 8. Тело ошибок ogen-декодирования не соответствует ErrorResponse — DONE

- [x] Файл: `internal/adapters/http/ogen_errors.go` (`newMaxBodyErrorHandler` →
      `newOgenErrorHandler`) и vendor `ogenerrors/handler.go:48`
- [x] Подтверждено вживую на запущенном сервере: битый JSON, missing required field, неверный
      тип → `400 {"error_message": ...}`; сюда же попадал и **415** (неверный `Content-Type`),
      о котором в заявке не сказано
- [x] Строже, чем «не совпадает»: в `openapi.yaml` `message` — **required** поле
      `ErrorResponse`, то есть клиент со строгой валидацией по контракту видел тело без
      обязательного поля. И ровно на тех входах, которые клиент чаще всего портит
- [x] Действие: `newOgenErrorHandler` больше не делегирует в `DefaultErrorHandler`, а пишет
      `errorJSON{Message: ...}` сам. Классификация статусов не менялась — тот же
      `ogenerrors.ErrorCode`, что использовал дефолтный хендлер; отдельно остаётся только 413
      для превышения лимита тела (ogen сам считает это 400)
- [x] Тест: `TestOgenDecodeErrors_UseErrorResponseShape` (4 подкейса: битый JSON, missing
      required, неверный тип, неверный Content-Type) + `TestCreateNote_BodyExceedsLimit_
      UsesErrorResponseShape`. Тело декодируется в `map[string]any`, а не в структуру, — иначе
      отсутствие `message` выглядело бы как пустая строка, а это ровно проверяемый дефект.
      Проверено, что при возврате делегирования тесты падают
- [x] 415 задокументирован в `openapi.yaml` для всех четырёх операций с `requestBody`
      (`POST /notes`, `PUT /notes/{id}`, `POST /links`, `POST /links/bulk`). Попутно нашлось,
      что `PUT /notes/{id}` не документировал и **413**, хотя лимит тела на него действует, —
      добавлено. `go generate` прогнан: `spec/openapi.yaml`, `ogenapi/`, `pkg/client/`
      пересобраны, сгенерированный клиент теперь декодирует 415 в типизированную ошибку вместо
      падения на неожиданном ответе. `TestOpenAPISpecInSync` зелёный
- [x] Сообщения санитизированы (`ogenErrorMessage`): наружу идут только структурированные детали,
      которые ogen отдаёт типизированно, — имя провалившего валидацию поля
      (`invalid request body: content (field required)`) и отвергнутый Content-Type
      (`unsupported content type: text/plain`, обрезается до 64 рун, т.к. заголовок ограничен
      только `MaxHeaderBytes`). Всё остальное схлопывается в фиксированное сообщение по статусу.
      Исходная ошибка не теряется — уходит в лог: `Debug` для 4xx (ошибка клиента), `Error` для
      5xx. Константа `decodeErrorMessage` общая с `common.writeDecodeError`, чтобы ogen-путь и
      нативный путь отвечали одинаково
- [x] Тесты: `TestOgenDecodeErrors_UseErrorResponseShape` теперь сверяет точный текст,
      `TestOgenDecodeErrors_DoNotLeakInternals` отдельно проверяет отсутствие `CreateNoteRequest`,
      `operation CreateNote`, `decode request`, `callback` на пяти вариантах битого тела,
      `TestOgenDecodeErrors_LongContentTypeIsTruncated` — обрезку эха

### 9. 429/500 отдаются через http.Error (text/plain) — DONE

- [x] Подтверждено: `middleware_faillock.go:40` и `middleware_ratelimit.go:57,64,70` (3 места)
      использовали `http.Error`, который пишет `Content-Type: text/plain; charset=utf-8` вместо
      документированного JSON `{"message"}`
- [x] Уточнение: выделенного `writeError()`, переиспользуемого «как есть», не нашлось —
      `(*common).writeError` завязан на маппинг `domain`-ошибок в статус (`errorStatusMessage`),
      а эти два middleware принимают решение о статусе сами (лимит, cache-miss), без
      domain-ошибки на входе. Вместо переиспользования `writeError` заведён отдельный
      низкоуровневый хелпер `writeJSONErrorStatus(w, r, log, status, msg)` в `errors.go`,
      которым теперь пользуются оба: и middleware (status уже решён), и потенциально
      `(*common).writeError` (не тронут — работает как раньше, использует ту же
      `errorJSON`/`mimeJSON` инфраструктуру)
- [x] `middleware_faillock.go`: `withFailLockout` получил параметр `log *slog.Logger`,
      единственный `http.Error` заменён на `writeJSONErrorStatus`
- [x] `middleware_ratelimit.go`: `allowRequest`, `withRateLimit`, `withTOTPRateLimit` получили
      `log *slog.Logger` (у `withRateLimit` — через новое поле `rateLimitMiddleware.log`); все
      3 вызова `http.Error` заменены
- [x] Обновлены все call sites: `router.go` (`registerOgenRoutes`, `wrapMiddleware`),
      `web_note.go` (`NoteHandler.RegisterRoutes`), `web_auth.go` (`AuthHandler.RegisterRoutes`)
      — везде прокинут `handler.log`/`h.log`
- [x] Действие: 429 добавлен в `openapi.yaml` для `PUT /notes/{id}`, `DELETE /notes/{id}` —
      как и просили — **и дополнительно** для `DELETE /links/{id}`, которая обёрнута тем же
      `withFailLockout` (`router.go:134`) и была тем же пробелом, просто не названа в находке.
      Синхронизирована embed-копия `internal/adapters/http/spec/openapi.yaml`
      (`go generate ./internal/adapters/http/...`, использует существующий
      `//go:generate cp -f ../../../openapi.yaml spec/openapi.yaml`)
- [x] Тест: `internal/adapters/http/middleware_faillock_test.go` (новый файл) —
      `TestWithFailLockout_LockedOut_ReturnsJSONError` (Content-Type + точное тело через
      `assert.JSONEq`), `TestWithFailLockout_UnderThreshold_ReachesUpstream` (контрольный кейс)
- [x] Тест: `internal/adapters/http/middleware_ratelimit_test.go` — добавлена JSON-проверка в
      `TestWithTOTPRateLimit_BlocksOverLimit` + новый `TestWithRateLimit_Exceeded_ReturnsJSONError`
      для общего `withRateLimit` (не только TOTP-путь)
- [x] Полный прогон: `go build ./...`, `go vet ./...` (+ `-tags=integration`),
      `go test -race -count=1 ./...`, `go test -tags=integration -race -count=1 ./tests/...`,
      `golangci-lint run` — везде чисто (кроме уже существующих, не связанных с этой находкой
      `nolintlint`-предупреждений в других файлах — `web_auth.go`, `link_handler.go`,
      `handler_test.go`, `server.go:172`)
- [x] Уточнение по регенерации ogen (изначально помечено как «не сделано, вне scope» —
      неверно, сделано в рамках этой же находки): в репозитории действительно нет
      `go:generate`-директивы для `internal/adapters/http/ogenapi/`/`pkg/client`, но бинарник
      `bin/ogen` уже стоял в `bin/`, и предыдущая генерация под тот же `openapi.yaml` (для #7/#8)
      уже лежала в рабочем дереве как ориентир по вызову. Прогнано вручную:
      `bin/ogen -target internal/adapters/http/ogenapi -package ogenapi -clean openapi.yaml` и
      аналогично для `-target pkg/client -package client`. Результат: `UpdateNoteTooManyRequests`,
      `DeleteNoteTooManyRequests`, `DeleteLinkTooManyRequests` появились в
      `oas_schemas_gen.go`/`oas_json_gen.go`/`oas_response_{decoders,encoders}_gen.go` обоих
      пакетов; в `pkg/client/oas_response_decoders_gen.go` подтверждён `case 429` в
      `decodeUpdateNoteResponse` (и парных decoder'ах) — клиент теперь декодирует 429 в
      типизированный вариант, а не в generic-ошибку
- [x] `mapUpdateError`/`mapDeleteError`/`mapDeleteLinkError` (`ogen_errors.go`) трогать не
      потребовалось: 429 для этих операций пишет `withFailLockout`/`withRateLimit` **до** того,
      как запрос доходит до ogen-хендлера (см. порядок в `router.go`), так что серверу не нужно
      самому конструировать `*TooManyRequests` — сгенерированные типы нужны только для
      декодирования на стороне клиента, и это уже подтверждено предыдущим пунктом
- [x] Полный прогон после регенерации: `go build ./...` (включая `pkg/client`),
      `go vet ./...` + `-tags=integration`, `go test -race -count=1 ./...`,
      `go test -tags=integration -race -count=1 ./tests/...`,
      `golangci-lint run ./...` (канонической командой из `go.mk`, без нестандартных флагов —
      комбинация `--build-tags=integration` с явным списком пакетов ранее дала ложные
      `nolintlint`-срабатывания на `//nolint:ireturn` в `middleware_faillock.go`/
      `middleware_ratelimit.go`, не воспроизводится канонической командой) — везде чисто

### 10. SQLite RunInTx и SQLITE_BUSY_SNAPSHOT — DONE (переоценка вниз была ошибочной)

- [x] Текущее расположение: `internal/infra/storage/sqlite/db.go`, `WithPragmas` и `Open`.
      Изначально фикс был в `internal/infra/server/db.go` (`withSQLitePragmas`,
      `openSQLiteDB`); перенос и подключение тестов завершены в №12.
- [x] ~~«busy_timeout=5000 уже стоит, значит не fail immediately»~~ — **неверно для этого
      случая**. Прагма действительно применяется (проверено `PRAGMA busy_timeout` = 5000), но
      SQLite **не вызывает busy handler**, когда транзакция уже держит read-лок и пытается его
      повысить до write: повышение под read-локом грозит дедлоком, поэтому возвращается
      `SQLITE_BUSY` немедленно — либо `SQLITE_BUSY_SNAPSHOT`, если другой писатель успел
      закоммитить дальше снапшота. `busy_timeout` бессилен ровно там, где выглядит нужнее всего
- [x] ~~«теоретически, узкий race-window, редкий кейс»~~ — **не редкий**. Замер на реальной
      форме транзакций (read-then-write, как во всех `RunInTx` в `storage/sqlite`: count+insert
      для API-токенов, select+update для consume инвайта и reveal), 8 писателей × 200 итераций:

      | режим | успешно | SQLITE_BUSY (5) | SQLITE_BUSY_SNAPSHOT (517) |
      |---|---|---|---|
      | deferred (было) | 120 / 1600 | 1186 | 294 |
      | immediate | 1600 / 1600 | 0 | 0 |

      Потеря 92.5% транзакций. Данные при этом не бились — счётчик сходился с числом успешных,
      то есть это чистые откаты, а не потерянные апдейты
- [x] Действие: `_txlock=immediate` добавлен в `WithPragmas` (прежнее имя `withSQLitePragmas`; поддержка драйвера
      проверена: `modernc.org/sqlite` разбирает `_txlock` в `deferred|immediate|exclusive`).
      Идемпотентность как у прагм — явно заданный оператором `_txlock` сохраняется
- [x] Цена задокументирована в комментарии: read-only транзакция теперь тоже встала бы в
      очередь за писателями. Сейчас таких нет — `RunInTx` используется только для
      read-modify-write; будущая read-only должна брать обычный запрос, а не откатывать эту
      настройку
- [x] Тесты: `TestSQLiteConcurrentWriters_NoLockContention` (ноль ошибок + счётчик равен числу
      транзакций), `TestSQLiteConcurrentWriters_DeferredIsTheProblem` (фиксирует причину: на
      явном `_txlock=deferred` тот же профиль всё ещё падает — если однажды перестанет, тест
      покраснеет и настройку можно будет пересмотреть), `TestWithSQLitePragmas` расширен.
      Прогнано `-count=5 -race`, нестабильности нет
- [x] Замеченное попутно открытие SQLite в обход боевого DSN исправлено в №12:
      SQLite-тесты в `tests/auth`, `tests/manager` и тесты репозиториев используют
      `sqlite.Open` / `Open`, включая прагмы и `_txlock=immediate`.

### 11. vendor не закоммичен, .dockerignore отсутствовал — DONE

- [x] `git ls-files vendor/` → 0 файлов — подтверждено
- [x] `.gitignore:3` содержит `vendor/` — подтверждено
- [x] `CLAUDE.md` утверждал «vendor is committed (`go mod vendor`)» — расхождение с реальным
      состоянием репозитория, подтверждено
- [x] Действие: выбран вариант (б) — исправлен `CLAUDE.md` (`## Infrastructure Notes`):
      теперь явно сказано, что `vendor/` тоже в `.gitignore` и сборка тянет зависимости из
      Go module proxy, а не из закоммиченного vendor-дерева. Вариант (а) (`git add -f vendor/`)
      не выбирался — коммит vendor-дерева это отдельное, более тяжёлое решение с последствиями
      для размера репозитория и ревью диффов, не для automated-doc-fix
- [x] Создан корневой `.dockerignore`: исключены `.env`/`.env.*`, `*.db`, `*.sqlite`,
      `*.sqlite3` и SQLite sidecar-файлы, `.git`, `bin/`, локальные бинарники, `*.log`,
      `*.test`, `*.out`, `*.prof`, отчёты покрытия и тестов, локальные кеши.
      Шаблоны секретов и БД действуют и во вложенных каталогах.
- [x] `docker build .` прошёл. Проверены 5767 записей `/src` в `builder`: реально
      существующие локально `.env`, `padmark.db`, `.git`, `bin/`, `test.log`, `cover.out`
      отсутствуют. Проверены все три слоя итогового образа: только `/etc/passwd`,
      бинарник сервера и каталоги, без секретных файлов и локальных БД.
- Уточнение исходной находки: `COPY . .` загрязнял слой **builder** и кеш сборки;
  финальная scratch-стадия и раньше копировала только бинарник, `/etc/passwd` и `/data`.

### 12. Тесты открывали SQLite в обход боевого DSN — DONE (прагмы теперь покрыты, не из REVIEW.md)

- [x] Найдено пользователем при код-ревью, не входило в исходный REVIEW.md. Подтверждено дважды
      перед фиксом
- [x] 14 файлов на весь проект открывали SQLite напрямую через
      `sql.Open(sqliteshim.DriverName(), <голый DSN>)`, в обход `openSQLiteDB`/
      `withSQLitePragmas` (`internal/infra/server/db.go`) — 10 юнит-тестов репозиториев в
      `internal/infra/storage/sqlite/*_test.go` + 4 интеграционных теста в `tests/`
- [x] Реальная потеря: `journal_mode(WAL)` и `_txlock=immediate` (сам фикс находки #10,
      `SQLITE_BUSY_SNAPSHOT`) не проверялись **нигде**, кроме двух файлов в пакете `server`,
      которые могли звать неэкспортированные функции напрямую. `foreign_keys(1)`/
      `busy_timeout(5000)` местами дублировались вручную прямо в DSN-строке
      (`api_token_repository_test.go`, `user_repository_test.go`) — копипаста, не переиспользование
- [x] Риск: изменение `withSQLitePragmas` в будущем (новая прагма, другая `_txlock`-стратегия)
      прошло бы мимо всех этих тестов — они гоняли не то поведение, что реально работает в проде
- [x] Реализован вариант 2 (из предложенных: (1) экспортировать функцию из `server`, (2) перенести
      в `storage/sqlite`, (3) только задокументировать) — перенос в
      `internal/infra/storage/sqlite`, архитектурно правильное место (прагмы это забота
      sqlite-слоя, не композиционного корня сервера)
- [x] Новый `internal/infra/storage/sqlite/db.go`: экспортированные `Open(dsn) (*bun.DB, error)`
      и `WithPragmas(dsn) string` (перенесены из `server/db.go` как есть, с той же
      документацией и замерами по `_txlock=immediate`)
- [x] `internal/infra/server/db.go`: `openSQLiteDB`/`withSQLitePragmas` удалены,
      `openers["sqlite"] = sqlite.Open` — `dbOpener` и `sqlite.Open` совпадают по сигнатуре
      один в один, обёртка не нужна
- [x] `internal/infra/server/sqlite_concurrency_test.go` → перенесён (`git mv`) в
      `internal/infra/storage/sqlite/concurrency_test.go`, символы обновлены на `Open`/
      `WithPragmas` — это и есть регрессионный тест на `_txlock=immediate` из находки #10,
      теперь живёт рядом с самой функцией
- [x] `TestWithSQLitePragmas` вынесен из `internal/infra/server/redirect_test.go` в новый
      `internal/infra/storage/sqlite/db_test.go` как `TestWithPragmas`
- [x] Все 14 мест переведены на `Open(dsn)`/`sqlite.Open(dsn)` — включая 2 cascade-теста
      (`TestCreate_CascadesWithUser`, `TestRevoke_CascadesSessionsAndInvites`), где убрана ручная
      дупликация `_pragma=foreign_keys(1)` в DSN-строке (теперь она включена по умолчанию для
      всех, комментарии обновлены — раньше говорили «suite's shared DSN does not set the
      pragma», теперь это неверно, поэтому переписаны)
- [x] Убраны лишние импорты (`database/sql`, `sqlitedialect`, `sqliteshim`) везде, где они стали
      не нужны после перехода на `Open`
- [x] Полный прогон: `go build ./...`, `go vet ./...` (+ `-tags=integration`),
      `go test -race -count=1 ./...`, `go test -tags=integration -race -count=1 ./tests/...`,
      `golangci-lint run ./...` (каноническая команда из `go.mk`) — everywhere чисто, кроме
      предсуществующих 26 `nolintlint`-предупреждений в файлах, которых эта правка не касалась

---

## Часть 2. Дополнительные проверки REVIEW.md — проверено, исправления остаются

- [x] Проверена UI-часть находки №1: `TestOwnerEditFlow_EditPage_PrivacyGate` проходит через
      настоящий HTTP-стек и SQLite. Текущая матрица — в Части 1, №1: анонимный браузер 303,
      аутентифицированный чужой владелец 403, API-клиент 401; плейнтекст не выдаётся.
- [x] Проверены q-values в `internal/adapters/http/negotiate.go`: параметры после `;`
      отбрасываются, выбирается первый распознанный MIME. Воспроизведено на текущей функции:
      `text/html;q=0,application/json;q=1` → HTML;
      `application/json;q=0.1,text/html;q=1` → JSON.
- [x] Проверены q-values в `internal/adapters/http/negotiate.go`: параметры после `;`
      отбрасывались, выбирался первый распознанный MIME; зафиксированы дефекты старого
      выбора по порядку заголовка («q весам не соответствует»)
- [x] Исправлена семантика выбора формата (переписан `negotiate.go`, RFC 9110 Accept):
      q-веса — первичный ключ ранжирования (`text/plain;q=0.1, application/json;q=1` → JSON,
      а не plain); `q=0` = «недопустимо» и вето сильнее wildcard («text/html;q=0, */*» →
      JSON, не HTML); специфичность ломает равный q (exact > text/*|application/* > */*);
      позиция в заголовке — последний тай-брейк; полное равенство (один `*/*`/`text/*`
      накрывает несколько форматов) разрешает серверное предпочтение `formatOrder`
      **JSON → HTML → plain** (машинный дефолт, `curl` остаётся на JSON); `?raw=1` по-прежнему
      сильнее всего. Обработаны: отсутствие q (=1), клемп >1, битый/отрицательный q (=0,
      никогда не выигрывает), параметры кроме q игнорируются, регистр и пробелы. Комментарий
      в коде переписан под фактическую семантику. Не-HTML `Accept: audio/mp3` и «всё
      отказано» (например `text/*;q=0`) → дефолтный JSON (без 406 — статусное поведение
      сознательно не менялось)
- [x] Регрессионные тесты: `internal/adapters/http/negotiate_test.go` (11 table-кейсов:
      q-ранжирование в обе стороны, тай-брейки специфичность/позиция, q=0-вето против
      wildcard, wildcards, точные типы, параметры, клемп/битый q, fallback-и, регистр/пробелы,
      реальный Chrome-список и curl, `?raw=1`). Проверено, что privacy-гейт и JSON-ветка
      `/setup` на реальных заголовках браузера/curl/CLI не изменились; JSON-ключи ответов
      и CLI-контракты не затронуты
- [x] Прогон: `golangci-lint run ./internal/adapters/http/` — 0 issues; пакетные тесты
      и полный `go test -tags=integration ./...` — зелёные
- [x] Проверены build-теги: все шесть файлов `tests/auth` имеют `//go:build integration`;
      три файла `tests/manager` тега не имеют. Последний пакет запускается обычным
      `go test ./...` и поднимает PostgreSQL через testcontainers. В `.github/workflows/ci.yml`
      тесты запускаются без `-tags=integration`, поэтому `tests/auth` в CI пропускается.
- [x] Согласовать политику build-тегов (CLAUDE.md: всё в `tests/` — за `//go:build integration`):
      тег добавлен в три файла `tests/manager`; в CI добавлена джоба `integration`
      (`go test -race -tags=integration ./tests/...`, зеркалит `make test`), docker-push
      гейтится ею. Юнит-прогон `tests/` больше не трогает
- [x] Проверены три `http.Server` в `internal/infra/server/server.go`: основной,
      HTTP→HTTPS redirect и metrics. У всех `IdleTimeout` явно не задан (0), но эффективное
      поведение разное: Go использует `ReadTimeout` как fallback. У основного он настраивается,
      у redirect/metrics тоже равен 0; `ReadHeaderTimeout` idle-timeout не заменяет.
- [x] Определена и явно задана политика `IdleTimeout` для всех трёх серверов; добавлена
      проверка настроек. Сам факт отсутствия поля проверен, политика исправлена. Сделано:
      константа `idleTimeout = 60 * time.Second` рядом с `readHeaderTimeout` (вариант 2
      без нового флага — 22 флага уже предел) с комментарием «почему явно»; три литерала
      вынесены в конструкторы `newMainServer` / `newRedirectServer` / `newMetricsServer`,
      все задают `ReadHeaderTimeout + IdleTimeout` (инлайн-литерал с выпавшим полем не
      поймает ни компилятор, ни линтер, а конструкторы — то, что закрепляется тестом).
      Read/Write/MaxHeaderBytes только на main (флаги); на auxiliary не заданы намеренно —
      один редирект/метрики без тела. Тест `TestServerTimeoutPolicy`
      (`internal/infra/server/timeouts_test.go`) фиксирует поле на всех трёх. Прогон:
      `go build ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run ./...` (0 issues),
      `go test ./internal/infra/server/` — чисто. Оговорка: без `-race` — cgo-линковка
      сломана машинным toolchain'ом (SDK `MacOSX27.0.sdk`: arch `arm64e.x1` не знает tapi
      установленного clang), проверено `CGO_ENABLED=0` — проект pure-Go (modernc sqlite +
      pgdriver); `-race` на darwin недоступен до обновления LLVM/CommandLineTools.

---

## Часть 3. Находки из PRODUCT.md (8 пунктов — «чего padmark пока не даёт»)

### 1. Агентный интерфейс — DONE (CLI, Go-клиент, MCP-сервер, llms.txt)

- [x] Перепроверено исходное утверждение «нет SDK на Go»: оно неверно. Есть
      сгенерированный ogen-клиент `pkg/client`, которым пользуется `padmark-cli`
      (`internal/adapters/cli/app.go`). REST/OpenAPI и CLI уже доступны для автоматизации.
- [x] Действие: `/llms.txt` добавлен — короткий рукописный Markdown-обзор машинного surface
      (операции, auth, предупреждение про `edit_code`, ссылка на `/api/openapi.yaml`, не дубль
      spec). Публичный (`isPublicPath`), глушится при `--disable-api` вместе с `/api/openapi.yaml`,
      сегмент `llms.txt` зарезервирован в `domain.ReservedSlugPrefixes()` точно по сегменту —
      `llms`, `llms-notes`, `llms/...` остаются валидными слагами
- [x] Действие: MCP-сервер реализован — `cmd/padmark-mcp`, официальная SDK
      `github.com/modelcontextprotocol/go-sdk` (stdio-прокси через `pkg/client`, код в
      `internal/adapters/mcp/`). 9 tools: create/get/update/delete_note, create_link(s_bulk),
      get/delete_link, check_server (healthz+readyz). Token-резолв как у CLI
      (`--token`/`PADMARK_TOKEN` → `~/.config/padmark/token`, envelope `pmk_…` принят),
      общий файловый код вынесен в `internal/infra/tokenfile`. Contract edit_code: код от
      create кладётся в session-vault и не печатается в result (не попадает в транскрипт
      модели), `reveal_edit_code` — opt-in печать. Fail-soft: сеть на старте не трогается,
      ошибки сервера/сети возвращаются isError-результатом с remediation. Тесты: юнит
      (`internal/adapters/mcp/`, mockgen + in-memory MCP-транспорт SDK) и интеграционный
      (`tests/mcp/`, реальный SQLite-стек + боевой роутер + настоящий MCP-клиент).
      README: раздел «MCP server» c примером `mcpServers`-конфига; «Three ways» → «Four ways»
- [x] Действие: формулировки README сверены с реальным состоянием — расхождений нет, правки
      не потребовались. Проверено по коду: (а) заявление «built for agents» обосновано — MCP
      (`cmd/padmark-mcp`, 9 tools), `/llms.txt`, CLI, ogen-клиент `pkg/client`; (б) claim
      «one URL, many consumers»: `Accept: application/json` → JSON, браузер → HTML (совпадает
      с `negotiate`), CLI читает stdin без `--content`/`--file` (`cli/app.go:131-154`); (в)
      маршруты `/api` (Redoc), `/api/openapi.yaml`, `/llms.txt` зарегистрированы
      (`web_index.go`); (г) раздел «MCP server»: путь бинаря, список 9 tools — точное
      совпадение с `internal/adapters/mcp/`, флаги `--token`/`--base-url`/`--timeout` и
      env `PADMARK_TOKEN`/`PADMARK_URL`/`PADMARK_TIMEOUT` с дефолтом
      `http://localhost:8080` — как в `mcp/flags.go`, конверт `pmk_…` несёт server URL
      (`DecodeAPITokenEnvelope`), `reveal_edit_code` — opt-in печать, по умолчанию код не
      попадает в результат (не в транскрипт модели); (д) «get_note is not read-only» /
      «get_link is read-only, burn на редирект-пути» — совпадает с серверной семантикой
      (Peek против Redirect); (е) «обе доки отдаются без токена и глохнут под
      `--disable-api`» — `isDisabledAPIPath` покрывает `apiDocsPath`/`apiSpecPath`/
      `llmsTxtPath`, в auth-whitelist оба пути есть (`middleware_auth.go:168,172`)

### 2. Развязать custom slug и ключ шифрования — DONE (реализация; переход существующих установок — операция администратора)

- Новые записи notes/links и обновление заметок используют `pmc2$p$…` (`content.go`):
  случайный 256-битный DEK, AES-256-GCM и обёртка DEK под KEK из keyring. HashSlug и тип
  ресурса используются только как AAD. HKDF(slug) остался в отдельном legacy-reader
  для старых строк; механизм шифрования TOTP сохранён.
- [x] Спроектирована схема с отдельным случайным 256-битным ключом данных (DEK) и
      мастер-ключом инсталляции, шифрующим DEK (KEK). Основной формат реализован;
      реализованные переход и ротация описаны ниже.
- [x] Продумана миграция с учётом того, что исходные slug отсутствуют в БД.
- [x] Реализован новый формат: `ContentCipher.Encrypt/Decrypt` принимают DB-ID, не slug.
      Ключи от ID не производятся. Адаптер `ContentEncryptor` получает legacy-slug отдельно
      и использует его только для внутреннего legacy-слоя `kind=l`; raw legacy в обычном
      чтении отклоняется. Ошибка версии/ключа/tag не запускает legacy-fallback.
- [x] Сервер подключает новый шифратор к обоим менеджерам; обязательный
      `--content-keyring-file` / `PADMARK_CONTENT_KEYRING_FILE` проверяется до открытия БД.
      Файл содержит active kid и 32-байтные ключи в base64; случайного fallback нет.
- [x] Реализованы управление ключами, миграция и ротация; проверены SQLite и PostgreSQL.
      CLI `encryption`: keygen/key-add/register/key-activate/key-remove, status, migrate,
      rewrap, convert; для трёх data-команд предусмотрен dry-run.
- [x] Обновлены README, `.env.example`, Docker Compose и исключения keyring из git/build
      context. `--custom-slugs=false` сохранён: публичные предсказуемые URL всё ещё угадываются.

**Граница текущей поставки:** новые writes независимы от slug. `serve` до запуска listeners
и TTL/burn-путей проверяет реестр KEK и все живые конверты; raw legacy, повреждённые записи,
отсутствующие/подменённые ключи и незавершённая миграция блокируют запуск. `encryption migrate`
пакетно оборачивает старые строки в `kind=l`, не зная исходных slug. `encryption convert`
принимает известные slug через stdin и переводит заметки/ссылки в `kind=p`. Для неизвестных
slug внутренний legacy-слой остаётся; его число видно в `status`. `Peek` не переписывает данные;
обычный Update заметки пишет `kind=p`. Записи реальной установки автоматически не мигрировались.

Проверки управления ключами: `tests/encryption` запускает одинаковые сценарии на SQLite
и реальном PostgreSQL: dry-run, повторный запуск после сбоя, CAS при update/delete,
сохранение метаданных, ротация без изменения payload, неверные/удалённые ключи, повреждения,
конвертация известных slug. Проверено восстановление SQLite через `VACUUM INTO` и PostgreSQL
через `pg_dump`/`pg_restore`: старой копии нужен старый KEK. CLI-проверки покрывают генерацию
0600 без перезаписи, регистрацию/активацию и запрет удаления используемого/активного KEK.

Проверка текущей реализации: `CGO_ENABLED=0 go test -tags=integration -count=1 ./...`
прошёл; race для crypto/notes/links и fuzz декодера (5 секунд) прошли. Lint пакетов
управления ключами — без замечаний; общий lint сообщает отдельную ошибку `exhaustruct_v5`
в `internal/adapters/http/negotiate.go:195`. Docker build и CLI smoke-test прошли;
в слоях runtime/builder не обнаружены исключённые локальные БД, env-файлы и keyring.

#### Реализованная схема

Каждая новая заметка и ссылка получает собственный DEK: 32 байта из `crypto/rand`.
При обновлении зашифрованного содержимого генерируется новый DEK. AES-256-GCM шифрует
`Note.Content` / `Link.TargetURL`, затем KEK шифрует DEK. В БД сохраняются только шифротекст,
зашифрованный DEK и несекретные параметры формата. Это стандартная
[схема envelope encryption](https://docs.cloud.google.com/kms/docs/envelope-encryption).

```text
crypto/rand → DEK ── AES-256-GCM ──→ encrypted payload
               └── AES-256-GCM под KEK ──→ wrapped DEK
slug → SHA-256 → поиск записи и привязка шифротекста к записи (AAD)
```

`notes.id` / `links.id` остаются `HashSlug(slug)`: URL, PK, reveal-токены и связи не меняются.
HashSlug используется как идентификатор в authenticated additional data (AAD), но не как
ключ или вход KDF. Тип ресурса (`note` / `link`) тоже входит в AAD, чтобы нельзя было
перенести конверт между таблицами или строками. Это не защита от отката всей записи
к старой корректной версии: anti-rollback потребовал бы отдельного доверенного состояния.

Цель — защитить зашифрованный payload при утечке БД без KEK. Заголовки заметок, owner/privacy,
счётчики и временные метки сейчас открытые и в этом изменении такими остаются.
Доступ к серверу с загруженными ключами позволяет читать контент; публичный угадываемый URL
остаётся публичным. Шифрование не заменяет privacy-гейт и не делает slug секретнее.

#### Формат конверта и разделение с TOTP

Использовать существующие TEXT-поля `notes.content` / `links.target_url`; ключ и payload
записываются одной строкой атомарно. Строго версионированный формат:

```text
pmc2$<kind>$<kid>$<base64url(wrapped_dek)>$<base64url(payload)>
```

- `kind=p`: внутри новый plaintext-payload; `kind=l`: внутри старый зашифрованный payload
  для переходной миграции ниже. Оба вида защищены новым случайным DEK и KEK.
- `kid`: несекретный ID KEK из keyring, `[A-Za-z0-9_-]{1,64}`. Версия фиксирует алгоритмы;
  произвольного выбора алгоритма из недоверенного конверта нет.
- Оба бинарных поля имеют вид `nonce || ciphertext || tag`: nonce 12 байт, tag 16 байт.
  `wrapped_dek` после декодирования имеет ровно 60 байт, расшифрованный DEK — ровно 32.
  Для каждого шифрования — свежий случайный nonce; ошибки RNG прерывают операцию.
- AAD payload: фиксированный purpose-prefix, версия, kind, тип ресурса и 32 байта DB-ID.
  AAD обёртки DEK дополнительно включает `kid` и SHA-256 зашифрованного payload.
  Кодирование полей однозначное: фиксированные размеры или длина перед переменным полем.
  `kid` не входит в AAD payload: ротация KEK меняет только обёртку DEK.
- Неизвестная версия/kind/kid, неправильная длина, повреждённый tag или некорректная кодировка —
  ошибка, без попытки прочитать повреждённый `pmc2` как legacy. Задать пределы размера до
  декодирования/выделения памяти, учитывая дополнительный base64 у переходных записей.

Вынести это в отдельный `ContentCipher`. Существующий `crypto.Encryptor` используется внутри
`crypto.Sealer` для TOTP (`sealer.go`) — заменять поведение `crypto.New()` глобально нельзя.
TOTP-конверты и их password-derived ключи этим проектом не меняются. Интерфейсы notes/links
должны явно различать контекст записи и legacy-slug; новая ветка `kind=p` вообще не получает
slug для криптографической операции. Legacy-декодер остаётся изолированным переходным путём.

#### Мастер-ключ, запуск и ротация

- Ввести `--content-keyring-file` / `PADMARK_CONTENT_KEYRING_FILE`: путь к файлу с активным
  `kid` и набором KEK. Каждый KEK — случайные 32 байта в base64, не пароль, не CSRF-секрет.
  Значения ключей не передавать аргументами CLI и не писать в логи. Keyring хранится вне БД
  и build context; в Docker монтируется как secret, доступный UID сервера только для чтения.
- Отдельная команда генерации создаёт keyring без перезаписи существующего файла, с правами
  0600. `serve` не создаёт случайный KEK при каждом старте и не откатывается к slug-шифрованию
  при отсутствии ключа. Без корректного keyring новая версия не начинает обслуживать запросы.
- Добавить миграциями обоих бэкендов метаданные инсталляции: UUID инсталляции, состояние
  перехода и реестр `kid` с AEAD-проверочным значением, привязанным к UUID/kid отдельным AAD.
  Реестр не содержит открытых KEK/DEK. При запуске проверять ключи, требуемые текущей БД,
  чтобы неправильный keyring останавливал сервер до обработки burn/TTL-запросов.
  Регистрация нового ключа — явная операция, не перезапись проверки при неудачной расшифровке.
  Для новой пустой БД первоначальная регистрация и завершённое состояние создаются одной
  транзакцией; обнаруженные существующие raw-записи требуют отдельной миграции.
- Ротация: сначала доставить новый ключ всем репликам для чтения, затем переключить активный
  `kid`; командой rewrap расшифровать только DEK и зашифровать его новым KEK. Payload и его
  nonce остаются прежними. Обновлять целый конверт условным CAS по старому значению.
- Старый KEK удаляется из рабочего keyring только после проверки отсутствия ссылок на него.
  Для старых резервных копий он сохраняется отдельно. Потеря всех подходящих KEK означает
  потерю данных; вместе с процедурой backup нужна проверяемая процедура restore ключей.
- Для случайных GCM-nonce не превышать `2^32` операций шифрования одним ключом суммарно
  по репликам, миграциям и rewrap; ротировать KEK заранее. Лимит и формат nonce соответствуют
  [документации Go AEAD](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce).

#### Миграция существующих записей

**Полная автоматическая расшифровка по одному дампу невозможна:** БД содержит SHA-256(slug),
а старый AES-ключ получается из исходного slug. Хеш, edit_code и пароль владельца не позволяют
восстановить этот ключ. Поэтому «SQL-миграция перешифрует всё в plaintext-v2» была бы неверным
обещанием. Один lazy-upgrade также оставил бы непосещаемые записи с прежней защитой.

Реализован двухступенчатый переход с явным учётом оставшихся legacy-записей:

1. Сделать резервную копию БД и отдельно keyring. Остановить все старые экземпляры сервера
   и другие writers. Первое внедрение — с окном обслуживания; смешанный деплой старой
   версии, пишущей legacy, и новой версии не поддерживается.
2. Применить schema-миграции для метаданных; отдельной data-migration командой обойти записи.
   Проверить структуру старого base64 и минимальную длину nonce/tag; без знания slug
   зашифровать **существующий legacy-шифротекст** как payload `kind=l`
   под новым случайным DEK и завернуть DEK под KEK. Так все живые строки получают внешнюю
   защиту от утечки БД сразу. Это не расшифровка legacy и не доказательство его целостности.
3. Перезапись — `WHERE id = ? AND content = ?` / `target_url = ?`, только поле шифротекста.
   Никаких upsert: удалённая запись не возрождается. Не менять views, TTL, burn-флаги,
   edit_code, ownership, created_at/updated_at. Малые транзакции, повторяемый запуск,
   dry-run, итоговые счётчики raw/legacy-wrapped/native/failed и ненулевой exit при ошибке.
4. Уже созданные конверты при повторном запуске проверять и пропускать. Повреждённый конверт
   не оборачивать повторно как legacy. Лишь после полного прохода, проверки новых конвертов
   и отсутствия raw-строк отметить переход завершённым и запустить новую версию.
   Обычный `serve` не принимает незавершённый переход и не пишет/читает raw legacy.
5. При чтении `kind=l` новый компонент сначала снимает внешнее шифрование, затем применяет
   legacy-декодер с предоставленным slug. Новые записи и обычное обновление заметки всегда
   пишут `kind=p`. Для полной конвертации старых заметок **и ссылок** предусмотреть отдельную
   команду со списком известных slug через stdin, проверкой HashSlug и CAS; не логировать
   slug/содержимое. Для неизвестных slug обёртка остаётся, что явно отражается в статистике.
6. `Peek` остаётся без записей в БД: он вызывается до privacy-гейта и обещает отсутствие
   side-effects. Не добавлять туда скрытый lazy-upgrade. Online-миграция после авторизованного
   чтения возможна позднее как отдельная доработка. Burn/TTL-пути не должны вызывать миграцию
   через `View`/`Redirect` или повторно создавать уже consumed-запись.

В `kind=l` зависимость **внутреннего старого слоя** от slug ещё сохраняется; это переходный
формат, а не полное выполнение требования о независимости ключа. Удаление legacy-кода
возможно только при нулевом числе таких записей. Если известен весь набор slug, команда
полной конвертации закрывает этот остаток сразу; иначе требуется дальнейшая конвертация,
истечение TTL или явное решение оператора о данных, которые больше нельзя открыть.

Новый формат не исправляет уже утёкшие старые дампы, WAL, журналы PostgreSQL и резервные копии:
для них сохраняется прежняя модель угроз. План обслуживания должен учитывать их срок хранения
и очистку освобождённых страниц средствами конкретной БД. После появления новых записей нельзя
просто откатить бинарник на старую версию; восстановление из pre-migration backup откатит
и последующие изменения данных. Автоматической разрушающей down-миграции не предусматривается.

#### Реализация и критерии готовности

- [x] `internal/infra/crypto`: ContentCipher, строгий codec `kind=p`, keyring loader
      и изолированный legacy-reader. Round-trip, разные DEK/nonce для одинакового
      plaintext, неверный KEK, подмена kind/kid/ID/типа ресурса, перемешивание конвертов,
      обрезанные данные, RNG failure и отсутствие fallback проверяются тестами.
- [x] `internal/usecases/notes` / `links`: новый порт шифрования, запись `kind=p`, чтение
      нового формата и wrapped legacy; `kind=p` расшифровывается по DB-ID без исходного slug.
      Общая интеграционная suite переведена на реальные envelope-шифраторы; проверены
      Create/Update/Peek/View/Redirect и burn/TTL на SQLite/Postgres. TOTP-тесты проходят.
- [x] Реализованы `kind=l` и rewrap для массовой миграции и смены KEK без изменения payload.
- [x] SQLite/Postgres: метаданные миграции/ключей, пакетный обход и CAS; проверка повторного
      запуска после сбоя, конкурентного update/delete, отсутствия потери счётчиков и
      восстановления удалённых записей. Полная миграция не раскрывает контент в выводе.
- [x] Server/CLI: генерация keyring, status/dry-run/migrate/rewrap/convert, проверка keyring до старта,
      остановка при отсутствии/подмене ключей или незавершённой миграции.
      Проверяются структура файла, AEAD key-check инсталляции и все живые конверты.
- [x] Проверены restore с соответствующим keyring и ротация на обеих БД, включая `kind=l`;
      отсутствие мастер-ключей в git, Docker build context и слоях образа.
- [x] Документация: новые требования запуска/backup/rotation, пределы
      защиты, путь обновления старой установки и учёт незавершённой native-конвертации.

Default `--custom-slugs` — **по-прежнему false**:
новая схема убирает криптографическую причину запрета, но не угадываемость публичных URL.
Разрешать через явный флаг; изменение default вынести в отдельное продуктовое решение
после внедрения. README описывает уже работающий новый формат и отдельно ограничения legacy.

### 4. Hosted demo — TODO (в репозитории не подтверждено)

- README, OpenAPI и Docker-артефакты описывают self-host. Ссылки на работающий demo/playground
  и конфигурации такого деплоя в репозитории не найдены. Внешнее состояние хостинга не проверялось.
- Продуктовая гипотеза: публичная песочница упростит знакомство с продуктом;
  время установки и влияние на acquisition здесь не измерены.
- [ ] Действие: развернуть `padmark.dev` (или аналог) с rate-limit и коротким TTL (например
      1 час) на все создаваемые в песочнице данные
- Это не блокирует остальную разработку — по продуктовому приоритету стоит ниже
      технического ядра (P0–P2 сначала)

### 5. Headless auth setup — DONE

- Было: HTML/form-маршруты `GET/POST /setup`, `/admin`, `POST /admin/api-keys`,
  `POST /admin/invite` с CSRF/session-flow (`web_auth.go`, `web_admin.go`);
  готового headless-контракта и CLI-команд не было. «Невозможно завести в CI» было
  категорично (браузерный поток скриптуем), но headless-контракт отсутствовал — это закрыто.
- [x] Решения пользователя: `POST /api/tokens` — self-service для любого аутентифицированного
      (токен всегда на себя); bootstrap — JSON-ветка на существующем `POST /setup` (без нового
      маршрута); `POST /api/invites` — admin-only (зеркало `POST /admin/invite`)
- [x] Действие: `padmark-cli setup` — реализовано (`cli/setup.go`): JSON-POST на `/setup`
      с явным `Accept: application/json`, печатает otpauth URI одной строкой в stdout,
      предупреждения — в stderr. Пароль: `--password` или первая строка пайпнутого stdin;
      TTY без флага — отказ (`errSetupNoPassword`), visible-echo промпт сознательно не
      введён (обход позволял бы x/term-зависимость — единственная, что потребовалась бы).
      `--invite` зачисляет нового не-админа по инвайту
- [x] Механика JSON-ветки `POST /setup` (`web_setup_json.go`, `setupGuard` в `web_auth.go`):
      ветка выбирается по `negotiate` (JSON ⟺ не-HTML/не-raw — браузеры всегда на
      CSRF-guarded HTML-пути, `?raw=1` туда же); JSON-клиент не CSRF-guarded (нет cookie-
      сессии, инвайт — bearer-эквивалент), хардненинг — отказ на `Sec-Fetch-Site` не в
      `same-origin|none` (закрывает cross-site fetch с форсированным Accept). Статусы:
      201, 400, 403 (setup closed), 409 (username), 410 (инвайт потрачен/просрочен),
      422 (weak password). Ответ: `{"otp_auth_uri", "username", "is_admin"}`
- [x] Действие: `POST /api/tokens` — реализовано: ogen-операция `createAPIToken`
      (`openapi.yaml`, тег accounts; регенерированы ogenapi/pkg/client), handler в
      `ogen_handler.go` (+ `ogen_accounts.go`: WithAuthManager, конверт pmk_ от
      `requestOrigin`-middleware), self-scoped (`CreateAPIToken(ctx, usr.ID)`), cap 20 → 429
      (`domain.ErrAPITokenLimit` в `domainErrStatus`), 503 при не подключённом store
      (`ErrFeatureNotSupported`)
- [x] Действие: `POST /api/invites` — реализовано: admin-gate в handler (зеркало
      `requireAdmin`) + re-check в usecase, ответ `{"invite_url"}` с учётом
      `--public-scheme` (`withRequestOrigin`; тест ForcedScheme). Обе операции добавлены в
      `registerOgenRoutes`, в `isDisabledAPIPath` (`--disable-api` глушит их до auth) и в
      `spec/llms.txt` (раздел Account endpoints + JSON-/setup)
- [x] Use-cases: `AcceptFirstAdmin`/`AcceptInvite` теперь возвращают `*auth.Enrollment
      {TOTPURI, QRCode}` (URI строится пока raw-секрет в scope — после Seala достать нельзя);
      `crypto.OTPAuthURI` + `TOTP.TOTPURI` (валидация парсингом otp.NewKeyFromURL).
      Мок-интерфейсы перегенерированы
- [x] README: раздел «Headless provisioning (accounts)» (CLI-команды, once-only предупреждения,
      cap, JSON-/setup, 503)
- [x] Тесты: юнит `internal/adapters/http/accounts_test.go` (JSON-branch матрица negotiate/
      Sec-Fetch-Site/raw=1; 429/410/409/422/403-401-503; envelope от origin; ForcedScheme),
      `internal/adapters/cli/setup_test.go` (пайпнутый пароль, TTY-отказ), интеграционный
      `tests/auth/accounts_flow_test.go` → `TestAccountsFlow`: CLI setup → otpauth URI →
      create-token → конверт сразу применим → инвайт через API → CLI setup по инвайту →
      replay 410 → не-админ 403 → cap 429 (20 токенов), закрытый bootstrap 403
- [x] Прогон: `go build ./...`, `go vet ./...` (+ `-tags=integration`), `gofmt`,
      `golangci-lint run ./...` — чисто по всем файлам этой задачи; `go test -count=1 ./...`
      и `-tags=integration ./...` — 19 пакетов ok, без FAIL (без `-race` — cgo-линкер на
      машине сломан, см. Часть 2 п.4). Полный lint не содержит новых замечаний; 4 preexisting
      goconst в файлах параллельной правки (`mcp/app.go`, `mcp/server.go`,
      `crypto/content.go`, `infra/server/app.go`) — не из этой задачи

### 6. `edit_code` показывается один раз и невосстановим — DONE (документация + owner bypass)

- Для анонимных заметок (дефолт при `--enable-accounts=false`) `edit_code` — единственный
      способ редактировать/удалить заметку
- Показывается ровно один раз при создании, в БД хранится только argon2id-хеш.
  Потеря кода анонимной заметки лишает возможности менять/удалять её, но сама по себе
  не лишает чтения по URL. Одного URL недостаточно для редактирования.
- [x] README, «How notes work» и «How short links work», явно предупреждает:
      код показан один раз и не восстанавливается.
- [x] Альтернатива для владельца уже реализована: `notes.Manager.Update/Delete` и
      `links.Manager.Delete` разрешают действие с той же аутентифицированной учётной записью
      без `edit_code`. README, «Editing without edit_code (owners)», описывает это поведение;
      `tests/auth/owner_edit_flow_test.go` проверяет его сквозным запросом.
- Восстановление анонимного кода не добавлялось и не требуется выбранной моделью.

### 7. Machine-readable ответ `--disable-api` — DONE

- [x] Middleware отдаёт `503 {"message":"API is disabled","code":"api_disabled"}`.
      Код стабилен; клиентам не нужно сравнивать текст сообщения.
- [x] Выбрано нейтральное английское сообщение `API is disabled`.
- [x] В `openapi.yaml` описан 503 для всех note/link-операций (включая bulk);
      существующие 503 account-операций также описывают `api_disabled`.
      `ErrorResponse.code` опционален для совместимости остальных ошибок.
- [x] Обновлены встроенная спецификация, ogen-сервер/Go-клиент, README и обработчики CLI/MCP.
- [x] Проверены HTTP-ответы, декодирование Go-клиентом, отличие от 503 `/readyz` и
      распознавание кода в CLI/MCP независимо от текста. Интеграционные тесты проходят;
      `golangci-lint` — 0 замечаний.
- Порядок middleware остаётся отдельной задачей (Часть 1, №4): внешние обёртки могут
  ответить раньше. 503 `/readyz` сохраняет прежний смысл ошибки хранилища без `api_disabled`.

### 8. Обновление `PUT /links/{id}` — DONE

- Было: короткую ссылку нельзя было обновить; удаление и повторное создание теряли views,
  прежний edit_code и не гарантировали сохранение slug.
- [x] `links.Manager.Update` и `PUT /links/{id}` обновляют `target_url`/`ttl`/`privacy`/
      `direct_redirect`, сохраняя slug, views, edit_code, owner, created_at и burn_after_click.
      `target_url` обязателен (только http/https); остальные поля при пропуске сохраняются.
      TTL отсчитывается от обновления: `0` — немедленное истечение, `null` — снятие срока;
      `direct_redirect: null` возвращает серверный default. Privacy owner требует владельца.
- [x] Owner bypass строго для создателя (включая владельца без admin-прав); прочие вызывающие
      используют исходный edit_code. Анонимные изменения private-ссылок получают 401;
      неправильный код — 403, десять отказов включают lockout 429. `--disable-api` учтён.
- [x] SQLite/Postgres Update изменяет только разрешённые поля; `RETURNING` отдаёт фактический
      счётчик, не затирая конкурентные клики. Новый URL шифруется. Кэш сбрасывается после
      записи; общий slug-lock исключает повторное заполнение старым URL/privacy в процессе.
- [x] Обновлены OpenAPI, HTTP-адаптер (`ogen_link_update.go`), ogen-сервер/Go-клиент,
      моки, README и `/llms.txt`.
- [x] Проверка 2026-10-14: unit-тесты, SQLite-репозиторий, сквозной `TestLinkUpdateFlow`
      (реальный auth/router + сгенерированный клиент) и manager-тест с реальным шифрованием — PASS.
      PostgreSQL-тест добавлен; выполнение не подтверждено, Docker недоступен.
      Сборка всех пакетов, `go vet -mod=readonly ./...`, `git diff --check` — PASS.
      Полный lint: одно существующее замечание `exhaustruct_v5` в `negotiate.go:195`;
      в файлах реализации обновления ссылок замечаний нет.

---

## Часть 4. Из PRODUCT.md — «Куда это вести» (4 приоритетных направления, по ROI)

- [x] 1. Развязать slug и content-encryption key (Часть 3, №2: новый формат, миграция и
      управление KEK реализованы; конвертация legacy конкретной установки требует исходных slug)
- [ ] 2. Hosted sandbox — `padmark.dev` с rate-limit и 1h TTL (дублирует пункт «4» из Части 3)
- [x] 3. Реальный agent surface — `padmark-mcp` и `/llms.txt` сделаны (см. Часть 3, №1)
- [x] 4. Headless auth — `padmark-cli setup`, `POST /api/tokens`, `POST /api/invites`
      (реализовано; детали в пункте «5» Части 3). Повторная проверка 2026-10-14:
      тесты CLI/HTTP/auth/crypto и сквозной `TestAccountsFlow` на SQLite — PASS.
      Общий прогон `tests/auth` с `-tags=integration` остановился на `TestPostgresAuth`:
      Docker недоступен (`rootless Docker not found`); PostgreSQL в этой проверке не подтверждён.

---

## Часть 5. Из PRODUCT.md — постоянные ограничения (не задачи на реализацию)

- Не сворачивать Note и Link в единый Resource — разные lifecycle и payload;
  сейчас это отдельные domain-типы и менеджеры.
- Не добавлять новый syntax highlighting — уже работает (`goldmark-highlighting` + `chroma`).
- Не раздувать scope дальше без отдельного решения. Число «22 server-флага» из исходного
  PRODUCT.md не используется как актуальный подсчёт или программное ограничение.

---

## Часть 6. Что уже есть и должно сохраняться (проверено по текущему коду)

- [x] Onion-архитектура в `internal/` (domain / usecases / adapters / infra), зависимости
      только внутрь — не нарушать при доработках выше
- [x] OpenAPI spec в репозитории, ogen-генерируемые сервер и клиент как single source of truth
      для HTTP-контракта — сохранить эту практику при добавлении новых эндпоинтов (Части 2, 3)
- [x] UI передаёт `edit_code` через `sessionStorage` (`templates/index.html`), страница
      успеха читает и удаляет значение (`templates/success.html`). В `localStorage` хранится
      тема оформления, не этот код. Сохранить одноразовый показ секретов и UI-warning.
- [x] `buildNamedRoutes()` = `domain.ReservedSlugPrefixes()` как single source of truth для
      first-segment route names — обязательно обновлять при любом новом первом-сегменте роуте
- [x] Owner-scoping строгий (никаких «admin может всё») — сохранить при доработке
      headless-auth (Часть 3, п.5) и `PUT /links/{id}` (Часть 3, п.8)
- [x] `goldmark-highlighting` + `chroma` подключены в `internal/infra/render/markdown.go`,
      покрыты тестами renderer — сохранить существующую подсветку.
