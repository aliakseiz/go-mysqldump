# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](http://keepachangelog.com/en/1.0.0/)
and this project adheres to [Semantic Versioning](http://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

### Changed

### Fixed
- Empty schema name is correctly handled by using the current database

### Deprecated

### Removed

### Security

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
