//nolint:lll // SQL expectations are long
package mysqldump_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aliakseiz/go-mysqldump/v2"
	"github.com/stretchr/testify/require"
)

//nolint:ireturn // sqlmock.New returns the Sqlmock interface
func getMockData(t *testing.T) (*mysqldump.Data, sqlmock.Sqlmock, error) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		return nil, nil, fmt.Errorf("create sqlmock: %w", err)
	}

	mock.ExpectBegin()

	data := &mysqldump.Data{
		Connection: db,
	}

	err = data.Begin(t.Context())
	if err != nil {
		return nil, nil, fmt.Errorf("begin: %w", err)
	}

	return data, mock, nil
}

func TestGetTablesOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"Tables_in_Testdb"}).
		AddRow("Test_Table_1").
		AddRow("Test_Table_2")

	mock.ExpectQuery("^SHOW TABLES$").WillReturnRows(rows)

	result, err := data.GetTables(t.Context())
	require.NoError(t, err)

	// we make sure that all conditions were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	require.Equal(t, []string{"Test_Table_1", "Test_Table_2"}, result)
}

func TestIgnoreTablesOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"Tables_in_Testdb"}).
		AddRow("Test_Table_1").
		AddRow("Test_Table_2")

	mock.ExpectQuery("^SHOW TABLES$").WillReturnRows(rows)

	data.IgnoreTables = map[string]struct{}{"Test_Table_1": {}}

	result, err := data.GetTables(t.Context())
	require.NoError(t, err)

	// we make sure that all conditions were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	require.Equal(t, []string{"Test_Table_2"}, result)
}

func TestGetTablesNil(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"Tables_in_Testdb"}).
		AddRow("Test_Table_1").
		AddRow(nil).
		AddRow("Test_Table_3")

	mock.ExpectQuery("^SHOW TABLES$").WillReturnRows(rows)

	result, err := data.GetTables(t.Context())
	require.NoError(t, err)

	// we make sure that all conditions were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	require.Equal(t, []string{"Test_Table_1", "Test_Table_3"}, result)
}

func TestGetServerVersionOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"Version()"}).
		AddRow("test_version")

	mock.ExpectQuery("^SELECT version()").WillReturnRows(rows)

	meta := mysqldump.MetaData{}

	require.NoError(t, meta.UpdateServerVersion(t.Context(), data), "error was not expected while updating stats")

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	require.Equal(t, "test_version", meta.ServerVersion)
}

func TestCreateTableSQLOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	// Add expectation for table type check
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "Test_Table").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))

	rows := sqlmock.NewRows([]string{"Table", "Create Table"}).
		AddRow("Test_Table", "CREATE TABLE 'Test_Table' (`id` int(11) NOT NULL AUTO_INCREMENT,`s` char(60) DEFAULT NULL, PRIMARY KEY (`id`))ENGINE=InnoDB DEFAULT CHARSET=latin1")

	mock.ExpectQuery("^SHOW CREATE TABLE `Test_Table`$").WillReturnRows(rows)

	table, err := data.CreateTable(t.Context(), "Test_Schema", "Test_Table")
	require.NoError(t, err)

	result, err := table.CreateSQL(t.Context())
	require.NoError(t, err)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	expectedResult := "CREATE TABLE 'Test_Table' (`id` int(11) NOT NULL AUTO_INCREMENT,`s` char(60) DEFAULT NULL, PRIMARY KEY (`id`))ENGINE=InnoDB DEFAULT CHARSET=latin1"

	if !reflect.DeepEqual(result, expectedResult) {
		t.Fatalf("expected %#v, got %#v", expectedResult, result)
	}
}

func TestCreateTableRowValues(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, "test@test.de", "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2")

	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "test").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(rows)

	table, err := data.CreateTable(t.Context(), "Test_Schema", "test")
	require.NoError(t, err)

	require.True(t, table.Next(t.Context()))

	result := table.RowValues()
	require.NoError(t, table.Err)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	require.Equal(t, "('1','test@test.de','Test Name 1')", result)
}

