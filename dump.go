package mysqldump

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"reflect"
	"text/template"
	"time"
)

// Data struct to configure dump behavior.
// Out:              Stream to write to
// Connection:       Database connection to dump
// IgnoreTables:     Mark sensitive tables to ignore
// MaxAllowedPacket: Sets the largest packet size to use in backups
// LockTables:       Lock all tables for the duration of the dump.
type Data struct {
	Out              io.Writer
	Connection       *sql.DB
	IgnoreTables     map[string]struct{}
	MaxAllowedPacket int
	LockTables       bool
	DBName           string

	tx           *sql.Tx
	headerTmpl   *template.Template
	recreateTmpl *template.Template
	tableTmpl    *template.Template
	footerTmpl   *template.Template
	err          error
}

type recreate struct {
	Database string
}

// Table struct contains variables for table template.
type Table struct {
	Name   string
	Err    error
	IsView bool

	data   *Data
	rows   *sql.Rows
	values []any
}

// MetaData struct contains variables for header and footer templates.
type MetaData struct {
	DumpVersion   string
	ServerVersion string
	CompleteTime  string
}

const (
	// Version of this plugin for easy reference.
	Version = "0.5.1"

	defaultMaxAllowedPacket = 4194304
)

// takes a *MetaData.
const headerTmpl = `-- Go SQL Dump {{ .DumpVersion }}
--
-- ------------------------------------------------------
-- Server version	{{ .ServerVersion }}

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

// takes a *MetaData.
const footerTmpl = `/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;

/*!40101 SET SQL_MODE=@OLD_SQL_MODE */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;
/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40101 SET CHARACTER_SET_RESULTS=@OLD_CHARACTER_SET_RESULTS */;
/*!40101 SET COLLATION_CONNECTION=@OLD_COLLATION_CONNECTION */;
/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;

-- Dump completed on {{ .CompleteTime }}
`

const tableTmpl = `
--
-- Table structure for table {{ .NameEsc }}
--

{{ if .IsView }}
DROP VIEW IF EXISTS {{ .NameEsc }};
{{ else }}
DROP TABLE IF EXISTS {{ .NameEsc }};
{{ end }}
/*!40101 SET @saved_cs_client     = @@character_set_client */;
 SET character_set_client = utf8mb4 ;
{{ .CreateSQL }};
/*!40101 SET character_set_client = @saved_cs_client */;

{{ if not .IsView }}
--
-- Dumping data for table {{ .NameEsc }}
--

