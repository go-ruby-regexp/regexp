/*
 * onig_bench.c — Onigmo (C) side of the go-ruby-regexp performance-parity
 * harness. Reads the materialised corpus from stdin as TSV produced by
 * `bench dump`:
 *
 *     name <TAB> re2 <TAB> base64(pattern) <TAB> base64(haystack)
 *
 * For each case it compiles the pattern (best-of-N single compiles) and runs a
 * full leftmost search over the haystack (best-of-N), emitting the same CSV
 * schema as the Go harness:
 *
 *     engine,case,compile_ns,match_ns_per_op,mb_per_s,matched,begin,end
 *
 * Patterns are compiled with the Ruby syntax + UTF-8 encoding, the same
 * configuration MRI uses, so this is the authoritative C-Onigmo bar.
 *
 * Single-threaded, monotonic-clock timing (CLOCK_MONOTONIC), best (minimum) of
 * OUTER_REPS timed batches with auto-scaled inner iterations — the same protocol
 * as the Go side.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <time.h>
#include "onigmo.h"

#define OUTER_REPS 12
#define MIN_INNER 50
#define MIN_BATCH_NS 50000000LL /* 50 ms */

static long long now_ns(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return (long long)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

/* base64 decode (standard alphabet). Returns malloc'd buffer, sets *outlen. */
static unsigned char *b64decode(const char *in, size_t *outlen) {
  static int8_t T[256];
  static int init = 0;
  if (!init) {
    memset(T, -1, sizeof(T));
    const char *A = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    for (int i = 0; i < 64; i++) T[(unsigned char)A[i]] = (int8_t)i;
    init = 1;
  }
  size_t n = strlen(in);
  unsigned char *out = malloc(n); /* upper bound */
  size_t o = 0;
  int val = 0, bits = -8;
  for (size_t i = 0; i < n; i++) {
    unsigned char c = (unsigned char)in[i];
    if (c == '=' || c == '\n' || c == '\r') break;
    int d = T[c];
    if (d < 0) continue;
    val = (val << 6) | d;
    bits += 6;
    if (bits >= 0) {
      out[o++] = (unsigned char)((val >> bits) & 0xFF);
      bits -= 8;
    }
  }
  *outlen = o;
  return out;
}

/* Compile once; on error print to stderr and exit. */
static regex_t *compile_once(const unsigned char *pat, size_t patlen) {
  regex_t *reg = NULL;
  OnigErrorInfo einfo;
  int r = onig_new(&reg, pat, pat + patlen, ONIG_OPTION_DEFAULT,
                   ONIG_ENCODING_UTF8, ONIG_SYNTAX_RUBY, &einfo);
  if (r != ONIG_NORMAL) {
    OnigUChar s[ONIG_MAX_ERROR_MESSAGE_LEN];
    onig_error_code_to_str(s, r, &einfo);
    fprintf(stderr, "onig compile error: %s\n", s);
    exit(1);
  }
  return reg;
}

int main(void) {
  char line[1 << 20];
  printf("engine,case,compile_ns,match_ns_per_op,mb_per_s,matched,begin,end\n");

  while (fgets(line, sizeof(line), stdin)) {
    /* parse name \t re2 \t patb64 \t hayb64 */
    char *name = strtok(line, "\t");
    char *re2 = strtok(NULL, "\t");
    char *patb64 = strtok(NULL, "\t");
    char *hayb64 = strtok(NULL, "\t\n");
    if (!name || !re2 || !patb64 || !hayb64) continue;

    size_t patlen, haylen;
    unsigned char *pat = b64decode(patb64, &patlen);
    unsigned char *hay = b64decode(hayb64, &haylen);

    /* ---- compile best-of-N (auto-scaled inner) ---- */
    int inner = MIN_INNER;
    for (;;) {
      long long t0 = now_ns();
      for (int i = 0; i < inner; i++) {
        regex_t *r = compile_once(pat, patlen);
        onig_free(r);
      }
      long long el = now_ns() - t0;
      if (el >= MIN_BATCH_NS || inner >= (1 << 22)) break;
      long long factor = el > 0 ? (MIN_BATCH_NS / el) + 1 : 8;
      if (factor < 2) factor = 2;
      inner *= (int)factor;
    }
    long long best_comp = INT64_MAX;
    for (int rep = 0; rep < OUTER_REPS; rep++) {
      long long t0 = now_ns();
      for (int i = 0; i < inner; i++) {
        regex_t *r = compile_once(pat, patlen);
        onig_free(r);
      }
      long long el = now_ns() - t0;
      if (el < best_comp) best_comp = el;
    }
    long long comp_ns = best_comp / inner;

    /* ---- one compiled regex for the match loop + verification ---- */
    regex_t *reg = compile_once(pat, patlen);
    OnigRegion *region = onig_region_new();
    const unsigned char *end = hay + haylen;
    int matched = 0;
    long beg0 = -1, end0 = -1;
    int rr = onig_search(reg, hay, end, hay, end, region, ONIG_OPTION_NONE);
    if (rr >= 0) {
      matched = 1;
      beg0 = region->beg[0];
      end0 = region->end[0];
    }

    /* ---- match best-of-N (auto-scaled inner) ---- */
    inner = MIN_INNER;
    for (;;) {
      long long t0 = now_ns();
      for (int i = 0; i < inner; i++) {
        (void)onig_search(reg, hay, end, hay, end, region, ONIG_OPTION_NONE);
      }
      long long el = now_ns() - t0;
      if (el >= MIN_BATCH_NS || inner >= (1 << 22)) break;
      long long factor = el > 0 ? (MIN_BATCH_NS / el) + 1 : 8;
      if (factor < 2) factor = 2;
      inner *= (int)factor;
    }
    long long best_match = INT64_MAX;
    for (int rep = 0; rep < OUTER_REPS; rep++) {
      long long t0 = now_ns();
      for (int i = 0; i < inner; i++) {
        (void)onig_search(reg, hay, end, hay, end, region, ONIG_OPTION_NONE);
      }
      long long el = now_ns() - t0;
      if (el < best_match) best_match = el;
    }
    long long match_ns = best_match / inner;
    double mbps = match_ns > 0 ? ((double)haylen / 1e6) / ((double)match_ns / 1e9) : 0.0;

    printf("onigmo,%s,%lld,%lld,%.1f,%d,%ld,%ld\n",
           name, comp_ns, match_ns, mbps, matched, beg0, end0);

    onig_region_free(region, 1);
    onig_free(reg);
    free(pat);
    free(hay);
  }

  onig_end();
  return 0;
}
