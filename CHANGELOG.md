# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/en/1.0.0/)
and this project adheres to [Semantic Versioning](http://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Unit, function and integration tests

### Changed
- `Version` in the dump header from `0.5.1` to `2.0.0`

### Fixed
- `SHOW CREATE VIEW` failure
- `Data.Close` panic on a second call or on a `Data` without a `Connection`
- `INSERT` statement exceeding `MaxAllowedPacket` by one byte
- Empty `BLOB` value dumped as `NULL`; it is now dumped as `_binary ''`
- Restore failed with `Error 1146`
- Restore failed because of the `DEFINER=` clause in dumped views
- Backticks in table and database names not doubled

### Deprecated

### Removed
- Outdated tests

### Security

## [2.0.0] - 2026-10-12

## BREAKING CHANGES
- Module path is `github.com/aliakseiz/go-mysqldump/v2`
- Methods that query the database take a `context.Context` as first parameter. All queries use the context, so a dump can be cancelled or time-limited

### Changed
- `UNLOCK TABLES` still runs after the context is cancelled
- Failing `SHOW CREATE` no longer leaves a half-written table header in the output
- `IgnoreTables` uses map instead of slice
- Errors from `database/sql`, `os` and `text/template` are wrapped with context
- `Close` returns the error as well as the connection
- Updated Go to 1.27

### Fixed
- Failed `SELECT` produced a truncated table and a successful `Dump`
- `Stream` goroutine stayed blocked forever when the output writer failed
- Linter warnings
- Outdated example in `doc.go`

## [1.3.0] - 2025-10-23

### Fixed
- Empty schema name is correctly handled by using the current database

## [1.2.0] - 2025-01-31

### Added
- `schema` parameter to table operations for more precise database handling

### Removed
- Unnecessary blank import in README.md

## [1.1.0] - 2024-05-28

### Added
- Support of VIEW tables

### Changed
- Update `go-sqlmock` from `v1.5.0` to `v1.5.2`
- Update `testify` from `v1.6.1` to `v1.9.0`

## [1.0.2] - 2022-07-04

### Fixed
- Dump of databases with names containing hyphens (`some-service-db`)

## [1.0.1] - 2020-12-08

### Changed
- Linter 1.31 with gofumpt.

## [1.0.0] - 2020-12-08
Initial release based on [JamesStewy/go-mysqldump](https://github.com/JamesStewy/go-mysqldump) including [PR#14](https://github.com/JamesStewy/go-mysqldump/pull/14)

### Added
- Database name to drop and recreate in a dump
