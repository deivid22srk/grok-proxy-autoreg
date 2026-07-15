# Grok Proxy CLI — Auto-Reg Edition

<p align="center">
  <strong>Proxy OpenAI-compatible para Grok com auto-rotação de contas E auto-registro via email temporário</strong><br/>
  Multi-conta · streaming · thinking · API <code>/v1</code> local · cria conta nova automaticamente quando bate no rate-limit
</p>

<p align="center">
  <a href="#instalação">Instalação</a> ·
  <a href="#uso-rápido">Uso rápido</a> ·
  <a href="#auto-registro-de-contas-novo">Auto-registro</a> ·
  <a href="#comandos">Comandos</a> ·
  <a href="#arquitetura">Arquitetura</a> ·
  <a href="#disclaimer">Disclaimer</a>
</p>

---

## O que é isso?

Fork do [`deivid22srk/grok-proxy-cli`](https://github.com/deivid22srk/grok-proxy-cli) (que por sua vez é fork terminal-only do [`Maicon501a/grok-proxy-plus`](https://github.com/Maicon501a/grok-proxy-plus)) adicionando **duas capacidades novas**:

1. **Auto-registro de contas Grok** usando email temporário (tmaily.com via `emailproxy`, ou invertexto.com como fallback) — o fluxo é **assistido**: o programa provê o email, monitora o inbox, detecta o email de verificação e extrai o código (formato `XXX-XXX`) automaticamente. Você só abre a URL e digita o código no formulário do x.ai.
2. **Auto-provisionamento sob demanda** — quando todas as contas configuradas batem no rate-limit do xAI (HTTP 429/402), o proxy automaticamente provisiona uma conta nova via email temporário, completa o OAuth, salva no store e usa para retentar a requisição.

Isso significa que **enquanto o servidor está rodando, ele nunca recusa uma requisição por rate-limit** — ele simplesmente cria uma conta nova e segue em frente.

> **Não afiliado à xAI.** Projeto comunidade não-oficial. Use por sua conta e risco. Veja [DISCLAIMER.md](./DISCLAIMER.md) e [LICENSE](./LICENSE).

---

## Instalação

### Requisitos

- Go 1.23+ (para build from source)
- ~100 MB livres (sem Playwright — agora é HTTP puro)

### From source (recomendado)

```bash
git clone https://github.com/deivid22srk/grok-proxy-autoreg.git
cd grok-proxy-autoreg

# Compila o binário principal (CLI)
CGO_ENABLED=0 go build -o grok-proxy-cli ./cmd/grok-proxy-cli

# Compila o proxy de email (separado)
CGO_ENABLED=0 go build -o emailproxy ./cmd/emailproxy

# (opcional) instala no $PATH
sudo cp grok-proxy-cli /usr/local/bin/
sudo cp emailproxy /usr/local/bin/

# valida
./grok-proxy-cli --help
./emailproxy --listen 127.0.0.1:8788 &
curl http://127.0.0.1:8788/health
```

> ⚠️ **Importante**: você precisa de DOIS binários — `grok-proxy-cli` (a CLI principal) e `emailproxy` (o proxy que expõe tmaily.com/invertexto.com como API REST limpa, já que o x.ai bloqueia os domínios do mail.tm).

---

## Uso rápido

### Workflow padrão (3 terminais)

Você roda 3 processos separados — idealmente em 3 terminais diferentes para conseguir ver os logs de cada um:

#### Terminal 1 — Proxy de email (sempre rodando)

```bash
./emailproxy --listen 127.0.0.1:8788
```

Saída:
```
emailproxy 0.1.0 listening on http://127.0.0.1:8788
endpoints:
  GET    /health
  GET    /backends
  POST   /inboxes                  body: {backend, prefix?, domain?}
  GET    /inboxes/{sid}/messages?address=...
  DELETE /inboxes/{sid}

backends:
  tmaily      — tmaily.com (REST, primary)
  invertexto  — invertexto.com / uorak.com (SSE, fallback)

press Ctrl+C to stop
```

Esse proxy fica vivo e provê emails temporários com domínios que o x.ai aceita (`hqpdf.com`, `imgcompress.io`, `watersoftenersystemcost.com`, `uorak.com`, `10timer.com`, etc — NUNCA `web-library.net` que é rejeitado).

#### Terminal 2 — Criar contas (uma vez)

```bash
# Cria 3 contas em sequência (uma por vez — você completa o signup
# no navegador para cada uma, o programa detecta o email e mostra
# o código de verificação automaticamente)
export GROK_DATA_DIR=~/.local/share/GrokDesktop
./grok-proxy-cli autoreg-batch 3 --provider emailproxy:tmaily
```

Para cada conta o fluxo é:
1. Programa provisiona um email temporário novo (ex: `abc123@hqpdf.com`)
2. Programa inicia o device-code flow no x.ai
3. Programa imprime em banner:
   ```
   PASSO 1 — Abra esta URL no seu navegador:
   https://accounts.x.ai/oauth2/device?user_code=XXXX-XXXX
   PASSO 2 — Use este email no signup:
   abc123@hqpdf.com
   Código do dispositivo: XXXX-XXXX
   ```
4. Você abre a URL no navegador e cria a conta usando o email exibido
5. Quando o x.ai envia o email de verificação, o programa detecta em ~3s e mostra:
   ```
   PASSO 3 — Email de verificação recebido!
   CÓDIGO DE VERIFICAÇÃO (digite no formulário do x.ai):
      >>>   X6B-09B   <<<
   ```
6. Você digita esse código no formulário do x.ai
7. O OAuth completa, o programa salva a conta no store e inicia a próxima

Também pode criar uma conta única:
```bash
./grok-proxy-cli autoreg --provider emailproxy:tmaily
```

Ou usar o backend invertexto (fallback):
```bash
./grok-proxy-cli autoreg --provider emailproxy:invertexto
```

#### Terminal 3 — Subir o proxy OpenAI (depois de ter contas)

```bash
export GROK_DATA_DIR=~/.local/share/GrokDesktop
./grok-proxy-cli serve --auto-reg
```

Saída:
```
grok-proxy-plus listening on http://127.0.0.1:8787/v1
endpoints:
  GET  /v1/models
  POST /v1/chat/completions
  POST /v1/responses
  POST /v1/messages
press Ctrl+C to stop
auto-rotation: enabled (use --no-rotate to disable)
auto-registration: enabled (provider=emailproxy:tmaily)
  when all accounts hit rate-limit, the program will print a
  URL + temp email — you complete the signup in your browser
  and it auto-detects the verification email.
active account: abc123@hqpdf.com
```

Agora qualquer cliente OpenAI-compatible pode apontar para `http://127.0.0.1:8787/v1`:

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"grok-4.5","messages":[{"role":"user","content":"Oi"}]}'
```

Quando o xAI retornar 429/402 (rate-limit), o proxy:

1. Marca a conta atual como limitada (cooldown 5 min rate-limit, 6 h quota diária)
2. Procura outra conta ativa no store
3. Se não houver, dispara o auto-registro (igual ao Terminal 2):
   - Provisiona email temporário via emailproxy (tmaily/invertexto)
   - Inicia device-code flow do x.ai
   - Imprime URL + email + código do dispositivo no stderr
   - Você abre a URL, cria a conta com o email temporário
   - Programa detecta email de verificação e mostra o código
   - Você digita o código no formulário do x.ai
   - OAuth completa, conta é salva e usada para retomar a requisição
4. Refaz a requisição original com a conta nova

---

## Comandos

```
grok-proxy-cli                          inicia o proxy local (default = serve)
grok-proxy-cli serve                    proxy OpenAI local; flags: --listen, --api-key,
                                        --no-proxy, --no-rotate, --rotate-verbose,
                                        --auto-reg, --provider <emailproxy:tmaily|emailproxy:invertexto|mail.tm|tempmail.lol>,
                                        --keep-inbox, --email-wait, --signup-timeout
