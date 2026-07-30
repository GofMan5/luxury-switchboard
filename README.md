# Provider Switchboard

Локальное OpenAI/Anthropic-реле с серым mouse-first TUI. Клиент всегда использует один адрес:

- OpenAI / Codex: `http://127.0.0.1:8798/v1`;
- Anthropic: `http://127.0.0.1:8798`.

## Запуск

```powershell
python -m pip install -r requirements.txt
python main.py
python main.py --start-tunnel
```

Реле слушает только `127.0.0.1`. Режимы без TUI:

```powershell
python main.py --plain
python main.py --headless --port 18798 --provider echo
```

Фоновый headless-запуск:

```powershell
$relay = Start-Process python -ArgumentList @("main.py", "--headless", "--port", "18798", "--provider", "echo") -WorkingDirectory $PWD -WindowStyle Hidden -PassThru
Stop-Process -Id $relay.Id
```

## TUI

- `Dashboard` — live-запросы, модель, фактический/effective RPM, очередь, retries, context, tok/s, токены и latency. Цветной статус: зелёный — готово, синий — выполняется, жёлтый — очередь/retry, красный — ошибка, серый — отменено. Двойной клик или `Enter` по строке открывает безопасные детали с финальным HTTP-статусом (`200 OK`, `403 Forbidden`, `504 Gateway Timeout` и т. п.). Последние 100 завершённых запросов восстанавливаются из SQLite после перезапуска.
- `Providers` — выбор, добавление, редактирование и удаление провайдеров; список моделей; несколько ключей в порядке приоритета. `Higher`/`Lower` меняют приоритет сразу и сохраняют его; системный Env Lite закреплён первым.
- `Stats` — явные кнопки `24h`, `48h`, `72h`, `All` и агрегаты без хранения prompt/response body.
- `Tunnel` — запуск управляемого SSH-туннеля к VPS в один клик, allowlist публичных моделей, конечная проверка доступности выбранных или всех моделей, копирование URL/ключа и ротация ключа.
- `Shared` — синхронизированный список общих туннелей, где собственный подписан `Ваш коннект`; pause/resume/stop работают по ревизии без передачи внутренних ID.

Для ключа задаются RPM и необязательный HTTP proxy вида `http://user:pass@host:port`. Пустой proxy у нового ключа означает прямое подключение. Для выбранного ключа пустое поле сохраняет текущий proxy, слово `direct` отключает его. Credentials замаскированы и не попадают в таблицы, историю или метрики.

Интерфейс адаптируется к окну: на `80×24` первыми остаются видны статус, модель, tok/s, context и текущий RPM; редактор ключа перестраивается в два ряда, остальные секции прокручиваются вертикально. Широкие технические поля таблиц доступны горизонтальной прокруткой. Редактируемый RPM не перезаписывается live-обновлением, а effective RPM учитывает одновременно лимит провайдера и сумму лимитов ключей.

`Tok/s` фиксируется по финальному usage за время успешной upstream-попытки: от полной отправки запроса до terminal usage. Очередь и предыдущие retry не входят, а TTFT/reasoning входят — поэтому буферизированный ответ больше не показывает тысячи фиктивных tok/s. Пока OpenAI/Anthropic не прислал terminal usage, показывается `—`: точный live tok/s без tokenizer неизвестен.

## SSH VPS tunnel

Для запуска нужны системный Windows OpenSSH (`ssh.exe`) и отдельный publisher-ключ `%LOCALAPPDATA%\ProviderSwitchboard\ssh\model-tunnel_ed25519`. Controller всегда подключается ограниченным пользователем `model-tunnel`; root-ключ и SSH agent не используются. ED25519 host key VPS закреплён в приложении: рядом с publisher-ключом атомарно создаётся отдельный `model-tunnel_known_hosts`, а пользовательские SSH config/agent и глобальные trust-файлы игнорируются.

Выданный publisher profile имеет строгий формат `v1.<port>.<48 lowercase hex>`. Из него одновременно выводятся reverse-port и персональный адрес `https://luxuryprivate.duckdns.org/model-tunnel/<slug>/v1`, поэтому чужой slug нельзя подставить к своему порту. Profile хранится в DPAPI-конфиге и меняется только при остановленном туннеле.

Во вкладке `Tunnel` нужно обновить каталог, выбрать модели и нажать `Start`. `Online` и копирование адреса доступны только после authenticated HTTPS readiness-запроса к собственному synthetic `/v1/models`. При смене активного провайдера туннель безопасно останавливается, а allowlist очищается, чтобы модели предыдущего маршрута не попали в новый. Если текущая конфигурация временно непригодна для публикации, TUI показывает `Paused`, а не ложный `Online`. Per-IP RPM применяется отдельной FIFO-очередью для каждого адреса; `0` означает unlimited, а превышение лимита не отдаёт клиенту `429`.

