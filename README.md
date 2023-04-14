# Overview
Implements a minimal Sia subscriber for processing data from Sia's consensus set
and transaction pool.

# Usage
```
subscriberd --dir ~/subscriber
```

## Building
```
go build -o bin/ ./cmd/subscriberd
```

### Testnet
```
go build -o bin/ -tags testnet ./cmd/subscriberd
```