LOCK TABLES {{ .NameEsc }} WRITE;
/*!40000 ALTER TABLE {{ .NameEsc }} DISABLE KEYS */;
{{ range $value := .Stream }}
{{- $value }}
{{ end -}}
/*!40000 ALTER TABLE {{ .NameEsc }} ENABLE KEYS */;
UNLOCK TABLES;
{{ end }}
`

// Takes a *recreate.
const recreateTmpl = " " +
	"DROP DATABASE IF EXISTS `{{ .Database }}`;" +
	"CREATE DATABASE `{{ .Database }}` CHARACTER SET = 'utf8' COLLATE = 'utf8_general_ci';" +
	"USE `{{ .Database }}`;"

const nullType = "NULL"

var (
	errUnexpectedTable = errors.New("returned table is not the same as requested table")
	errDoubleInit      = errors.New("cannot init twice")
	errNoColumns       = errors.New("no columns in table")
	errNoDatabase      = errors.New("no database selected")
)

// Dump data using struct.
func (data *Data) Dump() error {
	err := data.start()
	if err != nil {
		return err
	}

	// The transaction is read-only, so a rollback error carries no information.
	defer data.rollback() //nolint:errcheck // read-only transaction

	meta, err := data.writeHeader()
	if err != nil {
		return err
	}

	tables, err := data.GetTables()
	if err != nil {
		return err
	}

	unlock, err := data.lockTables(tables)
	if err != nil {
		return err
	}
	defer unlock()

	err = data.dumpTables(tables)
	if err != nil {
		return err
	}

	meta.CompleteTime = time.Now().String()

	err = data.footerTmpl.Execute(data.Out, meta)
	if err != nil {
		return fmt.Errorf("write footer: %w", err)
	}

	return nil
}

// Begin starts a read-only transaction that will be whatever the database was when it was called.
func (data *Data) Begin() error {
	tx, err := data.Connection.BeginTx(context.Background(), &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	data.tx = tx

	return nil
}

// WriteTable fills table template with provided *table data.
func (data *Data) WriteTable(table *Table) error {
	err := data.tableTmpl.Execute(data.Out, table)
	if err != nil {
		return fmt.Errorf("write table %s: %w", table.Name, err)
	}

	return table.Err
}

// GetTemplates initializes the templates on data from the constants in this file.
func (data *Data) GetTemplates() error {
	var err error

	data.headerTmpl, err = template.New("mysqldumpHeader").Parse(headerTmpl)
	if err != nil {
		return fmt.Errorf("parse header template: %w", err)
	}

	data.recreateTmpl, err = template.New("mysqldumpRecreate").Parse(recreateTmpl)
	if err != nil {
		return fmt.Errorf("parse recreate template: %w", err)
	}

	data.tableTmpl, err = template.New("mysqldumpTable").Parse(tableTmpl)
	if err != nil {
		return fmt.Errorf("parse table template: %w", err)
	}

	data.footerTmpl, err = template.New("mysqldumpTable").Parse(footerTmpl)
	if err != nil {
		return fmt.Errorf("parse footer template: %w", err)
	}

	return nil
}

// GetTables returns database tables.
func (data *Data) GetTables() ([]string, error) {
	tables := make([]string, 0)

	rows, err := data.tx.Query("SHOW TABLES")
	if err != nil {
		return tables, fmt.Errorf("show tables: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var table sql.NullString

		err = rows.Scan(&table)
		if err != nil {
			return tables, fmt.Errorf("scan table name: %w", err)
		}

		if table.Valid && !data.isIgnoredTable(table.String) {
			tables = append(tables, table.String)
		}
	}

	err = rows.Err()
	if err != nil {
		return tables, fmt.Errorf("iterate tables: %w", err)
	}

	return tables, nil
}

// CreateTable initializes a Table struct.
func (data *Data) CreateTable(schema, name string) (*Table, error) {
	table := &Table{
		Name: name,
		data: data,
	}

	var tableType string

	// If schema is empty, get the current database
	actualSchema := schema
	if actualSchema == "" {
		var currentDB sql.NullString

		err := data.tx.QueryRow("SELECT DATABASE()").Scan(&currentDB)
		if err != nil {
			return nil, fmt.Errorf("get current database: %w", err)
		}

		if !currentDB.Valid || currentDB.String == "" {
			return nil, errNoDatabase
		}

		actualSchema = currentDB.String
	}

	err := data.tx.QueryRow(
		"SELECT table_type FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
		actualSchema, name,
	).Scan(&tableType)
	if err != nil {
		return nil, fmt.Errorf("get type of table %s: %w", name, err)
	}

	if tableType == "VIEW" {
		table.IsView = true
	}

	return table, nil
}

// start applies defaults, parses templates, and opens the transaction.
func (data *Data) start() error {
	if data.MaxAllowedPacket == 0 {
		data.MaxAllowedPacket = defaultMaxAllowedPacket
	}

	err := data.GetTemplates()
	if err != nil {
		return err
	}

	// Start the read-only transaction. The caller defers the rollback until the end.
	// This way the database will have the exact state it did at the beginning of
	// the backup, and nothing can be accidentally committed.
	return data.Begin()
}

// writeHeader writes the dump header and, when DBName is set, the database recreate section.
func (data *Data) writeHeader() (MetaData, error) {
	meta := MetaData{
		DumpVersion: Version,
	}

	err := meta.UpdateServerVersion(data)
	if err != nil {
		return meta, err
	}

	err = data.headerTmpl.Execute(data.Out, meta)
	if err != nil {
		return meta, fmt.Errorf("write header: %w", err)
	}

	if data.DBName != "" {
		err = data.recreateTmpl.Execute(data.Out, recreate{Database: data.DBName})
		if err != nil {
			return meta, fmt.Errorf("write database recreate section: %w", err)
		}
	}

	return meta, nil
}

// lockTables locks all tables for reading when LockTables is set and returns the function releasing the locks.
func (data *Data) lockTables(tables []string) (func(), error) {
	if !data.LockTables || len(tables) == 0 {
		return func() {}, nil
	}

	var buf bytes.Buffer

	buf.WriteString("LOCK TABLES ")

	for index, name := range tables {
		if index != 0 {
			buf.WriteString(",")
		}

		buf.WriteString("`" + name + "` READ /*!32311 LOCAL */")
	}

	_, err := data.Connection.Exec(buf.String())
	if err != nil {
		return nil, fmt.Errorf("lock tables: %w", err)
	}

	return func() {
		_, _ = data.Connection.Exec("UNLOCK TABLES")
	}, nil
}

func (data *Data) dumpTables(tables []string) error {
	for _, name := range tables {
		err := data.dumpTable(data.DBName, name)
		if err != nil {
			return err
		}
	}

	return data.err
}

// rollback cancels the transaction.
func (data *Data) rollback() error {
	err := data.tx.Rollback()
	if err != nil {
		return fmt.Errorf("rollback transaction: %w", err)
	}

	return nil
}

func (data *Data) dumpTable(schema, name string) error {
	if data.err != nil {
		return data.err
	}

	table, err := data.CreateTable(schema, name)
	if err != nil {
		return err
	}

	return data.WriteTable(table)
}

func (data *Data) isIgnoredTable(name string) bool {
	_, ok := data.IgnoreTables[name]

	return ok
}

// UpdateServerVersion fetches server version and stores it in Data.
func (meta *MetaData) UpdateServerVersion(data *Data) error {
	var serverVersion sql.NullString

	err := data.tx.QueryRow("SELECT version()").Scan(&serverVersion)
	meta.ServerVersion = serverVersion.String

	if err != nil {
		return fmt.Errorf("get server version: %w", err)
	}

	return nil
}

// NameEsc returns the backtick-quoted table name.
func (table *Table) NameEsc() string {
	return "`" + table.Name + "`"
}

// CreateSQL returns the CREATE statement of the table or view.
func (table *Table) CreateSQL() (string, error) {
	var (
		nameReturn, createSQL sql.NullString
		createSQLQuery        string
	)

	if table.IsView {
		createSQLQuery = "SHOW CREATE VIEW " + table.NameEsc()
	} else {
		createSQLQuery = "SHOW CREATE TABLE " + table.NameEsc()
	}

	err := table.data.tx.QueryRow(createSQLQuery).Scan(&nameReturn, &createSQL)
	if err != nil {
		return "", fmt.Errorf("get create statement of table %s: %w", table.Name, err)
	}

	if nameReturn.String != table.Name {
		return "", errUnexpectedTable
	}

	return createSQL.String, nil
}

// Init runs the table SELECT and prepares scan targets according to the column types.
func (table *Table) Init() error {
	if len(table.values) != 0 {
		return errDoubleInit
	}

	var err error

	//nolint:rowserrcheck // rows are iterated and closed in Next
	table.rows, err = table.data.tx.Query(fmt.Sprintf("SELECT * FROM %s", table.NameEsc()))
	if err != nil {
		return fmt.Errorf("select from table %s: %w", table.Name, err)
	}

	columns, err := table.rows.Columns()
	if err != nil {
		return fmt.Errorf("get columns of table %s: %w", table.Name, err)
	}

	if len(columns) == 0 {
		return errNoColumns
	}

	columnTypes, err := table.rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("get column types of table %s: %w", table.Name, err)
	}

	table.values = make([]any, len(columnTypes))

	for i, columnType := range columnTypes {
		table.values[i] = reflect.New(scanTargetType(columnType)).Interface()
	}

	return nil
}

// scanTargetType picks the Go type a column is scanned into.
//
//nolint:ireturn // reflect.Type is the stdlib interface describing types
func scanTargetType(columnType *sql.ColumnType) reflect.Type {
	if columnType.DatabaseTypeName() == "BLOB" {
		return reflect.TypeFor[sql.RawBytes]()
	}

	scanType := columnType.ScanType()
	if scanType == nil {
		return reflect.TypeFor[sql.NullString]()
	}

	switch scanType.Kind() { //nolint:exhaustive // other kinds are scanned as strings
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.TypeFor[sql.NullInt64]()
	default:
		return reflect.TypeFor[sql.NullString]()
	}
}

// Next advances to the next row and scans it into the table values. Failures are stored in Table.Err.
func (table *Table) Next() bool {
	if table.rows == nil {
		err := table.Init()
		if err != nil {
			table.Err = err

			return false
		}
	}
	// Fallthrough
	if !table.rows.Next() {
		err := table.rows.Close()
		if err != nil {
			table.Err = fmt.Errorf("close rows of table %s: %w", table.Name, err)
		}

		table.rows = nil

		return false
	}

	err := table.rows.Scan(table.values...)
	if err != nil {
		table.Err = fmt.Errorf("scan row of table %s: %w", table.Name, err)

		return false
	}

	err = table.rows.Err()
	if err != nil {
		table.Err = fmt.Errorf("iterate rows of table %s: %w", table.Name, err)

		return false
	}

	return true
}

// RowValues returns the current row as an SQL values tuple.
func (table *Table) RowValues() string {
	return table.RowBuffer().String()
}

// RowBuffer returns the current row as an SQL values tuple in a buffer.
func (table *Table) RowBuffer() *bytes.Buffer {
	var buf bytes.Buffer

	buf.WriteString("(")

	for key, value := range table.values {
		if key != 0 {
			buf.WriteString(",")
		}

		writeValue(&buf, value)
	}

	buf.WriteString(")")

	return &buf
}

// writeValue writes a scanned column value as an SQL literal.
func writeValue(buf *bytes.Buffer, value any) {
	switch val := value.(type) {
	case nil:
		buf.WriteString(nullType)
	case *sql.NullString:
		if val.Valid {
			fmt.Fprintf(buf, "'%s'", Sanitize(val.String))
		} else {
			buf.WriteString(nullType)
		}
	case *sql.NullInt64:
		if val.Valid {
			fmt.Fprintf(buf, "%d", val.Int64)
		} else {
			buf.WriteString(nullType)
		}
	case *sql.RawBytes:
		if len(*val) == 0 {
			buf.WriteString(nullType)
		} else {
			fmt.Fprintf(buf, "_binary '%s'", Sanitize(string(*val)))
		}
	default:
		fmt.Fprintf(buf, "'%s'", value)
	}
}

// Stream returns a channel of INSERT statements batching rows up to MaxAllowedPacket.
func (table *Table) Stream() <-chan string {
	valueOut := make(chan string, 1)

	go func() {
		defer close(valueOut)

		var insert bytes.Buffer

		for table.Next() {
			row := table.RowBuffer()
			// Truncate our insert if it won't fit
			if insert.Len() != 0 && insert.Len()+row.Len() > table.data.MaxAllowedPacket-1 {
				insert.WriteString(";")

				valueOut <- insert.String()

				insert.Reset()
			}

			if insert.Len() == 0 {
				fmt.Fprintf(&insert, "INSERT INTO %s VALUES ", table.NameEsc())
			} else {
				insert.WriteString(",")
			}

			insert.Write(row.Bytes())
		}

		if insert.Len() != 0 {
			insert.WriteString(";")

			valueOut <- insert.String()
		}
	}()

	return valueOut
}

// SetData assigns the data pointer to the table.
func (table *Table) SetData(data *Data) {
	table.data = data
}
