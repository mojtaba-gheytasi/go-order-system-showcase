# AGENTS.md

Guidance for AI coding agents working in this repository. This is the single source of
truth; `CLAUDE.md` is a one-line import of this file and holds nothing of its own. New
guidance goes here, never there — a rule written into `CLAUDE.md` duplicates this file,
and the two then drift apart without anyone noticing.

## What this repository is

A showcase of distributed-systems decisions, not a product. Three Go services —
order (REST), inventory (gRPC), notification (AMQP consumer) — plus two contract
modules. The value is in the reasoning: `README.md` and `ARCHITECTURE.md` are the
primary artifacts, and the code exists to make their claims true. Treat a change to
behaviour as incomplete until the matching documentation sentence is corrected.

## How to explain, and how to build

**Explain things simply.** Use plain words and short sentences. Say the thing directly
instead of dressing it up. If a term is jargon, either drop it or explain it in the same
breath the first time it shows up.

**Length is not the problem — confusion is.** A longer explanation that lands is better
than a short one that has to be read twice. Do not squeeze an idea down until it turns
into a riddle. Take the extra paragraph. Add the small example. Spell out the step that
seems obvious, because it is usually the one that was skipped.

**The same rule applies to the code.** Prefer the straightforward version. A clever
one-liner that needs a comment to be readable is worse than three plain lines that need
none. Reach for an abstraction when there is a second caller, not because there might be
one later.

The one place to stay short is commit messages — see Conventions at the end.

## Commands

Everything runs through `make` from the repository root. `make help` lists all targets.

```bash
make check             # compose-config + buf lint + go vet + unit tests
make test              # unit tests, every module, -race
make test-integration  # -tags=integration, every module, needs a Docker daemon
make modules           # print the discovered Go modules
```

Module-wide targets (`deps fmt vet test test-integration`) loop over every `go.mod`
found by `find`, so a new service joins fmt, vet, test and CI without editing the
Makefile or the workflow. They stop at the first failing module.

Single test or single package — `cd` into the module first, there is no root module:

```bash
cd order-service && go test -race -run TestCreateOrderUsesCatalogPrices ./internal/order/application/
cd notification-service && go test -race -count=1 -tags=integration -run TestOnlyOneConcurrentClaimIsGranted ./internal/notification/adapter/outbound/postgres/
```

Integration tests are guarded by `//go:build integration` and start real Postgres and
RabbitMQ containers through testcontainers-go. They are slow; prefer the unit tests
while iterating.

Contracts (needs `buf` on the host; plugins are pinned remotely, nothing else to install):

```bash
make proto           # regenerate; output is committed
make proto-lint
make proto-breaking  # per module, against main
```

Local stack (`make dev` foreground, `make up` detached) plus per-service helpers:
`order-logs`, `inventory-db-shell`, `notification-migrate-up`, `rabbitmq-ui`, and so on.
Each service reads `<service>/.env` (copied from `.env.example`, git-ignored); the
Makefile `-include`s those files so Compose can interpolate the same values.

`make order-migrate-up` and `make sqlvet` need host tools: `brew install golang-migrate`
and `go install github.com/houqp/sqlvet@v1.2.0`.

There is no lint target and no golangci-lint config. `make check` is the closest thing,
and end-to-end, lint, and import-boundary checks are listed as outstanding rather than
stubbed in — the Makefile does not advertise checks that silently pass.

## Module layout

Five Go modules, no workspace file:

```
order-service/            order-service/api/        (protobuf only — an event has no client stub)
inventory-service/        inventory-service/api/    (gRPC + protobuf)
notification-service/
```

Contract modules are resolved with `replace` directives pointing at local paths, so
inventory deleting an RPC that order calls is a compile error in the pull request that
caused it. notification-service `replace`s `order-service/api` the same way.

Each contract module is its own buf workspace **on purpose**: a single root workspace
would let one service's `.proto` import another's and resolve it. Separate workspaces
make that import fail, which turns the boundary from a convention into a rule. Do not
add buf files at the repository root.

