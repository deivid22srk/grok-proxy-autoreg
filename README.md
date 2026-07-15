# Grok Proxy CLI — Auto-Reg Edition

<p align="center">
  <strong>Proxy OpenAI-compatible para Grok com auto-rotação de contas E auto-registro via email temporário + Playwright</strong><br/>
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

1. **Auto-registro de contas Grok** usando email temporário (mail.tm ou tempmail.lol) — o fluxo completo de signup é automatizado com **Playwright** controlando um Chromium headless.
2. **Auto-provisionamento sob demanda** — quando todas as contas configuradas batem no rate-limit do xAI (HTTP 429/402), o proxy automaticamente provisiona uma conta nova via email temporário, completa o OAuth, salva no store e usa para retentar a requisição.

Isso significa que **enquanto o servidor está rodando, ele nunca recusa uma requisição por rate-limit** — ele simplesmente cria uma conta nova e segue em frente.

> **Não afiliado à xAI.** Projeto comunidade não-oficial. Use por sua conta e risco. Veja [DISCLAIMER.md](./DISCLAIMER.md) e [LICENSE](./LICENSE).

---

## Instalação

### Requisitos

- Go 1.23+ (para build from source)
- ~250 MB livres para o Chromium do Playwright (instalado automaticamente na primeira execução)

### From source (recomendado)

```bash
git clone https://github.com/deivid22srk/<NOVO_REPO>.git
cd <NOVO_REPO>
make cli                              # gera build/bin/grok-proxy-cli
# ou diretamente:
CGO_ENABLED=0 go build -o grok-proxy-cli ./cmd/grok-proxy-cli
```

Na primeira vez que rodar o `autoreg` ou o `serve` com `--auto-reg`, o binário baixa o Chromium automaticamente (`playwright.Install()`).

### Instalação do binário

```bash
sudo cp grok-proxy-cli /usr/local/bin/
grok-proxy-cli --help
```

---

## Uso rápido

### 1. Iniciar o servidor (com auto-registro habilitado por padrão)

```bash
grok-proxy-cli serve
```

Saída esperada:

```
grok-proxy-plus listening on http://127.0.0.1:8787/v1
endpoints:
  GET  /v1/models
  POST /v1/chat/completions
  POST /v1/responses
  POST /v1/messages
press Ctrl+C to stop
auto-rotation: enabled (use --no-rotate to disable)
auto-registration: enabled (provider=mail.tm, headed=false)
  when all accounts hit rate-limit, a fresh Grok account will be
  created via temp email + Playwright and used to retry the request.
no account configured — auto-registration will provision one on first request
```

Aponte qualquer cliente OpenAI-compatible para `http://127.0.0.1:8787/v1` e use. Quando o xAI retornar 429/402, o proxy:

1. Marca a conta atual como limitada (cooldown 5 min para rate-limit, 6 h para quota diária)
2. Procura outra conta ativa no store
3. Se não houver, dispara o auto-registro:
   - Cria um inbox no mail.tm (ou tempmail.lol via `--provider`)
   - Abre um Chromium headless, navega para a URL do device-code do xAI
   - Preenche o email temporário, submete o formulário
   - Aguarda o email de verificação (filtro: remetente contém `x.ai`)
   - Extrai o link de verificação e abre **na mesma sessão do browser** (cookies batem)
   - Faz o poll do device-code até receber access_token + refresh_token
   - Salva a nova conta no store e a marca como ativa
4. Refaz a requisição original com a conta nova

### 2. Criar uma conta manualmente (uma única vez, antes do serve)

```bash
grok-proxy-cli autoreg
```

Você verá:

```
auto-registro: provider=mail.tm, browser=true, headed=false
  email-wait=5m0s  signup-timeout=10m0s  keep-inbox=false

[autoreg] provisionando inbox temporário (mail.tm)
[autoreg] inbox criado: abc123xyz@web-library.net (provedor mail.tm)
[autoreg] iniciando device-code flow no x.ai
[autoreg] iniciando signup automatizado via Playwright
[autoreg/playwright: navegando para https://auth.x.ai/device?user_code=XXXX
[autoreg/playwright: email preenchido (input[type='email'])
[autoreg/playwright: clicou em submit (button[type='submit'])
[autoreg/playwright: aguardando email de verificação…
[autoreg/playwright: email recebido de noreply@x.ai — assunto "Verify your email"
[autoreg/playwright: abrindo link de verificação no browser: https://…
[autoreg/playwright: verificação parece ter sido concluída
[autoreg] aguardando tokens OAuth…
[autoreg] conta obtida: id=01HXX… email=abc123xyz@web-library.net

✓ conta criada e ativada com sucesso!
  ID:     01HXX…
  email:  abc123xyz@web-library.net
  label:  abc123xyz@web-library.net
  inbox:  abc123xyz@web-library.net (mail.tm)
  salvo em: /home/user/.local/share/GrokDesktop/accounts
```

### 3. Modo assistido (quando o Playwright não conseguir)

```bash
grok-proxy-cli autoreg --no-browser
```

O programa vai imprimir a URL de verificação, o código do dispositivo e o email temporário. Você abre o navegador manualmente, completa o signup com esse email, e o programa cuida de:

- Aguardar o email de verificação
- Extrair o link
- Fazer GET no link (confirmação via HTTP simples)
- Poll do device-code

### 4. Modo debug (ver o browser)

```bash
grok-proxy-cli autoreg --headed
# ou no serve:
grok-proxy-cli serve --headed
```

---

## Auto-registro de contas (NOVO)

