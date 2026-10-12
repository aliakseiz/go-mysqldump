//nolint:lll // SQL expectations are long
package mysqldump_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aliakseiz/go-mysqldump/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

const (
	sqlVersion    = `^SELECT version\(\)$`
	sqlShowTables = `^SHOW TABLES$`
	sqlDatabase   = `^SELECT DATABASE\(\)$`
	sqlTableType  = `^SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?$`
	sqlUnlock     = `^UNLOCK TABLES$`

	testSchema = "Test_Schema"
	currentDB  = "current_db"

	manyRows     = 30
	smallPacket  = 40
	testDeadline = 5 * time.Second

	tableCreateSQL = "CREATE TABLE 'Test_Table' (`id` int(11) NOT NULL AUTO_INCREMENT,`email` char(60) DEFAULT NULL, `name` char(60), PRIMARY KEY (`id`))ENGINE=InnoDB DEFAULT CHARSET=latin1"
	viewCreateSQL  = "CREATE VIEW 'Test_View' AS SELECT `id`, `email`, `name` FROM 'Test_Table'"
)

// Expected output fragments. Backticks are written as ~ and swapped back before comparing.
const (
	expectedHeader = `-- Go SQL Dump ` + mysqldump.Version + `
--
-- ------------------------------------------------------
-- Server version	test_version

/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET @OLD_CHARACTER_SET_RESULTS=@@CHARACTER_SET_RESULTS */;
/*!40101 SET @OLD_COLLATION_CONNECTION=@@COLLATION_CONNECTION */;
 SET NAMES utf8mb4 ;
/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;
/*!40103 SET TIME_ZONE='+00:00' */;
/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;
/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;
/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;
`

	expectedTableHead = `
--
-- Table structure for table ~Test_Table~
--


DROP TABLE IF EXISTS ~Test_Table~;

/*!40101 SET @saved_cs_client     = @@character_set_client */;
 SET character_set_client = utf8mb4 ;
CREATE TABLE 'Test_Table' (~id~ int(11) NOT NULL AUTO_INCREMENT,~email~ char(60) DEFAULT NULL, ~name~ char(60), PRIMARY KEY (~id~))ENGINE=InnoDB DEFAULT CHARSET=latin1;
/*!40101 SET character_set_client = @saved_cs_client */;


--
-- Dumping data for table ~Test_Table~
--

LOCK TABLES ~Test_Table~ WRITE;
/*!40000 ALTER TABLE ~Test_Table~ DISABLE KEYS */;
`

	expectedInsertFirst  = "INSERT INTO ~Test_Table~ VALUES ('1',NULL,'Test Name 1');\n"
	expectedInsertSecond = "INSERT INTO ~Test_Table~ VALUES ('2','test2@test.de','Test Name 2');\n"
	expectedInsertBoth   = "INSERT INTO ~Test_Table~ VALUES ('1',NULL,'Test Name 1'),('2','test2@test.de','Test Name 2');\n"

	expectedTableTail = `/*!40000 ALTER TABLE ~Test_Table~ ENABLE KEYS */;
UNLOCK TABLES;

`

	expectedTable = expectedTableHead + expectedInsertBoth + expectedTableTail

	expectedView = `
--
-- Table structure for table ~Test_View~
--


DROP VIEW IF EXISTS ~Test_View~;

/*!40101 SET @saved_cs_client     = @@character_set_client */;
 SET character_set_client = utf8mb4 ;
CREATE VIEW 'Test_View' AS SELECT ~id~, ~email~, ~name~ FROM 'Test_Table';
/*!40101 SET character_set_client = @saved_cs_client */;


`

	expectedFooter = `/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;

/*!40101 SET SQL_MODE=@OLD_SQL_MODE */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;
/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;

`

	// Written by the dump when a database name is configured; has no trailing newline.
	expectedRecreateFormat = " DROP DATABASE IF EXISTS ~%[1]s~;" +
		"CREATE DATABASE ~%[1]s~ CHARACTER SET = 'utf8' COLLATE = 'utf8_general_ci';" +
		"USE ~%[1]s~;"
)

