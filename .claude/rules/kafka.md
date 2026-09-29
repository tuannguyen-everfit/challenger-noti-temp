# Kafka — topic naming, consumer groups, JSONHandler pattern

Read this whenever you touch `internal/infra/kafka`, `consumer_kafka.go`, or `<Feature>Topic` constants.

## 1. Topic naming

```
xx-<event-name>
```

Where `<event-name>` = `<source-feature>-<verb-past-tense>`.

- ✓ `xx-items-published`, `xx-users-registered`, `xx-orders-shipped`
- ✗ `items-published` — missing `xx-` prefix
- ✗ `xx-publish-item` — wrong verb form (must be past tense)

The `xx-` prefix is **this service's 2-char namespace** — replace `xx` with the prefix chosen for the service (challenger-service uses `ch-`). It pairs with `ef-` (everfit-core) and other 2-char service prefixes — when scrolling a Kafka UI, the prefix tells you which service produced the topic.

Export the constant from the producing feature:

```go
// internal/features/<producer>/service.go
const <Event>Topic = "xx-<producer>-<verb-past-tense>"
```

## 2. Consumer group naming

```
xx-<subscriber>-<event-name>
```

Subscriber name FIRST after the prefix, then the topic's event-name.

- ✓ `xx-watchlist-items-published`, `xx-notifications-users-registered`
- ✗ `xx-watchlist` — no event-name → conflicts when one subscriber consumes multiple topics
- ✗ `xx-items-published-watchlist` — subscriber order swapped

**Rule:** each `(subscriber, topic)` pair gets its OWN consumer group so offsets are independent. Declared as a `const ConsumerGroup` in the subscriber's `consumer_kafka.go`.

```go
// internal/features/<subscriber>/consumer_kafka.go
const ConsumerGroup = "xx-<subscriber>-<producer>-<verb-past-tense>"
```

## 3. Consumer wrapper — kafka.JSONHandler pattern

For typed events with JSON encoding, use `kafka.JSONHandler[T]` so each consumer doesn't reimplement decode + commit-on-malformed.

```go
// internal/features/<subscriber>/consumer_kafka.go
type EventHandler func(ctx context.Context, evt producer.Event) error

// Consumer is the feature's Kafka subscriber. Layer name is unprefixed per
// the four-layer naming standard ([layout.md](layout.md) §2.2). Infra goes in
// the filename + constructor name.
type Consumer struct {
    c       kafka.Consumer
    handler EventHandler
    log     *slog.Logger
}

func NewKafkaConsumer(c kafka.Consumer, handler EventHandler, log *slog.Logger) *Consumer {
    return &Consumer{
        c:       c,
        handler: handler,
        log:     log.With(slog.String("<subscriber>.consumer", "<producer>_<event>")),
    }
}

func (k *Consumer) Run(ctx context.Context) error {
    return k.c.Run(ctx, kafka.JSONHandler[producer.Event](k.log, k.handler))
}

func (k *Consumer) Close() error { return k.c.Close() }
```

### Decode policy (fixed)

`kafka.JSONHandler[T]` applies these on every message:

| Outcome | Action |
|---|---|
| Decode succeeds + handler returns `nil` | Commit offset, move on |
| Decode succeeds + handler returns error | Propagate — `kafka.Consumer.Run` retries N times then DLQs |
| Decode fails (malformed JSON) | **LOG error + return nil** (commit offset). Retrying malformed payload would never succeed. |

### Why a function value, not `*Service`

The consumer needs ONE method (`OnEvent`). Pass the bound method value:

```go
// in internal/app/app.go composition:
<subscriber>.NewKafkaConsumer(reader, <subscriber>Svc.OnEvent, log)
```

Not a struct field (would haul fields the consumer never reads). Not a one-method interface (overkill for same-package code). See [cross-feature.md](cross-feature.md) "One method → function value".

## 4. Idempotency

Kafka is at-least-once. Handlers MUST be idempotent — same event delivered twice must produce identical state.

- Dedupe on a payload field (event_id, version, …) when the operation isn't naturally idempotent.
- For "stamp a timestamp" operations like `MarkNotified`, setting the same value twice is a no-op — naturally idempotent.

## 5. Lifecycle wiring

The consumer goroutine starts in `app.go` only when Kafka is configured:

```go
if producer != nil {
    reader, _ := kafka.NewConsumer(kafka.ConsumerConfig{
        Brokers: cfg.Kafka.Brokers,
        Topic:   <producer>.<Event>Topic,
        GroupID: <subscriber>.ConsumerGroup,
    }, log)
    sub := <subscriber>.NewKafkaConsumer(reader, <subscriber>Svc.OnEvent, log)

    consumerCtx, cancelConsumer = context.WithCancel(ctx)
    defer cancelConsumer()
    safego.Go(func() {
        if rErr := sub.Run(consumerCtx); rErr != nil {
            log.Error("<subscriber> consumer exited", slog.String("error", rErr.Error()))
        }
    })
}
```

- `safego.Go` wraps the goroutine — a panic doesn't crash the process.
- `cancelConsumer` is deferred immediately after creation (vet-clean) — no lost cancel on early-return paths.
- Shutdown sequence: cancel ctx → close consumer → flush producer → close cache → close DB.

