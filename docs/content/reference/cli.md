---
title: "CLI reference"
description: "Every vec command, global flag, and interactive shell command."
weight: 10
---

```
vec [global-flags] [database] [sql]
vec [global-flags] <subcommand> [args]
```

With a database path and a SQL string, vec runs the statement and exits.
With a database path and no SQL, it opens the interactive shell.
With a subcommand, it runs that operation.

```bash
# Run one statement and exit.
vec docs.vec "SELECT id FROM docs ORDER BY embedding <-> '[1,0,0,0]' LIMIT 5"

# Open the interactive shell.
vec docs.vec

# Run a subcommand.
vec build docs.vec docs
```

## Global flags

| Flag | Meaning |
|------|---------|
| `-h`, `-help` | Print usage and exit |
| `-v`, `-version` | Print the version and exit |
| `-ro`, `-readonly` | Open the database read-only |
| `-batch` | Non-interactive mode: read SQL from stdin, no prompt |
| `-m`, `-mode <mode>` | Output mode (see below); default `table` |
| `-init <file>` | Run the statements in `<file>` before the shell starts |
| `-null <text>` | String to print for a NULL value |
| `-noheader` | Omit the header row in tabular output |
| `-nocolor` | Disable ANSI color |
| `-timeout`, `-busy-timeout <dur>` | How long a write waits for the lock before failing busy |

## Output modes

`-m` and the `.mode` shell command accept: `table` (the default boxed form, also `box`), `column`, `list`, `tabs` (also `tsv`), `csv`, `json`, `jsonl` (also `ndjson`), `line`, `markdown` (also `md`), and `quote`.

## Subcommands

| Command | Does |
|---------|------|
| `create <db> "CREATE TABLE ..."` | Create a collection |
| `tables <db>` | List collections |
| `indexes <db>` | List indexes |
| `stats <db> [name]` | Point and index counts for one or all collections |
| `build <db> <name>` | Build the collection's index |
| `reindex <db> <name>` | Rebuild the collection's index |
| `check <db>` | Integrity check |
| `version` | Print the version |
| `help` | Print usage |

Run `vec <subcommand> --help` for the up-to-date argument list.

The `serve`, `import`, `export`, `backup`, `restore`, `vacuum`, and `bench` subcommands are reserved.
Importing, exporting, and serving are available from the library today through the [`bulk`](/guides/bulk-loading/) and [`server`](/guides/serving-over-the-network/) packages; the matching CLI wrappers are not wired yet, and the commands print a notice saying so.

## Interactive shell

With no SQL argument, vec opens a shell over the database.
Type VectorSQL statements ending in `;`, or a dot-command:

| Command | Does |
|---------|------|
| `.tables` | List collections |
| `.indexes` | List indexes |
| `.schema [name]` | Show a collection's schema |
| `.stats` | Show database statistics |
| `.mode <mode>` | Set the output mode |
| `.header on\|off` | Toggle the header row |
| `.null <text>` | Set the NULL placeholder |
| `.open <db>` | Open a different database |
| `.read <file>` | Run the statements in a file (also `.source`) |
| `.output <file>` | Send output to a file (also `.out`; no argument restores stdout) |
| `.echo on\|off` | Echo each statement before running it |
| `.show` | Show the current settings |
| `.version` | Print the version |
| `.help` | List dot-commands |
| `.quit` | Exit (also `.exit`, `.q`) |
