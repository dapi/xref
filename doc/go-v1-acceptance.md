# Go xref 1.0 acceptance criteria

This release is a useful local index for Ruby/Rails source analysis without
booting the application. It is a focused offline tool, not a port of every
command in the original Ruby engine. Production logs and live data analysis are
outside this release. The original author and licenses remain credited.

Release `v1.0.0` only when all conditions below pass on public fixtures. A
private large Rails project may be used for local validation; its source and
route details must not be published in this repository or CI output.

1. **Standalone operation:** Build and run instructions work from another
   directory. `refresh`, `where`, `find`, `blind`, `audit`, `coverage`, `route`,
   `trace`, and `callers` need no Rails boot, Bundler, database server, or
   network. The default schema source is local `db/structure.sql`; a live
   PostgreSQL connection requires an explicit `--schema ...=pg:...`. The index
   location is outside the project and visible in `refresh` and `audit`.
2. **Evidence:** Parsed SQL, common direct ActiveRecord calls, scopes, and
   unambiguous model associations yield table or column references with file,
   line, and confidence. Dynamic receivers and unsupported transitions remain
   visible as gaps. No missing chain is presented as proof of no database use.
3. **Route navigation:** `route`, `trace`, and `coverage` connect statically
   recognized routes to local actions, calls, scopes, and templates. Public
   tests cover a found chain, an external superclass, a dynamic call, and a
   page without an indexed reference.
4. **Freshness and privacy:** Source, schema, or analyzer changes invalidate
   affected records. An unchanged second query avoids rescanning them. Cache
   files are owner only, and analyzed source is never sent away.
5. **Delivery:** `go test ./...`, `go vet ./...`, and a build pass in CI. The
   README leads with the Go commands and clearly separates their behavior from
   the retained Ruby engine. A local large project check confirms useful
   chains, explained gaps, and operation without Rails or a database, with no
   private code published.

After these checks, tag `v1.0.0` with examples and known limitations. New
features belong to later versions unless they correct a failed criterion.
