# Engineering contract

## Product bar

- Весь код обязан быть качественным, производительным, понятным и пригодным для production. Никакой халтуры, временных заглушек и скрытых нерабочих контролов.
- Интерфейс обязан быть красивым, спокойным, визуально цельным, удобным мышью и клавиатурой, адаптивным от компактного окна до большого экрана.
- Пользовательский сценарий важнее внутренней демонстрации технологий: меньше действий, ясные названия, предсказуемые состояния, быстрый feedback.
- Секреты, ключи, proxy credentials, SSH identity и тела запросов не попадают в UI, логи, crash reports, публичный tunnel или git. Доверенный owner-only экран Providers может показывать настроенный alias/endpoint для управления, но публичный tunnel, telemetry, client UI и shared control никогда не получают provider metadata или upstream URL; credential после записи не отражается обратно даже владельцу.

## Architecture: no monoliths

- Никаких монолитов, god-object, god-store, глобального mutable state, файлов-свалок и классов/пакетов, которые одновременно владеют несколькими независимыми сценариями.
- Искусственного ограничения по числу строк нет. Монолит определяется смешением ответственностей, высокой связностью, fan-in/fan-out и невозможностью независимо тестировать сценарий, а не длиной файла. Не дробить цельный код только ради метрики строк.
- Frontend и backend строятся независимо по hexagonal architecture и vertical slices.
- Каждый пользовательский сценарий содержит собственные domain/application/ports/adapters. UI, Tauri, HTTP, stdio, SQLite, SSH и файловая система являются внешними адаптерами.
- Зависимости направлены внутрь: adapters -> application -> domain. Domain не импортирует framework, transport, storage или UI.
- Связь между slices только через маленькие типизированные ports/contracts или явно определённые domain events. Запрещены неявные циклические зависимости и доступ к внутренностям соседнего slice.
- Общий код выносится только после реального повторения. Не создавать speculative abstraction, универсальный framework внутри проекта или wrapper без самостоятельной ответственности.
- Composition root только собирает зависимости и lifecycle; бизнес-логика в нём запрещена.

## Target runtime

- Backend и relay реализуются на Go. Он владеет routing, provider/key pools, RPM queues, retries, streaming, persistence, tunnel control, privacy и telemetry.
- Tauri v2 — тонкая desktop-оболочка. Rust-код содержит только обязательный lifecycle/sidecar bridge и не дублирует Go domain logic.
- Go sidecar общается с Tauri через версионированный stdio protocol. `stdout` зарезервирован только под framed protocol; диагностические логи идут в `stderr`.
- Каждая команда имеет correlation id, typed payload/result, terminal outcome и cancellation. Потоки обязаны поддерживать backpressure, bounded buffers и корректное завершение при shutdown/restart.
- Transport contracts валидируются на обеих сторонах. Неизвестная версия, malformed frame и oversized payload завершаются контролируемой ошибкой без зависания и утечки данных.

## Fast, deliberate delivery

- Не выполнять тупые действия, которые замедляют обновления без измеримой пользы: бессмысленные полные rebuild, повтор одинаковых тестов, последовательный запуск независимых проверок, лишние генераторы, ручное копирование boilerplate и ненужные зависимости запрещены.
- Сначала запускать минимальный targeted test/benchmark затронутого slice. Широкую матрицу запускать один раз на стабильном milestone или когда изменена публичная граница, schema, dependency, security boundary либо соседний тест упал.
- Независимые проверки выполнять параллельно. Использовать incremental build/cache и не инвалидировать их без причины.
- Оптимизация подтверждается профилем, benchmark или измерением latency/memory/allocations. Не усложнять код ради предполагаемой производительности.
- Hot paths не блокируют UI: никакого sync I/O, unbounded queue, busy loop, polling без backoff или полного rerender больших списков.
- Обновление должно быть атомарным и откатываемым. Старый рабочий Python relay хранится в `old-backup/python-relay` и не изменяется при разработке новой версии.

## Configuration-first product

- Минимум operational hardcode. Listener port, provider profile/dialect, endpoint, auth scheme/header, RPM, queue, timeouts, retry/heartbeat, cache, persistence/retention, routes and tunnel limits имеют валидируемые defaults и настраиваются через понятный Settings/UI contract.
- Hardcoded остаются только security invariants и абсолютные safety caps: loopback listener, remote HTTPS, bounded memory/frame/body, forbidden headers, secret redaction и fail-closed tunnel privacy. UI не позволяет ослабить эти границы до небезопасного состояния.
- Settings — отдельный vertical slice с typed schema, versioned migration, atomic encrypted persistence и preview/validation до commit. Никаких `map[string]any` или разбросанных env reads внутри domain/application.
- Любой OpenAI/Anthropic-compatible provider подключается typed profile: dialect, base URL, discovery/health paths, auth mode/header, cache/retry/timeouts and keys. Provider-specific workaround живёт в adapter/profile, а не в общей relay domain logic.
- Relay routes и public Tunnel routes независимы. Tunnel публикует только owner-defined aliases; raw upstream model ID, provider ID/name/URL, auth header и `owned_by` запрещены на public boundary.

## Frontend quality

