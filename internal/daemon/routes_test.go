package daemon

import (
	"testing"
)

func TestRouteTableCRUD(t *testing.T) {
	table := NewRouteTable()

	// Add
	table.Add(&Route{Name: "myapp", Port: 3000, Type: RouteSticky})
	table.Add(&Route{Name: "api", Port: 8080, Type: RouteManaged})

	// Get
	r, ok := table.Get("myapp")
	if !ok || r.Port != 3000 {
		t.Fatalf("Get(myapp) = %v, %v; want port 3000", r, ok)
	}
	_, ok = table.Get("nope")
	if ok {
		t.Fatal("Get(nope) should return false")
	}

	// List returns sorted
	list := table.List()
	if len(list) != 2 {
		t.Fatalf("List() len = %d; want 2", len(list))
	}
	if list[0].Name != "api" || list[1].Name != "myapp" {
		t.Errorf("List() not sorted: got %s, %s", list[0].Name, list[1].Name)
	}

	// Remove
	if !table.Remove("myapp") {
		t.Error("Remove(myapp) should return true")
	}
	if table.Remove("myapp") {
		t.Error("Remove(myapp) second call should return false")
	}
	if len(table.List()) != 1 {
		t.Error("expected 1 route after removal")
	}
}

func TestRouteTableOverwrite(t *testing.T) {
	table := NewRouteTable()
	table.Add(&Route{Name: "app", Port: 3000, Type: RouteSticky})
	table.Add(&Route{Name: "app", Port: 4000, Type: RouteManaged})

	r, _ := table.Get("app")
	if r.Port != 4000 {
		t.Errorf("overwrite failed: port = %d; want 4000", r.Port)
	}
	if r.Type != RouteManaged {
		t.Errorf("overwrite failed: type = %s; want managed", r.Type)
	}
	if len(table.List()) != 1 {
		t.Error("overwrite should not create duplicate")
	}
}
