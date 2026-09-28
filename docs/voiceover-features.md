# Инвентаризация agent-voice-over — фичлист для переноса в hey-agent

Дата: 2026-09-28. Снято read-only: демон `agent-voice-over daemon` (pid 1248,
работает с 24.09) не останавливался, файлы не менялись.

## Объекты

- Репозиторий: `/home/vlad/Projects/global/agent-voice-over` (module
  `github.com/john-smith-ceo/agent-voice-over`, Go 1.26). README в
  `~/Tools/linux/agent-voice-over/README.md` указывает устаревший путь
  `~/Projects/agent-voice-over` — репозиторий переехал в `global/`.
- Бинарник: `/home/vlad/.local/bin/agent-voice-over` (ELF, stripped, ~8.6 МБ).
- Единственная внешняя зависимость по `go.mod`: `fyne.io/systray v1.12.2`
  (+ `godbus/dbus`, `x/sys` indirect). Остальное — stdlib.
- Запуск: systemd user unit `agent-voice-over.service` (enabled, active).

## Фичлист

| # | Фича | Источник (файл) | Интерфейс | Переносить в hey-agent? |
|---|---|---|---|---|
| 1 | CLI-диспетчер без cobra: `say` (дефолт), `send`, `daemon`, `tray`, `on`, `off`, `status`, `voices`, `use`, `version`/`--version` | `cmd/agent-voice-over/main.go:75-133` | `agent-voice-over <cmd>`; голый текст без команды = `say` | да |
| 2 | `send` — клиент демона: JSON-запрос по unix-сокету, текст из argv или stdin; флаги `--socket`, `--voice`, `--style`, `--speed-up 0..25`, `--normal-speed` | `main.go:135-165` | `printf '%s' "$T" \| agent-voice-over send --voice cedar` | да |
| 3 | `say` — daemon-first: шлёт в сокет, при недоступном демоне синтезирует и играет сам; `--profile` принудительно локально, `--no-play`, `--stage-chars` (деф. 400) | `main.go:193-261` | `agent-voice-over say "текст"` | да (fallback — вопрос PO) |
| 4 | `daemon` — unix-сокет сервер: слушает `$DATA/agent-voice.sock` (0600), протокол — одна строка JSON `voiceRequest{voice,instructions,normal_speed,speed_up,text}`, ответ `ok\n` / `error: …\n` | `main.go:263-344` | сокет `~/.local/share/agent-voice-over/agent-voice.sock` | да |
| 5 | Строгая очередь речи: `sync.Mutex` вокруг synth+play — фразы от разных клиентов не накладываются | `main.go:286,326-339` | внутреннее | да |
| 6 | Перечитывание конфига на каждое соединение → смена голоса (`use`, tray) действует без рестарта демона; per-request override `voice`/`instructions`/`speed_up` | `main.go:297-317` | внутреннее | да |
| 7 | Очистка Markdown перед озвучкой: вырезает code-блоки, URL, заголовки, буллеты, `**bold**`, строки-таблицы/`$ cmd`/`#!`/`sudo`; inline-code без путей/флагов оставляет | `main.go:66-72,453-483` | внутреннее, покрыто тестами `main_test.go` | да |
| 8 | Словарь произношения `lexicon.txt` (`оригинал = произношение`, построчный ReplaceAll; ошибка чтения молча игнорируется) | `main.go:475-481`; `lexicons/lexicon.txt`, `lexicon-en.txt` | `$CFG/lexicon*.txt` | да (сейчас сломан — см. ниже) |
| 9 | Разбиение на предложения и «стейджи» ≤400 символов для потокового синтеза | `main.go:485-527` | внутреннее | да |
| 10 | Движок Piper: запуск `piper --model X --length_scale Y --output_file` на каждую фразу (НЕ резидентный — модель грузится заново) | `main.go:533-569` | профиль `engine: piper`, `piper_bin`, `model` | да; резидентный piper — вопрос PO (экономия ~340 мс/фразу, план есть в `docs/cleanup-plan.md:25`) |
| 11 | Движок OpenAI TTS: `POST https://api.openai.com/v1/audio/speech`, `model` (деф. `gpt-4o-mini-tts`), `voice` (деф. `onyx`), `response_format: wav`, `speed`, опц. `instructions`; HTTP-клиент 90 с | `main.go:616-685` | профиль `engine: openai`, `model`, `voice`, `instructions`, `speed` | да |
| 12 | Разрешение ключа OpenAI: env `OPENAI_API_KEY` → `OPEN_AI_API_KEY` → файл `~/.config/agent-voice-over/.secret` (формат `KEY=value` или сырое значение) | `main.go:587-614` | env + файл 0600 | да (согласовать с keychain/env-схемой hey-agent) |
| 13 | Кэш WAV: `sha256(profile\|version\|model\|rate\|text)[:8].wav` в `$DATA/cache-go/`; попадание → мгновенное воспроизведение; вытеснения нет | `main.go:551-554,640-644` | `~/.local/share/agent-voice-over/cache-go/` | да (вопрос PO: политика очистки) |
| 14 | Воспроизведение: `ffplay -nodisp -autoexit -loglevel quiet` по умолчанию; `mpv --no-video --really-quiet` при `AGENT_VOICE_PLAYER=mpv`; блокирующий `cmd.Run()` | `main.go:687-696` | env `AGENT_VOICE_PLAYER` | да |
| 15 | `--speed-up 0..25%` на одну фразу → `RateMultiplier`: piper — делит `length_scale`, OpenAI — умножает `speed` | `main.go:446-451,563,632-636` | флаг `--speed-up`/`--normal-speed`, поле `speed_up` в сокете | да |
| 16 | Профили JSON: `name,title,version,engine,piper_bin,model,length_scale,lexicon,filter,gain_db,translit,voice,instructions,speed` | `main.go:28-43`; `profiles/*.json` | `$DATA/profiles/<name>.json` | да |
| 17 | Активный профиль — файл `$CFG/profile` (сейчас `cedar`); дефолт `jarvis-v1`; флаг включения `$CFG/enabled` | `main.go:389-397,711-715` | `use <имя>`, `on [профиль]` | да |
| 18 | Транслитерация кириллицы→латиница для англ. голосов (`translit: true`, порт `lib/translit.py`) | `translit.go`; `profiles/northern-v1.json` | поле профиля | да |
| 19 | Авто-стиль «мягкий женский» для OpenAI-голосов `coral,nova,shimmer,alloy,sage,verse,marin` | `main.go:409-411`, `tray.go:37-46` | внутреннее | вопрос PO |
| 20 | Встроенный список 13 голосов OpenAI + генерация профилей на лету в `$CFG/openai-voices/<voice>.json` (на базе `onyx-v1.json`) | `tray.go:29-35,331-359`, `main.go:427-444` | `--voice <openai-имя>` без файла профиля | да |
| 21 | ffmpeg-цепочка DSP из профиля, плейсхолдер `{gain}` ← `gain_db` (у jarvis-v1/northern-v1 — эквалайзер/компрессор/лимитер) | `filter.go`, поле `filter` профиля | внутреннее | вопрос PO (нужно только для piper-профилей) |
| 22 | Tray (fyne/systray): статус, переключатель Offline↔Модель (`jarvis-v1`↔`onyx-v1`), ввод/удаление API-ключа через **osascript** (macOS-диалоги!), чекбоксы голосов, start/stop, иконка-точка | `tray.go` | `agent-voice-over tray`, юнит `-tray.service` | вопрос PO (у hey-agent статус — tmux status-line; ключ-диалог на Linux сломан by design) |
| 23 | Управление службой Linux: `systemctl --user start/stop agent-voice-over.service` + файл `enabled` | `service_linux.go` | `on`/`off` | да (если hey-agent остаётся под systemd — сейчас он в tmux-окне) |
| 24 | Управление службой macOS: flock `daemon.lock`, `daemon.pid`, detached-spawn с логами `/tmp/agent-voice-over.daemon.{out,err}`, `pkill` fallback | `service_darwin.go` | `on`/`off` | вопрос PO (платформенный охват hey-agent) |
| 25 | Резолвер каталогов с env и legacy-fallback: `AGENT_VOICE_CONFIG` → `~/.config/agent-voice-over` → `~/.config/jarvis-voice`; `AGENT_VOICE_HOME` → `~/.local/share/agent-voice-over` → `~/.local/share/jarvis-voice` | `main.go:356-380` | env | нет (новая схема имён hey-agent), сам механизм — да |
| 26 | Скиллы Claude Code `agent-voice-over` / `agent-voice-off` («только явные вызовы, хуков нет») | `skills/*/SKILL.md`; ссылки в `~/.claude/skills/` (сейчас БИТЫЕ) | skill-файлы | переписать под hey-agent |
| 27 | `install.sh`: сборка, симлинки profiles/lexicons/skills, bootstrap `.secret` из `~/Tools/linux/openai/.env`, установка юнитов/launchd | `install.sh` | скрипт | частично (слить с install-потоком hey-agent) |
| 28 | `fetch-voices.sh`: piper engine + onnx-модели → `~/.local/share/piper` (~130-700 МБ, Linux/macOS ветки) | `fetch-voices.sh` | скрипт | да, если piper остаётся |

