package mysqldump_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aliakseiz/go-mysqldump/v2"
	"github.com/stretchr/testify/require"
)

const (
	manyRows     = 30
	smallPacket  = 40
	testDeadline = 5 * time.Second
)

var (
	errBoom  = errors.New("boom")
	errWrite = errors.New("write failed")
)

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

func newManyRows() *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "name"})

	for i := range manyRows {
		rows.AddRow(i, "x")
	}

	return rows
}

func expectBaseTable(mock sqlmock.Sqlmock, schema, name string) {
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).
		WithArgs(schema, name).
		WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))
}

func expectCreateTable(mock sqlmock.Sqlmock, name string) {
	mock.ExpectQuery("^SHOW CREATE TABLE `" + name + "`$").
		WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow(name, "CREATE TABLE `"+name+"` (`id` int)"))
}

func TestDumpStopsWhenContextCancelled(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	mock.ExpectQuery(`^SELECT version\(\)$`).WillReturnRows(sqlmock.NewRows([]string{"Version()"}).AddRow("test_version"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	out := &cancelOnWrite{cancel: cancel}

	err = (&mysqldump.Data{Out: out, Connection: db}).Dump(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, out.buf.String(), "Dump completed")
}

func TestDumpReturnsRowIterationError(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"id", "name"}).
		AddRow(1, "a").
		AddRow(2, "b").
		RowError(1, errBoom)

	mock.ExpectBegin()
	mock.ExpectQuery(`^SELECT version\(\)$`).WillReturnRows(sqlmock.NewRows([]string{"Version()"}).AddRow("test_version"))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(sqlmock.NewRows([]string{"Tables_in_Testdb"}).AddRow("test"))
	expectBaseTable(mock, "test_db", "test")
	expectCreateTable(mock, "test")
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(rows)
	mock.ExpectRollback()

	var buf bytes.Buffer

	err = (&mysqldump.Data{Out: &buf, Connection: db, DBName: "test_db"}).Dump(t.Context())

	require.ErrorIs(t, err, errBoom)
	require.NotContains(t, buf.String(), "Dump completed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStreamStopsWhenContextCancelled(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err)

	defer func() { _ = data.Close() }()

	expectBaseTable(mock, "Test_Schema", "test")
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(newManyRows())

	data.MaxAllowedPacket = smallPacket

	table, err := data.CreateTable(t.Context(), "Test_Schema", "test")
	require.NoError(t, err)

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

func TestWriteTableReturnsWriteError(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err)

	defer func() { _ = data.Close() }()

	expectBaseTable(mock, "Test_Schema", "test")
	expectCreateTable(mock, "test")
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(newManyRows())

	data.Out = failingWriter{}
	data.MaxAllowedPacket = smallPacket
	require.NoError(t, data.GetTemplates())

	table, err := data.CreateTable(t.Context(), "Test_Schema", "test")
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
