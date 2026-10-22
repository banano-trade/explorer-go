# Banano Explorer

A fast, server-rendered block explorer for [Banano](https://banano.cc). One Go binary; the only thing it
needs is a Banano node (its RPC and websocket). No Node.js, no database, no external API required.

What's in it: accounts (history with type filters, jump to height, CSV export, receivable, delegators,
lifetime insights), blocks (where a payment is, raw JSON), live confirmations with confirm times,
representatives with a transparent rep score (A–D), "pick a good rep", network stats and charts,
WeGoodFam™ health page plus `/api/health` for bots, rich list, burn leaderboard, known accounts,
QR payment codes, share images, personal labels, keyboard shortcuts, light and dark themes.

## Run

    go build -o banano-explorer ./cmd/explorer
    ./banano-explorer -node http://[::1]:7072 -ws ws://[::1]:7079 -state ./data/state

Flags (`-h` for all): `-listen` (default 127.0.0.1:9990), `-public-url` (for share links), `-fiat=false`
to skip the CoinGecko price lookup, `-known DIR` to use your own known-accounts list,
`-import-uptime DIR` to import spyglass-api rep uptime history once.

The node needs RPC and websocket enabled. The explorer only calls read-only RPC actions.

## Develop

    go test ./...                                   # unit tests
    NODE_URL='http://[::1]:7072' go test ./...      # plus tests against a live node

Deployment: `deploy/banano-explorer.service` is a sandboxed systemd unit; put nginx in front.

## Credits

Known-account names come from [spyglass-api](https://github.com/dev-ptera/spyglass-api) (MIT).
Fonts: Outfit and Overpass Mono (SIL Open Font License). Colors from the Banano press kit.