func TestCreateTableValuesSteam(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, "test@test.de", "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2")

	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "test").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(rows)

	data.MaxAllowedPacket = 4096

	table, err := data.CreateTable(t.Context(), "Test_Schema", "test")
	require.NoError(t, err)

	s := table.Stream(t.Context())
	require.Equal(t, "INSERT INTO `test` VALUES ('1','test@test.de','Test Name 1'),('2','test2@test.de','Test Name 2');", <-s)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
}

func TestCreateTableValuesSteamSmallPackets(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, "test@test.de", "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2")

	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "test").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(rows)

	data.MaxAllowedPacket = 64

	table, err := data.CreateTable(t.Context(), "Test_Schema", "test")
	require.NoError(t, err)

	s := table.Stream(t.Context())
	require.Equal(t, "INSERT INTO `test` VALUES ('1','test@test.de','Test Name 1');", <-s)
	require.Equal(t, "INSERT INTO `test` VALUES ('2','test2@test.de','Test Name 2');", <-s)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
}

func TestCreateTableAllValuesWithNil(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	rows := sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, nil, "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2").
		AddRow(3, "", "Test Name 3")

	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "test").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))
	mock.ExpectQuery("^SELECT (.+) FROM `test`$").WillReturnRows(rows)

	table, err := data.CreateTable(t.Context(), "Test_Schema", "test")
	require.NoError(t, err)

	results := make([]string, 0)

	for table.Next(t.Context()) {
		row := table.RowValues()
		require.NoError(t, table.Err)

		results = append(results, row)
	}

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	expectedResults := []string{"('1',NULL,'Test Name 1')", "('2','test2@test.de','Test Name 2')", "('3','','Test Name 3')"}

	require.Equal(t, expectedResults, results)
}

func TestCreateTableOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	// Add expectation for table type check
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "Test_Table").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))

	createTableRows := sqlmock.NewRows([]string{"Table", "Create Table"}).
		AddRow("Test_Table", "CREATE TABLE 'Test_Table' (`id` int(11) NOT NULL AUTO_INCREMENT,`s` char(60) DEFAULT NULL, PRIMARY KEY (`id`))ENGINE=InnoDB DEFAULT CHARSET=latin1")

	createTableValueRows := sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, nil, "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2")

	mock.ExpectQuery("^SHOW CREATE TABLE `Test_Table`$").WillReturnRows(createTableRows)
	mock.ExpectQuery("^SELECT (.+) FROM `Test_Table`$").WillReturnRows(createTableValueRows)

	var buf bytes.Buffer

	data.Out = &buf
	data.MaxAllowedPacket = 4096

	require.NoError(t, data.GetTemplates())

	table, err := data.CreateTable(t.Context(), "Test_Schema", "Test_Table")
	require.NoError(t, err)

	_ = data.WriteTable(t.Context(), table)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	expectedResult := `
--
-- Table structure for table ~Test_Table~
--


DROP TABLE IF EXISTS ~Test_Table~;

/*!40101 SET @saved_cs_client     = @@character_set_client */;
 SET character_set_client = utf8mb4 ;
CREATE TABLE 'Test_Table' (~id~ int(11) NOT NULL AUTO_INCREMENT,~s~ char(60) DEFAULT NULL, PRIMARY KEY (~id~))ENGINE=InnoDB DEFAULT CHARSET=latin1;
/*!40101 SET character_set_client = @saved_cs_client */;


--
-- Dumping data for table ~Test_Table~
--

LOCK TABLES ~Test_Table~ WRITE;
/*!40000 ALTER TABLE ~Test_Table~ DISABLE KEYS */;
INSERT INTO ~Test_Table~ VALUES ('1',NULL,'Test Name 1'),('2','test2@test.de','Test Name 2');
/*!40000 ALTER TABLE ~Test_Table~ ENABLE KEYS */;
UNLOCK TABLES;

`
	result := strings.ReplaceAll(buf.String(), "`", "~")
	require.Equal(t, expectedResult, result)
}