var (
	errBoom  = errors.New("boom")
	errWrite = errors.New("write failed")

	fixtureTable = tableSpec{name: "Test_Table"}
	fixtureView  = tableSpec{name: "Test_View", view: true}
	ignoredTable = tableSpec{name: "Test_Ignored", ignored: true}
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// tableSpec describes a table the mocked server reports in SHOW TABLES.
type tableSpec struct {
	name    string
	view    bool
	ignored bool
}

func (s tableSpec) typeName() string {
	if s.view {
		return "VIEW"
	}

	return "BASE TABLE"
}

func (s tableSpec) output() string {
	if s.view {
		return expectedView
	}

	return expectedTable
}

// dumpOpts configures both the Data under test and the matching mock expectations.
type dumpOpts struct {
	specs  []tableSpec
	lock   bool
	dbName string
}

func (o dumpOpts) schema() string {
	if o.dbName != "" {
		return o.dbName
	}

	return currentDB
}

func (o dumpOpts) dumped() []tableSpec {
	dumped := make([]tableSpec, 0, len(o.specs))

	for _, spec := range o.specs {
		if !spec.ignored {
			dumped = append(dumped, spec)
		}
	}

	return dumped
}

func (o dumpOpts) data(db *sql.DB, out io.Writer) *mysqldump.Data {
	ignore := make(map[string]struct{})

	for _, spec := range o.specs {
		if spec.ignored {
			ignore[spec.name] = struct{}{}
		}
	}

	return &mysqldump.Data{
		Out:          out,
		Connection:   db,
		IgnoreTables: ignore,
		LockTables:   o.lock,
		DBName:       o.dbName,
	}
}

// wantOutput is the dump text up to the completion comment.
func (o dumpOpts) wantOutput() string {
	var out strings.Builder

	out.WriteString(expectedHeader)

	if o.dbName != "" {
		fmt.Fprintf(&out, expectedRecreateFormat, o.dbName)
	}

	for _, spec := range o.dumped() {
		out.WriteString(spec.output())
	}

	out.WriteString(expectedFooter)

	return out.String()
}

// cancelOnWrite cancels the context on the first write, then buffers the output.
type cancelOnWrite struct {
	cancel context.CancelFunc
	buf    bytes.Buffer
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	w.cancel()

	n, err := w.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer write: %w", err)
	}

	return n, nil
}

// failingWriter rejects every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errWrite
}

// failOnWriter rejects the writes containing marker and buffers the others.
// Template text nodes are written whole, so a marker taken from a text node selects one template section.
type failOnWriter struct {
	marker string
	buf    bytes.Buffer
}

func (w *failOnWriter) Write(p []byte) (int, error) {
	if w.marker != "" && bytes.Contains(p, []byte(w.marker)) {
		return 0, errWrite
	}

	n, err := w.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer write: %w", err)
	}

	return n, nil
}

func newMockDB(tb testing.TB) (*sql.DB, sqlmock.Sqlmock) {
	tb.Helper()

	db, mock, err := sqlmock.New()
	require.NoError(tb, err, "an error was not expected when opening a stub database connection")

	tb.Cleanup(func() { _ = db.Close() })

	return db, mock
}

// newMockData returns a Data with an open transaction, as after Begin.
func newMockData(tb testing.TB) (*mysqldump.Data, sqlmock.Sqlmock) {
	tb.Helper()

	db, mock := newMockDB(tb)

	mock.ExpectBegin()

	data := &mysqldump.Data{Connection: db}
	require.NoError(tb, data.Begin(tb.Context()))

	return data, mock
}

// newTable returns a table named "t" whose SELECT yields rows. MaxAllowedPacket is left to the caller.
func newTable(tb testing.TB, rows *sqlmock.Rows) (*mysqldump.Data, sqlmock.Sqlmock, *mysqldump.Table) {
	tb.Helper()

	data, mock := newMockData(tb)

	expectTableType(mock, testSchema, "t", "BASE TABLE")
	mock.ExpectQuery(selectAll("t")).WillReturnRows(rows)

	table, err := data.CreateTable(tb.Context(), testSchema, "t")
	require.NoError(tb, err)

	return data, mock, table
}

func selectAll(name string) string {
	return "^SELECT \\* FROM `" + name + "`$"
}

func tableRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, nil, "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2")
}

// stringRows returns rows of a single string column.
func stringRows(values ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"v"})

	for _, value := range values {
		rows.AddRow(value)
	}

	return rows
}

func newManyRows() *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "name"})

	for i := range manyRows {
		rows.AddRow(i, "x")
	}

	return rows
}

func expectVersion(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(sqlVersion).WillReturnRows(sqlmock.NewRows([]string{"Version()"}).AddRow("test_version"))
}

func expectDatabase(mock sqlmock.Sqlmock, name string) {
	mock.ExpectQuery(sqlDatabase).WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(name))
}

func expectTableType(mock sqlmock.Sqlmock, schema, name, tableType string) {
	mock.ExpectQuery(sqlTableType).
		WithArgs(schema, name).
		WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow(tableType))
}

