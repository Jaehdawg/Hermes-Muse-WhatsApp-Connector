# Hermes Muse WhatsApp Connector

A loopback-only WhatsApp companion service for sending to and receiving from **Muse** (`@bot`) chats.

> Use this when a normal Baileys WhatsApp send reaches a Muse chat but does not get a usable response. Muse uses WhatsApp's WASA-encrypted request path; this connector uses [whatsmeow](https://github.com/tulir/whatsmeow) for that path while leaving ordinary WhatsApp traffic on the existing bridge.

## Architecture

```text
existing Baileys bridge ── normal chats ──> WhatsApp
                       └─ @bot chats ─────> this connector (127.0.0.1:3001) ──> WhatsApp Muse

this connector's GET /messages ──────────> existing bridge inbound queue
```

The connector owns a **separate WhatsApp companion-device session** and accepts only `@bot` JIDs. It binds to `127.0.0.1`; do not expose it to a LAN or the internet.

## Requirements

- Go 1.26+
- A WhatsApp account that can link a companion device
- CGO enabled and SQLite development headers
- Node.js 18+ only when using the supplied JavaScript adapter

### OS prerequisites

```bash
# Ubuntu / Debian / WSL
sudo apt-get update && sudo apt-get install -y build-essential libsqlite3-dev

# macOS (Xcode Command Line Tools supplies the compiler and SQLite)
xcode-select --install

# Windows
# Build inside WSL, or install a CGO-capable Go/C toolchain. WSL is recommended.
```

## Install and pair

```bash
git clone https://github.com/Jaehdawg/Hermes-Muse-WhatsApp-Connector.git
cd Hermes-Muse-WhatsApp-Connector
go build -o hermes-muse-whatsapp-connector .
./hermes-muse-whatsapp-connector
```

The connector listens at `http://127.0.0.1:3001`. Its default companion credential database is:

```text
~/.hermes/whatsapp/native-bot/store.db
```

Override either setting when needed:

```bash
HERMES_NATIVE_BOT_PORT=3001 \
HERMES_NATIVE_BOT_DB=/secure/path/muse-store.db \
./hermes-muse-whatsapp-connector
```

Request a pairing code with the WhatsApp account's phone number in E.164 digits:

```bash
curl -X POST http://127.0.0.1:3001/pair-code \
  -H 'Content-Type: application/json' \
  -d '{"phone":"15551234567"}'
```

On the phone: **WhatsApp → Settings → Linked devices → Link a device → Link with phone number instead**. Enter the returned code before it expires (normally 160 seconds).

```bash
curl http://127.0.0.1:3001/health
# {"connected":true,"paired":true}
```

## Find your Muse bot ID

A Muse chat identifier has this exact form:

```text
<digits>@bot
```

After pairing, message the Muse agent from the WhatsApp phone app and wait for its reply. Then read the connector queue:

```bash
curl http://127.0.0.1:3001/messages
```

The reply includes the exact JID to reuse:

```json
[{"chatId":"YOUR_MUSE_BOT_ID@bot","messageId":"...","sender":"...","text":"...","timestamp":"..."}]
```

Copy `chatId` exactly. Do not infer, modify, or turn it into a phone number. `GET /messages` is a destructive drain: it atomically returns queued replies and clears them. Run **one consumer only**.

## API contract

Every response is JSON.

### `GET /health`

Returns HTTP `200`:

```json
{"connected":true,"paired":true}
```

`connected` reports the live WhatsApp connection; `paired` reports whether a companion credential exists locally.

### `POST /pair-code`

Request:

```json
{"phone":"15551234567"}
```

Success (`200`):

```json
{"code":"1234-5678","expiresInSeconds":160}
```

Errors: `400` malformed JSON/missing phone, `409` already paired, `502` WhatsApp did not create a code.

### `POST /send`

Request:

```json
{"chatId":"YOUR_MUSE_BOT_ID@bot","message":"Hello."}
```

Success (`200`):

```json
{"success":true,"messageId":"...","timestamp":"..."}
```

Errors: `400` malformed input, blank message, non-`@bot` or invalid JID; `503` not paired or disconnected; `502` WhatsApp failed the send.

