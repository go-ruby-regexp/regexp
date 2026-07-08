# frozen_string_literal: true
#
# Pure-Ruby regular-expression usage, backed by the Onigmo-compatible engine in
# go-embedded-ruby (rbgo). Regexp is a core feature, so no `require` is needed.
# Run it with:  rbgo examples/regexp_usage.rb

# Named captures.
email = "jean.dupont@example.gouv.fr"
if (m = email.match(/\A(?<user>[^@]+)@(?<domain>[^@]+)\z/))
  puts "user:   #{m[:user]}"
  puts "domain: #{m[:domain]}"
end

# Scanning and splitting.
p "a1b2c3".scan(/([a-z])(\d)/)
p "one two   three".split(/\s+/)

# Substitution with a block.
puts "HELLO".gsub(/[AEIOU]/) { |v| v.downcase }