## Отсутствует в Go-версии (было в старом стеке jarvis / заявлено, но не сделано)

- **`hush` — обрыв звучащей фразы.** В `known`-командах нет; старый
  `bin/jarvis-voice hush` и хук `jarvis-voice-hush.py` (SIGTERM группе
  по `state/<sess>.pid`) удалены вместе со стеком. При этом правила
  агентов (`~/Agents/rules/devin.md:18`) до сих пор советуют
  `--on-record "jarvis-voice hush"` — бинарника `jarvis-voice` в
  `~/.local/bin` нет. **Вопрос PO: нужна ли команда «замолчи» в hey-agent
  (например `hey-agent hush` / сообщение в сокет), чтобы hey-agent listen
  мог обрывать речь при старте записи.**
- **`mic-busy` не читается никем.** Семантика: hey-agent
  (`internal/bridge/bridge.go:337-362`) создаёт файл `--busy-file`
  (сейчас `~/.config/jarvis-voice/mic-busy`, env `HEY_AGENT_BUSY_FILE`)
  на время записи и удаляет по окончании; `--on-record` (`HEY_AGENT_ON_RECORD`)
  — shell-команда при старте записи. Читателем был только legacy-хук
  `jarvis-voice-drain.py:129-147` (`wait_for_silence`: ждёт ≤180 с, метку
  старше 300 с считает заброшенной и снимает). **В Go- daemon проверки
  mic-busy нет → защиты от акустической петли (TTS попадает в микрофон)
  фактически нет.** Переносить: да — читать mic-busy в демоне hey-agent
  перед каждым stage и/или обрывать синтез.