### `GET /messages`

Returns HTTP `200` with an array of inbound replies. Each item has `chatId`, `messageId`, `sender`, `text`, and `timestamp`.

```json
[]
```

An empty array means no reply is currently queued. It does **not** prove a Muse reply will never arrive. Wait and poll again.

## Integrate an existing Baileys bridge

The repository includes a small, dependency-free Node client and an adaptation example:

- [`examples/muse-connector.mjs`](examples/muse-connector.mjs): HTTP client and sequential poller.
- [`examples/baileys-bridge-adapter.mjs`](examples/baileys-bridge-adapter.mjs): the two seams for a Baileys bridge.

Integration responsibilities:

1. **Outbound:** before `sock.sendMessage`, route `chatId`s ending in `@bot` to `muse.send(chatId, text)`. Keep all other chats on Baileys.
2. **Inbound:** start exactly one Muse poller and write each drained reply to the same inbound queue/event bus used by the rest of the bridge.

The adapter returns a stop function; call it during bridge shutdown. Do not start a poller per request or more than one poller per connector, because multiple consumers can drain replies before another process sees them.

Test the client:

```bash
node --test examples/muse-connector.test.mjs
```

## Run as a systemd user service

```bash
sudo install -m 0755 hermes-muse-whatsapp-connector /usr/local/bin/hermes-muse-whatsapp-connector
mkdir -p ~/.config/systemd/user
cp hermes-muse-whatsapp-connector.service.example ~/.config/systemd/user/hermes-muse-whatsapp-connector.service
systemctl --user daemon-reload
systemctl --user enable --now hermes-muse-whatsapp-connector.service
systemctl --user status hermes-muse-whatsapp-connector.service
```

View logs:

```bash
journalctl --user-unit hermes-muse-whatsapp-connector.service --follow
```

For a custom database path or port, add an `Environment=HERMES_NATIVE_BOT_DB=/secure/path/muse-store.db` and/or `Environment=HERMES_NATIVE_BOT_PORT=3001` line under `[Service]`, then reload and restart the service.

## Verification runbook

1. `GET /health` returns `{"connected":true,"paired":true}`.
2. Message the Muse agent from the phone app.
3. `GET /messages` returns a non-empty array containing its `chatId`.
4. Send a test with `POST /send` using that exact JID; receive `{"success":true,...}`.
5. Wait for the Muse reply and poll `GET /messages` until it appears.
6. Start the Baileys adapter, repeat the send through the bridge's normal outbound path, and confirm the reply reaches the normal inbound queue.

## Troubleshooting

- **`503 native bot transport is not paired and connected`:** pair first, then check `/health`. A linked-device removal invalidates the local session; pair again with a fresh credential database if needed.
- **Pairing code expired:** request a new code; do not reuse an old one.
- **`GET /messages` is empty:** it drains the queue. Wait for the reply, poll again, and make sure no other process is also consuming it.
- **Normal WhatsApp works but Muse does not:** verify the exact JID ends in `@bot` and that the bridge routes it to this connector rather than to Baileys.
- **Build fails at `github.com/mattn/go-sqlite3`:** install the compiler and SQLite headers above, then rebuild with CGO enabled.
- **Connection drops:** inspect the systemd journal, check Linked devices in WhatsApp, then restart the connector. Do not delete the SQLite database unless intentionally pairing a new device.

## Instructions for AI agents

Treat these as invariants:

1. Preserve the `@bot` JID exactly as returned by `GET /messages`.
2. Route only `@bot` traffic to this connector; leave ordinary WhatsApp traffic on the existing bridge.
3. Run a single inbound consumer because `GET /messages` drains the queue.
4. Never commit, upload, log, or expose the SQLite credential database or pairing codes.
5. Keep the service on loopback; do not add a public listener, reverse proxy, or cloud tunnel.
6. Validate the integration using the verification runbook after every routing change.

## Security

The SQLite session database is a WhatsApp companion credential. Protect it like a password. `.gitignore` excludes session databases and build outputs, but always verify `git status` before committing.

## License

MIT. See [LICENSE](LICENSE).