func TestCreateTableOkSmallPackets(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	// Add expectation for table type check
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).WithArgs("Test_Schema", "Test_Table").WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))

	createTableRows := sqlmock.NewRows([]string{"Table", "Create Table"}).
		AddRow("Test_Table", "CREATE TABLE 'Test_Table' (`id` int(11) NOT NULL AUTO_INCREMENT,`s` char(60) DEFAULT NULL, PRIMARY KEY (`id`))ENGINE=InnoDB DEFAULT CHARSET=latin1")

	createTableValueRows := sqlmock.NewRows([]string{"id", "email", "name"}).
		AddRow(1, nil, "Test Name 1").
		AddRow(2, "test2@test.de", "Test Name 2")

	mock.ExpectQuery("^SHOW CREATE TABLE `Test_Table`$").WillReturnRows(createTableRows)
	mock.ExpectQuery("^SELECT (.+) FROM `Test_Table`$").WillReturnRows(createTableValueRows)

	buf := new(bytes.Buffer)
	data.Out = buf
	data.MaxAllowedPacket = 64

	require.NoError(t, data.GetTemplates())

	table, err := data.CreateTable(t.Context(), "Test_Schema", "Test_Table")
	require.NoError(t, err)

	_ = data.WriteTable(t.Context(), table)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")

	expectedResult := `
--
-- Table structure for table ~Test_Table~
--


DROP TABLE IF EXISTS ~Test_Table~;

/*!40101 SET @saved_cs_client     = @@character_set_client */;
 SET character_set_client = utf8mb4 ;
CREATE TABLE 'Test_Table' (~id~ int(11) NOT NULL AUTO_INCREMENT,~s~ char(60) DEFAULT NULL, PRIMARY KEY (~id~))ENGINE=InnoDB DEFAULT CHARSET=latin1;
/*!40101 SET character_set_client = @saved_cs_client */;


--
-- Dumping data for table ~Test_Table~
--

LOCK TABLES ~Test_Table~ WRITE;
/*!40000 ALTER TABLE ~Test_Table~ DISABLE KEYS */;
INSERT INTO ~Test_Table~ VALUES ('1',NULL,'Test Name 1');
INSERT INTO ~Test_Table~ VALUES ('2','test2@test.de','Test Name 2');
/*!40000 ALTER TABLE ~Test_Table~ ENABLE KEYS */;
UNLOCK TABLES;

`
	result := strings.ReplaceAll(buf.String(), "`", "~")
	require.Equal(t, expectedResult, result)
}

func TestCreateTableWithEmptySchemaOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	// Mock the DATABASE() query that should be called when schema is empty
	databaseRows := sqlmock.NewRows([]string{"DATABASE()"}).
		AddRow("current_db")

	mock.ExpectQuery("^SELECT DATABASE\\(\\)$").WillReturnRows(databaseRows)

	// Add expectation for table type check with the retrieved database name
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).
		WithArgs("current_db", "Test_Table").
		WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))

	// Call CreateTable with empty schema (simulating empty DBName)
	table, err := data.CreateTable(t.Context(), "", "Test_Table")
	require.NoError(t, err)
	require.NotNil(t, table)
	require.Equal(t, "Test_Table", table.Name)
	require.False(t, table.IsView)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
}

func TestCreateTableWithEmptySchemaAndNoDatabase(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	// Mock the DATABASE() query returning NULL (no database selected)
	databaseRows := sqlmock.NewRows([]string{"DATABASE()"}).
		AddRow(nil)

	mock.ExpectQuery("^SELECT DATABASE\\(\\)$").WillReturnRows(databaseRows)

	// Call CreateTable with empty schema when no database is selected
	table, err := data.CreateTable(t.Context(), "", "Test_Table")
	require.Error(t, err)
	require.Nil(t, table)
	require.EqualError(t, err, "no database selected")

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
}

