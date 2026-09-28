# hey-agent: контракт v2 — единый voice-daemon

Статус: драфт контракта по решениям PO от 28.09.2026 (JOH-348 v2).
Заменяет прежние ограничения: «без daemon» и «no tray / no GUI» отменены
решением PO — помечены в разделе «Изменения решений».

## 1. Модель

Один пользовательский сервис `hey-agent` — единственный владелец аудио-тракта:

- **Ввод**: хоткей → запись микрофона (ffmpeg) → OpenAI-compatible
  транскрибация → активная tmux-панель.
- **Вывод**: TTS-озвучка (`speak`/`hush`), перенесённая из agent-voice-over.
- **Индикация**: tmux status-right (in/out-индикаторы) + иконка в трее Linux.

Daemon работает foreground-процессом под systemd user unit
(`hey-agent.service`) либо вручную `hey-agent daemon`. Скрытое tmux-окно
`hey-agent-voice` больше не используется — надзор переехал в systemd.

## 2. Управляющий интерфейс (unix socket)

Daemon слушает `$XDG_RUNTIME_DIR/hey-agent.sock`
(fallback: `/tmp/hey-agent-$UID.sock`). Протокол — по одной JSON-строке на
запрос и на ответ (newline-delimited JSON):

```text
→ {"cmd":"status"}                    {"state":"idle","target":"%7",...}
→ {"cmd":"bind"}                      {"ok":true}      — захватить хоткей
→ {"cmd":"unbind"}                    {"ok":true}      — отпустить хоткей
→ {"cmd":"speak","text":"…"}         {"ok":true}      — озвучить (очередь)
→ {"cmd":"hush"}                      {"ok":true}      — оборвать озвучку
→ {"cmd":"config","submit":true,...}  {"ok":true}
→ {"cmd":"stop"}                      {"ok":true}      — мягкое завершение
```

CLI-команды `hey-agent status|bind|unbind|speak|hush|config|stop` — тонкие
клиенты этого сокета: ими пользуются и человек, и скрипты, и tray-меню.
`hey-agent listen` остаётся совместимой командой: поднимает daemon (или
присоединяется к нему) и выбирает цель по активной панели.

Любая команда к сокету отвечает за ≤2 с или ошибкой `{"ok":false,"error":…}`.

Совместимость с вызовами агентов: `agent-voice-over send` сегодня шлёт
JSON-строку `voiceRequest{voice,instructions,normal_speed,speed_up,text}`
и ждёт `ok`/`error: …`. При cutover команда hey-agent читает тот же
контракт (`speak` принимает поля voiceRequest поверх своих), чтобы правила
`~/Agents/rules/*.md` и скиллы продолжили работать после переключения
симлинка — без переписывания вызовов.

## 3. Целевая панель — «актуальная», fail-closed

Цель каждой записи вычисляется заново и проверяется дважды:

1. **До записи** — при старте `Recording` резолвер находит активную панель:
   среди attached tmux-клиентов берётся клиент с максимальным
   `client_activity`, затем его активная сессия → окно → панель
   (`tmux list-clients -F '#{client_activity} #{client_session}'`,
   `display-message -p '#{pane_id}'` в активном окне).
2. **Перед отправкой** — после транскрибации резолвер запускается снова и
   сравнивает результат с сохранённой целью.

Матрица fail-closed (текст не вставляется и не отправляется, статус = error):

| Ситуация | Поведение |
|---|---|
| Нет attached-клиента / панели | отказ записи или дроп доставки |
| Цель сменилась во время записи | дроп доставки, статус error |
| Неоднозначность (два клиента с равной activity) | ошибка, видимый сигнал |
| Панель закрыта за время записи | дроп, error |
| Провайдер ошибся/timeout/пустой ответ | error, временный wav удалён |

`--target` остаётся явным override: заданная панель проверяется на
существование, но не на «активность». `--submit` — по-прежнему opt-in.

## 4. Машина состояний аудио

```text
Idle → Recording → Transcribing → Delivering → Idle
Idle → Speaking → Idle
Recording ⊥ Speaking — взаимоисключающие: запись глушит вывод и наоборот.
```

- `speak` во время `Recording`/`Transcribing` — задерживается до `Idle`
  (очередь глубиной 1: новый speak заменяет ожидающий).