// viewRows returns a SHOW CREATE VIEW result: the server appends the client character set and collation.
func viewRows(name, createSQL string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"View", "Create View", "character_set_client", "collation_connection"}).
		AddRow(name, createSQL, "utf8mb4", "utf8mb4_0900_ai_ci")
}

func expectShowCreate(mock sqlmock.Sqlmock, spec tableSpec) {
	if spec.view {
		mock.ExpectQuery("^SHOW CREATE VIEW `" + spec.name + "`$").
			WillReturnRows(viewRows(spec.name, viewCreateSQL))

		return
	}

	mock.ExpectQuery("^SHOW CREATE TABLE `" + spec.name + "`$").
		WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow(spec.name, tableCreateSQL))
}

// expectPrelude queues the statements every dump starts with: Begin, version and SHOW TABLES.
func expectPrelude(mock sqlmock.Sqlmock, tables ...string) {
	mock.ExpectBegin()
	expectVersion(mock)

	rows := sqlmock.NewRows([]string{"Tables_in_Testdb"})
	for _, name := range tables {
		rows.AddRow(name)
	}

	mock.ExpectQuery(sqlShowTables).WillReturnRows(rows)
}

func lockRegex(specs []tableSpec) string {
	parts := make([]string, 0, len(specs))

	for _, spec := range specs {
		parts = append(parts, regexp.QuoteMeta("`"+spec.name+"` READ /*!32311 LOCAL */"))
	}

	return "^LOCK TABLES " + strings.Join(parts, ",") + "$"
}

// expectDumpBody queues the statements of a successful dump, ending with the rollback.
// UNLOCK TABLES is expected before the rollback because the unlock is deferred after it.
func expectDumpBody(mock sqlmock.Sqlmock, opts dumpOpts) {
	names := make([]string, 0, len(opts.specs))
	for _, spec := range opts.specs {
		names = append(names, spec.name)
	}

	expectPrelude(mock, names...)

	dumped := opts.dumped()
	locked := opts.lock && len(dumped) > 0

	if locked {
		mock.ExpectExec(lockRegex(dumped)).WillReturnResult(sqlmock.NewResult(0, 0))
	}

	for _, spec := range dumped {
		if opts.dbName == "" {
			expectDatabase(mock, opts.schema())
		}

		expectTableType(mock, opts.schema(), spec.name, spec.typeName())
		expectShowCreate(mock, spec)

		if !spec.view {
			mock.ExpectQuery(selectAll(spec.name)).WillReturnRows(tableRows())
		}
	}

	if locked {
		mock.ExpectExec(sqlUnlock).WillReturnResult(sqlmock.NewResult(0, 0))
	}

	mock.ExpectRollback()
}

// dumpText returns the dump text before the completion comment, with backticks swapped for ~.
func dumpText(t *testing.T, out string) string {
	t.Helper()

	body, _, found := strings.Cut(strings.ReplaceAll(out, "`", "~"), "-- Dump completed")
	require.True(t, found, "the dump has no completion comment")

	return body
}

func TestData_Dump(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts dumpOpts
	}{
		{
			name: "tables and view locked",
			opts: dumpOpts{specs: []tableSpec{fixtureTable, fixtureView}, lock: true},
		},
		{
			name: "tables and view unlocked",
			opts: dumpOpts{specs: []tableSpec{fixtureTable, fixtureView}},
		},
		{
			name: "database name set",
			opts: dumpOpts{specs: []tableSpec{fixtureTable, fixtureView}, lock: true, dbName: "app_db"},
		},
		{
			name: "view only",
			opts: dumpOpts{specs: []tableSpec{fixtureView}, lock: true},
		},
		{
			name: "ignored table skipped",
			opts: dumpOpts{specs: []tableSpec{fixtureTable, ignoredTable}, lock: true},
		},
		{
			name: "no tables",
			opts: dumpOpts{lock: true},
		},
		{
			name: "all tables ignored",
			opts: dumpOpts{specs: []tableSpec{ignoredTable}, lock: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock := newMockDB(t)
			expectDumpBody(mock, tt.opts)

			var buf bytes.Buffer

			require.NoError(t, tt.opts.data(db, &buf).Dump(t.Context()))
			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
			require.Equal(t, tt.opts.wantOutput(), dumpText(t, buf.String()))
		})
	}
}

