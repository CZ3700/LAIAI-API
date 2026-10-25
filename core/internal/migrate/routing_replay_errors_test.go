package migrate

import (
	"errors"
	"testing"
	"testing/fstest"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

// Anonymous checks in the unpublished 0129 ALTER avoid a named-constraint
// collision on replay. This must not relax the global migration error policy.
func TestRoutingReplayDoesNotIgnoreDuplicateCheck(t *testing.T) {
	err := &mysqlDriver.MySQLError{Number: 3822, Message: "Duplicate check constraint name 'routing_replay_check'"}
	if isAlreadyExists(err) {
		t.Fatal("duplicate CHECK errors must remain fatal outside the formal replay path")
	}
}

func TestMySQLRoutingDuplicateNamedCheckStopsMigration(t *testing.T) {
	db := runtimeMigrationDatabase(t)
	for _, statement := range []string{
		"CREATE TABLE routing_replay_probe (id INT PRIMARY KEY, value INT NOT NULL, CONSTRAINT routing_replay_check CHECK (value > 0)) ENGINE=InnoDB",
		"INSERT INTO routing_replay_probe(id,value) VALUES(1,1)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	files := fstest.MapFS{
		"0001_check_collision.sql": {Data: []byte("ALTER TABLE routing_replay_probe ADD CONSTRAINT routing_replay_check CHECK (value > 0);")},
		"0002_after_collision.sql": {Data: []byte("CREATE TABLE routing_replay_after_collision(id INT PRIMARY KEY) ENGINE=InnoDB;")},
	}
	ran, err := Up(db, files)
	var sqlErr *mysqlDriver.MySQLError
	if !errors.As(err, &sqlErr) || sqlErr.Number != 3822 || len(ran) != 0 {
		t.Fatalf("named CHECK collision must stop migration with 3822: ran=%v err=%v", ran, err)
	}
	var versions int64
	if err := db.Model(&Record{}).Count(&versions).Error; err != nil || versions != 0 {
		t.Fatal("failed migration recorded a version", versions, err)
	}
	if db.Migrator().HasTable("routing_replay_after_collision") {
		t.Fatal("migration continued past the named CHECK collision")
	}
	var value int
	if err := db.Raw("SELECT value FROM routing_replay_probe WHERE id=1").Scan(&value).Error; err != nil || value != 1 {
		t.Fatal("failed migration changed the existing row", err)
	}
	err = db.Exec("INSERT INTO routing_replay_probe(id,value) VALUES(2,0)").Error
	sqlErr = nil
	if !errors.As(err, &sqlErr) || sqlErr.Number != 3819 {
		t.Fatal("original CHECK no longer enforces its invariant", err)
	}
}