### Como funciona

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
│  1. tempmail.CreateInbox() → abc123@web-library.net              │
│  2. oauth.StartDevice() → device_code, user_code, verify_url     │
│  3. playwrightSignup():                                          │
│     - Lança Chromium headless                                    │
│     - Navega para verify_url                                     │
│     - Preenche email temporário                                  │
│     - Clica em "Sign up" / submit                                │
│     - Aguarda email de verificação (poll mail.tm a cada 3s)      │
│     - Extrai link de verificação                                 │
│     - Abre link NO MESMO browser context (cookies batem)         │
│  4. oauth.PollDevice() → access_token + refresh_token           │
│  5. store.UpsertAccount() + SetActiveAccount()                   │
└───────────────┬──────────────────────────────────────────────────┘
                │
                ▼
┌──────────────────────────────────────────────────────────────────┐
│  Rotator refaz a requisição com a conta nova                    │
└──────────────────────────────────────────────────────────────────┘
```

### Provedores de email temporário

| Provedor | API | Status | Domínio | Recomendação |
|----------|-----|--------|---------|--------------|
| **mail.tm** | REST + JWT, sem API key | ✅ funcionando | `web-library.net` (rotativo) | **padrão** — domínio menos flaggado |
| **tempmail.lol** | REST + token opaco | ✅ funcionando | `*.icodetensor.com` | fallback — inbox expira em 1h |

Trocar via `--provider tempmail.lol`.

> Os três sites que você pediu (emailtemp.org, invertexto.com, tmaily.com) **não têm API pública** — são todos web-only. Por isso a implementação usa mail.tm (API REST bem documentada) como primário.

### Limitações e riscos

- **CAPTCHA**: se o xAI apresentar CAPTCHA no signup, o Playwright não consegue resolver automaticamente. Nesses casos, caímos no modo assistido (`--no-browser`) — o programa imprime a URL + email + código e você completa manualmente.
- **Domínios bloqueados**: o xAI pode rejeitar domínios de email temporário conhecidos. mail.tm usa `web-library.net` que é menos flaggado que `1secmail.com`/`sharklasers.com`, mas não há garantia. Se falhar, tente `--provider tempmail.lol`.
- **Mudanças no fluxo de signup do x.ai**: o código do Playwright usa seletores best-effort (`input[type='email']`, `button[type='submit']`). Se o x.ai mudar o HTML, os seletores podem quebrar — ajuste em `internal/autoreg/playwright_signup.go`.
- **Anti-bot**: o Chromium do Playwright dispara flags de automação. Adicionamos `--disable-blink-features=AutomationControlled` e sobrescrevemos `navigator.webdriver`, mas fingerprinting avançado pode ainda detectar. Use `--headed` para debugar.

---

## Comandos

```
grok-proxy-cli                       inicia o proxy local (default = serve)
grok-proxy-cli serve                 mesmo; flags: --listen, --api-key, --no-proxy,
                                     --no-rotate, --rotate-verbose, --auto-reg,
                                     --no-auto-reg, --provider <mail.tm|tempmail.lol>,
                                     --headed, --keep-inbox, --email-wait, --signup-timeout
grok-proxy-cli login                 sign in manual via device-code OAuth
grok-proxy-cli autoreg               cria uma nova conta automaticamente (mail.tm + Playwright)
                                     flags: --provider, --no-browser, --headed, --keep-inbox,
                                     --email-wait, --signup-timeout
grok-proxy-cli accounts              lista contas
grok-proxy-cli use <id>              troca conta ativa (prefixo do id OK)
grok-proxy-cli logout <id>           remove conta
grok-proxy-cli models                lista modelos disponíveis
grok-proxy-cli chat                  REPL interativo com streaming
grok-proxy-cli ask "<prompt>"        one-shot; flags: --effort, --model, --no-think
grok-proxy-cli rotate                status da rotação; flags: --next, --reset <id>, --reset-all
```

Flag global (qualquer comando):

```
--data-dir <path>                    sobrescreve diretório AppData
```

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
│   ├── grok-proxy-cli/         # CLI terminal (NOVO: comando autoreg)
│   └── selftest/
├── internal/
│   ├── app/                    # core headless (NOVO: SetAutoReg, RegisterNewAccount)
│   ├── oauth/                  # device login + refresh
│   ├── store/                  # multi-conta AppData
│   ├── upstream/               # cli-chat-proxy client (stream)
│   ├── proxyhttp/              # servidor HTTP OpenAI/Anthropic
│   ├── pricing/                # estimativa de custo
│   ├── rotator/                # rotação de contas (NOVO: tryAutoReg callback)
│   ├── tempmail/               # NOVO: cliente mail.tm + tempmail.lol
│   │   ├── provider.go         # interface Provider + tipos unificados
│   │   ├── mailtm.go           # mail.tm (REST + JWT)
│   │   ├── tempmail_lol.go     # tempmail.lol (fallback)
│   │   └── util.go             # extract verification link
│   ├── autoreg/                # NOVO: orquestrador de signup
│   │   ├── manager.go          # fluxo: inbox → device-code → poll → save
│   │   └── playwright_signup.go # browser automation (Chromium)
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

### Dependências novas

```text
github.com/mxschmitt/playwright-go v0.6100.0   # binding Go para Playwright
```

O Chromium é baixado para `~/.cache/ms-playwright/` na primeira execução (~95 MB).

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

> ⚠️ O auto-registro via Playwright requer que o Chromium correspondente à plataforma esteja instalado. Em ambientes CI/Docker, rode `grok-proxy-cli autoreg --no-browser` uma primeira vez para baixar os binários, ou use `playwright.Install()` programaticamente.

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
- Browser automation via [Playwright for Go](https://github.com/mxschmitt/playwright-go).

---

## License

**MIT (Non-Commercial)** — grátis para uso pessoal / não-comercial.
**Uso comercial** requer permissão por escrito.
Termos completos: [LICENSE](./LICENSE).