func TestData_Dump_Errors(t *testing.T) {
	t.Parallel()

	oneTable := dumpOpts{specs: []tableSpec{fixtureTable}, dbName: testSchema}

	tests := []struct {
		name    string
		opts    dumpOpts
		marker  string                     // writes containing it fail
		expect  func(mock sqlmock.Sqlmock) // nil queues a complete successful dump
		wantIs  error
		wantMsg string
	}{
		{
			name: "begin",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin().WillReturnError(errBoom)
			},
			wantIs: errBoom,
		},
		{
			name: "server version",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectQuery(sqlVersion).WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name:   "header write",
			marker: "Server version",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				expectVersion(mock)
				mock.ExpectRollback()
			},
			wantIs: errWrite,
		},
		{
			name:   "recreate write",
			opts:   dumpOpts{dbName: "app_db"},
			marker: "DROP DATABASE",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				expectVersion(mock)
				mock.ExpectRollback()
			},
			wantIs: errWrite,
		},
		{
			name: "show tables query",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				expectVersion(mock)
				mock.ExpectQuery(sqlShowTables).WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "show tables iteration",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				expectVersion(mock)
				mock.ExpectQuery(sqlShowTables).
					WillReturnRows(sqlmock.NewRows([]string{"Tables_in_Testdb"}).AddRow("a").RowError(0, errBoom))
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "lock tables",
			opts: dumpOpts{lock: true},
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				mock.ExpectExec("^LOCK TABLES").WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "current database",
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				mock.ExpectQuery(sqlDatabase).WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "table type",
			opts: oneTable,
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				mock.ExpectQuery(sqlTableType).WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "show create",
			opts: oneTable,
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
				mock.ExpectQuery("^SHOW CREATE TABLE").WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "show create returns another table",
			opts: oneTable,
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
				mock.ExpectQuery("^SHOW CREATE TABLE").
					WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("Other_Table", tableCreateSQL))
				mock.ExpectRollback()
			},
			wantMsg: "returned table is not the same as requested table",
		},
		{
			name: "select rows",
			opts: oneTable,
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
				expectShowCreate(mock, fixtureTable)
				mock.ExpectQuery(selectAll("Test_Table")).WillReturnError(errBoom)
				mock.ExpectRollback()
			},
			wantIs: errBoom,
		},
		{
			name: "table without columns",
			opts: oneTable,
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
				expectShowCreate(mock, fixtureTable)
				mock.ExpectQuery(selectAll("Test_Table")).WillReturnRows(sqlmock.NewRows([]string{}))
				mock.ExpectRollback()
			},
			wantMsg: "no columns in table",
		},
		{
			// The row stream may or may not reach the SELECT before the write fails, so it is not expected.
			name:   "table write",
			opts:   oneTable,
			marker: "Table structure",
			expect: func(mock sqlmock.Sqlmock) {
				expectPrelude(mock, "Test_Table")
				expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
				expectShowCreate(mock, fixtureTable)
				mock.ExpectRollback()
			},
			wantIs: errWrite,
		},
		{
			name:   "footer write",
			opts:   oneTable,
			marker: "Dump completed",
			wantIs: errWrite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock := newMockDB(t)

			if tt.expect == nil {
				expectDumpBody(mock, tt.opts)
			} else {
				tt.expect(mock)
			}

			out := &failOnWriter{marker: tt.marker}

			err := tt.opts.data(db, out).Dump(t.Context())

			require.Error(t, err)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}

			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
			}

			require.NotContains(t, out.buf.String(), "Dump completed")
			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
		})
	}
}

func TestData_Dump_ContextCancelled(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	mock.ExpectBegin()
	expectVersion(mock)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	out := &cancelOnWrite{cancel: cancel}

	err := (&mysqldump.Data{Out: out, Connection: db}).Dump(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, out.buf.String(), "Dump completed")
}

func TestData_Dump_RowIterationError(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)

	rows := sqlmock.NewRows([]string{"id", "name"}).
		AddRow(1, "a").
		AddRow(2, "b").
		RowError(1, errBoom)

	expectPrelude(mock, "Test_Table")
	expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
	expectShowCreate(mock, fixtureTable)
	mock.ExpectQuery(selectAll("Test_Table")).WillReturnRows(rows)
	mock.ExpectRollback()

	var buf bytes.Buffer

	err := (&mysqldump.Data{Out: &buf, Connection: db, DBName: testSchema}).Dump(t.Context())

	require.ErrorIs(t, err, errBoom)
	require.NotContains(t, buf.String(), "Dump completed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestData_WriteTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		spec   tableSpec
		packet int
		want   string
	}{
		{
			name:   "rows batched into one statement",
			spec:   fixtureTable,
			packet: 4096,
			want:   expectedTable,
		},
		{
			name:   "rows split by a small packet",
			spec:   fixtureTable,
			packet: 64,
			want:   expectedTableHead + expectedInsertFirst + expectedInsertSecond + expectedTableTail,
		},
		{
			name:   "view has no data section",
			spec:   fixtureView,
			packet: 4096,
			want:   expectedView,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, mock := newMockData(t)

			expectTableType(mock, testSchema, tt.spec.name, tt.spec.typeName())
			expectShowCreate(mock, tt.spec)

			if !tt.spec.view {
				mock.ExpectQuery(selectAll(tt.spec.name)).WillReturnRows(tableRows())
			}

			var buf bytes.Buffer

			data.Out = &buf
			data.MaxAllowedPacket = tt.packet

			require.NoError(t, data.GetTemplates())

			table, err := data.CreateTable(t.Context(), testSchema, tt.spec.name)
			require.NoError(t, err)

			require.NoError(t, data.WriteTable(t.Context(), table))
			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
			require.Equal(t, tt.want, strings.ReplaceAll(buf.String(), "`", "~"))
		})
	}
}

