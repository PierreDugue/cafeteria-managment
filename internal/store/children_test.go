package store

import (
	"context"
	"slices"
	"testing"
)

func seedChildren(t *testing.T, s *Store, rows []ImportRow) {
	t.Helper()
	if _, err := s.ImportFamilies(context.Background(), ImportRequest{FileName: "a.xlsx", RowsRead: len(rows), Rows: rows}); err != nil {
		t.Fatal(err)
	}
}

func childNames(cs []Child) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Name+"|"+c.Email)
	}
	return out
}

var childrenRows = []ImportRow{
	{ParentName: "Zed Parent", ChildName: "Zoé", Email: "z@example.com"},
	{ParentName: "Anne Martin", ChildName: "Émile", Email: "anne@example.com"},
	{ParentName: "Bob Durand", ChildName: "léa", Email: "bob@example.com"},
	{ParentName: "Carl Durand", ChildName: "Léa", Email: "carl@example.com"},
	{ParentName: "Anne Martin", ChildName: "Alice", Email: "anne@example.com"},
}

func TestChildren(t *testing.T) {
	ctx := context.Background()

	t.Run("empty database", func(t *testing.T) {
		got, err := openTest(t).Children(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("got %#v, want empty non-nil slice", got)
		}
	})

	t.Run("order and parent fields", func(t *testing.T) {
		s := openTest(t)
		seedChildren(t, s, childrenRows)
		got, err := s.Children(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"Alice|anne@example.com",
			"Émile|anne@example.com",
			"léa|bob@example.com",
			"Léa|carl@example.com",
			"Zoé|z@example.com",
		}
		if !slices.Equal(childNames(got), want) {
			t.Fatalf("order = %v, want %v", childNames(got), want)
		}
		for _, c := range got {
			if c.ID == 0 || c.ParentID == 0 || c.ParentName == "" || c.Email == "" {
				t.Errorf("incomplete child %+v", c)
			}
		}
		if got[1].ParentName != "Anne Martin" || got[0].ParentID != got[1].ParentID {
			t.Errorf("parent fields wrong: %+v %+v", got[0], got[1])
		}
	})

	t.Run("same name ordered by email", func(t *testing.T) {
		s := openTest(t)
		seedChildren(t, s, []ImportRow{
			{ParentName: "P2", ChildName: "Léa", Email: "b@example.com"},
			{ParentName: "P1", ChildName: "Léa", Email: "a@example.com"},
		})
		got, _ := s.Children(ctx)
		want := []string{"Léa|a@example.com", "Léa|b@example.com"}
		if !slices.Equal(childNames(got), want) {
			t.Fatalf("got %v, want %v", childNames(got), want)
		}
	})
}

func TestChildrenByIDs(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChildren(t, s, childrenRows)
	all, err := s.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty input", func(t *testing.T) {
		for _, ids := range [][]int64{nil, {}} {
			got, err := s.ChildrenByIDs(ctx, ids)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || len(got) != 0 {
				t.Fatalf("got %#v, want empty non-nil slice", got)
			}
		}
	})

	t.Run("subset in store order", func(t *testing.T) {
		// Ask in reverse order; result must follow the store order.
		ids := []int64{all[4].ID, all[1].ID, all[0].ID}
		got, err := s.ChildrenByIDs(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		want := []Child{all[0], all[1], all[4]}
		if !slices.Equal(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("unknown ids skipped", func(t *testing.T) {
		got, err := s.ChildrenByIDs(ctx, []int64{9999, all[2].ID, -1, 0})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []Child{all[2]}) {
			t.Fatalf("got %+v", got)
		}
		got, err = s.ChildrenByIDs(ctx, []int64{9999})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})

	t.Run("all ids equals Children", func(t *testing.T) {
		ids := []int64{}
		for _, c := range all {
			ids = append(ids, c.ID)
		}
		got, err := s.ChildrenByIDs(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, all) {
			t.Fatalf("got %+v, want %+v", got, all)
		}
	})

	t.Run("many ids", func(t *testing.T) {
		ids := make([]int64, 2000)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		got, err := s.ChildrenByIDs(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(all) {
			t.Fatalf("got %d children, want %d", len(got), len(all))
		}
	})

	t.Run("closed store", func(t *testing.T) {
		c := openTest(t)
		c.Close()
		if _, err := c.ChildrenByIDs(ctx, []int64{1}); err == nil {
			t.Fatal("want error")
		}
		if _, err := c.Children(ctx); err == nil {
			t.Fatal("want error")
		}
	})
}