## 6. Consuming/publishing everfit-microservices topics (`au-`/`gb-`/`cdc-`/`mp-`)

Cross-service topics owned by everfit-microservices ride the shared `@everfit-io/module-queue` bus, which differs from a service-internal `xx-` topic in TWO ways that are **silent footguns** — get either wrong and the consumer runs, commits offsets, and syncs nothing, with no error.

### 6.1 Env topic prefix — `<AWS_MSK_PREFIX>-<topic>`

The bus namespaces every topic by environment: the producer publishes to `${AWS_MSK_PREFIX}-${topic}` (`dev-au-account-updated`, `prod-gb-profile-updated`, …). The logical name (`au-account-updated`) is NOT what's on the broker.

- Keep the LOGICAL base name as the feature const (`AccountUpdatedTopic = "au-account-updated"`).
- Apply the prefix at the composition root: `kafka.PrefixTopic(cfg.Kafka.TopicPrefix, profile.AccountUpdatedTopic)`.
- `SVC_KAFKA_TOPIC_PREFIX` MUST match the target cluster's `AWS_MSK_PREFIX` per environment — it does NOT default-derive from `cfg.Env` (this service's env labels need not equal the bus's). Default `"dev"`, matching the bus.

### 6.2 Message envelope — payload sits under `data`

`module-queue` serializes the wire value as `JSON.stringify(new QueueMessage(data, additionalInfo))`, i.e. `{"data": {<actual payload>}, "additionalInfo": …}`. The real fields are under `data`, NOT at the top level. (Kafka GZIP is transport-level — segmentio/kafka-go decompresses it transparently; the app sees plain JSON.)

Decode the envelope, dispatch the inner payload:

```go
type accountUpdatedEnvelope struct {
    Data AccountUpdatedEvent `json:"data"`
}

func (k *Consumer) Run(ctx context.Context) error {
    return k.c.Run(ctx, kafka.JSONHandler[accountUpdatedEnvelope](k.log,
        func(ctx context.Context, env accountUpdatedEnvelope) error {
            return k.handler(ctx, env.Data)
        }))
}
```

A top-level decode does NOT error (encoding/json ignores the unknown `data` key) — it yields an all-zero event that falls through every guard. The only catch is a test that feeds a REAL enveloped payload through `Run` (see `profile/consumer_kafka_test.go`); unit tests calling the handler with a hand-built struct will NOT catch the mismatch.

When PUBLISHING back to the bus (`gb-profile-updated`), wrap your payload in the same `{data: …}` shape and prefix the topic identically.

### 6.3 Field types (confirm against the producer's TS contract)

The everfit `account-updated.message.ts` types: `gender: GENDERS` is a **string** enum (`'-1'|'0'|'1'|'2'`), `birthday: Date` serializes to ISO-8601, `avatar` is an Image object with `.original`. A type mismatch on ANY field (e.g. decoding a number into a `string`) fails the WHOLE decode → "malformed event" → commit + skip. Mirror the producer's types exactly; use `json.RawMessage` for shape-flexible fields (avatar).

## STOP rules

- ✗ Subscribe with an ad-hoc consumer group name. Must be `xx-<subscriber>-<event-name>`.
- ✗ Reimplement the decode + commit-on-malformed pattern. Use `kafka.JSONHandler[T]`.
- ✗ Hold `*Service` in the consumer wrapper just to call one method. Pass the function value.
- ✗ Unwrapped `go func() { ... }()` for the consumer goroutine. Use `safego.Go` (see [concurrency.md](concurrency.md)).
- ✗ Synchronous call to another feature inside a Kafka handler. If you're in a handler you're reactive — use the event payload, don't poll back to the producer.
- ✗ Naming the consumer struct `KafkaConsumer` (or any infra-prefixed layer name). The struct is `Consumer`; the constructor is `NewKafkaConsumer`. See [layout.md](layout.md) §2.2.
- ✗ Subscribing to a bare everfit-microservices topic (`au-account-updated`) without the env prefix. The real topic is `<AWS_MSK_PREFIX>-au-account-updated` — use `kafka.PrefixTopic` (§6.1). Silent: receives zero messages.
- ✗ Decoding an everfit-microservices payload at the top level. The fields live under `data` (`module-queue` `{data, additionalInfo}` envelope, §6.2). Silent: every event decodes empty + skips.
- ✗ Trusting a unit test that calls the handler with a hand-built struct as proof the consumer works. It bypasses decode — add a test feeding a REAL enveloped wire payload through `Run` (§6.2).

## References

- `internal/infra/kafka/jsonhandler.go` — the reusable decode wrapper (`kafka.JSONHandler[T]`).
- `internal/infra/kafka/consumer.go` — Consumer interface + retry policy.
- `internal/infra/kafka/topic.go` — `kafka.PrefixTopic` for the env topic prefix (§6.1).
- `internal/features/profile/consumer_kafka.go` (lives in the upstream challenger-service repo — not in this template) — canonical cross-service consumer: envelope unwrap + base-name const.
- [layout.md](layout.md) §2.1 — `consumer_kafka.go` file slot and `Consumer` struct naming.
