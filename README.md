# sui-arb-demo

Two small, well-tested Go building blocks for on-chain market data on Sui:

- **`feed/`**: a checkpoint consumer that turns an unreliable stream into an
  ordered, gap-free, duplicate-free feed.
- **`clmm/`**: Cetus-style concentrated-liquidity (CLMM) math: Q64.64 sqrt
  prices, tick conversion, fee-aware swap steps, and multi-tick exact-input quotes.

There is no trading strategy, no keys and no wallet code. It's a showcase of the plumbing.

## Layout

```
feed/        Processor, Source/Subscription interfaces, GraphQL + JSON-RPC sources
clmm/        tick math, swap math, pool quoting
cmd/demo/    tail live mainnet checkpoints through the processor
cmd/quote/   quote a swap on a hardcoded synthetic pool
```

## Design

### Sequence-gap detection (`feed/`)

Sui checkpoints carry a strictly increasing sequence number, so the stream
itself tells you whether anything is missing. Transports don't guarantee
delivery: streams drop messages under load, replay on reconnect, and polling
APIs only show you the tip. Any state derived from checkpoints (pool
reserves, tick tables) silently goes wrong if a single one is skipped.

`Processor` keeps one cursor, `next`, and enforces:

- **Gap**: if `seq > next`, fetch `next .. seq-1` through `Source.Fetch` (with
  retries) and emit them *before* the newer checkpoint.
- **Duplicate or late**: if `seq < next`, drop it.
- **Disconnect**: reconnect with exponential backoff using `Stream(ctx, next)`,
  so it resumes exactly at the first unprocessed checkpoint.
- **Safety valves**: `MaxGap` rejects absurd jumps, and handler errors stop the
  processor with `Next()` as the correct resume point.

The handler therefore sees every checkpoint exactly once, in order. The tests
drive this with a scripted fake source that injects gaps, duplicates,
reordering, disconnects, replays, and transient fetch failures.

Two concrete sources ship with the package. Both poll the tip and rely on the
processor to backfill:

- `GraphQLSource`: the public Sui GraphQL endpoint (default for the demo).
- `JSONRPCSource`: `sui_getLatestCheckpointSequenceNumber` / `sui_getCheckpoint`.
  It's for nodes that still serve JSON-RPC, since public fullnodes have deprecated it.

A gRPC checkpoint stream plugs in by implementing the same two-method `Source` interface.

### Big-integer CLMM math (`clmm/`)

On-chain CLMMs compute in u128/u256 fixed point with specific rounding
directions. A float64 quote drifts by more than a fee tier on large pools, and
rounding the wrong way breaks invariants such as "input covers output".
Everything here uses `math/big` and mirrors the on-chain rounding:

- `SqrtPriceAtTick` reproduces the Cetus algorithm bit for bit (Q64.64 for
  negative ticks, Q32.96 for positive, truncating multiplies). It hits the
  on-chain constants exactly: tick 0 = 2^64, `MIN_SQRT_PRICE = 4295048016`,
  `MAX_SQRT_PRICE = 79226673515401279992447579055`. It's also checked against
  a 512-bit `big.Float` reference.
- `TickAtSqrtPrice` binary-searches the monotonic forward function, so it is
  exact by construction.
- `ComputeSwapStep` follows Cetus semantics: the fee is taken from the input
  (`FeeRateDenominator = 1e6`), input amounts round up and output amounts round down.
- `Pool.QuoteExactIn` walks initialized ticks in either direction, applies
  `liquidity_net` on each crossing, and honours price limits. It also reports
  ticks crossed, the end state, and whether liquidity ran out.

## Usage

Requires Go 1.22+.

```sh
go test ./...            # unit tests (no network)
go vet ./...

# Tail mainnet checkpoints; drop every 3rd streamed one to watch backfill work
go run ./cmd/demo -n 20 -drop-every 3
go run ./cmd/demo -api jsonrpc -url http://localhost:9000   # self-hosted node

# Quote on the synthetic pool
go run ./cmd/quote -in 50000000
go run ./cmd/quote -in 120000000 -a2b=false
```

Example quote output:

```
direction      a2b=true
amount in      50000000 (fee 125001)
amount out     49612249
price          1.00000000 -> 0.98887642
tick           0 -> -112 (crossed 1)
liquidity      10000000000 -> 8000000000
```

## License

MIT, see [LICENSE](LICENSE).
