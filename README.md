# Go Order System

A small order processing system built as three Go services — written to show how I
think about backend architecture, not to sell a product.

The code is deliberately modest in size. The reasoning behind it is the point, and it
lives in **[Architecture.md](Architecture.md)**.

---

## What it does

A client places an order. The order service reserves stock from inventory over gRPC,
saves the order, and publishes an event. The notification service picks that event up
and tells the customer.

```mermaid
graph LR
    C[Client] -->|REST| O[order-service]
    O -->|gRPC| I[inventory-service]
    O -->|RabbitMQ| N[notification-service]
```

| Service | Responsibility | Exposes |
| --- | --- | --- |
| **order** | Accepts and manages orders | REST API, RabbitMQ events |
| **inventory** | Tracks and reserves stock | gRPC API |
| **notification** | Sends notifications | consumes events |

That is the entire feature set, and it is small on purpose: enough surface for the
problems worth showing — partial failure, contracts between services, consistency
across a network — without the noise of a real product.

---

## Why this exists

Plenty of showcase projects demonstrate that the author can wire libraries together.
This one tries to demonstrate judgment: why three services rather than one, where a
contract belongs, what happens when the third call in a chain fails, and when a pattern
is not worth what it costs.

Every significant decision is written down with its alternatives, its trade-offs, and
the conditions under which I would decide differently.

**[Architecture.md](Architecture.md)** answers:

- Why three services — and what would tell me the boundary is wrong
- Why one repository instead of three
- Where the gRPC and RabbitMQ contracts live, and which service owns them
- How a service stays testable while depending on Postgres, gRPC, and RabbitMQ
- What happens when a step in the middle of the flow fails
- What CI enforces, so the document cannot quietly drift away from the code

If you only read one thing here, read that.

---

## Stack

| Purpose | Choice | Why |
| --- | --- | --- |
| Language | Go | — |
| Synchronous calls | gRPC + Protocol Buffers | typed contract, generated client |
| Schema tooling | [buf](https://buf.build) | generation, linting, breaking-change detection |
| Asynchronous messaging | RabbitMQ | routing and dead-letter support without operating a log |
| Storage | PostgreSQL via `pgx` | transactions, which the outbox depends on |
| HTTP | standard library `net/http` | routing since Go 1.22 is enough; no framework earns its place here |
| Logging | `log/slog` | structured logging in the standard library |
| Migrations | `golang-migrate` | plain SQL, versioned |
| Integration tests | `testcontainers-go` | real Postgres and RabbitMQ, started by the test |
| Linting | `golangci-lint` | — |
| Local environment | Docker Compose | — |
| CI | GitHub Actions | — |

The pattern in that table is that most rows are the standard library or a single small
dependency. Reaching for a framework is a decision that should have to justify itself.

---

## Patterns and approaches

Each is argued for in [Architecture.md](Architecture.md); the short version:

**Boundaries**
- Service boundaries drawn around rules and failure modes, not around database tables
- Each service owns its public API in a separate, dependency-light Go module
- Go workspace with one module per service, so no service can reach into another's
  internals

**Inside a service**
- Ports and adapters: the domain declares interfaces, infrastructure satisfies them
- Domain packages import only the standard library, enforced in CI
- Applied where there are rules worth protecting — deliberately *not* applied to
  notification, which has none

**Across the network**
- Transactional outbox, so saving an order and publishing its event cannot disagree
- Idempotency keys on every call that changes state, so retrying is safe
- Expiring reservations, so a failure between two steps repairs itself
- Idempotent consumers, because every hop delivers at least once
- Dead-letter queues with three retries, and permanent failures parked immediately
  rather than retried pointlessly

---

## Tests

Every layer is tested, and the layers are separated so that the fast tests stay fast.

| Level | What it covers | Needs |
| --- | --- | --- |
| **Unit — domain** | Business rules in isolation: an order cannot be confirmed without a reservation; stock never goes below zero | nothing — no Docker, no network |
| **Unit — use cases** | Orchestration, against in-memory fakes of the ports | nothing |
| **Integration — adapters** | Repositories against real Postgres, publisher and consumer against real RabbitMQ, including retry and dead-letter behaviour | testcontainers |
| **Integration — contracts** | The whole workspace compiles, so every gRPC client matches its server's contract | nothing |
| **End to end** | Placing an order all the way through to a notification | Docker Compose |

```bash
make test              # unit tests — fast, no infrastructure
make test-integration  # adapters, against containers
make test-e2e          # all three services, via Docker Compose
make check             # lint, buf lint, buf breaking, import boundaries
```

Two things worth noticing:

- **Failure paths are tested, not just happy paths.** A timed-out reservation, a
  duplicate event, and a message that exhausts its retries all have tests. They are the
  cases the architecture exists for.
- **The architecture document is checked by CI.** Claims like "the domain depends on no
  infrastructure" are import checks that fail the build, not statements of intent.

---

## Repository layout

```
order-service/
  api/                     public contract — proto, generated code, topology names
    go.mod                 its own module: gRPC and protobuf, nothing else
  internal/order/
    domain/                rules — standard library only
    app/                   use cases
    adapter/               http, grpc, postgres, rabbitmq
  cmd/order/               wiring and entry point
  go.mod

inventory-service/         same shape
notification-service/      thinner — a consumer and an email adapter, no layering it
                           does not need

go.work                    ties the modules together for local development
buf.yaml                   proto workspace: lint and breaking-change rules
Architecture.md            the decisions, and why
```

---

## Running it locally

```bash
git clone <repo> && cd go-order-system-showcase
make up          # Postgres, RabbitMQ, and all three services
make seed        # a few products with stock
make demo        # place an order and watch it flow through
```

`go build ./...` works on a fresh clone with no code generation step — the generated
protobuf code is committed, for [reasons explained here](Architecture.md#trade-offs-accepted).

---

## Status

In progress. The architecture is settled and documented; the services are being built
against it.

- [ ] Contracts, buf setup, Go workspace
- [ ] inventory-service — gRPC server, reservations with expiry
- [ ] order-service — REST API, gRPC client, outbox
- [ ] notification-service — consumer, retries, dead-letter queue
- [ ] End-to-end flow and Docker Compose
- [ ] CI checks
