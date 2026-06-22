# ruby_bench.rb — Ruby (MRI) side of the go-ruby-regexp performance-parity
# harness. MRI's Regexp is backed by Onigmo, so this is a reproducible proxy for
# the C-Onigmo bar (it carries interpreter/method-dispatch overhead the C harness
# does not, so treat it as an upper bound on Onigmo-via-Ruby, not raw C speed).
#
# Reads the materialised corpus from stdin as TSV produced by `bench dump`:
#     name <TAB> re2 <TAB> base64(pattern) <TAB> base64(haystack)
# and emits the same CSV schema as the other harnesses:
#     engine,case,compile_ns,match_ns_per_op,mb_per_s,matched,begin,end
#
# Same best-of-N protocol: minimum over OUTER_REPS timed batches with auto-scaled
# inner iterations, monotonic clock.
require "base64"

OUTER_REPS   = 12
MIN_INNER    = 50
MIN_BATCH_NS = 50_000_000 # 50 ms

def now_ns
  Process.clock_gettime(Process::CLOCK_MONOTONIC, :nanosecond)
end

# best_ns runs the block with an auto-scaled inner count, returns ns/op (min).
def best_ns
  inner = MIN_INNER
  loop do
    t0 = now_ns
    inner.times { yield }
    el = now_ns - t0
    break if el >= MIN_BATCH_NS || inner >= (1 << 22)
    factor = el > 0 ? (MIN_BATCH_NS / el) + 1 : 8
    factor = 2 if factor < 2
    inner *= factor
  end
  best = Float::INFINITY
  OUTER_REPS.times do
    t0 = now_ns
    inner.times { yield }
    el = now_ns - t0
    best = el if el < best
  end
  best / inner
end

puts "engine,case,compile_ns,match_ns_per_op,mb_per_s,matched,begin,end"

STDIN.each_line do |line|
  name, _re2, patb64, hayb64 = line.chomp.split("\t")
  next unless name && patb64 && hayb64
  pat = Base64.decode64(patb64).force_encoding("UTF-8")
  hay = Base64.decode64(hayb64).force_encoding("UTF-8")
  bytelen = hay.bytesize

  comp_ns = best_ns { Regexp.new(pat) }

  re = Regexp.new(pat)
  m = re.match(hay)
  matched = m ? 1 : 0
  # Byte offsets (the C/Go harnesses report byte offsets).
  beg = m ? m.begin(0) : -1
  fin = m ? m.end(0) : -1
  if m
    beg = hay[0...m.begin(0)].bytesize
    fin = hay[0...m.end(0)].bytesize
  end

  match_ns = best_ns { re.match?(hay) }
  mbps = match_ns > 0 ? (bytelen.to_f / 1e6) / (match_ns / 1e9) : 0.0

  printf("ruby,%s,%d,%d,%.1f,%d,%d,%d\n", name, comp_ns, match_ns, mbps, matched, beg, fin)
end
