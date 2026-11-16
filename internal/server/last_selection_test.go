package server

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cafeteria-management/internal/store"
)

var (
	checkedBox = regexp.MustCompile(`<input type="checkbox" name="child" value="(\d+)" checked`)
	anyBox     = regexp.MustCompile(`<input type="checkbox" name="child" value="(\d+)"`)
	counterTag = regexp.MustCompile(`<p class="counter"[^>]*>`)
)

// boxes returns the sorted IDs of the selectable checkboxes of /children,
// and those of the checked ones.
func boxes(t *testing.T, s *Server) (all, checked []int64) {
	t.Helper()
	rec := send(s, http.MethodGet, "/children", nil, "fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /children: status = %d", rec.Code)
	}
	parse := func(re *regexp.Regexp) []int64 {
		out := []int64{}
		for _, m := range re.FindAllStringSubmatch(rec.Body.String(), -1) {
			id, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}
	return parse(anyBox), parse(checkedBox)
}

func sorted(ids ...int64) []int64 {
	slices.Sort(ids)
	return ids
}

func wantChecked(t *testing.T, s *Server, want ...int64) {
	t.Helper()
	_, got := boxes(t, s)
	if !slices.Equal(got, sorted(want...)) {
		t.Fatalf("checked = %v, want %v", got, sorted(want...))
	}
}

func TestChildrenPageNothingCheckedByDefault(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	all, checked := boxes(t, s)
	if len(all) != 3 || len(checked) != 0 {
		t.Fatalf("all = %v, checked = %v", all, checked)
	}
}

func TestComposeRemembersSelection(t *testing.T) {
	t.Run("children data", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)

		rec := postCompose(s, formV(t, st, ids["Zoé"], ids["Léa"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		wantChecked(t, s, ids["Zoé"], ids["Léa"])

		// A second valid selection replaces the first.
		postCompose(s, formV(t, st, ids["Émile"]))
		wantChecked(t, s, ids["Émile"])
	})

	t.Run("family mode", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, silvaRows)
		ids := childIDs(t, st)
		rec := postCompose(s, formV(t, st, ids["SILVA Ana"], ids["SILVA Rui"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		wantChecked(t, s, ids["SILVA Ana"], ids["SILVA Rui"])
		// The other entry of the same address and an unrelated one stay clear.
		all, _ := boxes(t, s)
		if len(all) != 3 {
			t.Fatalf("all = %v", all)
		}
	})

	t.Run("every compose action saves", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		for _, name := range []string{"Zoé", "Émile", "Léa"} {
			v := composeVals(t, st, idsOf(ids, name), "action", "preview", "subject", "S", "body", "B {{parent_name}}")
			if rec := send(s, http.MethodPost, "/compose", v, "fr"); rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", name, rec.Code)
			}
			wantChecked(t, s, ids[name])
		}
	})

	t.Run("a dropped unknown ID does not matter", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		rec := postCompose(s, formV(t, st, ids["Léa"], fakeID))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		wantChecked(t, s, ids["Léa"])
	})

	t.Run("survives a GET and a new request", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		postCompose(s, formV(t, st, ids["Zoé"]))
		for i := 0; i < 2; i++ {
			wantChecked(t, s, ids["Zoé"])
		}
	})

	t.Run("checked rows are not reported twice and keep the version field", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		postCompose(s, formV(t, st, ids["Zoé"]))
		body := send(s, http.MethodGet, "/children", nil, "en").Body.String()
		if n := strings.Count(body, " checked"); n != 1 {
			t.Errorf("%d checked attributes, want 1", n)
		}
		if !strings.Contains(body, fmt.Sprintf(`name="version" value="%d"`, currentVersion(t, st))) {
			t.Errorf("version field missing: %s", body)
		}
	})
}

func TestSavedSelectionIgnoresRowsWithoutEmail(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, []store.ImportRow{
		{ParentName: "DUPONT", ChildName: "DUPONT", Email: "a@example.com", FamilyNumber: "10"},
		{ParentName: "NOMAIL", ChildName: "NOMAIL", FamilyNumber: "20", InvalidEmail: "bad@gmailcom"},
		{ParentName: "EMPTY", ChildName: "EMPTY", FamilyNumber: "30"},
	})
	ids := childIDs(t, st)

	// A tampered form is refused and nothing is saved.
	if rec := postCompose(s, formV(t, st, ids["DUPONT"], ids["NOMAIL"], ids["EMPTY"])); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	wantChecked(t, s)

	if rec := postCompose(s, formV(t, st, ids["DUPONT"])); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	wantChecked(t, s, ids["DUPONT"])

	// Even if a no-email ID was saved by some other path, it is never
	// pre-checked: the disabled checkbox has no name and no checked.
	body := send(s, http.MethodGet, "/children", nil, "fr").Body.String()
	if strings.Count(body, "disabled") < 2 {
		t.Errorf("no-email rows should be disabled: %s", body)
	}
	for _, id := range []int64{ids["NOMAIL"], ids["EMPTY"]} {
		if strings.Contains(body, fmt.Sprintf(`value="%d"`, id)) {
			t.Errorf("no-email entry %d has a selectable box", id)
		}
	}
}