func TestData_WriteTable_WriteError(t *testing.T) {
	t.Parallel()

	data, mock := newMockData(t)

	expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
	expectShowCreate(mock, fixtureTable)
	mock.ExpectQuery(selectAll("Test_Table")).WillReturnRows(newManyRows())

	data.Out = failingWriter{}
	data.MaxAllowedPacket = smallPacket

	require.NoError(t, data.GetTemplates())

	table, err := data.CreateTable(t.Context(), testSchema, "Test_Table")
	require.NoError(t, err)

	done := make(chan error, 1)

	go func() { done <- data.WriteTable(t.Context(), table) }()

	select {
	case err = <-done:
	case <-time.After(testDeadline):
		require.FailNow(t, "WriteTable did not return after the output failed")
	}

	require.ErrorIs(t, err, errWrite)
	// The race detector fails this read when the producer is still running.
	// The error type depends on whether the cancellation hit the SELECT or the row iteration.
	require.Error(t, table.Err)
}

func TestData_GetTables(t *testing.T) {
	t.Parallel()

	names := func(values ...any) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"Tables_in_Testdb"})
		for _, value := range values {
			rows.AddRow(value)
		}

		return rows
	}

	tests := []struct {
		name    string
		rows    *sqlmock.Rows
		ignore  map[string]struct{}
		want    []string
		wantErr error
	}{
		{
			name: "all tables",
			rows: names("Test_Table_1", "Test_Table_2"),
			want: []string{"Test_Table_1", "Test_Table_2"},
		},
		{
			name:   "ignored table left out",
			rows:   names("Test_Table_1", "Test_Table_2"),
			ignore: map[string]struct{}{"Test_Table_1": {}},
			want:   []string{"Test_Table_2"},
		},
		{
			name: "null name left out",
			rows: names("Test_Table_1", nil, "Test_Table_3"),
			want: []string{"Test_Table_1", "Test_Table_3"},
		},
		{
			name: "no tables",
			rows: names(),
			want: []string{},
		},
		{
			name:    "iteration error",
			rows:    names("Test_Table_1").RowError(0, errBoom),
			wantErr: errBoom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, mock := newMockData(t)
			data.IgnoreTables = tt.ignore

			mock.ExpectQuery(sqlShowTables).WillReturnRows(tt.rows)

			got, err := data.GetTables(t.Context())

			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestData_GetTables_QueryError(t *testing.T) {
	t.Parallel()

	data, mock := newMockData(t)

	mock.ExpectQuery(sqlShowTables).WillReturnError(errBoom)

	_, err := data.GetTables(t.Context())

	require.ErrorIs(t, err, errBoom)
}

func TestData_CreateTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schema   string
		expect   func(mock sqlmock.Sqlmock)
		wantView bool
		wantIs   error
		wantMsg  string
	}{
		{
			name:   "base table",
			schema: testSchema,
			expect: func(mock sqlmock.Sqlmock) {
				expectTableType(mock, testSchema, "Test_Table", "BASE TABLE")
			},
		},
		{
			name:   "view",
			schema: testSchema,
			expect: func(mock sqlmock.Sqlmock) {
				expectTableType(mock, testSchema, "Test_Table", "VIEW")
			},
			wantView: true,
		},
		{
			name: "empty schema resolves the current database",
			expect: func(mock sqlmock.Sqlmock) {
				expectDatabase(mock, currentDB)
				expectTableType(mock, currentDB, "Test_Table", "BASE TABLE")
			},
		},
		{
			name: "empty schema resolves the current database for a view",
			expect: func(mock sqlmock.Sqlmock) {
				expectDatabase(mock, currentDB)
				expectTableType(mock, currentDB, "Test_Table", "VIEW")
			},
			wantView: true,
		},
		{
			name: "empty schema and no database selected",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(sqlDatabase).WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(nil))
			},
			wantMsg: "no database selected",
		},
		{
			name: "empty schema and empty database name",
			expect: func(mock sqlmock.Sqlmock) {
				expectDatabase(mock, "")
			},
			wantMsg: "no database selected",
		},
		{
			name: "current database query fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(sqlDatabase).WillReturnError(errBoom)
			},
			wantIs: errBoom,
		},
		{
			name:   "table type query fails",
			schema: testSchema,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(sqlTableType).WillReturnError(errBoom)
			},
			wantIs: errBoom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, mock := newMockData(t)

			tt.expect(mock)

			table, err := data.CreateTable(t.Context(), tt.schema, "Test_Table")

			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

			if tt.wantIs != nil || tt.wantMsg != "" {
				require.Error(t, err)
				require.Nil(t, table)

				if tt.wantIs != nil {
					require.ErrorIs(t, err, tt.wantIs)
				}

				if tt.wantMsg != "" {
					require.ErrorContains(t, err, tt.wantMsg)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, "Test_Table", table.Name)
			require.Equal(t, tt.wantView, table.IsView)
		})
	}
}

