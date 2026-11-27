#!/usr/bin/env bash
set -euo pipefail

if (( $# > 1 )); then
  printf '用法: %s [输出 .tar.gz 路径]\n' "$0" >&2
  exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
output=${1:-$root/dist/sre-aiobs-single-node.tar.gz}
case "$output" in
  *.tar.gz) ;;
  *) printf '输出文件名须以 .tar.gz 结尾。\n' >&2; exit 2 ;;
esac

if ! git -C "$root" rev-parse --verify HEAD >/dev/null 2>&1; then
  printf '请从已提交的 Git 项目目录生成部署包。\n' >&2
  exit 1
fi
if [[ -n "$(git -C "$root" status --porcelain)" ]]; then
  printf '请先提交项目改动，再从 HEAD 生成可复现的部署包。\n' >&2
  exit 1
fi
if git -C "$root" ls-tree -r --name-only HEAD | grep -E '(^|/)(\.env($|\.)|config\.yaml$|[^/]+\.mmdb$)' | grep -v '^tests/config\.yaml$' >/dev/null; then
  printf '提交包含本地配置或凭据文件，已中止打包。\n' >&2
  exit 1
fi

mkdir -p "$(dirname "$output")"
output=$(cd "$(dirname "$output")" && pwd)/$(basename "$output")
temporary=$(mktemp "$output.XXXXXX")
trap 'rm -f "$temporary"' EXIT
git -C "$root" archive --format=tar.gz --prefix=sre-aiobs/ HEAD >"$temporary"
chmod 0644 "$temporary"
mv -f "$temporary" "$output"
printf '部署包 %s\n' "$output"
