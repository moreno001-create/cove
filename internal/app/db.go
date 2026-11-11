package app

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	name TEXT PRIMARY KEY,
	display TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL
);
-- Sessions and app passwords are stored as hashes only.
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS app_passwords (
	id INTEGER PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	name TEXT NOT NULL,
	hash TEXT NOT NULL UNIQUE,
	created INTEGER NOT NULL,
	last_used INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS shares (
	token TEXT PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	path TEXT NOT NULL,
	password TEXT NOT NULL DEFAULT '',
	expires INTEGER NOT NULL DEFAULT 0,
	upload INTEGER NOT NULL DEFAULT 0,
	created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS shares_user ON shares(user, path);
CREATE TABLE IF NOT EXISTS trash (
	id TEXT PRIMARY KEY,
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	orig TEXT NOT NULL,
	deleted INTEGER NOT NULL,
	dir INTEGER NOT NULL,
	size INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS calendars (
	user TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	color TEXT NOT NULL,
	PRIMARY KEY (user, id)
);
-- One calendar object (an .ics file to CalDAV clients): an event, with any changed occurrences.
CREATE TABLE IF NOT EXISTS events (
	user TEXT NOT NULL,
	cal TEXT NOT NULL,
	name TEXT NOT NULL,
	ics TEXT NOT NULL,
	etag TEXT NOT NULL,
	modified INTEGER NOT NULL,
	PRIMARY KEY (user, cal, name),
	FOREIGN KEY (user, cal) REFERENCES calendars(user, id) ON DELETE CASCADE
);
`

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// One connection: SQLite writes one at a time anyway, and this keeps "database is locked" away.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + uploadsSchema + accessSchema + calendarsSchema + contactsSchema + notifySchema + invitesSchema + itemTrashSchema + homeSchema + searchSchema + notesSchema +
		pushSchema + subscriptionsSchema + groupsSchema + activitySchema + hashesSchema + fridgeSchema + householdSchema + chatSchema + photosSchema + aiSchema + settingsSchema + familySchema + extrasSchema + batch4Schema + batch5Schema + householdsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting up the database: %w", err)
	}
	migrateCalendars(db)
	migrateUsers(db)
	migrateGroups(db)
	migrateHousehold(db)
	migrateFamily(db)
	migrateHouseholds(db)
	return db, nil
}