func TestMetaData_UpdateServerVersion(t *testing.T) {
	t.Parallel()

	t.Run("version stored", func(t *testing.T) {
		t.Parallel()

		data, mock := newMockData(t)
		expectVersion(mock)

		meta := mysqldump.MetaData{}

		require.NoError(t, meta.UpdateServerVersion(t.Context(), data), "error was not expected while updating stats")
		require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
		require.Equal(t, "test_version", meta.ServerVersion)
	})

	t.Run("query fails", func(t *testing.T) {
		t.Parallel()

		data, mock := newMockData(t)
		mock.ExpectQuery(sqlVersion).WillReturnError(errBoom)

		meta := mysqldump.MetaData{}

		require.ErrorIs(t, meta.UpdateServerVersion(t.Context(), data), errBoom)
		require.Empty(t, meta.ServerVersion)
	})
}

func TestTable_NameEsc(t *testing.T) {
	t.Parallel()

	require.Equal(t, "`Test_Table`", (&mysqldump.Table{Name: "Test_Table"}).NameEsc())
}

func TestTable_CreateSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    tableSpec
		rows    *sqlmock.Rows
		wantSQL string
		wantIs  error
		wantMsg string
	}{
		{
			name:    "table",
			spec:    fixtureTable,
			rows:    sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("Test_Table", tableCreateSQL),
			wantSQL: tableCreateSQL,
		},
		{
			name:    "view",
			spec:    fixtureView,
			rows:    viewRows("Test_View", viewCreateSQL),
			wantSQL: viewCreateSQL,
		},
		{
			name:   "no row returned",
			spec:   fixtureTable,
			rows:   sqlmock.NewRows([]string{"Table", "Create Table"}),
			wantIs: sql.ErrNoRows,
		},
		{
			name:   "row iteration fails",
			spec:   fixtureTable,
			rows:   sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("Test_Table", tableCreateSQL).RowError(0, errBoom),
			wantIs: errBoom,
		},
		{
			name:    "single column result",
			spec:    fixtureTable,
			rows:    sqlmock.NewRows([]string{"Table"}).AddRow("Test_Table"),
			wantMsg: "fewer than two columns",
		},
		{
			name:    "another table returned",
			spec:    fixtureTable,
			rows:    sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("Other_Table", tableCreateSQL),
			wantMsg: "returned table is not the same as requested table",
		},
		{
			name:   "query fails",
			spec:   fixtureTable,
			wantIs: errBoom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, mock := newMockData(t)

			expectTableType(mock, testSchema, tt.spec.name, tt.spec.typeName())

			query := mock.ExpectQuery("^SHOW CREATE (TABLE|VIEW) `" + tt.spec.name + "`$")
			if tt.rows != nil {
				query.WillReturnRows(tt.rows)
			} else {
				query.WillReturnError(errBoom)
			}

			table, err := data.CreateTable(t.Context(), testSchema, tt.spec.name)
			require.NoError(t, err)

			got, err := table.CreateSQL(t.Context())

			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)

				return
			}

			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantSQL, got)
		})
	}
}

func TestTable_Init(t *testing.T) {
	t.Parallel()

	t.Run("second call fails", func(t *testing.T) {
		t.Parallel()

		_, mock, table := newTable(t, stringRows("a"))

		require.NoError(t, table.Init(t.Context()))
		require.ErrorContains(t, table.Init(t.Context()), "cannot init twice")
		require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
	})

	t.Run("uses the data assigned by SetData", func(t *testing.T) {
		t.Parallel()

		data, mock := newMockData(t)
		mock.ExpectQuery(selectAll("t")).WillReturnRows(stringRows("a"))

		table := &mysqldump.Table{Name: "t"}
		table.SetData(data)

		require.NoError(t, table.Init(t.Context()))
		require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
	})
}

