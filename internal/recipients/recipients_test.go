package recipients

import (
	"slices"
	"testing"

	"cafeteria-management/internal/store"
)

func TestGroup(t *testing.T) {
	tests := []struct {
		name string
		in   []store.Child
		want []Recipient
	}{
		{"nil", nil, []Recipient{}},
		{"empty", []store.Child{}, []Recipient{}},
		{
			"two children one parent keep input order",
			[]store.Child{
				{ID: 2, Name: "Zoé", ParentID: 1, ParentName: "Anne", Email: "a@x.fr"},
				{ID: 1, Name: "Alice", ParentID: 1, ParentName: "Anne", Email: "a@x.fr"},
			},
			[]Recipient{{ParentID: 1, ParentName: "Anne", Email: "a@x.fr", Children: []string{"Zoé", "Alice"}}},
		},
		{
			"same child name under two parents",
			[]store.Child{
				{ID: 1, Name: "Léa", ParentID: 1, ParentName: "Anne", Email: "a@x.fr"},
				{ID: 2, Name: "Léa", ParentID: 2, ParentName: "Bob", Email: "b@x.fr"},
			},
			[]Recipient{
				{ParentID: 1, ParentName: "Anne", Email: "a@x.fr", Children: []string{"Léa"}},
				{ParentID: 2, ParentName: "Bob", Email: "b@x.fr", Children: []string{"Léa"}},
			},
		},
		{
			"ordered by parent name key ignoring accents and case",
			[]store.Child{
				{ID: 1, Name: "a", ParentID: 1, ParentName: "Zoé", Email: "z@x.fr"},
				{ID: 2, Name: "b", ParentID: 2, ParentName: "émile", Email: "e@x.fr"},
				{ID: 3, Name: "c", ParentID: 3, ParentName: "Adam", Email: "ad@x.fr"},
			},
			[]Recipient{
				{ParentID: 3, ParentName: "Adam", Email: "ad@x.fr", Children: []string{"c"}},
				{ParentID: 2, ParentName: "émile", Email: "e@x.fr", Children: []string{"b"}},
				{ParentID: 1, ParentName: "Zoé", Email: "z@x.fr", Children: []string{"a"}},
			},
		},
		{
			"same parent name ordered by email",
			[]store.Child{
				{ID: 1, Name: "a", ParentID: 1, ParentName: "Martin", Email: "b@x.fr"},
				{ID: 2, Name: "b", ParentID: 2, ParentName: "MARTIN", Email: "a@x.fr"},
			},
			[]Recipient{
				{ParentID: 2, ParentName: "MARTIN", Email: "a@x.fr", Children: []string{"b"}},
				{ParentID: 1, ParentName: "Martin", Email: "b@x.fr", Children: []string{"a"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Group(tt.in)
			if got == nil {
				t.Fatal("got nil, want non-nil slice")
			}
			if !slices.EqualFunc(got, tt.want, func(a, b Recipient) bool {
				return a.ParentID == b.ParentID && a.ParentName == b.ParentName && a.Email == b.Email && slices.Equal(a.Children, b.Children)
			}) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGroupSkipsEntriesWithoutParent(t *testing.T) {
	in := []store.Child{
		{ID: 1, Name: "NOMAIL", FamilyNumber: "10", InvalidEmail: "bad"},
		{ID: 2, Name: "DUPONT", FamilyNumber: "20", ParentID: 1, ParentName: "DUPONT", Email: "a@x.fr"},
		{ID: 3, Name: "EMPTY", FamilyNumber: "30"},
	}
	got := Group(in)
	if len(got) != 1 || got[0].ParentID != 1 || len(got[0].Children) != 1 || got[0].Children[0] != "DUPONT" {
		t.Fatalf("got %+v", got)
	}

	only := Group(in[:1])
	if only == nil || len(only) != 0 {
		t.Fatalf("got %#v, want empty non-nil slice", only)
	}
}

func TestGroupTwoFamiliesSharingOneEmail(t *testing.T) {
	got := Group([]store.Child{
		{ID: 1, Name: "DUPONT", FamilyNumber: "10", ParentID: 7, ParentName: "DUPONT", Email: "shared@x.fr"},
		{ID: 2, Name: "MARTIN", FamilyNumber: "20", ParentID: 7, ParentName: "DUPONT", Email: "shared@x.fr"},
	})
	if len(got) != 1 || !slices.Equal(got[0].Children, []string{"DUPONT", "MARTIN"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestGroupChildIDsAndFamilyNumbersParallelToChildren(t *testing.T) {
	t.Run("children data", func(t *testing.T) {
		got := Group([]store.Child{
			{ID: 5, Name: "Zoé", ParentID: 1, ParentName: "Anne", Email: "a@x.fr"},
			{ID: 3, Name: "Alice", ParentID: 1, ParentName: "Anne", Email: "a@x.fr"},
			{ID: 4, Name: "Léa", ParentID: 2, ParentName: "Bob", Email: "b@x.fr"},
		})
		if len(got) != 2 {
			t.Fatalf("got %+v", got)
		}
		if !slices.Equal(got[0].ChildIDs, []int64{5, 3}) || !slices.Equal(got[0].Children, []string{"Zoé", "Alice"}) {
			t.Errorf("Anne = %+v", got[0])
		}
		if !slices.Equal(got[0].FamilyNumbers, []string{"", ""}) || !slices.Equal(got[1].FamilyNumbers, []string{""}) {
			t.Errorf("family numbers = %q %q", got[0].FamilyNumbers, got[1].FamilyNumbers)
		}
		if !slices.Equal(got[1].ChildIDs, []int64{4}) {
			t.Errorf("Bob = %+v", got[1])
		}
	})

	t.Run("family data keeps input order and skips entries without email", func(t *testing.T) {
		got := Group([]store.Child{
			{ID: 2, Name: "MARTIN", FamilyNumber: "20", ParentID: 7, ParentName: "DUPONT", Email: "s@x.fr"},
			{ID: 9, Name: "NOMAIL", FamilyNumber: "99"},
			{ID: 1, Name: "DUPONT", FamilyNumber: "10", ParentID: 7, ParentName: "DUPONT", Email: "s@x.fr"},
		})
		if len(got) != 1 {
			t.Fatalf("got %+v", got)
		}
		r := got[0]
		if !slices.Equal(r.ChildIDs, []int64{2, 1}) || !slices.Equal(r.FamilyNumbers, []string{"20", "10"}) || !slices.Equal(r.Children, []string{"MARTIN", "DUPONT"}) {
			t.Errorf("recipient = %+v", r)
		}
	})

	t.Run("lengths always match", func(t *testing.T) {
		for _, r := range Group([]store.Child{
			{ID: 1, Name: "a", ParentID: 1, ParentName: "P", Email: "p@x.fr"},
			{ID: 2, Name: "b", FamilyNumber: "1", ParentID: 2, ParentName: "Q", Email: "q@x.fr"},
			{ID: 3, Name: "c", FamilyNumber: "2", ParentID: 2, ParentName: "Q", Email: "q@x.fr"},
		}) {
			if len(r.ChildIDs) != len(r.Children) || len(r.FamilyNumbers) != len(r.Children) {
				t.Errorf("not parallel: %+v", r)
			}
		}
	})
}
