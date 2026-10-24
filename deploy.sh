#!/bin/sh
# Builds the binary for the server (Linux ARM64) and replaces the running version.
set -eu

SERVER=deploy@91.99.174.129
DIR=shopping-list

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o shopping-list-linux .
scp shopping-list-linux "$SERVER:$DIR/shopping-list.new"
# The unit uses Restart=always, so stopping the process restarts it with the new binary.
ssh "$SERVER" "mv $DIR/shopping-list.new $DIR/shopping-list && pkill -TERM -x shopping-list"
