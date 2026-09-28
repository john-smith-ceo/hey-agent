#!/usr/bin/env bash
# stand-up.sh — поднимает изолированный тестовый стенд hey-agent.
#
# Что входит в стенд и зачем:
#   1. Отдельный tmux-сервер на сокете `heydev` (НЕ дефолтный) с сессией `stand`:
#      окна `a` и `b` — «приёмники»: всё, что hey-agent вставит в панель,
#      пишется в файл-маркер, так доставку видно глазами и grep'ом;
#      окно `ctl` — обычный shell, из него запускают hey-agent.
#   2. Xvfb на дисплее :99 — свой X-сервер, чтобы хоткей (XRecord) слушался
#      не на реальном рабочем столе. Если Xvfb не установлен — стенд поднимется
#      в режиме «только tmux+mock», DISPLAY внутри стенда всё равно :99,
#      поэтому тестовый процесс не сможет случайно уйти на ваш :0.
#   3. mock-provider.sh на 127.0.0.1:18923 — локальная замена OpenAI-compatible
#      API: GET .../models и POST .../audio/transcriptions.
#
# Изоляция от хозяйской системы:
#   - tmux зовём ТОЛЬКО как `tmux -L heydev` — сессии и сокет пользователя не
#     затрагиваются;
#   - hey-agent внутри панелей видит DISPLAY=:99 и HEY_AGENT_ENV_FILE, лежащий
#     в каталоге стенда, — реальный ~/.env и дисплей :0 ему недоступны;
#   - все pid-файлы, логи и маркеры — в ~/.cache/hey-agent-stand/.
#
# Скрипт идемпотентен: повторный запуск пропускает уже живые части.

set -euo pipefail

# --- Константы стенда --------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEV_BIN="$REPO_DIR/dev-bin/hey-agent"

TMUX_SOCKET="heydev"            # имя сокета: tmux -L heydev
SESSION="stand"                 # имя сессии внутри этого сервера
STAND_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/hey-agent-stand"

XVFB_DISPLAY=":99"              # X-дисплей стенда
XVFB_SCREEN="1280x800x24"

MOCK_HOST="127.0.0.1"
MOCK_PORT="18923"
MOCK_MODEL="stand-mock"
MOCK_URL="http://$MOCK_HOST:$MOCK_PORT"
export MOCK_MODEL               # чтобы mock-provider.sh увидел её в окружении

# --- Мелкие помощники --------------------------------------------------------

say()  { printf '%s\n' "$*"; }
warn() { printf 'ВНИМАНИЕ: %s\n' "$*" >&2; }
die()  { printf 'ОШИБКА: %s\n' "$*" >&2; exit 1; }