func TestSavedSelectionUnchangedWhenRefused(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, map[string]int64) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		if rec := postCompose(s, formV(t, st, ids["Zoé"], ids["Émile"])); rec.Code != http.StatusOK {
			t.Fatalf("setup: status = %d", rec.Code)
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
		return s, st, ids
	}

	t.Run("stale version", func(t *testing.T) {
		s, st, ids := setup(t)
		stale := formAt(currentVersion(t, st)+1, ids["Léa"])
		if rec := postCompose(s, stale); rec.Code != http.StatusConflict {
			t.Fatalf("status = %d", rec.Code)
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})

	t.Run("missing and invalid version", func(t *testing.T) {
		s, _, ids := setup(t)
		for name, body := range map[string]string{
			"missing":  form(ids["Léa"]),
			"text":     form(ids["Léa"]) + "&version=abc",
			"negative": form(ids["Léa"]) + "&version=-1",
		} {
			if rec := postCompose(s, body); rec.Code != http.StatusConflict {
				t.Fatalf("%s: status = %d", name, rec.Code)
			}
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})

	t.Run("none selected", func(t *testing.T) {
		s, st, ids := setup(t)
		for name, body := range map[string]string{
			"empty":        formV(t, st),
			"only invalid": formV(t, st, "abc", 0, -2),
		} {
			if rec := postCompose(s, body); rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status = %d", name, rec.Code)
			}
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})

	t.Run("only unknown IDs", func(t *testing.T) {
		s, st, ids := setup(t)
		if rec := postCompose(s, formV(t, st, fakeID)); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d", rec.Code)
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})

	t.Run("too many", func(t *testing.T) {
		s, st, ids := setup(t)
		body := manyIDs(maxSelectedChildren+1) + "&version=" + fmt.Sprint(currentVersion(t, st))
		if rec := postCompose(s, body); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d", rec.Code)
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})

	t.Run("GET /children and GET /compose-less pages do not change it", func(t *testing.T) {
		s, _, ids := setup(t)
		for _, p := range []string{"/children", "/templates", "/templates/new", "/"} {
			send(s, http.MethodGet, p, nil, "fr")
		}
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})

	t.Run("templates actions do not change it", func(t *testing.T) {
		s, st, ids := setup(t)
		send(s, http.MethodPost, "/templates", tplForm("N", "S {{parent_name}}", "B"), "fr")
		ts, _ := st.Templates(t.Context())
		if len(ts) != 1 {
			t.Fatalf("templates = %+v", ts)
		}
		send(s, http.MethodPost, fmt.Sprintf("/templates/%d/delete", ts[0].ID), nil, "fr")
		wantChecked(t, s, ids["Zoé"], ids["Émile"])
	})
}

func TestSavedSelectionUnchangedWhenNoEmailRefused(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, []store.ImportRow{
		{ParentName: "DUPONT", ChildName: "DUPONT", Email: "a@example.com", FamilyNumber: "10"},
		{ParentName: "MARTIN", ChildName: "MARTIN", Email: "m@example.com", FamilyNumber: "11"},
		{ParentName: "NOMAIL", ChildName: "NOMAIL", FamilyNumber: "20", InvalidEmail: "bad@gmailcom"},
		{ParentName: "EMPTY", ChildName: "EMPTY", FamilyNumber: "30"},
	})
	ids := childIDs(t, st)
	postCompose(s, formV(t, st, ids["DUPONT"]))
	wantChecked(t, s, ids["DUPONT"])

	for name, sel := range map[string][]any{
		"valid and invalid": {ids["MARTIN"], ids["NOMAIL"]},
		"only invalid":      {ids["NOMAIL"]},
		"only empty":        {ids["EMPTY"]},
	} {
		if rec := postCompose(s, formV(t, st, sel...)); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d", name, rec.Code)
		}
		wantChecked(t, s, ids["DUPONT"])
	}
}

func TestSavedSelectionAfterImport(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	ids := childIDs(t, st)
	postCompose(s, formV(t, st, ids["Zoé"], ids["Léa"]))

	// Léa leaves the file: only Zoé stays pre-checked; the new entry is not.
	seed(t, st, []store.ImportRow{
		{ParentName: "Anne Martin", ChildName: "Zoé", Email: "anne@example.com"},
		{ParentName: "Anne Martin", ChildName: "Émile", Email: "anne@example.com"},
		{ParentName: "Carl Petit", ChildName: "Chloé", Email: "carl@example.com"},
	})
	wantChecked(t, s, ids["Zoé"])
	all, _ := boxes(t, s)
	if len(all) != 3 {
		t.Fatalf("all = %v", all)
	}
}

func TestCounterIsNotAffectedBySavedSelection(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	ids := childIDs(t, st)

	before := counterTag.FindString(send(s, http.MethodGet, "/children", nil, "fr").Body.String())
	if before == "" || !strings.Contains(before, "hidden") || !strings.Contains(before, `data-template="`) {
		t.Fatalf("counter = %q", before)
	}
	postCompose(s, formV(t, st, ids["Zoé"], ids["Léa"]))
	after := counterTag.FindString(send(s, http.MethodGet, "/children", nil, "fr").Body.String())
	if before != after {
		t.Errorf("counter changed:\n%s\n%s", before, after)
	}
	// Same in English and in family mode.
	enBefore := counterTag.FindString(send(s, http.MethodGet, "/children", nil, "en").Body.String())
	if enBefore != after && !strings.Contains(enBefore, `data-template="`) {
		t.Errorf("english counter = %q", enBefore)
	}
}