grok-proxy-cli login                    sign in manual via device-code OAuth
grok-proxy-cli autoreg                  cria UMA conta (fluxo assistido); flags: --provider,
                                        --keep-inbox, --email-wait, --signup-timeout
grok-proxy-cli autoreg-batch N          cria N contas em sequência (uma por vez); flags:
                                        --provider, --keep-inbox, --email-wait,
                                        --signup-timeout, --pause (default 5s)
grok-proxy-cli accounts                 lista contas
grok-proxy-cli use <id>                 troca conta ativa (prefixo do id OK)
grok-proxy-cli logout <id>              remove conta
grok-proxy-cli models                   lista modelos disponíveis
grok-proxy-cli chat                     REPL interativo com streaming
grok-proxy-cli ask "<prompt>"           one-shot; flags: --effort, --model, --no-think
grok-proxy-cli rotate                   status da rotação; flags: --next, --reset <id>, --reset-all
```

Flag global (qualquer comando):

```
--data-dir <path>                       sobrescreve diretório AppData
GROK_DATA_DIR environment variable      alternativa ao --data-dir
```

### Comando separado: emailproxy

```
emailproxy --listen 127.0.0.1:8788       sobe o proxy de email (DEVE rodar antes do autoreg/serve
                                         se for usar --provider emailproxy:*)
