# Overview
Implements a minimal Sia subscriber for processing data from Sia's consensus set

# Usage
```
cmcd -dir ~/cmcd
```

## Building
```
go build -o bin/ ./cmd/cmcd
```

### Testnet
```
go build -o bin/ -tags testnet ./cmd/cmcd
```