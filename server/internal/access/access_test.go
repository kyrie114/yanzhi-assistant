package access

import "testing"

func TestGateOrder(t *testing.T) {
	if Gate(nil, ActList, false) != Unauthenticated {
		t.Fatal("anon")
	}
	user := &Principal{Role: "user", Verified: true}
	admin := &Principal{Role: "admin", Verified: true}
	if Gate(user, ActAdmin, false) != Forbidden {
		t.Fatal("user admin")
	}
	if Gate(admin, ActAdmin, false) != Allow {
		t.Fatal("admin models")
	}
	if Gate(admin, ActOpen, false) != NotFound || Gate(admin, ActOpenGraph, false) != NotFound {
		t.Fatal("admin resource")
	}
	if Gate(admin, ActList, false) != Allow {
		t.Fatal("admin list is empty, not 404")
	}
	unverified := &Principal{Role: "user", Verified: false}
	if Gate(unverified, ActUpload, false) != Allow {
		t.Fatal("slice 1 leaves email restrict off")
	}
	if Gate(unverified, ActAsk, true) != EmailUnverified {
		t.Fatal("restrict on")
	}
}
