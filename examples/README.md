# Ruby examples

Pure-Ruby examples for the Onigmo-compatible regular-expression engine provided
by [go-embedded-ruby](https://github.com/go-embedded-ruby/ruby) (rbgo). Regexp
is a core feature, so no `require` is needed. Run them with the `rbgo`
interpreter:

```sh
rbgo examples/regexp_usage.rb
```

| File | Shows |
| --- | --- |
| [`regexp_usage.rb`](regexp_usage.rb) | Named captures, `scan`, `split`, block `gsub`. |

Each example is executed as-is under rbgo.