emailproxy --help                        lista flags
```

---

## Provedores de email temporário

| Provider (flag `--provider`) | Backend | Domínios típicos | Recomendação |
|------------------------------|---------|------------------|--------------|
| **`emailproxy:tmaily`** (padrão) | tmaily.com via `emailproxy` | `hqpdf.com`, `imgcompress.io`, `watersoftenersystemcost.com`, `10timer.com`, etc | ✅ **recomendado** — domínios rotativos não-bloqueados pelo x.ai |
| **`emailproxy:invertexto`** | invertexto.com / uorak.com via `emailproxy` | `uorak.com` | fallback — útil se tmaily começar a pedir Turnstile |
| `mail.tm` | mail.tm direto (sem proxy) | `web-library.net` | ⚠️ **NÃO use** — x.ai rejeita esse domínio |
| `tempmail.lol` | tempmail.lol direto (sem proxy) | `*.icodetensor.com`, `*.actionvspot.com` | funciona mas expira em 1h |

> **Por que o emailproxy?** Os sites que você indicou (emailtemp.org, invertexto.com, tmaily.com) **não têm API pública** — só interfaces web. O `emailproxy` faz o trabalho sujo de chamar os endpoints internos deles (REST no tmaily, SSE no invertexto) e expor uma API REST limpa e unificada. Assim conseguimos domínios de email que o x.ai aceita no signup (testado em produção: `watersoftenersystemcost.com` passou).

---

## Auto-registro de contas — como funciona

```
┌──────────────────────────────────────────────────────────────────┐
│  Requisição chega no /v1/chat/completions                        │
└───────────────┬──────────────────────────────────────────────────┘
                │
                ▼
┌──────────────────────────────────────────────────────────────────┐
│  Rotator: tenta a conta ativa                                    │
│  ↓ HTTP 429 / 402                                                │
│  Marca conta como limitada, tenta próxima                        │
└───────────────┬──────────────────────────────────────────────────┘
                │
                ▼  todas limitadas
┌──────────────────────────────────────────────────────────────────┐
│  AUTO-REG (callback)                                             │
│                                                                  │
│  1. emailproxy.CreateInbox() → abc123@hqpdf.com                  │
│  2. oauth.StartDevice() → device_code, user_code, verify_url     │
│  3. Banner impresso no stderr:                                   │
│     - URL de verificação (user abre no navegador)                │
│     - Email temporário (user digita no signup do x.ai)           │
│     - Código do dispositivo                                      │
│  4. Provider.WaitForMessage() — poll a cada 3s no inbox          │
│     Quando email de noreply@x.ai chega, extrai código            │
│     do assunto (formato "X6B-09B xAI confirmation code")         │
│  5. Banner com o CÓDIGO:                                         │
│        >>>   X6B-09B   <<<                                       │
│     User digita no formulário do x.ai                            │
│  6. oauth.PollDevice() → access_token + refresh_token            │
│  7. store.UpsertAccount() + SetActiveAccount()                   │
└───────────────┬──────────────────────────────────────────────────┘
                │
                ▼
