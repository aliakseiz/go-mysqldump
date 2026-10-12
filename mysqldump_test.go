package mysqldump_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/aliakseiz/go-mysqldump/v2"
	"github.com/stretchr/testify/require"
)

var errClose = errors.New("close failed")

// closeRecorder is a writer with a Close method that records the call.
type closeRecorder struct {
	bytes.Buffer

	closed bool
	err    error
}

func (c *closeRecorder) Close() error {
	c.closed = true

	return c.err
}

func TestRegister(t *testing.T) {
	t.Parallel()

	t.Run("directory missing", func(t *testing.T) {
		t.Parallel()

		db, _ := newMockDB(t)

		data, err := mysqldump.Register(db, filepath.Join(t.TempDir(), "missing"), "dump", "")

		require.ErrorContains(t, err, "invalid directory")
		require.Nil(t, data)
	})

	t.Run("directory is a file", func(t *testing.T) {
		t.Parallel()

		db, _ := newMockDB(t)

		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, nil, 0o600))

		data, err := mysqldump.Register(db, file, "dump", "")

		require.ErrorContains(t, err, "invalid directory")
		require.Nil(t, data)
	})

	t.Run("dump already exists", func(t *testing.T) {
		t.Parallel()

		db, _ := newMockDB(t)

		dir := t.TempDir()
		existing := filepath.Join(dir, "dump.sql")
		require.NoError(t, os.WriteFile(existing, []byte("keep"), 0o600))

		data, err := mysqldump.Register(db, dir, "dump", "")

		require.ErrorContains(t, err, "dump already exists")
		require.Nil(t, data)

		content, err := os.ReadFile(existing) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.Equal(t, "keep", string(content), "the existing dump must not be overwritten")
	})

	t.Run("file cannot be created", func(t *testing.T) {
		t.Parallel()

		db, _ := newMockDB(t)

		data, err := mysqldump.Register(db, t.TempDir(), "missing/dump", "")

		require.ErrorIs(t, err, fs.ErrNotExist)
		require.ErrorContains(t, err, "create dump file")
		require.Nil(t, data)
	})

	t.Run("dump file created", func(t *testing.T) {
		t.Parallel()

		db, mock := newMockDB(t)
		mock.ExpectClose()

		dir := t.TempDir()

		data, err := mysqldump.Register(db, dir, "dump", "app_db")
		require.NoError(t, err)

		require.FileExists(t, filepath.Join(dir, "dump.sql"))
		require.Same(t, db, data.Connection)
		require.Equal(t, "app_db", data.DBName)
		require.IsType(t, &os.File{}, data.Out)

		require.NoError(t, data.Close())
	})
}

func TestDump(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)
	expectDumpBody(mock, dumpOpts{specs: []tableSpec{fixtureTable}})

	var buf bytes.Buffer

	require.NoError(t, mysqldump.Dump(t.Context(), db, &buf))
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	want := dumpOpts{specs: []tableSpec{fixtureTable}}.wantOutput()
	require.Equal(t, want, dumpText(t, buf.String()))
}

func TestData_Close(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		out     io.Writer
		connErr error
		wantIs  []error
		wantMsg string
	}{
		{
			name: "closes the out stream and the connection",
			out:  &closeRecorder{},
		},
		{
			name: "out stream without a Close method",
			out:  &bytes.Buffer{},
		},
		{
			name:   "out stream close error",
			out:    &closeRecorder{err: errClose},
			wantIs: []error{errClose}, wantMsg: "close out stream",
		},
		{
			name:    "connection close error",
			out:     &closeRecorder{},
			connErr: errBoom,
			wantIs:  []error{errBoom}, wantMsg: "close connection",
		},
		{
			name:    "both close errors are reported",
			out:     &closeRecorder{err: errClose},
			connErr: errBoom,
			wantIs:  []error{errClose, errBoom},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock := newMockDB(t)

			closeExpectation := mock.ExpectClose()
			if tt.connErr != nil {
				closeExpectation.WillReturnError(tt.connErr)
			}

			data := &mysqldump.Data{Out: tt.out, Connection: db}

			err := data.Close()

			if recorder, ok := tt.out.(*closeRecorder); ok {
				require.True(t, recorder.closed, "the out stream must be closed")
			}

			require.Nil(t, data.Connection)
			require.Nil(t, data.Out)
			require.NoError(t, mock.ExpectationsWereMet(), "the connection must be closed")

			if len(tt.wantIs) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, want := range tt.wantIs {
				require.ErrorIs(t, err, want)
			}

			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
			}
		})
	}
}

func TestData_Close_Twice(t *testing.T) {
	t.Parallel()

	db, mock := newMockDB(t)
	mock.ExpectClose()

	out := &closeRecorder{}
	data := &mysqldump.Data{Out: out, Connection: db}

	require.NoError(t, data.Close())
	require.NotPanics(t, func() { require.NoError(t, data.Close()) })
	require.NoError(t, mock.ExpectationsWereMet(), "the connection must be closed once")
}

func TestData_Close_WithoutConnection(t *testing.T) {
	t.Parallel()

	out := &closeRecorder{}
	data := &mysqldump.Data{Out: out}

	require.NotPanics(t, func() { require.NoError(t, data.Close()) })
	require.True(t, out.closed, "the out stream must be closed")
}