# Жив ли процесс из pid-файла. Файл может устареть (после ребута), поэтому
# проверяем именно процесс, а не факт наличия файла.
pid_alive() {
    local file="$1" pid
    [ -f "$file" ] || return 1
    pid="$(cat "$file" 2>/dev/null)" || return 1
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

# --- 0. Что вообще есть на машине -------------------------------------------

command -v tmux >/dev/null 2>&1 || die "tmux не найден — стенд без него не живёт"
command -v curl >/dev/null 2>&1 || die "curl не найден — нечем проверять мок"

mkdir -p "$STAND_DIR"

# --- 1. Окружение, которое унаследуют все части стенда -----------------------
#
# tmux-сервер запоминает окружение в момент старта и отдаёт его каждой панели.
# Поэтому переменные выставляем ДО `tmux new-session`: так панели сразу видят
# изолированный DISPLAY и мок-провайдера, а dev-бинарник лежит в PATH.
#
# HEY_AGENT_ENV_FILE указывает на файл ВНУТРИ стенда: даже если кто-то вызовет
# `hey-agent setup-key`, запись пойдёт сюда, а не в ~/.env пользователя.
export DISPLAY="$XVFB_DISPLAY"
export HEY_AGENT_BASE_URL="$MOCK_URL/v1"
export HEY_AGENT_API_KEY="stand-mock-key"
export HEY_AGENT_MODEL="$MOCK_MODEL"
export HEY_AGENT_ENV_FILE="$STAND_DIR/stand.env"
export PATH="$(dirname "$DEV_BIN"):$PATH"

printf 'HEY_AGENT_API_KEY=%s\n' "$HEY_AGENT_API_KEY" >"$HEY_AGENT_ENV_FILE"
chmod 600 "$HEY_AGENT_ENV_FILE"

say "== hey-agent test stand up =="

# --- 2. X-сервер: Xvfb на :99 ------------------------------------------------

XVFB_STATE="нет"   # станет "поднят" / "уже работал" / "чужой :99" / "отсутствует"
if pid_alive "$STAND_DIR/xvfb.pid"; then
    XVFB_STATE="уже работал (pid $(cat "$STAND_DIR/xvfb.pid"))"
elif [ -e "$STAND_DIR/xvfb.external" ]; then
    XVFB_STATE="чужой процесс на $XVFB_DISPLAY (не наш — не гасим)"
elif command -v Xvfb >/dev/null 2>&1; then
    # -nolisten tcp: X только по unix-сокету, сеть не трогаем.
    nohup Xvfb "$XVFB_DISPLAY" -screen 0 "$XVFB_SCREEN" -nolisten tcp \
        >"$STAND_DIR/xvfb.log" 2>&1 &
    echo $! >"$STAND_DIR/xvfb.pid"
    # Ждём, пока дисплей начнёт отвечать (xdpyinfo — штатный X-клиент).
    for _ in $(seq 1 50); do
        xdpyinfo -display "$XVFB_DISPLAY" >/dev/null 2>&1 && break
        sleep 0.1
    done
    if xdpyinfo -display "$XVFB_DISPLAY" >/dev/null 2>&1; then
        XVFB_STATE="поднят на $XVFB_DISPLAY (pid $(cat "$STAND_DIR/xvfb.pid"))"
    else
        warn "Xvfb запущен, но $XVFB_DISPLAY не отвечает — смотри $STAND_DIR/xvfb.log"
        XVFB_STATE="запущен, но не отвечает"
    fi
elif xdpyinfo -display "$XVFB_DISPLAY" >/dev/null 2>&1; then
    # На :99 уже кто-то слушает, но pid-файла стенда нет — чужой X-сервер.
    # Использовать можно, гасить при stand-down НЕЛЬЗЯ.
    touch "$STAND_DIR/xvfb.external"
    warn "$XVFB_DISPLAY занят чужим X-сервером; стенд использует его, но не гасит"
    XVFB_STATE="чужой процесс на $XVFB_DISPLAY (не наш — не гасим)"
else
    warn "Xvfb не найден — стенд работает в режиме «только tmux+mock»."
    warn "DISPLAY внутри стенда всё равно $XVFB_DISPLAY: попытка XRecord/X11"
    warn "упадёт с «cannot open display», а не уйдёт на ваш реальный дисплей."
    XVFB_STATE="отсутствует (режим tmux+mock)"
fi
echo "$XVFB_DISPLAY" >"$STAND_DIR/display"
say "X11:      $XVFB_STATE"

# --- 3. Мок провайдера на 127.0.0.1:18923 ------------------------------------

MOCK_STATE="нет"
if pid_alive "$STAND_DIR/mock.pid"; then
    MOCK_STATE="уже работал (pid $(cat "$STAND_DIR/mock.pid"))"
elif curl -sf "$MOCK_URL/health" 2>/dev/null | grep -q '"stand": *"hey-agent"'; then
    # Порт отвечает нашим health — мок жив, но pid-файл потерян: не перезапускаем,
    # но честно помечаем, что stand-down его не снимет (PID неизвестен).
    warn "мок на $MOCK_URL уже отвечает, но pid-файла нет — stand-down его не погасит"
    MOCK_STATE="уже отвечает на $MOCK_URL (чужой запуск, без pid-файла)"
else
    nohup bash "$SCRIPT_DIR/mock-provider.sh" >"$STAND_DIR/mock.log" 2>&1 &
    echo $! >"$STAND_DIR/mock.pid"
    # Ждём health — без готового мока транскрибация в тесте уйдёт в никуда.
    for _ in $(seq 1 50); do
        curl -sf "$MOCK_URL/health" >/dev/null 2>&1 && break
        sleep 0.1
    done
    if curl -sf "$MOCK_URL/health" >/dev/null 2>&1; then
        MOCK_STATE="поднят на $MOCK_URL (pid $(cat "$STAND_DIR/mock.pid"))"
    else
        die "мок не ответил на $MOCK_URL — смотри $STAND_DIR/mock.log"
    fi
fi
say "mock:     $MOCK_STATE"

# --- 4. tmux-сервер heydev: сессия stand, окна a / b / ctl -------------------
#
# Панели-приёмники — это cat, который пишет весь ввод в файл-маркер.
# `stty -icanon` выключает канонический режим pty: без него line discipline
# отдаёт байты только после Enter, и проверка доставки «без submit» была бы
# слепой. Echo оставляем — в панели видно, что пришло.

sink_command() {
    local title="$1" logfile="$2"
    printf "printf '%%s\\n' '%s'; stty -icanon time 0 min 1; exec cat >> '%s'" \
        "$title" "$logfile"
}

TMUX_STATE="нет"
if tmux -L "$TMUX_SOCKET" has-session -t "$SESSION" 2>/dev/null; then
    TMUX_STATE="уже работал (сессия $SESSION на сокете $TMUX_SOCKET)"
    # На случай, если сервер стартовал раньше с другим окружением: обновим env
    # сессии — оно применится к новым окнам/процессам в ней.
    tmux -L "$TMUX_SOCKET" set-environment -t "$SESSION" DISPLAY "$XVFB_DISPLAY"
    tmux -L "$TMUX_SOCKET" set-environment -t "$SESSION" HEY_AGENT_BASE_URL "$HEY_AGENT_BASE_URL"
    tmux -L "$TMUX_SOCKET" set-environment -t "$SESSION" HEY_AGENT_API_KEY "$HEY_AGENT_API_KEY"
    tmux -L "$TMUX_SOCKET" set-environment -t "$SESSION" HEY_AGENT_MODEL "$HEY_AGENT_MODEL"
    tmux -L "$TMUX_SOCKET" set-environment -t "$SESSION" HEY_AGENT_ENV_FILE "$HEY_AGENT_ENV_FILE"
else
    : >"$STAND_DIR/pane-target-a.log"
    : >"$STAND_DIR/pane-target-b.log"

    # Первое окно сессии — `a`. `-d` — без attach, стенд живёт фоном.
    tmux -L "$TMUX_SOCKET" new-session -d -s "$SESSION" -n a \
        "$(sink_command "=== стенд: панель A, ввод уходит в pane-target-a.log ===" \
            "$STAND_DIR/pane-target-a.log")"

    tmux -L "$TMUX_SOCKET" new-window -t "$SESSION:" -n b \
        "$(sink_command "=== стенд: панель B, ввод уходит в pane-target-b.log ===" \
            "$STAND_DIR/pane-target-b.log")"

    # ctl — контрольная панель с обычным shell. Env уже внутри (см. шаг 1):
    # отсюда `hey-agent listen --target ...` общается с сокетом heydev через
    # переменную TMUX, которую tmux выставляет каждой панели сам.
    tmux -L "$TMUX_SOCKET" new-window -t "$SESSION:" -n ctl "bash"

    # Дисциплина: у всего сервера только этот сокет — дефолтный не трогаем.
    TMUX_STATE="поднят (сессия $SESSION: окна a, b, ctl)"
fi
say "tmux:     $TMUX_STATE"

# ID панелей (%N) — самый стабильный target для hey-agent и send-keys.
# Записываем в файлы, чтобы тест читал их, а не парсил tmux.
tmux -L "$TMUX_SOCKET" display-message -p -t "$SESSION:a" '#{pane_id}' \
    >"$STAND_DIR/pane-a.id" 2>/dev/null || true
tmux -L "$TMUX_SOCKET" display-message -p -t "$SESSION:b" '#{pane_id}' \
    >"$STAND_DIR/pane-b.id" 2>/dev/null || true

# Маркер сокета: stand-down сверяется с ним, тесты читают путь из файла.
echo "/tmp/tmux-$(id -u)/$TMUX_SOCKET" >"$STAND_DIR/tmux-socket"
echo "$SESSION" >"$STAND_DIR/tmux-session"

# --- 5. Итог и инструкция -----------------------------------------------------

cat <<EOF

стенд поднят. Каталог состояния: $STAND_DIR
  xvfb.pid, mock.pid   — кого гасить при stand-down
  pane-target-{a,b}.log — файлы-маркеры доставки текста
  pane-{a,b}.id        — tmux pane id (%N) окон a и b
  mock.log, xvfb.log   — логи процессов стенда

Как пользоваться:

  # зайти в стенд (отдельный сервер, не ваш tmux):
  tmux -L $TMUX_SOCKET attach -t $SESSION

  # в окне ctl уже выставлены DISPLAY=$XVFB_DISPLAY и HEY_AGENT_* — там:
  hey-agent doctor
  hey-agent listen --target \$(cat $STAND_DIR/pane-a.id) --key F9 --submit

  # или «снаружи», без attach — то же через send-keys в окно ctl:
  tmux -L $TMUX_SOCKET send-keys -t $SESSION:ctl \\
      'hey-agent listen --target '\"\$(cat $STAND_DIR/pane-a.id)\"' --key F9 --submit' Enter

  # проверка мока руками:
  curl -s -X POST $MOCK_URL/v1/audio/transcriptions
  curl -s $MOCK_URL/v1/models

  # проверка доставки: после распознавания строка «stand mock transcript»
  # должна появиться в файле-маркере выбранной панели:
  tail -f $STAND_DIR/pane-target-a.log

Погасить стенд:
  bash $SCRIPT_DIR/stand-down.sh
EOF