┌──────────────────────────────────────────────────────────────────┐
│  Rotator refaz a requisição com a conta nova                    │
└──────────────────────────────────────────────────────────────────┘
```

### Limitações e riscos

- **Você precisa completar o signup no navegador**: o fluxo é assistido (não automatizado). O programa monitora o inbox e extrai o código de verificação automaticamente, mas você precisa abrir a URL e digitar o código no formulário do x.ai.
- **Turnstile (CAPTCHA)**: o tmaily.com pode ocasionalmente pedir Turnstile sob carga. Se acontecer, troque para `--provider emailproxy:invertexto` (que não tem Cloudflare no caminho do SSE).
- **Domínios bloqueados**: se um domínio específico for rejeitado pelo x.ai no futuro, o tmaily rotacionia domínios automaticamente (há 7+ disponíveis). Use `--provider emailproxy:invertexto` como fallback.
- **Rate limit do device-code do x.ai**: se você iniciar muitos signups em sequência curta, o x.ai pode retornar 429 ("slow_down") no endpoint `POST /oauth2/device`. Aguarde ~60s e tente de novo.

---

## OpenAI-compatible proxy

Depois de `grok-proxy-cli serve`, o servidor escuta em:

```
http://127.0.0.1:8787/v1
```

(Se `8787` estiver ocupado, cai para **`8788`**.)

| Setting | Value |
|---------|--------|
| **Base URL** | `http://127.0.0.1:8787/v1` |
| **API key** | qualquer string (ou a chave setada via `--api-key`) |
| **Model** | `grok-4.5` ou `grok-4.5-responses` |

### Exemplo — cURL

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer grok-desktop" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4.5",
    "stream": true,
    "reasoning_effort": "high",
    "messages": [{"role":"user","content":"Hello"}]
  }'
```

### Exemplo — Claude Code / Anthropic Messages

```bash
curl http://127.0.0.1:8787/v1/messages \
  -H "x-api-key: grok-desktop" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4.5",
    "max_tokens": 1024,
    "stream": true,
    "messages": [{"role":"user","content":"Hello"}]
  }'
```

### Exemplo — env vars

```bash
export OPENAI_BASE_URL=http://127.0.0.1:8787/v1
export OPENAI_API_KEY=grok-desktop
export OPENAI_MODEL=grok-4.5
```

### Modos de API

| Modo | Endpoint | Notas |
|------|----------|--------|
| **chat** | `/v1/chat/completions` | OpenAI chat + `reasoning_content` stream |
| **responses** | `/v1/responses` | Multi-turn + `web_search` / `x_search` nativo |
| **messages** | `/v1/messages` | Anthropic Messages API (stream + tools) |
| ~~completions~~ | `/v1/completions` | **Não suportado** (legacy) |

---

## Multi-conta

- `grok-proxy-cli login` → device-code login (xAI) manual
- `grok-proxy-cli autoreg` → cria conta automaticamente via temp email
- Cada conta é salva separadamente em AppData
- `grok-proxy-cli use <id-prefix>` troca a ativa
- A conta **ativa** é usada tanto por `chat`/`ask` quanto pelo proxy

Diretório de dados (nunca commitado):

| OS | Path |
|----|------|
| Windows | `%LOCALAPPDATA%\GrokDesktop\` |
| macOS | `~/Library/Application Support/GrokDesktop/` |
| Linux | `~/.local/share/GrokDesktop/` |

```text
GrokDesktop/
├── settings.json
├── usage.json
├── history.json
├── accounts/<id>.json
└── logs/
```

---

## Arquitetura

```text
.
├── cmd/
│   ├── grok-proxy-cli/         # CLI principal (comandos: serve, autoreg, autoreg-batch, login, etc)
│   ├── emailproxy/             # NOVO: proxy HTTP para tmaily.com + invertexto.com
│   ├── test_code/              # teste do ExtractVerificationCode
│   ├── test_emailproxy/        # teste do proxy de email
│   ├── test_oauth/             # teste do device-code do x.ai
│   ├── test_pipeline/          # teste do pipeline completo (mock)
│   ├── test_proxy/             # teste do proxy OpenAI
│   ├── test_tempmail/          # teste dos providers de email
│   ├── inspect_xai/            # debug: inspeciona HTML do accounts.x.ai
│   ├── inspect_xai2/           # debug: variante com fingerprint reforçado
│   └── selftest/               # integration smoke test
├── internal/
│   ├── app/                    # core headless (NOVO: SetAutoReg, RegisterNewAccount)
│   ├── oauth/                  # device login + refresh
│   ├── store/                  # multi-conta AppData
│   ├── upstream/               # cli-chat-proxy client (stream)
│   ├── proxyhttp/              # servidor HTTP OpenAI/Anthropic
│   ├── pricing/                # estimativa de custo
│   ├── rotator/                # rotação de contas (NOVO: tryAutoReg callback)
│   ├── tempmail/               # NOVO: providers de email temporário
│   │   ├── provider.go         # interface Provider + tipos unificados
│   │   ├── mailtm.go           # mail.tm (REST + JWT) — DOMÍNIO BLOQUEADO pelo x.ai
│   │   ├── tempmail_lol.go     # tempmail.lol (fallback direto)
│   │   ├── emailproxy.go       # emailproxy (tmaily/invertexto via proxy) — RECOMENDADO
│   │   └── util.go             # extract verification code + link
│   ├── autoreg/                # NOVO: orquestrador de signup assistido
│   │   ├── manager.go          # fluxo: inbox → device-code → email → code → poll → save
│   │   └── playwright_signup.go # (legado, não usado — mantido como referência)
│   ├── skills/
│   └── mcpconfig/
├── install.sh
├── Makefile
├── .github/workflows/
├── main.go / app.go            # app desktop Wails (preservado)
├── frontend/                   # UI desktop (preservado)
├── LICENSE
├── DISCLAIMER.md
└── README.md
```

### Dependências

```text
github.com/google/uuid          # já existente
github.com/mxschmitt/playwright-go  # ainda no go.mod mas NÃO usado no fluxo default
                                    # (código morto em playwright_signup.go — seguro remover)
