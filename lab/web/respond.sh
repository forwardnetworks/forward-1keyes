#!/bin/sh
# One tiny HTTP response per connection: read the request first, then answer.
while IFS= read -r line; do
  [ "$line" = "$(printf '\r')" ] && break
  [ -z "$line" ] && break
done
printf 'HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nok\n'