func TestTable_Next(t *testing.T) {
	t.Parallel()

	t.Run("scan error is stored", func(t *testing.T) {
		t.Parallel()

		rows := sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn("c").OfType("INT", int64(0))).AddRow("abc")
		_, _, table := newTable(t, rows)

		require.False(t, table.Next(t.Context()))
		require.ErrorContains(t, table.Err, "scan row of table t")
	})

	t.Run("select error is stored", func(t *testing.T) {
		t.Parallel()

		data, mock := newMockData(t)
		mock.ExpectQuery(selectAll("t")).WillReturnError(errBoom)

		table := &mysqldump.Table{Name: "t"}
		table.SetData(data)

		require.False(t, table.Next(t.Context()))
		require.ErrorIs(t, table.Err, errBoom)
	})
}

func TestTable_RowValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		column *sqlmock.Column
		value  driver.Value
		want   string
	}{
		{
			name:   "not null integer is unquoted",
			column: sqlmock.NewColumn("c").OfType("INT", int64(0)),
			value:  int64(42),
			want:   "(42)",
		},
		{
			name:   "varchar is quoted",
			column: sqlmock.NewColumn("c").OfType("VARCHAR", ""),
			value:  "text",
			want:   "('text')",
		},
		{
			name:   "decimal is quoted",
			column: sqlmock.NewColumn("c").OfType("DECIMAL", ""),
			value:  "12.50",
			want:   "('12.50')",
		},
		{
			name:   "null string",
			column: sqlmock.NewColumn("c").OfType("VARCHAR", ""),
			value:  nil,
			want:   "(NULL)",
		},
		{
			name:   "empty string stays a string",
			column: sqlmock.NewColumn("c").OfType("VARCHAR", ""),
			value:  "",
			want:   "('')",
		},
		{
			name:   "blob with content",
			column: sqlmock.NewColumn("c").OfType("BLOB", []byte{}),
			value:  []byte("raw'data"),
			want:   `(_binary 'raw\'data')`,
		},
		{
			name:   "empty blob is not NULL",
			column: sqlmock.NewColumn("c").OfType("BLOB", []byte{}),
			value:  []byte{},
			want:   "(_binary '')",
		},
		{
			name:   "null blob",
			column: sqlmock.NewColumn("c").OfType("BLOB", []byte{}),
			value:  nil,
			want:   "(NULL)",
		},
		{
			name:   "column without scan type is quoted",
			column: sqlmock.NewColumn("c"),
			value:  "x",
			want:   "('x')",
		},
		{
			name:   "string literal is escaped",
			column: sqlmock.NewColumn("c").OfType("VARCHAR", ""),
			value:  "O'Reilly \\ \"q\" \n\r\x00\x1a\b",
			want:   `('O\'Reilly \\ \"q\" \n\r\0\Z\b')`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := sqlmock.NewRowsWithColumnDefinition(tt.column).AddRow(tt.value)
			_, _, table := newTable(t, rows)

			require.True(t, table.Next(t.Context()))
			require.NoError(t, table.Err)
			require.Equal(t, tt.want, table.RowValues())

			// Scanning into RawBytes keeps the rows locked until the next call, which also closes them.
			require.False(t, table.Next(t.Context()))
			require.NoError(t, table.Err)
		})
	}
}

func TestTable_RowValues_Columns(t *testing.T) {
	t.Parallel()

	_, _, table := newTable(t, tableRows())

	var got []string

	for table.Next(t.Context()) {
		got = append(got, table.RowValues())
	}

	require.NoError(t, table.Err)
	require.Equal(t, []string{"('1',NULL,'Test Name 1')", "('2','test2@test.de','Test Name 2')"}, got)
}

