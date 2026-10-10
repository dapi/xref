package xrefgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var pgSourceName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// The query reads relation and attribute metadata only. PostgreSQL enforces
// read-only mode for the session as an additional guard.
const liveSchemaQuery = `BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';
SELECT COALESCE(jsonb_object_agg(relname, columns), '{}'::jsonb)
FROM (
  SELECT c.relname,
         COALESCE(jsonb_agg(a.attname ORDER BY a.attnum)
           FILTER (WHERE a.attnum IS NOT NULL), '[]'::jsonb) AS columns
  FROM pg_catalog.pg_class AS c
  JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
  LEFT JOIN pg_catalog.pg_attribute AS a
    ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
  WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
  GROUP BY c.oid, c.relname
) AS relations;
COMMIT;`

// parseLiveSchema accepts a name, not a connection string. Connection settings
// arrive through XREF_<name>_PG* variables, allowing passwords to come from pass
// without appearing in command arguments, the index, or a repository file.
func parseLiveSchema(name string) (Schema, error) {
	if !pgSourceName.MatchString(name) {
		return Schema{}, fmt.Errorf("источник PostgreSQL должен иметь имя из A-Z, 0-9 и _ (например pg:STAGE)")
	}
	prefix := "XREF_" + name + "_"
	get := func(key string) string { return os.Getenv(prefix + key) }
	host, database, user := get("PGHOST"), get("PGDATABASE"), get("PGUSER")
	if host == "" || database == "" || user == "" {
		return Schema{}, fmt.Errorf("для pg:%s нужны %sPGHOST, %sPGDATABASE и %sPGUSER", name, prefix, prefix, prefix)
	}
	port := get("PGPORT")
	if port == "" {
		port = "5432"
	}
	password := get("PGPASSWORD")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "psql", "--no-psqlrc", "--no-password", "--quiet", "--tuples-only", "--no-align", "--command", liveSchemaQuery)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"PGHOST=" + host,
		"PGPORT=" + port,
		"PGDATABASE=" + database,
		"PGUSER=" + user,
		"PGPASSWORD=" + password,
		"PGCONNECT_TIMEOUT=5",
	}
	if mode := get("PGSSLMODE"); mode != "" {
		cmd.Env = append(cmd.Env, "PGSSLMODE="+mode)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if password != "" {
			message = strings.ReplaceAll(message, password, "[секрет]")
		}
		if len(message) > 500 {
			message = message[:500]
		}
		if message == "" {
			message = err.Error()
		}
		return Schema{}, fmt.Errorf("psql для pg:%s: %s", name, message)
	}
	var tables map[string][]string
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &tables); err != nil {
		return Schema{}, fmt.Errorf("psql для pg:%s вернул некорректный каталог: %w", name, err)
	}
	if len(tables) == 0 {
		return Schema{}, fmt.Errorf("psql для pg:%s не нашёл таблиц в схеме public", name)
	}
	canonical, err := json.Marshal(tables)
	if err != nil {
		return Schema{}, err
	}
	return Schema{Path: "pg:" + name, SHA256: digest(canonical), Tables: tables}, nil
}