- Frontend организован по feature slices; app shell только композирует navigation, layout и lifecycle.
- Состояние локально сценарию. Частые live updates не должны перерисовывать всё приложение; использовать selectors, batching, virtualization и bounded history там, где это измеримо нужно.
- Дизайн строится из единой системы tokens: neutral grey palette, typography, spacing, density, borders, radii, semantic status colors, focus, disabled и motion states.
- Все вкладки и модалки проверяются минимум в compact, standard и wide layouts. Никаких обрезанных кнопок, наложений, горизонтального overflow основных сценариев и нечитаемых таблиц.
- Все интерактивные элементы имеют hover/focus/pressed/disabled/loading/error states, понятную подпись и доступную keyboard navigation. Двойной клик не может быть единственным способом открыть важное действие.
- Анимация объясняет изменение состояния и уважает `prefers-reduced-motion`; декоративный шум, glow, gradient abuse, псевдо-AI эстетика и случайные карточки запрещены.

## Backend reliability and privacy

- Retry policy классифицирует transport, 429, 5xx, permanent request errors и terminal stream outcomes. Нельзя бесконечно повторять заведомо permanent ошибку или выдавать оборванный stream без terminal event.
- Очереди RPM справедливые, отменяемые и наблюдаемые; неограниченное ожидание не означает неограниченное потребление памяти.
- При смене provider все старые запросы получают cancellation и освобождают sockets/leases/queue slots.
- Публичный tunnel работает fail-closed: allowlisted models only, sanitized headers/body/SSE, neutral errors, no `owned_by`, provider identity, upstream fields или membership oracle.
- Логи структурированные и санитизированные. Хранят status, timing, queue/retry, token subsets и безопасные terminal details; никогда prompt, output body или credential.

## Definition of done

- Реальный основной сценарий работает, а не только компилируется.
- Затронутый slice имеет domain/unit tests и adapter/integration coverage; критичные stdio, relay streaming, retry, cancellation, persistence и privacy boundaries имеют end-to-end tests.
- Go проходит format, vet, tests и race checks для конкурентного кода. Frontend проходит typecheck, lint, tests и production build. Tauri bundle собирается из проверенных sidecar bytes.
- UI проверен кликами и скриншотами на заявленных размерах; визуальный концепт и финальный render сравнены напрямую.
- Live relay gate включает sequential + parallel large-context запросы, long reasoning/SSE terminal completion, cancellation/provider switch и image path на соседнем loopback-порту.
- Перед commit проверяются diff, секреты, артефакты, package manifest и отсутствие оставшихся test listeners/processes.

## Editions

- Продукт собирается в двух изданиях из одного дерева: `owner` (полное) и `public` (для обычных пользователей). Публичное издание не содержит public tunnel, tunnel clients и shared control.
- Вырезание обязано быть физическим, а не косметическим. Go-код владельца живёт за build tag `public` (`edition_owner.go` / `edition_public.go`), поэтому в публичном бинаре нет ни handler'ов, ни SSH-publisher, ни hub-клиента: запуск sidecar вручную не открывает эти команды.
- Frontend режется тем же принципом: ветки, которые должны исчезнуть из бандла, проверяют литерал `__OWNER_EDITION__` прямо на месте (`App.tsx`, `ServicesProvider`), иначе bundler не удалит модуль. Читаемый `OWNER_EDITION` из `app/edition.ts` используется там, где нужен только runtime-выбор.
- `system.handshake` возвращает `edition` и capabilities этого издания. UI не должен предлагать сценарий, которого нет в ответе handshake.
- Новый owner-only сценарий добавляется сразу в оба места: за build tag в Go и за `__OWNER_EDITION__` во frontend; иначе публичная сборка получит мёртвую вкладку или живую команду.
- `pnpm build` и `pnpm build:linux` собирают оба издания: owner в `artifacts/release`, public в `artifacts/release-public`, у каждого свой `SHA256SUMS.txt`. Релиз падает, если в публичном бандле остались owner-чанки или в публичном sidecar нашлись owner-команды.
- `pnpm backend:check` прогоняет тесты обоих изданий. `TestEditionAnswersOnlyItsOwnCommands` спрашивает у реального протокола, что издание умеет, и обязан оставаться правдой для обеих сборок.

## Release version

- Любое изменение, которое уходит пользователю, поднимает версию продукта. Патч — только фиксы, minor — новые сценарии или изменённый контракт клиента.
- Версия живёт ровно в пяти местах и обязана совпадать: `src-tauri/tauri.conf.json` (источник для bundle и имён артефактов), `src-tauri/Cargo.toml`, `package.json`, `frontend/package.json` и `AppVersion` в `backend/internal/slices/system/adapters/stdio/register.go`.
- UI никогда не хардкодит версию. Sidebar показывает то, что вернул `system.handshake`, то есть Go `AppVersion`; новые места отображения берут её оттуда же.
- `pnpm build` и `pnpm build:linux` падают до сборки, если пять значений разошлись (`assertVersionsAgree` в `scripts/build-release.mjs`). Не обходить проверку, а выравнивать версии.
- Новая версия попадает в commit вместе с изменениями. Корневой `SHA256SUMS.txt` содержит ровно одну строку — контрольную сумму собранного owner-инсталлятора из `artifacts/release`, её проверяет `scripts/package-friend.ps1`. Папки изданий получают свои `SHA256SUMS.txt` от сборки; чужие артефакты удаляются автоматически.
- Новая stdio-команда добавляется одновременно в Go handler и в allowlist `src-tauri/src/sidecar.rs`; расхождение ловит `TestEveryControlPlaneCommandIsAllowedByTheDesktopShell`.

