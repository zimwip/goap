package modelgw

import (
	"context"
	"errors"
	"testing"
)

func TestStoreProviderKeyRejectsBadName(t *testing.T) {
	svc, _ := newService(NewMemoryStore())
	if _, err := svc.StoreProviderKey(context.Background(), "../etc", "k"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestStoreProviderKeyRejectsEmptyKey(t *testing.T) {
	svc, _ := newService(NewMemoryStore())
	if _, err := svc.StoreProviderKey(context.Background(), "acme", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestStoreProviderKeyNoVaultConfigured(t *testing.T) {
	svc, _ := newService(NewMemoryStore()) // svc.Vault is nil
	if _, err := svc.StoreProviderKey(context.Background(), "acme", "sk-raw"); err == nil {
		t.Fatal("want an error when no vault write support is configured")
	}
}

func TestStoreProviderKeySuccess(t *testing.T) {
	svc, _ := newService(NewMemoryStore())
	var gotPath, gotField, gotValue string
	svc.Vault = func(_ context.Context, path, field, value string) error {
		gotPath, gotField, gotValue = path, field, value
		return nil
	}
	ref, err := svc.StoreProviderKey(context.Background(), "acme", "sk-raw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "goap/modelgw/acme#api_key"; ref != want {
		t.Fatalf("ref = %q, want %q", ref, want)
	}
	if gotPath != "goap/modelgw/acme" || gotField != "api_key" || gotValue != "sk-raw" {
		t.Fatalf("Vault called with (%q, %q, %q)", gotPath, gotField, gotValue)
	}
}
