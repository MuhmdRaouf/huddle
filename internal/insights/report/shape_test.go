package report

import (
	"reflect"
	"sort"
	"testing"
)

// TestStructureCarriesNoFreeTextFields walks every type a Structure holds and lists its string
// fields. Each one is an identifier or a label, never prose; a new string field fails here until
// someone reviews it and adds it to the list.
func TestStructureCarriesNoFreeTextFields(t *testing.T) {
	allowed := map[string]bool{
		"WindowView.Session":          true, // a session label
		"SessionRow.Label":            true,
		"SessionRow.Repo":             true, // a repo label
		"ModelRow.Model":              true, // a model id from the catalog
		"ModelRow.Provider":           true, // a lane id
		"ToolRow.Name":                true, // a tool name
		"AskStats.LongestWaitSession": true, // a session label
		"DaySummary.Day":              true, // a date
	}
	var found []string
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Slice || typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Type.Kind() == reflect.String {
				found = append(found, typ.Name()+"."+f.Name)
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(Structure{}))
	sort.Strings(found)
	for _, name := range found {
		if !allowed[name] {
			t.Errorf("string field %s is not on the reviewed list", name)
		}
	}
	for name := range allowed {
		i := sort.SearchStrings(found, name)
		if i == len(found) || found[i] != name {
			t.Errorf("reviewed field %s no longer exists", name)
		}
	}
}
