#!/usr/bin/env sh
set -eu
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init \
  --quiet --generalInfo main.go --parseInternal \
  --output api/generated --outputTypes json
