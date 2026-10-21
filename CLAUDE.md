# CLAUDE.md

Guidance for working in this repo. The command reference is `README.md`.

## Working on the code

- Go, cobra CLI. `internal/unegui` talks to unegui.mn (raw API shapes → `Item`, `Advert`,
  `Category`, `Filter`), `internal/cli` the commands, `internal/output` tables and fields,
  `internal/watch` the watch bot's battery parsing, state file and Telegram client.
- `make check && make build` before finishing. Tests never contact unegui.mn: they use
  `internal/unegui/testdata/` and `httptest`. The fixtures have the seller's name and id
  replaced; keep it that way when refreshing them.
- To see what the site sends, `bin/uneguictl … --json`, or fetch the API directly with a
  browser-impersonating client (`uv run --with curl_cffi`); plain `curl` gets a Cloudflare 403.

## Invariants

- **Requests go through tls-client with a Chrome profile, and the User-Agent names the same
  Chrome version.** net/http is refused by Cloudflare. Bump both together.
- **Read only.** No command posts, messages, favourites or signs in on unegui.mn. (`watch`
  sends Telegram messages, to the one chat in `TELEGRAM_CHAT_ID`, and obeys no other chat.)
- **The phone number is opt-in, one advert at a time.** `get_phone` counts a "phone view"
  the seller sees. Never call it from `search`, in a loop, or in tests against the live site.
  `watch` fetches one only for a `/phone` message from the user; its checks never call it.
- **An unknown category is an error, not a site-wide search.** The API answers 200 for any
  path and silently searches everything; `listing` checks the answer's `rubric` id, and
  `Filters` checks the category with a one-advert listing first. Keep both.
- **Price sorting is fixed up client-side.** The API orders VIP → Онцгой → regular, each tier
  sorted by price. `search --sort cheapest/expensive` fetches until it has `--limit` regular
  adverts and sorts the lot; don't drop that loop condition.
- `--limit` is capped at 1000 (ten requests); `page_size` above 100 is a 422.
