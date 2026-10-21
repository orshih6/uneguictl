# uneguictl

Search [unegui.mn](https://www.unegui.mn) — Mongolia's classifieds site — from a shell, a
script or an AI assistant. No account, no sign-in: it reads the same public JSON the website
loads.

```console
$ uneguictl search iphone 15 pro --match --sort cheapest -n 3
998 adverts match "iphone 15 pro"; 3 kept after --match/--no-promoted.
PRICE        TITLE                                                LOCATION                          POSTED         URL
1.7 сая ₮*   [VIP] Apple iPhone 15 Pro, 256 gb, Natural Titanium  Улаанбаатар — Хан-Уул — Хансвил…  2 өдрийн өмнө  https://www.unegui.mn/adv/10828312_…/
1.75 сая ₮*  [Онцгой] Apple iPhone 15 Pro, 128 gb, White Titanium  Улаанбаатар — Баянгол — 2-р хор…  1 өдрийн өмнө  https://www.unegui.mn/adv/10831461_…/
1.75 сая ₮   [Онцгой] Apple iPhone 15 Pro, 256 gb, White Titanium  Улаанбаатар — Баянгол — 19-р хо…  3 өдрийн өмнө  https://www.unegui.mn/adv/10827105_…/

$ uneguictl show https://www.unegui.mn/adv/10828312_apple-iphone-15-pro-256-gb-natural-titanium/
```

`*` after a price means the seller added a note such as "Үнэ тохирно" (negotiable).

## Install

```bash
make build      # bin/uneguictl
make install    # into $GOBIN
```

## Commands

| Command | What it does |
|---|---|
| `search [words…]` | Adverts by words, `--category`, `--min`/`--max` price, `--filter`, `--sort`. `-n` how many (default 30, at most 1000). |
| `show <url or slug>` | One advert in full: price, features, description, photos, seller. `--phone` adds the seller's number. |
| `categories [category]` | The category tree with paths; with a category, the ones directly under it (down to single phone or car models). |
| `filters <category>` | A category's filters and their values, for `search --filter`. |

Every command takes `--json`. Results go to standard output; the "N adverts match" line and
errors go to standard error, so `--json | jq` stays clean.

### Searching well

- **Narrow with a category.** The site's word search is loose: "iphone 15" also finds printer
  toner with "15" in the title. Find the path with `uneguictl categories`, then
  `uneguictl categories <path>` to go deeper, and pass it as `-c`. A category URL copied from
  the browser works too.
- **`--match`** keeps only adverts whose title contains every search word as a whole word
  (`15` does not match `150`).
- **Prices** take `500k`, `1.5m`, `2.5 сая` or plain `1500000` (₮).
- **Filters**: `uneguictl filters avto-mashin/-avtomashin-zarna` lists e.g.
  `attrs__hrof  mehanik=Механик avtomat=Автомат`. Pass `-f attrs__hrof=avtomat`; several values
  `-f attrs__bagtaamzh=7,12`; numeric ranges `-f attrs__year_min=2018 -f attrs__milliage_max=100000`.
- **Paid adverts.** The site always lists VIP adverts first, then Онцгой, then the rest,
  whatever the sort. With `--sort cheapest`/`expensive`, uneguictl keeps fetching until it has
  `-n` regular adverts and sorts them together with every promoted one, so the order is the true
  one. `--no-promoted` leaves paid adverts out.

```bash
uneguictl search -c utas--dugaar/gar-utas-ba-utasnuud/iphone/iphone-15-pro --max 2.5m -s cheapest
uneguictl search -c avto-mashin/-avtomashin-zarna prius -f attrs__year_min=2018 -f attrs__hrof=avtomat
uneguictl search macbook --json | jq -r '.items[] | "\(.price)\t\(.title)\t\(.url)"'
```

### The seller's phone

`show --phone` fetches the number the site shows to anyone who clicks "show phone". The site
counts each such request as a phone view that the seller sees, so it is never fetched unless
asked for, and never for a whole search.

## How it works

The site is a Next.js app over a JSON API (`/api/v2/spa/…`); uneguictl calls the endpoints the
browser calls:

| Endpoint | Used for |
|---|---|
| `adverts/listing/<category or "search">/?q=&ordering=&price_min=&price_max=&page_size=` | search; pages by the `next_page` cursor it returns |
| `adverts/card/<id>_<slug>/` | `show` (the id alone is a 404) |
| `adverts/filters/<category>/` | `filters` |
| `rubrics/rubricator/` | `categories` |
| `/api/items/get_phone/<id>/` | `show --phone` |

Cloudflare sits in front and refuses clients whose TLS handshake is not a browser's (plain
`curl` gets a 403 "Just a moment…" page). uneguictl uses
[tls-client](https://github.com/bogdanfinn/tls-client) with a Chrome profile, which passes
without solving any challenge. If Cloudflare tightens this, commands exit with status 3.

It breaks when unegui.mn changes its API. The response shapes live in `internal/unegui`.

## Exit status

| | |
|---|---|
| 0 | success, including a search that found nothing |
| 1 | any other error |
| 3 | Cloudflare refused the request; wait and try again |
| 4 | no such advert (or it was removed) or category |
| 130 | interrupted |
