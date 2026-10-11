# Go port for Ruby/Rails

This fork keeps the original Ruby engine and specification. The Go CLI is a
separate, focused source index. Credit goes to [Nik Grebnev](https://github.com/nikgrebnev)
and [the original xref](https://github.com/nikgrebnev/xref); see
[MIT](../LICENSE) and [CC BY 4.0](../LICENSE-docs).

## Build and run

Go and a C toolchain are required because the Ruby and PostgreSQL parsers use
cgo. From this repository:

```sh
go build -o bin/xref-go ./cmd/xref-go
bin/xref-go --project /path/to/rails-app refresh
bin/xref-go --project /path/to/rails-app where products.id
bin/xref-go --project /path/to/rails-app route 'GET /products'
bin/xref-go --project /path/to/rails-app coverage
```

The CLI reads `db/structure.sql` by default if it exists. It never boots Rails,
loads Bundler, or connects to PostgreSQL for these commands. The index is a
permission-restricted JSON file under the user's cache directory, outside the
analyzed project; `refresh` and `audit` print its path. Every query updates
changed source files. `--project` selects the source checkout, `--index`
overrides the cache path, `--json` emits structured output, and `--limit N`
limits human-readable rows.

| Command | Result |
| --- | --- |
| `refresh` | Scan source and local schema; print counts and index path. |
| `where table[.column]` | Find parsed SQL and inferred ActiveRecord references, with confidence. |
| `find text` | Search project text, with index context. |
| `blind` | Find references to tables absent from the selected schema. |
| `drift` | Compare two or more explicitly supplied schema sources. |
| `audit` | Show index size and observed blind spots. |
| `route 'GET /path'` | Trace a static route to source and table references. |
| `trace table[.column]` | Show indexed route chains to a table or column. |
| `coverage` | Summarize chains and gaps for every statically recognized route. |
| `callers relative/path.rb` | Find indexed calls into a file. |

The CLI indexes Ruby, SQL, ERB, and Haml source. It recognizes common
ActiveRecord calls, scopes, and unambiguous declared model associations, even
when associations come from an explicitly included concern. References from
associations and convention-based model names are *candidates*, not proof that
a query ran. `route`, `trace`, and `coverage` likewise describe potential
static paths, never production execution.

Pass a local schema explicitly with `--schema repo=/path/to/structure.sql`.
To compare an explicitly requested live schema, use `--schema stage=pg:STAGE`;
the CLI reads connection settings from `XREF_STAGE_PGHOST`,
`XREF_STAGE_PGPORT`, `XREF_STAGE_PGDATABASE`, `XREF_STAGE_PGUSER`,
`XREF_STAGE_PGPASSWORD`, and optionally `XREF_STAGE_PGSSLMODE`. A live read
requires `psql` and uses a read-only transaction. Do not pass a password in
command-line arguments or commit it to a config file.

## Known limits

Static route parsing does not execute `draw`, constraints, concerns, custom
actions, or runtime conditions. Dynamic Ruby dispatch, callbacks, arbitrary
inheritance, partial templates without literal renders, and nonstandard
ERB/Haml constructs can break a chain. `coverage` shows unresolved transitions;
a route without a chain does **not** prove that it avoids the database. The
original Ruby engine has many more commands, including production log and
live-data analysis; those are not part of this Go CLI.

See the [release criteria](go-v1-acceptance.md) and the
[detailed Russian guide](go-port.md).
