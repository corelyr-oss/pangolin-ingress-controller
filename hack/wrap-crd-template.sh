#!/usr/bin/env bash
# Wraps a generated CRD manifest in the Helm conditional used by the chart.
#
# The CRD deliberately lives in chart/templates/ rather than chart/crds/:
# Helm never upgrades a CRD that already exists when it is shipped in crds/,
# which would strand users on whichever v1alpha1 schema they first installed.
set -euo pipefail

target="${1:?usage: wrap-crd-template.sh <file>}"
guard='{{- if .Values.crds.install }}'

if head -1 "$target" | grep -qF "$guard"; then
  exit 0
fi

tmp="$(mktemp)"
{
  echo "$guard"
  cat "$target"
  echo '{{- end }}'
} >"$tmp"
mv "$tmp" "$target"