`Test selected` и `Test all` выполняют короткую реальную генерацию с жёстким таймаутом 10 секунд и показывают локальные состояния `Testing`, `Available`, `Unavailable` или `Timeout`; ответы, ключи и данные провайдера не сохраняются и не публикуются.

Смена активного провайдера останавливает публичный туннель и очищает его allowlist: это не даёт случайно оставить старый каталог моделей у нового upstream. После переключения достаточно снова открыть `Tunnel`, выбрать актуальные модели и нажать `Start`.

Publisher identity, publisher profile и публичный API key — разные сущности. Публичный ключ генерируется локально, принимается как `Authorization: Bearer` или `x-api-key`; `Rotate key` сразу отзывает предыдущий. Ни он, ни publisher-ключ не передаются провайдеру.

Каждому следующему владельцу выдаются отдельные Ed25519 key, port и slug. Поэтому два реле не конфликтуют; повторный запуск одного profile на двух машинах fail-closed завершится ошибкой занятого reverse-port.

Публичная граница не является passthrough/reverse proxy:

- `GET /v1/models` всегда synthetic и содержит только выбранные model ID, без запроса к upstream;
- inference принимается только через exact `POST /v1/responses`, `/v1/chat/completions`, `/v1/completions`, `/v1/messages`, `/v1/images/generations` и `/v1/images/edits`; image edits принимают JSON references и `multipart/form-data`, остальные methods, paths, query и trailing slash отклоняются;
- model должна точно входить в allowlist; входной public key никогда не передаётся дальше, а provider/upstream, внутренние ключи и proxy не попадают в ответ;
- active provider с `passthrough` auth публиковать запрещено: туннель fail-closed отклонит запрос;
- JSON и весь SSE-ответ целиком буферизуются и проверяются до public commit; unsafe/malformed данные не проходят. Локальное реле на `8798` не коммитит `/v1/responses` до `response.completed` и бесшовно повторяет оборванную попытку; non-stream ответы проверяются до commit, а публичный Tunnel отдаёт SSE только после полной проверки ради fail-closed анонимности.

Выбранные model ID и содержимое запроса/ответа являются полезной нагрузкой публичного API и проходят через VPS, который терминирует TLS. Скрываются и не передаются именно настроенные provider name/id/URL, внутренние ключи и proxy credentials.

## Lite → Pro

`FREEMODEL_API_KEY` становится первым Lite-ключом EchoGate: `30 RPM`, прямой IP. Общий Pro-ключ добавляется вторым через `Providers`, обычно с `120 RPM` и proxy. Планировщик сначала использует Lite, затем Pro, затем держит запросы в бессрочной FIFO-очереди.

Если Lite отвечает `404` с точным сообщением `Model '<model>' is not available on your plan`, только эта модель на этом ключе пропускается 5 минут. После паузы следующий запрос снова пробует Lite. Остальные модели продолжают использовать Lite. Все upstream `4xx/5xx`, transport errors и timeouts повторяются до успеха или остановки клиента/реле; `429` и `504` до клиента не коммитятся. Ошибка конкретного запроса (`400/404/409/413/422`) не замораживает ключ и не ставит в очередь другие модели.

## EchoGate cache

У существующих валидных Anthropic breakpoint-блоков
`cache_control: {"type": "ephemeral"}` TTL меняется на `1h`. Невалидный top-level `cache_control` для Responses не добавляется; Local-запросы не меняются.

## FREEMODEL_API_KEY

Сохранить ключ в User Environment Windows достаточно один раз:

```powershell
[Environment]::SetEnvironmentVariable("FREEMODEL_API_KEY", "<key>", "User")
```

Реле читает значение и из текущего процесса, и напрямую из `HKCU\Environment`, поэтому перезапуск Codex через `$env:FREEMODEL_API_KEY=...; codex` больше не нужен. Сам env Lite-ключ не копируется в конфиг.

Провайдеры, выбранный active provider, RPM, порядок дополнительных ключей и proxy сохраняются после каждого изменения в `%LOCALAPPDATA%\ProviderSwitchboard\config.v1.dpapi`. Файл целиком зашифрован Windows DPAPI для текущего пользователя и записывается атомарно; ключи и proxy credentials не попадают в логи. После ошибки записи изменение остаётся рабочим в текущей сессии, а TUI показывает `settings not saved`.

Санитизированная история хранится в `%LOCALAPPDATA%\ProviderSwitchboard\history.db`: SQLite WAL, индексы по времени/provider и безопасная миграция схемы. Сохраняются status/state, latency/queue, input/context/output/cached/reasoning/total tokens, tok/s и cache TTL; prompt, response body, API keys и proxy credentials не записываются. Если запись БД перестала работать, TUI явно показывает `history not recording`.

## Проверка

```powershell
python -m py_compile main.py relay_config.py relay_history.py relay_http.py relay_runtime.py relay_tunnel.py relay_ui.py deploy/tunnel_hub.py test_relay.py test_hub.py test_shared_control.py test_tunnel_privacy.py test_ui_layout.py
python -m unittest -v
```