- хоткей во время `Speaking` — обрывает вывод и начинает запись.
- `hush` обрывает `Speaking`.
- `Escape` во время `Recording` — `Idle` без транскрибации и без доставки.
- busy-file/on-record-совместимость: `HEY_AGENT_BUSY_FILE` по-прежнему
  трогается в `Recording` (для сторонних watcher'ов), **и** daemon сам
  проверяет метку перед стартом `Speaking`: свежая метка — отложить фразу,
  метка старше 300 с — считать заброшенной и снять (семантика
  legacy-`jarvis-voice-drain.py`). Сейчас mic-busy никем не читается —
  защиты от петли фактически нет; это место закрывает дыру.

## 5. Провайдеры

- Транскрибация: текущий OpenAI-compatible контракт без изменений
  (`HEY_AGENT_BASE_URL`, `HEY_AGENT_MODEL`, `HEY_AGENT_API_KEY`,
  `HEY_AGENT_ENV_FILE`; multipart `file`+`model`; timeout 60 с; ошибки без
  секретов и без тела ответа).
- TTS (перенос agent-voice-over, `internal/voiceout`): два движка —
  OpenAI `POST /v1/audio/speech` (`HEY_AGENT_TTS_*` env + общий
  `HEY_AGENT_API_KEY`; дефолт `gpt-4o-mini-tts`/`onyx`) и локальный Piper
  (`piper --model --output_file` на фразу, модели из
  `~/.local/share/piper/`). Профили JSON (`engine`, `voice`,
  `instructions`, `speed`, `length_scale`, `filter`, `gain_db`,
  `translit`, `lexicon`) — переносятся как есть; активный профиль —
  `~/.config/hey-agent/profile` (миграция `cedar`). Пайплайн перед
  синтезом: очистка Markdown, словарь произношения, разбиение на фразы
  ≤400 символов; кэш WAV по sha256. Проигрывание `ffplay -nodisp
  -autoexit` (`HEY_AGENT_PLAYER` → `mpv`); `hush` убивает процесс
  проигрывателя — команды `hush` в Go-версии voiceover нет, это новая
  функция, заявленная в правилах агентов ещё со старого стека.

## 6. Индикация

**tmux status-right** (только в сессиях, где есть привязанная панель):
раздельные сегменты `in:` и `out:` по реальной готовности daemon'а —
`in:idle|rec…|transcribe…|err`, `out:idle|speak…|err`. Park/restore
пользовательского `status-right` — текущая механика `@hey-agent-status-*`
сохраняется. «Daemon жив» отличать от «готов» — состояние приходит из
socket `status`, а не из факта процесса.

**Tray (Linux)**: StatusNotifierItem по session D-Bus через
`godbus/dbus` — без cgo/GTK. Первичное меню (новая логика):

```text
hey-agent — idle
───────────────
Bind hotkey / Unbind hotkey
✓ Submit automatically
✓ Voice output
───────────────
Hush
Quit hey-agent
```

Иконка по состоянию FSM: idle / rec / transcribe / speaking / error.
Нет tray-host (headless, Watcher отсутствует) — daemon работает без иконки,
ошибки только в лог.

## 7. Секреты и безопасность

- Канон секрета — `~/Tools/linux/hey-agent/.env` (0600); `HEY_AGENT_ENV_FILE`
  переопределяет. Провайдер-ключи не логируются и не попадают в ответы.
  ⚠ Расхождение кода: дефолтный путь записи в `setup-key` сейчас
  `~/Projects/tools/hey-agent/.env` (`main.go:314-318`) — вопрос PO #4 ниже.
- Сокет — 0600, только uid владельца; командная полезная нагрузка — текст
  для озвучки/конфига, никаких путей к файлам и shell.
- Временные wav/mp3 удаляются при любом исходе (уже есть в record/).
- Daemon не читает чужие панели: он пишет только в резолвленную цель.

## 8. Миграция «2 бинарника → 1»

1. Dev-ветка `vlad2141950/joh-348-…` в `~/Projects/global/hey-agent-dev`;
   сборка в `dev-bin/hey-agent`.
2. Весь перенос фич agent-voice-over — внутри `internal/voiceout`;
   работающий `agent-voice-over` (pid 1248) не трогаем до cutover.
3. На cutover (только по решению PO): пересобрать `bin/hey-agent`,
   перевесить `~/.local/bin/hey-agent`, выключить `agent-voice-over` daemon,
   пройти `docs/removal-list.md`.
4. Откат: восстановить симлинк и запустить прежний слушатель по snapshot
   `docs/snapshot-2026-09-28.md`.

## 9. Тестовый контракт

- Unit: socket-протокол, резолвер (мок tmux-выводов), FSM-переходы,
  fail-closed матрица, tray-маппинг состояние→иконка/меню, dotenv-пермишены.
- Интеграция на стенде (JOH-354): отдельный `tmux -L heydev` socket, Xvfb
  `:99`, httptest-мок провайдера; кейсы Ф2.3 целиком.
- `go test ./...` и `go vet ./...` — обязательное зелёное условие.

## 10. Изменения решений PO (журнал)

| Правило | Было | Стало (28.09.2026) |
|---|---|---|
| Процесс | foreground-listener в скрытом tmux-окне | пользовательский daemon + unix socket |
| Voice output | отдельная утилита agent-voice-over | внутри hey-agent (`speak`/`hush`) |
| Tray | out of scope v1 | StatusNotifierItem по D-Bus |
| Цель | явный `--target` / текущая панель | «актуальная» панель + fail-closed |

## Открытые вопросы к PO

1. systemd user unit — создаём файл-шаблон в `packaging/`, но включение
   (enable) — только на cutover. Так и задумано?
2. Состав tray-меню финальный? Сейчас черновик из JOH-348.
3. TTS-провайдер: тот же OpenAI-совместимый ключ, или отдельный?
4. **Путь `.env`**: живой канон `~/Tools/linux/hey-agent/.env`, но код
   `setup-key` пишет в `~/Projects/tools/hey-agent/.env` (отсюда
   «API key missing» у dev-сборки). Варианты: (а) сменить дефолт на
   `~/Tools/...`, (б) читать оба (Tools → fallback Projects), (в) только
   `HEY_AGENT_ENV_FILE`. Какой канон?
5. Совместимость с `agent-voice-over send`: сохранять ли формат
   `voiceRequest{voice,instructions,normal_speed,speed_up,text}` как
   клиентский контракт `speak` (для `~/Agents/rules/*.md` без правок)?
6. Piper-движок: переносить полностью (модели, транслит, DSP) или
   в v2 только OpenAI-TTS, piper — отдельной задачей? Сейчас в спеке оба.
7. Кэш TTS-WAV и профили голосов: мигрировать каталоги
   `~/.config/agent-voice-over/` → `~/.config/hey-agent/` на cutover
   или читать старые как read-only источник?
8. Битые симлинки lexicon/tray/skills в agent-voice-over — закрыть
   в cutover вместе с выводом из эксплуатации (не чинить сейчас)?
