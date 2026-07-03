#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/splendideXmendax/mysmpp.git}"
BRANCH="${BRANCH:-main}"
APP_DIR="${APP_DIR:-}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:19087/healthz}"

log() {
  printf '[deploy] %s\n' "$*"
}

find_repo() {
  if [ -n "$APP_DIR" ] && [ -d "$APP_DIR/.git" ]; then
    printf '%s\n' "$APP_DIR"
    return 0
  fi
  for dir in /root/mysmpp /opt/mysmpp /srv/mysmpp /home/*/mysmpp; do
    if [ -d "$dir/.git" ] && git -C "$dir" remote -v | grep -q 'splendideXmendax/mysmpp'; then
      printf '%s\n' "$dir"
      return 0
    fi
  done
  return 1
}

if repo_dir="$(find_repo)"; then
  log "using existing repo: $repo_dir"
else
  repo_dir="${APP_DIR:-/opt/mysmpp}"
  log "cloning repo to $repo_dir"
  mkdir -p "$(dirname "$repo_dir")"
  git clone "$REPO_URL" "$repo_dir"
fi

cd "$repo_dir"
log "repo before: $(git rev-parse --short HEAD 2>/dev/null || true)"
git fetch origin "$BRANCH"
git checkout "$BRANCH"
git pull --ff-only origin "$BRANCH"
log "repo after: $(git rev-parse --short HEAD)"

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 && [ -f docker-compose.yml ]; then
  log "deploying with docker compose"
  docker compose up -d --build
elif command -v docker-compose >/dev/null 2>&1 && [ -f docker-compose.yml ]; then
  log "deploying with docker-compose"
  docker-compose up -d --build
else
  log "docker compose not found; building binary and restarting systemd service"
  if ! command -v go >/dev/null 2>&1; then
    log "go toolchain not found and docker compose unavailable"
    exit 1
  fi
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /usr/local/bin/mysmpp ./cmd/mysmpp
  chmod +x /usr/local/bin/mysmpp
  systemctl restart mysmpp
fi

log "waiting for health check"
for i in $(seq 1 30); do
  if curl -fsS "$HEALTH_URL"; then
    printf '\n'
    log "deploy ok"
    exit 0
  fi
  sleep 1
done

log "health check failed: $HEALTH_URL"
exit 1
