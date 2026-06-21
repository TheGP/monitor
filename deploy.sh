#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY_NAME="monitor"
PM2_APP_NAME="monitor"

echo "========================================"
echo "DEPLOYING MONITOR"
echo "========================================"
cd "$SCRIPT_DIR"

# Optional git pull (only if this is a git repo)
if [ -d .git ]; then
    BEFORE_HASH=$(git rev-parse HEAD)
    echo "Pulling latest changes..."
    git pull origin master
    AFTER_HASH=$(git rev-parse HEAD)
    echo "Changes: $BEFORE_HASH → $AFTER_HASH"
fi

if ! command -v go &> /dev/null; then
    echo "go not found — install Go first"
    exit 1
fi

echo ""
echo "========================================"
echo "BUILD"
echo "========================================"
go build -o "$BINARY_NAME" .
echo "Build successful"

if ! command -v pm2 &> /dev/null; then
    echo "pm2 not found. Install: npm install -g pm2"
    exit 1
fi

echo ""
echo "========================================"
echo "PROCESS MANAGER"
echo "========================================"

if pm2 describe "$PM2_APP_NAME" > /dev/null 2>&1; then
    echo "Reloading $PM2_APP_NAME..."
    pm2 reload "$PM2_APP_NAME"
    echo "Reloaded"
else
    echo "Starting $PM2_APP_NAME..."
    pm2 start "./$BINARY_NAME" --name "$PM2_APP_NAME" --error /dev/null
    echo "Started"
fi

pm2 save

echo ""
echo "========================================"
echo "DEPLOY COMPLETE"
echo "========================================"
pm2 status
