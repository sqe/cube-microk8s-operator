#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

for command in go kustomize; do
  command -v "$command" >/dev/null 2>&1 || { echo "Required command not found: $command" >&2; exit 1; }
done

unformatted=$(gofmt -l api cmd internal)
if [[ -n "$unformatted" ]]; then
  echo "Go files need formatting:" >&2
  echo "$unformatted" >&2
  exit 1
fi
go test ./...
go vet ./...
PYTHONPATH=python python3 -m unittest discover -s python/tests -v
PYTHONPYCACHEPREFIX=${TMPDIR:-/tmp}/cube-operator-pycache python3 -m compileall -q -f python/cube_kopf python/tests
kustomize build config/default >/dev/null
kustomize build config/kopf >/dev/null
kustomize build demo/kind >/dev/null

if command -v terraform >/dev/null 2>&1; then
  terraform fmt -check -recursive
  terraform init -backend=false -input=false >/dev/null
  terraform validate
else
  echo "terraform not installed; skipping Terraform validation" >&2
fi
if command -v shellcheck >/dev/null 2>&1; then
  shellcheck scripts/*.sh
else
  echo "shellcheck not installed; skipping shell lint" >&2
fi
echo "Static and unit checks passed."
