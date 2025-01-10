# Overview

Implements a minimal API for returning the blockchain's total and circulating supply in the format required by CoinMarketCap. Connects to an existing `walletd` node for consensus data.

Follows the guidelines layed out here: https://support.coinmarketcap.com/hc/en-us/articles/360043396252-Supply-Circulating-Total-Max

# Usage
```
cmcd -dir ~/cmcd -api "http://localhost:9980/api" -password "my walletd password"
```

## Building
```
go build -o bin/ ./cmd/cmcd
```

### Testnet
```
go build -o bin/ -tags testnet ./cmd/cmcd
```