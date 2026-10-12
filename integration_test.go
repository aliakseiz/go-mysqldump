//go:build integration

package mysqldump_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aliakseiz/go-mysqldump/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// Run against a throwaway server with a user that may create databases:
//
//	docker run --rm -d --name mysqldump-it -e MYSQL_ROOT_PASSWORD=pw -p 127.0.0.1:3307:3306 mysql:8.4
//	MYSQL_DSN='root:pw@tcp(127.0.0.1:3307)/' go test -tags=integration -run TestIntegration ./...
const dsnEnv = "MYSQL_DSN"

const integrationTimeout = 60 * time.Second

var integrationSchema = []string{
	"CREATE TABLE items (" +
		"id INT NOT NULL AUTO_INCREMENT PRIMARY KEY," +
		"qty INT NOT NULL," +
		"note INT NULL," +
		"units INT UNSIGNED NOT NULL," +
		"price DECIMAL(10,2) NOT NULL," +
		"label VARCHAR(100) NULL," +
		"body TEXT NULL," +
		"payload BLOB NULL)",
	"CREATE TABLE bulk (" +
		"id INT NOT NULL AUTO_INCREMENT PRIMARY KEY," +
		"label VARCHAR(200) NOT NULL)",
	"CREATE VIEW labelled_items AS SELECT id, label FROM items WHERE label IS NOT NULL",
	// Sorts before the table it reads, so the dump must write views after tables.
	"CREATE VIEW a_items AS SELECT id, qty FROM items",
	"CREATE TABLE `back``tick` (id INT NOT NULL PRIMARY KEY, v VARCHAR(20) NOT NULL)",
	"INSERT INTO `back``tick` VALUES (1, 'a`b')",
}

const (
	bulkRows       = 300
	bulkPacket     = 2048
	defaultPacket  = 0
	insertKeyword  = "INSERT INTO"
	itemsInsertSQL = "INSERT INTO items (qty, note, units, price, label, body, payload) VALUES (?, ?, ?, ?, ?, ?, ?)"
)

// openDB opens a pool on the server of the base DSN, with the given default database.
func openDB(tb testing.TB, base *mysql.Config, dbName string, multiStatements bool) *sql.DB {
	tb.Helper()

	cfg := base.Clone()
	cfg.DBName = dbName
	cfg.MultiStatements = multiStatements

	connector, err := mysql.NewConnector(cfg)
	require.NoError(tb, err)

	db := sql.OpenDB(connector)

	tb.Cleanup(func() { _ = db.Close() })

	return db
}

func execAll(ctx context.Context, tb testing.TB, db *sql.DB, statements ...string) {
	tb.Helper()

	for _, statement := range statements {
		_, err := db.ExecContext(ctx, statement)
		require.NoError(tb, err, statement)
	}
}

func seedSource(ctx context.Context, tb testing.TB, db *sql.DB) {
	tb.Helper()

	execAll(ctx, tb, db, integrationSchema...)

	rows := [][]any{
		{1, nil, uint32(4294967295), "19.99", "plain", "text", []byte("blob 'quoted' \\ data")},
		{-7, 42, uint32(0), "0.00", "it's a \"quoted\" back\\slash", "line1\nline2\ttab", nil},
		{0, nil, uint32(1), "-1234.50", nil, nil, nil},
		{2147483647, -2147483648, uint32(2), "99999999.99", "héllo", "multi\r\nline", []byte("x")},
		{3, nil, uint32(3), "1.00", "empty blob", nil, []byte{}},
	}

	for _, row := range rows {
		_, err := db.ExecContext(ctx, itemsInsertSQL, row...)
		require.NoError(tb, err)
	}

	for i := range bulkRows {
		label := fmt.Sprintf("row %d %s", i, strings.Repeat("x", i%50))

		_, err := db.ExecContext(ctx, "INSERT INTO bulk (label) VALUES (?)", label)
		require.NoError(tb, err)
	}
}

