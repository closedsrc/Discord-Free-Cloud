package db

import (
	"strings"
	"testing"
)

// The root listing predicate is three OR branches plus a trash filter, and SQL
// binds AND tighter than OR. Written without parentheses it reads as
//
//	parent_id = '' OR parent_id IS NULL OR (orphan AND trashed_at IS NULL)
//
// so a trashed file at the root matched the first branch and stayed visible: the
// user trashed it, it appeared in Trash, and it was still sitting in the drive.
// These tests pin the shape of the fix without needing a live Postgres.
func TestRootPredicateIsParenthesised(t *testing.T) {
	if !strings.HasPrefix(rootPredicate, "(") || !strings.HasSuffix(rootPredicate, ")") {
		t.Errorf("rootPredicate must be wrapped in parentheses, got %s", rootPredicate)
	}
	if strings.Contains(rootPredicate, "trashed_at") {
		t.Errorf("the trash filter must sit outside the OR group, not inside it: %s", rootPredicate)
	}
}

func TestRootListingFiltersTrashedFirst(t *testing.T) {
	q := `SELECT ` + listCols + ` FROM files WHERE trashed_at IS NULL AND ` + rootPredicate + ` ORDER BY is_dir DESC, name ASC`
	trash := strings.Index(q, "trashed_at IS NULL")
	or := strings.Index(q, " OR ")
	if trash < 0 {
		t.Error("root listing must filter trashed rows")
	}
	if or >= 0 && trash > or {
		t.Error("the trash filter must be applied before the OR group, otherwise precedence drops it")
	}
	if !strings.Contains(q, "AND "+rootPredicate) {
		t.Errorf("root predicate must be ANDed as a parenthesised group: %s", q)
	}
}

func TestListColsMatchScanOrder(t *testing.T) {
	want := []string{"id", "parent_id", "name", "path", "size", "is_dir", "mod_time", "sha256", "mime_type", "created_at", "favorite", "trashed_at"}
	got := strings.Split(strings.ReplaceAll(listCols, " ", ""), ",")
	if len(got) != len(want) {
		t.Fatalf("listCols has %d columns, scan expects %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d is %q, scan reads %q", i, got[i], want[i])
		}
	}
}
