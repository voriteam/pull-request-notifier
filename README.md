# pull-request-notifier

[![CI](https://github.com/voriteam/pull-request-notifier/actions/workflows/ci.yaml/badge.svg)](https://github.com/voriteam/pull-request-notifier/actions/workflows/ci.yaml)

An open source Go service that delivers GitHub pull request activity as Slack DMs. Self-hosted, single binary, SQLite-backed.

## Features

- **Review requested** → DMs the reviewer with PR details (file count, diff stats). Message updates in-place when the PR merges or closes.
- **Live activity** → Review-requested DMs update in real time with approval status and comment counts as activity happens.
- **Review submitted** (approved / changes requested) → DMs the PR author.
- **PR comments & @-mentions** → DMs the relevant recipients with the comment body.
- **Reply from Slack** → Opens a modal; submits to GitHub as a comment (threaded for inline review comments).
- **React from Slack** → 👍 👀 🎉 quick buttons + 👎 😄 😕 ❤️ 🚀 overflow menu → posts a GitHub reaction.
- **CI check failures** → DMs when a check run fails on a PR branch, with check name, repo, and branch.
- **Display names** → Shows full names (fetched from GitHub profiles) instead of usernames, with a 4-hour cache.
- **Self-service account linking** → `/link-github` slash command initiates GitHub OAuth; no manual mapping needed.
- **Admin dashboard** → GitHub OAuth-protected page at `/admin` showing all linked accounts.

## Architecture

Single Go binary backed by SQLite. No external database required.

```
GitHub webhook ──► POST /webhooks/github
Slack slash cmd ──► POST /slack/commands
Slack interactivity ──► POST /slack/interactions
GitHub OAuth ──► GET /oauth/github
               GET /oauth/github/callback
Admin dashboard ──► GET /admin
Health check ──► GET /healthz
```

## Quick start (Docker Compose)

```bash
cp docker-compose.yml my-compose.yml
# Edit my-compose.yml: set BASE_URL and fill in the env vars below.

docker compose up -d
```

Required environment variables:

| Variable | Description |
|---|---|
| `BASE_URL` | Public URL of this service (e.g. `https://pr-notifier.example.com`) |
| `GITHUB_CLIENT_ID` | GitHub OAuth App client ID |
| `GITHUB_CLIENT_SECRET` | GitHub OAuth App client secret |
| `GITHUB_WEBHOOK_SECRET` | Secret configured on your GitHub webhook |
| `GITHUB_APP_ID` | GitHub App ID |
| `GITHUB_PRIVATE_KEY` | GitHub App private key (PEM format; literal `\n` is supported) |
| `GITHUB_INSTALLATION_ID` | GitHub App installation ID |
| `SLACK_BOT_TOKEN` | Slack bot token (`xoxb-...`) |
| `SLACK_SIGNING_SECRET` | Slack app signing secret |
| `DB_PATH` | SQLite file path (default: `/data/pr-notifier.db`) |

## Kubernetes (Helm)

```bash
helm install pr-notifier ./helm/pull-request-notifier \
  --namespace pr-notifier --create-namespace \
  --set config.baseURL=https://pr-notifier.example.com \
  --set secrets.githubClientID=... \
  --set secrets.githubClientSecret=... \
  --set secrets.githubWebhookSecret=... \
  --set secrets.slackBotToken=... \
  --set secrets.slackSigningSecret=... \
  --set gateway.enabled=true \
  --set gateway.hostname=pr-notifier.example.com \
  --set "gateway.parentRefs[0].name=my-gateway" \
  --set "gateway.parentRefs[0].namespace=gateway-system"
```

For production, use `existingSecret` to reference a pre-created Kubernetes Secret instead of passing values directly.

## Setup

### 1. GitHub App

Create a GitHub App at **Settings → Developer settings → GitHub Apps → New GitHub App**:

**General**:
- **Homepage URL**: `https://your-domain`
- **Callback URL**: `https://your-domain/oauth/github/callback`
- **Webhook URL**: `https://your-domain/webhooks/github`
- **Webhook secret**: generate a strong secret (this becomes `GITHUB_WEBHOOK_SECRET`):
  ```bash
  openssl rand -hex 32
  ```

**Permissions**:
- **Repository permissions**:
  - Pull requests: Read & write (post comments, reactions)
  - Issues: Read-only (required for PR comment notifications)
  - Contents: Read-only
  - Checks: Read-only (for CI failure notifications)
- **Organization permissions**:
  - Members: Read-only (for admin dashboard access control)
- **Account permissions**:
  - Email addresses: Read-only

**Subscribe to events**:
- Pull request
- Pull request review
- Pull request review comment
- Issue comment
- Check run

After creating the app, note the **App ID**, **Client ID**, and generate a **Client secret** — these become `GITHUB_APP_ID`, `GITHUB_CLIENT_ID`, and `GITHUB_CLIENT_SECRET`. Generate a private key and save it — this becomes `GITHUB_PRIVATE_KEY`.

Install the app on your organization and note the **Installation ID** (visible in the URL) — this becomes `GITHUB_INSTALLATION_ID`. Webhooks are delivered automatically for all repos the app has access to — no per-repo configuration needed.

### 2. Slack App

Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From a manifest**. Paste the contents of [`slack-app-manifest.yaml`](slack-app-manifest.yaml), replacing `YOUR_DOMAIN` with your public URL. Install the app to your workspace.

Note the **Bot Token** (`xoxb-...`) and **Signing Secret** from the app settings — these become `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET`.

### 3. Link accounts

Each engineer runs `/link-github` in Slack once. This initiates GitHub OAuth and stores the `github_username ↔ slack_user_id` mapping. The data is entirely re-creatable — if the SQLite file is lost, users simply run `/link-github` again.

## Development

```bash
git clone https://github.com/voriteam/pull-request-notifier
cd pull-request-notifier
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go build .
```

Requires `gcc` for SQLite (CGO). On macOS: `xcode-select --install`. On Ubuntu/Debian: `apt install gcc`.

## Data & backups

All state lives in a single SQLite file. The data is low-stakes (only account mappings and message timestamps for in-place edits — no message content). For production, mount the `/data` volume on persistent storage. If you want continuous replication, [Litestream](https://litestream.io) works well as a sidecar.

## Observability

Tracing and log export are optional. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable OpenTelemetry trace and log export via OTLP HTTP.

| Variable | Description |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint (e.g. `http://otel-collector:4318`) |

Standard OTel env vars (`OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_HEADERS`) are also supported. SDK-level export errors are surfaced as structured `otel.sdk.error` log entries.

In Helm, `OTEL_SERVICE_NAME` defaults to the `app.kubernetes.io/name` pod label. Override with `otel.serviceName` or `nameOverride`.

Helm:
```bash
helm install pr-notifier ./helm/pull-request-notifier \
  --set otel.enabled=true \
  --set otel.endpoint=http://otel-collector:4318
```

## License

MIT
