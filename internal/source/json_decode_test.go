package source

import "testing"

func TestJSONPathKeyLeasesFollowValueLifetime(t *testing.T) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	baseline := ledger.used
	var path jsonPathTracker
	if err := path.start(jsonObjectStart, 0); err != nil {
		t.Fatal(err)
	}
	key := jsonFrame{
		Kind: jsonObjectKey, Raw: []byte(`"a\u0062"`), DecodedBytes: 2,
	}
	if err := path.keyFrame(scope, key); err != nil {
		t.Fatal(err)
	}
	if ledger.used != baseline+2 {
		t.Fatalf("pending key ownership = %d, want %d", ledger.used, baseline+2)
	}
	if err := path.scalar(func(value jsonPathValue) error {
		if value.name != "ab" {
			t.Fatalf("decoded key = %q, want ab", value.name)
		}
		if ledger.used != baseline+2 {
			t.Fatalf("scalar key released before visitor: used=%d", ledger.used)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ledger.used != baseline {
		t.Fatalf("scalar key ownership leaked: used=%d baseline=%d", ledger.used, baseline)
	}

	if err := path.keyFrame(scope, key); err != nil {
		t.Fatal(err)
	}
	if err := path.start(jsonObjectStart, 0); err != nil {
		t.Fatal(err)
	}
	if ledger.used != baseline+2 {
		t.Fatalf("container key ownership = %d, want %d", ledger.used, baseline+2)
	}
	if err := path.end(jsonObjectEnd); err != nil {
		t.Fatal(err)
	}
	if ledger.used != baseline {
		t.Fatalf("container key ownership leaked: used=%d baseline=%d", ledger.used, baseline)
	}
	path.close()
}

func TestJSONPathCloseReleasesPartialKeyLeases(t *testing.T) {
	ledger, err := newSourceLedger(0)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newJSONDecodeScope(ledger)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	baseline := ledger.used
	var path jsonPathTracker
	if err := path.start(jsonObjectStart, 0); err != nil {
		t.Fatal(err)
	}
	if err := path.keyFrame(scope, jsonFrame{
		Kind: jsonObjectKey, Raw: []byte(`"unknown"`), DecodedBytes: 7,
	}); err != nil {
		t.Fatal(err)
	}
	path.close()
	if ledger.used != baseline {
		t.Fatalf("partial path ownership leaked: used=%d baseline=%d", ledger.used, baseline)
	}
}