func TestCreateViewWithEmptySchemaOk(t *testing.T) {
	t.Parallel()

	data, mock, err := getMockData(t)
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = data.Close() }()

	// Mock the DATABASE() query that should be called when schema is empty
	databaseRows := sqlmock.NewRows([]string{"DATABASE()"}).
		AddRow("current_db")

	mock.ExpectQuery("^SELECT DATABASE\\(\\)$").WillReturnRows(databaseRows)

	// Add expectation for table type check - this time it's a VIEW
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).
		WithArgs("current_db", "Test_View").
		WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("VIEW"))

	// Call CreateTable with empty schema (simulating empty DBName)
	table, err := data.CreateTable(t.Context(), "", "Test_View")
	require.NoError(t, err)
	require.NotNil(t, table)
	require.Equal(t, "Test_View", table.Name)
	require.True(t, table.IsView)

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
}

func TestDumpWithEmptyDBName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	data := &mysqldump.Data{
		Out:        &buf,
		LockTables: true,
		DBName:     "", // Empty DBName - the scenario from the issue
	}

	db, mock, err := sqlmock.New()
	require.NoError(t, err, "an error was not expected when opening a stub database connection")

	defer func() { _ = db.Close() }()

	data.Connection = db

	showTablesRows := sqlmock.NewRows([]string{"Tables_in_Testdb"}).
		AddRow("Test_Table")

	serverVersionRows := sqlmock.NewRows([]string{"Version()"}).
		AddRow("test_version")

	createTableRows := sqlmock.NewRows([]string{"Table", "Create Table"}).
		AddRow("Test_Table", "CREATE TABLE 'Test_Table' (`id` int(11) NOT NULL AUTO_INCREMENT, PRIMARY KEY (`id`))ENGINE=InnoDB DEFAULT CHARSET=latin1")

	createTableValueRows := sqlmock.NewRows([]string{"id"}).
		AddRow(1).
		AddRow(2)

	databaseRows := sqlmock.NewRows([]string{"DATABASE()"}).
		AddRow("mydb")

	mock.ExpectBegin()
	mock.ExpectQuery(`^SELECT version\(\)$`).WillReturnRows(serverVersionRows)
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTablesRows)
	mock.ExpectExec("^LOCK TABLES `Test_Table` READ /\\*!32311 LOCAL \\*/$").WillReturnResult(sqlmock.NewResult(1, 1))

	// When DBName is empty, DATABASE() should be called
	mock.ExpectQuery("^SELECT DATABASE\\(\\)$").WillReturnRows(databaseRows)
	mock.ExpectQuery(`SELECT table_type FROM information_schema.tables WHERE table_schema = \? AND table_name = \?`).
		WithArgs("mydb", "Test_Table").
		WillReturnRows(sqlmock.NewRows([]string{"table_type"}).AddRow("BASE TABLE"))
	mock.ExpectQuery("^SHOW CREATE TABLE `Test_Table`$").WillReturnRows(createTableRows)
	mock.ExpectQuery("^SELECT (.+) FROM `Test_Table`$").WillReturnRows(createTableValueRows)

	mock.ExpectRollback()

	require.NoError(t, data.Dump(t.Context()), "an error was not expected when dumping with empty DBName")

	// Verify the output doesn't contain DROP DATABASE / CREATE DATABASE statements
	result := buf.String()
	require.NotContains(t, result, "DROP DATABASE")
	require.NotContains(t, result, "CREATE DATABASE")
	require.Contains(t, result, "DROP TABLE IF EXISTS `Test_Table`")

	// we make sure that all expectations were met
	require.NoError(t, mock.ExpectationsWereMet(), "there were unfulfilled conditions")
}
