---
title: "Serving over the network"
description: "Project one .vec file over REST, gRPC, and the PostgreSQL wire protocol with the server package."
weight: 40
---

The same engine you embed as a library can answer over the network.
One open database, one writer pipeline, three protocols on top: REST/JSON for any HTTP client, gRPC over HTTP/2 for generated clients, and the PostgreSQL wire protocol so an existing `psql` or pgvector client connects with no code change.

The server is the `server` package.
It uses the standard library only: the proto3 codec and the PG wire framing are hand-written, so there is no grpc-go or protobuf runtime to pull in.

## Start a server

Build a `Config`, open it over a `.vec` file, and serve until the context is cancelled:

```go
package main

import (
	"log"

	"github.com/tamnd/vec/server"
)

func main() {
	cfg := server.DefaultConfig()
	cfg.Path = "articles.vec"
	cfg.RESTAddr = "127.0.0.1:7701"
	cfg.GRPCAddr = "127.0.0.1:7700"
	cfg.PGAddr = "127.0.0.1:5432" // empty disables the PG listener
	cfg.AuthMode = "token"
	cfg.Tokens = []server.Token{{ID: "admin", Secret: "secret", Role: "admin"}}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := server.SignalContext() // cancels on SIGINT/SIGTERM
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
```

An empty address disables that protocol, so you can run REST only, or add the PG listener when you want `psql` access.
`DefaultConfig` listens on `0.0.0.0:7701` for REST and `0.0.0.0:7700` for gRPC; override the addresses before calling `New`.

## REST

The REST surface is a small JSON API under `/v1`.
Health and readiness are unauthenticated; everything else needs a token:

```bash
# Liveness, no auth.
curl http://127.0.0.1:7701/v1/health

# Create a collection (admin role).
curl -H "Authorization: Bearer secret" \
  -X POST http://127.0.0.1:7701/v1/collections \
  -d '{"name":"docs","columns":[{"name":"embedding","type":"vector","dim":4,"metric":"l2"}]}'

# Query the nearest points (reader role).
curl -H "Authorization: Bearer secret" \
  -X POST http://127.0.0.1:7701/v1/collections/docs/query \
  -d '{"vector":[1,0,0,0],"k":10}'
```

Points, queries, reindex, vacuum, and backup all hang off `/v1/collections/{name}/...` and `/v1/admin/...`.

## gRPC

The gRPC service carries the same operations as binary proto3 over HTTP/2, for clients that want a typed stub and streaming.
It shares the open database and the writer pipeline with REST, so a write over gRPC and a read over REST see the same data.

## PostgreSQL wire

With `PGAddr` set, vec speaks the PostgreSQL wire protocol.
An existing client connects as if to Postgres:

```bash
psql "host=127.0.0.1 port=5432 user=vec"
```

This is the path for tools and drivers already built around pgvector: they keep their SQL and their connection code, and vec answers.

## Authentication and roles

`AuthMode` of `token` checks a bearer token against the configured `Tokens`.
Each token carries a role: `reader` can query, `readwrite` can also upsert and delete, and `admin` can create and drop collections and run admin operations.
A token can also scope itself to named `Collections`, empty means all.
The REST routes enforce the role per endpoint, so a reader token cannot create a collection.

For transport security, set `TLSCert` and `TLSKey` to serve over TLS, and `TLSCA` to require client certificates (mTLS).

## Metrics

Set `MetricsAddr` to expose Prometheus metrics on a separate port, or scrape `GET /metrics` on the REST listener.
The [encryption](/guides/encryption-at-rest/) guide covers serving an encrypted database, which works the same way: the server opens the file with a passphrase and serves the decrypted view.
