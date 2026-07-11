#!/usr/bin/env bash
set -euo pipefail

CONFIG_FILE="litellm_config.yaml"
PORT=4000
LOG_FILE="litellm.log"

if [ ! -f "$CONFIG_FILE" ]; then
  echo "Erro: $CONFIG_FILE não encontrado no diretório atual."
  echo "Crie o arquivo antes de rodar este script."
  exit 1
fi

# Mata qualquer litellm antigo rodando na mesma porta
if pgrep -f "litellm --config $CONFIG_FILE --port $PORT" > /dev/null; then
  echo "Já existe um litellm rodando nessa config/porta. Encerrando antes de subir de novo..."
  pkill -f "litellm --config $CONFIG_FILE --port $PORT" || true
  sleep 1
fi

echo "Subindo o proxy LiteLLM na porta $PORT..."
nohup litellm --config "$CONFIG_FILE" --port "$PORT" > "$LOG_FILE" 2>&1 &
LITELLM_PID=$!

# Garante que o proxy é encerrado quando o script/claude terminar
cleanup() {
  echo ""
  echo "Encerrando o proxy LiteLLM (PID $LITELLM_PID)..."
  kill "$LITELLM_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Espera o proxy responder antes de continuar
echo "Aguardando o LiteLLM ficar pronto..."
MAX_TRIES=30
TRIES=0
until curl -s "http://localhost:$PORT/health/liveliness" > /dev/null 2>&1 \
   || curl -s "http://localhost:$PORT" > /dev/null 2>&1; do
  TRIES=$((TRIES + 1))
  if [ "$TRIES" -ge "$MAX_TRIES" ]; then
    echo "O LiteLLM não respondeu a tempo. Veja o log:"
    cat "$LOG_FILE"
    exit 1
  fi
  sleep 1
done

echo "LiteLLM pronto! Iniciando o Claude Code..."

export ANTHROPIC_BASE_URL="http://localhost:$PORT"
export ANTHROPIC_AUTH_TOKEN="dummy"
export ANTHROPIC_MODEL="qwen3.7-plus"
export ANTHROPIC_SMALL_FAST_MODEL="qwen3.7-plus"

claude
