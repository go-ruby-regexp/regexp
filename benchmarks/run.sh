#!/usr/bin/env bash
# run.sh — reproducible cross-engine performance-parity harness for
# go-ruby-regexp (the from-scratch pure-Go Onigmo reimplementation) vs the C
# Onigmo it reimplements and Go's stdlib regexp (RE2).
#
# It builds the C Onigmo from source (pinned tag, isolated prefix), builds the Go
# harness (its own module, GOWORK=off so a stray workspace cannot interfere),
# materialises one shared corpus, and runs all engines with the same best-of-N
# protocol and a per-case wall-clock cap so a catastrophic backtracking case in
# the C/Ruby engines cannot wedge the run. Results are merged into results.csv.
#
# Usage:   ./run.sh            # full run -> results.csv
#          PER_CASE_TIMEOUT=90 ./run.sh
#
# Requirements: a C compiler, Go, and (optionally) Ruby for the Onigmo-via-MRI
# proxy column. Onigmo build needs autoconf/automake/libtool — provided here via
# pkgx if they are not already on PATH.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

ONIGMO_TAG="${ONIGMO_TAG:-Onigmo-6.2.0}"
ONIGMO_REPO="${ONIGMO_REPO:-https://github.com/k-takata/Onigmo.git}"
WORK="${WORK:-$HERE/.work}"
PREFIX="$WORK/onigmo-install"
PER_CASE_TIMEOUT="${PER_CASE_TIMEOUT:-90}"

mkdir -p "$WORK"

# ---- 1. Build Onigmo (C) from source, isolated, once. ----------------------
if [ ! -f "$PREFIX/lib/libonigmo.a" ]; then
  echo ">> building Onigmo C ($ONIGMO_TAG) ..."
  rm -rf "$WORK/Onigmo"
  git clone --depth 1 --branch "$ONIGMO_TAG" "$ONIGMO_REPO" "$WORK/Onigmo" 2>/dev/null \
    || git clone --depth 1 "$ONIGMO_REPO" "$WORK/Onigmo"
  pushd "$WORK/Onigmo" >/dev/null
  # Old C sources cast function pointers in a way clang's default -std=gnu23
  # rejects as errors; relax to gnu17 and downgrade those two diagnostics.
  CF="-O2 -std=gnu17 -Wno-incompatible-function-pointer-types -Wno-deprecated-non-prototype"
  if command -v autoreconf >/dev/null 2>&1; then
    autoreconf -i
  else
    pkgx +gnu.org/autoconf +gnu.org/automake +gnu.org/libtool +gnu.org/m4 -- autoreconf -i
  fi
  ./configure --prefix="$PREFIX" CFLAGS="$CF"
  make -j"$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
  make install
  popd >/dev/null
fi
ONIGMO_VER="$(grep -h 'ONIGMO_VERSION_' "$PREFIX/include/onigmo.h" | awk '{print $3}' | paste -sd. -)"
echo ">> Onigmo C version: $ONIGMO_VER"

# ---- 2. Build the C harness. ----------------------------------------------
cc -O2 -I"$PREFIX/include" onig/onig_bench.c -L"$PREFIX/lib" -lonigmo -o "$WORK/onig_bench"

# ---- 3. Build the Go harness + dump the shared corpus. --------------------
export GOWORK=off
go build -o "$WORK/bench" .
"$WORK/bench" dump > "$WORK/cases.tsv"
echo ">> $(wc -l < "$WORK/cases.tsv" | tr -d ' ') cases"

# ---- 4. Run ours + RE2 (Go side). -----------------------------------------
echo ">> running Go engines (ours + RE2) ..."
"$WORK/bench" > "$WORK/go_results.csv"

# ---- 5. Run Onigmo C, per-case with a wall-clock cap. ---------------------
echo ">> running Onigmo C ..."
run_capped() { # $1=label $2=runner-cmd...; reads one tsv line on stdin
  local label="$1"; shift
  : > "$WORK/${label}_results.csv"
  local first=1
  while IFS= read -r line; do
    local name out rc
    name="$(printf '%s' "$line" | cut -f1)"
    set +e
    out="$(printf '%s\n' "$line" | timeout "$PER_CASE_TIMEOUT" "$@" 2>/dev/null)"
    rc=$?
    set -e
    if [ "$first" = 1 ]; then printf '%s\n' "$out" | head -1 > "$WORK/${label}_results.csv"; first=0; fi
    if [ "$rc" -eq 124 ]; then
      # -2 sentinel == catastrophic backtracking past the cap (DNF).
      echo "${label},${name},-2,-2,0.0,-2,-2,-2" >> "$WORK/${label}_results.csv"
      echo "   [$name] TIMEOUT > ${PER_CASE_TIMEOUT}s (catastrophic backtracking)"
    else
      printf '%s\n' "$out" | tail -n +2 >> "$WORK/${label}_results.csv"
    fi
  done < "$WORK/cases.tsv"
}
DYLD_LIBRARY_PATH="$PREFIX/lib" LD_LIBRARY_PATH="$PREFIX/lib" \
  run_capped onigmo env DYLD_LIBRARY_PATH="$PREFIX/lib" LD_LIBRARY_PATH="$PREFIX/lib" "$WORK/onig_bench"

# ---- 6. Run Ruby (Onigmo-via-MRI proxy), if available. --------------------
if command -v ruby >/dev/null 2>&1; then
  echo ">> running Ruby (Onigmo via MRI) ..."
  run_capped ruby ruby ruby/ruby_bench.rb
else
  echo ">> ruby not found; skipping the MRI proxy column"
  : > "$WORK/ruby_results.csv"
fi

# ---- 7. Merge. ------------------------------------------------------------
{
  head -1 "$WORK/go_results.csv"
  for f in go onigmo ruby; do
    [ -s "$WORK/${f}_results.csv" ] && tail -n +2 "$WORK/${f}_results.csv"
  done
} > results.csv
echo ">> wrote results.csv"
column -t -s, results.csv
