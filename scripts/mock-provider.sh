#!/usr/bin/env bash
# mock-provider.sh — локальный HTTP-мок OpenAI-совместимого STT-провайдера.
#
# Зачем: hey-agent шлёт аудио на <HEY_AGENT_BASE_URL>/audio/transcriptions и
# проверяет ключ через GET <HEY_AGENT_BASE_URL>/models (см. internal/transcribe).
# Вместо реального API (деньги, сеть, секреты) поднимаем ответчик на localhost,
# который всегда возвращает один и тот же текст.
#
# Реализация — встроенный python3: он есть на машине и умеет и методы, и JSON,
# чего «голый» http.server или nc без обвязки не дадут.
#
# Переменные окружения:
#   MOCK_PORT     — порт (по умолчанию 18923)
#   MOCK_HOST     — интерфейс (по умолчанию 127.0.0.1, наружу не смотрим)
#   MOCK_MODEL    — id модели, который вернёт GET .../models (по умолчанию stand-mock)
#
# Остановка: по PID (stand-up.sh пишет его в pid-файл, stand-down.sh гасит).

set -euo pipefail

MOCK_PORT="${MOCK_PORT:-18923}"
MOCK_HOST="${MOCK_HOST:-127.0.0.1}"
MOCK_MODEL="${MOCK_MODEL:-stand-mock}"

if ! command -v python3 >/dev/null 2>&1; then
    echo "mock-provider: python3 не найден — мок поднять нельзя" >&2
    exit 1
fi

# exec: python заменяет bash-процесс, поэтому PID-файл остаётся точным —
# stand-down.sh убивает именно сервер, а не обёртку.
exec python3 - "$MOCK_HOST" "$MOCK_PORT" "$MOCK_MODEL" <<'PYEOF'
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

host, port, model = sys.argv[1], int(sys.argv[2]), sys.argv[3]

# Ответ транскрибации — фиксированная строка, чтобы тест мог её проверить
# в логе-маркере панели.
TRANSCRIPT = {"text": "stand mock transcript"}

# Список моделей для Verify(): hey-agent ищет в нём HEY_AGENT_MODEL.
MODELS = {
    "object": "list",
    "data": [
        {"id": model, "object": "model"},
        {"id": "gpt-transcribe", "object": "model"},
    ],
}

class Handler(BaseHTTPRequestHandler):
    """Минимальный OpenAI-compatible ответчик: модели, транскрибация, health."""

    def _json(self, code, payload):
        body = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _drain_body(self):
        # Дочитать тело запроса обязательно: иначе при keep-alive остаток
        # multipart-данных будет прочитан как «следующий запрос» и соединение
        # сломается.
        length = int(self.headers.get("Content-Length") or 0)
        if length:
            self.rfile.read(length)

    def do_GET(self):
        self._drain_body()
        if self.path.endswith("/models"):
            self._json(200, MODELS)
        elif self.path == "/health":
            self._json(200, {"status": "ok", "stand": "hey-agent"})
        else:
            self._json(404, {"error": {"message": "mock: unknown GET " + self.path}})

    def do_POST(self):
        self._drain_body()
        if self.path.endswith("/audio/transcriptions"):
            self._json(200, TRANSCRIPT)
        else:
            self._json(404, {"error": {"message": "mock: unknown POST " + self.path}})

    def log_message(self, fmt, *args):
        # Лог каждого запроса уходит в stderr → stand-up.sh пишет его в mock.log,
        # так видно, что hey-agent реально ходил в мок.
        sys.stderr.write("mock %s - %s\n" % (self.address_string(), fmt % args))

ThreadingHTTPServer((host, port), Handler).serve_forever()
PYEOF
