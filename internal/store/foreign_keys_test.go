package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// SQLite disables foreign keys per connection unless the DSN says otherwise,
// and every ON DELETE CASCADE in this schema then does nothing. The README
// documents a DSN carrying the parameter; nothing required it, and an operator
// who wrote file:/data/git-ctx.db — a DSN this platform starts on without
// complaint — got a database that does not enforce its own constraints.
//
// It was not even consistently wrong, which is why it survived. The migration
// that rebuilds the repositories table turns foreign keys off and back on, and
// that PRAGMA sticks to the single pooled connection: the boot that migrated
// enforced, and every restart after it did not. A restart is the case this test
// is about.
func TestForeignKeysAreEnforcedWhateverTheDSNSays(t *testing.T) {
	for _, dsn := range []string{
		"file:" + filepath.Join(t.TempDir(), "plain.db"),
		"file:" + filepath.Join(t.TempDir(), "explicit.db") + "?_foreign_keys=on",
		"file:" + filepath.Join(t.TempDir(), "other.db") + "?_busy_timeout=1000",
	} {
		t.Run(dsn, func(t *testing.T) {
			ctx := context.Background()
			first, err := Open(ctx, "sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = first.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','s','alice','')`); err != nil {
				t.Fatal(err)
			}
			first.DB.Close()

			// The restart. Every migration has already run, so nothing sets the
			// PRAGMA on the way through any more.
			reopened, err := Open(ctx, "sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.DB.Close()

			var on int
			if err = reopened.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
				t.Fatal(err)
			}
			if on != 1 {
				t.Fatalf("foreign keys are off after a restart, so every ON DELETE CASCADE in the schema is inert")
			}

			// A cascade that does not fire leaves rows nothing will ever collect:
			// notifications are pruned by the retention pass and their deliveries
			// would outlive them for good.
			if _, err = reopened.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES('n1','u1','t','r','T','M')`); err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.DB.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash) VALUES('d1','n1','email','h')`); err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.DB.Exec(`DELETE FROM notifications WHERE id='n1'`); err != nil {
				t.Fatal(err)
			}
			var orphaned int
			if err = reopened.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&orphaned); err != nil {
				t.Fatal(err)
			}
			if orphaned != 0 {
				t.Errorf("%d delivery rows outlived the notification they belong to", orphaned)
			}

			// And a row naming a parent that does not exist has to be refused.
			if _, err = reopened.DB.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash) VALUES('d2','ghost','email','h2')`); err == nil {
				t.Error("a delivery for a notification that does not exist was accepted")
			}
		})
	}
}

// A parameter the operator did set is theirs. Supplying a default must not
// rewrite a deliberate choice, including the choice to turn something off.
func TestOperatorDSNParametersAreNotOverwritten(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"file:a.db", "file:a.db?_foreign_keys=on&_busy_timeout=5000"},
		{"file:a.db?cache=shared", "file:a.db?cache=shared&_foreign_keys=on&_busy_timeout=5000"},
		{"file:a.db?_busy_timeout=250", "file:a.db?_busy_timeout=250&_foreign_keys=on"},
		{"file:a.db?_foreign_keys=off", "file:a.db?_foreign_keys=off&_busy_timeout=5000"},
		{":memory:", ":memory:"},
	} {
		got := sqliteDefaults(c.in)
		if !sameParameters(got, c.want) {
			t.Errorf("sqliteDefaults(%q) = %q, want the parameters of %q", c.in, got, c.want)
		}
	}
}

// sameParameters compares two DSNs without depending on the order the defaults
// happen to be appended in.
func sameParameters(a, b string) bool {
	splitDSN := func(dsn string) (string, map[string]bool) {
		path, query, _ := strings.Cut(dsn, "?")
		set := map[string]bool{}
		for _, pair := range strings.Split(query, "&") {
			if pair != "" {
				set[pair] = true
			}
		}
		return path, set
	}
	pathA, setA := splitDSN(a)
	pathB, setB := splitDSN(b)
	if pathA != pathB || len(setA) != len(setB) {
		return false
	}
	for key := range setA {
		if !setB[key] {
			return false
		}
	}
	return true
}
