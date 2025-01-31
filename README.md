# Go MySQL dump

[![License][License-Image]][License-Url]
[![Godoc][Godoc-Image]][Godoc-Url]
[![ReportCard][ReportCard-Image]][ReportCard-Url]

Create MySQL dumps without `mysqldump` dependency.

Fork from [JamesStewy/go-mysqldump](https://github.com/JamesStewy/go-mysqldump). Includes pull request [JamesStewy/go-mysqldump#14](https://github.com/JamesStewy/go-mysqldump/pull/14).

### Features
- Works with https://golang.org/pkg/compress/
- Partial writes not stored in memory and stream monitoring. Works with https://github.com/machinebox/progress
- Supports unquoted numbers
- Supports BLOB dumps
- Supports VIEW tables
- Allows ignoring tables
- SQL string quoting / sanitation (useful for BLOBs and JSONs)
- Concurrent dumping


### Example

```go
package main

import (
	"database/sql"
	"fmt"

	"github.com/aliakseiz/go-mysqldump"
	"github.com/go-sql-driver/mysql"
)

func main() {
	// Open connection to database
	config := mysql.NewConfig()
	config.User = "your-user"
	config.Passwd = "your-pw"
	config.DBName = "your-db"
	config.Net = "tcp"
	config.Addr = "your-hostname:your-port"

	dumpDir := "dumps"                                                     // you should create this directory
	dumpFilenameFormat := fmt.Sprintf("%s-20060102T150405", config.DBName) // accepts time layout string and add .sql at the end of file
	
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		fmt.Println("Error opening database: ", err)
		
		return
	}
	// Register database with mysqldump
	dumper, err := mysqldump.Register(db, dumpDir, dumpFilenameFormat, config.DBName)
	if err != nil {
		fmt.Println("Error registering database:", err)
		
		return
	}
	// Dump database to file
	if err = dumper.Dump(); err != nil {
		fmt.Println("Error dumping:", err)
		
		return
	}
	
	fmt.Printf("File is saved to %s", dumpFilenameFormat)
	// Close dumper, connected database and file stream.
	dumper.Close()
}

```

[License-Url]: http://opensource.org/licenses/MIT
[License-Image]: https://img.shields.io/npm/l/express.svg

[Stability-Status-Image]: http://badges.github.io/stability-badges/dist/experimental.svg

[Godoc-Url]: https://pkg.go.dev/mod/github.com/aliakseiz/go-mysqldump
[Godoc-Image]: https://godoc.org/github.com/aliakseiz/go-mysqldump?status.svg

[ReportCard-Url]: https://goreportcard.com/report/github.com/aliakseiz/go-mysqldump
[ReportCard-Image]: https://goreportcard.com/badge/github.com/aliakseiz/go-mysqldump
