package mysqldump

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var (
	errInvalidDir    = errors.New("invalid directory")
	errExistsAlready = errors.New("dump already exists")
)

// Register a new dumper.
// db: Database that will be dumped (https://golang.org/pkg/database/sql/#DB).
// dir: Path to the directory where the dumps will be stored.
// format: Format to be used to name each dump file.
//
//	Uses time.Time.Format (https://golang.org/pkg/time/#Time.Format). format appended with '.sql'.
//
// database: Database name to drop and create within a dump file. Recreate section omitted, when empty.
func Register(db *sql.DB, dir, format, database string) (*Data, error) {
	if !isDir(dir) {
		return nil, errInvalidDir
	}

	name := time.Now().Format(format)
	filePath := filepath.Join(dir, name+".sql")

	// Check dump directory
	if exists(filePath) {
		return nil, errExistsAlready
	}

	// Create .sql file. The caller chooses the path by design.
	file, err := os.Create(filePath) //nolint:gosec // dump path is caller-supplied by design
	if err != nil {
		return nil, fmt.Errorf("create dump file: %w", err)
	}

	return &Data{
		Out:        file,
		Connection: db,
		DBName:     database,
	}, nil
}

// Dump Creates a MYSQL dump from the connection to the stream.
func Dump(ctx context.Context, db *sql.DB, out io.Writer) error {
	return (&Data{
		Connection: db,
		Out:        out,
	}).Dump(ctx)
}

// Close the dumper.
// Will also close the database the dumper is connected to as well as the out stream if it has a Close method.
// Calling Close again, or on a Data without a Connection, is safe and closes nothing twice.
func (data *Data) Close() error {
	defer func() {
		data.Connection = nil
		data.Out = nil
	}()

	var outErr error

	if out, ok := data.Out.(io.Closer); ok {
		outErr = out.Close()
	}

	if data.Connection != nil {
		err := data.Connection.Close()
		if err != nil {
			return errors.Join(outErr, fmt.Errorf("close connection: %w", err))
		}
	}

	if outErr != nil {
		return fmt.Errorf("close out stream: %w", outErr)
	}

	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)

	return err == nil && fi.IsDir()
}