// queryAll reads every row of a query as strings, with NULL as a distinct marker.
func queryAll(ctx context.Context, tb testing.TB, db *sql.DB, query string) [][]string {
	tb.Helper()

	rows, err := db.QueryContext(ctx, query)
	require.NoError(tb, err, query)

	defer func() { require.NoError(tb, rows.Close()) }()

	columns, err := rows.Columns()
	require.NoError(tb, err)

	var result [][]string

	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		targets := make([]any, len(columns))

		for i := range values {
			targets[i] = &values[i]
		}

		require.NoError(tb, rows.Scan(targets...))

		line := make([]string, len(columns))

		for i, value := range values {
			if value.Valid {
				line[i] = "v:" + value.String
			} else {
				line[i] = "NULL"
			}
		}

		result = append(result, line)
	}

	require.NoError(tb, rows.Err())

	return result
}

func TestIntegration_RoundTrip(t *testing.T) {
	t.Parallel()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set", dsnEnv)
	}

	base, err := mysql.ParseDSN(dsn)
	require.NoError(t, err)

	tests := []struct {
		name   string
		lock   bool
		packet int
	}{
		{name: "default packet", packet: defaultPacket},
		{name: "small packet", packet: bulkPacket},
		{name: "table locks", lock: true, packet: bulkPacket},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), integrationTimeout)
			t.Cleanup(cancel)

			admin := openDB(t, base, "", false)

			stamp := time.Now().UnixNano()
			src := fmt.Sprintf("mysqldump_it_src_%d", stamp)
			dst := fmt.Sprintf("mysqldump_it_dst_%d", stamp)

			for _, name := range []string{src, dst} {
				execAll(ctx, t, admin, "CREATE DATABASE `"+name+"`")

				t.Cleanup(func() {
					_, _ = admin.ExecContext(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS `"+name+"`")
				})
			}

			seeder := openDB(t, base, src, false)
			seedSource(ctx, t, seeder)

			// The dumping connection defaults to the source schema and the dump carries no recreate section.
			var dump bytes.Buffer

			dumper := openDB(t, base, src, false)
			require.NoError(t, (&mysqldump.Data{
				Out:              &dump,
				Connection:       dumper,
				LockTables:       tt.lock,
				MaxAllowedPacket: tt.packet,
			}).Dump(ctx))
			require.NoError(t, dumper.Close())

			if tt.packet == bulkPacket {
				inserts := strings.Count(dump.String(), insertKeyword+" `bulk`")
				require.Greater(t, inserts, 1, "the bulk table must span several statements")
			}

			require.NotContains(t, dump.String(), "DEFINER=", "the views must not carry the source definer")

			restorer := openDB(t, base, dst, true)
			execAll(ctx, t, restorer, dump.String())

			for _, query := range []string{
				"SELECT * FROM items ORDER BY id",
				"SELECT * FROM bulk ORDER BY id",
				"SELECT * FROM labelled_items ORDER BY id",
				"SELECT * FROM a_items ORDER BY id",
				"SELECT * FROM `back``tick` ORDER BY id",
			} {
				want := queryAll(ctx, t, seeder, query)
				require.NotEmpty(t, want, query)
				require.Equal(t, want, queryAll(ctx, t, restorer, query), query)
			}

			viewType := queryAll(ctx, t, restorer, "SELECT table_type FROM information_schema.tables "+
				"WHERE table_schema = DATABASE() AND table_name = 'labelled_items'")
			require.Equal(t, [][]string{{"v:VIEW"}}, viewType)

			definition := queryAll(ctx, t, restorer, "SELECT view_definition FROM information_schema.views "+
				"WHERE table_schema = DATABASE() AND table_name = 'labelled_items'")
			require.Len(t, definition, 1)
			require.NotContains(t, definition[0][0], "`"+src+"`", "the restored view must not read from the source schema")
		})
	}
}
