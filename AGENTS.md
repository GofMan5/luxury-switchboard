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
- Трансляция в Chat Completions обязана конвертировать, а не выбрасывать: freeform `custom` tool и его replayed history (`custom_tool_call`, `custom_tool_call_output`) уходят документированной функцией, иначе модель на chat-only провайдере снова начнёт пересказывать вызовы вместо их выполнения. Переписывание пути запроса на лету (проба endpoint'а) меняет цель, но не диалект: клиенту отвечают в том, в котором он открыл соединение, поэтому upstream-запрос всегда получает собственный `*url.URL`.
- Публичный tunnel работает fail-closed: allowlisted models only, sanitized headers/body/SSE, neutral errors, no `owned_by`, provider identity, upstream fields или membership oracle.
- Логи структурированные и санитизированные. Хранят status, timing, queue/retry, token subsets и безопасные terminal details; никогда prompt, output body или credential.

## Guardrails

- Каждый ответ провайдера проходит локальную проверку перед тем, как клиент увидит байт. Slice `backend/internal/slices/guardrails` содержит правила (данные vendored из holone, MIT, атрибуция в `adapters/ruleset/LICENSE-holone.txt`), но движок написан здесь и не тянет зависимостей.
- Проверяется **финальное** тело в диалекте клиента, после всех трансляций и починок relay. Иначе payload спрячется в промежуточном диалекте, который relay ещё переписывал.
- Дельты стрима склеиваются до сопоставления. Провайдер, разбивший `curl x | sh` на три события, обязан ловиться так же, как отправивший одной строкой. Внутри одного события `data:`-строки склеиваются через `\n` (так требует W3C), поэтому один перевод строки посреди payload тоже не обход: блоки режутся по `\n\n`, а не по `\n`.
- Тело больше буферного лимита инспектируется по прочитанному префиксу, а не пропускается. Обрезанные байты нельзя отдавать, но читать их можно, и пропуск проверки здесь продавал бы тишину за padding: провайдеру хватило бы добить ответ до потолка.
- Режим по умолчанию `monitor`, не `block`. Правила совпадают с shell/network идиомой, которую честный ассистент выдаёт постоянно; блокировка по умолчанию убила бы легитимные ответы в первый день. `block` включает владелец для провайдеров, которым не доверяет.
- Tool call, которого клиент не просил, — не пограничный случай, а аномалия: запускать его нечем, значит вставил провайдер. Если запрос нечитаем, аномалия **не** выставляется (fail-open именно здесь): обвинение стоит легитимного ответа, а pattern-правила всё равно работают.
- Правила наружу не выходят никогда. UI и stdio отдают счётчики, категории и excerpt найденного, но не паттерны: отчёт не должен превращаться в инструкцию по обходу.
- Отказ уходит в туннель как обычная ошибка dispatch. Снаружи он неотличим от недоступного провайдера — ни по статусу, ни по телу, ни по заголовкам, ни по `errorCode` в истории клиента (`safeError` — allowlist, не blacklist).
- Guardrails есть в **обоих** изданиях. Публичный пользователь имеет то же право знать, что ему прислал провайдер. Это подтверждает `TestEditionAnswersOnlyItsOwnCommands`: он спрашивает `guardrails.status` у живого протокола и требует `guardrails.manage` в capabilities handshake.
- Данные vendored, значит нотис едет с ними: `LICENSE-holone.txt` попадает в оба инсталлятора как `bundle.resources` (в NSIS — рядом с exe, в deb/AppImage — `usr/lib/Luxury Switchboard/`). Правила встроены в бинарь через `go:embed`, а лицензия MIT требует нотис в каждой копии.
- Режим — persisted setting, но применяется на следующем запросе, а не после restart (`settingsService.OnApplied` → `Inspector.SetMode`, `Settings.RequiresRestart` игнорирует это поле). Новое поле настроек обязано попасть в `Normalized()`, иначе файл прошлой версии не пройдёт `Validate()` и молча сбросит всю конфигурацию пользователя.

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
- Frontend режется тем же принципом: ветки, которые должны исчезнуть из бандла, проверяют литерал `__OWNER_EDITION__` прямо на месте (`App.tsx`, `ServicesProvider`, `navigation.ts`), иначе bundler не удалит модуль — `navigation.ts` ветвился на реэкспорт и тянул в публичный бандл иконки owner-вкладок. Читаемый `OWNER_EDITION` из `app/edition.ts` используется там, где нужен только runtime-выбор.
- `pnpm backend:check` прогоняет тесты обоих изданий, и CI обязан прогонять `-tags public` тоже. Без этого `TestEditionAnswersOnlyItsOwnCommands` в автоматике проверяет только owner-ветку, а утёкшая в публичный бинарь команда уезжает незамеченной. Тест спрашивает у реального протокола, что издание умеет, и обязан оставаться правдой для обеих сборок.
- Allowlist в `src-tauri/src/sidecar.rs` один на оба издания намеренно: shell решает только, достаточно ли фрейм корректен, а существует ли команда — ответ sidecar, и публичный собран без этих handler'ов, поэтому owner-метод возвращает `method_not_found`. Разделение списка купило бы другую строку ошибки, не границу.
- `system.handshake` возвращает `edition` и capabilities этого издания. UI не должен предлагать сценарий, которого нет в ответе handshake; новая вкладка добавляет свою capability, иначе handshake врёт про то, что бинарь умеет.
- Новый owner-only сценарий добавляется сразу в оба места: за build tag в Go и за `__OWNER_EDITION__` во frontend; иначе публичная сборка получит мёртвую вкладку или живую команду.
- `pnpm build` и `pnpm build:linux` собирают оба издания: owner в `artifacts/release`, public в `artifacts/release-public`, у каждого свой `SHA256SUMS.txt`. Релиз падает, если в публичном бандле остались owner-чанки или в публичном sidecar нашлись owner-команды.
- Публичный артефакт несёт метку издания в имени: `Luxury-Switchboard-<version>-public-windows-x64-setup.exe`. Оба инсталлятора делят bundle identifier и путь установки, поэтому публичный молча заменяет owner-установку — при одинаковых именах после скачивания их не различить ничем, кроме папки. Имя owner-артефакта остаётся без метки: его называют корневой `SHA256SUMS.txt` и `package-friend.ps1`.
- Коллектор релиза забирает из `src-tauri/target/release/bundle/` только файлы, записанные текущей сборкой (по mtime). Там лежат инсталляторы всех прошлых версий и прошлых названий продукта; отбор по одному имени однажды нашёл два кандидата на `1.0.8`, а хуже был бы один — тогда релиз молча увёз бы бинарь недельной давности.
- CI поднимает и складывает **обе** папки изданий. Корень artifact'а — `artifacts/`, а не `artifacts/release`: иначе public-инсталлятор собрался бы и был выброшен, а job `collect` упал бы на `--checksums-only`, потому что тот пишет суммы для каждого издания и требует непустую папку.

## Product name

- Продукт называется **Luxury Switchboard**. Так он подписан в `tauri.conf.json` (`productName`, заголовок окна), в sidebar, в `frontend/index.html`, в описании крейта и в именах артефактов (`Luxury-Switchboard-<version>-...`).
- Внутренние идентификаторы это переименование **не** трогает и трогать нельзя: bundle identifier `one.luxuryprivate.switchboard`, Go module path, каталог данных `ProviderSwitchboard`, DPAPI/keyring entropy и заголовки `X-Switchboard-*`. Смена любого из них теряет сохранённые ключи и конфиг пользователя.
- `productName` — не косметика: NSIS кладёт по нему каталог установки (`$LOCALAPPDATA\<productName>`) и ключ удаления. Переименование не обновляет прошлую установку, а создаёт вторую рядом: два ярлыка, один каталог данных, один порт. Инструкция в `FRIEND-SETUP.md` обязана говорить снести старую запись, пока хоть у кого-то стоит сборка с прошлым именем.

## Release version

- Версии **только патчевые**: линия навсегда `1.0.x` — `1.0.7`, `1.0.8`, … `1.0.105`. Никаких minor и major (`1.1.0`, `1.3.0` — ошибка, которую уже пришлось откатывать). Любое изменение, которое уходит пользователю, поднимает патч на один.
- Версия не набирается руками. `pnpm version:set 1.0.8` (или `pnpm version:set` — инкремент патча) пишет её во все шесть мест: `src-tauri/tauri.conf.json` (источник истины для bundle и имён артефактов), `src-tauri/Cargo.toml`, `src-tauri/Cargo.lock` (пакетная строфа `switchboard-desktop`), `package.json`, `frontend/package.json` и `AppVersion` в `backend/internal/slices/system/adapters/stdio/register.go`. Список мест живёт один раз в `scripts/version.mjs`; забытый lockfile даёт дерево, которое gate считает согласованным, а `cargo --locked` отвергает.
- UI никогда не хардкодит версию. Sidebar показывает то, что вернул `system.handshake`, то есть Go `AppVersion`, который проставила сборка; новые места отображения берут её оттуда же. `App.test.tsx` держит это правдой.
- `pnpm build` и `pnpm build:linux` падают до сборки, если значения разошлись или версия не патчевая (`assertVersionsAgree` и `assertPatchOnly` в `scripts/version.mjs`). Не обходить проверку, а прогнать `pnpm version:set`.
- Новая версия попадает в commit вместе с изменениями. Корневой `SHA256SUMS.txt` содержит ровно одну строку — контрольную сумму собранного owner-инсталлятора из `artifacts/release`, её проверяет `scripts/package-friend.ps1`. Папки изданий получают свои `SHA256SUMS.txt` от сборки; чужие артефакты удаляются автоматически.
- Новая stdio-команда добавляется одновременно в Go handler и в allowlist `src-tauri/src/sidecar.rs`; расхождение ловит `TestEveryControlPlaneCommandIsAllowedByTheDesktopShell`.