func TestTable_Stream(t *testing.T) {
	t.Parallel()

	const (
		prefix = "INSERT INTO `t` VALUES "
		tuple  = "('aa')"
	)

	// A statement of n tuples is len(prefix)+7n bytes, so two tuples take 37 bytes.
	statement := func(tuples int) string {
		return prefix + strings.TrimSuffix(strings.Repeat(tuple+",", tuples), ",") + ";"
	}

	tests := []struct {
		name   string
		rows   int
		packet int
		want   []string
	}{
		{name: "zero rows emit nothing", rows: 0, packet: 4096, want: nil},
		{name: "large packet batches all rows", rows: 3, packet: 4096, want: []string{statement(3)}},
		{name: "packet one byte below two tuples", rows: 3, packet: 36, want: []string{statement(1), statement(1), statement(1)}},
		{name: "packet equal to two tuples", rows: 3, packet: 37, want: []string{statement(2), statement(1)}},
		{name: "packet above two tuples", rows: 3, packet: 38, want: []string{statement(2), statement(1)}},
		{name: "packet below two tuples", rows: 3, packet: 35, want: []string{statement(1), statement(1), statement(1)}},
		{name: "row larger than the packet is emitted alone", rows: 3, packet: 10, want: []string{statement(1), statement(1), statement(1)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			values := make([]string, tt.rows)
			for i := range values {
				values[i] = "aa"
			}

			data, mock, table := newTable(t, stringRows(values...))
			data.MaxAllowedPacket = tt.packet

			var got []string
			for stmt := range table.Stream(t.Context()) {
				got = append(got, stmt)
			}

			require.NoError(t, table.Err)
			require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTable_Stream_KeepsEveryRow(t *testing.T) {
	t.Parallel()

	const prefix = "INSERT INTO `t` VALUES "

	for _, rowCount := range []int{0, 1, 2, 7, 12} {
		for _, packet := range []int{1, 35, 36, 37, 60, 100, 4096} {
			t.Run(fmt.Sprintf("rows=%d/packet=%d", rowCount, packet), func(t *testing.T) {
				t.Parallel()

				values := make([]string, rowCount)
				for i := range values {
					values[i] = "aa"
				}

				data, _, table := newTable(t, stringRows(values...))
				data.MaxAllowedPacket = packet

				tuples := 0

				for stmt := range table.Stream(t.Context()) {
					require.True(t, strings.HasPrefix(stmt, prefix), "statement %q", stmt)
					require.True(t, strings.HasSuffix(stmt, ";"), "statement %q", stmt)

					count := strings.Count(stmt, "('aa')")
					tuples += count

					// A lone tuple may exceed the packet: a row cannot be split.
					if count >= 2 {
						require.LessOrEqual(t, len(stmt), packet, "statement %q", stmt)
					}
				}

				require.NoError(t, table.Err)
				require.Equal(t, rowCount, tuples)
			})
		}
	}
}

func TestTable_Stream_ContextCancelled(t *testing.T) {
	t.Parallel()

	data, _, table := newTable(t, newManyRows())
	data.MaxAllowedPacket = smallPacket

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	stream := table.Stream(ctx)

	// The first statement proves the SELECT finished, so the cancellation hits row streaming.
	require.NotEmpty(t, <-stream)

	cancel()

	timeout := time.After(testDeadline)

	for open := true; open; {
		select {
		case _, open = <-stream:
		case <-timeout:
			require.FailNow(t, "stream did not stop after the context was cancelled")
		}
	}

	require.ErrorIs(t, table.Err, context.Canceled)
}

// TestTable_Stream_Abandoned leaves the stream undrained after the cancellation. The producer can only exit through
// the context branch of its send, and goleak in TestMain reports it when it does not.
func TestTable_Stream_Abandoned(t *testing.T) {
	t.Parallel()

	data, _, table := newTable(t, newManyRows())
	data.MaxAllowedPacket = smallPacket

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	stream := table.Stream(ctx)

	require.NotEmpty(t, <-stream)

	cancel()
}

func BenchmarkData_Dump(b *testing.B) {
	opts := dumpOpts{specs: []tableSpec{fixtureTable, fixtureView}, lock: true}

	for b.Loop() {
		db, mock, err := sqlmock.New()
		require.NoError(b, err)

		expectDumpBody(mock, opts)

		require.NoError(b, opts.data(db, io.Discard).Dump(b.Context()))
		_ = db.Close()
	}
}

// BenchmarkTable_RowBuffer measures tuple rendering of the current row, with the mock set up once.
func BenchmarkTable_RowBuffer(b *testing.B) {
	for _, size := range []int{16, 1024, 65536} {
		b.Run(fmt.Sprintf("value %dB", size), func(b *testing.B) {
			rows := sqlmock.NewRowsWithColumnDefinition(
				sqlmock.NewColumn("id").OfType("INT", int64(0)),
				sqlmock.NewColumn("name").OfType("VARCHAR", ""),
			).AddRow(int64(1), strings.Repeat("a'", size/2))

			_, _, table := newTable(b, rows)
			require.True(b, table.Next(b.Context()))

			b.ReportAllocs()
			b.SetBytes(int64(size))

			for b.Loop() {
				_ = table.RowBuffer()
			}
		})
	}
}