`make proto` generates into a temp directory and swaps `gen/` in only after buf
succeeds — a failed run (rate-limited plugin registry, malformed proto) must leave the
committed output untouched. CI then runs `make proto` and asserts
`test -z "$(git status --porcelain)"`; `git diff --exit-code` does not work here, because
both it and bare `git status --porcelain` exit 0 when the only difference is an
untracked file.

## Architecture

Ports and adapters, with the layering scaled to the rules each service actually has:

- **order-service** has four pieces — `domain/`, `application/`, `adapter/{inbound,outbound}/`,
  `wiring/` — because it has an aggregate with a lifecycle and transitions that can be refused.
- **inventory-service** has no `domain/`. Its one invariant (available stock never drops
  below zero) is a *contention* invariant, enforced by one conditional `UPDATE` and a
  `CHECK` constraint, not by an in-memory object.
- **notification-service** has no `domain/` either, but does have a use case: *has this
  notification already been sent, and may I attempt it now?*

The test for whether a `domain/` package is earned: **can code in it return a business
error?** If not, the rules are in the wrong place. Domain packages import the standard
library only.

**Composition root.** Only `internal/<context>/wiring` and `internal/bootstrap` may import
both a concrete adapter and the application layer. `wiring` builds the bounded context and
returns a struct (not a bare handler) so a second transport can arrive as another field;
`bootstrap` builds the process; `main` turns an error into an exit code. Dependencies are
passed explicitly — no container, no service locator.

**Generated and framework types stop at the adapter.** No Gin type crosses the inbound
boundary; `orderv1.OrderAccepted` is converted inside `amqpapi`; `application/` never
imports a generated package.

## Facts that are easy to get wrong

**Message ownership: events belong to the producer, commands to the receiver.** Opposite
rules, because an event has any number of consumers and a command has exactly one.
`OrderAccepted` lives in `order-service/api`; `ReserveStock` lives in `inventory-service/api`.
Declaring a broker object is not owning it — both sides declare the `orders` exchange so
either can start first.

**The delivery guarantee is "at-least-once publication, at-least-once processing once
routed."** The event is saved in `outbox_events` in the same transaction as the
acceptance, and a background worker publishes it. An unknown outcome is retried, so
duplicates are expected; never call publication exactly once. Retries stop only by
marking the event `needs_attention`, which keeps it for a manual redrive. The retry
budget counts only attempts that reached the broker: an outage pauses the worker
instead. Never write, in code or docs, that a failed publish means no email is sent: a
confirm timeout leaves the outcome genuinely *unknown*. A publish has three outcomes
(routed / known-not-routed via `mandatory` + `basic.return` / unknown), never two.

**Deduplication is of the effect, not the message.** The key is
`(subject_id, notification_kind, channel)`, not `event_id`, and the claim is a fenced lease —
every finalising write carries `AND claim_token = ?`. The retry budget is the `attempts`
column, **not** `x-death`: a busy-claim nack consumes a transport round trip without ever
calling the provider.

**Adding a second consumed event** is one `Subscription` value in
`notification/wiring/wiring.go` plus the file that builds it — not a second copy of the
transport.

## Conventions

**Commits: a title only.** No body, no explanation, no trailers, no `Co-Authored-By`.
Format is `Area: Imperative summary` — `Order: Publish OrderAccepted to RabbitMQ`,
`Inventory: Move the development seed into the service`, `Docs: …`, `Build: …`, `CI: …`.

**Comments are sparse.** Comment what is surprising — why the sort is there, why the
budget is not `x-death`, why a temp-dir swap. Never narrate what the code plainly says;
such a comment is only a future lie. Measured density in this repo is roughly 2–11%.

**Documentation is part of the deliverable.** Neither document may call "planned"
something that exists, describe as built anything missing, or state a guarantee stronger
than the one above. Words worth re-checking whenever behaviour changes: *planned*,
*future work*, *not implemented*, *idempotent*, *exactly once*, *guaranteed*.

**Generated code is committed**, so `git clone && go build ./...` works with no tools
installed.