- `mode brief|full` (сводка vs полный текст), `warmup` (прогрев кэша),
  посессионные профили (`$CFG/sessions/<sess>`), чтение транскрипта
  (hooks stop/drain) — из `docs/cleanup-plan.md`; хуки сознательно
  выкинуты («no hooks»), остальное не портировано.

## Как вызывают агенты

Контракт — явные вызовы бинарника, без хуков. Канон в `~/Agents/rules/`:

| Агент | Вызов |
|---|---|
| Devin | `printf '%s' "$TEXT" \| agent-voice-over send --voice cedar` |
| Codex | `… send --voice onyx` |
| Cursor | `… send --voice marin` |
| Mistral | `… send --voice fable` |
| Claude | `… send` (голос демона по умолчанию = активный профиль) |

По смыслу `send` — единственный используемый путь: текст идёт в stdin,
демон сериализует и озвучивает. Управление — `on|off|status|voices|use`
(документировано в skills). Управляющие команды вызывают `systemctl --user`
— на машине без systemd `on/off` не работают (darwin-ветка spawn'ит сама).

## Конфиги и пути для cutover

| Путь | Что | Состояние |
|---|---|---|
| `~/.local/bin/agent-voice-over` | бинарник | живой, pid 1248 |
| `~/.config/systemd/user/agent-voice-over.service` | user unit — **обычный файл** (копия), `EnvironmentFile=-%h/Projects/tools/openai/.env` | enabled+active; путь env-файла **не существует** (`~/Projects/tools` нет) → ключ берётся из `.secret` |
| `~/.config/systemd/user/agent-voice-over-tray.service` | symlink → `~/Projects/agent-voice-over/systemd/…` | **БИТЫЙ** (репо переехал в `global/`), юнит inactive |
| `~/.config/agent-voice-over/` | `enabled`, `profile` (= `cedar`), `.secret` (0600), `openai-voices/{cedar,fable,onyx}.json` | активный конфиг |
| `~/.config/agent-voice-over/lexicon{,-en}.txt` | symlink → `~/Projects/agent-voice-over/lexicons/…` | **БИТЫЕ** — словарь молча не применяется (`os.ReadFile` ошибку глотает) |
| `~/.local/share/agent-voice-over/` | `agent-voice.sock` (0600), `cache-go/`, `profiles` → `~/Projects/global/agent-voice-over/profiles` | рабочий; profiles-ссылка живая |
| `~/.config/jarvis-voice/` | пустой; сюда hey-agent пишет `mic-busy` на время записи | legacy-имя, используется флагом `--busy-file` |
| `~/.local/share/piper/` | `engine/piper` + `voices/*.onnx` (11 моделей) | для piper-профилей |
| `~/Tools/linux/openai/.env` | env-файл юнита (в repo-версии); имена переменных: `OPENAI_API_KEY`, `OPEN_AI_API_KEY` | существует; установленный юнит указывает на другой, несуществующий путь |
| `~/.claude/skills/agent-voice-over{,-off}` | symlinks → `~/Projects/agent-voice-over/skills/…` | **БИТЫЕ** |
| env интерфейса | `AGENT_VOICE_CONFIG`, `AGENT_VOICE_HOME`, `AGENT_VOICE_PLAYER`, `OPENAI_API_KEY`, `OPEN_AI_API_KEY`, `OPENAI_ACCOUNT_LABEL` | — |
| env со стороны hey-agent | `HEY_AGENT_BUSY_FILE`, `HEY_AGENT_ON_RECORD` | bridge.go |

## Открытые вопросы (к PO)

1. Поглощение или coexistence: hey-agent забирает `daemon`+`send` целиком
   (один бинарь, свой сокет) или агенты продолжают звать отдельный
   `agent-voice-over`?
2. `hush`: делать ли обрыв речи по `--on-record`/команде — сейчас контракт
   заявлен в правилах, но реализации нет.
3. mic-busy: переносить чтение метки в TTS-движок hey-agent (ждать/снимать
   stale >300 с, как drain.py) — иначе петля «голос → микрофон».
4. Судьба piper-стека (ruslan/northern, транслит, ffmpeg-DSP) при переходе
   на OpenAI-голоса; резидентный piper ради ~340 мс.
5. Тray vs tmux status-line hey-agent: нужна ли GTK/systray-иконка.
6. systemd user unit для hey-agent-voice (как у a-v-o) или остаёмся на
   tmux-окне `hey-agent-voice`.
7. Починить или закрыть при переносе: битые symlinks лексиконов/tray/skills
   и расхождение `EnvironmentFile` (repo vs installed unit).
8. Имя busy-файла/каталога состояния при cutover: `jarvis-voice` →
   что-то под `~/.config/hey-agent/`?
