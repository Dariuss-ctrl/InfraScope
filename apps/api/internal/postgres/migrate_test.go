package postgres

import "testing"

func TestMigrationsAreNumberedAndOrdered(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migration %d is %s (version %d); versions must be consecutive from 1", i, m.name, m.version)
		}
		if m.sql == "" {
			t.Errorf("%s is empty", m.name)
		}
	}
}