```

Sem Chromium, sem WebKit, sem GTK — tudo HTTP puro. Binário final ~15 MB.

---

## Build from source

```bash
# CLI terminal (sem dependências GUI)
make cli              # gera build/bin/grok-proxy-cli
# ou:
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
  -o grok-proxy-cli ./cmd/grok-proxy-cli

# instalar no $GOBIN ou ~/.local/bin
make install
```

Cross-compile:

```bash
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o grok-proxy-cli-linux-amd64   ./cmd/grok-proxy-cli
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -o grok-proxy-cli-linux-arm64   ./cmd/grok-proxy-cli
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o grok-proxy-cli-darwin-arm64  ./cmd/grok-proxy-cli
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o grok-proxy-cli-windows-amd64.exe ./cmd/grok-proxy-cli
```

> ℹ️ Sem dependências de browser — o fluxo é 100% HTTP puro. Não precisa de Chromium, WebKit, GTK nem Node. Funciona em containers Docker minimalistas.

### Desktop (Wails) — ainda suportado

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
wails build
```

### Self-test (sem GUI)

```bash
go run ./cmd/selftest
```

---

## Notas de segurança

- **Tokens nunca vão para o git** — só AppData local
- OAuth `client_id` no código-fonte é o **público** do CLI xAI (PKCE, sem client secret)
- Não commite `accounts/`, `*.env`, nem binários de release de máquina suja
- Trate o proxy como **localhost-only** a não ser que saiba o que está fazendo
- Tokens de email temporário (mail.tm/tempmail.lol) são efêmeros e ficam só em memória durante o fluxo

---

## Disclaimer

**Use por sua conta e risco.** Os autores **não se responsabilizam** por bans, cobranças, perda de dados, violações de ToS ou quaisquer danos. Este **não** é um produto oficial xAI. Texto completo: [DISCLAIMER.md](./DISCLAIMER.md).

---

## Acknowledgements

- Forkado de [`deivid22srk/grok-proxy-cli`](https://github.com/deivid22srk/grok-proxy-cli) — que por sua vez é fork de [`Maicon501a/grok-proxy-plus`](https://github.com/Maicon501a/grok-proxy-plus) — obrigado pelo app desktop original, fluxo OAuth e proxy server.
- Auto-registro usa [mail.tm](https://mail.tm) e [tempmail.lol](https://tempmail.lol) (ambos com APIs públicas gratuitas).
- Auto-registro usa [tmaily.com](https://tmaily.com) e [invertexto.com](https://www.invertexto.com/gerador-email-temporario) (ambos sem API pública, acessados via `emailproxy` interno).

---

## License

**MIT (Non-Commercial)** — grátis para uso pessoal / não-comercial.
**Uso comercial** requer permissão por escrito.
Termos completos: [LICENSE](./LICENSE).
